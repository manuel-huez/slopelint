package defenselint

import (
	"os"
	"reflect"
	"strings"

	"example.com/defenselint/internal/lint"
	"golang.org/x/tools/go/analysis"
)

const defaultMaxStates = 32

type analysisResult struct{}

var maxStates = defaultMaxStates
var cacheEnabled = true
var cacheDir string
var experimental bool

// Analyzer reports defensive checks already implied by current control-flow path.
var Analyzer = &analysis.Analyzer{
	Name:       "defenselint",
	Doc:        "report defensive checks that are already impossible or guaranteed",
	FactTypes:  lint.AnalysisFactTypes(),
	Run:        run,
	ResultType: reflect.TypeFor[analysisResult](),
}

func init() {
	Analyzer.Flags.IntVar(
		&maxStates,
		"max-states",
		defaultMaxStates,
		"maximum number of symbolic states before widening",
	)
	Analyzer.Flags.BoolVar(
		&cacheEnabled,
		"cache",
		true,
		"reuse cached analysis for unchanged packages",
	)
	Analyzer.Flags.StringVar(
		&cacheDir,
		"cache-dir",
		"",
		"directory for persistent analysis cache",
	)
	Analyzer.Flags.BoolVar(
		&experimental,
		"experimental",
		false,
		"enable experimental smell rules",
	)
}

func run(pass *analysis.Pass) (any, error) {
	issues, err := lint.RunAnalysis(pass, lint.Options{
		MaxStates:    maxStates,
		CacheEnabled: cacheEnabled && cacheEnvEnabled(),
		CacheDir:     resolvedCacheDir(),
		Experimental: experimental,
	})
	if err != nil {
		return nil, err
	}

	for _, issue := range issues {
		pass.Report(analysis.Diagnostic{
			Pos:      issue.Pos,
			Message:  issue.Message,
			Category: issue.Kind,
		})
	}

	return analysisResult{}, nil
}

func cacheEnvEnabled() bool {
	value := strings.TrimSpace(os.Getenv("DEFENSELINT_CACHE"))
	if value == "" {
		return true
	}

	switch strings.ToLower(value) {
	case "0", "false", "off", "no":
		return false
	default:
		return true
	}
}

func resolvedCacheDir() string {
	if cacheDir != "" {
		return cacheDir
	}

	return strings.TrimSpace(os.Getenv("DEFENSELINT_CACHE_DIR"))
}
