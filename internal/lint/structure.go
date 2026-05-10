package lint

import (
	structurecheck "example.com/slopelint/internal/lint/structure"

	"go/ast"
)

func (l *linter) scanStructuralBlock(stmts []ast.Stmt) {
	for _, finding := range l.structureScanner().ScanBlock(stmts) {
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
