package lint

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"
)

func (l *linter) invalidateLHS(st *state, lhs ast.Expr) {
	if sym, ok := l.symbolOf(lhs); ok {
		l.invalidatePrefix(st, sym.key)
		return
	}

	for root := range l.rootsInExpr(lhs) {
		l.invalidatePrefix(st, root)
	}
}

func (l *linter) invalidatePrefix(st *state, prefix string) {
	for key := range st.facts {
		if isSameOrChild(key, prefix) || predicateFactInvalidatedByPrefix(key, prefix) {
			delete(st.facts, key)
		}
	}

	removeAliasPrefix(st, prefix, false)
	removeBindingsForPrefix(st, prefix)
}

func (l *linter) invalidateDescendants(st *state, prefix string) {
	for key := range st.facts {
		if key != prefix &&
			(isSameOrChild(key, prefix) || predicateFactInvalidatedByPrefix(key, prefix)) {
			delete(st.facts, key)
		}
	}

	removeAliasPrefix(st, prefix, true)
	removeBindingsForPrefix(st, prefix)
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

func (l *linter) wipeFacts(states []state) []state {
	if len(states) == 0 {
		return nil
	}

	out := make([]state, len(states))
	for i := range out {
		out[i] = newState()
	}

	return out
}

func (l *linter) invalidateForExprSideEffects(states []state, expr ast.Expr) []state {
	out := make([]state, 0, len(states))
	for _, st := range states {
		out = append(out, l.invalidateForExprSideEffectsOne(st, expr))
	}

	return l.normalizeStates(out)
}

func (l *linter) invalidateForExprSideEffectsOne(st state, expr ast.Expr) state {
	out := st.clone()

	ast.Inspect(expr, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.CallExpr:
			out = l.invalidateForCallOne(out, n)
		}

		return true
	})

	return out
}

func (l *linter) invalidateForCall(states []state, call *ast.CallExpr) []state {
	out := make([]state, 0, len(states))
	for _, st := range states {
		out = append(out, l.invalidateForCallOne(st, call))
	}

	return l.normalizeStates(out)
}

func (l *linter) invalidateForFuncLitEffectsSeen(
	st state,
	lit *ast.FuncLit,
	seen map[*ast.FuncLit]struct{},
) state {
	if lit == nil || lit.Body == nil {
		return st
	}

	if _, ok := seen[lit]; ok {
		return st
	}

	seen[lit] = struct{}{}
	out := st.clone()

	ast.Inspect(lit.Body, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}

		if !l.invalidateForFuncLitNode(&out, n, seen) {
			return false
		}

		return true
	})

	return out
}

func (l *linter) invalidateForFuncLitNode(
	out *state,
	node ast.Node,
	seen map[*ast.FuncLit]struct{},
) bool {
	switch n := node.(type) {
	case *ast.AssignStmt:
		for _, lhs := range n.Lhs {
			l.invalidateLHS(out, lhs)
		}
	case *ast.IncDecStmt:
		l.invalidateLHS(out, n.X)
	case *ast.RangeStmt:
		l.invalidateRangeLoopTargets(out, n)
	case *ast.CallExpr:
		*out = l.invalidateForCallOneSeen(*out, n, seen)
	case *ast.SendStmt:
		*out = l.invalidateForExprSideEffectsOne(*out, n.Chan)
		*out = l.invalidateForExprSideEffectsOne(*out, n.Value)
	}

	return true
}

func (l *linter) invalidateRangeLoopTargets(out *state, stmt *ast.RangeStmt) {
	if stmt.Key != nil {
		l.invalidateLHS(out, stmt.Key)
	}

	if stmt.Value != nil {
		l.invalidateLHS(out, stmt.Value)
	}
}

func (l *linter) invalidateForCallOne(st state, call *ast.CallExpr) state {
	return l.invalidateForCallOneSeen(st, call, make(map[*ast.FuncLit]struct{}))
}

func (l *linter) invalidateForCallOneSeen(
	st state,
	call *ast.CallExpr,
	seen map[*ast.FuncLit]struct{},
) state {
	if tv, ok := l.pkg.TypesInfo.Types[call.Fun]; ok && tv.IsType() {
		return st
	}

	out := st.clone()

	if lit, ok := l.unparen(call.Fun).(*ast.FuncLit); ok {
		out = l.invalidateForFuncLitEffectsSeen(out, lit, seen)
	} else if lit, ok := l.localFuncLitForExpr(call.Fun); ok {
		out = l.invalidateForFuncLitEffectsSeen(out, lit, seen)
	}

	l.invalidateCallReceiver(&out, call.Fun)
	out = l.invalidateCallArguments(out, call.Args, seen)
	l.invalidateMutatingBuiltinArgs(&out, call)

	summary := l.summaryForCall(call)

	nextStates := l.applySummaryContracts(out, call, summary.always)
	if len(nextStates) == 0 {
		return newState()
	}

	return nextStates[0]
}

func (l *linter) invalidateCallReceiver(out *state, fun ast.Expr) {
	sel, ok := l.unparen(fun).(*ast.SelectorExpr)
	if !ok {
		return
	}

	selection := l.pkg.TypesInfo.Selections[sel]
	if selection == nil || selection.Kind() != types.MethodVal || !isPointerLike(selection.Recv()) {
		return
	}

	l.forEachExprRoot(sel.X, func(root string) {
		l.invalidateDescendants(out, root)
	})
}

func (l *linter) invalidateCallArguments(
	out state,
	args []ast.Expr,
	seen map[*ast.FuncLit]struct{},
) state {
	for _, arg := range args {
		out = l.invalidateForFuncLitsInExprSeen(out, arg, seen)

		if lit, ok := l.localFuncLitForExpr(arg); ok {
			out = l.invalidateForFuncLitEffectsSeen(out, lit, seen)
			continue
		}

		if address, ok := l.unparen(arg).(*ast.UnaryExpr); ok && address.Op == token.AND {
			l.forEachExprRoot(address.X, func(root string) {
				l.invalidatePrefix(&out, root)
			})

			continue
		}

		tv, ok := l.pkg.TypesInfo.Types[arg]
		if !ok || !isPointerLike(tv.Type) {
			continue
		}

		l.forEachExprRoot(arg, func(root string) {
			l.invalidateDescendants(&out, root)
		})
	}

	return out
}

func (l *linter) invalidateMutatingBuiltinArgs(out *state, call *ast.CallExpr) {
	id, ok := l.unparen(call.Fun).(*ast.Ident)
	if !ok {
		return
	}

	builtin, ok := l.pkg.TypesInfo.ObjectOf(id).(*types.Builtin)
	if !ok {
		return
	}

	switch builtin.Name() {
	case "copy", "clear", "delete", "append", "close":
		for _, arg := range call.Args {
			l.forEachExprRoot(arg, func(root string) {
				l.invalidateDescendants(out, root)
			})
		}
	}
}

func (l *linter) forEachExprRoot(expr ast.Expr, visit func(string)) {
	for root := range l.rootsInExpr(expr) {
		visit(root)
	}
}

func (l *linter) invalidateForFuncLitsInExprSeen(
	st state,
	expr ast.Expr,
	seen map[*ast.FuncLit]struct{},
) state {
	out := st

	ast.Inspect(expr, func(node ast.Node) bool {
		lit, ok := node.(*ast.FuncLit)
		if !ok {
			return true
		}

		out = l.invalidateForFuncLitEffectsSeen(out, lit, seen)

		return false
	})

	return out
}

func (l *linter) rootsInExpr(expr ast.Expr) map[string]struct{} {
	roots := make(map[string]struct{})

	ast.Inspect(expr, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case ast.Expr:
			if sym, ok := l.symbolOf(n); ok {
				roots[sym.root] = struct{}{}
			}
		}

		return true
	})

	return roots
}
