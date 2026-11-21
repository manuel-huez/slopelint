// Package astutil holds Go syntax queries that several check packages share.
package astutil

import (
	"go/ast"
	"go/types"
)

// UsesObject reports whether node contains an identifier that resolves to
// target.
func UsesObject(node ast.Node, target types.Object, info *types.Info) bool {
	found := false

	ast.Inspect(node, func(n ast.Node) bool {
		if ident, ok := n.(*ast.Ident); ok && info.ObjectOf(ident) == target {
			found = true
		}

		return !found
	})

	return found
}

// HasCall reports whether evaluating expr calls a function. Calls inside a
// function literal run only when the literal is called, so they do not count.
func HasCall(expr ast.Expr) bool {
	found := false

	ast.Inspect(expr, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.CallExpr:
			found = true
		}

		return !found
	})

	return found
}
