package lint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"go/token"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const similarityScanCacheSchema = 4

type similarityScanCache struct {
	Schema            int                       `json:"schema"`
	SimilaritySchema  int                       `json:"similarity_schema"`
	SourceDigest      string                    `json:"source_digest"`
	Model             string                    `json:"model"`
	ModelDigest       string                    `json:"model_digest"`
	Descriptions      bool                      `json:"descriptions"`
	DescriptionSchema int                       `json:"description_schema,omitempty"`
	DescriptionModel  string                    `json:"description_model,omitempty"`
	DescriptionEffort string                    `json:"description_effort,omitempty"`
	DescriptionDigest string                    `json:"description_digest,omitempty"`
	Files             []similarityCachedFile    `json:"files"`
	Blocks            []similarityCachedBlock   `json:"blocks"`
	Matches           []similarityCachedMatch   `json:"matches"`
	Findings          []similarityCachedFinding `json:"findings"`
}

type similarityCachedMatch struct {
	ID               string  `json:"id"`
	Left             string  `json:"left"`
	Right            string  `json:"right"`
	EmbeddingScore   float64 `json:"embedding_score"`
	DescriptionScore float64 `json:"description_score,omitempty"`
	StructuralScore  float64 `json:"structural_score"`
	LocalityTier     int     `json:"locality_tier"`
	LeftChunk        int     `json:"left_chunk,omitempty"`
	RightChunk       int     `json:"right_chunk,omitempty"`
}

type similarityCachedBlock struct {
	Identity               string   `json:"identity"`
	Symbol                 string   `json:"symbol"`
	PackageDir             string   `json:"package_dir,omitempty"`
	RelativePath           string   `json:"relative_path"`
	Content                string   `json:"content"`
	ContentHash            string   `json:"content_hash"`
	Offset                 int      `json:"offset"`
	Line                   int      `json:"line"`
	Column                 int      `json:"column"`
	IsTest                 bool     `json:"is_test,omitempty"`
	Structural             []uint64 `json:"structural"`
	VectorCount            int      `json:"vector_count"`
	Description            string   `json:"description,omitempty"`
	DescriptionHash        string   `json:"description_hash,omitempty"`
	DescriptionVectorCount int      `json:"description_vector_count,omitempty"`
}

type similarityCachedFile struct {
	RelativePath string `json:"relative_path"`
	ContentHash  string `json:"content_hash"`
}

type similarityCachedFinding struct {
	Acceptance   similarityAcceptance `json:"acceptance"`
	RelativePath string               `json:"relative_path"`
	Offset       int                  `json:"offset"`
	Line         int                  `json:"line"`
	Column       int                  `json:"column"`
	Message      string               `json:"message"`
}

type similarityFinding struct {
	acceptance similarityAcceptance
	issue      Issue
}

type similarityScanPolicy struct {
	ScanSchema        int
	SimilaritySchema  int
	Model             string
	ModelDigest       string
	Descriptions      bool
	DescriptionSchema int
	DescriptionModel  string
	DescriptionEffort string
}

func (cache similarityScanCache) policyMatches(descriptions bool) bool {
	want := similarityScanPolicy{
		ScanSchema:       similarityScanCacheSchema,
		SimilaritySchema: similaritySchema,
		Model:            similarityModelName,
		ModelDigest:      similarityModelDigest,
		Descriptions:     descriptions,
	}

	if descriptions {
		want.DescriptionSchema = similarityDescriptionPromptSchema
		want.DescriptionModel = similarityDescriptionModel
		want.DescriptionEffort = similarityDescriptionEffort
	}

	got := similarityScanPolicy{
		ScanSchema:        cache.Schema,
		SimilaritySchema:  cache.SimilaritySchema,
		Model:             cache.Model,
		ModelDigest:       cache.ModelDigest,
		Descriptions:      cache.Descriptions,
		DescriptionSchema: cache.DescriptionSchema,
		DescriptionModel:  cache.DescriptionModel,
		DescriptionEffort: cache.DescriptionEffort,
	}

	digestLength := 0
	if descriptions {
		digestLength = len(similarityModelDigest)
	}

	return got == want &&
		len(cache.SourceDigest) == sha256.Size*2 &&
		len(cache.DescriptionDigest) == digestLength
}

func (cache similarityScanCache) covers(sourceDigest string, descriptions bool) bool {
	return cache.policyMatches(descriptions) && cache.SourceDigest == sourceDigest
}

func loadSimilarityScanCache(
	root string,
	moduleRoot string,
	descriptions bool,
) (similarityScanCache, bool) {
	data, err := os.ReadFile(similarityScanCachePath(root, moduleRoot, descriptions))
	if err != nil {
		return similarityScanCache{}, false
	}

	var cache similarityScanCache
	if json.Unmarshal(data, &cache) != nil {
		return similarityScanCache{}, false
	}

	if !cache.valid() {
		return similarityScanCache{}, false
	}

	return cache, true
}

func (cache similarityScanCache) valid() bool {
	files, ok := validSimilarityCachedFiles(cache.Files)
	if !ok {
		return false
	}

	blockHashes, ok := validSimilarityCachedBlocks(
		cache.Blocks,
		files,
		cache.Descriptions,
	)
	if !ok {
		return false
	}

	matches := make(map[string]similarityCachedMatch, len(cache.Matches))
	for _, match := range cache.Matches {
		if !match.valid(blockHashes) {
			return false
		}

		if _, duplicate := matches[match.ID]; duplicate {
			return false
		}

		matches[match.ID] = match
	}

	if len(matches) > 0 && len(cache.Findings) == 0 {
		return false
	}

	seen := make(map[string]struct{}, len(cache.Findings))
	for _, finding := range cache.Findings {
		if !finding.valid(matches, blockHashes, seen) {
			return false
		}

		seen[finding.Acceptance.ID] = struct{}{}
	}

	return true
}

func validSimilarityCachedFiles(files []similarityCachedFile) (map[string]string, bool) {
	hashes := make(map[string]string, len(files))

	for _, file := range files {
		if file.RelativePath == "" || !filepath.IsLocal(file.RelativePath) ||
			len(file.ContentHash) != sha256.Size*2 {
			return nil, false
		}

		if _, duplicate := hashes[file.RelativePath]; duplicate {
			return nil, false
		}

		hashes[file.RelativePath] = file.ContentHash
	}

	return hashes, true
}

func validSimilarityCachedBlocks(
	blocks []similarityCachedBlock,
	files map[string]string,
	descriptions bool,
) (map[string]string, bool) {
	hashes := make(map[string]string, len(blocks))

	for _, block := range blocks {
		if !validSimilarityCachedBlock(block, files, descriptions) {
			return nil, false
		}

		if _, duplicate := hashes[block.Identity]; duplicate {
			return nil, false
		}

		hashes[block.Identity] = block.ContentHash
	}

	return hashes, true
}

func validSimilarityCachedBlock(
	block similarityCachedBlock,
	files map[string]string,
	descriptions bool,
) bool {
	if !validSimilarityCachedBlockSource(block, files) || block.VectorCount <= 0 {
		return false
	}

	if descriptions {
		return block.DescriptionVectorCount > 0 &&
			similarityStringsPresent(block.Description, block.DescriptionHash) &&
			len(block.DescriptionHash) == sha256.Size*2
	}

	return block.DescriptionVectorCount == 0 &&
		block.Description == "" && block.DescriptionHash == ""
}

func validSimilarityCachedBlockSource(
	block similarityCachedBlock,
	files map[string]string,
) bool {
	if !similarityStringsPresent(
		block.Identity,
		block.Symbol,
		block.RelativePath,
		block.Content,
		block.ContentHash,
	) || !filepath.IsLocal(block.RelativePath) || files[block.RelativePath] == "" ||
		len(block.ContentHash) != sha256.Size*2 || block.Offset < 0 ||
		block.Line <= 0 || block.Column <= 0 || len(block.Structural) == 0 {
		return false
	}

	digest := sha256.Sum256([]byte(block.Content))

	return hex.EncodeToString(digest[:]) == block.ContentHash &&
		validSimilarityCachedBlockLocation(block) &&
		strictlyIncreasing(block.Structural)
}

func validSimilarityCachedBlockLocation(block similarityCachedBlock) bool {
	packageDir := filepath.ToSlash(filepath.Dir(block.RelativePath))
	if packageDir == "." {
		packageDir = ""
	}

	return block.Identity == block.RelativePath+"::"+block.Symbol &&
		block.IsTest == strings.HasSuffix(block.RelativePath, "_test.go") &&
		block.PackageDir == packageDir
}

func strictlyIncreasing(values []uint64) bool {
	for index := 1; index < len(values); index++ {
		if values[index-1] >= values[index] {
			return false
		}
	}

	return true
}

func storeSimilarityScanCache(
	root string,
	moduleRoot string,
	sourceDigest string,
	descriptionDigest string,
	descriptions bool,
	files []similarityCachedFile,
	blocks []similarityCachedBlock,
	matches []similarityMatch,
	findings []similarityFinding,
) error {
	cache := similarityScanCache{
		Schema:           similarityScanCacheSchema,
		SimilaritySchema: similaritySchema,
		SourceDigest:     sourceDigest,
		Model:            similarityModelName,
		ModelDigest:      similarityModelDigest,
		Descriptions:     descriptions,
		Files:            files,
		Blocks:           blocks,
		Matches:          make([]similarityCachedMatch, 0, len(matches)),
		Findings:         make([]similarityCachedFinding, 0, len(findings)),
	}
	if descriptions {
		cache.DescriptionSchema = similarityDescriptionPromptSchema
		cache.DescriptionModel = similarityDescriptionModel
		cache.DescriptionEffort = similarityDescriptionEffort
		cache.DescriptionDigest = descriptionDigest
	}

	for _, match := range matches {
		cache.Matches = append(cache.Matches, similarityCachedMatch{
			ID:               match.ID,
			Left:             match.Left.Identity,
			Right:            match.Right.Identity,
			EmbeddingScore:   match.EmbeddingScore,
			DescriptionScore: match.DescriptionScore,
			StructuralScore:  match.StructuralScore,
			LocalityTier:     match.LocalityTier,
			LeftChunk:        match.LeftChunk,
			RightChunk:       match.RightChunk,
		})
	}

	for _, finding := range findings {
		position := issuePosition(finding.issue)

		relativePath, err := filepath.Rel(moduleRoot, position.Filename)
		if err != nil {
			return err
		}

		cache.Findings = append(cache.Findings, similarityCachedFinding{
			Acceptance:   finding.acceptance,
			RelativePath: filepath.ToSlash(relativePath),
			Offset:       position.Offset,
			Line:         position.Line,
			Column:       position.Column,
			Message:      finding.issue.Message,
		})
	}

	data, err := json.Marshal(cache)
	if err != nil {
		return err
	}

	return writeFileAtomically(similarityScanCachePath(root, moduleRoot, descriptions), data)
}

func (cache similarityScanCache) replayFindings(moduleRoot string) ([]similarityFinding, bool) {
	blockHashes := similarityBlockHashes(cache.Blocks)

	matches := make(map[string]similarityCachedMatch, len(cache.Matches))
	for _, match := range cache.Matches {
		matches[match.ID] = match
	}

	findings := make([]similarityFinding, 0, len(cache.Findings))
	seen := make(map[string]struct{}, len(cache.Findings))

	for _, cached := range cache.Findings {
		if !cached.valid(matches, blockHashes, seen) {
			return nil, false
		}

		seen[cached.Acceptance.ID] = struct{}{}

		findings = append(findings, similarityFinding{
			acceptance: cached.Acceptance,
			issue: Issue{
				Kind:    similarityIssueKind,
				Message: cached.Message,
				position: token.Position{
					Filename: filepath.Join(moduleRoot, filepath.FromSlash(cached.RelativePath)),
					Offset:   cached.Offset,
					Line:     cached.Line,
					Column:   cached.Column,
				},
			},
		})
	}

	return findings, true
}

func (cached similarityCachedMatch) valid(blockHashes map[string]string) bool {
	if !similarityStringsPresent(cached.ID, cached.Left, cached.Right) ||
		cached.Left == cached.Right || cached.LocalityTier < 0 ||
		cached.LocalityTier > similarityMaximumLocalityTier ||
		cached.LeftChunk < 0 || cached.RightChunk < 0 ||
		!similarityScoresValid(
			cached.EmbeddingScore,
			cached.DescriptionScore,
			cached.StructuralScore,
		) {
		return false
	}

	leftHash := blockHashes[cached.Left]

	rightHash := blockHashes[cached.Right]
	if !similarityStringsPresent(leftHash, rightHash) {
		return false
	}

	return cached.ID == similarityPairID(
		&similarityBlock{Identity: cached.Left, ContentHash: leftHash},
		&similarityBlock{Identity: cached.Right, ContentHash: rightHash},
	)
}

func (cached similarityCachedFinding) valid(
	matches map[string]similarityCachedMatch,
	blockHashes map[string]string,
	seen map[string]struct{},
) bool {
	if !similarityStringsPresent(
		cached.Acceptance.ID,
		cached.Acceptance.Left,
		cached.Acceptance.LeftHash,
		cached.Acceptance.Right,
		cached.Acceptance.RightHash,
		cached.RelativePath,
		cached.Message,
	) {
		return false
	}

	if !filepath.IsLocal(cached.RelativePath) || cached.Offset < 0 {
		return false
	}

	if cached.Line <= 0 || cached.Column <= 0 {
		return false
	}

	match, ok := matches[cached.Acceptance.ID]
	if !ok || cached.Acceptance.Left != match.Left ||
		cached.Acceptance.Right != match.Right {
		return false
	}

	if blockHashes[match.Left] != cached.Acceptance.LeftHash ||
		blockHashes[match.Right] != cached.Acceptance.RightHash {
		return false
	}

	if _, duplicate := seen[cached.Acceptance.ID]; duplicate {
		return false
	}

	return true
}

func similarityScoresValid(scores ...float64) bool {
	for _, score := range scores {
		if math.IsNaN(score) || math.IsInf(score, 0) || score < 0 || score > 1 {
			return false
		}
	}

	return true
}

func similarityStringsPresent(values ...string) bool {
	return !slices.Contains(values, "")
}

func (cache similarityScanCache) restoreMatches(
	blocks []*similarityBlock,
	changed map[string]struct{},
) []similarityMatch {
	byIdentity := make(map[string]*similarityBlock, len(blocks))
	for _, block := range blocks {
		byIdentity[block.Identity] = block
	}

	matches := make([]similarityMatch, 0, len(cache.Matches))
	for _, cached := range cache.Matches {
		if _, ok := changed[cached.Left]; ok {
			continue
		}

		if _, ok := changed[cached.Right]; ok {
			continue
		}

		left := byIdentity[cached.Left]

		right := byIdentity[cached.Right]
		if left == nil || right == nil {
			continue
		}

		matches = append(matches, similarityMatch{
			ID:               cached.ID,
			Left:             left,
			Right:            right,
			EmbeddingScore:   cached.EmbeddingScore,
			DescriptionScore: cached.DescriptionScore,
			StructuralScore:  cached.StructuralScore,
			LocalityTier:     cached.LocalityTier,
			LeftChunk:        cached.LeftChunk,
			RightChunk:       cached.RightChunk,
		})
	}

	return matches
}

func (cache similarityScanCache) changedBlocks(blocks []*similarityBlock) map[string]struct{} {
	previous := similarityBlockHashes(cache.Blocks)
	changed := make(map[string]struct{})

	for _, block := range blocks {
		if previous[block.Identity] != block.ContentHash {
			changed[block.Identity] = struct{}{}
		}
	}

	return changed
}

func cacheSimilarityBlocks(blocks []*similarityBlock) []similarityCachedBlock {
	cached := make([]similarityCachedBlock, 0, len(blocks))
	for _, block := range blocks {
		structural := make([]uint64, 0, len(block.Structural))
		for shingle := range block.Structural {
			structural = append(structural, shingle)
		}

		slices.Sort(structural)

		cached = append(cached, similarityCachedBlock{
			Identity:               block.Identity,
			Symbol:                 block.Symbol,
			PackageDir:             block.PackageDir,
			RelativePath:           block.RelativePath,
			Content:                block.Content,
			ContentHash:            block.ContentHash,
			Offset:                 block.Position.Offset,
			Line:                   block.Position.Line,
			Column:                 block.Position.Column,
			IsTest:                 block.IsTest,
			Structural:             structural,
			VectorCount:            block.VectorCount,
			Description:            block.Description,
			DescriptionHash:        block.DescriptionHash,
			DescriptionVectorCount: block.DescriptionVectorCount,
		})
	}

	return cached
}

func (cache similarityScanCache) restoreBlockMetadata(blocks []*similarityBlock) {
	cached := make(map[string]similarityCachedBlock, len(cache.Blocks))
	for _, block := range cache.Blocks {
		cached[block.Identity] = block
	}

	for _, block := range blocks {
		previous, ok := cached[block.Identity]
		if !ok || previous.ContentHash != block.ContentHash {
			continue
		}

		block.VectorCount = previous.VectorCount
		block.Description = previous.Description
		block.DescriptionHash = previous.DescriptionHash
		block.DescriptionVectorCount = previous.DescriptionVectorCount
	}
}

func (cache similarityScanCache) blocksForFile(
	root string,
	relativePath string,
) []*similarityBlock {
	blocks := make([]*similarityBlock, 0)

	for _, cached := range cache.Blocks {
		if cached.RelativePath != relativePath {
			continue
		}

		structural := make(map[uint64]struct{}, len(cached.Structural))
		for _, shingle := range cached.Structural {
			structural[shingle] = struct{}{}
		}

		blocks = append(blocks, &similarityBlock{
			Identity:     cached.Identity,
			Symbol:       cached.Symbol,
			PackageDir:   cached.PackageDir,
			PackageParts: similarityPathParts(cached.PackageDir),
			RelativePath: cached.RelativePath,
			Content:      cached.Content,
			ContentHash:  cached.ContentHash,
			Position: token.Position{
				Filename: filepath.Join(root, filepath.FromSlash(cached.RelativePath)),
				Offset:   cached.Offset,
				Line:     cached.Line,
				Column:   cached.Column,
			},
			IsTest:                 cached.IsTest,
			Structural:             structural,
			VectorCount:            cached.VectorCount,
			Description:            cached.Description,
			DescriptionHash:        cached.DescriptionHash,
			DescriptionVectorCount: cached.DescriptionVectorCount,
		})
	}

	return blocks
}

func similarityBlockHashes(blocks []similarityCachedBlock) map[string]string {
	hashes := make(map[string]string, len(blocks))
	for _, block := range blocks {
		hashes[block.Identity] = block.ContentHash
	}

	return hashes
}

func similarityScanCachePath(root, moduleRoot string, descriptions bool) string {
	absolute, err := filepath.Abs(moduleRoot)
	if err != nil {
		absolute = moduleRoot
	}

	descriptionMode := "off"
	if descriptions {
		descriptionMode = "on"
	}

	sum := sha256.Sum256([]byte(absolute + "\x00descriptions=" + descriptionMode))
	key := hex.EncodeToString(sum[:])

	return filepath.Join(root, "repos", key[:2], key[2:]+".json")
}
