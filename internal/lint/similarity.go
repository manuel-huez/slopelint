package lint

import (
	"bytes"
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
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
)

const (
	similaritySchema           = 9
	similarityMinimumBlocks    = 2
	similarityFirstDistantTier = 2
	similarityIssueKind        = "semantic_duplicate"
	similarityAcceptAllID      = "all"

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
	// parallel sequences while keeping one bounded output allocation.
	similarityEmbeddingBatchSize = 128

	// Related analyzer functions score highly even when behavior differs. Precision-first
	// thresholds keep only near-identical embeddings; locality still lowers nearby gates.
	similaritySameFileThreshold    = 0.970
	similaritySamePackageThreshold = 0.975
	similarityDistantThreshold     = 0.980
	similarityTierThresholdStep    = 0.003
	similarityMaximumThreshold     = 0.995
	similarityTestThresholdOffset  = 0.025
	similarityTestMaximumThreshold = 0.999

	similarityDescriptionSameFileThreshold    = 0.950
	similarityDescriptionSamePackageThreshold = 0.960
	similarityDescriptionDistantThreshold     = 0.960
	similarityDescriptionTierStep             = 0.005
	similarityDescriptionTestOffset           = 0.015
	similarityDescriptionMaximumThreshold     = 0.995
)

// SimilarityOptions controls repo-wide semantic duplicate analysis.
type SimilarityOptions struct {
	CI              bool
	CacheEnabled    bool
	CacheDir        string
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
	RelativePath string
	Content      string
	ContentHash  string
	Position     token.Position
	Pos          token.Pos
	FSet         *token.FileSet
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
	releaseSimilarityTypes(pkgs)

	// Cache hits carry source metadata only. Parse syntax here only when a stale or
	// missing stamp proves that block extraction is required.
	if err := loadPackageSyntax(pkgs); err != nil {
		return nil, err
	}

	blocks, err := collectSimilarityBlocks(pkgs, root)
	if err != nil {
		return nil, err
	}

	// Blocks retain token files and formatted content, not AST nodes. Drop syntax
	// before model allocation so repo analysis and inference memory do not overlap.
	for _, pkg := range pkgs {
		if pkg == nil {
			continue
		}

		pkg.FSet = nil
		pkg.Files = nil
	}

	debug.FreeOSMemory()

	matches, cacheRoot, descriptionDigest, err := similarityMatchesForBlocks(
		blocks,
		stamp,
		stampExists,
		root,
		descriptionRuntime,
		opts,
	)
	if err != nil {
		return nil, err
	}

	acceptances, issues, err := reviewSimilarityMatches(
		matches,
		stamp,
		stampExists,
		blocks,
		opts.AcceptedPairIDs,
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
		descriptionRuntime.enabled,
		descriptionDigest,
	)
	if err := storeSimilarityStamp(root, clean); err != nil {
		return nil, err
	}

	if opts.CacheEnabled {
		// Manifest is an optimization only. Stamp remains sufficient for correctness.
		_ = storeSimilarityManifest(cacheRoot, root, sourceDigest, blocks)
	}

	return nil, nil
}

func releaseSimilarityTypes(pkgs []*LoadedPackage) {
	// Reparse stable repo files after releasing type graphs and compiler-generated
	// CGo syntax, so those large phases do not overlap with vectors and pair state.
	releasedAnalysis := false

	for _, pkg := range pkgs {
		if pkg == nil {
			continue
		}

		releasedAnalysis = releasedAnalysis ||
			pkg.TypesPkg != nil || pkg.TypesInfo != nil || len(pkg.Files) > 0
		pkg.TypesPkg = nil
		pkg.TypesInfo = nil
		pkg.FSet = nil
		pkg.Files = nil
	}

	if releasedAnalysis {
		debug.FreeOSMemory()
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
	stamp similarityStamp,
	stampExists bool,
	root string,
	descriptionRuntime similarityDescriptionRuntime,
	opts SimilarityOptions,
) (matches []similarityMatch, cacheRoot string, descriptionDigest string, err error) {
	cacheRoot, err = similarityVectorCacheRoot(opts.CacheDir)
	if err != nil {
		return nil, "", "", err
	}

	if len(blocks) < similarityMinimumBlocks {
		descriptionDigest, err = similarityDescriptionsDigest(nil)
		return nil, cacheRoot, descriptionDigest, err
	}

	embeddings := newSimilarityEmbeddingRuntime(
		opts.embedder,
		cacheRoot,
		opts.CacheEnabled,
	)
	defer func() { err = errors.Join(err, embeddings.close()) }()

	// Source inference and remote behavior description are independent channels.
	// Overlap them so a cold baseline uses local CPU while Codex waits remotely.
	descriptionDone := make(chan struct{})

	var descriptionErr error
	go func() {
		descriptionDigest, descriptionErr = populateSimilarityDescriptions(
			blocks,
			descriptionRuntime,
			cacheRoot,
			opts.CacheEnabled,
		)

		close(descriptionDone)
	}()

	sourceVectors, sourceErr := embeddings.populate(blocks, similaritySourceVector)

	<-descriptionDone

	if err = errors.Join(sourceErr, descriptionErr); err != nil {
		return nil, "", "", err
	}

	var descriptionVectors similarityVectorMatrix
	if descriptionRuntime.enabled {
		descriptionVectors, err = embeddings.populate(
			blocks,
			similarityDescriptionVector,
		)
		if err != nil {
			return nil, "", "", err
		}
	}

	var changed map[string]struct{}
	if opts.CacheEnabled {
		changed = changedSimilarityBlocks(
			blocks,
			stamp,
			stampExists,
			descriptionRuntime.enabled,
			cacheRoot,
			root,
		)
	}

	matches = groupSimilarityMatches(
		blocks,
		scanSimilarityPairs(
			blocks,
			similarityVectorMatrices{
				Source:      sourceVectors,
				Description: descriptionVectors,
			},
			changed,
		),
	)

	if err := populateSimilarityMatchDetails(
		matches,
		descriptionRuntime,
		cacheRoot,
		opts.CacheEnabled,
	); err != nil {
		return nil, "", "", err
	}

	return matches, cacheRoot, descriptionDigest, nil
}

func reviewSimilarityMatches(
	matches []similarityMatch,
	stamp similarityStamp,
	stampExists bool,
	blocks []*similarityBlock,
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

	current := make(map[string]similarityMatch, len(matches))
	for _, match := range matches {
		current[match.ID] = match
	}

	acceptances := carrySimilarityAcceptances(stamp, stampExists, blocks)
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

	issues := make([]Issue, 0, len(matches))
	for _, match := range matches {
		if _, ok := carried[match.ID]; ok {
			continue
		}

		if _, ok := requested[match.ID]; ok || acceptAll {
			acceptances = append(acceptances, match.acceptance())
			continue
		}

		issues = append(issues, match.issue())
	}

	return acceptances, issues, nil
}

func collectSimilarityBlocks(
	pkgs []*LoadedPackage,
	root string,
) ([]*similarityBlock, error) {
	blocks := make([]*similarityBlock, 0)

	for _, pkg := range pkgs {
		if pkg == nil || pkg.FSet == nil {
			continue
		}

		for _, file := range pkg.Files {
			if file == nil || ast.IsGenerated(file) {
				continue
			}

			fileBlocks, err := collectSimilarityFileBlocks(pkg, file, root)
			if err != nil {
				return nil, err
			}

			blocks = append(blocks, fileBlocks...)
		}
	}

	sort.Slice(blocks, func(i, j int) bool { return blocks[i].Identity < blocks[j].Identity })

	return blocks, nil
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
			RelativePath: relativePath,
			Content:      content,
			ContentHash:  hex.EncodeToString(sum[:]),
			Position:     pkg.FSet.Position(fn.Pos()),
			Pos:          fn.Pos(),
			FSet:         pkg.FSet,
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
