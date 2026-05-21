package deadcode

import "go/ast"

func flowElementValue(expr ast.Expr) ast.Expr {
	if keyed, ok := unparenReflectedExpr(expr).(*ast.KeyValueExpr); ok {
		return keyed.Value
	}

	return expr
}
