package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSimilarityScanCacheSharesLinkedWorktree(t *testing.T) {
	primary := newTestModule(t)
	cacheDir := t.TempDir()
	writeSimilarityTestSource(t, primary)
	initTestGitRepository(t, primary)

	firstEmbedder := &similarityTestEmbedder{vector: func(string) []float32 {
		return []float32{1, 0}
	}}
	firstPackages := loadPackagesForTest(t, primary)
	first, err := CheckSimilarCode(firstPackages, SimilarityOptions{
		CacheEnabled:        true,
		cacheDir:            cacheDir,
		embedder:            firstEmbedder,
		descriptionDisabled: true,
	})
	requireSingleSimilarityIssue(t, first, err)

	if firstEmbedder.calls == 0 {
		t.Fatal("primary scan did not populate embeddings")
	}

	linked := addTestGitWorktree(t, primary)
	linkedEmbedder := &similarityTestEmbedder{vector: func(string) []float32 {
		t.Fatal("linked worktree recomputed cached embedding")
		return nil
	}}
	linkedPackages := loadPackagesForTest(t, linked)
	second, err := CheckSimilarCode(linkedPackages, SimilarityOptions{
		CacheEnabled:        true,
		cacheDir:            cacheDir,
		embedder:            linkedEmbedder,
		descriptionDisabled: true,
	})
	requireSingleSimilarityIssue(t, second, err)

	if linkedEmbedder.calls != 0 {
		t.Fatalf("linked worktree embedding calls = %d", linkedEmbedder.calls)
	}

	if FormatIssuePosition(second[0]) == FormatIssuePosition(first[0]) ||
		!strings.HasPrefix(FormatIssuePosition(second[0]), linked+string(filepath.Separator)) {
		t.Fatalf("linked finding path = %q", FormatIssuePosition(second[0]))
	}
}

func TestSimilarityBlockCacheReusesRenamedFile(t *testing.T) {
	tmp := newTestModule(t)
	originalPath := filepath.Join(tmp, similarityTestFilename)
	renamedPath := filepath.Join(tmp, "renamed.go")

	writeSimilarityTestSource(t, tmp)

	files, blocks, err := collectSimilarityBlocks(
		loadPackagesForTest(t, tmp),
		tmp,
		similarityScanCache{},
	)
	if err != nil {
		t.Fatal(err)
	}

	for _, block := range blocks {
		block.VectorCount = 7
	}

	previous := similarityScanCache{Files: files, Blocks: cacheSimilarityBlocks(blocks)}

	if err := os.Rename(originalPath, renamedPath); err != nil {
		t.Fatal(err)
	}

	_, renamedBlocks, err := collectSimilarityBlocks(
		loadPackagesForTest(t, tmp),
		tmp,
		previous,
	)
	if err != nil {
		t.Fatal(err)
	}

	if len(renamedBlocks) != len(blocks) {
		t.Fatalf("renamed blocks = %d, want %d", len(renamedBlocks), len(blocks))
	}

	for _, block := range renamedBlocks {
		if block.RelativePath != "renamed.go" || block.VectorCount != 7 {
			t.Fatalf("renamed block = %#v", block)
		}

		if !strings.HasPrefix(block.Identity, "renamed.go::") {
			t.Fatalf("renamed identity = %q", block.Identity)
		}
	}
}
