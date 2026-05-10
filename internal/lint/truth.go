package lint

import (
	"go/ast"
	"go/token"
)

func (l *linter) truthAcross(states []state, expr ast.Expr) (triState, *evidence) {
	return truthAcrossStates(states, func(st state) (triState, *evidence) {
		return l.truth(st, expr)
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

//nolint:cyclop,gocognit,nestif // Boolean reasoning is intentionally explicit for analyzer correctness.
func (l *linter) truth(st state, expr ast.Expr) (triState, *evidence) {
	expr = l.unparen(expr)
	if scalar, ok := l.scalarOf(expr); ok && scalar.kind == scalarBool {
		if scalar.text == boolTrueText {
			return triTrue, nil
		}

		return triFalse, nil
	}

	if tv, ok := l.pkg.TypesInfo.Types[expr]; ok && tv.Value != nil &&
		!l.containsRuntimeTargetConstant(expr) {
		if scalar, ok := scalarFromConstantValue(tv.Value); ok && scalar.kind == scalarBool {
			if scalar.text == boolTrueText {
				return triTrue, nil
			}

			return triFalse, nil
		}
	}

	switch expr := expr.(type) {
	case *ast.Ident, *ast.SelectorExpr:
		if sym, ok := l.symbolOf(expr); ok && isBoolType(sym.typ) {
			if f, ok := st.facts[sym.key]; ok {
				if f.exact != nil && f.exact.value.kind == scalarBool {
					if f.exact.value.text == boolTrueText {
						copyWhy := f.exact.why
						return triTrue, &copyWhy
					}

					copyWhy := f.exact.why

					return triFalse, &copyWhy
				}
			}
		}
	case *ast.CallExpr:
		if sym, ok := l.predicateCallSymbolOf(expr); ok {
			return l.truthSymbolScalar(
				st,
				sym,
				scalar{kind: scalarBool, text: boolTrueText},
				true,
			)
		}
	case *ast.UnaryExpr:
		if expr.Op == token.NOT {
			tri, ev := l.truth(st, expr.X)
			//exhaustive:ignore triState unknown falls through to conservative unknown.
			switch tri {
			case triTrue:
				return triFalse, ev
			case triFalse:
				return triTrue, ev
			default:
				return triUnknown, nil
			}
		}
	case *ast.BinaryExpr:
		//exhaustive:ignore token.Token includes operators not meaningful for this expression type.
		switch expr.Op {
		case token.LAND:
			left, leftEv := l.truth(st, expr.X)
			if left == triFalse {
				return triFalse, leftEv
			}

			right, rightEv := l.truth(st, expr.Y)
			if right == triFalse {
				return triFalse, rightEv
			}

			if left == triTrue && right == triTrue {
				if leftEv != nil {
					return triTrue, leftEv
				}

				return triTrue, rightEv
			}

			return triUnknown, nil
		case token.LOR:
			left, leftEv := l.truth(st, expr.X)
			if left == triTrue {
				return triTrue, leftEv
			}

			right, rightEv := l.truth(st, expr.Y)
			if right == triTrue {
				return triTrue, rightEv
			}

			if left == triFalse && right == triFalse {
				if leftEv != nil {
					return triFalse, leftEv
				}

				return triFalse, rightEv
			}

			return triUnknown, nil
		case token.EQL, token.NEQ:
			return l.truthCompare(st, expr.X, expr.Y, expr.Op == token.EQL)
		case token.GTR, token.GEQ, token.LSS, token.LEQ:
			return l.truthOrderedCompare(st, expr.X, expr.Y, expr.Op)
		}
	}

	return triUnknown, nil
}

func (l *linter) truthCompare(st state, lhs, rhs ast.Expr, wantEq bool) (triState, *evidence) {
	if sym, scalar, ok := l.symbolScalar(lhs, rhs); ok {
		return l.truthSymbolScalar(st, sym, scalar, wantEq)
	}

	if sym, scalar, ok := l.symbolScalar(rhs, lhs); ok {
		return l.truthSymbolScalar(st, sym, scalar, wantEq)
	}

	if ls, ok := l.scalarOf(lhs); ok {
		if rs, ok := l.scalarOf(rhs); ok {
			equal := ls == rs
			if wantEq == equal {
				return triTrue, nil
			}

			return triFalse, nil
		}
	}

	return triUnknown, nil
}

func (l *linter) truthOrderedCompare(
	st state,
	lhs, rhs ast.Expr,
	op token.Token,
) (triState, *evidence) {
	sym, scalar, ok := l.symbolScalar(lhs, rhs)
	if ok {
		return l.truthSymbolOrdered(st, sym, scalar, op)
	}

	sym, scalar, ok = l.symbolScalar(rhs, lhs)
	if !ok {
		return triUnknown, nil
	}

	return l.truthSymbolOrdered(st, sym, scalar, reverseOrderedOp(op))
}

func (l *linter) truthSymbolScalar(
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

func (l *linter) truthSymbolOrdered(
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
