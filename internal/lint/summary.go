package lint

import (
	"go/ast"
	"go/types"
	"sort"
	"strconv"
	"strings"
)

const lenPathSegment = "#len"

const (
	scalarKeyParts      = 2
	returnStateCapacity = 2
)

type resultSummary struct {
	whenTrue   []guardContract
	whenFalse  []guardContract
	whenNil    []guardContract
	whenNonNil []guardContract
}

type callSummary struct {
	always  []guardContract
	results map[int]resultSummary
}

type summaryBinding struct {
	target contractTarget
	root   string
}

type summarizableFunc struct {
	decl        *ast.FuncDecl
	key         string
	bindings    []summaryBinding
	resultCount int
}

type classifiedReturn struct {
	state state
	kind  returnKind
}

func (l *linter) inferCallSummaries() {
	funcs := l.collectSummarizableFuncs()
	l.inferredFacts = make(map[string]callSummary, len(funcs))

	maxPasses := len(funcs) + 1
	for range maxPasses {
		changed := false

		for _, fn := range funcs {
			summary := l.summarizeFunc(fn)

			prev := l.inferredFacts[fn.key]
			if callSummaryEqual(prev, summary) {
				continue
			}

			l.inferredFacts[fn.key] = summary
			changed = true
		}

		if !changed {
			return
		}
	}
}

func (l *linter) collectSummarizableFuncs() []summarizableFunc {
	out := make([]summarizableFunc, 0)

	for _, file := range l.pkg.Files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}

			obj, ok := l.pkg.TypesInfo.ObjectOf(fn.Name).(*types.Func)
			if !ok || obj == nil {
				continue
			}

			sig, _ := obj.Type().(*types.Signature)
			out = append(out, summarizableFunc{
				decl:        fn,
				key:         funcObjectKey(obj),
				bindings:    l.summaryBindingsForFunc(fn),
				resultCount: signatureResultCount(sig),
			})
		}
	}

	return out
}

func signatureResultCount(sig *types.Signature) int {
	if sig == nil || sig.Results() == nil {
		return 0
	}

	return sig.Results().Len()
}

func (l *linter) summaryBindingsForFunc(fn *ast.FuncDecl) []summaryBinding {
	out := make([]summaryBinding, 0)
	appendFieldBindings := func(fields *ast.FieldList, recv bool) {
		if fields == nil {
			return
		}

		paramIndex := 0

		for _, field := range fields.List {
			for _, name := range field.Names {
				obj, ok := l.pkg.TypesInfo.ObjectOf(name).(*types.Var)
				if !ok || obj == nil {
					if !recv {
						paramIndex++
					}

					continue
				}

				binding := summaryBinding{
					target: contractTarget{param: paramIndex, recv: recv},
					root:   symbolForObject(obj).root,
				}
				out = append(out, binding)

				if !recv {
					paramIndex++
				}
			}
		}
	}

	appendFieldBindings(fn.Recv, true)
	appendFieldBindings(fn.Type.Params, false)

	return out
}

func (l *linter) summarizeFunc(fn summarizableFunc) callSummary {
	if l.hasUnstructuredJumps(fn.decl.Body) {
		return callSummary{}
	}

	res := l.execBlock(fn.decl.Body.List, []state{newState()})

	returns := append([]returnState{}, res.returns...)
	for _, st := range res.next {
		returns = append(returns, returnState{state: st})
	}

	if len(returns) == 0 {
		return callSummary{}
	}

	summary := callSummary{
		always: l.summaryContractsForReturns(returns, fn.bindings, 0, returnUnspecified),
	}

	for index := range fn.resultCount {
		result := resultSummary{
			whenTrue:   l.summaryContractsForReturns(returns, fn.bindings, index, returnBoolTrue),
			whenFalse:  l.summaryContractsForReturns(returns, fn.bindings, index, returnBoolFalse),
			whenNil:    l.summaryContractsForReturns(returns, fn.bindings, index, returnNil),
			whenNonNil: l.summaryContractsForReturns(returns, fn.bindings, index, returnNonNil),
		}
		if resultSummaryEmpty(result) {
			continue
		}

		if summary.results == nil {
			summary.results = make(map[int]resultSummary)
		}

		summary.results[index] = normalizeResultSummary(result)
	}

	return normalizeCallSummary(summary)
}

func (l *linter) summaryContractsForReturns(
	returns []returnState,
	bindings []summaryBinding,
	resultIndex int,
	kind returnKind,
) []guardContract {
	states := make([]state, 0, len(returns))
	for _, ret := range returns {
		if kind != returnUnspecified && ret.kindAt(resultIndex) != kind {
			continue
		}

		states = append(states, ret.state)
	}

	if len(states) == 0 {
		return nil
	}

	states = l.normalizeStates(states)

	merged := states[0]
	if len(states) > 1 {
		merged = l.mergeStates(states)
	}

	return normalizeGuardContracts(summaryContractsFromState(merged, bindings))
}

func summaryContractsFromState(st state, bindings []summaryBinding) []guardContract {
	out := make([]guardContract, 0)

	for _, key := range sortedFactKeys(st.facts) {
		target, ok := summaryTargetForKey(key, bindings)
		if !ok {
			continue
		}

		f := st.facts[key]
		if f.exact != nil {
			out = append(out, guardContract{
				target: target,
				value:  f.exact.value,
				wantEq: true,
			})
		}

		for _, notKey := range sortedEvidenceKeys(f.not) {
			value := st.facts[key].notValue(notKey)
			out = append(out, guardContract{
				target: target,
				value:  value,
				wantEq: false,
			})
		}
	}

	return out
}

func summaryTargetForKey(key string, bindings []summaryBinding) (contractTarget, bool) {
	for _, binding := range bindings {
		if key != binding.root && !strings.HasPrefix(key, binding.root+"|") {
			continue
		}

		target := binding.target

		suffix := strings.TrimPrefix(key, binding.root)
		if suffix == "" {
			return target, true
		}

		path := strings.Split(strings.TrimPrefix(suffix, "|"), "|")
		if !validSummaryPath(path) {
			return contractTarget{}, false
		}

		target.path = append([]string(nil), path...)

		return target, true
	}

	return contractTarget{}, false
}

func validSummaryPath(path []string) bool {
	for i, segment := range path {
		if segment == "" {
			return false
		}

		if segment != lenPathSegment {
			continue
		}

		if i != len(path)-1 {
			return false
		}
	}

	return true
}

func normalizeGuardContracts(contracts []guardContract) []guardContract {
	if len(contracts) == 0 {
		return nil
	}

	seen := make(map[string]guardContract, len(contracts))
	for _, contract := range contracts {
		seen[guardContractKey(contract)] = contract
	}

	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	out := make([]guardContract, 0, len(keys))
	for _, key := range keys {
		out = append(out, seen[key])
	}

	return out
}

func normalizeResultSummary(summary resultSummary) resultSummary {
	summary.whenTrue = normalizeGuardContracts(summary.whenTrue)
	summary.whenFalse = normalizeGuardContracts(summary.whenFalse)
	summary.whenNil = normalizeGuardContracts(summary.whenNil)
	summary.whenNonNil = normalizeGuardContracts(summary.whenNonNil)

	return summary
}

func normalizeCallSummary(summary callSummary) callSummary {
	summary.always = normalizeGuardContracts(summary.always)
	if len(summary.results) == 0 {
		summary.results = nil
		return summary
	}

	out := make(map[int]resultSummary, len(summary.results))
	for index, result := range summary.results {
		result = normalizeResultSummary(result)
		if resultSummaryEmpty(result) {
			continue
		}

		out[index] = result
	}

	if len(out) == 0 {
		summary.results = nil
		return summary
	}

	summary.results = out

	return summary
}

func resultSummaryEmpty(summary resultSummary) bool {
	return len(summary.whenTrue) == 0 &&
		len(summary.whenFalse) == 0 &&
		len(summary.whenNil) == 0 &&
		len(summary.whenNonNil) == 0
}

func guardContractKey(contract guardContract) string {
	target := "param:" + strconv.Itoa(contract.target.param)
	if contract.target.recv {
		target = "recv"
	}

	return target + "|" + strings.Join(contract.target.path, ".") + "|" + contract.value.key() +
		"|" + strconv.FormatBool(contract.wantEq)
}

func callSummaryEqual(left, right callSummary) bool {
	if !guardContractsEqual(left.always, right.always) {
		return false
	}

	leftKeys := sortedSummaryResultIndices(left.results)

	rightKeys := sortedSummaryResultIndices(right.results)
	if len(leftKeys) != len(rightKeys) {
		return false
	}

	for i := range leftKeys {
		if leftKeys[i] != rightKeys[i] {
			return false
		}

		if !resultSummaryEqual(left.results[leftKeys[i]], right.results[rightKeys[i]]) {
			return false
		}
	}

	return true
}

func resultSummaryEqual(left, right resultSummary) bool {
	return guardContractsEqual(left.whenTrue, right.whenTrue) &&
		guardContractsEqual(left.whenFalse, right.whenFalse) &&
		guardContractsEqual(left.whenNil, right.whenNil) &&
		guardContractsEqual(left.whenNonNil, right.whenNonNil)
}

func sortedSummaryResultIndices(results map[int]resultSummary) []int {
	if len(results) == 0 {
		return nil
	}

	indices := make([]int, 0, len(results))
	for index := range results {
		indices = append(indices, index)
	}

	sort.Ints(indices)

	return indices
}

func guardContractsEqual(left, right []guardContract) bool {
	if len(left) != len(right) {
		return false
	}

	for i := range left {
		if guardContractKey(left[i]) != guardContractKey(right[i]) {
			return false
		}
	}

	return true
}

func (f fact) notValue(key string) scalar {
	parts := strings.SplitN(key, ":", scalarKeyParts)
	if len(parts) != scalarKeyParts {
		return scalar{}
	}

	kind, err := strconv.Atoi(parts[0])
	if err != nil {
		return scalar{}
	}

	return scalar{kind: scalarKind(kind), text: parts[1]}
}

func (l *linter) summaryForCall(call *ast.CallExpr) callSummary {
	obj, key, ok := l.calledFunc(call)
	if !ok {
		return callSummary{}
	}

	summary := l.inferredFacts[key]

	if len(summary.always) == 0 &&
		len(sortedSummaryResultIndices(summary.results)) == 0 &&
		l.externalSummary != nil {
		if external, ok := l.externalSummary(obj); ok {
			summary = external
		}
	}

	return l.summaryWithExplicit(key, summary)
}

func (l *linter) summaryWithExplicit(key string, summary callSummary) callSummary {
	if explicit := l.explicitFacts[key]; len(explicit) != 0 {
		summary.always = normalizeGuardContracts(append(summary.always, explicit...))
	}

	return normalizeCallSummary(summary)
}

func (l *linter) applySummaryContracts(
	st state,
	call *ast.CallExpr,
	contracts []guardContract,
) []state {
	return applyContractSequence(
		st,
		contracts,
		l.normalizeStates,
		func(currentState state, contract guardContract) []state {
			sym, ok := l.symbolForContractTarget(call, contract.target)
			if !ok {
				return []state{currentState}
			}

			ev := evidence{
				pos:  call.Pos(),
				text: l.contractEvidenceText(sym, contract, call),
			}

			var (
				next state
				ok2  bool
			)

			if contract.wantEq {
				next, ok2 = l.setExact(currentState, sym, contract.value, ev)
			} else {
				next, ok2 = l.addNot(currentState, sym, contract.value, ev)
			}

			if !ok2 {
				return nil
			}

			return []state{next}
		},
	)
}

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

func (l *linter) refineCallExpr(st state, call *ast.CallExpr, wantTrue bool) ([]state, bool) {
	if !isBoolType(l.pkg.TypesInfo.TypeOf(call)) {
		return nil, false
	}

	summary := l.summaryForCall(call)

	result, ok := summary.results[0]
	if !ok {
		return nil, false
	}

	contracts := append([]guardContract{}, summary.always...)
	if wantTrue {
		contracts = append(contracts, result.whenTrue...)
	} else {
		contracts = append(contracts, result.whenFalse...)
	}

	return l.applySummaryContracts(st, call, normalizeGuardContracts(contracts)), true
}

//nolint:cyclop // Scalar-based call refinement branches by supported result kind.
func (l *linter) refineCallScalar(
	st state,
	left, right ast.Expr,
	wantEq bool,
) ([]state, bool) {
	call, scalar, ok := callScalar(left, right, l)
	if !ok {
		call, scalar, ok = callScalar(right, left, l)
		if !ok {
			return nil, false
		}
	}

	summary := l.summaryForCall(call)

	result, ok := summary.results[0]
	if !ok {
		return nil, false
	}

	contracts := append([]guardContract{}, summary.always...)

	switch {
	case scalar.kind == scalarBool && scalar.text == boolTrueText:
		if wantEq {
			contracts = append(contracts, result.whenTrue...)
		} else {
			contracts = append(contracts, result.whenFalse...)
		}
	case scalar.kind == scalarBool && scalar.text == boolFalseText:
		if wantEq {
			contracts = append(contracts, result.whenFalse...)
		} else {
			contracts = append(contracts, result.whenTrue...)
		}
	case scalar.kind == scalarNil:
		if wantEq {
			contracts = append(contracts, result.whenNil...)
		} else {
			contracts = append(contracts, result.whenNonNil...)
		}
	default:
		return nil, false
	}

	return l.applySummaryContracts(st, call, normalizeGuardContracts(contracts)), true
}

func callScalar(callExpr, scalarExpr ast.Expr, l *linter) (*ast.CallExpr, scalar, bool) {
	call, ok := l.unparen(callExpr).(*ast.CallExpr)
	if !ok {
		return nil, scalar{}, false
	}

	value, ok := l.scalarOf(scalarExpr)
	if !ok {
		return nil, scalar{}, false
	}

	return call, value, true
}
