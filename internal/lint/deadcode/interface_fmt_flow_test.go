package deadcode

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestWalkFmtStringerFlowScansSimpleStatementsOnce(t *testing.T) {
	t.Parallel()

	file, err := parser.ParseFile(token.NewFileSet(), "sample.go", `package sample

func f() int {
	value := call()
	consume(value)
	return value
}
`, 0)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}

	fn := file.Decls[0].(*ast.FuncDecl)
	exprScans := 0

	walkFmtStringerFlowBlock(struct{}{}, fn.Body.List, fmtStringerFlowOps[struct{}]{
		expr: func(struct{}, ast.Node) {
			exprScans++
		},
		assign: func(struct{}, []ast.Expr, []ast.Expr) {},
		empty:  func() struct{} { return struct{}{} },
		clone:  func(state struct{}) struct{} { return state },
		merge:  func(state struct{}, _ struct{}) struct{} { return state },
	})

	if exprScans != 3 {
		t.Fatalf("expression scans = %d, want 3", exprScans)
	}
}
