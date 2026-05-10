package lint

import (
	"go/token"
	"go/types"
	"strconv"
	"strings"
)

func isLenSymbol(sym symbol) bool {
	return strings.HasSuffix(sym.key, "|#len")
}

func lenSymbolForBase(base symbol) symbol {
	return symbol{
		key:  base.key + "|" + lenPathSegment,
		root: base.root,
		name: "len(" + base.name + ")",
		typ:  types.Typ[types.Int],
	}
}

func scalarIntValue(value scalar) (int64, bool) {
	if value.kind != scalarInt {
		return 0, false
	}

	out, err := strconv.ParseInt(value.text, 10, 64)
	if err != nil {
		return 0, false
	}

	return out, true
}

func reverseOrderedOp(op token.Token) token.Token {
	//exhaustive:ignore token.Token includes many non-ordered operators irrelevant here.
	switch op {
	case token.GTR:
		return token.LSS
	case token.GEQ:
		return token.LEQ
	case token.LSS:
		return token.GTR
	case token.LEQ:
		return token.GEQ
	default:
		return op
	}
}

func compareInts(left, right int64, op token.Token) bool {
	//exhaustive:ignore token.Token includes many non-ordered operators irrelevant here.
	switch op {
	case token.GTR:
		return left > right
	case token.GEQ:
		return left >= right
	case token.LSS:
		return left < right
	case token.LEQ:
		return left <= right
	default:
		return false
	}
}

func lenCompareFromNonNegative(limit int64, op token.Token) (bool, bool) {
	//exhaustive:ignore token.Token includes many non-ordered operators irrelevant here.
	switch op {
	case token.GTR:
		if limit < 0 {
			return true, true
		}

		if limit >= 0 {
			return false, false
		}
	case token.GEQ:
		if limit <= 0 {
			return true, true
		}
	case token.LSS:
		if limit <= 0 {
			return false, true
		}
	case token.LEQ:
		if limit < 0 {
			return false, true
		}
	}

	return false, false
}

func lenCompareFromNonZero(limit int64, op token.Token) (bool, bool) {
	//exhaustive:ignore token.Token includes many non-ordered operators irrelevant here.
	switch op {
	case token.GTR:
		if limit == 0 {
			return true, true
		}
	case token.GEQ:
		if limit <= 1 {
			return true, true
		}
	case token.LSS:
		if limit <= 1 {
			return false, true
		}
	case token.LEQ:
		if limit == 0 {
			return false, true
		}
	}

	return false, false
}

func orderedLenLimit(sym symbol, value scalar) (int64, bool) {
	if !isLenSymbol(sym) || value.kind != scalarInt {
		return 0, false
	}

	return scalarIntValue(value)
}

func lenTruthFromLowerBound(limit int64, op token.Token) (triState, bool) {
	always, ok := lenCompareFromNonNegative(limit, op)
	if !ok {
		return triUnknown, false
	}

	if always {
		return triTrue, true
	}

	return triFalse, true
}

func truthFromExactLenFact(f fact, limit int64, op token.Token) (triState, *evidence, bool) {
	if f.exact == nil || f.exact.value.kind != scalarInt {
		return triUnknown, nil, false
	}

	current, ok := scalarIntValue(f.exact.value)
	if !ok {
		return triUnknown, nil, false
	}

	copyWhy := f.exact.why
	if compareInts(current, limit, op) {
		return triTrue, &copyWhy, true
	}

	return triFalse, &copyWhy, true
}

func truthFromNonZeroLenFact(f fact, limit int64, op token.Token) (triState, *evidence) {
	ev, ok := f.not[scalar{kind: scalarInt, text: zeroIntText}.key()]
	if !ok {
		return triUnknown, nil
	}

	always, ok := lenCompareFromNonZero(limit, op)
	if !ok {
		return triUnknown, nil
	}

	copyWhy := ev
	if always {
		return triTrue, &copyWhy
	}

	return triFalse, &copyWhy
}

type lenRefinement uint8

const (
	lenRefineUnknown lenRefinement = iota
	lenRefineExactZero
	lenRefineNotZero
)

type lenCompareRule struct {
	op        token.Token
	limit     int64
	whenTrue  lenRefinement
	whenFalse lenRefinement
}

var lenCompareRules = []lenCompareRule{
	{op: token.GTR, limit: 0, whenTrue: lenRefineNotZero, whenFalse: lenRefineExactZero},
	{op: token.GEQ, limit: 1, whenTrue: lenRefineNotZero, whenFalse: lenRefineExactZero},
	{op: token.LSS, limit: 1, whenTrue: lenRefineExactZero, whenFalse: lenRefineNotZero},
	{op: token.LEQ, limit: 0, whenTrue: lenRefineExactZero, whenFalse: lenRefineNotZero},
}

func lenRefinementForCompare(limit int64, op token.Token, wantTrue bool) lenRefinement {
	for _, rule := range lenCompareRules {
		if rule.op != op || rule.limit != limit {
			continue
		}

		if wantTrue {
			return rule.whenTrue
		}

		return rule.whenFalse
	}

	return lenRefineUnknown
}

func (l *linter) refineLenCompare(
	st state,
	sym symbol,
	op token.Token,
	limit int64,
	wantTrue bool,
	pos token.Pos,
) (state, bool, bool) {
	switch lenRefinementForCompare(limit, op, wantTrue) {
	case lenRefineUnknown:
		return state{}, false, false
	case lenRefineExactZero:
		return l.refineLenToExactZero(st, sym, pos)
	case lenRefineNotZero:
		return l.refineLenToNotZero(st, sym, pos)
	}

	return state{}, false, false
}

func (l *linter) refineLenToExactZero(st state, sym symbol, pos token.Pos) (state, bool, bool) {
	value := scalar{kind: scalarInt, text: zeroIntText}
	next, ok := l.setExact(
		st,
		sym,
		value,
		evidence{pos: pos, text: l.relationText(sym, "==", value)},
	)

	return next, true, ok
}

func (l *linter) refineLenToNotZero(st state, sym symbol, pos token.Pos) (state, bool, bool) {
	value := scalar{kind: scalarInt, text: zeroIntText}
	next, ok := l.addNot(
		st,
		sym,
		value,
		evidence{pos: pos, text: l.relationText(sym, "!=", value)},
	)

	return next, true, ok
}
