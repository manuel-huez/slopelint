package lint

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

func scanSimilarityPairs(
	blocks []*similarityBlock,
	changed map[string]struct{},
) []similarityMatch {
	if len(blocks) < similarityMinimumBlocks {
		return nil
	}

	// Each worker owns one left-index result slice. This saturates local CPUs without
	// locks and preserves deterministic flattening after the pair scan.
	byLeft := make([][]similarityMatch, len(blocks))
	jobs := make(chan int)
	workerCount := min(runtime.GOMAXPROCS(0), len(blocks))

	var workers sync.WaitGroup

	workers.Add(workerCount)

	for range workerCount {
		go func() {
			defer workers.Done()

			for leftIndex := range jobs {
				byLeft[leftIndex] = similarityMatchesForLeft(blocks, leftIndex, changed)
			}
		}()
	}

	for leftIndex := range len(blocks) - 1 {
		jobs <- leftIndex
	}

	close(jobs)
	workers.Wait()

	matches := make([]similarityMatch, 0)
	for _, leftMatches := range byLeft {
		matches = append(matches, leftMatches...)
	}

	return matches
}

func similarityMatchesForLeft(
	blocks []*similarityBlock,
	leftIndex int,
	changed map[string]struct{},
) []similarityMatch {
	left := blocks[leftIndex]
	matches := make([]similarityMatch, 0)

	for _, right := range blocks[leftIndex+1:] {
		if changed != nil {
			_, leftChanged := changed[left.Identity]
			_, rightChanged := changed[right.Identity]

			if !leftChanged && !rightChanged {
				continue
			}
		}

		embedding, leftChunk, rightChunk := maximumCosineSimilarity(
			left.Vectors,
			right.Vectors,
		)
		tier := similarityLocalityTier(left, right)
		threshold := similarityEmbeddingThreshold(tier, left.IsTest || right.IsTest)

		if embedding < threshold {
			continue
		}

		matches = append(matches, similarityMatch{
			ID:              similarityPairID(left, right),
			Left:            left,
			Right:           right,
			EmbeddingScore:  embedding,
			StructuralScore: structuralSimilarity(left.Structural, right.Structural),
			LocalityTier:    tier,
			LeftChunk:       leftChunk,
			RightChunk:      rightChunk,
		})
	}

	return matches
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

func maximumCosineSimilarity(left, right [][]float32) (float64, int, int) {
	var maximum float64

	var leftChunk, rightChunk int

	for leftIndex, leftVector := range left {
		for rightIndex, rightVector := range right {
			score := cosineSimilarity(leftVector, rightVector)
			if score > maximum {
				maximum = score
				leftChunk = leftIndex
				rightChunk = rightIndex
			}
		}
	}

	return maximum, leftChunk, rightChunk
}

func cosineSimilarity(left, right []float32) float64 {
	if len(left) == 0 || len(left) != len(right) {
		return 0
	}

	var dot, leftNorm, rightNorm float64

	for i, value := range left {
		leftValue := float64(value)
		rightValue := float64(right[i])
		dot += leftValue * rightValue
		leftNorm += leftValue * leftValue
		rightNorm += rightValue * rightValue
	}

	if leftNorm == 0 || rightNorm == 0 {
		return 0
	}

	return dot / math.Sqrt(leftNorm*rightNorm)
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
	if len(match.Left.Vectors) > 1 || len(match.Right.Vectors) > 1 {
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

	message := fmt.Sprintf(
		"%s appears similar to %s at %s:%d (embedding %.3f%s, structure %.3f, locality tier %d%s, id %s)",
		match.Left.Symbol,
		match.Right.Symbol,
		match.Right.RelativePath,
		match.Right.Position.Line,
		match.EmbeddingScore,
		chunkDetail,
		match.StructuralScore,
		match.LocalityTier,
		groupDetail,
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
