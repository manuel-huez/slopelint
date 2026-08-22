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
	"math"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	similaritySchema           = 6
	similarityMinimumBlocks    = 2
	similarityFirstDistantTier = 2
	similarityIssueKind        = "semantic_duplicate"
	similarityAcceptAllID      = "all"

	// Small functions produce common boilerplate matches already covered by structural rules.
	similarityMinimumTokens = 50

	// Byte-bounded overlapping chunks keep every part of large functions inside the
	// native model context. The cap also fits byte-fallback tokens plus model markers
	// inside the 2048-token micro-batch, without truncation or tokenizer assumptions.
	similarityEmbeddingChunkBytes   = 2000
	similarityEmbeddingChunkOverlap = 256
	similarityEmbeddingLineSearch   = 512
	similarityChunkCachePrefix      = "chunk-v1\x00"
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
)

// SimilarityOptions controls repo-wide semantic duplicate analysis.
type SimilarityOptions struct {
	CI              bool
	CacheEnabled    bool
	CacheDir        string
	AcceptedPairIDs []string
	embedder        similarityEmbedder
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
}

type similarityMatch struct {
	ID              string
	Left            *similarityBlock
	Right           *similarityBlock
	EmbeddingScore  float64
	StructuralScore float64
	LocalityTier    int
	LeftChunk       int
	RightChunk      int
	Members         []*similarityBlock
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

	if stampExists && stamp.covers(sourceDigest) {
		return nil, nil
	}

	return analyzeChangedSimilarCode(pkgs, root, sourceDigest, stamp, stampExists, opts)
}

func analyzeChangedSimilarCode(
	pkgs []*LoadedPackage,
	root string,
	sourceDigest string,
	stamp similarityStamp,
	stampExists bool,
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

	matches, cacheRoot, err := similarityMatchesForBlocks(
		blocks,
		stamp,
		stampExists,
		root,
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

	clean := newSimilarityStamp(sourceDigest, len(blocks), acceptances)
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
	opts SimilarityOptions,
) ([]similarityMatch, string, error) {
	cacheRoot, err := similarityVectorCacheRoot(opts.CacheDir)
	if err != nil {
		return nil, "", err
	}

	if len(blocks) < similarityMinimumBlocks {
		return nil, cacheRoot, nil
	}

	vectors, err := populateSimilarityVectors(
		blocks,
		opts.embedder,
		cacheRoot,
		opts.CacheEnabled,
	)
	if err != nil {
		return nil, "", err
	}

	var changed map[string]struct{}
	if opts.CacheEnabled {
		changed = changedSimilarityBlocks(blocks, stamp, stampExists, cacheRoot, root)
	}

	return groupSimilarityMatches(
		blocks,
		scanSimilarityPairs(blocks, vectors, changed),
	), cacheRoot, nil
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

func similarityEmbeddingInputs(content string) []string {
	if len(content) <= similarityEmbeddingChunkBytes {
		return []string{content}
	}

	chunks := make([]string, 0, 1+len(content)/similarityEmbeddingChunkBytes)

	for start := 0; start < len(content); {
		end := min(start+similarityEmbeddingChunkBytes, len(content))
		if end < len(content) {
			// Prefer a nearby line boundary so each fragment remains useful code context.
			searchStart := max(
				start+similarityEmbeddingChunkBytes/2,
				end-similarityEmbeddingLineSearch,
			)
			if newline := strings.LastIndexByte(content[searchStart:end], '\n'); newline >= 0 {
				end = searchStart + newline + 1
			}

			for end > start && !utf8.RuneStart(content[end]) {
				end--
			}
		}

		chunks = append(chunks, content[start:end])
		if end == len(content) {
			break
		}

		start = max(start+1, end-similarityEmbeddingChunkOverlap)
		for start < end && !utf8.RuneStart(content[start]) {
			start++
		}
	}

	return chunks
}

func populateSimilarityVectors(
	blocks []*similarityBlock,
	embedder similarityEmbedder,
	cacheRoot string,
	cacheEnabled bool,
) (matrix similarityVectorMatrix, err error) {
	blockInputs, inputs := similarityVectorInputs(blocks)

	missing := loadCachedSimilarityVectors(inputs, cacheRoot, cacheEnabled)
	if len(missing) == 0 {
		return packSimilarityVectors(blocks, blockInputs)
	}

	if embedder == nil {
		embedder, err = newNativeSimilarityEmbedder(cacheRoot)
		if err != nil {
			return similarityVectorMatrix{}, err
		}

		defer func() {
			err = errors.Join(err, embedder.close())
		}()
	}

	if embedder == nil {
		return similarityVectorMatrix{}, errors.New("embedding engine is unavailable")
	}

	for start := 0; start < len(missing); start += similarityEmbeddingBatchSize {
		end := min(start+similarityEmbeddingBatchSize, len(missing))
		if err := populateSimilarityVectorBatch(
			missing[start:end],
			embedder,
			cacheRoot,
			cacheEnabled,
		); err != nil {
			return similarityVectorMatrix{}, err
		}
	}

	return packSimilarityVectors(blocks, blockInputs)
}

func similarityVectorInputs(
	blocks []*similarityBlock,
) ([][]*similarityVectorInput, []*similarityVectorInput) {
	byKey := make(map[string]*similarityVectorInput, len(blocks))
	blockInputs := make([][]*similarityVectorInput, len(blocks))

	for blockIndex, block := range blocks {
		chunks := similarityEmbeddingInputs(block.Content)
		blockInputs[blockIndex] = make([]*similarityVectorInput, 0, len(chunks))
		chunked := len(chunks) > 1

		for index, content := range chunks {
			cacheContent := content
			if chunked {
				cacheContent = similarityChunkCachePrefix + content
			}

			fingerprint := similarityModelDigest + "\x00" + strconv.Itoa(
				similarityVectorInputSchema,
			) + "\x00" + cacheContent
			sum := sha256.Sum256([]byte(fingerprint))
			key := hex.EncodeToString(sum[:])

			input := byKey[key]
			if input == nil {
				input = &similarityVectorInput{
					Content:  content,
					CacheKey: key,
					Location: fmt.Sprintf("%s chunk %d", block.Identity, index+1),
				}
				byKey[key] = input
			}

			blockInputs[blockIndex] = append(blockInputs[blockIndex], input)
		}
	}

	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	inputs := make([]*similarityVectorInput, 0, len(keys))
	for _, key := range keys {
		inputs = append(inputs, byKey[key])
	}

	return blockInputs, inputs
}

func loadCachedSimilarityVectors(
	inputs []*similarityVectorInput,
	cacheRoot string,
	cacheEnabled bool,
) []*similarityVectorInput {
	missing := make([]*similarityVectorInput, 0)

	for _, input := range inputs {
		if cacheEnabled {
			if vector, ok := loadSimilarityVector(cacheRoot, input.CacheKey); ok {
				input.Vector = vector
				continue
			}
		}

		missing = append(missing, input)
	}

	return missing
}

func populateSimilarityVectorBatch(
	batch []*similarityVectorInput,
	embedder similarityEmbedder,
	cacheRoot string,
	cacheEnabled bool,
) error {
	inputs := make([]string, len(batch))
	for i, input := range batch {
		inputs[i] = input.Content
	}

	vectors, err := embedder.embed(inputs)
	if err != nil {
		return err
	}

	if len(vectors) != len(inputs) {
		return fmt.Errorf(
			"embedding engine returned %d embeddings for %d code blocks",
			len(vectors),
			len(inputs),
		)
	}

	for i, input := range batch {
		vector := vectors[i]
		if err := normalizeSimilarityVector(vector); err != nil {
			return fmt.Errorf("embedding %s: %w", input.Location, err)
		}

		input.Vector = vector

		if cacheEnabled {
			_ = storeSimilarityVector(cacheRoot, input.CacheKey, vector)
		}
	}

	return nil
}

func packSimilarityVectors(
	blocks []*similarityBlock,
	blockInputs [][]*similarityVectorInput,
) (similarityVectorMatrix, error) {
	dimensions := 0
	totalVectors := 0

	for _, inputs := range blockInputs {
		totalVectors += len(inputs)
		for _, input := range inputs {
			if dimensions == 0 {
				dimensions = len(input.Vector)
			}

			if len(input.Vector) != dimensions {
				return similarityVectorMatrix{}, fmt.Errorf(
					"embedding %s has %d dimensions, want %d",
					input.Location,
					len(input.Vector),
					dimensions,
				)
			}
		}
	}

	// One flat matrix gives pair workers sequential float32 reads and avoids one
	// allocation plus pointer chase for every cached chunk.
	matrix := similarityVectorMatrix{
		Values:     make([]float32, totalVectors*dimensions),
		Dimensions: dimensions,
	}
	vectorIndex := 0

	for blockIndex, inputs := range blockInputs {
		blocks[blockIndex].VectorStart = vectorIndex
		blocks[blockIndex].VectorCount = len(inputs)

		for _, input := range inputs {
			copy(
				matrix.Values[vectorIndex*dimensions:(vectorIndex+1)*dimensions],
				input.Vector,
			)
			vectorIndex++
		}
	}

	return matrix, nil
}

func normalizeSimilarityVector(vector []float32) error {
	if len(vector) == 0 {
		return errors.New("empty vector")
	}

	var norm float64

	for _, value := range vector {
		floatValue := float64(value)
		if math.IsNaN(floatValue) || math.IsInf(floatValue, 0) {
			return errors.New("non-finite vector value")
		}

		norm += floatValue * floatValue
	}

	if norm == 0 {
		return errors.New("zero vector")
	}

	// Normalize once at the engine boundary so cache format and threshold behavior
	// do not depend on model-runner output conventions.
	inverseNorm := 1 / math.Sqrt(norm)
	for index, value := range vector {
		vector[index] = float32(float64(value) * inverseNorm)
	}

	return nil
}
