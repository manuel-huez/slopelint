package lint

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/scanner"
	"go/token"
	"hash/fnv"
	"path/filepath"
	"slices"
	"sort"
	"strconv"

	"github.com/manuel-huez/slopelint/internal/lint/outline"
)

const (
	// Matching policy changes rescore findings without changing content-addressed vectors.
	similaritySchema        = 13
	similarityMinimumBlocks = 2
	// Immediate siblings and parent-child packages share refactor ownership.
	// Deeper branches do not; skipping them bounds cold all-pairs work.
	similarityMaximumLocalityTier = 2
	similarityIssueKind           = "semantic_duplicate"
	similarityAcceptAllID         = "all"

	// Small functions produce common boilerplate matches already covered by structural rules.
	similarityMinimumTokens = 50

	// Byte-bounded overlapping chunks keep every part of large functions inside the
	// server model context. The cap also fits byte-fallback tokens plus model markers
	// inside the 2048-token micro-batch, without truncation or tokenizer assumptions.
	similarityEmbeddingChunkBytes     = 2000
	similarityEmbeddingChunkOverlap   = 256
	similarityEmbeddingLineSearch     = 512
	similarityChunkCachePrefix        = "chunk-v1\x00"
	similarityDescriptionVectorPrefix = "description-v2\x00"
	// Large outer batches amortize HTTP requests. llama.cpp splits these into
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

	// Cross-package descriptions recover refactors with different source shape;
	// related helpers within one package still need the stricter existing gates.
	similarityDescriptionSameFileThreshold    = 0.950
	similarityDescriptionSamePackageThreshold = 0.960
	similarityDescriptionDistantThreshold     = 0.958
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
	reviewer        codeReviewer
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
	rawMatches        []similarityMatch
	cacheRoot         string
	descriptionDigest string
}

type similarityVectorKind uint8

const (
	similaritySourceVector similarityVectorKind = iota
	similarityDescriptionVector
)

// checkSemanticCode checks or attests semantic duplicates and code review for
// loaded packages, and code review for the script files. Local runs update
// the stamp only when both are clean; CI runs only validate it. candidates
// delivers dropped-error candidates from structural lint; nil means none.
func checkSemanticCode(
	ctx context.Context,
	pkgs []*LoadedPackage,
	scripts []string,
	opts SimilarityOptions,
	candidates <-chan []Issue,
) ([]Issue, error) {
	if len(pkgs) == 0 {
		return nil, nil
	}

	check, err := loadSimilarityCheck(pkgs, scripts)
	if err != nil {
		return nil, err
	}

	if opts.CI {
		return nil, verifySimilarityStamp(check.stamp, check.sourceDigest)
	}

	runtime, err := codexRuntimeForOptions(opts)
	if err != nil {
		return nil, err
	}

	if check.stamp.Schema != 0 &&
		check.stamp.covers(check.sourceDigest, runtime.enabled, runtime.reviewer != nil) {
		// A current stamp proves both checks were clean for this source. Refresh
		// repository attestations when their file-selection rule changes.
		return nil, refreshSimilarityStamp(check.root, check.stamp)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	review := startCodeReview(
		ctx,
		cancel,
		append(similarityRepoFiles(pkgs), scripts...),
		runtime.reviewer,
		opts,
		candidates,
	)

	issues, clean, err := checkSimilarCode(ctx, pkgs, check, runtime, opts)
	if err != nil {
		cancel()
	}

	reviewIssues, groups, reviewErr := review.wait()
	if err = errors.Join(err, reviewErr); err != nil {
		return nil, err
	}

	for _, group := range uncoveredTestGroups(groups, issues) {
		reviewIssues = append(reviewIssues, group.issue)
	}

	issues = append(issues, reviewIssues...)
	if len(issues) > 0 {
		sortIssues(issues)
		return issues, nil
	}

	if runtime.reviewer != nil {
		clean.Review = &codeReviewCurrentPolicy
	}

	return nil, storeSimilarityStamp(check.root, clean)
}

// checkSimilarCode reports semantic duplicates and the stamp to store when
// none remain.
func checkSimilarCode(
	ctx context.Context,
	pkgs []*LoadedPackage,
	check similarityCheck,
	runtime codexRuntime,
	opts SimilarityOptions,
) ([]Issue, similarityStamp, error) {
	if opts.CacheEnabled {
		cacheRoot, cacheErr := similarityVectorCacheRoot(opts.cacheDir)
		if cacheErr != nil {
			return nil, similarityStamp{}, cacheErr
		}

		cache, ok := loadSimilarityScanCache(
			cacheRoot,
			check.root,
			runtime.enabled,
			check.sourceDigest,
		)
		if ok && cache.covers(check.sourceDigest, runtime.enabled) {
			findings, valid := cache.replayFindings(check.root)
			if valid {
				return acceptSimilarityFindings(
					check,
					cache.DescriptionDigest,
					cache.Descriptions,
					cache.Blocks,
					findings,
					opts.AcceptedPairIDs,
				)
			}
		}
	}

	return analyzeChangedSimilarCode(ctx, pkgs, check, runtime, opts)
}

func similarityRepoFiles(pkgs []*LoadedPackage) []string {
	var files []string

	for _, pkg := range pkgs {
		if pkg != nil {
			files = append(files, pkg.repoFiles...)
		}
	}

	return files
}

func analyzeChangedSimilarCode(
	ctx context.Context,
	pkgs []*LoadedPackage,
	check similarityCheck,
	descriptionRuntime codexRuntime,
	opts SimilarityOptions,
) ([]Issue, similarityStamp, error) {
	cacheRoot, err := similarityVectorCacheRoot(opts.cacheDir)
	if err != nil {
		return nil, similarityStamp{}, err
	}

	var previous similarityScanCache
	if opts.CacheEnabled {
		previous, _ = loadSimilarityScanCache(
			cacheRoot,
			check.root,
			descriptionRuntime.enabled,
			check.sourceDigest,
		)
	}

	files, blocks, err := collectSimilarityBlocks(pkgs, check.scripts, check.root, previous)
	if err != nil {
		return nil, similarityStamp{}, err
	}

	releaseSimilarityTypes(pkgs)

	scan, err := similarityMatchesForBlocks(
		ctx,
		blocks,
		cacheRoot,
		previous,
		descriptionRuntime,
		opts,
	)
	if err != nil {
		return nil, similarityStamp{}, err
	}

	matches := groupSimilarityMatches(blocks, scan.rawMatches)
	if err := populateSimilarityMatchDetails(
		ctx,
		matches,
		descriptionRuntime,
		scan.cacheRoot,
		opts.CacheEnabled,
	); err != nil {
		return nil, similarityStamp{}, err
	}

	cachedBlocks := cacheSimilarityBlocks(blocks)

	findings := similarityFindingsForMatches(matches)
	if opts.CacheEnabled {
		// Scan cache is an optimization only. Source digest and committed stamp
		// remain the correctness boundary.
		if storeSimilarityScanCache(
			scan,
			check,
			descriptionRuntime.enabled,
			files,
			cachedBlocks,
			findings,
		) == nil {
			maybePruneCaches(opts.cacheDir)
		}
	}

	return acceptSimilarityFindings(
		check,
		scan.descriptionDigest,
		descriptionRuntime.enabled,
		cachedBlocks,
		findings,
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

// similarityCheck is the module, current source digest, and committed stamp
// that one similarity run compares. A missing stamp has Schema 0.
type similarityCheck struct {
	root         string
	sourceDigest string
	stamp        similarityStamp
	scripts      []string
}

func loadSimilarityCheck(pkgs []*LoadedPackage, scripts []string) (similarityCheck, error) {
	root, err := similarityModuleRoot(pkgs)
	if err != nil {
		return similarityCheck{}, err
	}

	sourceDigest, err := similaritySourceDigest(pkgs, scripts, root)
	if err != nil {
		return similarityCheck{}, err
	}

	stamp, err := loadSimilarityStamp(root)
	if err != nil {
		return similarityCheck{}, err
	}

	return similarityCheck{
		root:         root,
		sourceDigest: sourceDigest,
		stamp:        stamp,
		scripts:      scripts,
	}, nil
}

func similarityMatchesForBlocks(
	ctx context.Context,
	blocks []*similarityBlock,
	cacheRoot string,
	previous similarityScanCache,
	descriptionRuntime codexRuntime,
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
			descriptionRuntime.enabled,
		)
	}

	scanBlocks, candidates := blocks, blocks
	if previous.Schema != 0 {
		scanBlocks = incrementalSimilarityScanBlocks(blocks, changed)
		candidates = changedSimilarityBlocks(blocks, changed)
	}

	descriptionBlocks := slices.DeleteFunc(
		slices.Clone(candidates),
		func(block *similarityBlock) bool {
			return !similarityDescribed(block.RelativePath)
		},
	)

	if previous.Schema != 0 && descriptionRuntime.enabled {
		if err := reuseRenamedSimilarityDescriptions(
			descriptionBlocks,
			previous,
			cacheRoot,
		); err != nil {
			return similarityScanResult{}, err
		}
	}

	vectors, err := populateSimilarityScanVectors(
		ctx,
		scanBlocks,
		descriptionBlocks,
		descriptionRuntime,
		result.cacheRoot,
		opts,
	)
	if err != nil {
		return similarityScanResult{}, err
	}

	if descriptionRuntime.enabled {
		// The digest covers all blocks, including descriptions restored from the
		// previous scan, not only those described now.
		result.descriptionDigest, err = similarityDescriptionsDigest(blocks)
		if err != nil {
			return similarityScanResult{}, err
		}
	}

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

	return result, nil
}

func restoreUnchangedSimilarityScan(
	result similarityScanResult,
	previous similarityScanCache,
	blocks []*similarityBlock,
	changed map[string]struct{},
	descriptions bool,
) (similarityScanResult, error) {
	result.descriptionDigest = previous.DescriptionDigest
	if descriptions && len(previous.Blocks) != len(blocks) {
		var err error

		result.descriptionDigest, err = similarityDescriptionsDigest(blocks)
		if err != nil {
			return similarityScanResult{}, err
		}
	}

	result.rawMatches = previous.restoreMatches(blocks, changed)

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

func acceptSimilarityFindings(
	check similarityCheck,
	descriptionDigest string,
	descriptions bool,
	blocks []similarityCachedBlock,
	findings []similarityFinding,
	requestedIDs []string,
) ([]Issue, similarityStamp, error) {
	acceptances, issues, err := reviewSimilarityFindings(
		findings,
		check.stamp,
		similarityBlockHashes(blocks),
		requestedIDs,
	)
	if err != nil || len(issues) > 0 {
		return issues, similarityStamp{}, err
	}

	sort.Slice(acceptances, func(i, j int) bool { return acceptances[i].ID < acceptances[j].ID })

	return nil, newSimilarityStamp(
		check.sourceDigest,
		len(blocks),
		acceptances,
		descriptions,
		descriptionDigest,
	), nil
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

	acceptances := carrySimilarityAcceptances(stamp, blockHashes)
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
			IsTest:       outline.TestFile(relativePath),
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
