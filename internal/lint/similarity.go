package lint

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/scanner"
	"go/token"
	"hash/fnv"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	similaritySchema        = 10
	similarityMinimumBlocks = 2
	// Immediate siblings and parent-child packages share refactor ownership.
	// Deeper branches do not; skipping them bounds cold all-pairs work.
	similarityMaximumLocalityTier = 2
	similarityIssueKind           = "semantic_duplicate"
	similarityAcceptAllID         = "all"

	// Small functions produce common boilerplate matches already covered by structural rules.
	similarityMinimumTokens = 50

	// Byte-bounded overlapping chunks keep every part of large functions inside the
	// native model context. The cap also fits byte-fallback tokens plus model markers
	// inside the 2048-token micro-batch, without truncation or tokenizer assumptions.
	similarityEmbeddingChunkBytes     = 2000
	similarityEmbeddingChunkOverlap   = 256
	similarityEmbeddingLineSearch     = 512
	similarityChunkCachePrefix        = "chunk-v1\x00"
	similarityDescriptionVectorPrefix = "description-v2\x00"
	// Large outer batches amortize Go/C++ calls. llama.cpp splits these into four
	// parallel sequences. Byte cap checkpoints long-code batches more often.
	similarityEmbeddingBatchSize  = 128
	similarityEmbeddingBatchBytes = 64000

	// Related analyzer functions score highly even when behavior differs. Precision-first
	// thresholds keep only near-identical embeddings; locality still lowers nearby gates.
	similaritySameFileThreshold    = 0.970
	similaritySamePackageThreshold = 0.975
	similarityDistantThreshold     = 0.980
	similarityTestThresholdOffset  = 0.025
	similarityTestMaximumThreshold = 0.999

	similarityDescriptionSameFileThreshold    = 0.950
	similarityDescriptionSamePackageThreshold = 0.960
	similarityDescriptionDistantThreshold     = 0.960
	similarityDescriptionTestOffset           = 0.015
)

// SimilarityOptions controls repo-wide semantic duplicate analysis.
type SimilarityOptions struct {
	CI              bool
	CacheEnabled    bool
	cacheDir        string
	AcceptedPairIDs []string
	embedder        similarityEmbedder
	describer       similarityDescriber

	// Tests can isolate native-vector behavior without discovering host Codex auth.
	descriptionDisabled bool
}

type similarityBlock struct {
	Identity     string
	Symbol       string
	PackageDir   string
	PackageParts []string
	RelativePath string
	Content      string
	ContentHash  string
	Position     token.Position
	IsTest       bool
	Structural   map[uint64]struct{}
	VectorStart  int
	VectorCount  int

	Description            string
	DescriptionDetail      string
	DescriptionHash        string
	DescriptionVectorStart int
	DescriptionVectorCount int
}

type similarityMatch struct {
	ID               string
	Left             *similarityBlock
	Right            *similarityBlock
	EmbeddingScore   float64
	DescriptionScore float64
	StructuralScore  float64
	LocalityTier     int
	LeftChunk        int
	RightChunk       int
	Members          []*similarityBlock
}

type similarityVectorInput struct {
	Content  string
	CacheKey string
	Location string
	Vector   []float32
}

type similarityVectorMatrix struct {
	Values     []float32
	Dimensions int
}

type similarityVectorMatrices struct {
	Source      similarityVectorMatrix
	Description similarityVectorMatrix
}

type similarityScanResult struct {
	matches           []similarityMatch
	rawMatches        []similarityMatch
	cacheRoot         string
	descriptionDigest string
}

type similarityVectorKind uint8

const (
	similaritySourceVector similarityVectorKind = iota
	similarityDescriptionVector
)

// CheckSimilarCode checks or attests semantic duplicate analysis for loaded packages.
// Local runs use the native embedding engine and update the stamp; CI runs only validate it.
func CheckSimilarCode(pkgs []*LoadedPackage, opts SimilarityOptions) ([]Issue, error) {
	if len(pkgs) == 0 {
		return nil, nil
	}

	root, sourceDigest, stamp, stampExists, err := loadSimilarityCheck(pkgs)
	if err != nil {
		return nil, err
	}

	if opts.CI {
		return nil, verifySimilarityStamp(stamp, stampExists, sourceDigest)
	}

	descriptionRuntime, err := similarityDescriptionRuntimeForOptions(opts)
	if err != nil {
		return nil, err
	}

	if stampExists && stamp.covers(sourceDigest, descriptionRuntime.enabled) {
		return nil, nil
	}

	if opts.CacheEnabled {
		cacheRoot, cacheErr := similarityVectorCacheRoot(opts.cacheDir)
		if cacheErr != nil {
			return nil, cacheErr
		}

		cache, ok := loadSimilarityScanCache(
			cacheRoot,
			root,
			descriptionRuntime.enabled,
			sourceDigest,
		)
		if ok && cache.covers(sourceDigest, descriptionRuntime.enabled) {
			findings, valid := cache.replayFindings(root)
			if valid {
				return completeSimilarityReview(
					root,
					sourceDigest,
					cache.DescriptionDigest,
					cache.Descriptions,
					cache.Blocks,
					findings,
					stamp,
					stampExists,
					opts.AcceptedPairIDs,
				)
			}
		}
	}

	return analyzeChangedSimilarCode(
		pkgs,
		root,
		sourceDigest,
		stamp,
		stampExists,
		descriptionRuntime,
		opts,
	)
}

func analyzeChangedSimilarCode(
	pkgs []*LoadedPackage,
	root string,
	sourceDigest string,
	stamp similarityStamp,
	stampExists bool,
	descriptionRuntime similarityDescriptionRuntime,
	opts SimilarityOptions,
) ([]Issue, error) {
	cacheRoot, err := similarityVectorCacheRoot(opts.cacheDir)
	if err != nil {
		return nil, err
	}

	var previous similarityScanCache
	if opts.CacheEnabled {
		previous, _ = loadSimilarityScanCache(
			cacheRoot,
			root,
			descriptionRuntime.enabled,
			sourceDigest,
		)
	}

	files, blocks, err := collectSimilarityBlocks(pkgs, root, previous)
	if err != nil {
		return nil, err
	}

	releaseSimilarityTypes(pkgs)

	scan, err := similarityMatchesForBlocks(
		blocks,
		cacheRoot,
		previous,
		descriptionRuntime,
		opts,
	)
	if err != nil {
		return nil, err
	}

	cachedBlocks := cacheSimilarityBlocks(blocks)

	findings := similarityFindingsForMatches(scan.matches)
	if opts.CacheEnabled {
		// Scan cache is an optimization only. Source digest and committed stamp
		// remain the correctness boundary.
		if storeSimilarityScanCache(
			scan.cacheRoot,
			root,
			sourceDigest,
			scan.descriptionDigest,
			descriptionRuntime.enabled,
			files,
			cachedBlocks,
			scan.rawMatches,
			findings,
		) == nil {
			maybePruneCaches(opts.cacheDir)
		}
	}

	return completeSimilarityReview(
		root,
		sourceDigest,
		scan.descriptionDigest,
		descriptionRuntime.enabled,
		cachedBlocks,
		findings,
		stamp,
		stampExists,
		opts.AcceptedPairIDs,
	)
}

func releaseSimilarityTypes(pkgs []*LoadedPackage) {
	// Blocks retain formatted content and positions only. Release type graphs and
	// syntax before vectors and pair state allocate their larger working sets.
	for _, pkg := range pkgs {
		if pkg == nil {
			continue
		}

		pkg.TypesPkg = nil
		pkg.TypesInfo = nil
		pkg.FSet = nil
		pkg.Files = nil
	}
}

func loadSimilarityCheck(
	pkgs []*LoadedPackage,
) (string, string, similarityStamp, bool, error) {
	root, err := similarityModuleRoot(pkgs)
	if err != nil {
		return "", "", similarityStamp{}, false, err
	}

	sourceDigest, err := similaritySourceDigest(pkgs, root)
	if err != nil {
		return "", "", similarityStamp{}, false, err
	}

	stamp, err := loadSimilarityStamp(root)
	if err != nil {
		return "", "", similarityStamp{}, false, err
	}

	return root, sourceDigest, stamp, stamp.Schema != 0, nil
}

func similarityMatchesForBlocks(
	blocks []*similarityBlock,
	cacheRoot string,
	previous similarityScanCache,
	descriptionRuntime similarityDescriptionRuntime,
	opts SimilarityOptions,
) (result similarityScanResult, err error) {
	result.cacheRoot = cacheRoot

	if len(blocks) < similarityMinimumBlocks {
		result.descriptionDigest, err = similarityDescriptionsDigest(nil)
		return result, err
	}

	previous, changed, noChangedBlocks := previousSimilarityScan(
		blocks,
		previous,
		descriptionRuntime.enabled,
		opts.CacheEnabled,
	)

	if noChangedBlocks {
		return restoreUnchangedSimilarityScan(
			result,
			previous,
			blocks,
			changed,
			descriptionRuntime,
			opts.CacheEnabled,
		)
	}

	scanBlocks := blocks

	descriptionBlocks := blocks
	if previous.Schema != 0 {
		scanBlocks = incrementalSimilarityScanBlocks(blocks, changed)

		descriptionBlocks = changedSimilarityBlocks(blocks, changed)
		if descriptionRuntime.enabled {
			if err := reuseRenamedSimilarityDescriptions(
				descriptionBlocks,
				previous,
				cacheRoot,
			); err != nil {
				return similarityScanResult{}, err
			}
		}
	}

	vectors, descriptionDigest, err := populateSimilarityScanVectors(
		scanBlocks,
		descriptionBlocks,
		blocks,
		descriptionRuntime,
		result.cacheRoot,
		opts,
	)
	if err != nil {
		return similarityScanResult{}, err
	}

	result.descriptionDigest = descriptionDigest

	result.rawMatches = scanSimilarityPairs(
		scanBlocks,
		vectors,
		changed,
	)
	if previous.Schema != 0 {
		result.rawMatches = append(
			previous.restoreMatches(blocks, changed),
			result.rawMatches...,
		)
		sortSimilarityMatches(result.rawMatches)
	}

	if err := populateSimilarityScanDetails(
		&result,
		blocks,
		descriptionRuntime,
		opts.CacheEnabled,
	); err != nil {
		return similarityScanResult{}, err
	}

	return result, nil
}

func restoreUnchangedSimilarityScan(
	result similarityScanResult,
	previous similarityScanCache,
	blocks []*similarityBlock,
	changed map[string]struct{},
	descriptionRuntime similarityDescriptionRuntime,
	cacheEnabled bool,
) (similarityScanResult, error) {
	result.descriptionDigest = previous.DescriptionDigest
	if descriptionRuntime.enabled && len(previous.Blocks) != len(blocks) {
		var err error

		result.descriptionDigest, err = similarityDescriptionsDigest(blocks)
		if err != nil {
			return similarityScanResult{}, err
		}
	}

	result.rawMatches = previous.restoreMatches(blocks, changed)

	if err := populateSimilarityScanDetails(
		&result,
		blocks,
		descriptionRuntime,
		cacheEnabled,
	); err != nil {
		return similarityScanResult{}, err
	}

	return result, nil
}

func previousSimilarityScan(
	blocks []*similarityBlock,
	previous similarityScanCache,
	descriptions bool,
	cacheEnabled bool,
) (similarityScanCache, map[string]struct{}, bool) {
	if !cacheEnabled || previous.Schema == 0 {
		return similarityScanCache{}, nil, false
	}

	if !previous.policyMatches(descriptions) {
		return similarityScanCache{}, nil, false
	}

	changed := previous.changedBlocks(blocks)
	previous.restoreBlockMetadata(blocks)

	return previous, changed, len(changed) == 0
}

func changedSimilarityBlocks(
	blocks []*similarityBlock,
	changed map[string]struct{},
) []*similarityBlock {
	selected := make([]*similarityBlock, 0, len(changed))

	for _, block := range blocks {
		if _, ok := changed[block.Identity]; ok {
			selected = append(selected, block)
		}
	}

	return selected
}

func incrementalSimilarityScanBlocks(
	blocks []*similarityBlock,
	changed map[string]struct{},
) []*similarityBlock {
	changedBlocks := changedSimilarityBlocks(blocks, changed)
	selected := make([]*similarityBlock, 0, len(blocks))

	for _, block := range blocks {
		include := false

		for _, changedBlock := range changedBlocks {
			if similarityLocalityTier(block, changedBlock) <= similarityMaximumLocalityTier {
				include = true
				break
			}
		}

		if include {
			selected = append(selected, block)
		}
	}

	return selected
}

func populateSimilarityScanDetails(
	result *similarityScanResult,
	blocks []*similarityBlock,
	descriptionRuntime similarityDescriptionRuntime,
	cacheEnabled bool,
) error {
	result.matches = groupSimilarityMatches(blocks, result.rawMatches)

	return populateSimilarityMatchDetails(
		result.matches,
		descriptionRuntime,
		result.cacheRoot,
		cacheEnabled,
	)
}

func completeSimilarityReview(
	root string,
	sourceDigest string,
	descriptionDigest string,
	descriptions bool,
	blocks []similarityCachedBlock,
	findings []similarityFinding,
	stamp similarityStamp,
	stampExists bool,
	requestedIDs []string,
) ([]Issue, error) {
	acceptances, issues, err := reviewSimilarityFindings(
		findings,
		stamp,
		stampExists,
		similarityBlockHashes(blocks),
		requestedIDs,
	)
	if err != nil {
		return nil, err
	}

	if len(issues) > 0 {
		sortIssues(issues)
		return issues, nil
	}

	sort.Slice(acceptances, func(i, j int) bool { return acceptances[i].ID < acceptances[j].ID })

	clean := newSimilarityStamp(
		sourceDigest,
		len(blocks),
		acceptances,
		descriptions,
		descriptionDigest,
	)
	if err := storeSimilarityStamp(root, clean); err != nil {
		return nil, err
	}

	return nil, nil
}

func similarityFindingsForMatches(matches []similarityMatch) []similarityFinding {
	findings := make([]similarityFinding, 0, len(matches))
	for _, match := range matches {
		findings = append(findings, similarityFinding{
			acceptance: match.acceptance(),
			issue:      match.issue(),
		})
	}

	return findings
}

func reviewSimilarityFindings(
	findings []similarityFinding,
	stamp similarityStamp,
	stampExists bool,
	blockHashes map[string]string,
	requestedIDs []string,
) ([]similarityAcceptance, []Issue, error) {
	requested := make(map[string]struct{}, len(requestedIDs))
	acceptAll := false

	for _, id := range requestedIDs {
		if id == similarityAcceptAllID {
			acceptAll = true
			continue
		}

		requested[id] = struct{}{}
	}

	current := make(map[string]similarityFinding, len(findings))
	for _, finding := range findings {
		current[finding.acceptance.ID] = finding
	}

	acceptances := carrySimilarityAcceptances(stamp, stampExists, blockHashes)
	carried := make(map[string]struct{}, len(acceptances))

	for _, accepted := range acceptances {
		carried[accepted.ID] = struct{}{}
	}

	for id := range requested {
		_, currentFinding := current[id]

		_, alreadyAccepted := carried[id]
		if !currentFinding && !alreadyAccepted {
			return nil, nil, fmt.Errorf("semantic similarity pair %q is not a current finding", id)
		}
	}

	issues := make([]Issue, 0, len(findings))
	for _, finding := range findings {
		if _, ok := carried[finding.acceptance.ID]; ok {
			continue
		}

		if _, ok := requested[finding.acceptance.ID]; ok || acceptAll {
			acceptances = append(acceptances, finding.acceptance)
			continue
		}

		issues = append(issues, finding.issue)
	}

	return acceptances, issues, nil
}

func similaritySourceFiles(
	pkgs []*LoadedPackage,
	root string,
) ([]similarityBlockSourceFile, error) {
	filesByPath := make(map[string]similarityBlockSourceFile)

	for _, pkg := range pkgs {
		if pkg == nil {
			continue
		}

		packageFiles, err := similarityPackageSourceFiles(pkg, root)
		if err != nil {
			return nil, err
		}

		for _, file := range packageFiles {
			if _, duplicate := filesByPath[file.relativePath]; !duplicate {
				filesByPath[file.relativePath] = file
			}
		}
	}

	paths := make([]string, 0, len(filesByPath))
	for path := range filesByPath {
		paths = append(paths, path)
	}

	sort.Strings(paths)

	files := make([]similarityBlockSourceFile, 0, len(paths))
	for _, path := range paths {
		files = append(files, filesByPath[path])
	}

	return files, nil
}

func similarityPackageSourceFiles(
	pkg *LoadedPackage,
	root string,
) ([]similarityBlockSourceFile, error) {
	syntax := make(map[string]*ast.File, len(pkg.Files))
	for _, file := range pkg.Files {
		if file != nil && pkg.FSet != nil {
			filename := pkg.FSet.PositionFor(file.Package, true).Filename
			syntax[filepath.Clean(filename)] = file
		}
	}

	files := make([]similarityBlockSourceFile, 0, len(pkg.repoFiles))
	for _, filename := range pkg.repoFiles {
		absolutePath := filepath.Clean(filename)

		relativePath, err := filepath.Rel(root, absolutePath)
		if err != nil {
			return nil, fmt.Errorf("resolve similarity path for %s: %w", filename, err)
		}

		relativePath = filepath.ToSlash(relativePath)
		if !filepath.IsLocal(relativePath) {
			return nil, fmt.Errorf("similarity source %s is outside module root", filename)
		}

		content, err := os.ReadFile(absolutePath)
		if err != nil {
			return nil, err
		}

		digest := sha256.Sum256(content)
		files = append(files, similarityBlockSourceFile{
			pkg:          pkg,
			fset:         pkg.FSet,
			file:         syntax[absolutePath],
			absolutePath: absolutePath,
			relativePath: relativePath,
			contentHash:  hex.EncodeToString(digest[:]),
		})
	}

	return files, nil
}

func collectSimilaritySourceFileBlocks(
	source similarityBlockSourceFile,
	root string,
) ([]*similarityBlock, error) {
	fset := source.fset
	file := source.file

	if file == nil || fset == nil {
		fset = token.NewFileSet()

		var err error

		file, err = parser.ParseFile(fset, source.absolutePath, nil, parser.ParseComments)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", source.absolutePath, err)
		}
	}

	if ast.IsGenerated(file) {
		return nil, nil
	}

	pkg := *source.pkg
	pkg.FSet = fset

	return collectSimilarityFileBlocks(&pkg, file, root)
}

func collectSimilarityFileBlocks(
	pkg *LoadedPackage,
	file *ast.File,
	root string,
) ([]*similarityBlock, error) {
	filename := pkg.FSet.Position(file.Pos()).Filename

	relativePath, err := filepath.Rel(root, filename)
	if err != nil {
		return nil, fmt.Errorf("resolve similarity path for %s: %w", filename, err)
	}

	relativePath = filepath.ToSlash(relativePath)

	packageDir := filepath.ToSlash(filepath.Dir(relativePath))
	if packageDir == "." {
		packageDir = ""
	}

	packageParts := similarityPathParts(packageDir)

	blocks := make([]*similarityBlock, 0)

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}

		content, tokens, err := renderSimilarityFunction(pkg.FSet, fn)
		if err != nil {
			return nil, err
		}

		if len(tokens) < similarityMinimumTokens {
			continue
		}

		symbol := similarityFunctionSymbol(pkg.FSet, fn)
		sum := sha256.Sum256([]byte(content))
		blocks = append(blocks, &similarityBlock{
			Identity:     relativePath + "::" + symbol,
			Symbol:       symbol,
			PackageDir:   packageDir,
			PackageParts: packageParts,
			RelativePath: relativePath,
			Content:      content,
			ContentHash:  hex.EncodeToString(sum[:]),
			Position:     pkg.FSet.Position(fn.Pos()),
			IsTest:       strings.HasSuffix(relativePath, "_test.go"),
			Structural:   structuralShingles(tokens),
		})
	}

	return blocks, nil
}

func renderSimilarityFunction(fset *token.FileSet, fn *ast.FuncDecl) (string, []string, error) {
	copyFn := *fn
	copyFn.Doc = nil

	var rendered bytes.Buffer
	if err := format.Node(&rendered, fset, &copyFn); err != nil {
		return "", nil, fmt.Errorf("format similarity function %s: %w", fn.Name.Name, err)
	}

	content := rendered.String()

	return content, normalizedSimilarityTokens(content), nil
}

func similarityFunctionSymbol(fset *token.FileSet, fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}

	var receiver bytes.Buffer
	if err := format.Node(&receiver, fset, fn.Recv.List[0].Type); err != nil {
		return fn.Name.Name
	}

	return receiver.String() + "." + fn.Name.Name
}

func normalizedSimilarityTokens(source string) []string {
	fset := token.NewFileSet()
	file := fset.AddFile("similarity.go", -1, len(source))

	var scan scanner.Scanner
	scan.Init(file, []byte(source), nil, 0)

	identifiers := make(map[string]string)
	tokens := make([]string, 0)

	for {
		_, tok, lit := scan.Scan()
		if tok == token.EOF {
			break
		}

		if tok == token.SEMICOLON {
			continue
		}

		if tok == token.IDENT {
			normalized, ok := identifiers[lit]
			if !ok {
				normalized = "id" + strconv.Itoa(len(identifiers))
				identifiers[lit] = normalized
			}

			tokens = append(tokens, normalized)

			continue
		}

		if tok == token.INT || tok == token.FLOAT || tok == token.IMAG || tok == token.CHAR {
			tokens = append(tokens, similarityLiteralToken("number", lit))

			continue
		}

		if tok == token.STRING {
			tokens = append(tokens, similarityLiteralToken("string", lit))

			continue
		}

		tokens = append(tokens, tok.String())
	}

	return tokens
}

func similarityLiteralToken(kind, literal string) string {
	sum := sha256.Sum256([]byte(literal))
	return kind + ":" + hex.EncodeToString(sum[:4])
}

func structuralShingles(tokens []string) map[uint64]struct{} {
	const width = 5

	out := make(map[uint64]struct{})
	if len(tokens) < width {
		return out
	}

	for start := 0; start+width <= len(tokens); start++ {
		hash := fnv.New64a()
		for _, value := range tokens[start : start+width] {
			_, _ = hash.Write([]byte(value))
			_, _ = hash.Write([]byte{0})
		}

		out[hash.Sum64()] = struct{}{}
	}

	return out
}
