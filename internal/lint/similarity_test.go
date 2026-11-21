package lint

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	similaritySameFileCase    = "same file"
	similaritySamePackageCase = "same package"
	similarityPackageA        = "internal/a"
	similarityFileA           = "internal/a/a.go"
)

func TestSimilarityWithoutCacheWritesNoScanResult(t *testing.T) {
	t.Setenv(codexEnv, offText)

	tmp := newTestModule(t)
	cacheDir := t.TempDir()
	writeSimilarityTestSource(t, tmp)

	embedder := &similarityTestEmbedder{vector: func(string) []float32 {
		return []float32{1, 0}
	}}

	pkgs := loadPackagesForTest(t, tmp)

	sourceDigest, err := similaritySourceDigest(pkgs, nil, tmp)
	if err != nil {
		t.Fatal(err)
	}

	issues, err := checkSemanticCode(t.Context(), pkgs, nil, SimilarityOptions{
		cacheDir: cacheDir,
		embedder: embedder,
	}, nil, nil)
	requireSingleSimilarityIssue(t, issues, err)

	cacheRoot, err := similarityVectorCacheRoot(cacheDir)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(
		similarityScanCachePath(cacheRoot, tmp, false, sourceDigest),
	); !os.IsNotExist(err) {
		t.Fatalf("cache-disabled scan result exists: %v", err)
	}
}

func TestSimilarityLocalityTier(t *testing.T) {
	const (
		testPackageDir  = "pkg"
		testPackageFile = "pkg/a.go"
	)

	t.Parallel()

	tests := []struct {
		name  string
		left  similarityBlock
		right similarityBlock
		want  int
	}{
		{
			name:  similaritySameFileCase,
			left:  similarityBlock{RelativePath: testPackageFile, PackageDir: testPackageDir},
			right: similarityBlock{RelativePath: testPackageFile, PackageDir: testPackageDir},
			want:  0,
		},
		{
			name:  similaritySamePackageCase,
			left:  similarityBlock{RelativePath: testPackageFile, PackageDir: testPackageDir},
			right: similarityBlock{RelativePath: "pkg/b.go", PackageDir: testPackageDir},
			want:  1,
		},
		{
			name:  "siblings",
			left:  similarityBlock{RelativePath: similarityFileA, PackageDir: similarityPackageA},
			right: similarityBlock{RelativePath: "internal/b/b.go", PackageDir: "internal/b"},
			want:  2,
		},
		{
			name:  "parent child",
			left:  similarityBlock{RelativePath: "internal/a.go", PackageDir: "internal"},
			right: similarityBlock{RelativePath: similarityFileA, PackageDir: similarityPackageA},
			want:  2,
		},
		{
			name: "extra layer",
			left: similarityBlock{RelativePath: "internal/a.go", PackageDir: "internal"},
			right: similarityBlock{
				RelativePath: "internal/a/deep/a.go",
				PackageDir:   "internal/a/deep",
			},
			want: 3,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := similarityLocalityTier(&test.left, &test.right); got != test.want {
				t.Fatalf("tier = %d, want %d", got, test.want)
			}
		})
	}
}

func TestSimilarityVectorCacheRoundTrip(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	key := strings.Repeat("a", 64)

	want := []float32{0.25, -0.5, 0.75}
	if err := storeSimilarityVector(root, key, want); err != nil {
		t.Fatalf("store vector: %v", err)
	}

	if err := normalizeSimilarityVector(want); err != nil {
		t.Fatalf("normalize expected vector: %v", err)
	}

	got, ok := loadSimilarityVector(root, key)
	if !ok {
		t.Fatal("cached vector missing")
	}

	if len(got) != len(want) {
		t.Fatalf("vector length = %d, want %d", len(got), len(want))
	}

	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("vector[%d] = %f, want %f", i, got[i], want[i])
		}
	}
}

func TestSimilarityDotProductHandlesLargeFiniteValues(t *testing.T) {
	t.Parallel()

	vector := []float32{math.MaxFloat32, math.MaxFloat32}
	if err := normalizeSimilarityVector(vector); err != nil {
		t.Fatalf("normalize large vector: %v", err)
	}

	if got := normalizedDotProduct(vector, vector); math.Abs(got-1) > 1e-6 {
		t.Fatalf("self dot product = %f, want 1", got)
	}
}

func TestNormalizedSimilarityTokensIgnoreIdentifierNames(t *testing.T) {
	t.Parallel()

	left := normalizedSimilarityTokens(
		"func sum(values []int) int { total := 0; for _, value := range values { total += value }; return total }",
	)
	right := normalizedSimilarityTokens(
		"func add(numbers []int) int { result := 0; for _, number := range numbers { result += number }; return result }",
	)

	if got := structuralSimilarity(structuralShingles(left), structuralShingles(right)); got != 1 {
		t.Fatalf("structural similarity = %f, want 1", got)
	}
}

func TestNormalizedSimilarityTokensKeepLiteralIdentity(t *testing.T) {
	t.Parallel()

	left := normalizedSimilarityTokens(`func value() string { return "left" }`)
	right := normalizedSimilarityTokens(`func value() string { return "right" }`)

	if got := structuralSimilarity(structuralShingles(left), structuralShingles(right)); got == 1 {
		t.Fatalf("structural similarity = %f, want literal difference", got)
	}
}

func TestSimilarityEmbeddingInputsChunkWithoutLosingContent(t *testing.T) {
	t.Parallel()

	content := "HEAD" + strings.Repeat("a", 3496) +
		"MIDDLE" + strings.Repeat("b", 3494) + "TAIL"
	chunks := similarityEmbeddingInputs(content)

	if len(chunks) < 3 {
		t.Fatalf("chunks = %d, want at least 3", len(chunks))
	}

	for index, chunk := range chunks {
		if len(chunk) > similarityEmbeddingChunkBytes {
			t.Fatalf(
				"chunk %d bytes = %d, want <= %d",
				index,
				len(chunk),
				similarityEmbeddingChunkBytes,
			)
		}
	}

	for _, marker := range []string{"HEAD", "MIDDLE", "TAIL"} {
		found := false

		for _, chunk := range chunks {
			if strings.Contains(chunk, marker) {
				found = true
				break
			}
		}

		if !found {
			t.Fatalf("marker %q missing from chunks", marker)
		}
	}
}

func TestMaximumDotSimilarityKeepsBestChunk(t *testing.T) {
	t.Parallel()

	blocks := []*similarityBlock{{}, {}}
	matrices := similarityVectorMatrixForTest(t, blocks, [][][]float32{
		{{1, 0}, {0, 1}},
		{{-1, 0}, {0, 1}},
	})

	score, leftChunk, rightChunk := maximumDotSimilarity(matrices.Source, blocks[0], blocks[1])
	if math.Abs(score-1) > 1e-12 || leftChunk != 1 || rightChunk != 1 {
		t.Fatalf(
			"maximum dot similarity = (%f, %d, %d), want (1, 1, 1)",
			score,
			leftChunk,
			rightChunk,
		)
	}
}

func TestSimilarityThresholdUsesLocalityAndTestCode(t *testing.T) {
	t.Parallel()

	if sameFile, samePackage := similarityEmbeddingThreshold(
		0,
		false,
		0,
	), similarityEmbeddingThreshold(
		1,
		false,
		0,
	); sameFile >= samePackage {
		t.Fatalf("same-file threshold %f must be below same-package %f", sameFile, samePackage)
	}

	if production, testCode := similarityEmbeddingThreshold(
		0,
		false,
		0,
	), similarityEmbeddingThreshold(
		0,
		true,
		0,
	); production >= testCode {
		t.Fatalf("production threshold %f must be below test threshold %f", production, testCode)
	}

	// A configured threshold shifts every gate; test offsets and the cap stay.
	tests := []struct {
		name  string
		tier  int
		test  bool
		shift float64
		want  float64
	}{
		{name: similaritySameFileCase, tier: 0, want: 0.970},
		{name: similaritySamePackageCase, tier: 1, want: 0.975},
		{name: "sibling package", tier: 2, want: 0.980},
		{name: "test code", tier: 0, test: true, want: 0.995},
		{name: "sibling test code", tier: 2, test: true, want: 0.999},
		{name: "lower same file", tier: 0, shift: -0.02, want: 0.950},
		{name: "lower sibling package", tier: 2, shift: -0.02, want: 0.960},
		{name: "lower sibling test code", tier: 2, test: true, shift: -0.02, want: 0.985},
		{name: "higher test code cap", tier: 0, test: true, shift: 0.02, want: 0.999},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := similarityEmbeddingThreshold(test.tier, test.test, test.shift)
			if math.Abs(got-test.want) > 1e-12 {
				t.Fatalf("threshold = %f, want %f", got, test.want)
			}
		})
	}
}

func TestSimilarityDescriptionThresholdUsesIndependentCalibration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		tier  int
		test  bool
		shift float64
		want  float64
	}{
		{name: similaritySameFileCase, tier: 0, want: 0.950},
		{name: similaritySamePackageCase, tier: 1, want: 0.960},
		{name: "sibling package", tier: 2, want: 0.958},
		{name: "test code", tier: 0, test: true, want: 0.965},
		{name: "sibling test code", tier: 2, test: true, want: 0.975},
		{name: "lower same file", tier: 0, shift: -0.02, want: 0.930},
		{name: "higher sibling test code", tier: 2, test: true, shift: 0.02, want: 0.995},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := similarityDescriptionThreshold(test.tier, test.test, test.shift)
			if math.Abs(got-test.want) > 1e-12 {
				t.Fatalf("threshold = %f, want %f", got, test.want)
			}
		})
	}
}

func TestSimilaritySkipsPackagesBeyondImmediateRelations(t *testing.T) {
	t.Parallel()

	blocks := []*similarityBlock{
		{
			Identity:     "internal/a/child/a.go::first",
			RelativePath: "internal/a/child/a.go",
			PackageDir:   "internal/a/child",
		},
		{
			Identity:     "internal/b/child/b.go::second",
			RelativePath: "internal/b/child/b.go",
			PackageDir:   "internal/b/child",
		},
	}
	matrices := similarityVectorMatrixForTest(t, blocks, [][][]float32{
		{{1, 0}},
		{{1, 0}},
	})

	if _, ok := similarityMatchForPair(blocks, matrices, 0, 1, 0); ok {
		t.Fatal("distant package branches were compared")
	}
}

func TestSimilarityBlockCacheParsesOnlyChangedFiles(t *testing.T) {
	t.Parallel()

	tmp := newTestModule(t)
	firstPath := filepath.Join(tmp, "first.go")
	secondPath := filepath.Join(tmp, "second.go")

	writeFile(t, firstPath, similarityTestSource)
	writeFile(t, secondPath, strings.NewReplacer(
		"func first", "func third",
		"func second", "func fourth",
	).Replace(similarityTestSource))

	files, blocks, err := collectSimilarityBlocks(
		loadPackagesForTest(t, tmp),
		nil,
		tmp,
		similarityScanCache{},
	)
	if err != nil {
		t.Fatal(err)
	}

	if len(blocks) != 4 {
		t.Fatalf("initial blocks = %d, want 4", len(blocks))
	}

	for _, block := range blocks {
		block.VectorCount = 1
	}

	previous := similarityScanCache{
		Files:  files,
		Blocks: cacheSimilarityBlocks(blocks),
	}

	writeFile(t, firstPath, "// Changed file comment.\n"+similarityTestSource)

	_, blocks, err = collectSimilarityBlocks(loadPackagesForTest(t, tmp), nil, tmp, previous)
	if err != nil {
		t.Fatal(err)
	}

	for _, block := range blocks {
		wantCount := 1
		if block.RelativePath == "first.go" {
			wantCount = 0
		}

		if block.VectorCount != wantCount {
			t.Fatalf(
				"%s vector count = %d, want %d",
				block.Identity,
				block.VectorCount,
				wantCount,
			)
		}
	}
}

func TestIncrementalSimilarityScanLoadsOnlyLocalCandidates(t *testing.T) {
	t.Parallel()

	blocks := []*similarityBlock{
		{
			Identity:     similarityFileA + "::changed",
			RelativePath: similarityFileA,
			PackageDir:   similarityPackageA,
		},
		{
			Identity:     "internal/a/b.go::same",
			RelativePath: "internal/a/b.go",
			PackageDir:   similarityPackageA,
		},
		{
			Identity:     "internal/a/child/c.go::child",
			RelativePath: "internal/a/child/c.go",
			PackageDir:   "internal/a/child",
		},
		{
			Identity:     "internal/b/child/d.go::distant",
			RelativePath: "internal/b/child/d.go",
			PackageDir:   "internal/b/child",
		},
	}
	changed := map[string]struct{}{blocks[0].Identity: {}}

	selected := incrementalSimilarityScanBlocks(blocks, changed)
	if len(selected) != 3 {
		t.Fatalf("incremental candidates = %d, want changed plus 2 local blocks", len(selected))
	}

	for _, block := range selected {
		if block.Identity == blocks[3].Identity {
			t.Fatal("distant block loaded for incremental scan")
		}
	}
}

func TestSimilarityScanCacheRejectsMismatchedFinding(t *testing.T) {
	t.Parallel()

	leftContent := "func first() {}"
	leftDigest := sha256.Sum256([]byte(leftContent))
	left := similarityCachedBlock{
		Identity:     "pkg/a.go::first",
		Symbol:       "first",
		PackageDir:   "pkg",
		RelativePath: "pkg/a.go",
		Content:      leftContent,
		ContentHash:  hex.EncodeToString(leftDigest[:]),
		Line:         1,
		Column:       1,
		VectorCount:  1,
	}
	rightContent := "func second() {}"
	rightDigest := sha256.Sum256([]byte(rightContent))
	right := similarityCachedBlock{
		Identity:     "pkg/b.go::second",
		Symbol:       "second",
		PackageDir:   "pkg",
		RelativePath: "pkg/b.go",
		Content:      rightContent,
		ContentHash:  hex.EncodeToString(rightDigest[:]),
		Line:         1,
		Column:       1,
		VectorCount:  1,
	}
	id := similarityPairID(
		&similarityBlock{Identity: left.Identity, ContentHash: left.ContentHash},
		&similarityBlock{Identity: right.Identity, ContentHash: right.ContentHash},
	)

	cache := similarityScanCache{
		Files: []similarityCachedFile{
			{RelativePath: left.RelativePath, ContentHash: strings.Repeat("c", sha256.Size*2)},
			{RelativePath: right.RelativePath, ContentHash: strings.Repeat("d", sha256.Size*2)},
		},
		Blocks: []similarityCachedBlock{left, right},
		Matches: []similarityCachedMatch{{
			ID:              id,
			Left:            left.Identity,
			Right:           right.Identity,
			EmbeddingScore:  1,
			StructuralScore: 1,
			LocalityTier:    0,
		}},
		Findings: []similarityCachedFinding{{
			Pair: similarityPair{
				ID:        id,
				Left:      left.Identity,
				LeftHash:  left.ContentHash,
				Right:     right.Identity,
				RightHash: right.ContentHash,
			},
			RelativePath: "pkg/a.go",
			Line:         1,
			Column:       1,
			Message:      "duplicate",
		}},
	}
	if !cache.valid() {
		t.Fatal("valid scan cache rejected")
	}

	cache.Findings[0].Pair.RightHash = left.ContentHash
	if cache.valid() {
		t.Fatal("mismatched finding hash accepted")
	}
}

func TestSimilarityStructureCannotBypassEmbeddingThreshold(t *testing.T) {
	t.Parallel()

	structural := map[uint64]struct{}{1: {}}
	blocks := []*similarityBlock{
		{
			Identity:     similarityTestFilename + "::first",
			RelativePath: similarityTestFilename,
			Structural:   structural,
		},
		{
			Identity:     similarityTestFilename + "::second",
			RelativePath: similarityTestFilename,
			Structural:   structural,
		},
	}
	matrices := similarityVectorMatrixForTest(t, blocks, [][][]float32{
		{{1, 0}},
		{{0.8, 0.6}},
	})

	if matches := groupSimilarityMatches(
		blocks,
		scanSimilarityPairs(blocks, matrices, nil, 0),
	); len(
		matches,
	) != 0 {
		t.Fatalf("matches = %#v, want embedding gate to reject pair", matches)
	}
}

func TestSimilarityDescriptionIsIndependentAndSameKindOnly(t *testing.T) {
	t.Parallel()

	makeBlocks := func(testKinds ...bool) []*similarityBlock {
		blocks := make([]*similarityBlock, len(testKinds))
		for index, isTest := range testKinds {
			blocks[index] = &similarityBlock{
				Identity:               string(rune('a' + index)),
				RelativePath:           "same.go",
				IsTest:                 isTest,
				VectorStart:            index,
				VectorCount:            1,
				DescriptionVectorStart: index,
				DescriptionVectorCount: 1,
			}
		}

		return blocks
	}

	sourceScore := 0.20
	sourceOther := float32(math.Sqrt(1 - sourceScore*sourceScore))
	matrices := similarityVectorMatrices{
		Source: similarityVectorMatrix{
			Values:     []float32{1, 0, float32(sourceScore), sourceOther},
			Dimensions: 2,
		},
		Description: similarityVectorMatrix{
			Values:     []float32{1, 0, 1, 0},
			Dimensions: 2,
		},
	}

	blocks := makeBlocks(false, false)

	match, ok := similarityMatchForPair(blocks, matrices, 0, 1, 0)
	if !ok {
		t.Fatal("behavior-equivalent pair was not reported independently")
	}

	if match.EmbeddingScore >= similaritySameFileThreshold ||
		match.DescriptionScore != 1 {
		t.Fatalf("description-backed match = %#v, found=%t", match, ok)
	}

	crossKind := matrices
	crossKind.Source.Values = append([]float32(nil), matrices.Source.Values...)
	crossKind.Source.Values[2] = 0.20

	crossKind.Source.Values[3] = float32(math.Sqrt(1 - 0.20*0.20))
	if _, ok := similarityMatchForPair(
		makeBlocks(false, true),
		crossKind,
		0,
		1,
		0,
	); ok {
		t.Fatal("production and test descriptions matched across shapes")
	}
}

func TestSimilarityCompactsExactCopyGroups(t *testing.T) {
	t.Parallel()

	blocks := make([]*similarityBlock, 4)
	for index := range blocks {
		blocks[index] = &similarityBlock{
			Identity:     string(rune('a' + index)),
			ContentHash:  "same-content",
			RelativePath: similarityTestFilename,
		}
	}

	matrices := similarityVectorMatrixForTest(t, blocks, [][][]float32{
		{{1, 0}},
		{{1, 0}},
		{{1, 0}},
		{{1, 0}},
	})

	matches := groupSimilarityMatches(blocks, scanSimilarityPairs(blocks, matrices, nil, 0))
	if len(matches) != 1 {
		t.Fatalf("exact-copy groups = %d, want 1", len(matches))
	}

	if got := len(matches[0].Members); got != len(blocks) {
		t.Fatalf("exact-copy group members = %d, want %d", got, len(blocks))
	}

	changed := map[string]struct{}{blocks[3].Identity: {}}
	if got := len(scanSimilarityPairs(blocks, matrices, changed, 0)); got != len(blocks)-1 {
		t.Fatalf("incremental pairs = %d, want %d", got, len(blocks)-1)
	}

	if got := len(groupSimilarityMatches(
		blocks,
		scanSimilarityPairs(blocks, matrices, changed, 0),
	)); got != 1 {
		t.Fatalf("incremental exact-copy matches = %d, want 1", got)
	}
}

func similarityVectorMatrixForTest(
	t *testing.T,
	blocks []*similarityBlock,
	vectors [][][]float32,
) similarityVectorMatrices {
	t.Helper()

	if len(blocks) != len(vectors) {
		t.Fatalf("blocks = %d, vectors = %d", len(blocks), len(vectors))
	}

	inputs := make([][]*similarityVectorInput, len(vectors))
	for blockIndex, blockVectors := range vectors {
		inputs[blockIndex] = make([]*similarityVectorInput, len(blockVectors))
		for vectorIndex, vector := range blockVectors {
			vector = append([]float32(nil), vector...)
			if err := normalizeSimilarityVector(vector); err != nil {
				t.Fatalf("normalize block %d vector %d: %v", blockIndex, vectorIndex, err)
			}

			inputs[blockIndex][vectorIndex] = &similarityVectorInput{
				Location: "test",
				Vector:   vector,
			}
		}
	}

	matrix, err := packSimilarityVectors(blocks, inputs, similaritySourceVector)
	if err != nil {
		t.Fatalf("pack vectors: %v", err)
	}

	return similarityVectorMatrices{Source: matrix}
}

func requireSingleSimilarityIssue(t *testing.T, issues []Issue, err error) Issue {
	t.Helper()

	if err != nil {
		t.Fatal(err)
	}

	if len(issues) != 1 || issues[0].Kind != similarityIssueKind {
		t.Fatalf("issues = %#v, want one semantic duplicate", issues)
	}

	return issues[0]
}

func writeSimilarityTestSource(t *testing.T, dir string) {
	t.Helper()

	writeFile(t, filepath.Join(dir, similarityTestFilename), similarityTestSource)
}

const similarityTestFilename = "sample.go"

const similarityTestSource = "package sample\n\n" + similarityTestSourceBody

const similarityTestSourceBody = `func first(values []int) int {
	total := 0
	for index, value := range values {
		if index%2 == 0 {
			total += value * 2
			continue
		}
		if value > 10 {
			total -= value
		} else {
			total += value
		}
	}
	return total
}

func second(numbers []int) int {
	result := 1
	for position, number := range numbers {
		if position%3 == 0 {
			result *= number + 1
			continue
		}
		if number < 0 {
			result -= number
		} else {
			result += number
		}
	}
	return result
}
`
