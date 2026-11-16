package smells

import (
	"fmt"
	"go/ast"
	"go/token"
)

const (
	privateHelperMaxBodyStmts = 3
	privateHelperMaxParams    = 2
)

// singleUseHelperCheck reports tiny private functions with one production use.
type singleUseHelperCheck struct {
	ruleEnv
}

func (l singleUseHelperCheck) check() {
	useCounts := l.productionPackageFuncUseCounts()

	for _, fn := range l.pkg.ProductionFuncs {
		if !l.isSingleUseHelper(fn, useCounts) {
			continue
		}

		l.report(
			fn.Name.Pos(),
			"abstraction_overkill",
			fmt.Sprintf(
				`private helper %q has one production use and a tiny body; inline or give it a stronger role`,
				fn.Name.Name,
			),
		)

		reportGenericNameForHelper(l.findingSink, fn, "one tiny use")
	}
}

func (l singleUseHelperCheck) isSingleUseHelper(
	fn *ast.FuncDecl,
	useCounts map[string]int,
) bool {
	if fn.Recv != nil || funcParamCount(fn.Type.Params) > privateHelperMaxParams {
		return false
	}

	obj, ok := privateFuncObject(l.pkg.TypesInfo, fn)
	if !ok || useCounts[funcObjectKey(obj)] != 1 {
		return false
	}

	if len(fn.Body.List) == 0 || len(fn.Body.List) > privateHelperMaxBodyStmts {
		return false
	}

	if _, forwards := forwardedBodyCall(l.pkg.TypesInfo, fn, obj); forwards {
		return false
	}

	return l.bodyIsTiny(fn)
}

func (l singleUseHelperCheck) bodyIsTiny(fn *ast.FuncDecl) bool {
	if l.hasAttachedComment(fn.Body, fn.Doc) {
		return false
	}

	for _, stmt := range fn.Body.List {
		if l.hasAttachedComment(stmt, fn.Doc) || !privateHelperStmtIsTiny(stmt) {
			return false
		}
	}

	return true
}

func privateHelperStmtIsTiny(stmt ast.Stmt) bool {
	if privateHelperStmtHasComplexNode(stmt) {
		return false
	}

	switch stmt := stmt.(type) {
	case *ast.ExprStmt, *ast.AssignStmt, *ast.SendStmt, *ast.IncDecStmt, *ast.ReturnStmt:
		return true
	case *ast.DeclStmt:
		decl, ok := stmt.Decl.(*ast.GenDecl)
		return ok && decl.Tok == token.VAR
	default:
		return false
	}
}

func privateHelperStmtHasComplexNode(stmt ast.Stmt) bool {
	complex := false

	ast.Inspect(stmt, func(n ast.Node) bool {
		if complex {
			return false
		}

		switch n.(type) {
		case *ast.FuncLit, *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt,
			*ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt,
			*ast.GoStmt, *ast.DeferStmt:
			complex = true
			return false
		default:
			return true
		}
	})

	return complex
}
