package lint

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSimilarityLocalWritesStampAndCIUsesItWithoutNativeEngine(t *testing.T) {
	tmp := newTestModule(t)
	cacheDir := t.TempDir()
	writeSimilarityTestSource(t, tmp)

	embedder := &similarityTestEmbedder{vector: func(input string) []float32 {
		if strings.Contains(input, "func first") {
			return []float32{1, 0}
		}

		return []float32{0, 1}
	}}

	pkgs := loadPackagesForTest(t, tmp)

	issues, err := CheckSimilarCode(pkgs, SimilarityOptions{
		CacheEnabled: true,
		CacheDir:     cacheDir,
		embedder:     embedder,
	})
	if err != nil {
		t.Fatalf("local check: %v", err)
	}

	if len(issues) != 0 {
		t.Fatalf("local issues: %v", issues)
	}

	if embedder.calls != 1 {
		t.Fatalf("embedding batches = %d, want 1", embedder.calls)
	}

	stampPath := filepath.Join(tmp, similarityStampName)
	if _, err := os.Stat(stampPath); err != nil {
		t.Fatalf("stamp: %v", err)
	}

	pkgs = loadPackagesForTest(t, tmp)

	issues, err = CheckSimilarCode(pkgs, SimilarityOptions{})
	if err != nil {
		t.Fatalf("stamped local check: %v", err)
	}

	if len(issues) != 0 {
		t.Fatalf("stamped local issues: %v", issues)
	}

	issues, err = CheckSimilarCode(pkgs, SimilarityOptions{CI: true})
	if err != nil {
		t.Fatalf("CI check: %v", err)
	}

	if len(issues) != 0 {
		t.Fatalf("CI issues: %v", issues)
	}

	writeFile(
		t,
		filepath.Join(tmp, similarityTestFilename),
		similarityTestSource+"\n// source changed\n",
	)
	pkgs = loadPackagesForTest(t, tmp)

	_, err = CheckSimilarCode(pkgs, SimilarityOptions{CI: true})
	if err == nil || !strings.Contains(err.Error(), "is stale") {
		t.Fatalf("stale CI check error = %v", err)
	}
}

func TestSimilarityReportsThenRecordsAcceptedPair(t *testing.T) {
	tmp := newTestModule(t)
	cacheDir := t.TempDir()
	writeSimilarityTestSource(t, tmp)

	embedder := &similarityTestEmbedder{vector: func(string) []float32 {
		return []float32{1, 0}
	}}

	pkgs := loadPackagesForTest(t, tmp)

	issues, err := CheckSimilarCode(pkgs, SimilarityOptions{
		CacheEnabled: true,
		CacheDir:     cacheDir,
		embedder:     embedder,
	})
	if err != nil {
		t.Fatalf("first check: %v", err)
	}

	if len(issues) != 1 || issues[0].Kind != similarityIssueKind {
		t.Fatalf("issues = %#v, want one semantic duplicate", issues)
	}

	if _, err := os.Stat(filepath.Join(tmp, similarityStampName)); !os.IsNotExist(err) {
		t.Fatalf("stamp exists before review: %v", err)
	}

	pairID := similarityIssuePairID(t, issues[0].Message)

	issues, err = CheckSimilarCode(pkgs, SimilarityOptions{
		CacheEnabled:    true,
		CacheDir:        cacheDir,
		AcceptedPairIDs: []string{pairID},
	})
	if err != nil {
		t.Fatalf("accepted check: %v", err)
	}

	if len(issues) != 0 {
		t.Fatalf("accepted issues: %v", issues)
	}

	stamp, err := loadSimilarityStamp(tmp)
	if err != nil || stamp.Schema == 0 {
		t.Fatalf("load stamp: schema=%d err=%v", stamp.Schema, err)
	}

	if len(stamp.Accepted) != 1 || stamp.Accepted[0].ID != pairID {
		t.Fatalf("accepted stamp = %#v", stamp.Accepted)
	}
}

func TestSimilarityKeepsRepeatedAcceptedPair(t *testing.T) {
	tmp := newTestModule(t)
	cacheDir := t.TempDir()
	writeSimilarityTestSource(t, tmp)

	embedder := &similarityTestEmbedder{vector: func(string) []float32 {
		return []float32{1, 0}
	}}

	pkgs := loadPackagesForTest(t, tmp)

	_, err := CheckSimilarCode(pkgs, SimilarityOptions{
		CacheEnabled:    true,
		CacheDir:        cacheDir,
		AcceptedPairIDs: []string{similarityAcceptAllID},
		embedder:        embedder,
	})
	if err != nil {
		t.Fatalf("initial acceptance: %v", err)
	}

	stamp, err := loadSimilarityStamp(tmp)
	if err != nil || len(stamp.Accepted) != 1 {
		t.Fatalf("load accepted stamp: accepted=%d err=%v", len(stamp.Accepted), err)
	}

	writeFile(
		t,
		filepath.Join(tmp, similarityTestFilename),
		similarityTestSource+"\n// unrelated change\n",
	)
	pkgs = loadPackagesForTest(t, tmp)

	issues, err := CheckSimilarCode(pkgs, SimilarityOptions{
		CacheEnabled:    true,
		CacheDir:        cacheDir,
		AcceptedPairIDs: []string{stamp.Accepted[0].ID},
	})
	if err != nil {
		t.Fatalf("repeat accepted pair: %v", err)
	}

	if len(issues) != 0 {
		t.Fatalf("repeat accepted issues: %v", issues)
	}
}

func TestSimilarityAcceptAllRecordsCurrentPairs(t *testing.T) {
	tmp := newTestModule(t)
	cacheDir := t.TempDir()
	writeSimilarityTestSource(t, tmp)

	embedder := &similarityTestEmbedder{vector: func(string) []float32 {
		return []float32{1, 0}
	}}

	pkgs := loadPackagesForTest(t, tmp)

	issues, err := CheckSimilarCode(pkgs, SimilarityOptions{
		CacheDir:        cacheDir,
		AcceptedPairIDs: []string{similarityAcceptAllID},
		embedder:        embedder,
	})
	if err != nil {
		t.Fatalf("accept all: %v", err)
	}

	if len(issues) != 0 {
		t.Fatalf("issues: %v", issues)
	}

	stamp, err := loadSimilarityStamp(tmp)
	if err != nil {
		t.Fatal(err)
	}

	if len(stamp.Accepted) != 1 {
		t.Fatalf("accepted pairs = %d, want 1", len(stamp.Accepted))
	}

	cacheRoot, err := similarityVectorCacheRoot(cacheDir)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(similarityManifestPath(cacheRoot, tmp)); !os.IsNotExist(err) {
		t.Fatalf("cache-disabled manifest exists: %v", err)
	}
}

func TestSimilarityRejectsUnknownAcceptance(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, similarityTestFilename), "package sample\n")

	pkgs := loadPackagesForTest(t, tmp)

	_, err := CheckSimilarCode(pkgs, SimilarityOptions{
		AcceptedPairIDs: []string{"sim-unknown"},
	})
	if err == nil || !strings.Contains(err.Error(), "is not a current finding") {
		t.Fatalf("unknown acceptance error = %v", err)
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
			name:  "same file",
			left:  similarityBlock{RelativePath: testPackageFile, PackageDir: testPackageDir},
			right: similarityBlock{RelativePath: testPackageFile, PackageDir: testPackageDir},
			want:  0,
		},
		{
			name:  "same package",
			left:  similarityBlock{RelativePath: testPackageFile, PackageDir: testPackageDir},
			right: similarityBlock{RelativePath: "pkg/b.go", PackageDir: testPackageDir},
			want:  1,
		},
		{
			name:  "siblings",
			left:  similarityBlock{RelativePath: "internal/a/a.go", PackageDir: "internal/a"},
			right: similarityBlock{RelativePath: "internal/b/b.go", PackageDir: "internal/b"},
			want:  2,
		},
		{
			name:  "parent child",
			left:  similarityBlock{RelativePath: "internal/a.go", PackageDir: "internal"},
			right: similarityBlock{RelativePath: "internal/a/a.go", PackageDir: "internal/a"},
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
	matrix := similarityVectorMatrixForTest(t, blocks, [][][]float32{
		{{1, 0}, {0, 1}},
		{{-1, 0}, {0, 1}},
	})

	score, leftChunk, rightChunk := maximumDotSimilarity(matrix, blocks[0], blocks[1])
	if math.Abs(score-1) > 1e-12 || leftChunk != 1 || rightChunk != 1 {
		t.Fatalf(
			"maximum dot similarity = (%f, %d, %d), want (1, 1, 1)",
			score,
			leftChunk,
			rightChunk,
		)
	}
}

func TestSimilarityThresholdRisesWithDistanceAndTestCode(t *testing.T) {
	t.Parallel()

	if sameFile, samePackage := similarityEmbeddingThreshold(
		0,
		false,
	), similarityEmbeddingThreshold(
		1,
		false,
	); sameFile >= samePackage {
		t.Fatalf("same-file threshold %f must be below same-package %f", sameFile, samePackage)
	}

	if production, testCode := similarityEmbeddingThreshold(
		0,
		false,
	), similarityEmbeddingThreshold(
		0,
		true,
	); production >= testCode {
		t.Fatalf("production threshold %f must be below test threshold %f", production, testCode)
	}

	tests := []struct {
		name string
		tier int
		test bool
		want float64
	}{
		{name: "same file", tier: 0, want: 0.970},
		{name: "same package", tier: 1, want: 0.975},
		{name: "sibling package", tier: 2, want: 0.980},
		{name: "extra package layer", tier: 3, want: 0.983},
		{name: "test code", tier: 0, test: true, want: 0.995},
		{name: "maximum", tier: 20, test: true, want: 0.999},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := similarityEmbeddingThreshold(test.tier, test.test)
			if math.Abs(got-test.want) > 1e-12 {
				t.Fatalf("threshold = %f, want %f", got, test.want)
			}
		})
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
	matrix := similarityVectorMatrixForTest(t, blocks, [][][]float32{
		{{1, 0}},
		{{0.8, 0.6}},
	})

	if matches := groupSimilarityMatches(
		blocks,
		scanSimilarityPairs(blocks, matrix, nil),
	); len(
		matches,
	) != 0 {
		t.Fatalf("matches = %#v, want embedding gate to reject pair", matches)
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

	matrix := similarityVectorMatrixForTest(t, blocks, [][][]float32{
		{{1, 0}},
		{{1, 0}},
		{{1, 0}},
		{{1, 0}},
	})

	matches := groupSimilarityMatches(blocks, scanSimilarityPairs(blocks, matrix, nil))
	if len(matches) != 1 {
		t.Fatalf("exact-copy groups = %d, want 1", len(matches))
	}

	if got := len(matches[0].Members); got != len(blocks) {
		t.Fatalf("exact-copy group members = %d, want %d", got, len(blocks))
	}

	changed := map[string]struct{}{blocks[3].Identity: {}}
	if got := len(scanSimilarityPairs(blocks, matrix, changed)); got != len(blocks)-1 {
		t.Fatalf("incremental pairs = %d, want %d", got, len(blocks)-1)
	}

	if got := len(groupSimilarityMatches(
		blocks,
		scanSimilarityPairs(blocks, matrix, changed),
	)); got != 1 {
		t.Fatalf("incremental exact-copy matches = %d, want 1", got)
	}
}

func similarityVectorMatrixForTest(
	t *testing.T,
	blocks []*similarityBlock,
	vectors [][][]float32,
) similarityVectorMatrix {
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

	matrix, err := packSimilarityVectors(blocks, inputs)
	if err != nil {
		t.Fatalf("pack vectors: %v", err)
	}

	return matrix
}

func TestSimilarityPolicyChangeDropsAcceptances(t *testing.T) {
	t.Parallel()

	block := &similarityBlock{Identity: "sample.go::first", ContentHash: "first"}
	stamp := newSimilarityStamp("source", 1, []similarityAcceptance{{
		ID:        "sim-old",
		Left:      block.Identity,
		LeftHash:  block.ContentHash,
		Right:     block.Identity,
		RightHash: block.ContentHash,
	}})
	stamp.Schema--

	if got := carrySimilarityAcceptances(stamp, true, []*similarityBlock{block}); len(got) != 0 {
		t.Fatalf("obsolete acceptances carried: %#v", got)
	}
}

func similarityIssuePairID(t *testing.T, message string) string {
	t.Helper()

	start := strings.LastIndex(message, "id ")
	if start < 0 || !strings.HasSuffix(message, ")") {
		t.Fatalf("finding has no pair id: %q", message)
	}

	return strings.TrimSuffix(message[start+3:], ")")
}

func writeSimilarityTestSource(t *testing.T, dir string) {
	t.Helper()

	writeFile(t, filepath.Join(dir, similarityTestFilename), similarityTestSource)
}

const similarityTestFilename = "sample.go"

const similarityTestSource = `package sample

func first(values []int) int {
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
