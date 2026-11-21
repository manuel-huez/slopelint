package lint

import (
	"go/ast"
	"go/types"
	"slices"
	"sort"
	"strconv"
	"strings"

	smellcheck "github.com/manuel-huez/slopelint/internal/lint/smells"
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
	always         []guardContract
	results        map[int]resultSummary
	parallelParams []int
}

type summaryBinding struct {
	target contractTarget
	root   string
}

type summarizableFunc struct {
	decl        *ast.FuncDecl
	obj         *types.Func
	key         string
	bindings    []summaryBinding
	resultCount int
	parallel    []int
}

type classifiedReturn struct {
	state state
	kind  returnKind
}

// callFacts holds call summaries for one package: explicit contracts,
// summaries inferred from function bodies, and imported summaries. Inference
// fills inferred in place, so later functions in a pass see earlier results.
type callFacts struct {
	q        *exprQuery
	budget   stateBudget
	explicit map[string][]guardContract
	inferred map[string]callSummary
	external func(*types.Func) (callSummary, bool)
}

// inferCallSummaries iterates to a fixed point because a summary can depend on
// summaries of functions declared later.
func (l *linter) inferCallSummaries(walker *pathWalker) {
	maxPasses := len(l.funcs) + 1
	for range maxPasses {
		changed := false

		for _, fn := range l.funcs {
			summary := walker.summarize(fn)

			prev := l.calls.inferred[fn.key]
			if callSummaryEqual(prev, summary) {
				continue
			}

			l.calls.inferred[fn.key] = summary
			changed = true
		}

		if !changed {
			return
		}
	}
}

func summarizableFuncs(pkg *LoadedPackage) []summarizableFunc {
	out := make([]summarizableFunc, 0)

	for _, file := range pkg.Files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}

			bindings := summaryBindingsForFunc(pkg.TypesInfo, fn)
			if len(bindings) == 0 {
				continue
			}

			obj := pkg.TypesInfo.ObjectOf(fn.Name).(*types.Func)
			out = append(out, summarizableFunc{
				decl:        fn,
				obj:         obj,
				key:         smellcheck.FuncObjectKey(obj),
				bindings:    bindings,
				resultCount: obj.Signature().Results().Len(),
			})
		}
	}

	return out
}

func summaryBindingsForFunc(info *types.Info, fn *ast.FuncDecl) []summaryBinding {
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
				obj, ok := info.ObjectOf(name).(*types.Var)
				if !ok {
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

func (w *pathWalker) summarize(fn summarizableFunc) callSummary {
	// Deferred writes run after return evaluation, so pre-defer facts are not call guarantees.
	if hasUnsupportedJumps(fn.decl.Body) || functionHasDefer(fn.decl.Body) {
		return callSummary{}
	}

	res := w.execBlock(fn.decl.Body.List, []state{newState()})

	returns := append([]returnState{}, res.returns...)
	for _, st := range res.next {
		returns = append(returns, returnState{state: st})
	}

	if len(returns) == 0 {
		return callSummary{}
	}

	summary := callSummary{
		always: w.summaryContractsForReturns(returns, fn.bindings, 0, returnUnspecified),
	}

	for index := range fn.resultCount {
		result := resultSummary{
			whenTrue:   w.summaryContractsForReturns(returns, fn.bindings, index, returnBoolTrue),
			whenFalse:  w.summaryContractsForReturns(returns, fn.bindings, index, returnBoolFalse),
			whenNil:    w.summaryContractsForReturns(returns, fn.bindings, index, returnNil),
			whenNonNil: w.summaryContractsForReturns(returns, fn.bindings, index, returnNonNil),
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

func (w *pathWalker) summaryContractsForReturns(
	returns []returnState,
	bindings []summaryBinding,
	resultIndex int,
	kind returnKind,
) []guardContract {
	states := make([]state, 0, len(returns))
	for _, ret := range returns {
		if kind != returnUnspecified && ret.kinds[resultIndex] != kind {
			continue
		}

		states = append(states, ret.state)
	}

	if len(states) == 0 {
		return nil
	}

	states = w.budget.normalize(states)

	merged := states[0]
	if len(states) > 1 {
		merged = mergeStates(states)
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
	if !guardContractsEqual(left.always, right.always) ||
		!slices.Equal(left.parallelParams, right.parallelParams) {
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

func (c *callFacts) forCall(call *ast.CallExpr) callSummary {
	obj, key, ok := c.q.calledFunc(call)
	if !ok {
		return callSummary{}
	}

	summary := c.inferred[key]

	if len(summary.always) == 0 && len(summary.results) == 0 {
		if external, ok := c.external(obj); ok {
			summary = external
		}
	}

	return c.withExplicit(key, summary)
}

func (c *callFacts) exported(fn summarizableFunc) callSummary {
	summary := c.withExplicit(fn.key, c.inferred[fn.key])
	summary.parallelParams = fn.parallel

	return summary
}

func (c *callFacts) withExplicit(key string, summary callSummary) callSummary {
	if explicit := c.explicit[key]; len(explicit) != 0 {
		summary.always = normalizeGuardContracts(append(summary.always, explicit...))
	}

	return normalizeCallSummary(summary)
}

// applyAlways applies the contracts call guarantees on every return; false
// means they contradict st.
func (c *callFacts) applyAlways(st state, call *ast.CallExpr) (state, bool) {
	nextStates := c.apply(st, call, c.forCall(call).always)
	if len(nextStates) == 0 {
		return state{}, false
	}

	return nextStates[0], true
}

func (c *callFacts) apply(
	st state,
	call *ast.CallExpr,
	contracts []guardContract,
) []state {
	return applyContractSequence(
		st,
		contracts,
		c.budget.normalize,
		func(currentState state, contract guardContract) []state {
			sym, ok := c.q.symbolForContractTarget(call, contract.target)
			if !ok {
				return []state{currentState}
			}

			ev := evidence{
				pos:  call.Pos(),
				text: c.q.contractEvidenceText(sym, contract, call),
			}

			var (
				next state
				ok2  bool
			)

			if contract.wantEq {
				next, ok2 = setExact(currentState, sym, contract.value, ev)
			} else {
				next, ok2 = addNot(currentState, sym, contract.value, ev)
			}

			if !ok2 {
				return nil
			}

			return []state{next}
		},
	)
}

func (c *callFacts) refineCall(st state, call *ast.CallExpr, wantTrue bool) ([]state, bool) {
	if !isBoolType(c.q.info.TypeOf(call)) {
		return nil, false
	}

	summary := c.forCall(call)

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

	return c.apply(st, call, normalizeGuardContracts(contracts)), true
}

func (c *callFacts) refineCallScalar(
	st state,
	left, right ast.Expr,
	wantEq bool,
) ([]state, bool) {
	call, ok := ast.Unparen(left).(*ast.CallExpr)
	value, valueOK := c.q.scalarOf(right)

	if !ok || !valueOK {
		call, ok = ast.Unparen(right).(*ast.CallExpr)
		value, valueOK = c.q.scalarOf(left)

		if !ok || !valueOK {
			return nil, false
		}
	}

	summary := c.forCall(call)

	result, ok := summary.results[0]
	if !ok {
		return nil, false
	}

	selected, ok := result.contractsForScalar(value, wantEq)
	if !ok {
		return nil, false
	}

	contracts := append([]guardContract{}, summary.always...)
	contracts = append(contracts, selected...)

	return c.apply(st, call, normalizeGuardContracts(contracts)), true
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
