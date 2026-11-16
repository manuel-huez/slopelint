package lint

import (
	"go/ast"
	"sort"
	"strconv"
	"strings"
)

func (c conditionEval) classifyReturnStates(st state, results []ast.Expr) []returnState {
	if len(results) == 0 {
		return []returnState{{state: st}}
	}

	current := []returnState{{state: st}}
	for index, expr := range results {
		next := make([]returnState, 0, len(current))
		for _, ret := range current {
			if states, ok := c.classifyBoolReturnStates(ret.state, expr); ok {
				next = append(next, appendClassifiedReturnStates(ret.kinds, index, states)...)
				continue
			}

			if states, ok := c.classifyNilReturnStates(ret.state, expr); ok {
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

	// Keep return evidence stable across runs by preserving first-seen order.
	seen := make(map[string]struct{}, len(states))
	out := make([]returnState, 0, len(states))

	for _, st := range states {
		key := st.state.hash() + "|" + returnKindsHash(st.kinds)
		if _, ok := seen[key]; ok {
			continue
		}

		seen[key] = struct{}{}

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

func (c conditionEval) classifyBoolReturnStates(
	st state,
	expr ast.Expr,
) ([]classifiedReturn, bool) {
	if !isBoolType(c.q.info.TypeOf(expr)) {
		return nil, false
	}

	out := make([]classifiedReturn, 0, returnStateCapacity)
	for _, next := range c.refineState(st, expr, true) {
		out = append(out, classifiedReturn{state: next, kind: returnBoolTrue})
	}

	for _, next := range c.refineState(st, expr, false) {
		out = append(out, classifiedReturn{state: next, kind: returnBoolFalse})
	}

	return out, true
}

func (c conditionEval) classifyNilReturnStates(st state, expr ast.Expr) ([]classifiedReturn, bool) {
	if value, ok := c.q.scalarOf(expr); ok && value.kind == scalarNil {
		return []classifiedReturn{{state: st, kind: returnNil}}, true
	}

	nilExpr := &ast.Ident{Name: nilText}
	if tri, _ := c.truthCompare(st, expr, nilExpr, true); tri == triTrue {
		return []classifiedReturn{{state: st, kind: returnNil}}, true
	} else if tri == triFalse {
		return []classifiedReturn{{state: st, kind: returnNonNil}}, true
	}

	call, ok := ast.Unparen(expr).(*ast.CallExpr)
	if ok {
		summary := c.calls.forCall(call)

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
		for _, next := range c.calls.apply(st, call, nilContracts) {
			out = append(out, classifiedReturn{state: next, kind: returnNil})
		}

		for _, next := range c.calls.apply(st, call, nonNilContracts) {
			out = append(out, classifiedReturn{state: next, kind: returnNonNil})
		}

		return out, true
	}

	return nil, false
}
