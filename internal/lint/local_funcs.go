package lint

import (
	"go/ast"
	"go/types"
)

func (l *linter) collectLocalFuncLits() {
	l.localFuncLits = make(map[types.Object]*ast.FuncLit)

	ambiguous := make(map[types.Object]struct{})

	for _, file := range l.pkg.Files {
		l.collectLocalFuncLitsFromFile(file, ambiguous)
	}
}

func (l *linter) collectLocalFuncLitsFromFile(
	file *ast.File,
	ambiguous map[types.Object]struct{},
) {
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			l.recordLocalFuncLitPairs(n.Lhs, n.Rhs, ambiguous)
		case *ast.ValueSpec:
			names := make([]ast.Expr, 0, len(n.Names))
			for _, name := range n.Names {
				names = append(names, name)
			}

			l.recordLocalFuncLitPairs(names, n.Values, ambiguous)
		}

		return true
	})
}

func (l *linter) recordLocalFuncLitPairs(
	lhs []ast.Expr,
	rhs []ast.Expr,
	ambiguous map[types.Object]struct{},
) {
	if len(lhs) != len(rhs) {
		return
	}

	for idx := range lhs {
		l.recordLocalFuncLit(lhs[idx], rhs[idx], ambiguous)
	}
}

func (l *linter) recordLocalFuncLit(
	lhs ast.Expr,
	rhs ast.Expr,
	ambiguous map[types.Object]struct{},
) {
	obj, ok := l.funcVarForExpr(lhs)
	if !ok {
		return
	}

	if _, ok := ambiguous[obj]; ok {
		return
	}

	lit, ok := l.unparen(rhs).(*ast.FuncLit)
	if !ok {
		l.markLocalFuncLitAmbiguous(obj, ambiguous)

		return
	}

	if prev, ok := l.localFuncLits[obj]; ok && prev != lit {
		l.markLocalFuncLitAmbiguous(obj, ambiguous)

		return
	}

	l.localFuncLits[obj] = lit
}

func (l *linter) funcVarForExpr(expr ast.Expr) (*types.Var, bool) {
	name, ok := l.unparen(expr).(*ast.Ident)
	if !ok {
		return nil, false
	}

	obj, ok := l.pkg.TypesInfo.ObjectOf(name).(*types.Var)
	if !ok || obj == nil || !isFuncTypedObject(obj) {
		return nil, false
	}

	return obj, true
}

func (l *linter) markLocalFuncLitAmbiguous(
	obj types.Object,
	ambiguous map[types.Object]struct{},
) {
	ambiguous[obj] = struct{}{}
	delete(l.localFuncLits, obj)
}

func (l *linter) localFuncLitForExpr(expr ast.Expr) (*ast.FuncLit, bool) {
	ident, ok := l.unparen(expr).(*ast.Ident)
	if !ok {
		return nil, false
	}

	obj := l.pkg.TypesInfo.ObjectOf(ident)
	if obj == nil {
		return nil, false
	}

	lit, ok := l.localFuncLits[obj]

	return lit, ok
}

func isFuncTypedObject(obj types.Object) bool {
	if obj == nil {
		return false
	}

	_, ok := types.Unalias(obj.Type()).Underlying().(*types.Signature)

	return ok
}
