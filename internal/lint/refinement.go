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

//nolint:cyclop,gocognit // Refinement intentionally enumerates boolean operators and propagation paths.
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
		if sym, ok := l.predicateCallSymbolOf(expr); ok {
			states := []state{st}
			if next, ok := l.refineCallExpr(st, expr, wantTrue); ok {
				states = next
			}

			return l.refinePredicateCallStates(states, sym, expr.Pos(), wantTrue)
		}

		if next, ok := l.refineCallExpr(st, expr, wantTrue); ok {
			return next
		}
	case *ast.BinaryExpr:
		//exhaustive:ignore token.Token includes operators not meaningful for this expression type.
		switch expr.Op {
		case token.LAND:
			if wantTrue {
				left := l.refineState(st, expr.X, true)
				return l.refineStates(left, expr.Y, true)
			}

			out := l.refineState(st, expr.X, false)
			leftTrue := l.refineState(st, expr.X, true)
			out = append(out, l.refineStates(leftTrue, expr.Y, false)...)

			return l.normalizeStates(out)
		case token.LOR:
			if !wantTrue {
				left := l.refineState(st, expr.X, false)
				return l.refineStates(left, expr.Y, false)
			}

			out := l.refineState(st, expr.X, true)
			leftFalse := l.refineState(st, expr.X, false)
			out = append(out, l.refineStates(leftFalse, expr.Y, true)...)

			return l.normalizeStates(out)
		case token.EQL:
			if next, ok := l.refineCallScalar(st, expr.X, expr.Y, wantTrue); ok {
				return next
			}

			if sym, scalar, ok := l.symbolScalar(expr.X, expr.Y); ok {
				return l.refineSymbolScalar(st, sym, scalar, wantTrue, expr.Pos())
			}

			if sym, scalar, ok := l.symbolScalar(expr.Y, expr.X); ok {
				return l.refineSymbolScalar(st, sym, scalar, wantTrue, expr.Pos())
			}
		case token.NEQ:
			if next, ok := l.refineCallScalar(st, expr.X, expr.Y, !wantTrue); ok {
				return next
			}

			if sym, scalar, ok := l.symbolScalar(expr.X, expr.Y); ok {
				return l.refineSymbolScalar(st, sym, scalar, !wantTrue, expr.Pos())
			}

			if sym, scalar, ok := l.symbolScalar(expr.Y, expr.X); ok {
				return l.refineSymbolScalar(st, sym, scalar, !wantTrue, expr.Pos())
			}
		case token.GTR, token.GEQ, token.LSS, token.LEQ:
			if sym, scalar, ok := l.symbolScalar(expr.X, expr.Y); ok {
				return l.refineSymbolOrdered(st, sym, scalar, expr.Op, wantTrue, expr.Pos())
			}

			if sym, scalar, ok := l.symbolScalar(expr.Y, expr.X); ok {
				return l.refineSymbolOrdered(
					st,
					sym,
					scalar,
					reverseOrderedOp(expr.Op),
					wantTrue,
					expr.Pos(),
				)
			}
		}
	case *ast.Ident, *ast.SelectorExpr:
		if sym, ok := l.symbolOf(expr); ok && isBoolType(sym.typ) {
			val := scalar{
				kind: scalarBool,
				text: map[bool]string{true: boolTrueText, false: boolFalseText}[wantTrue],
			}

			next, ok := l.setExact(
				st,
				sym,
				val,
				evidence{pos: expr.Pos(), text: l.relationText(sym, "==", val)},
			)
			if !ok {
				return nil
			}

			return l.applyBindingCondition(next, sym, map[bool]returnKind{
				true:  returnBoolTrue,
				false: returnBoolFalse,
			}[wantTrue])
		}
	}

	return []state{st}
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
