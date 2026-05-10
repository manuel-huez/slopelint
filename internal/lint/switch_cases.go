package lint

import (
	"go/ast"
	"go/token"
)

func (l *linter) truthAcrossCase(
	states []state,
	tag ast.Expr,
	list []ast.Expr,
) (triState, *evidence) {
	return truthAcrossStates(states, func(st state) (triState, *evidence) {
		return l.truthCase(st, tag, list)
	})
}

func (l *linter) truthCase(st state, tag ast.Expr, list []ast.Expr) (triState, *evidence) {
	if len(list) == 0 {
		return triTrue, nil
	}

	allFalse := true

	var firstFalse *evidence

	for _, expr := range list {
		var (
			tri triState
			ev  *evidence
		)

		if tag == nil {
			tri, ev = l.truth(st, expr)
		} else {
			tri, ev = l.truthSyntheticCompare(st, tag, expr)
		}

		if tri == triTrue {
			return triTrue, ev
		}

		if tri == triFalse && firstFalse == nil && ev != nil {
			copyEv := *ev
			firstFalse = &copyEv
		}

		if tri != triFalse {
			allFalse = false
		}
	}

	if allFalse {
		return triFalse, firstFalse
	}

	return triUnknown, nil
}

func (l *linter) truthSyntheticCompare(st state, lhs, rhs ast.Expr) (triState, *evidence) {
	if sym, scalar, ok := l.symbolScalar(lhs, rhs); ok {
		return l.truthSymbolScalar(st, sym, scalar, true)
	}

	if sym, scalar, ok := l.symbolScalar(rhs, lhs); ok {
		return l.truthSymbolScalar(st, sym, scalar, true)
	}

	if ls, ok := l.scalarOf(lhs); ok {
		if rs, ok := l.scalarOf(rhs); ok {
			if ls == rs {
				return triTrue, nil
			}

			return triFalse, nil
		}
	}

	return triUnknown, nil
}

func (l *linter) refineStatesCase(
	states []state,
	tag ast.Expr,
	list []ast.Expr,
	wantMatch bool,
) []state {
	if len(list) == 0 {
		if wantMatch {
			return l.normalizeStates(states)
		}

		return nil
	}

	out := make([]state, 0)
	for _, st := range states {
		out = append(out, l.refineStateCase(st, tag, list, wantMatch)...)
	}

	return l.normalizeStates(out)
}

func (l *linter) refineStateCase(st state, tag ast.Expr, list []ast.Expr, wantMatch bool) []state {
	if tag == nil {
		return l.refineStateListAsOr(st, list, wantMatch)
	}
	// switch x { case a, b: } behaves like x == a || x == b.
	branches := make([]ast.Expr, 0, len(list))
	for _, item := range list {
		branches = append(branches, &ast.BinaryExpr{X: tag, Op: token.EQL, Y: item})
	}

	return l.refineStateListAsOr(st, branches, wantMatch)
}

func (l *linter) refineStateListAsOr(st state, exprs []ast.Expr, wantTrue bool) []state {
	if len(exprs) == 0 {
		if wantTrue {
			return []state{st}
		}

		return nil
	}

	if wantTrue {
		var out []state

		currentFalse := []state{st}
		for _, expr := range exprs {
			matched := l.refineStates(currentFalse, expr, true)
			out = append(out, matched...)
			currentFalse = l.refineStates(currentFalse, expr, false)
		}

		return l.normalizeStates(out)
	}

	current := []state{st}
	for _, expr := range exprs {
		current = l.refineStates(current, expr, false)
	}

	return l.normalizeStates(current)
}
