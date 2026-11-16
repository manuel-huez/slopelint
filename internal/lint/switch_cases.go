package lint

import (
	"go/ast"
	"go/token"
)

func (c conditionEval) truthCase(st state, tag ast.Expr, list []ast.Expr) (triState, *evidence) {
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
			tri, ev = c.truth(st, expr)
		} else {
			tri, ev = c.truthCompare(st, tag, expr, true)
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

func (c conditionEval) refineStatesCase(
	states []state,
	tag ast.Expr,
	list []ast.Expr,
	wantMatch bool,
) []state {
	if len(list) == 0 {
		if wantMatch {
			return c.budget.normalize(states)
		}

		return nil
	}

	out := make([]state, 0)
	for _, st := range states {
		out = append(out, c.refineStateCase(st, tag, list, wantMatch)...)
	}

	return c.budget.normalize(out)
}

func (c conditionEval) refineStateCase(
	st state,
	tag ast.Expr,
	list []ast.Expr,
	wantMatch bool,
) []state {
	if tag == nil {
		return c.refineStateListAsOr(st, list, wantMatch)
	}
	// switch x { case a, b: } behaves like x == a || x == b.
	branches := make([]ast.Expr, 0, len(list))
	for _, item := range list {
		branches = append(branches, &ast.BinaryExpr{X: tag, Op: token.EQL, Y: item})
	}

	return c.refineStateListAsOr(st, branches, wantMatch)
}

func (c conditionEval) refineStateListAsOr(st state, exprs []ast.Expr, wantTrue bool) []state {
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
			matched := c.refineStates(currentFalse, expr, true)
			out = append(out, matched...)
			currentFalse = c.refineStates(currentFalse, expr, false)
		}

		return c.budget.normalize(out)
	}

	current := []state{st}
	for _, expr := range exprs {
		current = c.refineStates(current, expr, false)
	}

	return c.budget.normalize(current)
}
