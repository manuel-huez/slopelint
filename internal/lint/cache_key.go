package lint

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/types"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/tools/go/analysis"
)

func analysisCacheRoot(dir string) (string, error) {
	root, err := slopelintCacheRoot(dir)
	if err != nil {
		return "", err
	}

	return filepath.Join(root, fmt.Sprintf("analysis-v%d", analysisCacheSchema)), nil
}

func analysisCacheKey(
	pass *analysis.Pass,
	pkg *LoadedPackage,
	opts Options,
) (string, error) {
	buildID, err := analysisCacheBuildID(opts)
	if err != nil {
		return "", err
	}

	maxStates := opts.MaxStates
	if maxStates <= 0 {
		maxStates = 32
	}

	executable, err := analysisCacheExecutableStamp()
	if err != nil {
		return "", err
	}

	fingerprint := analysisCacheFingerprint{
		Schema:        analysisCacheSchema,
		BuildID:       buildID,
		Package:       pkg.ImportPath,
		MaxStates:     maxStates,
		Executable:    executable,
		ImportedFacts: analysisCacheImportedFacts(pass, pkg),
	}

	files, err := analysisCacheFiles(pass)
	if err != nil {
		return "", err
	}

	fingerprint.Files = files

	return analysisCacheFingerprintKey(fingerprint)
}

type standaloneAnalysisCacheFingerprint struct {
	Schema       int                    `json:"schema"`
	BuildID      string                 `json:"build_id"`
	Package      string                 `json:"package"`
	TestOnly     bool                   `json:"test_only"`
	MaxStates    int                    `json:"max_states"`
	SkipDeadCode bool                   `json:"skip_dead_code"`
	GoRuntime    string                 `json:"go_runtime"`
	Files        []analysisCacheFile    `json:"files"`
	Imports      []analysisCacheTypeAPI `json:"imports"`
}

type analysisCacheTypeAPI struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

func standaloneAnalysisCacheKey(
	importPath string,
	testOnly bool,
	importPaths []string,
	files []analysisCacheSourceFile,
	opts Options,
	typeDigests map[string]string,
) (string, error) {
	buildID, err := analysisCacheBuildID(opts)
	if err != nil {
		return "", err
	}

	maxStates := opts.MaxStates
	if maxStates <= 0 {
		maxStates = 32
	}

	cacheFiles := make([]analysisCacheFile, len(files))
	for index, file := range files {
		cacheFiles[index] = analysisCacheFile{SHA256: file.SHA256}
		// Only test-support naming diagnostics depend on ordinary source filenames.
		if testOnly {
			cacheFiles[index].Name = file.RelativePath
		}
	}

	sort.Slice(cacheFiles, func(i, j int) bool {
		if cacheFiles[i].SHA256 == cacheFiles[j].SHA256 {
			return cacheFiles[i].Name < cacheFiles[j].Name
		}

		return cacheFiles[i].SHA256 < cacheFiles[j].SHA256
	})

	imports := make([]analysisCacheTypeAPI, 0, len(importPaths))
	for _, path := range importPaths {
		imports = append(imports, analysisCacheTypeAPI{
			Path:   path,
			Digest: typeDigests[path],
		})
	}

	sort.Slice(imports, func(i, j int) bool { return imports[i].Path < imports[j].Path })

	return analysisCacheFingerprintKey(standaloneAnalysisCacheFingerprint{
		Schema:       analysisCacheSchema,
		BuildID:      buildID,
		Package:      importPath,
		TestOnly:     testOnly,
		MaxStates:    maxStates,
		SkipDeadCode: opts.skipDeadCode,
		GoRuntime:    runtime.Version() + "/" + runtime.GOOS + "/" + runtime.GOARCH,
		Files:        cacheFiles,
		Imports:      imports,
	})
}

func loadAnalysisCacheTypeDigests(
	targets []*packageMeta,
	byImportPath map[string]*packageMeta,
	cacheDir string,
) (map[string]string, []string) {
	root, err := analysisCacheRoot(cacheDir)
	if err != nil {
		return nil, analysisCacheTypePaths(targets, byImportPath)
	}

	digests := make(map[string]string)
	missing := make([]string, 0)

	for _, path := range analysisCacheTypePaths(targets, byImportPath) {
		if path == unsafeImportPath {
			digests[path] = analysisCacheTypeDigest(types.Unsafe)
			continue
		}

		cachePath, ok := analysisCacheTypeDigestPath(root, byImportPath[path])
		if !ok {
			missing = append(missing, path)
			continue
		}

		data, err := os.ReadFile(cachePath)
		if err != nil || len(data) != sha256.Size*2 {
			missing = append(missing, path)
			continue
		}

		if _, err := hex.DecodeString(string(data)); err != nil {
			missing = append(missing, path)
			continue
		}

		digests[path] = string(data)

		refreshCacheEntry(cachePath)
	}

	return digests, missing
}

func storeAnalysisCacheTypeDigests(
	targets []*packageMeta,
	byImportPath map[string]*packageMeta,
	cacheDir string,
	digests map[string]string,
) {
	root, err := analysisCacheRoot(cacheDir)
	if err != nil {
		return
	}

	for _, path := range analysisCacheTypePaths(targets, byImportPath) {
		digest := digests[path]
		if path == unsafeImportPath || len(digest) != sha256.Size*2 {
			continue
		}

		cachePath, ok := analysisCacheTypeDigestPath(root, byImportPath[path])
		if ok {
			_ = writeFileAtomically(cachePath, []byte(digest))
		}
	}
}

func analysisCacheTypePaths(
	targets []*packageMeta,
	byImportPath map[string]*packageMeta,
) []string {
	paths := make(map[string]struct{})

	for _, target := range targets {
		for _, path := range target.Imports {
			dep := byImportPath[path]
			if path == unsafeImportPath || dep != nil && dep.Export != "" {
				paths[path] = struct{}{}
			}
		}
	}

	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}

	sort.Strings(ordered)

	return ordered
}

func analysisCacheTypeDigestPath(
	root string,
	meta *packageMeta,
) (string, bool) {
	if meta == nil || meta.ImportPath == "" || meta.BuildID == "" {
		return "", false
	}

	key, err := analysisCacheFingerprintKey(struct {
		Schema    int    `json:"schema"`
		Package   string `json:"package"`
		BuildID   string `json:"build_id"`
		GoRuntime string `json:"go_runtime"`
	}{
		Schema:    analysisCacheSchema,
		Package:   meta.ImportPath,
		BuildID:   meta.BuildID,
		GoRuntime: runtime.Version() + "/" + runtime.GOOS + "/" + runtime.GOARCH,
	})
	if err != nil {
		return "", false
	}

	return filepath.Join(root, "type-api", key[:2], key[2:]), true
}

type analysisCacheSourceFile struct {
	Name         string
	RelativePath string
	SHA256       string
	Size         int
}

func analysisCacheSourceFiles(
	paths []string,
	sourceRoot string,
) ([]analysisCacheSourceFile, error) {
	files := make([]analysisCacheSourceFile, 0, len(paths))

	for _, name := range paths {
		content, err := os.ReadFile(name)
		if err != nil {
			return nil, err
		}

		relativePath, err := filepath.Rel(sourceRoot, name)
		if err != nil || !filepath.IsLocal(relativePath) {
			return nil, errAnalysisCacheDisabled
		}

		digest := sha256.Sum256(content)
		files = append(files, analysisCacheSourceFile{
			Name:         name,
			RelativePath: filepath.ToSlash(relativePath),
			SHA256:       hex.EncodeToString(digest[:]),
			Size:         len(content),
		})
	}

	return files, nil
}

func analysisCacheTypeDigest(pkg *types.Package) string {
	digest := sha256.New()
	_, _ = digest.Write([]byte(pkg.Path()))
	_, _ = digest.Write([]byte{0})

	names := pkg.Scope().Names()
	sort.Strings(names)

	qualifier := func(other *types.Package) string {
		if other == nil {
			return ""
		}

		return other.Path()
	}

	for _, name := range names {
		object := pkg.Scope().Lookup(name)
		if object == nil || !object.Exported() {
			continue
		}

		_, _ = digest.Write([]byte(types.ObjectString(object, qualifier)))
		_, _ = digest.Write([]byte{0})

		typeName, ok := object.(*types.TypeName)
		if !ok {
			continue
		}

		named, ok := types.Unalias(typeName.Type()).(*types.Named)
		if !ok {
			continue
		}

		methods := make([]string, 0, named.NumMethods())
		for method := range named.Methods() {
			if method.Exported() {
				methods = append(methods, types.ObjectString(method, qualifier))
			}
		}

		sort.Strings(methods)

		for _, method := range methods {
			_, _ = digest.Write([]byte(method))
			_, _ = digest.Write([]byte{0})
		}
	}

	return hex.EncodeToString(digest.Sum(nil))
}

func analysisCacheTypeDigests(pkgs []*LoadedPackage) map[string]string {
	digests := make(map[string]string)

	for _, pkg := range pkgs {
		for _, imported := range pkg.TypesPkg.Imports() {
			// Cache keys name imports by go list path, so a test variant keeps its
			// " [foo.test]" suffix apart from the base package API.
			path := imported.Path()
			if variant, ok := pkg.importMap[path]; ok {
				path = variant
			}

			if _, ok := digests[path]; ok {
				continue
			}

			digests[path] = analysisCacheTypeDigest(imported)
		}
	}

	return digests
}

func analysisCacheFingerprintKey(fingerprint any) (string, error) {
	data, err := json.Marshal(fingerprint)
	if err != nil {
		return "", err
	}

	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:]), nil
}

func analysisCacheBuildID(opts Options) (string, error) {
	if opts.cacheBuildID != "" {
		return opts.cacheBuildID, nil
	}

	return installedAnalysisCacheBuildID()
}

var installedAnalysisCacheBuildID = sync.OnceValues(func() (string, error) {
	// A "+dirty" version names a commit, not the uncommitted analyzer code.
	if info, ok := debug.ReadBuildInfo(); ok &&
		info.Main.Version != "" && info.Main.Version != "(devel)" &&
		!strings.HasSuffix(info.Main.Version, "+dirty") {
		return info.Main.Path + "@" + info.Main.Version, nil
	}

	// Local and dirty builds hash the executable once so rebuilt analyzers cannot
	// reuse findings from a different executable, even when source is unchanged.
	path, err := os.Executable()
	if err != nil {
		return "", err
	}

	file, err := os.Open(path)
	if err != nil {
		return "", err
	}

	defer func() { _ = file.Close() }()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
})

func analysisCacheExecutableStamp() (analysisCacheExecutable, error) {
	path, err := os.Executable()
	if err != nil {
		return analysisCacheExecutable{}, err
	}

	info, err := os.Stat(path)
	if err != nil {
		return analysisCacheExecutable{}, err
	}

	return analysisCacheExecutable{
		Path:             path,
		Size:             info.Size(),
		ModTimeUnixNanos: info.ModTime().UnixNano(),
	}, nil
}

func analysisCacheFiles(pass *analysis.Pass) ([]analysisCacheFile, error) {
	files := make([]analysisCacheFile, 0, len(pass.Files))

	for _, file := range pass.Files {
		tokenFile := pass.Fset.File(file.Package)
		if tokenFile == nil {
			return nil, errors.New("missing token file for package file")
		}

		name := tokenFile.Name()

		content, err := readAnalysisFile(pass, name)
		if err != nil {
			return nil, err
		}

		sum := sha256.Sum256(content)
		files = append(files, analysisCacheFile{SHA256: hex.EncodeToString(sum[:])})
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].SHA256 < files[j].SHA256
	})

	return files, nil
}

func readAnalysisFile(pass *analysis.Pass, name string) ([]byte, error) {
	if pass.ReadFile != nil {
		return pass.ReadFile(name)
	}

	return os.ReadFile(name)
}

const (
	cachePruneInterval   = 24 * time.Hour
	cacheUnusedRetention = 30 * 24 * time.Hour
	// Unknown files may belong to a newer slopelint sharing this cache during an
	// upgrade, or to a concurrent writer; keep them long enough for both.
	cacheUnknownRetention    = 7 * 24 * time.Hour
	cachePruneLockStaleAfter = time.Hour
	cachePruneMarkerName     = ".last-prune"
	cachePruneLockName       = ".prune-lock"
)

type cachePrunePolicy struct {
	unusedRetention  time.Duration
	unknownRetention time.Duration
}

var defaultCachePrunePolicy = cachePrunePolicy{
	unusedRetention:  cacheUnusedRetention,
	unknownRetention: cacheUnknownRetention,
}

type cachePruneEntryKind uint8

const (
	cachePruneUnknown cachePruneEntryKind = iota
	cachePruneAnalysis
	cachePruneScan
	cachePruneVector
	cachePruneCodexRecord
)

type cachePruneEntry struct {
	path     string
	modified time.Time
	kind     cachePruneEntryKind
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

	pruneUnusedCacheEntries(entries, now, policy)
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

			entries = append(entries, &cachePruneEntry{
				path:     path,
				modified: info.ModTime(),
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
	case parts[0] == "repos" && strings.HasSuffix(relative, similarityScanCacheExt):
		return cachePruneScan
	case parts[0] == "vectors" && strings.HasSuffix(relative, ".bin"):
		return cachePruneVector
	case (parts[0] == "descriptions" || parts[0] == "reviews") &&
		strings.HasSuffix(relative, ".json"):
		return cachePruneCodexRecord
	default:
		return cachePruneUnknown
	}
}

// pruneUnusedCacheEntries removes lint results and scan snapshots unused for the
// retention period. Per-block vectors and Codex description and review records
// stay until a cache schema change: content keys keep them valid, and regenerating them costs model calls.
func pruneUnusedCacheEntries(
	entries []*cachePruneEntry,
	now time.Time,
	policy cachePrunePolicy,
) {
	for _, entry := range entries {
		if entry.kind == cachePruneVector || entry.kind == cachePruneCodexRecord {
			continue
		}

		retention := policy.unusedRetention
		if entry.kind == cachePruneUnknown {
			retention = policy.unknownRetention
		}

		if entry.modified.Before(now.Add(-retention)) {
			_ = os.Remove(entry.path)
		}
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

// Go's package list includes ignored source files that Git's cheap untracked
// listing omits. Query Go instead of traversing every ignored tool cache.
func repoAnalysisApplyListedInputs(
	files map[string]string,
	root string,
	moduleRoot string,
	objectFormat string,
) (bool, error) {
	replacements, err := repoAnalysisReplacementRoots(moduleRoot, root)
	if err != nil {
		return false, err
	}

	needsIgnoredScan := false

	for _, module := range append([]string{moduleRoot}, replacements...) {
		cmd := exec.Command(
			"go",
			"list",
			"-find",
			"-buildvcs=false",
			"-json=Dir,Error,GoFiles,CgoFiles,IgnoredGoFiles,TestGoFiles,XTestGoFiles,OtherFiles,IgnoredOtherFiles,CFiles,CXXFiles,MFiles,HFiles,FFiles,SFiles,SwigFiles,SwigCXXFiles,SysoFiles,EmbedFiles,TestEmbedFiles,XTestEmbedFiles",
			"./...",
		)
		cmd.Dir = module

		output, err := cmd.Output()
		if err != nil {
			return false, fmt.Errorf("list source files in %s: %w", module, err)
		}

		decoder := json.NewDecoder(bytes.NewReader(output))

		for {
			var pkg repoAnalysisListedPackage
			if err := decoder.Decode(&pkg); err != nil {
				if err == io.EOF {
					break
				}

				return false, fmt.Errorf("decode Go source list: %w", err)
			}

			needsScan, err := repoAnalysisApplyListedPackage(files, root, objectFormat, pkg)
			if err != nil {
				return false, err
			}

			if needsScan {
				needsIgnoredScan = true
			}
		}

		for _, file := range []string{goModFilename, goSumFilename} {
			if err := repoAnalysisAddListedFile(
				files,
				root,
				filepath.Join(module, file),
				objectFormat,
			); err != nil {
				return false, err
			}
		}
	}

	if err := repoAnalysisAddListedFile(
		files, root, filepath.Join(moduleRoot, similarityStampName), objectFormat,
	); err != nil {
		return false, err
	}

	return needsIgnoredScan, nil
}

func repoAnalysisApplyListedPackage(
	files map[string]string,
	root string,
	objectFormat string,
	pkg repoAnalysisListedPackage,
) (bool, error) {
	if pkg.Error != nil {
		return false, fmt.Errorf("list Go source in %s: %s", pkg.Dir, pkg.Error.Err)
	}

	// Cgo can include headers in subdirectories not listed by go list.
	needsIgnoredScan := len(pkg.CgoFiles) > 0 || len(pkg.SwigFiles) > 0 ||
		len(pkg.SwigCXXFiles) > 0
	groups := [][]string{
		pkg.GoFiles, pkg.CgoFiles, pkg.IgnoredGoFiles,
		pkg.TestGoFiles, pkg.XTestGoFiles,
		pkg.OtherFiles, pkg.IgnoredOtherFiles,
		pkg.CFiles, pkg.CXXFiles, pkg.MFiles, pkg.HFiles,
		pkg.FFiles, pkg.SFiles, pkg.SwigFiles, pkg.SwigCXXFiles,
		pkg.SysoFiles, pkg.EmbedFiles, pkg.TestEmbedFiles, pkg.XTestEmbedFiles,
	}

	for _, group := range groups {
		for _, file := range group {
			if err := repoAnalysisAddListedFile(
				files,
				root,
				filepath.Join(pkg.Dir, file),
				objectFormat,
			); err != nil {
				return false, err
			}
		}
	}

	return needsIgnoredScan, nil
}

func repoAnalysisAddListedFile(files map[string]string, root, path, objectFormat string) error {
	name, err := filepath.Rel(root, path)
	if err != nil || !filepath.IsLocal(name) {
		return errAnalysisCacheDisabled
	}

	name = filepath.ToSlash(name)

	objectID, err := repoAnalysisBlobID(root, name, objectFormat)
	if err != nil {
		return err
	}

	delete(files, name)

	if objectID != "" {
		files[name] = objectID
	}

	return nil
}

type repoAnalysisListedPackage struct {
	Dir               string                `json:"Dir"`
	Error             *struct{ Err string } `json:"Error"`
	GoFiles           []string              `json:"GoFiles"`
	CgoFiles          []string              `json:"CgoFiles"`
	IgnoredGoFiles    []string              `json:"IgnoredGoFiles"`
	TestGoFiles       []string              `json:"TestGoFiles"`
	XTestGoFiles      []string              `json:"XTestGoFiles"`
	OtherFiles        []string              `json:"OtherFiles"`
	IgnoredOtherFiles []string              `json:"IgnoredOtherFiles"`
	CFiles            []string              `json:"CFiles"`
	CXXFiles          []string              `json:"CXXFiles"`
	MFiles            []string              `json:"MFiles"`
	HFiles            []string              `json:"HFiles"`
	FFiles            []string              `json:"FFiles"`
	SFiles            []string              `json:"SFiles"`
	SwigFiles         []string              `json:"SwigFiles"`
	SwigCXXFiles      []string              `json:"SwigCXXFiles"`
	SysoFiles         []string              `json:"SysoFiles"`
	EmbedFiles        []string              `json:"EmbedFiles"`
	TestEmbedFiles    []string              `json:"TestEmbedFiles"`
	XTestEmbedFiles   []string              `json:"XTestEmbedFiles"`
}
