package lint

import (
	"go/ast"
	"go/token"
)

// conditionEval decides conditions on path states and splits states by the
// outcome of a condition.
type conditionEval struct {
	q      *exprQuery
	calls  *callFacts
	budget stateBudget
}

func (c conditionEval) truthAcross(states []state, expr ast.Expr) (triState, *evidence) {
	return truthAcrossStates(states, func(st state) (triState, *evidence) {
		return c.truth(st, expr)
	})
}

func truthAcrossStates(
	states []state,
	truth func(state) (triState, *evidence),
) (triState, *evidence) {
	if len(states) == 0 {
		return triUnknown, nil
	}

	allTrue := true
	allFalse := true

	var because *evidence

	for _, st := range states {
		tri, ev := truth(st)
		if tri != triTrue {
			allTrue = false
		}

		if tri != triFalse {
			allFalse = false
		}

		if because == nil && ev != nil {
			copyEv := *ev
			because = &copyEv
		}
	}

	if allTrue {
		return triTrue, because
	}

	if allFalse {
		return triFalse, because
	}

	return triUnknown, nil
}

func (c conditionEval) truth(st state, expr ast.Expr) (triState, *evidence) {
	expr = ast.Unparen(expr)
	if value, ok := c.q.scalarOf(expr); ok && value.kind == scalarBool {
		return boolTextTruth(value.text), nil
	}

	switch expr := expr.(type) {
	case *ast.Ident, *ast.SelectorExpr:
		return c.truthBoolSymbol(st, expr)
	case *ast.CallExpr:
		if sym, ok := c.q.predicateCallSymbolOf(expr); ok {
			return truthSymbolScalar(
				st,
				sym,
				scalar{kind: scalarBool, text: boolTrueText},
				true,
			)
		}
	case *ast.UnaryExpr:
		if expr.Op != token.NOT {
			return triUnknown, nil
		}

		return c.truthNegation(st, expr.X)
	case *ast.BinaryExpr:
		return c.truthBinary(st, expr)
	}

	return triUnknown, nil
}

func boolTextTruth(text string) triState {
	if text == boolTrueText {
		return triTrue
	}

	return triFalse
}

func (c conditionEval) truthBoolSymbol(st state, expr ast.Expr) (triState, *evidence) {
	sym, ok := c.q.symbolOf(expr)
	if !ok || !isBoolType(sym.typ) {
		return triUnknown, nil
	}

	f, ok := st.facts[sym.key]
	if !ok || f.exact == nil || f.exact.value.kind != scalarBool {
		return triUnknown, nil
	}

	copyWhy := f.exact.why
	if f.exact.value.text == boolTrueText {
		return triTrue, &copyWhy
	}

	return triFalse, &copyWhy
}

func (c conditionEval) truthNegation(st state, expr ast.Expr) (triState, *evidence) {
	tri, ev := c.truth(st, expr)
	if tri == triUnknown {
		return triUnknown, nil
	}

	if tri == triTrue {
		return triFalse, ev
	}

	return triTrue, ev
}

func (c conditionEval) truthBinary(st state, expr *ast.BinaryExpr) (triState, *evidence) {
	// token.Token includes operators not meaningful for this expression type.
	//exhaustive:ignore
	switch expr.Op {
	case token.LAND:
		return c.truthJunction(st, expr.X, expr.Y, triFalse)
	case token.LOR:
		return c.truthJunction(st, expr.X, expr.Y, triTrue)
	case token.EQL, token.NEQ:
		return c.truthCompare(st, expr.X, expr.Y, expr.Op == token.EQL)
	case token.GTR, token.GEQ, token.LSS, token.LEQ:
		return c.truthOrderedCompare(st, expr.X, expr.Y, expr.Op)
	default:
		return triUnknown, nil
	}
}

// truthJunction evaluates && (decisive is triFalse) or || (decisive is
// triTrue). A decisive left side wins without looking at the right side.
func (c conditionEval) truthJunction(
	st state,
	lhs, rhs ast.Expr,
	decisive triState,
) (triState, *evidence) {
	left, leftEv := c.truth(st, lhs)
	if left == decisive {
		return decisive, leftEv
	}

	right, rightEv := c.truth(st, rhs)
	if right == decisive {
		return decisive, rightEv
	}

	other := triTrue
	if decisive == triTrue {
		other = triFalse
	}

	if left != other || right != other {
		return triUnknown, nil
	}

	if leftEv != nil {
		return other, leftEv
	}

	return other, rightEv
}

func (c conditionEval) truthCompare(
	st state,
	lhs, rhs ast.Expr,
	wantEq bool,
) (triState, *evidence) {
	if sym, scalar, ok := c.q.symbolScalar(lhs, rhs); ok {
		return truthSymbolScalar(st, sym, scalar, wantEq)
	}

	if sym, scalar, ok := c.q.symbolScalar(rhs, lhs); ok {
		return truthSymbolScalar(st, sym, scalar, wantEq)
	}

	if ls, ok := c.q.scalarOf(lhs); ok {
		if rs, ok := c.q.scalarOf(rhs); ok {
			equal := ls == rs
			if wantEq == equal {
				return triTrue, nil
			}

			return triFalse, nil
		}
	}

	return triUnknown, nil
}

func (c conditionEval) truthOrderedCompare(
	st state,
	lhs, rhs ast.Expr,
	op token.Token,
) (triState, *evidence) {
	sym, scalar, ok := c.q.symbolScalar(lhs, rhs)
	if ok {
		return truthSymbolOrdered(st, sym, scalar, op)
	}

	sym, scalar, ok = c.q.symbolScalar(rhs, lhs)
	if !ok {
		return triUnknown, nil
	}

	return truthSymbolOrdered(st, sym, scalar, reverseOrderedOp(op))
}

func truthSymbolScalar(
	st state,
	sym symbol,
	value scalar,
	wantEq bool,
) (triState, *evidence) {
	f, ok := st.facts[sym.key]
	if !ok {
		return triUnknown, nil
	}

	if f.exact != nil {
		equal := f.exact.value == value

		copyWhy := f.exact.why
		if wantEq == equal {
			return triTrue, &copyWhy
		}

		return triFalse, &copyWhy
	}

	if ev, ok := f.not[value.key()]; ok {
		copyWhy := ev
		if wantEq {
			return triFalse, &copyWhy
		}

		return triTrue, &copyWhy
	}

	return triUnknown, nil
}

func truthSymbolOrdered(
	st state,
	sym symbol,
	value scalar,
	op token.Token,
) (triState, *evidence) {
	limit, ok := orderedLenLimit(sym, value)
	if !ok {
		return triUnknown, nil
	}

	if tri, ok := lenTruthFromLowerBound(limit, op); ok {
		return tri, nil
	}

	f, ok := st.facts[sym.key]
	if !ok {
		return triUnknown, nil
	}

	if tri, ev, ok := truthFromExactLenFact(f, limit, op); ok {
		return tri, ev
	}

	return truthFromNonZeroLenFact(f, limit, op)
}
