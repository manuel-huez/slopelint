package flow

import (
	"go/ast"
	"go/types"
)

// localFuncLitIndex maps function variables to the one literal they ever hold.
// A variable assigned anything else, or two different literals, is ambiguous
// and stays out of the map.
type localFuncLitIndex struct {
	info      *types.Info
	lits      map[types.Object]*ast.FuncLit
	ambiguous map[types.Object]struct{}
}

func collectLocalFuncLits(files []*ast.File, info *types.Info) map[types.Object]*ast.FuncLit {
	index := localFuncLitIndex{
		info:      info,
		lits:      make(map[types.Object]*ast.FuncLit),
		ambiguous: make(map[types.Object]struct{}),
	}

	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.AssignStmt:
				index.recordPairs(n.Lhs, n.Rhs)
			case *ast.ValueSpec:
				names := make([]ast.Expr, 0, len(n.Names))
				for _, name := range n.Names {
					names = append(names, name)
				}

				index.recordPairs(names, n.Values)
			}

			return true
		})
	}

	return index.lits
}

func (index localFuncLitIndex) recordPairs(lhs []ast.Expr, rhs []ast.Expr) {
	if len(lhs) != len(rhs) {
		return
	}

	for idx := range lhs {
		index.record(lhs[idx], rhs[idx])
	}
}

func (index localFuncLitIndex) record(lhs ast.Expr, rhs ast.Expr) {
	name, ok := ast.Unparen(lhs).(*ast.Ident)
	if !ok {
		return
	}

	obj, ok := index.info.ObjectOf(name).(*types.Var)
	if !ok {
		return
	}

	if _, ok := types.Unalias(obj.Type()).Underlying().(*types.Signature); !ok {
		return
	}

	if _, ok := index.ambiguous[obj]; ok {
		return
	}

	lit, ok := ast.Unparen(rhs).(*ast.FuncLit)
	if prev, seen := index.lits[obj]; !ok || (seen && prev != lit) {
		index.ambiguous[obj] = struct{}{}
		delete(index.lits, obj)

		return
	}

	index.lits[obj] = lit
}

func (q *exprQuery) localFuncLitForExpr(expr ast.Expr) (*ast.FuncLit, bool) {
	ident, ok := ast.Unparen(expr).(*ast.Ident)
	if !ok {
		return nil, false
	}

	lit, ok := q.localFuncLits[q.info.ObjectOf(ident)]

	return lit, ok
}
