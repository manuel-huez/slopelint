package lint

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	similarityCacheReferenceSchema           = 1
	similarityReferencePathsPerBlockEstimate = 3
	similarityDetailedBlocksPerMatch         = 2
)

type similarityCacheReferenceManifest struct {
	Schema       int      `json:"schema"`
	SourceDigest string   `json:"source_digest"`
	Paths        []string `json:"paths"`
}

// storeSimilarityCacheReferenceManifest keeps daily GC independent of large scan JSON.
func storeSimilarityCacheReferenceManifest(
	root string,
	snapshotPath string,
	cache similarityScanCache,
) error {
	manifest := similarityCacheReferenceManifest{
		Schema:       similarityCacheReferenceSchema,
		SourceDigest: cache.SourceDigest,
		Paths:        similarityCacheReferencePaths(root, cache),
	}

	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}

	return writeFileAtomically(similarityCacheReferenceManifestPath(snapshotPath), data)
}

func loadSimilarityCacheReferenceManifest(
	snapshotPath string,
	sourceDigest string,
) ([]string, bool) {
	data, err := os.ReadFile(similarityCacheReferenceManifestPath(snapshotPath))
	if err != nil {
		return nil, false
	}

	var manifest similarityCacheReferenceManifest
	if json.Unmarshal(data, &manifest) != nil ||
		manifest.Schema != similarityCacheReferenceSchema ||
		manifest.SourceDigest != sourceDigest || !validSimilarityCacheReferencePaths(manifest.Paths) {
		return nil, false
	}

	return manifest.Paths, true
}

func validSimilarityCacheReferencePaths(paths []string) bool {
	previous := ""

	for _, path := range paths {
		localPath := filepath.FromSlash(path)

		validKind := strings.HasPrefix(path, "vectors/") && strings.HasSuffix(path, ".bin") ||
			strings.HasPrefix(path, "descriptions/signatures/") &&
				strings.HasSuffix(path, ".json") ||
			strings.HasPrefix(path, "descriptions/details/") &&
				strings.HasSuffix(path, ".json")
		if path == "" || path <= previous || path != filepath.ToSlash(localPath) ||
			!filepath.IsLocal(localPath) || !validKind {
			return false
		}

		previous = path
	}

	return true
}

func similarityCacheReferenceManifestPath(snapshotPath string) string {
	return snapshotPath + ".refs"
}

func similarityCacheReferencePaths(root string, cache similarityScanCache) []string {
	paths := make(
		map[string]struct{},
		len(cache.Blocks)*similarityReferencePathsPerBlockEstimate,
	)
	detailed := make(
		map[string]struct{},
		len(cache.Matches)*similarityDetailedBlocksPerMatch,
	)

	for _, match := range cache.Matches {
		detailed[match.Left] = struct{}{}
		detailed[match.Right] = struct{}{}
	}

	for _, block := range cache.Blocks {
		addSimilarityVectorReferencePaths(
			paths,
			root,
			block.Content,
			similaritySourceVector,
		)

		if !cache.Descriptions {
			continue
		}

		_, signatureKey := similarityDescriptionCacheKey(
			block.ContentHash,
			block.IsTest,
			similarityDescriptionSignatures,
		)
		addSimilarityCacheReferencePath(
			paths,
			root,
			similarityDescriptionPath(
				root,
				signatureKey,
				similarityDescriptionSignatures,
			),
		)
		addSimilarityVectorReferencePaths(
			paths,
			root,
			block.Description,
			similarityDescriptionVector,
		)

		if _, ok := detailed[block.Identity]; !ok {
			continue
		}

		_, detailKey := similarityDescriptionCacheKey(
			block.ContentHash,
			block.IsTest,
			similarityDescriptionDetails,
		)
		addSimilarityCacheReferencePath(
			paths,
			root,
			similarityDescriptionPath(
				root,
				detailKey,
				similarityDescriptionDetails,
			),
		)
	}

	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}

	slices.Sort(ordered)

	return ordered
}

func addSimilarityVectorReferencePaths(
	paths map[string]struct{},
	root string,
	content string,
	kind similarityVectorKind,
) {
	if content == "" {
		return
	}

	chunks := similarityEmbeddingInputs(content)
	chunked := len(chunks) > 1

	for _, chunk := range chunks {
		key := similarityVectorCacheKey(chunk, kind, chunked)
		addSimilarityCacheReferencePath(paths, root, similarityVectorPath(root, key))
	}
}

func addSimilarityCacheReferencePath(paths map[string]struct{}, root string, path string) {
	relative, err := filepath.Rel(root, path)
	if err == nil && filepath.IsLocal(relative) {
		paths[filepath.ToSlash(relative)] = struct{}{}
	}
}

func markSimilarityCacheReferences(
	root string,
	entries []*cachePruneEntry,
	now time.Time,
	policy cachePrunePolicy,
) bool {
	byPath := make(map[string]*cachePruneEntry, len(entries))
	for _, entry := range entries {
		byPath[entry.path] = entry
	}

	similarityRoot, err := similarityVectorCacheRoot(root)
	if err != nil {
		return false
	}

	complete := true
	oldest := now.Add(-policy.unusedRetention)

	for _, entry := range entries {
		if entry.kind != cachePruneScan || entry.modified.Before(oldest) {
			continue
		}

		sourceDigest := strings.TrimSuffix(filepath.Base(entry.path), ".json")
		paths, ok := loadSimilarityCacheReferenceManifest(entry.path, sourceDigest)

		if !ok {
			complete = false
			continue
		}

		markCacheReference(
			byPath[similarityCacheReferenceManifestPath(entry.path)],
			entry.modified,
		)

		for _, path := range paths {
			markCacheReference(
				byPath[filepath.Join(similarityRoot, filepath.FromSlash(path))],
				entry.modified,
			)
		}
	}

	return complete
}

func markCacheReference(entry *cachePruneEntry, lastUse time.Time) {
	if entry == nil {
		return
	}

	entry.referenced = true
	if lastUse.After(entry.lastUse) {
		entry.lastUse = lastUse
	}
}
