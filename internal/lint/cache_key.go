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
	if dir == "" {
		userCacheDir, err := os.UserCacheDir()
		if err != nil {
			return "", err
		}

		dir = filepath.Join(userCacheDir, "slopelint")
	}

	return filepath.Join(dir, fmt.Sprintf("analysis-v%d", analysisCacheSchema)), nil
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
	pkg *LoadedPackage,
	opts Options,
	typeDigests map[string]string,
) (string, error) {
	maxStates := opts.MaxStates
	if maxStates <= 0 {
		maxStates = 32
	}

	files, err := analysisCacheFiles(&analysis.Pass{
		Fset:     pkg.FSet,
		Files:    pkg.Files,
		ReadFile: os.ReadFile,
	})
	if err != nil {
		return "", err
	}

	imports := make([]analysisCacheTypeAPI, 0, len(pkg.TypesPkg.Imports()))
	for _, imported := range pkg.TypesPkg.Imports() {
		imports = append(imports, analysisCacheTypeAPI{
			Path:   imported.Path(),
			Digest: typeDigests[imported.Path()],
		})
	}

	sort.Slice(imports, func(i, j int) bool { return imports[i].Path < imports[j].Path })

	return analysisCacheFingerprintKey(standaloneAnalysisCacheFingerprint{
		Schema:       analysisCacheSchema,
		Package:      pkg.ImportPath,
		MaxStates:    maxStates,
		SkipDeadCode: opts.skipDeadCode,
		GoRuntime:    runtime.Version() + "/" + runtime.GOOS + "/" + runtime.GOARCH,
		Files:        files,
		Imports:      imports,
	})
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
		files = append(files, analysisCacheFile{
			Filename: name,
			SHA256:   hex.EncodeToString(sum[:]),
		})
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].Filename < files[j].Filename
	})

	return files, nil
}

func readAnalysisFile(pass *analysis.Pass, name string) ([]byte, error) {
	if pass.ReadFile != nil {
		return pass.ReadFile(name)
	}

	return os.ReadFile(name)
}
