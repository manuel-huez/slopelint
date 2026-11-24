package lint

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/manuel-huez/slopelint/internal/lint/outline"
)

type similarityScanJob struct {
	leftStart    int
	leftEnd      int
	changedIndex int
	otherStart   int
	otherEnd     int
}

func scanSimilarityPairs(
	blocks []*similarityBlock,
	vectors similarityVectorMatrices,
	changed map[string]struct{},
	shift float64,
) []similarityMatch {
	if len(blocks) < similarityMinimumBlocks {
		return nil
	}

	changedFlags := make([]bool, len(blocks))
	jobs := make([]similarityScanJob, 0)

	if changed == nil {
		const fullScanRows = 16
		for start := 0; start < len(blocks)-1; start += fullScanRows {
			jobs = append(jobs, similarityScanJob{
				leftStart:    start,
				leftEnd:      min(start+fullScanRows, len(blocks)-1),
				changedIndex: -1,
			})
		}
	} else {
		changedIndexes := make([]int, 0, len(changed))
		for index, block := range blocks {
			if _, ok := changed[block.Identity]; ok {
				changedIndexes = append(changedIndexes, index)
				changedFlags[index] = true
			}
		}

		// Split each changed row across CPUs. One edited block must still use the
		// full machine instead of becoming one long single-worker scan.
		const incrementalScanColumns = 256
		for _, changedIndex := range changedIndexes {
			for start := 0; start < len(blocks); start += incrementalScanColumns {
				jobs = append(jobs, similarityScanJob{
					changedIndex: changedIndex,
					otherStart:   start,
					otherEnd:     min(start+incrementalScanColumns, len(blocks)),
				})
			}
		}
	}

	results := make([][]similarityMatch, len(jobs))
	jobIndexes := make(chan int)
	workerCount := min(runtime.GOMAXPROCS(0), len(jobs))

	var workers sync.WaitGroup
	for range workerCount {
		workers.Go(func() {
			for index := range jobIndexes {
				results[index] = similarityMatchesForJob(
					blocks,
					vectors,
					jobs[index],
					changedFlags,
					shift,
				)
			}
		})
	}

	for index := range jobs {
		jobIndexes <- index
	}

	close(jobIndexes)
	workers.Wait()

	matches := make([]similarityMatch, 0)
	for _, result := range results {
		matches = append(matches, result...)
	}

	sortSimilarityMatches(matches)

	return matches
}

func similarityMatchesForJob(
	blocks []*similarityBlock,
	vectors similarityVectorMatrices,
	job similarityScanJob,
	changed []bool,
	shift float64,
) []similarityMatch {
	matches := make([]similarityMatch, 0)

	if job.changedIndex < 0 {
		for left := job.leftStart; left < job.leftEnd; left++ {
			for right := left + 1; right < len(blocks); right++ {
				if match, ok := similarityMatchForPair(
					blocks,
					vectors,
					left,
					right,
					shift,
				); ok {
					matches = append(matches, match)
				}
			}
		}

		return matches
	}

	for other := job.otherStart; other < job.otherEnd; other++ {
		// Two changed endpoints are owned by the lower index. Every other pair is
		// owned by its sole changed endpoint, so no all-pairs walk or duplicate work.
		if other == job.changedIndex || changed[other] && other < job.changedIndex {
			continue
		}

		left, right := min(job.changedIndex, other), max(job.changedIndex, other)
		if match, ok := similarityMatchForPair(
			blocks,
			vectors,
			left,
			right,
			shift,
		); ok {
			matches = append(matches, match)
		}
	}

	return matches
}

func similarityMatchForPair(
	blocks []*similarityBlock,
	vectors similarityVectorMatrices,
	leftIndex int,
	rightIndex int,
	shift float64,
) (similarityMatch, bool) {
	left := blocks[leftIndex]
	right := blocks[rightIndex]

	tier := similarityLocalityTier(left, right)
	if tier > similarityMaximumLocalityTier ||
		similarityLanguage(left.RelativePath) != similarityLanguage(right.RelativePath) {
		return similarityMatch{}, false
	}

	embedding, leftChunk, rightChunk := maximumDotSimilarity(vectors.Source, left, right)
	description := maximumDescriptionDotSimilarity(vectors.Description, left, right)
	sourceThreshold := similarityEmbeddingThreshold(tier, left.IsTest || right.IsTest, shift)
	descriptionThreshold := similarityDescriptionThreshold(tier, left.IsTest && right.IsTest, shift)

	// Code shape and described behavior are independent evidence. Either channel
	// can report a pair; one never gates, promotes, or weakens the other.
	if embedding < sourceThreshold && description < descriptionThreshold {
		return similarityMatch{}, false
	}

	return similarityMatch{
		ID:               similarityPairID(left, right),
		Left:             left,
		Right:            right,
		EmbeddingScore:   embedding,
		DescriptionScore: description,
		StructuralScore:  structuralSimilarity(left.Structural, right.Structural),
		LocalityTier:     tier,
		LeftChunk:        leftChunk,
		RightChunk:       rightChunk,
	}, true
}

func groupSimilarityMatches(
	blocks []*similarityBlock,
	matches []similarityMatch,
) []similarityMatch {
	if len(matches) == 0 {
		return nil
	}

	// Similar pairs form refactor units. Report each connected group once and include
	// every member, avoiding quadratic pair noise without hiding copied test helpers.
	blockIndexes := make(map[string]int, len(blocks))
	parents := make([]int, len(blocks))

	for index, block := range blocks {
		blockIndexes[block.Identity] = index
		parents[index] = index
	}

	findRoot := func(index int) int {
		for parents[index] != index {
			parents[index] = parents[parents[index]]
			index = parents[index]
		}

		return index
	}

	for _, match := range matches {
		leftRoot := findRoot(blockIndexes[match.Left.Identity])
		rightRoot := findRoot(blockIndexes[match.Right.Identity])

		if leftRoot != rightRoot {
			parents[rightRoot] = leftRoot
		}
	}

	representatives := make(map[int]similarityMatch)

	for _, match := range matches {
		root := findRoot(blockIndexes[match.Left.Identity])

		if _, exists := representatives[root]; !exists {
			representatives[root] = match
		}
	}

	for _, block := range blocks {
		root := findRoot(blockIndexes[block.Identity])
		representative, exists := representatives[root]

		if !exists {
			continue
		}

		representative.Members = append(representative.Members, block)
		representatives[root] = representative
	}

	groups := make([]similarityMatch, 0, len(representatives))
	for _, representative := range representatives {
		groups = append(groups, representative)
	}

	sortSimilarityMatches(groups)

	return groups
}

func sortSimilarityMatches(matches []similarityMatch) {
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Left.Identity != matches[j].Left.Identity {
			return matches[i].Left.Identity < matches[j].Left.Identity
		}

		return matches[i].Right.Identity < matches[j].Right.Identity
	})
}

func maximumDotSimilarity(
	matrix similarityVectorMatrix,
	left *similarityBlock,
	right *similarityBlock,
) (float64, int, int) {
	var maximum float64

	var leftChunk, rightChunk int

	for leftIndex := range left.VectorCount {
		leftVector := matrix.vector(left.VectorStart + leftIndex)
		for rightIndex := range right.VectorCount {
			rightVector := matrix.vector(right.VectorStart + rightIndex)

			score := normalizedDotProduct(leftVector, rightVector)
			if score > maximum {
				maximum = score
				leftChunk = leftIndex
				rightChunk = rightIndex
			}
		}
	}

	return maximum, leftChunk, rightChunk
}

func maximumDescriptionDotSimilarity(
	matrix similarityVectorMatrix,
	left *similarityBlock,
	right *similarityBlock,
) float64 {
	if left.IsTest != right.IsTest ||
		left.DescriptionVectorCount == 0 || right.DescriptionVectorCount == 0 {
		return 0
	}

	var maximum float64

	for leftIndex := range left.DescriptionVectorCount {
		leftVector := matrix.vector(left.DescriptionVectorStart + leftIndex)
		for rightIndex := range right.DescriptionVectorCount {
			rightVector := matrix.vector(right.DescriptionVectorStart + rightIndex)
			maximum = max(
				maximum,
				normalizedDotProduct(leftVector, rightVector),
			)
		}
	}

	return maximum
}

func (matrix similarityVectorMatrix) vector(index int) []float32 {
	start := index * matrix.Dimensions
	return matrix.Values[start : start+matrix.Dimensions]
}

func normalizedDotProduct(left, right []float32) float64 {
	if len(left) == 0 || len(left) != len(right) {
		return 0
	}

	// Multiple accumulators expose independent work to the CPU and retain float32
	// throughput. Inputs were normalized once before matrix packing.
	var sums [8]float32

	index := 0
	for ; index+8 <= len(left); index += 8 {
		sums[0] += left[index] * right[index]
		sums[1] += left[index+1] * right[index+1]
		sums[2] += left[index+2] * right[index+2]
		sums[3] += left[index+3] * right[index+3]
		sums[4] += left[index+4] * right[index+4]
		sums[5] += left[index+5] * right[index+5]
		sums[6] += left[index+6] * right[index+6]
		sums[7] += left[index+7] * right[index+7]
	}

	dot := sums[0] + sums[1] + sums[2] + sums[3] +
		sums[4] + sums[5] + sums[6] + sums[7]
	for ; index < len(left); index++ {
		dot += left[index] * right[index]
	}

	return float64(min(dot, 1))
}

func structuralSimilarity(left, right map[uint64]struct{}) float64 {
	if len(left) == 0 || len(right) == 0 {
		return 0
	}

	intersection := 0

	for shingle := range left {
		if _, ok := right[shingle]; ok {
			intersection++
		}
	}

	return float64(intersection) / float64(len(left)+len(right)-intersection)
}

// similarityEmbeddingThreshold adds shift, the configured offset from the
// tuned same-file gate, before the test offset and cap.
func similarityEmbeddingThreshold(tier int, testPair bool, shift float64) float64 {
	// Nearby duplicates are more actionable. Test harnesses need stronger evidence because
	// their setup shape repeats intentionally across cases.
	var threshold float64

	switch tier {
	case 0:
		threshold = similaritySameFileThreshold
	case 1:
		threshold = similaritySamePackageThreshold
	default:
		threshold = similarityDistantThreshold
	}

	threshold += shift

	if testPair {
		threshold = min(
			threshold+similarityTestThresholdOffset,
			similarityTestMaximumThreshold,
		)
	}

	return threshold
}

func similarityDescriptionThreshold(tier int, testPair bool, shift float64) float64 {
	var threshold float64

	switch tier {
	case 0:
		threshold = similarityDescriptionSameFileThreshold
	case 1:
		threshold = similarityDescriptionSamePackageThreshold
	default:
		threshold = similarityDescriptionDistantThreshold
		if testPair {
			// Shared setup across packages does not make test scenarios equivalent.
			threshold = similarityDescriptionSamePackageThreshold
		}
	}

	threshold += shift

	if testPair {
		threshold += similarityDescriptionTestOffset
	}

	return threshold
}

func similarityLocalityTier(left, right *similarityBlock) int {
	if left.RelativePath == right.RelativePath {
		return 0
	}

	if left.PackageDir == right.PackageDir {
		return 1
	}

	leftParts := left.PackageParts
	if leftParts == nil {
		leftParts = similarityPathParts(left.PackageDir)
	}

	rightParts := right.PackageParts
	if rightParts == nil {
		rightParts = similarityPathParts(right.PackageDir)
	}

	common := 0

	for common < len(leftParts) && common < len(rightParts) && leftParts[common] == rightParts[common] {
		common++
	}

	leftDistance := len(leftParts) - common
	rightDistance := len(rightParts) - common

	return 1 + max(leftDistance, rightDistance)
}

// similarityLanguage is the language of a block file; TSX files are
// TypeScript.
func similarityLanguage(relativePath string) outline.Language {
	if language := outline.LanguageFor(relativePath); language != "" {
		return language
	}

	return outline.Go
}

// similarityDescribed reports a block file the description channel covers.
// Its prompts describe Go, so TypeScript and Python blocks match by source
// only.
func similarityDescribed(relativePath string) bool {
	return similarityLanguage(relativePath) == outline.Go
}

func similarityPathParts(path string) []string {
	if path == "" {
		return nil
	}

	return strings.Split(filepath.ToSlash(path), "/")
}

func similarityPairID(left, right *similarityBlock) string {
	states := []string{
		left.Identity + "\x00" + left.ContentHash,
		right.Identity + "\x00" + right.ContentHash,
	}
	sort.Strings(states)

	sum := sha256.Sum256([]byte(strings.Join(states, "\x00")))

	return "sim-" + hex.EncodeToString(sum[:8])
}

// issue names both sides and, for groups, every member. Description
// details say what each side does, so an agent can tell one job from two.
func (match similarityMatch) issue() Issue {
	message := fmt.Sprintf(
		"%s is similar to %s at %s:%d",
		match.Left.Symbol,
		match.Right.Symbol,
		match.Right.RelativePath,
		match.Right.Position.Line,
	)

	if len(match.Members) > similarityMinimumBlocks {
		members := make([]string, len(match.Members))
		for index, member := range match.Members {
			members[index] = fmt.Sprintf(
				"%s:%d %s",
				member.RelativePath,
				member.Position.Line,
				member.Symbol,
			)
		}

		message += fmt.Sprintf("; group of %d: %s", len(members), strings.Join(members, ", "))
	}

	if len(match.Members) > 0 && match.Members[0].DescriptionDetail != "" {
		details := make([]string, 0, len(match.Members))
		for _, member := range match.Members {
			details = append(details, fmt.Sprintf(
				"%s: %s",
				member.Symbol,
				strings.ReplaceAll(member.DescriptionDetail, "\n", "; "),
			))
		}

		message += "; what each does: " + strings.Join(details, " | ")
	}

	return Issue{
		Kind:     similarityIssueKind,
		Message:  message,
		position: match.Left.Position,
	}
}

func (match similarityMatch) pair() similarityPair {
	return similarityPair{
		ID:        match.ID,
		Left:      match.Left.Identity,
		LeftHash:  match.Left.ContentHash,
		Right:     match.Right.Identity,
		RightHash: match.Right.ContentHash,
	}
}

type similarityBlockSourceFile struct {
	pkg          *LoadedPackage
	fset         *token.FileSet
	file         *ast.File
	absolutePath string
	relativePath string
	contentHash  string
}

func collectSimilarityBlocks(
	pkgs []*LoadedPackage,
	scripts []string,
	root string,
	previous similarityScanCache,
) ([]similarityCachedFile, []*similarityBlock, error) {
	files, err := similaritySourceFiles(pkgs, scripts, root)
	if err != nil {
		return nil, nil, err
	}

	reuse := newSimilarityBlockReuse(previous, root, files)
	cachedFiles := make([]similarityCachedFile, 0, len(files))
	blocks := make([]*similarityBlock, 0, len(previous.Blocks))

	for _, file := range files {
		cachedFiles = append(cachedFiles, similarityCachedFile{
			RelativePath: file.relativePath,
			ContentHash:  file.contentHash,
		})

		if reused, ok := reuse.blocks(file); ok {
			blocks = append(blocks, reused...)

			continue
		}

		fileBlocks, err := collectSimilaritySourceFileBlocks(file, root)
		if err != nil {
			return nil, nil, err
		}

		blocks = append(blocks, fileBlocks...)
	}

	sort.Slice(blocks, func(i, j int) bool { return blocks[i].Identity < blocks[j].Identity })

	return cachedFiles, blocks, nil
}

// similarityBlockReuse hands out the previous scan's blocks for unchanged
// files, also when a file moved within its package. Each previous file is
// reused once, and never while a current file still has its path and content.
type similarityBlockReuse struct {
	previous  similarityScanCache
	root      string
	byPath    map[string]string
	byContent map[string][]string
	current   map[string]string
	used      map[string]struct{}
}

func newSimilarityBlockReuse(
	previous similarityScanCache,
	root string,
	files []similarityBlockSourceFile,
) similarityBlockReuse {
	reuse := similarityBlockReuse{
		previous:  previous,
		root:      root,
		byPath:    make(map[string]string, len(previous.Files)),
		byContent: make(map[string][]string, len(previous.Files)),
		current:   make(map[string]string, len(files)),
		used:      make(map[string]struct{}, len(previous.Files)),
	}

	for _, file := range previous.Files {
		reuse.byPath[file.RelativePath] = file.ContentHash
		key := similarityCachedFileReuseKey(file.RelativePath, file.ContentHash)
		reuse.byContent[key] = append(reuse.byContent[key], file.RelativePath)
	}

	for _, file := range files {
		reuse.current[file.relativePath] = file.contentHash
	}

	return reuse
}

func (reuse similarityBlockReuse) blocks(
	file similarityBlockSourceFile,
) ([]*similarityBlock, bool) {
	if reuse.byPath[file.relativePath] == file.contentHash {
		reuse.used[file.relativePath] = struct{}{}

		return reuse.previous.blocksForFile(reuse.root, file.relativePath, file.relativePath), true
	}

	for _, previousPath := range reuse.byContent[similarityCachedFileReuseKey(file.relativePath, file.contentHash)] {
		if _, used := reuse.used[previousPath]; used ||
			previousPath != file.relativePath && reuse.current[previousPath] == file.contentHash {
			continue
		}

		reuse.used[previousPath] = struct{}{}

		return reuse.previous.blocksForFile(reuse.root, previousPath, file.relativePath), true
	}

	return nil, false
}

func similarityCachedFileReuseKey(relativePath, contentHash string) string {
	packageDir := filepath.ToSlash(filepath.Dir(relativePath))

	return packageDir + "\x00" + strconv.FormatBool(
		outline.TestFile(relativePath),
	) + "\x00" + contentHash
}

func similaritySourceFiles(
	pkgs []*LoadedPackage,
	scripts []string,
	root string,
) ([]similarityBlockSourceFile, error) {
	filesByPath := make(map[string]similarityBlockSourceFile)

	scriptFiles, err := similarityFileSources(nil, scripts, root)
	if err != nil {
		return nil, err
	}

	for _, file := range scriptFiles {
		filesByPath[file.relativePath] = file
	}

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

	sources, err := similarityFileSources(pkg, pkg.repoFiles, root)
	for index := range sources {
		sources[index].file = syntax[sources[index].absolutePath]
	}

	return sources, err
}

// similarityFileSources hashes files of pkg, or script files for a nil pkg.
func similarityFileSources(
	pkg *LoadedPackage,
	filenames []string,
	root string,
) ([]similarityBlockSourceFile, error) {
	files := make([]similarityBlockSourceFile, 0, len(filenames))
	for _, filename := range filenames {
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
		file := similarityBlockSourceFile{
			pkg:          pkg,
			absolutePath: absolutePath,
			relativePath: relativePath,
			contentHash:  hex.EncodeToString(digest[:]),
		}

		if pkg != nil {
			file.fset = pkg.FSet
		}

		files = append(files, file)
	}

	return files, nil
}

func collectSimilaritySourceFileBlocks(
	source similarityBlockSourceFile,
	root string,
) ([]*similarityBlock, error) {
	if source.pkg == nil {
		return collectSimilarityScriptBlocks(source)
	}

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

// collectSimilarityScriptBlocks returns the blocks of a TypeScript or
// Python file. Go's scanner tokenizes their comment-free content closely
// enough for the token minimum and the reported structure score.
func collectSimilarityScriptBlocks(source similarityBlockSourceFile) ([]*similarityBlock, error) {
	content, err := os.ReadFile(source.absolutePath)
	if err != nil {
		return nil, err
	}

	file, err := parseReadableScript(source.absolutePath, content)
	if err != nil {
		return nil, err
	}

	var blocks []*similarityBlock

	for _, block := range file.Blocks {
		if each := newSimilarityBlock(
			source.relativePath,
			block.Symbol,
			block.Content,
			scriptPosition(source.absolutePath, block.Pos),
			file.Test,
		); each != nil {
			blocks = append(blocks, each)
		}
	}

	return blocks, nil
}
