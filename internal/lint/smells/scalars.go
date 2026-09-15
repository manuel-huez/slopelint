package smells

import (
	"go/ast"
	"go/constant"
	"go/types"
)

type scalarKind uint8

const (
	scalarInvalid scalarKind = iota
	scalarNil
	scalarBool
	scalarString
	scalarInt
)

type scalar struct {
	kind scalarKind
	text string
}

func (l *Runner) scalarOf(expr ast.Expr) (scalar, bool) {
	expr = ast.Unparen(expr)

	if ident, ok := expr.(*ast.Ident); ok && ident.Name == nilText {
		return scalar{kind: scalarNil, text: nilText}, true
	}

	if l.runtimeTargetConstIn(expr) {
		return scalar{}, false
	}

	tv, ok := l.pkg.TypesInfo.Types[expr]
	if !ok {
		return scalar{}, false
	}

	if tv.IsNil() {
		return scalar{kind: scalarNil, text: nilText}, true
	}

	return scalarFromConstantValue(tv.Value)
}

func scalarFromConstantValue(value constant.Value) (scalar, bool) {
	if value == nil || value.Kind() == constant.Unknown {
		return scalar{}, false
	}

	out := scalar{kind: scalarInvalid}
	ok := true

	//exhaustive:ignore unsupported numeric kinds are intentionally not scalar facts.
	switch value.Kind() {
	case constant.Bool:
		out.kind = scalarBool
		out.text = boolFalseText

		if constant.BoolVal(value) {
			out.text = boolTrueText
		}
	case constant.String:
		out.kind, out.text = scalarString, constant.StringVal(value)
	case constant.Int:
		out.kind, out.text = scalarInt, value.ExactString()
	default:
		ok = false
	}

	return out, ok
}

func (l *Runner) runtimeTargetConstIn(expr ast.Expr) bool {
	found := false

	ast.Inspect(expr, func(n ast.Node) bool {
		if found || n == nil {
			return false
		}

		child, ok := l.unparenExprNode(n)
		if ok && l.isRuntimeTargetConst(child) {
			found = true
		}

		return !found
	})

	return found
}

func (l *Runner) unparenExprNode(n ast.Node) (ast.Expr, bool) {
	expr, ok := n.(ast.Expr)
	if !ok {
		return nil, false
	}

	return ast.Unparen(expr), true
}

func (l *Runner) isRuntimeTargetConst(expr ast.Expr) bool {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}

	constObj, _ := l.pkg.TypesInfo.Uses[selector.Sel].(*types.Const)

	return constObj != nil &&
		constObj.Pkg() != nil &&
		constObj.Pkg().Path() == "runtime" &&
		(constObj.Name() == "GOOS" || constObj.Name() == "GOARCH")
}
