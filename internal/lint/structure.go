package lint

import (
	structurecheck "github.com/manuel-huez/slopelint/internal/lint/structure"

	"go/ast"
)

func (l *linter) scanStructuralFunction(
	fnType *ast.FuncType,
	recv *ast.FieldList,
	body *ast.BlockStmt,
) {
	for _, finding := range l.structureScanner().ScanFunctionBody(
		fnType,
		recv,
		body,
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
