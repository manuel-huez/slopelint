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

func (l *linter) truth(st state, expr ast.Expr) (triState, *evidence) {
	expr = l.unparen(expr)
	if result, known := l.constantBoolTruth(expr); known {
		return result, nil
	}

	switch expr := expr.(type) {
	case *ast.Ident, *ast.SelectorExpr:
		return l.truthBoolSymbol(st, expr)
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
		if expr.Op != token.NOT {
			return triUnknown, nil
		}

		return l.truthNegation(st, expr.X)
	case *ast.BinaryExpr:
		return l.truthBinary(st, expr)
	}

	return triUnknown, nil
}

func (l *linter) constantBoolTruth(expr ast.Expr) (triState, bool) {
	if value, ok := l.scalarOf(expr); ok && value.kind == scalarBool {
		return boolTextTruth(value.text), true
	}

	tv, ok := l.pkg.TypesInfo.Types[expr]
	if !ok || tv.Value == nil || l.containsRuntimeTargetConstant(expr) {
		return triUnknown, false
	}

	value, ok := scalarFromConstantValue(tv.Value)
	if !ok || value.kind != scalarBool {
		return triUnknown, false
	}

	return boolTextTruth(value.text), true
}

func boolTextTruth(text string) triState {
	if text == boolTrueText {
		return triTrue
	}

	return triFalse
}

func (l *linter) truthBoolSymbol(st state, expr ast.Expr) (triState, *evidence) {
	sym, ok := l.symbolOf(expr)
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

func (l *linter) truthNegation(st state, expr ast.Expr) (triState, *evidence) {
	tri, ev := l.truth(st, expr)

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

func (l *linter) truthBinary(st state, expr *ast.BinaryExpr) (triState, *evidence) {
	//exhaustive:ignore token.Token includes operators not meaningful for this expression type.
	switch expr.Op {
	case token.LAND:
		return l.truthAnd(st, expr.X, expr.Y)
	case token.LOR:
		return l.truthOr(st, expr.X, expr.Y)
	case token.EQL, token.NEQ:
		return l.truthCompare(st, expr.X, expr.Y, expr.Op == token.EQL)
	case token.GTR, token.GEQ, token.LSS, token.LEQ:
		return l.truthOrderedCompare(st, expr.X, expr.Y, expr.Op)
	default:
		return triUnknown, nil
	}
}

func (l *linter) truthAnd(st state, lhs, rhs ast.Expr) (triState, *evidence) {
	left, leftEv := l.truth(st, lhs)
	if left == triFalse {
		return triFalse, leftEv
	}

	right, rightEv := l.truth(st, rhs)
	if right == triFalse {
		return triFalse, rightEv
	}

	if left != triTrue || right != triTrue {
		return triUnknown, nil
	}

	if leftEv != nil {
		return triTrue, leftEv
	}

	return triTrue, rightEv
}

func (l *linter) truthOr(st state, lhs, rhs ast.Expr) (triState, *evidence) {
	left, leftEv := l.truth(st, lhs)
	if left == triTrue {
		return triTrue, leftEv
	}

	right, rightEv := l.truth(st, rhs)
	if right == triTrue {
		return triTrue, rightEv
	}

	if left != triFalse || right != triFalse {
		return triUnknown, nil
	}

	if leftEv != nil {
		return triFalse, leftEv
	}

	return triFalse, rightEv
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
