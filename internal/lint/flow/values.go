package flow

import (
	"go/ast"
	"go/types"
	"strings"

	smellcheck "github.com/manuel-huez/slopelint/internal/lint/smells"
)

func (q *exprQuery) symbolScalar(symExpr, scalarExpr ast.Expr) (symbol, Scalar, bool) {
	sym, ok := q.valueSymbolOf(symExpr)
	if !ok {
		return symbol{}, Scalar{}, false
	}

	value, ok := q.scalarOf(scalarExpr)
	if !ok {
		return symbol{}, Scalar{}, false
	}

	return sym, value, true
}

func (q *exprQuery) valueSymbolOf(expr ast.Expr) (symbol, bool) {
	if sym, ok := q.symbolOf(expr); ok {
		return sym, true
	}

	if sym, ok := q.lenSymbolOf(expr); ok {
		return sym, true
	}

	return q.predicateCallSymbolOf(expr)
}

func (q *exprQuery) scalarOf(expr ast.Expr) (Scalar, bool) {
	expr = ast.Unparen(expr)
	if id, ok := expr.(*ast.Ident); ok && id.Name == nilText {
		return Scalar{Kind: ScalarNil, Text: nilText}, true
	}

	if tv, ok := q.info.Types[expr]; ok {
		if tv.Value != nil {
			if q.containsRuntimeTargetConstant(expr) {
				return Scalar{}, false
			}

			return scalarFromConstantValue(tv.Value)
		}

		if tv.IsNil() {
			return Scalar{Kind: ScalarNil, Text: nilText}, true
		}
	}

	return Scalar{}, false
}

// containsRuntimeTargetConstant reports runtime.GOOS or runtime.GOARCH in
// expr. Their values depend on the build target, so they are not facts.
func (q *exprQuery) containsRuntimeTargetConstant(expr ast.Expr) bool {
	found := false

	ast.Inspect(expr, func(n ast.Node) bool {
		if found || n == nil {
			return false
		}

		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}

		obj, ok := q.info.Uses[sel.Sel].(*types.Const)
		found = ok && obj.Pkg() != nil && obj.Pkg().Path() == "runtime" &&
			(obj.Name() == "GOOS" || obj.Name() == "GOARCH")

		return !found
	})

	return found
}

func (q *exprQuery) lenSymbolOf(expr ast.Expr) (symbol, bool) {
	call, ok := ast.Unparen(expr).(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return symbol{}, false
	}

	id, ok := ast.Unparen(call.Fun).(*ast.Ident)
	if !ok || id.Name != "len" {
		return symbol{}, false
	}

	if _, ok := q.info.ObjectOf(id).(*types.Builtin); !ok {
		return symbol{}, false
	}

	base, ok := q.symbolOf(call.Args[0])
	if !ok {
		return symbol{}, false
	}

	return lenSymbolForBase(base), true
}

func (q *exprQuery) predicateCallSymbolOf(expr ast.Expr) (symbol, bool) {
	call, ok := ast.Unparen(expr).(*ast.CallExpr)
	if !ok || len(call.Args) != 0 || !isBoolType(q.info.TypeOf(call)) {
		return symbol{}, false
	}

	obj, key, ok := q.calledFunc(call)
	if !ok || !smellcheck.IsPredicateName(obj.Name()) {
		return symbol{}, false
	}

	sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	if !ok || q.info.Selections[sel] == nil {
		return symbol{}, false
	}

	receiver, ok := q.symbolOf(sel.X)
	if !ok {
		return symbol{}, false
	}

	return symbol{
		key:  receiver.key + "|" + predicatePathSegmentPrefix + strings.ReplaceAll(key, "|", "/"),
		root: receiver.root,
		name: q.render(call),
		typ:  q.info.TypeOf(call),
	}, true
}
