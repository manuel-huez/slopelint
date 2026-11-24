package flow

import (
	"go/ast"
	"go/token"
)

func (c conditionEval) refineStates(states []state, expr ast.Expr, wantTrue bool) []state {
	out := make([]state, 0, len(states))
	for _, st := range states {
		out = append(out, c.refineState(st, expr, wantTrue)...)
	}

	return c.budget.normalize(out)
}

func (c conditionEval) refineState(st state, expr ast.Expr, wantTrue bool) []state {
	expr = ast.Unparen(expr)
	if tri, _ := c.truth(st, expr); tri == triTrue {
		if wantTrue {
			return []state{st}
		}

		return nil
	} else if tri == triFalse {
		if wantTrue {
			return nil
		}

		return []state{st}
	}

	switch expr := expr.(type) {
	case *ast.UnaryExpr:
		if expr.Op == token.NOT {
			return c.refineState(st, expr.X, !wantTrue)
		}
	case *ast.CallExpr:
		return c.refineCallCondition(st, expr, wantTrue)
	case *ast.BinaryExpr:
		return c.refineBinary(st, expr, wantTrue)
	case *ast.Ident, *ast.SelectorExpr:
		return c.refineBoolSymbol(st, expr, wantTrue)
	}

	return []state{st}
}

func (c conditionEval) refineCallCondition(
	st state,
	call *ast.CallExpr,
	wantTrue bool,
) []state {
	sym, predicate := c.q.predicateCallSymbolOf(call)

	states := []state{st}
	if next, ok := c.calls.refineCall(st, call, wantTrue); ok {
		states = next
	}

	if !predicate {
		return states
	}

	return c.refinePredicateCallStates(states, sym, call.Pos(), wantTrue)
}

func (c conditionEval) refineBinary(
	st state,
	expr *ast.BinaryExpr,
	wantTrue bool,
) []state {
	// token.Token includes operators not meaningful for this expression type.
	//exhaustive:ignore
	switch expr.Op {
	case token.LAND:
		return c.refineLogicalAnd(st, expr.X, expr.Y, wantTrue)
	case token.LOR:
		return c.refineLogicalOr(st, expr.X, expr.Y, wantTrue)
	case token.EQL:
		return c.refineEquality(st, expr.X, expr.Y, wantTrue, expr.Pos())
	case token.NEQ:
		return c.refineEquality(st, expr.X, expr.Y, !wantTrue, expr.Pos())
	case token.GTR, token.GEQ, token.LSS, token.LEQ:
		return c.refineOrderedComparison(st, expr, wantTrue)
	default:
		return []state{st}
	}
}

func (c conditionEval) refineLogicalAnd(
	st state,
	lhs ast.Expr,
	rhs ast.Expr,
	wantTrue bool,
) []state {
	if wantTrue {
		left := c.refineState(st, lhs, true)

		return c.refineStates(left, rhs, true)
	}

	out := c.refineState(st, lhs, false)
	leftTrue := c.refineState(st, lhs, true)
	out = append(out, c.refineStates(leftTrue, rhs, false)...)

	return c.budget.normalize(out)
}

func (c conditionEval) refineLogicalOr(
	st state,
	lhs ast.Expr,
	rhs ast.Expr,
	wantTrue bool,
) []state {
	if !wantTrue {
		left := c.refineState(st, lhs, false)

		return c.refineStates(left, rhs, false)
	}

	out := c.refineState(st, lhs, true)
	leftFalse := c.refineState(st, lhs, false)
	out = append(out, c.refineStates(leftFalse, rhs, true)...)

	return c.budget.normalize(out)
}

func (c conditionEval) refineEquality(
	st state,
	lhs ast.Expr,
	rhs ast.Expr,
	wantEqual bool,
	pos token.Pos,
) []state {
	if next, ok := c.calls.refineCallScalar(st, lhs, rhs, wantEqual); ok {
		return next
	}

	if sym, value, ok := c.q.symbolScalar(lhs, rhs); ok {
		return refineSymbolScalar(st, sym, value, wantEqual, pos)
	}

	if sym, value, ok := c.q.symbolScalar(rhs, lhs); ok {
		return refineSymbolScalar(st, sym, value, wantEqual, pos)
	}

	return []state{st}
}

func (c conditionEval) refineOrderedComparison(
	st state,
	expr *ast.BinaryExpr,
	wantTrue bool,
) []state {
	if sym, value, ok := c.q.symbolScalar(expr.X, expr.Y); ok {
		return refineSymbolOrdered(st, sym, value, expr.Op, wantTrue, expr.Pos())
	}

	if sym, value, ok := c.q.symbolScalar(expr.Y, expr.X); ok {
		return refineSymbolOrdered(
			st,
			sym,
			value,
			reverseOrderedOp(expr.Op),
			wantTrue,
			expr.Pos(),
		)
	}

	return []state{st}
}

func (c conditionEval) refineBoolSymbol(st state, expr ast.Expr, wantTrue bool) []state {
	sym, ok := c.q.symbolOf(expr)
	if !ok || !isBoolType(sym.typ) {
		return []state{st}
	}

	value := Scalar{Kind: ScalarBool, Text: boolFalseText}
	resultKind := returnBoolFalse

	if wantTrue {
		value.Text = boolTrueText
		resultKind = returnBoolTrue
	}

	next, ok := setExact(
		st,
		sym,
		value,
		evidence{pos: expr.Pos(), text: relationText(sym, "==", value)},
	)
	if !ok {
		return nil
	}

	return applyBindingCondition(next, sym, resultKind)
}

func (c conditionEval) refinePredicateCallStates(
	states []state,
	sym symbol,
	pos token.Pos,
	wantTrue bool,
) []state {
	value := Scalar{Kind: ScalarBool, Text: boolFalseText}
	if wantTrue {
		value.Text = boolTrueText
	}

	out := make([]state, 0, len(states))
	for _, st := range states {
		next, ok := setExact(
			st,
			sym,
			value,
			evidence{pos: pos, text: relationText(sym, "==", value)},
		)
		if ok {
			out = append(out, next)
		}
	}

	return c.budget.normalize(out)
}

func refineSymbolOrdered(
	st state,
	sym symbol,
	value Scalar,
	op token.Token,
	wantTrue bool,
	pos token.Pos,
) []state {
	if !isLenSymbol(sym) || value.Kind != ScalarInt {
		return []state{st}
	}

	limit, ok := scalarIntValue(value)
	if !ok {
		return []state{st}
	}

	if always, ok := lenCompareFromNonNegative(limit, op); ok {
		if wantTrue == always {
			return []state{st}
		}

		return nil
	}

	if next, refined, ok := refineLenCompare(st, sym, op, limit, wantTrue, pos); ok {
		if !refined {
			return []state{st}
		}

		return []state{next}
	}

	return []state{st}
}

func refineSymbolScalar(
	st state,
	sym symbol,
	value Scalar,
	wantEq bool,
	pos token.Pos,
) []state {
	if wantEq {
		next, ok := setExact(
			st,
			sym,
			value,
			evidence{pos: pos, text: relationText(sym, "==", value)},
		)
		if !ok {
			return nil
		}

		return applyBindingForScalar(next, sym, value, true)
	}

	next, ok := addNot(st, sym, value, evidence{pos: pos, text: relationText(sym, "!=", value)})
	if !ok {
		return nil
	}

	return applyBindingForScalar(next, sym, value, false)
}

func setExact(st state, sym symbol, value Scalar, why evidence) (state, bool) {
	out, keys := clonedAliasClosure(st, sym)

	for _, key := range keys {
		fact := out.facts[key]
		if fact.exact != nil {
			if fact.exact.value != value {
				return state{}, false
			}

			continue
		}

		if _, bad := fact.not[value.key()]; bad {
			return state{}, false
		}
	}

	for _, key := range keys {
		f := out.facts[key].clone()
		f.exact = &exactFact{value: value, why: why}
		f.not = nil
		out.facts[key] = f
	}

	return out, true
}

func addNot(st state, sym symbol, value Scalar, why evidence) (state, bool) {
	out, keys := clonedAliasClosure(st, sym)

	for _, key := range keys {
		if out.facts[key].exact != nil && out.facts[key].exact.value == value {
			return out, false
		}
	}

	for _, key := range keys {
		f := out.facts[key].clone()
		if f.exact != nil {
			continue
		}

		if f.not == nil {
			f.not = make(map[string]evidence)
		}

		f.not[value.key()] = why
		if isBoolType(sym.typ) && value.Kind == ScalarBool {
			other := Scalar{Kind: ScalarBool, Text: boolTrueText}
			if value.Text == boolTrueText {
				other.Text = boolFalseText
			}

			f.exact = &exactFact{value: other, why: why}
			f.not = nil
		}

		out.facts[key] = f
	}

	return out, true
}

func clonedAliasClosure(st state, sym symbol) (state, []string) {
	out := st.clone()

	return out, aliasClosure(out, sym.key)
}
