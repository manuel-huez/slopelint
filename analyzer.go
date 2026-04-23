package defenselint

import (
	"reflect"

	"example.com/defenselint/internal/lint"
	"golang.org/x/tools/go/analysis"
)

const defaultMaxStates = 32

type analysisResult struct{}

var maxStates = defaultMaxStates

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
}

func run(pass *analysis.Pass) (any, error) {
	issues, err := lint.RunAnalysis(pass, lint.Options{MaxStates: maxStates})
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
