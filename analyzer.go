package defenselint

import (
	"path/filepath"
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
	pkg := &lint.LoadedPackage{
		ImportPath: pass.Pkg.Path(),
		Name:       pass.Pkg.Name(),
		Dir:        packageDir(pass),
		FSet:       pass.Fset,
		Files:      pass.Files,
		TypesPkg:   pass.Pkg,
		TypesInfo:  pass.TypesInfo,
	}

	for _, issue := range lint.LintPackage(pkg, lint.Options{MaxStates: maxStates}) {
		pass.Report(analysis.Diagnostic{
			Pos:      issue.Pos,
			Message:  issue.Message,
			Category: issue.Kind,
		})
	}

	return analysisResult{}, nil
}

func packageDir(pass *analysis.Pass) string {
	for _, file := range pass.Files {
		pos := pass.Fset.Position(file.Package)
		if pos.Filename != "" {
			return filepath.Dir(pos.Filename)
		}
	}

	return ""
}
