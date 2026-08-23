package lint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
