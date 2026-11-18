package structure

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
)

func constValue(info *types.Info, expr ast.Expr) constant.Value {
	return info.Types[ast.Unparen(expr)].Value
}

func constBool(info *types.Info, expr ast.Expr) (bool, bool) {
	value := constValue(info, expr)
	if value == nil || value.Kind() != constant.Bool {
		return false, false
	}

	return constant.BoolVal(value), true
}

func constInt(info *types.Info, expr ast.Expr) (int64, bool) {
	value := constValue(info, expr)
	if value == nil || value.Kind() != constant.Int {
		return 0, false
	}

	return constant.Int64Val(value)
}

func isNilValue(info *types.Info, expr ast.Expr) bool {
	return isNilExpr(expr) || info.Types[ast.Unparen(expr)].IsNil()
}

func isNilExpr(expr ast.Expr) bool {
	id, ok := ast.Unparen(expr).(*ast.Ident)

	return ok && id.Name == nilText
}

func isNilOrScalarConst(info *types.Info, expr ast.Expr) bool {
	if isNilValue(info, expr) {
		return true
	}

	value := constValue(info, expr)
	if value == nil {
		return false
	}

	kind := value.Kind()

	return kind == constant.Bool || kind == constant.String || kind == constant.Int
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
