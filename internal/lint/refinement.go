package lint

import (
	"go/ast"
	"go/token"
)

func (l *linter) refineStates(states []state, expr ast.Expr, wantTrue bool) []state {
	out := make([]state, 0, len(states))
	for _, st := range states {
		out = append(out, l.refineState(st, expr, wantTrue)...)
	}

	return l.normalizeStates(out)
}

func (l *linter) refineState(st state, expr ast.Expr, wantTrue bool) []state {
	expr = l.unparen(expr)
	if tri, _ := l.truth(st, expr); tri == triTrue {
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
			return l.refineState(st, expr.X, !wantTrue)
		}
	case *ast.CallExpr:
		return l.refineCallCondition(st, expr, wantTrue)
	case *ast.BinaryExpr:
		return l.refineBinary(st, expr, wantTrue)
	case *ast.Ident, *ast.SelectorExpr:
		return l.refineBoolSymbol(st, expr, wantTrue)
	}

	return []state{st}
}

func (l *linter) refineCallCondition(
	st state,
	call *ast.CallExpr,
	wantTrue bool,
) []state {
	sym, predicate := l.predicateCallSymbolOf(call)

	states := []state{st}
	if next, ok := l.refineCallExpr(st, call, wantTrue); ok {
		states = next
	}

	if !predicate {
		return states
	}

	return l.refinePredicateCallStates(states, sym, call.Pos(), wantTrue)
}

func (l *linter) refineBinary(
	st state,
	expr *ast.BinaryExpr,
	wantTrue bool,
) []state {
	//exhaustive:ignore token.Token includes operators not meaningful for this expression type.
	switch expr.Op {
	case token.LAND:
		return l.refineLogicalAnd(st, expr.X, expr.Y, wantTrue)
	case token.LOR:
		return l.refineLogicalOr(st, expr.X, expr.Y, wantTrue)
	case token.EQL:
		return l.refineEquality(st, expr.X, expr.Y, wantTrue, expr.Pos())
	case token.NEQ:
		return l.refineEquality(st, expr.X, expr.Y, !wantTrue, expr.Pos())
	case token.GTR, token.GEQ, token.LSS, token.LEQ:
		return l.refineOrderedComparison(st, expr, wantTrue)
	default:
		return []state{st}
	}
}

func (l *linter) refineLogicalAnd(
	st state,
	lhs ast.Expr,
	rhs ast.Expr,
	wantTrue bool,
) []state {
	if wantTrue {
		left := l.refineState(st, lhs, true)

		return l.refineStates(left, rhs, true)
	}

	out := l.refineState(st, lhs, false)
	leftTrue := l.refineState(st, lhs, true)
	out = append(out, l.refineStates(leftTrue, rhs, false)...)

	return l.normalizeStates(out)
}

func (l *linter) refineLogicalOr(
	st state,
	lhs ast.Expr,
	rhs ast.Expr,
	wantTrue bool,
) []state {
	if !wantTrue {
		left := l.refineState(st, lhs, false)

		return l.refineStates(left, rhs, false)
	}

	out := l.refineState(st, lhs, true)
	leftFalse := l.refineState(st, lhs, false)
	out = append(out, l.refineStates(leftFalse, rhs, true)...)

	return l.normalizeStates(out)
}

func (l *linter) refineEquality(
	st state,
	lhs ast.Expr,
	rhs ast.Expr,
	wantEqual bool,
	pos token.Pos,
) []state {
	if next, ok := l.refineCallScalar(st, lhs, rhs, wantEqual); ok {
		return next
	}

	if sym, value, ok := l.symbolScalar(lhs, rhs); ok {
		return l.refineSymbolScalar(st, sym, value, wantEqual, pos)
	}

	if sym, value, ok := l.symbolScalar(rhs, lhs); ok {
		return l.refineSymbolScalar(st, sym, value, wantEqual, pos)
	}

	return []state{st}
}

func (l *linter) refineOrderedComparison(
	st state,
	expr *ast.BinaryExpr,
	wantTrue bool,
) []state {
	if sym, value, ok := l.symbolScalar(expr.X, expr.Y); ok {
		return l.refineSymbolOrdered(st, sym, value, expr.Op, wantTrue, expr.Pos())
	}

	if sym, value, ok := l.symbolScalar(expr.Y, expr.X); ok {
		return l.refineSymbolOrdered(
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

func (l *linter) refineBoolSymbol(st state, expr ast.Expr, wantTrue bool) []state {
	sym, ok := l.symbolOf(expr)
	if !ok || !isBoolType(sym.typ) {
		return []state{st}
	}

	value := scalar{kind: scalarBool, text: boolFalseText}
	resultKind := returnBoolFalse

	if wantTrue {
		value.text = boolTrueText
		resultKind = returnBoolTrue
	}

	next, ok := l.setExact(
		st,
		sym,
		value,
		evidence{pos: expr.Pos(), text: l.relationText(sym, "==", value)},
	)
	if !ok {
		return nil
	}

	return l.applyBindingCondition(next, sym, resultKind)
}

func (l *linter) refinePredicateCallStates(
	states []state,
	sym symbol,
	pos token.Pos,
	wantTrue bool,
) []state {
	value := scalar{kind: scalarBool, text: boolFalseText}
	if wantTrue {
		value.text = boolTrueText
	}

	out := make([]state, 0, len(states))
	for _, st := range states {
		next, ok := l.setExact(
			st,
			sym,
			value,
			evidence{pos: pos, text: l.relationText(sym, "==", value)},
		)
		if ok {
			out = append(out, next)
		}
	}

	return l.normalizeStates(out)
}

func (l *linter) refineSymbolOrdered(
	st state,
	sym symbol,
	value scalar,
	op token.Token,
	wantTrue bool,
	pos token.Pos,
) []state {
	if !isLenSymbol(sym) || value.kind != scalarInt {
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

	if next, refined, ok := l.refineLenCompare(st, sym, op, limit, wantTrue, pos); ok {
		if !refined {
			return []state{st}
		}

		return []state{next}
	}

	return []state{st}
}

func (l *linter) refineSymbolScalar(
	st state,
	sym symbol,
	value scalar,
	wantEq bool,
	pos token.Pos,
) []state {
	if wantEq {
		next, ok := l.setExact(
			st,
			sym,
			value,
			evidence{pos: pos, text: l.relationText(sym, "==", value)},
		)
		if !ok {
			return nil
		}

		return l.applyBindingForScalar(next, sym, value, true)
	}

	next, ok := l.addNot(st, sym, value, evidence{pos: pos, text: l.relationText(sym, "!=", value)})
	if !ok {
		return nil
	}

	return l.applyBindingForScalar(next, sym, value, false)
}

func (l *linter) setExact(st state, sym symbol, value scalar, why evidence) (state, bool) {
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

func (l *linter) addNot(st state, sym symbol, value scalar, why evidence) (state, bool) {
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
		if isBoolType(sym.typ) && value.kind == scalarBool {
			other := scalar{kind: scalarBool, text: boolTrueText}
			if value.text == boolTrueText {
				other.text = boolFalseText
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
