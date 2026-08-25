package lint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPruneCachesRemovesOnlyUnreachableOrExpiredData(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, time.August, 25, 12, 0, 0, 0, time.UTC)
	mature := now.Add(-2 * cacheConcurrentWriteGrace)
	expired := now.Add(-2 * cacheUnusedRetention)

	obsoleteAnalysis := filepath.Join(root, "analysis-v5", "old.json")
	obsoleteSimilarity := filepath.Join(root, "similarity-v0", "old.bin")
	futureAnalysis := filepath.Join(root, "analysis-v7", "future.json")
	futureSimilarity := filepath.Join(root, "similarity-v2", "future.bin")

	writeFile(t, obsoleteAnalysis, "old")
	writeFile(t, obsoleteSimilarity, "old")
	writeFile(t, futureAnalysis, "future")
	writeFile(t, futureSimilarity, "future")

	analysisRoot, err := analysisCacheRoot(root)
	if err != nil {
		t.Fatal(err)
	}

	expiredAnalysis := filepath.Join(analysisRoot, "packages", "aa", "expired.json")
	currentAnalysis := filepath.Join(analysisRoot, "packages", "bb", "current.json")

	writeFile(t, expiredAnalysis, "expired")
	writeFile(t, currentAnalysis, "current")
	setCacheTestModTime(t, expiredAnalysis, expired)
	setCacheTestModTime(t, currentAnalysis, mature)

	similarityRoot, err := similarityVectorCacheRoot(root)
	if err != nil {
		t.Fatal(err)
	}

	block := cacheGCTestBlock()
	snapshot := cacheGCTestSnapshot(block)

	snapshotData, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}

	snapshotPath := filepath.Join(
		similarityRoot,
		"repos",
		"aa",
		"repo",
		snapshot.SourceDigest+".json",
	)
	writeFile(t, snapshotPath, string(snapshotData))
	setCacheTestModTime(t, snapshotPath, mature)

	if err := storeSimilarityCacheReferenceManifest(
		similarityRoot,
		snapshotPath,
		snapshot,
	); err != nil {
		t.Fatal(err)
	}

	manifestPath := similarityCacheReferenceManifestPath(snapshotPath)
	setCacheTestModTime(t, manifestPath, mature)

	referencedPaths := cacheGCTestReferencedPaths(t, similarityRoot, block)
	for _, path := range referencedPaths {
		setCacheTestModTime(t, path, expired)
	}

	orphanVectorKey := strings.Repeat("e", sha256.Size*2)
	if err := storeSimilarityVector(similarityRoot, orphanVectorKey, []float32{1, 0}); err != nil {
		t.Fatal(err)
	}

	orphanVector := similarityVectorPath(similarityRoot, orphanVectorKey)
	setCacheTestModTime(t, orphanVector, mature)

	recentOrphanKey := strings.Repeat("1", sha256.Size*2)
	if err := storeSimilarityVector(similarityRoot, recentOrphanKey, []float32{1, 0}); err != nil {
		t.Fatal(err)
	}

	recentOrphan := similarityVectorPath(similarityRoot, recentOrphanKey)

	setCacheTestModTime(t, recentOrphan, now.Add(-cacheConcurrentWriteGrace/2))

	expiredBlock := cacheGCTestBlock()
	expiredBlock.Identity = "gone.go::gone"
	expiredBlock.Symbol = "gone"
	expiredBlock.RelativePath = "gone.go"
	expiredBlock.Content = "func gone(values []int) int { return cap(values) }"
	expiredContentDigest := sha256.Sum256([]byte(expiredBlock.Content))
	expiredBlock.ContentHash = hex.EncodeToString(expiredContentDigest[:])
	expiredSnapshot := cacheGCTestSnapshot(expiredBlock)
	expiredSnapshot.SourceDigest = strings.Repeat("d", sha256.Size*2)
	expiredSnapshotPath := filepath.Join(
		similarityRoot,
		"repos",
		"aa",
		"repo",
		expiredSnapshot.SourceDigest+".json",
	)

	expiredSnapshotData, err := json.Marshal(expiredSnapshot)
	if err != nil {
		t.Fatal(err)
	}

	writeFile(t, expiredSnapshotPath, string(expiredSnapshotData))
	setCacheTestModTime(t, expiredSnapshotPath, expired)

	expiredVectorKey := similarityVectorCacheKey(
		expiredBlock.Content,
		similaritySourceVector,
		false,
	)
	if err := storeSimilarityVector(similarityRoot, expiredVectorKey, []float32{1, 0}); err != nil {
		t.Fatal(err)
	}

	expiredVector := similarityVectorPath(similarityRoot, expiredVectorKey)
	setCacheTestModTime(t, expiredVector, expired)

	orphanDescription := filepath.Join(
		similarityRoot,
		"descriptions",
		"signatures",
		"ef",
		strings.Repeat("f", 62)+".json",
	)
	legacyDescription := filepath.Join(
		similarityRoot,
		"descriptions",
		"ab",
		strings.Repeat("c", 62)+".json",
	)

	writeFile(t, orphanDescription, "{}")
	writeFile(t, legacyDescription, "{}")
	setCacheTestModTime(t, orphanDescription, mature)
	setCacheTestModTime(t, legacyDescription, mature)

	modelRoot := filepath.Join(root, "models")
	currentModel := filepath.Join(modelRoot, similarityModelDigest+".gguf")
	obsoleteModel := filepath.Join(modelRoot, strings.Repeat("d", 64)+".gguf")

	writeFile(t, currentModel, "current")
	writeFile(t, obsoleteModel, "obsolete")
	setCacheTestModTime(t, currentModel, expired)
	setCacheTestModTime(t, obsoleteModel, expired)

	if err := pruneCaches(root, now, cachePrunePolicy{
		unusedRetention: cacheUnusedRetention,
		writeGrace:      cacheConcurrentWriteGrace,
		maximumBytes:    1024 * 1024,
	}); err != nil {
		t.Fatalf("prune caches: %v", err)
	}

	requireCacheTestPaths(t, append(
		referencedPaths,
		currentAnalysis,
		currentModel,
		snapshotPath,
		manifestPath,
		recentOrphan,
		futureAnalysis,
		futureSimilarity,
	)...)

	requireCacheTestPathsRemoved(t,
		obsoleteAnalysis,
		obsoleteSimilarity,
		expiredAnalysis,
		orphanVector,
		orphanDescription,
		legacyDescription,
		obsoleteModel,
		expiredSnapshotPath,
		expiredVector,
	)
}

func TestPruneCachesEnforcesGlobalSizeCapByLastUse(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, time.August, 25, 12, 0, 0, 0, time.UTC)

	analysisRoot, err := analysisCacheRoot(root)
	if err != nil {
		t.Fatal(err)
	}

	paths := []string{
		filepath.Join(analysisRoot, "packages", "aa", "oldest.json"),
		filepath.Join(analysisRoot, "packages", "bb", "middle.json"),
		filepath.Join(analysisRoot, "packages", "cc", "newest.json"),
	}
	for index, path := range paths {
		writeFile(t, path, strings.Repeat("x", 8))
		setCacheTestModTime(t, path, now.Add(time.Duration(index-3)*time.Hour))
	}

	if err := pruneCaches(root, now, cachePrunePolicy{
		unusedRetention: 365 * 24 * time.Hour,
		writeGrace:      0,
		maximumBytes:    12,
	}); err != nil {
		t.Fatalf("prune caches: %v", err)
	}

	for _, path := range paths[:2] {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("evicted cache %s still exists: %v", path, err)
		}
	}

	if _, err := os.Stat(paths[2]); err != nil {
		t.Fatalf("newest cache removed: %v", err)
	}
}

func TestPruneCachesPreservesBlobsWhileRetainedSnapshotNeedsManifest(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, time.August, 25, 12, 0, 0, 0, time.UTC)
	mature := now.Add(-2 * cacheConcurrentWriteGrace)

	similarityRoot, err := similarityVectorCacheRoot(root)
	if err != nil {
		t.Fatal(err)
	}

	snapshotPath := filepath.Join(
		similarityRoot,
		"repos",
		"aa",
		"repo",
		strings.Repeat("a", sha256.Size*2)+".json",
	)
	writeFile(t, snapshotPath, "{}")
	setCacheTestModTime(t, snapshotPath, mature)

	vectorKey := strings.Repeat("b", sha256.Size*2)
	if err := storeSimilarityVector(similarityRoot, vectorKey, []float32{1, 0}); err != nil {
		t.Fatal(err)
	}

	vectorPath := similarityVectorPath(similarityRoot, vectorKey)
	setCacheTestModTime(t, vectorPath, mature)

	if err := pruneCaches(root, now, cachePrunePolicy{
		unusedRetention: cacheUnusedRetention,
		writeGrace:      cacheConcurrentWriteGrace,
		maximumBytes:    1,
	}); err != nil {
		t.Fatalf("prune caches: %v", err)
	}

	if _, err := os.Stat(vectorPath); err != nil {
		t.Fatalf("blob removed before retained snapshots have manifests: %v", err)
	}
}

func cacheGCTestBlock() similarityCachedBlock {
	content := "func live(values []int) int { return len(values) }"
	contentDigest := sha256.Sum256([]byte(content))
	description := "count input values and return collection length"
	descriptionDigest := sha256.Sum256([]byte(description))

	return similarityCachedBlock{
		Identity:               "live.go::live",
		Symbol:                 "live",
		RelativePath:           "live.go",
		Content:                content,
		ContentHash:            hex.EncodeToString(contentDigest[:]),
		Line:                   1,
		Column:                 1,
		Structural:             []uint64{1},
		VectorCount:            1,
		Description:            description,
		DescriptionHash:        hex.EncodeToString(descriptionDigest[:]),
		DescriptionVectorCount: 1,
	}
}

func cacheGCTestSnapshot(block similarityCachedBlock) similarityScanCache {
	return similarityScanCache{
		Schema:            similarityScanCacheSchema,
		SimilaritySchema:  similaritySchema,
		SourceDigest:      strings.Repeat("a", sha256.Size*2),
		Model:             similarityModelName,
		ModelDigest:       similarityModelDigest,
		Descriptions:      true,
		DescriptionSchema: similarityDescriptionPromptSchema,
		DescriptionModel:  similarityDescriptionModel,
		DescriptionEffort: similarityDescriptionEffort,
		DescriptionDigest: strings.Repeat("b", sha256.Size*2),
		Files: []similarityCachedFile{{
			RelativePath: block.RelativePath,
			ContentHash:  strings.Repeat("c", sha256.Size*2),
		}},
		Blocks: []similarityCachedBlock{block},
	}
}

func cacheGCTestReferencedPaths(
	t *testing.T,
	root string,
	block similarityCachedBlock,
) []string {
	t.Helper()

	sourceKey := similarityVectorCacheKey(block.Content, similaritySourceVector, false)
	descriptionKey := similarityVectorCacheKey(
		block.Description,
		similarityDescriptionVector,
		false,
	)
	_, signatureKey := similarityDescriptionCacheKey(
		block.ContentHash,
		block.IsTest,
		similarityDescriptionSignatures,
	)

	if err := storeSimilarityVector(root, sourceKey, []float32{1, 0}); err != nil {
		t.Fatal(err)
	}

	if err := storeSimilarityVector(root, descriptionKey, []float32{0, 1}); err != nil {
		t.Fatal(err)
	}

	descriptionPath := similarityDescriptionPath(
		root,
		signatureKey,
		similarityDescriptionSignatures,
	)
	writeFile(t, descriptionPath, "{}")

	return []string{
		similarityVectorPath(root, sourceKey),
		similarityVectorPath(root, descriptionKey),
		descriptionPath,
	}
}

func requireCacheTestPaths(t *testing.T, paths ...string) {
	t.Helper()

	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("retained cache %s: %v", path, err)
		}
	}
}

func requireCacheTestPathsRemoved(t *testing.T, paths ...string) {
	t.Helper()

	for _, path := range paths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("stale cache %s still exists: %v", path, err)
		}
	}
}

func setCacheTestModTime(t *testing.T, path string, modified time.Time) {
	t.Helper()

	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatalf("set modtime %s: %v", path, err)
	}
}
