package deadcode

import (
	"go/ast"
	"go/token"
	"testing"
)

func TestDeadCodeCallContextFuncSkipsBodylessDeclarations(t *testing.T) {
	context := &ast.FuncDecl{
		Body: &ast.BlockStmt{Lbrace: token.Pos(1), Rbrace: token.Pos(10)},
	}
	linter := newPackageLinter(&Package{
		ProductionFuncs: []*ast.FuncDecl{nil, {}, context},
	})
	call := &ast.CallExpr{Fun: &ast.Ident{NamePos: token.Pos(5)}}

	if got := linter.deadCodeCallContextFunc(call); got != context {
		t.Fatalf("context func = %p, want %p", got, context)
	}
}
