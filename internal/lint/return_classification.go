package lint

import (
	"go/ast"
	"sort"
	"strconv"
	"strings"
)

func (l *linter) classifyReturnStates(st state, results []ast.Expr) []returnState {
	if len(results) == 0 {
		return []returnState{{state: st}}
	}

	current := []returnState{{state: st}}
	for index, expr := range results {
		next := make([]returnState, 0, len(current))
		for _, ret := range current {
			if states, ok := l.classifyBoolReturnStates(ret.state, expr); ok {
				next = append(next, appendClassifiedReturnStates(ret.kinds, index, states)...)
				continue
			}

			if states, ok := l.classifyNilReturnStates(ret.state, expr); ok {
				next = append(next, appendClassifiedReturnStates(ret.kinds, index, states)...)
				continue
			}

			next = append(next, returnState{
				state: ret.state,
				kinds: cloneReturnKinds(ret.kinds),
			})
		}

		current = dedupeReturnStates(next)
	}

	return current
}

func appendClassifiedReturnStates(
	baseKinds map[int]returnKind,
	index int,
	classified []classifiedReturn,
) []returnState {
	out := make([]returnState, 0, len(classified))
	for _, item := range classified {
		kinds := cloneReturnKinds(baseKinds)
		if kinds == nil {
			kinds = make(map[int]returnKind, 1)
		}

		kinds[index] = item.kind
		out = append(out, returnState{
			state: item.state,
			kinds: kinds,
		})
	}

	return out
}

func dedupeReturnStates(states []returnState) []returnState {
	if len(states) == 0 {
		return nil
	}

	seen := make(map[string]returnState, len(states))
	for _, st := range states {
		seen[st.state.hash()+"|"+returnKindsHash(st.kinds)] = st
	}

	out := make([]returnState, 0, len(seen))
	for _, st := range seen {
		out = append(out, st)
	}

	return out
}

func returnKindsHash(kinds map[int]returnKind) string {
	if len(kinds) == 0 {
		return ""
	}

	indices := make([]int, 0, len(kinds))
	for index := range kinds {
		indices = append(indices, index)
	}

	sort.Ints(indices)

	var sb strings.Builder
	for _, index := range indices {
		sb.WriteString(strconv.Itoa(index))
		sb.WriteByte(':')
		sb.WriteString(strconv.Itoa(int(kinds[index])))
		sb.WriteByte(';')
	}

	return sb.String()
}

func (l *linter) classifyBoolReturnStates(st state, expr ast.Expr) ([]classifiedReturn, bool) {
	if !isBoolType(l.pkg.TypesInfo.TypeOf(expr)) {
		return nil, false
	}

	out := make([]classifiedReturn, 0, returnStateCapacity)
	for _, next := range l.refineState(st, expr, true) {
		out = append(out, classifiedReturn{state: next, kind: returnBoolTrue})
	}

	for _, next := range l.refineState(st, expr, false) {
		out = append(out, classifiedReturn{state: next, kind: returnBoolFalse})
	}

	return out, true
}

func (l *linter) classifyNilReturnStates(st state, expr ast.Expr) ([]classifiedReturn, bool) {
	if value, ok := l.scalarOf(expr); ok && value.kind == scalarNil {
		return []classifiedReturn{{state: st, kind: returnNil}}, true
	}

	nilExpr := &ast.Ident{Name: nilText}
	if tri, _ := l.truthCompare(st, expr, nilExpr, true); tri == triTrue {
		return []classifiedReturn{{state: st, kind: returnNil}}, true
	} else if tri == triFalse {
		return []classifiedReturn{{state: st, kind: returnNonNil}}, true
	}

	call, ok := l.unparen(expr).(*ast.CallExpr)
	if ok {
		summary := l.summaryForCall(call)

		result, ok := summary.results[0]
		if !ok {
			return nil, false
		}

		nilContracts := normalizeGuardContracts(append([]guardContract{}, summary.always...))
		nilContracts = append(nilContracts, result.whenNil...)
		nonNilContracts := normalizeGuardContracts(append([]guardContract{}, summary.always...))

		nonNilContracts = append(nonNilContracts, result.whenNonNil...)
		if len(nilContracts) == 0 && len(nonNilContracts) == 0 {
			return nil, false
		}

		out := make([]classifiedReturn, 0, returnStateCapacity)
		for _, next := range l.applySummaryContracts(st, call, nilContracts) {
			out = append(out, classifiedReturn{state: next, kind: returnNil})
		}

		for _, next := range l.applySummaryContracts(st, call, nonNilContracts) {
			out = append(out, classifiedReturn{state: next, kind: returnNonNil})
		}

		return out, true
	}

	if !isPointerLike(l.pkg.TypesInfo.TypeOf(expr)) {
		return nil, false
	}

	return nil, false
}
