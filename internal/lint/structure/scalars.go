package structure

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"strconv"
)

type scalarKind = uint8

const scalarNil scalarKind = 1
const scalarBool scalarKind = 2
const scalarString scalarKind = 3
const scalarInt scalarKind = 4

type scalar struct {
	text string
	kind scalarKind
}

func (l *Runner) scalarOf(expr ast.Expr) (scalar, bool) {
	clean := l.unparen(expr)
	if value, ok := nilLiteralScalar(clean); ok {
		return value, true
	}

	tv, ok := l.pkg.TypesInfo.Types[clean]

	return scalarFromTypeValue(tv, ok)
}

func nilLiteralScalar(expr ast.Expr) (scalar, bool) {
	ident, ok := expr.(*ast.Ident)
	if ok && ident.Name == nilText {
		return newScalar(scalarNil, nilText), true
	}

	return scalar{}, false
}

func scalarFromTypeValue(tv types.TypeAndValue, ok bool) (scalar, bool) {
	if !ok {
		return scalar{}, false
	}

	if tv.IsNil() {
		return newScalar(scalarNil, nilText), true
	}

	if tv.Value == nil {
		return scalar{}, false
	}

	return scalarFromConstantValue(tv.Value)
}

func scalarFromConstantValue(value constant.Value) (scalar, bool) {
	if value == nil {
		return scalar{}, false
	}

	//exhaustive:ignore structural checks only use bool, string, and integer constants.
	switch value.Kind() {
	case constant.Bool:
		return newScalar(scalarBool, strconv.FormatBool(constant.BoolVal(value))), true
	case constant.String:
		return newScalar(scalarString, constant.StringVal(value)), true
	case constant.Int:
		return newScalar(scalarInt, value.ExactString()), true
	default:
		return scalar{}, false
	}
}

func newScalar(kind scalarKind, text string) scalar {
	return scalar{text: text, kind: kind}
}

func scalarIntValue(value scalar) (int64, bool) {
	if value.kind != scalarInt {
		return 0, false
	}

	out, err := strconv.ParseInt(value.text, 10, 64)

	return out, err == nil
}

func reverseOrderedOp(op token.Token) token.Token {
	opposites := map[token.Token]token.Token{
		token.GTR: token.LSS,
		token.GEQ: token.LEQ,
		token.LSS: token.GTR,
		token.LEQ: token.GEQ,
	}

	if reversed, ok := opposites[op]; ok {
		return reversed
	}

	return op
}
