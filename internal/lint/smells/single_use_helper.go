package smells

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
)

const (
	privateHelperMaxBodyStmts = 3
	privateHelperMaxParams    = 2
)

func (l *Runner) checkSingleUsePrivateHelpers() {
	useCounts := l.productionPackageFuncUseCounts()

	l.forEachProductionFunc(func(fn *ast.FuncDecl) {
		if !l.isSingleUsePrivateHelper(fn, useCounts) {
			return
		}

		l.report(
			fn.Name.Pos(),
			"abstraction_overkill",
			fmt.Sprintf(
				`private helper %q has one production use and a tiny body; inline or give it a stronger role`,
				fn.Name.Name,
			),
		)

		l.reportGenericNameForSingleUseHelper(fn)
	})
}

func (l *Runner) isSingleUsePrivateHelper(
	fn *ast.FuncDecl,
	useCounts map[string]int,
) bool {
	if !isEligiblePrivateSmellFunc(fn) || fn.Name.Name == initFuncName {
		return false
	}

	if funcParamCount(fn.Type.Params) > privateHelperMaxParams {
		return false
	}

	obj, ok := l.pkg.TypesInfo.ObjectOf(fn.Name).(*types.Func)
	if !ok || obj == nil || useCounts[funcObjectKey(obj)] != 1 {
		return false
	}

	if len(fn.Body.List) == 0 || len(fn.Body.List) > privateHelperMaxBodyStmts {
		return false
	}

	if l.privateHelperOverlapsTrivialForwarder(fn, obj) {
		return false
	}

	return l.privateHelperBodyIsTiny(fn.Body)
}

func (l *Runner) privateHelperOverlapsTrivialForwarder(fn *ast.FuncDecl, obj *types.Func) bool {
	call, ok := l.trivialForwarderBodyCall(fn, obj)
	if !ok {
		return false
	}

	return l.validForwardTarget(obj, call)
}

func (l *Runner) privateHelperBodyIsTiny(body *ast.BlockStmt) bool {
	if l.hasAttachedComment(body) {
		return false
	}

	for _, stmt := range body.List {
		if l.hasAttachedComment(stmt) || !privateHelperStmtIsTiny(stmt) {
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
