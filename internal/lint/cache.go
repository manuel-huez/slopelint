package lint

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/analysis"
)

const analysisCacheSchema = 3

const cacheDirPerm = 0o755

var errAnalysisCacheDisabled = errors.New("analysis cache disabled")

type analysisCache struct {
	path string
}

type repoAnalysisCache struct {
	path string
}

type analysisCacheEntry struct {
	Issues  []analysisCacheIssue  `json:"issues"`
	Exports []analysisCacheExport `json:"exports"`
}

type analysisCacheIssue struct {
	Filename string `json:"filename"`
	Offset   int    `json:"offset"`
	Kind     string `json:"kind"`
	Message  string `json:"message"`
}

type analysisCacheExport struct {
	FuncKey string          `json:"func_key"`
	Fact    callSummaryFact `json:"fact"`
}

type analysisCacheFingerprint struct {
	Schema        int                         `json:"schema"`
	Package       string                      `json:"package"`
	MaxStates     int                         `json:"max_states"`
	Executable    analysisCacheExecutable     `json:"executable"`
	Files         []analysisCacheFile         `json:"files"`
	ImportedFacts []analysisCacheImportedFact `json:"imported_facts"`
}

type repoAnalysisCachePackage struct {
	ImportPath string `json:"import_path"`
	Name       string `json:"name"`
}

type repoAnalysisCacheImport struct {
	Path    string   `json:"path"`
	Name    string   `json:"name"`
	Objects []string `json:"objects"`
}

type analysisCacheExecutable struct {
	Path             string `json:"path"`
	Size             int64  `json:"size"`
	ModTimeUnixNanos int64  `json:"mod_time_unix_nanos"`
}

type analysisCacheFile struct {
	Filename string `json:"filename"`
	SHA256   string `json:"sha256"`
}

type analysisCacheImportedFact struct {
	FuncKey string          `json:"func_key"`
	Fact    callSummaryFact `json:"fact"`
}

func newAnalysisCache(
	pass *analysis.Pass,
	pkg *LoadedPackage,
	opts Options,
) (*analysisCache, error) {
	if !opts.CacheEnabled {
		return nil, errAnalysisCacheDisabled
	}

	root, err := analysisCacheRoot(opts.CacheDir)
	if err != nil {
		return nil, err
	}

	key, err := analysisCacheKey(pass, pkg, opts)
	if err != nil {
		return nil, err
	}

	return &analysisCache{
		path: filepath.Join(root, key[:2], key[2:]+".json"),
	}, nil
}

func newRepoAnalysisCache(
	pkgs []*LoadedPackage,
	opts Options,
) (*repoAnalysisCache, error) {
	if !opts.CacheEnabled {
		return nil, errAnalysisCacheDisabled
	}

	root, err := analysisCacheRoot(opts.CacheDir)
	if err != nil {
		return nil, err
	}

	key, err := repoAnalysisCacheKey(pkgs, opts)
	if err != nil {
		return nil, err
	}

	return &repoAnalysisCache{
		path: filepath.Join(root, "repo", key[:2], key[2:]+".json"),
	}, nil
}

// CacheEnabledFromEnv reports whether SLOPELINT_CACHE leaves persistent cache enabled.
func CacheEnabledFromEnv() bool {
	value := strings.TrimSpace(os.Getenv("SLOPELINT_CACHE"))
	if value == "" {
		return true
	}

	switch strings.ToLower(value) {
	case zeroIntText, boolFalseText, "off", "no":
		return false
	default:
		return true
	}
}

// ResolveCacheDir returns explicit cache dir, or SLOPELINT_CACHE_DIR when set.
func ResolveCacheDir(dir string) string {
	if dir != "" {
		return dir
	}

	return strings.TrimSpace(os.Getenv("SLOPELINT_CACHE_DIR"))
}
