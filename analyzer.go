package slopelint

import (
	"os"
	"reflect"
	"strings"

	"example.com/slopelint/internal/lint"
	"golang.org/x/tools/go/analysis"
)

const defaultMaxStates = 32

type analysisResult struct{}

var maxStates = defaultMaxStates
var cacheEnabled = true
var cacheDir string
var experimental bool

// Analyzer reports path-proven redundancy and other low-signal code structure.
var Analyzer = &analysis.Analyzer{
	Name:       "slopelint",
	Doc:        "report path-proven redundancy and other low-signal code structure",
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
	value := strings.TrimSpace(os.Getenv("SLOPELINT_CACHE"))
	if value == "" {
		value = strings.TrimSpace(os.Getenv("DEFENSELINT_CACHE"))
	}

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

	dir := strings.TrimSpace(os.Getenv("SLOPELINT_CACHE_DIR"))
	if dir != "" {
		return dir
	}

	return strings.TrimSpace(os.Getenv("DEFENSELINT_CACHE_DIR"))
}
