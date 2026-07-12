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

func repoAnalysisCacheKey(pkgs []*LoadedPackage, opts Options) (string, error) {
	maxStates := opts.MaxStates
	if maxStates <= 0 {
		maxStates = 32
	}

	fingerprint := struct {
		Schema      int                        `json:"schema"`
		MaxStates   int                        `json:"max_states"`
		ClosedWorld bool                       `json:"closed_world"`
		Executable  analysisCacheExecutable    `json:"executable"`
		Packages    []repoAnalysisCachePackage `json:"packages"`
		Imports     []repoAnalysisCacheImport  `json:"imports"`
		Files       []analysisCacheFile        `json:"files"`
	}{
		Schema:      analysisCacheSchema,
		MaxStates:   maxStates,
		ClosedWorld: opts.ClosedWorld,
		Executable:  analysisCacheExecutableStamp(),
		Packages:    repoAnalysisCachePackages(pkgs),
		Imports:     repoAnalysisCacheImports(pkgs),
	}

	files, err := repoAnalysisCacheFiles(pkgs)
	if err != nil {
		return "", err
	}

	fingerprint.Files = files

	return analysisCacheFingerprintKey(fingerprint)
}

func analysisCacheFingerprintKey(fingerprint any) (string, error) {
	data, err := json.Marshal(fingerprint)
	if err != nil {
		return "", err
	}

	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:]), nil
}

func repoAnalysisCachePackages(pkgs []*LoadedPackage) []repoAnalysisCachePackage {
	out := make([]repoAnalysisCachePackage, 0, len(pkgs))
	for _, pkg := range pkgs {
		if pkg == nil {
			continue
		}

		out = append(out, repoAnalysisCachePackage{
			ImportPath: pkg.ImportPath,
			Name:       pkg.Name,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].ImportPath != out[j].ImportPath {
			return out[i].ImportPath < out[j].ImportPath
		}

		return out[i].Name < out[j].Name
	})

	return out
}

func repoAnalysisCacheImports(pkgs []*LoadedPackage) []repoAnalysisCacheImport {
	importsByPath := make(map[string]*types.Package)

	for _, pkg := range pkgs {
		if pkg == nil || pkg.TypesPkg == nil {
			continue
		}

		for _, imported := range pkg.TypesPkg.Imports() {
			if imported == nil {
				continue
			}

			importsByPath[imported.Path()] = imported
		}
	}

	paths := make([]string, 0, len(importsByPath))
	for path := range importsByPath {
		paths = append(paths, path)
	}

	sort.Strings(paths)

	out := make([]repoAnalysisCacheImport, 0, len(paths))
	for _, path := range paths {
		out = append(out, repoAnalysisCacheImportFor(importsByPath[path]))
	}

	return out
}

func repoAnalysisCacheImportFor(pkg *types.Package) repoAnalysisCacheImport {
	if pkg == nil {
		return repoAnalysisCacheImport{}
	}

	out := repoAnalysisCacheImport{
		Path: pkg.Path(),
		Name: pkg.Name(),
	}

	scope := pkg.Scope()
	if scope == nil {
		return out
	}

	names := scope.Names()

	out.Objects = make([]string, 0, len(names))
	for _, name := range names {
		obj := scope.Lookup(name)
		if obj == nil {
			continue
		}

		out.Objects = append(out.Objects, types.ObjectString(obj, cacheImportQualifier))
	}

	return out
}

func cacheImportQualifier(pkg *types.Package) string {
	if pkg == nil {
		return ""
	}

	return pkg.Path()
}

func analysisCacheExecutableStamp() analysisCacheExecutable {
	path, err := os.Executable()
	if err != nil {
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

func repoAnalysisCacheFiles(pkgs []*LoadedPackage) ([]analysisCacheFile, error) {
	seen := make(map[string]struct{})
	files := make([]analysisCacheFile, 0)

	for _, pkg := range pkgs {
		if pkg == nil {
			continue
		}

		for _, file := range pkg.Files {
			tokenFile := pkg.FSet.File(file.Package)
			if tokenFile == nil {
				return nil, errors.New("missing token file for package file")
			}

			name := tokenFile.Name()
			if _, ok := seen[name]; ok {
				continue
			}

			seen[name] = struct{}{}

			content, err := os.ReadFile(name)
			if err != nil {
				return nil, err
			}

			sum := sha256.Sum256(content)
			files = append(files, analysisCacheFile{
				Filename: name,
				SHA256:   hex.EncodeToString(sum[:]),
			})
		}
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
