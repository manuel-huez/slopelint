package lint

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	cachePruneInterval              = 24 * time.Hour
	cacheUnusedRetention            = 30 * 24 * time.Hour
	cacheConcurrentWriteGrace       = time.Hour
	cachePruneLockStaleAfter        = time.Hour
	cacheMaximumBytes         int64 = 512 * 1024 * 1024
	cachePruneMarkerName            = ".last-prune"
	cachePruneLockName              = ".prune-lock"
)

type cachePrunePolicy struct {
	unusedRetention time.Duration
	writeGrace      time.Duration
	maximumBytes    int64
}

var defaultCachePrunePolicy = cachePrunePolicy{
	unusedRetention: cacheUnusedRetention,
	writeGrace:      cacheConcurrentWriteGrace,
	maximumBytes:    cacheMaximumBytes,
}

type cachePruneEntryKind uint8

const (
	cachePruneUnknown cachePruneEntryKind = iota
	cachePruneAnalysis
	cachePruneScan
	cachePruneReferences
	cachePruneVector
	cachePruneDescription
	cachePruneModel
)

type cachePruneEntry struct {
	path       string
	size       int64
	modified   time.Time
	lastUse    time.Time
	kind       cachePruneEntryKind
	referenced bool
	removed    bool
}

// maybePruneCaches runs only after cache writes; exact cache hits skip maintenance entirely.
func maybePruneCaches(dir string) {
	root, err := slopelintCacheRoot(dir)
	if err != nil {
		return
	}

	now := time.Now()
	if !cachePruneDue(root, now) {
		return
	}

	if os.MkdirAll(root, cacheDirPerm) != nil {
		return
	}

	lockPath := filepath.Join(root, cachePruneLockName)
	if !acquireCachePruneLock(lockPath, now) {
		return
	}
	defer func() { _ = os.Remove(lockPath) }()

	// Another worktree can finish maintenance between the first check and lock claim.
	if !cachePruneDue(root, now) {
		return
	}

	_ = pruneCaches(root, now, defaultCachePrunePolicy)
	_ = writeFileAtomically(
		filepath.Join(root, cachePruneMarkerName),
		[]byte(now.UTC().Format(time.RFC3339Nano)),
	)
}

func cachePruneDue(root string, now time.Time) bool {
	info, err := os.Stat(filepath.Join(root, cachePruneMarkerName))
	if err != nil {
		return errors.Is(err, os.ErrNotExist)
	}

	age := now.Sub(info.ModTime())

	return age < 0 || age >= cachePruneInterval
}

func acquireCachePruneLock(path string, now time.Time) bool {
	if err := os.Mkdir(path, cacheDirPerm); err == nil {
		return true
	}

	info, err := os.Stat(path)
	if err != nil || now.Sub(info.ModTime()) < cachePruneLockStaleAfter {
		return false
	}

	if os.Remove(path) != nil {
		return false
	}

	return os.Mkdir(path, cacheDirPerm) == nil
}

// refreshCacheEntry records coarse-grained use without rewriting immutable cache data.
func refreshCacheEntry(path string) {
	info, err := os.Stat(path)
	if err != nil || time.Since(info.ModTime()) < cachePruneInterval {
		return
	}

	now := time.Now()
	_ = os.Chtimes(path, now, now)
}

func pruneCaches(root string, now time.Time, policy cachePrunePolicy) error {
	if err := removeObsoleteCacheSchemas(root); err != nil {
		return err
	}

	entries, err := cachePruneEntries(root)
	if err != nil {
		return err
	}

	// Legacy snapshots have no compact manifest. Keep all blobs until those snapshots
	// are loaded and backfilled or expire, rather than risk cross-worktree cache loss.
	completeReferences := markSimilarityCacheReferences(root, entries, now, policy)
	pruneUnusedCacheEntries(entries, now, policy, completeReferences)
	pruneCacheSize(entries, now, policy, completeReferences)
	removeEmptyCacheDirectories(root)

	return nil
}

func removeObsoleteCacheSchemas(root string) error {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	if err != nil {
		return err
	}

	var cleanupErr error

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		name := entry.Name()
		if !obsoleteVersionedCache(name, "analysis-v", analysisCacheSchema) &&
			!obsoleteVersionedCache(name, "similarity-v", similarityCacheSchema) {
			continue
		}

		cleanupErr = errors.Join(cleanupErr, os.RemoveAll(filepath.Join(root, name)))
	}

	return cleanupErr
}

func obsoleteVersionedCache(name string, prefix string, current int) bool {
	if !strings.HasPrefix(name, prefix) {
		return false
	}

	version, err := strconv.Atoi(strings.TrimPrefix(name, prefix))

	return err == nil && version < current
}

func cachePruneEntries(root string) ([]*cachePruneEntry, error) {
	analysisRoot, err := analysisCacheRoot(root)
	if err != nil {
		return nil, err
	}

	similarityRoot, err := similarityVectorCacheRoot(root)
	if err != nil {
		return nil, err
	}

	modelRoot := filepath.Join(root, "models")

	entries := make([]*cachePruneEntry, 0)

	for _, target := range []struct {
		root string
		kind func(string) cachePruneEntryKind
	}{
		{root: analysisRoot, kind: func(string) cachePruneEntryKind {
			return cachePruneAnalysis
		}},
		{root: similarityRoot, kind: func(path string) cachePruneEntryKind {
			return similarityCacheEntryKind(similarityRoot, path)
		}},
		{root: modelRoot, kind: func(string) cachePruneEntryKind {
			return cachePruneModel
		}},
	} {
		err = filepath.WalkDir(target.root, func(
			path string,
			entry fs.DirEntry,
			walkErr error,
		) error {
			if walkErr != nil {
				return walkErr
			}

			if entry.IsDir() {
				return nil
			}

			info, infoErr := entry.Info()
			if infoErr != nil {
				return infoErr
			}

			kind := target.kind(path)
			if kind == cachePruneModel &&
				filepath.Base(path) == similarityModelDigest+".gguf" {
				return nil
			}

			entries = append(entries, &cachePruneEntry{
				path:     path,
				size:     info.Size(),
				modified: info.ModTime(),
				lastUse:  info.ModTime(),
				kind:     kind,
			})

			return nil
		})
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}

	return entries, nil
}

func similarityCacheEntryKind(root string, path string) cachePruneEntryKind {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return cachePruneUnknown
	}

	parts := strings.Split(filepath.ToSlash(relative), "/")
	if len(parts) == 0 {
		return cachePruneUnknown
	}

	switch {
	case parts[0] == "repos" && strings.HasSuffix(relative, ".json.refs"):
		return cachePruneReferences
	case parts[0] == "repos" && strings.HasSuffix(relative, ".json"):
		return cachePruneScan
	case parts[0] == "vectors" && strings.HasSuffix(relative, ".bin"):
		return cachePruneVector
	case parts[0] == "descriptions" && strings.HasSuffix(relative, ".json"):
		return cachePruneDescription
	default:
		return cachePruneUnknown
	}
}

func pruneUnusedCacheEntries(
	entries []*cachePruneEntry,
	now time.Time,
	policy cachePrunePolicy,
	completeReferences bool,
) {
	oldest := now.Add(-policy.unusedRetention)
	recent := now.Add(-policy.writeGrace)

	for _, entry := range entries {
		if entry.modified.After(recent) {
			continue
		}

		remove := entry.lastUse.Before(oldest)

		unreferencedBlob := completeReferences &&
			(entry.kind == cachePruneVector || entry.kind == cachePruneDescription)
		if !entry.referenced && (unreferencedBlob ||
			entry.kind == cachePruneReferences || entry.kind == cachePruneUnknown) {
			remove = true
		}

		if remove && os.Remove(entry.path) == nil {
			entry.removed = true
		}
	}
}

func pruneCacheSize(
	entries []*cachePruneEntry,
	now time.Time,
	policy cachePrunePolicy,
	completeReferences bool,
) {
	var total int64

	remaining := make([]*cachePruneEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.removed {
			continue
		}

		total += entry.size
		remaining = append(remaining, entry)
	}

	if total <= policy.maximumBytes {
		return
	}

	slices.SortFunc(remaining, func(left, right *cachePruneEntry) int {
		if order := left.lastUse.Compare(right.lastUse); order != 0 {
			return order
		}

		return strings.Compare(left.path, right.path)
	})

	recent := now.Add(-policy.writeGrace)
	for _, entry := range remaining {
		if total <= policy.maximumBytes {
			break
		}

		unverifiedBlob := !completeReferences &&
			(entry.kind == cachePruneVector || entry.kind == cachePruneDescription)
		if unverifiedBlob || entry.modified.After(recent) || os.Remove(entry.path) != nil {
			continue
		}

		entry.removed = true
		total -= entry.size
	}
}

func removeEmptyCacheDirectories(root string) {
	dirs := make([]string, 0)
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && entry.IsDir() && path != root && entry.Name() != cachePruneLockName {
			dirs = append(dirs, path)
		}

		return nil
	})

	slices.Reverse(dirs)

	for _, dir := range dirs {
		_ = os.Remove(dir)
	}
}
