package lint

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"
)

// invalidator drops facts that writes and calls may change. Calls also apply
// the contracts their summaries always guarantee.
type invalidator struct {
	q      *exprQuery
	calls  *callFacts
	budget stateBudget
}

func (inv invalidator) invalidateLHS(st *state, lhs ast.Expr) {
	if sym, ok := inv.q.symbolOf(lhs); ok {
		st.invalidatePrefix(sym.key)
		return
	}

	for root := range inv.q.rootsInExpr(lhs) {
		st.invalidatePrefix(root)
	}
}

func (s *state) invalidatePrefix(prefix string) {
	for key := range s.facts {
		if isSameOrChild(key, prefix) || predicateFactInvalidatedByPrefix(key, prefix) {
			delete(s.facts, key)
		}
	}

	removeAliasPrefix(s, prefix, false)
	removeBindingsForPrefix(s, prefix)
}

func (s *state) invalidateDescendants(prefix string) {
	for key := range s.facts {
		if key != prefix &&
			(isSameOrChild(key, prefix) || predicateFactInvalidatedByPrefix(key, prefix)) {
			delete(s.facts, key)
		}
	}

	removeAliasPrefix(s, prefix, true)
	removeBindingsForPrefix(s, prefix)
}

func predicateFactInvalidatedByPrefix(key string, prefix string) bool {
	if !strings.Contains(key, "|"+predicatePathSegmentPrefix) {
		return false
	}

	root, _, ok := strings.Cut(prefix, "|")
	if !ok {
		return false
	}

	return strings.HasPrefix(key, root+"|"+predicatePathSegmentPrefix)
}

func wipeFacts(states []state) []state {
	out := make([]state, len(states))
	for i := range out {
		out[i] = newState()
	}

	return out
}

func (inv invalidator) forExprSideEffects(states []state, expr ast.Expr) []state {
	out := make([]state, 0, len(states))
	for _, st := range states {
		out = append(out, inv.forExprSideEffectsOne(st, expr))
	}

	return inv.budget.normalize(out)
}

func (inv invalidator) forExprSideEffectsOne(st state, expr ast.Expr) state {
	out := st.clone()

	ast.Inspect(expr, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.CallExpr:
			out = inv.forCallOne(out, n)
		case *ast.UnaryExpr:
			// An address stored in a value, such as &x in a map of any,
			// lets a later call write x without naming it.
			if n.Op == token.AND {
				for root := range inv.q.rootsInExpr(n.X) {
					out.invalidatePrefix(root)
				}
			}
		}

		return true
	})

	return out
}

func (inv invalidator) forCall(states []state, call *ast.CallExpr) []state {
	out := make([]state, 0, len(states))
	for _, st := range states {
		out = append(out, inv.forCallOne(st, call))
	}

	return inv.budget.normalize(out)
}

// forFuncLitEffects applies writes inside a literal that may run during the
// call. seen stops recursion through literals that call each other.
func (inv invalidator) forFuncLitEffects(
	st state,
	lit *ast.FuncLit,
	seen map[*ast.FuncLit]struct{},
) state {
	if _, ok := seen[lit]; ok {
		return st
	}

	seen[lit] = struct{}{}
	out := st.clone()

	ast.Inspect(lit.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.AssignStmt:
			for _, lhs := range n.Lhs {
				inv.invalidateLHS(&out, lhs)
			}
		case *ast.IncDecStmt:
			inv.invalidateLHS(&out, n.X)
		case *ast.RangeStmt:
			if n.Key != nil {
				inv.invalidateLHS(&out, n.Key)
			}

			if n.Value != nil {
				inv.invalidateLHS(&out, n.Value)
			}
		case *ast.CallExpr:
			out = inv.forCallOneSeen(out, n, seen)
		case *ast.SendStmt:
			out = inv.forExprSideEffectsOne(out, n.Chan)
			out = inv.forExprSideEffectsOne(out, n.Value)
		}

		return true
	})

	return out
}

func (inv invalidator) forCallOne(st state, call *ast.CallExpr) state {
	return inv.forCallOneSeen(st, call, make(map[*ast.FuncLit]struct{}))
}

func (inv invalidator) forCallOneSeen(
	st state,
	call *ast.CallExpr,
	seen map[*ast.FuncLit]struct{},
) state {
	if tv, ok := inv.q.info.Types[call.Fun]; ok && tv.IsType() {
		return st
	}

	out := st.clone()

	if lit, ok := ast.Unparen(call.Fun).(*ast.FuncLit); ok {
		out = inv.forFuncLitEffects(out, lit, seen)
	} else if lit, ok := inv.q.localFuncLitForExpr(call.Fun); ok {
		out = inv.forFuncLitEffects(out, lit, seen)
	}

	if recv, ok := pointerMethodReceiver(inv.q.info, call); ok {
		for root := range inv.q.rootsInExpr(recv) {
			out.invalidateDescendants(root)
		}
	}

	out = inv.callArguments(out, call.Args, seen)

	if isMutatingBuiltinCall(inv.q.info, call) {
		for _, arg := range call.Args {
			for root := range inv.q.rootsInExpr(arg) {
				out.invalidateDescendants(root)
			}
		}
	}

	next, ok := inv.calls.applyAlways(out, call)
	if !ok {
		return newState()
	}

	return next
}

func (inv invalidator) callArguments(
	out state,
	args []ast.Expr,
	seen map[*ast.FuncLit]struct{},
) state {
	for _, arg := range args {
		ast.Inspect(arg, func(node ast.Node) bool {
			lit, ok := node.(*ast.FuncLit)
			if !ok {
				return true
			}

			out = inv.forFuncLitEffects(out, lit, seen)

			return false
		})

		if lit, ok := inv.q.localFuncLitForExpr(arg); ok {
			out = inv.forFuncLitEffects(out, lit, seen)
			continue
		}

		if address, ok := ast.Unparen(arg).(*ast.UnaryExpr); ok && address.Op == token.AND {
			for root := range inv.q.rootsInExpr(address.X) {
				out.invalidatePrefix(root)
			}

			continue
		}

		tv, ok := inv.q.info.Types[arg]
		if !ok || !isPointerLike(tv.Type) {
			continue
		}

		for root := range inv.q.rootsInExpr(arg) {
			out.invalidateDescendants(root)
		}
	}

	return out
}

// pointerMethodReceiver returns the receiver expression of a method call that
// may write through it.
func pointerMethodReceiver(info *types.Info, call *ast.CallExpr) (ast.Expr, bool) {
	sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	if !ok {
		return nil, false
	}

	selection := info.Selections[sel]
	if selection == nil || selection.Kind() != types.MethodVal || !isPointerLike(selection.Recv()) {
		return nil, false
	}

	return sel.X, true
}
