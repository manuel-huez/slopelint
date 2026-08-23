package lint

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
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
) (similarityMatch, bool) {
	left := blocks[leftIndex]
	right := blocks[rightIndex]
	embedding, leftChunk, rightChunk := maximumDotSimilarity(vectors.Source, left, right)
	description := maximumDescriptionDotSimilarity(vectors.Description, left, right)
	tier := similarityLocalityTier(left, right)
	sourceThreshold := similarityEmbeddingThreshold(tier, left.IsTest || right.IsTest)
	descriptionThreshold := similarityDescriptionThreshold(tier, left.IsTest && right.IsTest)

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

func similarityEmbeddingThreshold(tier int, testPair bool) float64 {
	// Nearby duplicates are more actionable. Test harnesses need stronger evidence because
	// their setup shape repeats intentionally across cases.
	var threshold float64

	switch tier {
	case 0:
		threshold = similaritySameFileThreshold
	case 1:
		threshold = similaritySamePackageThreshold
	default:
		threshold = min(
			similarityDistantThreshold+
				similarityTierThresholdStep*float64(tier-similarityFirstDistantTier),
			similarityMaximumThreshold,
		)
	}

	if testPair {
		threshold = min(
			threshold+similarityTestThresholdOffset,
			similarityTestMaximumThreshold,
		)
	}

	return threshold
}

func similarityDescriptionThreshold(tier int, testPair bool) float64 {
	var threshold float64

	switch tier {
	case 0:
		threshold = similarityDescriptionSameFileThreshold
	case 1:
		threshold = similarityDescriptionSamePackageThreshold
	default:
		threshold = min(
			similarityDescriptionDistantThreshold+
				similarityDescriptionTierStep*float64(tier-similarityFirstDistantTier),
			similarityDescriptionMaximumThreshold,
		)
	}

	if testPair {
		threshold = min(
			threshold+similarityDescriptionTestOffset,
			similarityDescriptionMaximumThreshold,
		)
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

	leftParts := similarityPathParts(left.PackageDir)
	rightParts := similarityPathParts(right.PackageDir)
	common := 0

	for common < len(leftParts) && common < len(rightParts) && leftParts[common] == rightParts[common] {
		common++
	}

	leftDistance := len(leftParts) - common
	rightDistance := len(rightParts) - common

	return 1 + max(leftDistance, rightDistance)
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

func (match similarityMatch) issue() Issue {
	chunkDetail := ""
	if match.Left.VectorCount > 1 || match.Right.VectorCount > 1 {
		chunkDetail = fmt.Sprintf(
			" via chunks %d/%d",
			match.LeftChunk+1,
			match.RightChunk+1,
		)
	}

	groupDetail := ""

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

		groupDetail = fmt.Sprintf(
			"; group %d: %s",
			len(members),
			strings.Join(members, ", "),
		)
	}

	scoreDetail := ""
	behaviorDetail := ""

	if match.DescriptionScore > 0 {
		scoreDetail = fmt.Sprintf(
			", description %.3f/%.3f",
			match.DescriptionScore,
			similarityDescriptionThreshold(
				match.LocalityTier,
				match.Left.IsTest && match.Right.IsTest,
			),
		)
	}

	if len(match.Members) > 0 && match.Members[0].DescriptionDetail != "" {
		details := make([]string, 0, len(match.Members))
		for _, member := range match.Members {
			details = append(details, fmt.Sprintf(
				"%s: %q",
				member.Identity,
				member.DescriptionDetail,
			))
		}

		behaviorDetail = "; behavior [" + strings.Join(details, ", ") + "]"
	}

	message := fmt.Sprintf(
		"%s appears similar to %s at %s:%d (source %.3f/%.3f%s%s, structure %.3f, locality tier %d%s%s; id %s)",
		match.Left.Symbol,
		match.Right.Symbol,
		match.Right.RelativePath,
		match.Right.Position.Line,
		match.EmbeddingScore,
		similarityEmbeddingThreshold(
			match.LocalityTier,
			match.Left.IsTest || match.Right.IsTest,
		),
		chunkDetail,
		scoreDetail,
		match.StructuralScore,
		match.LocalityTier,
		groupDetail,
		behaviorDetail,
		match.ID,
	)

	return Issue{
		Pos:     match.Left.Pos,
		Kind:    similarityIssueKind,
		Message: message,
		fset:    match.Left.FSet,
	}
}

func (match similarityMatch) acceptance() similarityAcceptance {
	return similarityAcceptance{
		ID:        match.ID,
		Left:      match.Left.Identity,
		LeftHash:  match.Left.ContentHash,
		Right:     match.Right.Identity,
		RightHash: match.Right.ContentHash,
	}
}
