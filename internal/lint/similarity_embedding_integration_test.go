//go:build integration

package lint

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestLlamaServerSimilarityIntegration(t *testing.T) {
	embedder, err := newHTTPSimilarityEmbedder(os.Getenv(similarityLlamaURLEnv))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := embedder.close(); err != nil {
			t.Error(err)
		}
	})

	vectors, err := embedder.embed([]string{
		"func add(left, right int) int { return left + right }",
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(vectors) != 1 || len(vectors[0]) != similarityEmbeddingDimensions {
		t.Fatalf(
			"live embeddings = %d vectors with %d dimensions",
			len(vectors),
			len(vectors[0]),
		)
	}

	for dimension, value := range vectors[0] {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			t.Fatalf("live embedding dimension %d is not finite", dimension)
		}
	}

	module := newTestModule(t)
	writeFile(
		t,
		filepath.Join(module, "internal", "first", "duplicate.go"),
		"package first\n\n"+liveSimilarityDuplicateFunction,
	)
	writeFile(
		t,
		filepath.Join(module, "internal", "second", "duplicate.go"),
		"package second\n\n"+liveSimilarityDuplicateFunction,
	)

	issues, err := CheckSimilarCode(
		loadPackagesForTest(t, module),
		SimilarityOptions{
			CacheEnabled:        true,
			cacheDir:            t.TempDir(),
			descriptionDisabled: true,
		},
	)
	requireSingleSimilarityIssue(t, issues, err)
}

const liveSimilarityDuplicateFunction = `func summarize(values []int) int {
	total := 0
	for index, value := range values {
		if value < 0 {
			continue
		}
		if index%2 == 0 {
			total += value * 2
		} else {
			total += value
		}
	}
	if total > 100 {
		return total - 10
	}
	if total > 50 {
		return total - 5
	}
	return total
}
`
