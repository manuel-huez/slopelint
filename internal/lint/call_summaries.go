package lint

import (
	"go/ast"
	"go/types"
	"sort"
	"strconv"
	"strings"
)

const (
	lenPathSegment             = "#len"
	predicatePathSegmentPrefix = "#pred:"
)

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

			bindings := l.summaryBindingsForFunc(fn)
			if len(bindings) == 0 {
				continue
			}

			out = append(out, summarizableFunc{
				decl:        fn,
				key:         funcObjectKey(obj),
				bindings:    bindings,
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

		for fieldIndex, field := range fields.List {
			_, variadic := field.Type.(*ast.Ellipsis)
			variadic = !recv && variadic && fieldIndex == len(fields.List)-1

			for _, name := range field.Names {
				obj, ok := l.pkg.TypesInfo.ObjectOf(name).(*types.Var)
				if !ok || obj == nil {
					if !recv {
						paramIndex++
					}

					continue
				}

				binding := summaryBinding{
					target: contractTarget{
						param:    paramIndex,
						recv:     recv,
						variadic: variadic,
					},
					root: symbolForObject(obj).root,
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
	// Deferred writes run after return evaluation, so pre-defer facts are not call guarantees.
	if l.hasUnsupportedJumps(fn.decl.Body) || functionHasDefer(fn.decl.Body) {
		return callSummary{}
	}

	var res flowResult

	l.withReportsSuppressed(func() {
		res = l.execBlock(fn.decl.Body.List, []state{newState()})
	})

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

func functionHasDefer(body *ast.BlockStmt) bool {
	found := false

	ast.Inspect(body, func(node ast.Node) bool {
		if found {
			return false
		}

		switch node.(type) {
		case *ast.FuncLit:
			return false
		case *ast.DeferStmt:
			found = true
			return false
		default:
			return true
		}
	})

	return found
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

	for _, key := range sortedMapKeys(st.facts) {
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

		for _, notKey := range sortedMapKeys(f.not) {
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

		if strings.HasPrefix(segment, predicatePathSegmentPrefix) {
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

		leftResult := left.results[leftKeys[i]]
		rightResult := right.results[rightKeys[i]]

		if !guardContractsEqual(leftResult.whenTrue, rightResult.whenTrue) ||
			!guardContractsEqual(leftResult.whenFalse, rightResult.whenFalse) ||
			!guardContractsEqual(leftResult.whenNil, rightResult.whenNil) ||
			!guardContractsEqual(leftResult.whenNonNil, rightResult.whenNonNil) {
			return false
		}
	}

	return true
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

	selected, ok := result.contractsForScalar(scalar, wantEq)
	if !ok {
		return nil, false
	}

	contracts := append([]guardContract{}, summary.always...)
	contracts = append(contracts, selected...)

	return l.applySummaryContracts(st, call, normalizeGuardContracts(contracts)), true
}

func (result resultSummary) contractsForScalar(
	value scalar,
	wantEqual bool,
) ([]guardContract, bool) {
	switch value.kind {
	case scalarBool:
		if value.text != boolTrueText && value.text != boolFalseText {
			return nil, false
		}

		wantTrue := value.text == boolTrueText
		if !wantEqual {
			wantTrue = !wantTrue
		}

		if wantTrue {
			return result.whenTrue, true
		}

		return result.whenFalse, true
	case scalarNil:
		if wantEqual {
			return result.whenNil, true
		}

		return result.whenNonNil, true
	case scalarInvalid, scalarString, scalarInt:
		return nil, false
	}

	return nil, false
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
