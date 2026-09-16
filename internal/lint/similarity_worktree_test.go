package lint

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestSimilarityBlockCacheDropsDeletedFileState(t *testing.T) {
	tmp := newTestModule(t)
	deletedPath := filepath.Join(tmp, similarityTestFilename)
	writeSimilarityTestSource(t, tmp)
	writeFile(t, filepath.Join(tmp, "keep.go"), "package sample\n")

	files, blocks, err := collectSimilarityBlocks(
		loadPackagesForTest(t, tmp),
		tmp,
		similarityScanCache{},
	)
	if err != nil {
		t.Fatal(err)
	}

	if len(blocks) != 2 {
		t.Fatalf("initial blocks = %d, want 2", len(blocks))
	}

	previous := similarityScanCache{
		Files:  files,
		Blocks: cacheSimilarityBlocks(blocks),
		Matches: []similarityCachedMatch{{
			Left:  blocks[0].Identity,
			Right: blocks[1].Identity,
		}},
	}

	if err := os.Remove(deletedPath); err != nil {
		t.Fatal(err)
	}

	_, current, err := collectSimilarityBlocks(
		loadPackagesForTest(t, tmp),
		tmp,
		previous,
	)
	if err != nil {
		t.Fatal(err)
	}

	if len(current) != 0 {
		t.Fatalf("blocks after deletion = %d, want 0", len(current))
	}

	if restored := previous.restoreMatches(
		current,
		previous.changedBlocks(current),
	); len(
		restored,
	) != 0 {
		t.Fatalf("restored matches after deletion = %#v", restored)
	}
}

func TestSimilarityScanCacheBoundsPastSourceSnapshots(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, time.August, 25, 12, 0, 0, 0, time.UTC)
	total := similarityScanCacheSnapshots + 3

	for index := range total {
		path := filepath.Join(dir, fmt.Sprintf("%02d.json", index))
		writeFile(t, path, "{}")
		writeFile(t, similarityCacheReferenceManifestPath(path), "{}")
		setCacheTestModTime(t, path, now.Add(time.Duration(index)*time.Second))
	}

	pruneSimilarityScanCacheSnapshots(dir)

	snapshots := similarityScanCacheSnapshotsInDir(dir)
	if len(snapshots) != similarityScanCacheSnapshots {
		t.Fatalf("snapshots = %d, want %d", len(snapshots), similarityScanCacheSnapshots)
	}

	for index := range total - similarityScanCacheSnapshots {
		path := filepath.Join(dir, fmt.Sprintf("%02d.json", index))
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("old snapshot %s still exists: %v", path, err)
		}

		if _, err := os.Stat(similarityCacheReferenceManifestPath(path)); !os.IsNotExist(err) {
			t.Errorf("old reference manifest %s still exists: %v", path, err)
		}
	}
}
