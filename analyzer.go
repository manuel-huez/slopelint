package slopelint

import (
	"github.com/manuel-huez/slopelint/internal/lint"
	"golang.org/x/tools/go/analysis"
)

const defaultMaxStates = 32

var maxStates = defaultMaxStates
var cacheEnabled = true

// Analyzer reports path-proven redundancy and other low-signal code structure.
var Analyzer = &analysis.Analyzer{
	Name:      "slopelint",
	Doc:       "report path-proven redundancy and other low-signal code structure",
	FactTypes: lint.AnalysisFactTypes(),
	Run:       run,
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
}

func run(pass *analysis.Pass) (any, error) {
	issues, err := lint.RunAnalysis(pass, lint.Options{
		MaxStates:    maxStates,
		CacheEnabled: cacheEnabled && lint.CacheEnabledFromEnv(),
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

	// analysis.Run uses a nil result when ResultType is unset.
	return nil, nil //nolint:nilnil
}
