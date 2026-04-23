package lint

import (
	"go/ast"
	"go/token"
	"slices"
	"sort"
	"strconv"
	"strings"
)

type boundContract struct {
	symKey  string
	symName string
	value   scalar
	wantEq  bool
	why     evidence
}

type resultBinding struct {
	roots      []string
	whenTrue   []boundContract
	whenFalse  []boundContract
	whenNil    []boundContract
	whenNonNil []boundContract
}

func (b resultBinding) clone() resultBinding {
	return resultBinding{
		roots:      append([]string(nil), b.roots...),
		whenTrue:   cloneBoundContracts(b.whenTrue),
		whenFalse:  cloneBoundContracts(b.whenFalse),
		whenNil:    cloneBoundContracts(b.whenNil),
		whenNonNil: cloneBoundContracts(b.whenNonNil),
	}
}

func cloneBoundContracts(in []boundContract) []boundContract {
	if len(in) == 0 {
		return nil
	}

	out := make([]boundContract, len(in))
	copy(out, in)

	return out
}

func bindingHash(b resultBinding) string {
	var sb strings.Builder

	for _, root := range b.roots {
		sb.WriteString(root)
		sb.WriteByte(',')
	}

	sb.WriteByte('|')
	appendBoundContractsHash(&sb, b.whenTrue)
	sb.WriteByte('|')
	appendBoundContractsHash(&sb, b.whenFalse)
	sb.WriteByte('|')
	appendBoundContractsHash(&sb, b.whenNil)
	sb.WriteByte('|')
	appendBoundContractsHash(&sb, b.whenNonNil)

	return sb.String()
}

func appendBoundContractsHash(sb *strings.Builder, contracts []boundContract) {
	for _, contract := range contracts {
		sb.WriteString(contract.symKey)
		sb.WriteByte(':')
		sb.WriteString(contract.value.key())
		sb.WriteByte(':')
		sb.WriteString(strconv.FormatBool(contract.wantEq))
		sb.WriteByte(':')
		sb.WriteString(contract.why.text)
		sb.WriteByte(':')
		sb.WriteString(strconv.Itoa(int(contract.why.pos)))
		sb.WriteByte(';')
	}
}

func bindingConditionContracts(binding resultBinding, kind returnKind) []boundContract {
	switch kind {
	case returnUnspecified:
		return nil
	case returnBoolTrue:
		return binding.whenTrue
	case returnBoolFalse:
		return binding.whenFalse
	case returnNil:
		return binding.whenNil
	case returnNonNil:
		return binding.whenNonNil
	default:
		return nil
	}
}

func (l *linter) applyBoundContracts(st state, contracts []boundContract) []state {
	return applyContractSequence(
		st,
		contracts,
		dedupeStates,
		func(currentState state, contract boundContract) []state {
			var (
				next state
				ok   bool
			)

			sym := symbol{
				key:  contract.symKey,
				name: contract.symName,
				root: contract.symKey,
			}
			if contract.wantEq {
				next, ok = l.setExact(currentState, sym, contract.value, contract.why)
			} else {
				next, ok = l.addNot(currentState, sym, contract.value, contract.why)
			}

			if !ok {
				return nil
			}

			return []state{next}
		},
	)
}

func bindingAffectedByPrefix(binding resultBinding, prefix string) bool {
	for _, root := range binding.roots {
		if isSameOrChild(root, prefix) || isSameOrChild(prefix, root) {
			return true
		}
	}

	return false
}

func removeBindingsForPrefix(st *state, prefix string) {
	if len(st.bindings) == 0 {
		return
	}

	for key, binding := range st.bindings {
		if isSameOrChild(key, prefix) || bindingAffectedByPrefix(binding, prefix) {
			delete(st.bindings, key)
		}
	}
}

func sortedBindingKeys(bindings map[string]resultBinding) []string {
	keys := make([]string, 0, len(bindings))
	for key := range bindings {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	return keys
}

func normalizeResultBinding(binding resultBinding) resultBinding {
	slices.Sort(binding.roots)
	binding.roots = slices.Compact(binding.roots)
	binding.whenTrue = normalizeBoundContracts(binding.whenTrue)
	binding.whenFalse = normalizeBoundContracts(binding.whenFalse)
	binding.whenNil = normalizeBoundContracts(binding.whenNil)
	binding.whenNonNil = normalizeBoundContracts(binding.whenNonNil)

	return binding
}

func normalizeBoundContracts(contracts []boundContract) []boundContract {
	if len(contracts) == 0 {
		return nil
	}

	sort.Slice(contracts, func(i, j int) bool {
		return boundContractKey(contracts[i]) < boundContractKey(contracts[j])
	})

	out := contracts[:0]

	var prev string

	for _, contract := range contracts {
		key := boundContractKey(contract)
		if len(out) != 0 && key == prev {
			continue
		}

		out = append(out, contract)
		prev = key
	}

	return out
}

func boundContractKey(contract boundContract) string {
	return contract.symKey + "|" + contract.value.key() + "|" +
		strconv.FormatBool(contract.wantEq) + "|" + contract.why.text + "|" +
		strconv.Itoa(int(contract.why.pos))
}

func dedupeStates(states []state) []state {
	if len(states) == 0 {
		return nil
	}

	seen := make(map[string]state, len(states))
	for _, st := range states {
		seen[st.hash()] = st
	}

	out := make([]state, 0, len(seen))
	for _, st := range seen {
		out = append(out, st)
	}

	return out
}

func instantiateResultBinding(
	l *linter,
	call *ast.CallExpr,
	result resultSummary,
	kindPos token.Pos,
) (resultBinding, bool) {
	binding := resultBinding{}
	addContracts := func(dst *[]boundContract, contracts []guardContract) bool {
		for _, contract := range contracts {
			sym, ok := l.symbolForContractTarget(call, contract.target)
			if !ok {
				return false
			}

			binding.roots = append(binding.roots, sym.root)
			*dst = append(*dst, boundContract{
				symKey:  sym.key,
				symName: sym.name,
				value:   contract.value,
				wantEq:  contract.wantEq,
				why: evidence{
					pos:  kindPos,
					text: l.contractEvidenceText(sym, contract, call),
				},
			})
		}

		return true
	}

	if !addContracts(&binding.whenTrue, result.whenTrue) {
		return resultBinding{}, false
	}

	if !addContracts(&binding.whenFalse, result.whenFalse) {
		return resultBinding{}, false
	}

	if !addContracts(&binding.whenNil, result.whenNil) {
		return resultBinding{}, false
	}

	if !addContracts(&binding.whenNonNil, result.whenNonNil) {
		return resultBinding{}, false
	}

	return normalizeResultBinding(binding), true
}

func (l *linter) instantiateBindingForCall(
	call *ast.CallExpr,
	resultIndex int,
) (resultBinding, bool) {
	summary := l.summaryForCall(call)

	result, ok := summary.results[resultIndex]
	if !ok || resultSummaryEmpty(result) {
		return resultBinding{}, false
	}

	return instantiateResultBinding(l, call, result, call.Pos())
}

func (l *linter) applyBindingCondition(st state, sym symbol, kind returnKind) []state {
	binding, ok := st.bindings[sym.key]
	if !ok {
		return []state{st}
	}

	return l.applyBoundContracts(st, bindingConditionContracts(binding, kind))
}

func (l *linter) applyBindingForScalar(st state, sym symbol, value scalar, wantEq bool) []state {
	switch {
	case value.kind == scalarBool && value.text == boolTrueText:
		if wantEq {
			return l.applyBindingCondition(st, sym, returnBoolTrue)
		}

		return l.applyBindingCondition(st, sym, returnBoolFalse)
	case value.kind == scalarBool && value.text == boolFalseText:
		if wantEq {
			return l.applyBindingCondition(st, sym, returnBoolFalse)
		}

		return l.applyBindingCondition(st, sym, returnBoolTrue)
	case value.kind == scalarNil:
		if wantEq {
			return l.applyBindingCondition(st, sym, returnNil)
		}

		return l.applyBindingCondition(st, sym, returnNonNil)
	default:
		return []state{st}
	}
}
