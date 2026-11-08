package lint

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSimilarityStructuralCachePackingRoundTrip(t *testing.T) {
	values := []uint64{0, 1, 255, 1 << 40, ^uint64(0)}
	packed := packSimilarityStructural(values)
	got, ok := unpackSimilarityStructural(packed)

	if !ok || len(got) != len(values) {
		t.Fatalf("unpack = %v, valid=%t", got, ok)
	}

	for index := range values {
		if got[index] != values[index] {
			t.Fatalf("unpack[%d] = %d, want %d", index, got[index], values[index])
		}
	}
}

func TestPruneCachesKeepsBlockDataAndExpiresRebuildableData(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, time.August, 25, 12, 0, 0, 0, time.UTC)
	mature := now.Add(-2 * cacheConcurrentWriteGrace)
	expired := now.Add(-2 * cacheUnusedRetention)

	obsoleteAnalysis := filepath.Join(
		root,
		fmt.Sprintf("analysis-v%d", analysisCacheSchema-1),
		"old.json",
	)
	obsoleteSimilarity := filepath.Join(root, "similarity-v0", "old.bin")
	futureAnalysis := filepath.Join(
		root,
		fmt.Sprintf("analysis-v%d", analysisCacheSchema+1),
		"future.json",
	)
	futureSimilarity := filepath.Join(root, "similarity-v2", "future.bin")

	analysisRoot, err := analysisCacheRoot(root)
	if err != nil {
		t.Fatal(err)
	}

	similarityRoot, err := similarityVectorCacheRoot(root)
	if err != nil {
		t.Fatal(err)
	}

	snapshotDir := filepath.Join(similarityRoot, "repos", "aa", "repo")
	expiredAnalysis := filepath.Join(analysisRoot, "packages", "aa", "expired.json")
	currentAnalysis := filepath.Join(analysisRoot, "packages", "bb", "current.json")
	expiredSnapshot := filepath.Join(snapshotDir, "expired.json")
	currentSnapshot := filepath.Join(snapshotDir, "current.json")
	legacyManifest := filepath.Join(snapshotDir, "current.json.refs")
	recentUnknown := filepath.Join(snapshotDir, "writing.json.refs")
	vectorKey := strings.Repeat("e", sha256.Size*2)
	expiredVector := similarityVectorPath(similarityRoot, vectorKey)
	expiredDescription := similarityDescriptionPath(
		similarityRoot,
		strings.Repeat("f", sha256.Size*2),
		similarityDescriptionSignatures,
	)

	for path, modified := range map[string]time.Time{
		obsoleteAnalysis:   mature,
		obsoleteSimilarity: mature,
		futureAnalysis:     expired,
		futureSimilarity:   expired,
		expiredAnalysis:    expired,
		currentAnalysis:    mature,
		expiredSnapshot:    expired,
		currentSnapshot:    mature,
		legacyManifest:     mature,
		recentUnknown:      now,
		expiredVector:      expired,
		expiredDescription: expired,
	} {
		writeFile(t, path, "{}")
		setCacheTestModTime(t, path, modified)
	}

	if err := pruneCaches(root, now, defaultCachePrunePolicy); err != nil {
		t.Fatalf("prune caches: %v", err)
	}

	requireCacheTestPaths(t,
		futureAnalysis,
		futureSimilarity,
		currentAnalysis,
		currentSnapshot,
		recentUnknown,
		expiredVector,
		expiredDescription,
	)
	requireCacheTestPathsRemoved(t,
		obsoleteAnalysis,
		obsoleteSimilarity,
		expiredAnalysis,
		expiredSnapshot,
		legacyManifest,
	)
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
