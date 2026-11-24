package flow

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"strings"
)

// exprQuery answers read-only syntax and type questions about one package.
// Path analysis and call summaries share it, so they share its render cache.
type exprQuery struct {
	fset          *token.FileSet
	info          *types.Info
	typesPkg      *types.Package
	renderCache   map[ast.Node]string
	localFuncLits map[types.Object]*ast.FuncLit
}

func (q *exprQuery) isBoolExpr(expr ast.Expr) bool {
	return isBoolType(q.info.TypeOf(expr))
}

func (q *exprQuery) symbolOf(expr ast.Expr) (symbol, bool) {
	expr = ast.Unparen(expr)
	switch expr := expr.(type) {
	case *ast.Ident:
		obj := q.info.ObjectOf(expr)
		if _, ok := obj.(*types.Var); !ok {
			return symbol{}, false
		}

		return symbolForObject(obj), true
	case *ast.SelectorExpr:
		sel := q.info.Selections[expr]
		if sel == nil || sel.Kind() != types.FieldVal {
			return symbol{}, false
		}

		base, ok := q.symbolOf(expr.X)
		if !ok {
			return symbol{}, false
		}

		return base.child(expr.Sel.Name, sel.Type()), true
	default:
		return symbol{}, false
	}
}

func (q *exprQuery) rootsInExpr(expr ast.Expr) map[string]struct{} {
	roots := make(map[string]struct{})

	ast.Inspect(expr, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case ast.Expr:
			if sym, ok := q.symbolOf(n); ok {
				roots[sym.root] = struct{}{}
			}
		}

		return true
	})

	return roots
}

func (q *exprQuery) callNeverReturns(call *ast.CallExpr) bool {
	id, ok := ast.Unparen(call.Fun).(*ast.Ident)
	if !ok {
		return false
	}

	obj, ok := q.info.ObjectOf(id).(*types.Builtin)

	return ok && obj.Name() == panicText
}

func (q *exprQuery) render(node ast.Node) string {
	if s, ok := q.renderCache[node]; ok {
		return s
	}

	s := renderNode(q.fset, node)
	q.renderCache[node] = s

	return s
}

func (q *exprQuery) renderCaseClauseHeader(clause *ast.CaseClause) string {
	if len(clause.List) == 0 {
		return "default"
	}

	parts := make([]string, 0, len(clause.List))
	for _, expr := range clause.List {
		parts = append(parts, q.render(expr))
	}

	return strings.Join(parts, ", ")
}

func (q *exprQuery) becauseText(ev evidence) string {
	pos := q.fset.Position(ev.pos)
	if pos.IsValid() {
		return fmt.Sprintf(
			"reachable paths already establish %q at %s:%d",
			ev.text,
			pos.Filename,
			pos.Line,
		)
	}

	return fmt.Sprintf("reachable paths already establish %q", ev.text)
}
