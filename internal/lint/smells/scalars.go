package smells

import (
	"go/ast"
	"go/constant"
	"go/types"
	"strconv"
	"strings"
)

type symbol struct {
	key  string
	name string
	typ  types.Type
}

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

func (l *Runner) symbolScalar(symExpr, scalarExpr ast.Expr) (symbol, scalar, bool) {
	sym, symOK := l.valueSymbolOf(symExpr)
	value, scalarOK := l.scalarOf(scalarExpr)

	return sym, value, symOK && scalarOK
}

func (l *Runner) valueSymbolOf(expr ast.Expr) (symbol, bool) {
	switch {
	case l.isLenCall(expr):
		return l.lenSymbolOf(expr)
	case l.isZeroArgPredicateCall(expr):
		return l.predicateCallSymbolOf(expr)
	default:
		return l.symbolOf(expr)
	}
}

func (l *Runner) symbolOf(expr ast.Expr) (symbol, bool) {
	switch node := l.unparen(expr).(type) {
	case *ast.Ident:
		obj := l.pkg.TypesInfo.ObjectOf(node)
		if _, ok := obj.(*types.Var); !ok {
			return symbol{}, false
		}

		return symbolForObject(obj), true
	case *ast.SelectorExpr:
		selection := l.pkg.TypesInfo.Selections[node]
		if selection == nil || selection.Kind() != types.FieldVal {
			return symbol{}, false
		}

		base, ok := l.symbolOf(node.X)
		if !ok {
			return symbol{}, false
		}

		return symbol{
			key:  base.key + "|" + node.Sel.Name,
			name: base.name + "." + node.Sel.Name,
			typ:  selection.Type(),
		}, true
	default:
		return symbol{}, false
	}
}

func symbolForObject(obj types.Object) symbol {
	var b strings.Builder
	if pkg := obj.Pkg(); pkg != nil {
		b.WriteString(pkg.Path())
	}

	b.WriteByte(':')
	b.WriteString(obj.Name())
	b.WriteByte(':')
	b.WriteString(strconv.Itoa(int(obj.Pos())))

	return symbol{key: b.String(), name: obj.Name(), typ: obj.Type()}
}

func (l *Runner) scalarOf(expr ast.Expr) (scalar, bool) {
	expr = l.unparen(expr)

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

	return l.unparen(expr), true
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

func (l *Runner) isLenCall(expr ast.Expr) bool {
	call, ok := l.unparen(expr).(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return false
	}

	return l.builtinName(call.Fun) == "len"
}

func (l *Runner) lenSymbolOf(expr ast.Expr) (symbol, bool) {
	call := l.unparen(expr).(*ast.CallExpr)

	base, ok := l.symbolOf(call.Args[0])
	if !ok {
		return symbol{}, false
	}

	return symbol{
		key:  base.key + "|" + lenPathSegment,
		name: "len(" + base.name + ")",
		typ:  types.Typ[types.Int],
	}, true
}

func (l *Runner) isZeroArgPredicateCall(expr ast.Expr) bool {
	call, ok := l.unparen(expr).(*ast.CallExpr)
	if !ok || len(call.Args) != 0 || !isBoolType(l.pkg.TypesInfo.TypeOf(call)) {
		return false
	}

	fn, _, ok := l.calledFunc(call)

	return ok && fn != nil && isIsPredicateName(fn.Name())
}

func (l *Runner) predicateCallSymbolOf(expr ast.Expr) (symbol, bool) {
	call := l.unparen(expr).(*ast.CallExpr)

	selector, ok := l.unparen(call.Fun).(*ast.SelectorExpr)
	if !ok || l.pkg.TypesInfo.Selections[selector] == nil {
		return symbol{}, false
	}

	receiver, ok := l.symbolOf(selector.X)
	if !ok {
		return symbol{}, false
	}

	_, key, _ := l.calledFunc(call)

	return symbol{
		key:  receiver.key + "|" + predicatePathSegmentPrefx + strings.ReplaceAll(key, "|", "/"),
		name: l.render(call),
		typ:  l.pkg.TypesInfo.TypeOf(call),
	}, true
}

func (l *Runner) builtinName(expr ast.Expr) string {
	ident, ok := l.unparen(expr).(*ast.Ident)
	if !ok {
		return ""
	}

	obj, _ := l.pkg.TypesInfo.ObjectOf(ident).(*types.Builtin)
	if obj == nil {
		return ""
	}

	return obj.Name()
}
