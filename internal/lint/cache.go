package lint

import (
	"errors"
	"path/filepath"

	"golang.org/x/tools/go/analysis"
)

const analysisCacheSchema = 2

const cacheDirPerm = 0o755

var errAnalysisCacheDisabled = errors.New("analysis cache disabled")

type analysisCache struct {
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
