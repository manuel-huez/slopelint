package flow

import (
	"go/ast"
	"go/token"
	"go/types"
)

type prefixInvalidation uint8

const (
	invalidateDescendantsOnly prefixInvalidation = iota + 1
	invalidateFullPrefix
)

const (
	builtinCopy   = "copy"
	builtinClear  = "clear"
	builtinDelete = "delete"
	builtinAppend = "append"
	builtinClose  = "close"
)

type prefixInvalidations map[string]prefixInvalidation

// loopWrites collects every prefix a loop body may write. Facts about them do
// not hold at the start of a later iteration, so loops drop them on entry.
type loopWrites struct {
	q    *exprQuery
	out  prefixInvalidations
	seen map[*ast.FuncLit]struct{}
}

func loopInvalidations(q *exprQuery, body []ast.Stmt, post ast.Stmt) prefixInvalidations {
	writes := loopWrites{
		q:    q,
		out:  make(prefixInvalidations),
		seen: make(map[*ast.FuncLit]struct{}),
	}

	for _, stmt := range body {
		writes.stmt(stmt)
	}

	if post != nil {
		writes.stmt(post)
	}

	if len(writes.out) == 0 {
		return nil
	}

	return writes.out
}

// apply returns states without the invalidated facts; callers normalize.
func (invalidations prefixInvalidations) apply(states []state) []state {
	if len(states) == 0 || len(invalidations) == 0 {
		return states
	}

	out := make([]state, 0, len(states))
	for _, st0 := range states {
		st := st0.clone()

		for prefix, mode := range invalidations {
			if mode == invalidateFullPrefix {
				st.invalidatePrefix(prefix)
				continue
			}

			st.invalidateDescendants(prefix)
		}

		out = append(out, st)
	}

	return out
}

func (invalidations prefixInvalidations) addFull(prefix string) {
	if prefix == "" {
		return
	}

	invalidations[prefix] = invalidateFullPrefix
}

func (invalidations prefixInvalidations) addDescendants(prefix string) {
	if prefix == "" || invalidations[prefix] == invalidateFullPrefix {
		return
	}

	invalidations[prefix] = invalidateDescendantsOnly
}

func (w loopWrites) stmt(stmt ast.Stmt) {
	ast.Inspect(stmt, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.AssignStmt:
			for _, lhs := range n.Lhs {
				w.lhs(lhs)
			}
		case *ast.IncDecStmt:
			w.lhs(n.X)
		case *ast.RangeStmt:
			if n.Key != nil {
				w.lhs(n.Key)
			}

			if n.Value != nil {
				w.lhs(n.Value)
			}
		case *ast.CallExpr:
			w.call(n)
		case *ast.SendStmt:
			w.expr(n.Chan)
			w.expr(n.Value)
		}

		return true
	})
}

func (w loopWrites) expr(expr ast.Expr) {
	ast.Inspect(expr, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.CallExpr:
			w.call(n)
		}

		return true
	})
}

func (w loopWrites) call(call *ast.CallExpr) {
	if tv, ok := w.q.info.Types[call.Fun]; ok && tv.IsType() {
		return
	}

	if lit, ok := ast.Unparen(call.Fun).(*ast.FuncLit); ok {
		w.funcLit(lit)
	} else if lit, ok := w.q.localFuncLitForExpr(call.Fun); ok {
		w.funcLit(lit)
	}

	if recv, ok := pointerMethodReceiver(w.q.info, call); ok {
		w.addDescendants(recv)
	}

	w.callArgs(call.Args)

	if isMutatingBuiltinCall(w.q.info, call) {
		for _, arg := range call.Args {
			w.addDescendants(arg)
		}
	}
}

func (w loopWrites) callArgs(args []ast.Expr) {
	for _, arg := range args {
		if lit, ok := ast.Unparen(arg).(*ast.FuncLit); ok {
			w.funcLit(lit)
			continue
		}

		if lit, ok := w.q.localFuncLitForExpr(arg); ok {
			w.funcLit(lit)
			continue
		}

		if unary, ok := ast.Unparen(arg).(*ast.UnaryExpr); ok && unary.Op == token.AND {
			for root := range w.q.rootsInExpr(unary.X) {
				w.out.addFull(root)
			}

			continue
		}

		tv, ok := w.q.info.Types[arg]
		if ok && isPointerLike(tv.Type) {
			w.addDescendants(arg)
		}
	}
}

func (w loopWrites) addDescendants(expr ast.Expr) {
	for root := range w.q.rootsInExpr(expr) {
		w.out.addDescendants(root)
	}
}

func (w loopWrites) funcLit(lit *ast.FuncLit) {
	if _, ok := w.seen[lit]; ok {
		return
	}

	w.seen[lit] = struct{}{}

	for _, stmt := range lit.Body.List {
		w.stmt(stmt)
	}
}

func (w loopWrites) lhs(lhs ast.Expr) {
	if sym, ok := w.q.symbolOf(lhs); ok {
		w.out.addFull(sym.key)

		return
	}

	for root := range w.q.rootsInExpr(lhs) {
		w.out.addFull(root)
	}
}

func isMutatingBuiltinCall(info *types.Info, call *ast.CallExpr) bool {
	id, ok := ast.Unparen(call.Fun).(*ast.Ident)
	if !ok {
		return false
	}

	builtin, ok := info.ObjectOf(id).(*types.Builtin)
	if !ok {
		return false
	}

	switch builtin.Name() {
	case builtinCopy, builtinClear, builtinDelete, builtinAppend, builtinClose:
		return true
	default:
		return false
	}
}
