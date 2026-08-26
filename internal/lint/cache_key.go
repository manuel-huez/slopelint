package lint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/types"
	"os"
	"path/filepath"
	"runtime"
	"sort"

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
	maxStates := opts.MaxStates
	if maxStates <= 0 {
		maxStates = 32
	}

	fingerprint := analysisCacheFingerprint{
		Schema:        analysisCacheSchema,
		Package:       pkg.ImportPath,
		MaxStates:     maxStates,
		Executable:    analysisCacheExecutableStamp(),
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
			if _, ok := digests[imported.Path()]; ok {
				continue
			}

			digests[imported.Path()] = analysisCacheTypeDigest(imported)
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

func analysisCacheExecutableStamp() analysisCacheExecutable {
	path, err := os.Executable()
	if err != nil {
		return analysisCacheExecutable{}
	}

	return analysisCacheExecutableForPath(path)
}

func analysisCacheExecutableForPath(path string) analysisCacheExecutable {
	if path == "" {
		return analysisCacheExecutable{}
	}

	info, err := os.Stat(path)
	if err != nil {
		return analysisCacheExecutable{Path: path}
	}

	return analysisCacheExecutable{
		Path:             path,
		Size:             info.Size(),
		ModTimeUnixNanos: info.ModTime().UnixNano(),
	}
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
