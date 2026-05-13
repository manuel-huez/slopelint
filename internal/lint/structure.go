package lint

import (
	structurecheck "example.com/slopelint/internal/lint/structure"

	"go/ast"
)

func (l *linter) scanStructuralFunction(fnType *ast.FuncType, body *ast.BlockStmt) {
	for _, finding := range l.structureScanner().ScanFunctionBody(
		body,
		funcTypeHasResults(fnType),
	) {
		l.report(finding.Pos, finding.Kind, finding.Message)
	}
}

func (l *linter) structureScanner() *structurecheck.Runner {
	if l.structureRunner == nil {
		l.structureRunner = structurecheck.New(&structurecheck.Package{
			Files:     l.pkg.Files,
			FSet:      l.pkg.FSet,
			TypesPkg:  l.pkg.TypesPkg,
			TypesInfo: l.pkg.TypesInfo,
		})
	}

	return l.structureRunner
}

func funcTypeHasResults(fnType *ast.FuncType) bool {
	return fnType != nil && fnType.Results != nil && len(fnType.Results.List) != 0
}
