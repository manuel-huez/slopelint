package lint

import (
	"cmp"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"go/token"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	similarityScanCacheSchema    = 6
	similarityScanCacheSnapshots = 16
	// Sorted 64-bit hash deltas average close to one eight-byte varint.
	similarityPackedStructuralBytesPerValue = 8
)

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
	Identity               string `json:"identity"`
	Symbol                 string `json:"symbol"`
	PackageDir             string `json:"package_dir,omitempty"`
	RelativePath           string `json:"relative_path"`
	Content                string `json:"content"`
	ContentHash            string `json:"content_hash"`
	Offset                 int    `json:"offset"`
	Line                   int    `json:"line"`
	Column                 int    `json:"column"`
	IsTest                 bool   `json:"is_test,omitempty"`
	Structural             string `json:"structural"`
	VectorCount            int    `json:"vector_count"`
	Description            string `json:"description,omitempty"`
	DescriptionHash        string `json:"description_hash,omitempty"`
	DescriptionVectorCount int    `json:"description_vector_count,omitempty"`
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
	sourceDigest string,
) (similarityScanCache, bool) {
	exactPath := similarityScanCachePath(
		root,
		moduleRoot,
		descriptions,
		sourceDigest,
	)
	if cache, ok := readSimilarityScanCache(root, exactPath); ok {
		return cache, true
	}

	snapshots := similarityScanCacheSnapshotsInDir(filepath.Dir(exactPath))
	for _, snapshot := range slices.Backward(snapshots) {
		if snapshot.path == exactPath {
			continue
		}

		if cache, ok := readSimilarityScanCache(root, snapshot.path); ok {
			return cache, true
		}
	}

	return similarityScanCache{}, false
}

func readSimilarityScanCache(root string, path string) (similarityScanCache, bool) {
	data, err := os.ReadFile(path)
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

	if cache.policyMatches(cache.Descriptions) {
		refreshCacheEntry(path)

		if _, ok := loadSimilarityCacheReferenceManifest(path, cache.SourceDigest); !ok {
			_ = storeSimilarityCacheReferenceManifest(root, path, cache)
		}
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
		block.Line <= 0 || block.Column <= 0 || block.Structural == "" {
		return false
	}

	structural, ok := unpackSimilarityStructural(block.Structural)
	if !ok {
		return false
	}

	digest := sha256.Sum256([]byte(block.Content))

	return hex.EncodeToString(digest[:]) == block.ContentHash &&
		validSimilarityCachedBlockLocation(block) &&
		strictlyIncreasing(structural)
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

	path := similarityScanCachePath(
		root,
		moduleRoot,
		descriptions,
		sourceDigest,
	)
	if err := writeFileAtomically(path, data); err != nil {
		return err
	}

	if err := storeSimilarityCacheReferenceManifest(root, path, cache); err != nil {
		return err
	}

	pruneSimilarityScanCacheSnapshots(filepath.Dir(path))

	return nil
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
			Structural:             packSimilarityStructural(structural),
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
	cachedPath string,
	currentPath string,
) []*similarityBlock {
	blocks := make([]*similarityBlock, 0)
	packageDir := filepath.ToSlash(filepath.Dir(currentPath))

	if packageDir == "." {
		packageDir = ""
	}

	for _, cached := range cache.Blocks {
		if cached.RelativePath != cachedPath {
			continue
		}

		packed, ok := unpackSimilarityStructural(cached.Structural)
		if !ok {
			continue
		}

		structural := make(map[uint64]struct{}, len(packed))
		for _, shingle := range packed {
			structural[shingle] = struct{}{}
		}

		blocks = append(blocks, &similarityBlock{
			Identity:     currentPath + "::" + cached.Symbol,
			Symbol:       cached.Symbol,
			PackageDir:   packageDir,
			PackageParts: similarityPathParts(packageDir),
			RelativePath: currentPath,
			Content:      cached.Content,
			ContentHash:  cached.ContentHash,
			Position: token.Position{
				Filename: filepath.Join(root, filepath.FromSlash(currentPath)),
				Offset:   cached.Offset,
				Line:     cached.Line,
				Column:   cached.Column,
			},
			IsTest:                 strings.HasSuffix(currentPath, "_test.go"),
			Structural:             structural,
			VectorCount:            cached.VectorCount,
			Description:            cached.Description,
			DescriptionHash:        cached.DescriptionHash,
			DescriptionVectorCount: cached.DescriptionVectorCount,
		})
	}

	return blocks
}

func packSimilarityStructural(values []uint64) string {
	if len(values) == 0 {
		return ""
	}

	// Sorted hashes become positive deltas. Varints halve snapshot size versus
	// decimal JSON while preserving exact structural evidence.
	data := make([]byte, 0, len(values)*binary.MaxVarintLen64)
	buffer := make([]byte, binary.MaxVarintLen64)
	previous := uint64(0)

	for index, value := range values {
		delta := value
		if index > 0 {
			delta = value - previous
		}

		count := binary.PutUvarint(buffer, delta)
		data = append(data, buffer[:count]...)
		previous = value
	}

	return base64.RawStdEncoding.EncodeToString(data)
}

func unpackSimilarityStructural(packed string) ([]uint64, bool) {
	data, err := base64.RawStdEncoding.DecodeString(packed)
	if err != nil || len(data) == 0 {
		return nil, false
	}

	values := make(
		[]uint64,
		0,
		max(1, len(data)/similarityPackedStructuralBytesPerValue),
	)
	previous := uint64(0)

	for len(data) > 0 {
		delta, count := binary.Uvarint(data)
		if count <= 0 || len(values) > 0 && delta == 0 {
			return nil, false
		}

		value := delta
		if len(values) > 0 {
			value = previous + delta
			if value <= previous {
				return nil, false
			}
		}

		values = append(values, value)
		previous = value
		data = data[count:]
	}

	return values, true
}

func similarityBlockHashes(blocks []similarityCachedBlock) map[string]string {
	hashes := make(map[string]string, len(blocks))
	for _, block := range blocks {
		hashes[block.Identity] = block.ContentHash
	}

	return hashes
}

func similarityScanCachePath(
	root string,
	moduleRoot string,
	descriptions bool,
	sourceDigest string,
) string {
	location, err := repositoryCacheLocationForDir(moduleRoot)
	if err != nil {
		absolute, absoluteErr := filepath.Abs(moduleRoot)
		if absoluteErr != nil {
			absolute = moduleRoot
		}

		location.identity = absolute
		location.relativeDir = "."
	}

	descriptionMode := "off"
	if descriptions {
		descriptionMode = "on"
	}

	sum := sha256.Sum256([]byte(
		location.identity + "\x00" + location.relativeDir +
			"\x00descriptions=" + descriptionMode,
	))
	key := hex.EncodeToString(sum[:])

	return filepath.Join(root, "repos", key[:2], key[2:], sourceDigest+".json")
}

func pruneSimilarityScanCacheSnapshots(dir string) {
	snapshots := similarityScanCacheSnapshotsInDir(dir)
	for _, snapshot := range snapshots[:max(0, len(snapshots)-similarityScanCacheSnapshots)] {
		_ = os.Remove(snapshot.path)
		_ = os.Remove(similarityCacheReferenceManifestPath(snapshot.path))
	}
}

type similarityScanCacheSnapshot struct {
	path    string
	modTime int64
}

func similarityScanCacheSnapshotsInDir(dir string) []similarityScanCacheSnapshot {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	snapshots := make([]similarityScanCacheSnapshot, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}

		snapshots = append(snapshots, similarityScanCacheSnapshot{
			path:    filepath.Join(dir, entry.Name()),
			modTime: info.ModTime().UnixNano(),
		})
	}

	slices.SortFunc(snapshots, func(left, right similarityScanCacheSnapshot) int {
		if order := cmp.Compare(left.modTime, right.modTime); order != 0 {
			return order
		}

		return strings.Compare(left.path, right.path)
	})

	return snapshots
}
