package lint

import (
	"go/ast"
	"go/types"
	"strings"
)

func (l *linter) symbolScalar(symExpr, scalarExpr ast.Expr) (symbol, scalar, bool) {
	sym, ok := l.valueSymbolOf(symExpr)
	if !ok {
		return symbol{}, scalar{}, false
	}

	value, ok := l.scalarOf(scalarExpr)
	if !ok {
		return symbol{}, scalar{}, false
	}

	return sym, value, true
}

func (l *linter) valueSymbolOf(expr ast.Expr) (symbol, bool) {
	if sym, ok := l.symbolOf(expr); ok {
		return sym, true
	}

	if sym, ok := l.lenSymbolOf(expr); ok {
		return sym, true
	}

	return l.predicateCallSymbolOf(expr)
}

func (l *linter) scalarOf(expr ast.Expr) (scalar, bool) {
	expr = l.unparen(expr)
	if id, ok := expr.(*ast.Ident); ok && id.Name == nilText {
		return scalar{kind: scalarNil, text: nilText}, true
	}

	if l.isRuntimeTargetConstant(expr) {
		return scalar{}, false
	}

	if tv, ok := l.pkg.TypesInfo.Types[expr]; ok {
		if tv.Value != nil {
			if l.containsRuntimeTargetConstant(expr) {
				return scalar{}, false
			}

			return scalarFromConstantValue(tv.Value)
		}

		if tv.IsNil() {
			return scalar{kind: scalarNil, text: nilText}, true
		}
	}

	return scalar{}, false
}

func (l *linter) isRuntimeTargetConstant(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}

	obj, ok := l.pkg.TypesInfo.Uses[sel.Sel].(*types.Const)
	if !ok || obj.Pkg() == nil || obj.Pkg().Path() != "runtime" {
		return false
	}

	return obj.Name() == "GOOS" || obj.Name() == "GOARCH"
}

func (l *linter) containsRuntimeTargetConstant(expr ast.Expr) bool {
	found := false

	ast.Inspect(expr, func(n ast.Node) bool {
		if found || n == nil {
			return false
		}

		expr, ok := n.(ast.Expr)
		if !ok {
			return true
		}

		if l.isRuntimeTargetConstant(l.unparen(expr)) {
			found = true
			return false
		}

		return true
	})

	return found
}

func (l *linter) lenSymbolOf(expr ast.Expr) (symbol, bool) {
	call, ok := l.unparen(expr).(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return symbol{}, false
	}

	id, ok := l.unparen(call.Fun).(*ast.Ident)
	if !ok || id.Name != "len" {
		return symbol{}, false
	}

	obj, ok := l.pkg.TypesInfo.ObjectOf(id).(*types.Builtin)
	if !ok || obj == nil || obj.Name() != "len" {
		return symbol{}, false
	}

	base, ok := l.symbolOf(call.Args[0])
	if !ok {
		return symbol{}, false
	}

	return lenSymbolForBase(base), true
}

func (l *linter) predicateCallSymbolOf(expr ast.Expr) (symbol, bool) {
	call, ok := l.unparen(expr).(*ast.CallExpr)
	if !ok || len(call.Args) != 0 || !isBoolType(l.pkg.TypesInfo.TypeOf(call)) {
		return symbol{}, false
	}

	obj, key, ok := l.calledFunc(call)
	if !ok || obj == nil || !isIsPredicateName(obj.Name()) {
		return symbol{}, false
	}

	sel, ok := l.unparen(call.Fun).(*ast.SelectorExpr)
	if !ok || l.pkg.TypesInfo.Selections[sel] == nil {
		return symbol{}, false
	}

	receiver, ok := l.symbolOf(sel.X)
	if !ok {
		return symbol{}, false
	}

	return symbol{
		key:  receiver.key + "|" + predicatePathSegmentPrefix + strings.ReplaceAll(key, "|", "/"),
		root: receiver.root,
		name: l.render(call),
		typ:  l.pkg.TypesInfo.TypeOf(call),
	}, true
}
