package lint

import (
	"go/ast"
	"go/types"
)

func (l *linter) symbolOf(expr ast.Expr) (symbol, bool) {
	expr = l.unparen(expr)
	switch expr := expr.(type) {
	case *ast.Ident:
		obj := l.pkg.TypesInfo.ObjectOf(expr)
		if obj == nil {
			return symbol{}, false
		}

		if _, ok := obj.(*types.Var); !ok {
			return symbol{}, false
		}

		return symbolForObject(obj), true
	case *ast.SelectorExpr:
		sel := l.pkg.TypesInfo.Selections[expr]
		if sel == nil || sel.Kind() != types.FieldVal {
			return symbol{}, false
		}

		base, ok := l.symbolOf(expr.X)
		if !ok {
			return symbol{}, false
		}

		return base.child(expr.Sel.Name, sel.Type()), true
	default:
		return symbol{}, false
	}
}

func (l *linter) unparen(expr ast.Expr) ast.Expr {
	for {
		p, ok := expr.(*ast.ParenExpr)
		if !ok {
			return expr
		}

		expr = p.X
	}
}
