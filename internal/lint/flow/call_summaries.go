package flow

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

type ResultSummary struct {
	WhenTrue   []GuardContract
	WhenFalse  []GuardContract
	WhenNil    []GuardContract
	WhenNonNil []GuardContract
}

type CallSummary struct {
	Always         []GuardContract
	Results        map[int]ResultSummary
	ParallelParams []int
}

type summaryBinding struct {
	target ContractTarget
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
	explicit map[string][]GuardContract
	inferred map[string]CallSummary
	external func(*types.Func) (CallSummary, bool)
}

func (c *callFacts) updateInferred(key string, summary CallSummary) bool {
	if CallSummaryEqual(c.inferred[key], summary) {
		return false
	}

	c.inferred[key] = summary

	return true
}

// inferCallSummaries iterates to a fixed point because a summary can depend on
// summaries of functions declared later.
func inferCallSummaries(funcs []summarizableFunc, calls *callFacts, walker *pathWalker) {
	maxPasses := len(funcs) + 1
	for range maxPasses {
		changed := false

		for _, fn := range funcs {
			if calls.updateInferred(fn.key, walker.summarize(fn)) {
				changed = true
			}
		}

		if !changed {
			return
		}
	}
}

func summarizableFuncs(pkg *Package) []summarizableFunc {
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
					target: ContractTarget{
						Param:    paramIndex,
						Recv:     recv,
						Variadic: variadic,
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

func (w *pathWalker) summarize(fn summarizableFunc) CallSummary {
	// Deferred writes run after return evaluation, so pre-defer facts are not call guarantees.
	if hasUnsupportedJumps(fn.decl.Body) || functionHasDefer(fn.decl.Body) {
		return CallSummary{}
	}

	res := w.execBlock(fn.decl.Body.List, []state{newState()})

	returns := append([]returnState{}, res.returns...)
	for _, st := range res.next {
		returns = append(returns, returnState{state: st})
	}

	if len(returns) == 0 {
		return CallSummary{}
	}

	summary := CallSummary{
		Always: w.summaryContractsForReturns(returns, fn.bindings, 0, returnUnspecified),
	}

	for index := range fn.resultCount {
		result := ResultSummary{
			WhenTrue:   w.summaryContractsForReturns(returns, fn.bindings, index, returnBoolTrue),
			WhenFalse:  w.summaryContractsForReturns(returns, fn.bindings, index, returnBoolFalse),
			WhenNil:    w.summaryContractsForReturns(returns, fn.bindings, index, returnNil),
			WhenNonNil: w.summaryContractsForReturns(returns, fn.bindings, index, returnNonNil),
		}
		if resultSummaryEmpty(result) {
			continue
		}

		if summary.Results == nil {
			summary.Results = make(map[int]ResultSummary)
		}

		summary.Results[index] = NormalizeResultSummary(result)
	}

	return NormalizeCallSummary(summary)
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
) []GuardContract {
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

	return NormalizeGuardContracts(summaryContractsFromState(merged, bindings))
}

func summaryContractsFromState(st state, bindings []summaryBinding) []GuardContract {
	out := make([]GuardContract, 0)

	for _, key := range sortedMapKeys(st.facts) {
		target, ok := summaryTargetForKey(key, bindings)
		if !ok {
			continue
		}

		f := st.facts[key]
		if f.exact != nil {
			out = append(out, GuardContract{
				Target: target,
				Value:  f.exact.value,
				WantEq: true,
			})
		}

		for _, notKey := range sortedMapKeys(f.not) {
			value := st.facts[key].notValue(notKey)
			out = append(out, GuardContract{
				Target: target,
				Value:  value,
				WantEq: false,
			})
		}
	}

	return out
}

func summaryTargetForKey(key string, bindings []summaryBinding) (ContractTarget, bool) {
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
			return ContractTarget{}, false
		}

		target.Path = append([]string(nil), path...)

		return target, true
	}

	return ContractTarget{}, false
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
func NormalizeGuardContracts(contracts []GuardContract) []GuardContract {
	if len(contracts) == 0 {
		return nil
	}

	seen := make(map[string]GuardContract, len(contracts))
	for _, contract := range contracts {
		seen[guardContractKey(contract)] = contract
	}

	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	out := make([]GuardContract, 0, len(keys))
	for _, key := range keys {
		out = append(out, seen[key])
	}

	return out
}

func NormalizeResultSummary(summary ResultSummary) ResultSummary {
	summary.WhenTrue = NormalizeGuardContracts(summary.WhenTrue)
	summary.WhenFalse = NormalizeGuardContracts(summary.WhenFalse)
	summary.WhenNil = NormalizeGuardContracts(summary.WhenNil)
	summary.WhenNonNil = NormalizeGuardContracts(summary.WhenNonNil)

	return summary
}

func NormalizeCallSummary(summary CallSummary) CallSummary {
	summary.Always = NormalizeGuardContracts(summary.Always)
	if len(summary.Results) == 0 {
		summary.Results = nil
		return summary
	}

	out := make(map[int]ResultSummary, len(summary.Results))
	for index, result := range summary.Results {
		result = NormalizeResultSummary(result)
		if resultSummaryEmpty(result) {
			continue
		}

		out[index] = result
	}

	if len(out) == 0 {
		summary.Results = nil
		return summary
	}

	summary.Results = out

	return summary
}

func resultSummaryEmpty(summary ResultSummary) bool {
	return len(summary.WhenTrue) == 0 &&
		len(summary.WhenFalse) == 0 &&
		len(summary.WhenNil) == 0 &&
		len(summary.WhenNonNil) == 0
}

func guardContractKey(contract GuardContract) string {
	target := "param:" + strconv.Itoa(contract.Target.Param)
	if contract.Target.Recv {
		target = "recv"
	}

	return target + "|" + strings.Join(contract.Target.Path, ".") + "|" + contract.Value.key() +
		"|" + strconv.FormatBool(contract.WantEq)
}

func CallSummaryEqual(left, right CallSummary) bool {
	if !guardContractsEqual(left.Always, right.Always) ||
		!slices.Equal(left.ParallelParams, right.ParallelParams) {
		return false
	}

	leftKeys := SortedSummaryResultIndices(left.Results)

	rightKeys := SortedSummaryResultIndices(right.Results)
	if len(leftKeys) != len(rightKeys) {
		return false
	}

	for i := range leftKeys {
		if leftKeys[i] != rightKeys[i] {
			return false
		}

		leftResult := left.Results[leftKeys[i]]
		rightResult := right.Results[rightKeys[i]]

		if !guardContractsEqual(leftResult.WhenTrue, rightResult.WhenTrue) ||
			!guardContractsEqual(leftResult.WhenFalse, rightResult.WhenFalse) ||
			!guardContractsEqual(leftResult.WhenNil, rightResult.WhenNil) ||
			!guardContractsEqual(leftResult.WhenNonNil, rightResult.WhenNonNil) {
			return false
		}
	}

	return true
}

func SortedSummaryResultIndices(results map[int]ResultSummary) []int {
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

func guardContractsEqual(left, right []GuardContract) bool {
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
func (f fact) notValue(key string) Scalar {
	parts := strings.SplitN(key, ":", scalarKeyParts)
	if len(parts) != scalarKeyParts {
		return Scalar{}
	}

	kind, err := strconv.Atoi(parts[0])
	if err != nil {
		return Scalar{}
	}

	return Scalar{Kind: ScalarKind(kind), Text: parts[1]}
}

func (c *callFacts) forCall(call *ast.CallExpr) CallSummary {
	obj, key, ok := c.q.calledFunc(call)
	if !ok {
		return CallSummary{}
	}

	summary := c.inferred[key]

	if len(summary.Always) == 0 && len(summary.Results) == 0 {
		if external, ok := c.external(obj); ok {
			summary = external
		}
	}

	return c.withExplicit(key, summary)
}

func (c *callFacts) exported(fn summarizableFunc) CallSummary {
	summary := c.withExplicit(fn.key, c.inferred[fn.key])
	summary.ParallelParams = fn.parallel

	return summary
}

func (c *callFacts) withExplicit(key string, summary CallSummary) CallSummary {
	if explicit := c.explicit[key]; len(explicit) != 0 {
		summary.Always = NormalizeGuardContracts(append(summary.Always, explicit...))
	}

	return NormalizeCallSummary(summary)
}

// applyAlways applies the contracts call guarantees on every return; false
// means they contradict st.
func (c *callFacts) applyAlways(st state, call *ast.CallExpr) (state, bool) {
	nextStates := c.apply(st, call, c.forCall(call).Always)
	if len(nextStates) == 0 {
		return state{}, false
	}

	return nextStates[0], true
}

func (c *callFacts) apply(
	st state,
	call *ast.CallExpr,
	contracts []GuardContract,
) []state {
	return applyContractSequence(
		st,
		contracts,
		c.budget.normalize,
		func(currentState state, contract GuardContract) []state {
			sym, ok := c.q.symbolForContractTarget(call, contract.Target)
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

			if contract.WantEq {
				next, ok2 = setExact(currentState, sym, contract.Value, ev)
			} else {
				next, ok2 = addNot(currentState, sym, contract.Value, ev)
			}

			if !ok2 {
				return nil
			}

			return []state{next}
		},
	)
}

func (c *callFacts) refineCall(st state, call *ast.CallExpr, wantTrue bool) ([]state, bool) {
	if !c.q.isBoolExpr(call) {
		return nil, false
	}

	summary := c.forCall(call)

	result, ok := summary.Results[0]
	if !ok {
		return nil, false
	}

	contracts := append([]GuardContract{}, summary.Always...)
	if wantTrue {
		contracts = append(contracts, result.WhenTrue...)
	} else {
		contracts = append(contracts, result.WhenFalse...)
	}

	return c.apply(st, call, NormalizeGuardContracts(contracts)), true
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

	result, ok := summary.Results[0]
	if !ok {
		return nil, false
	}

	selected, ok := result.contractsForScalar(value, wantEq)
	if !ok {
		return nil, false
	}

	contracts := append([]GuardContract{}, summary.Always...)
	contracts = append(contracts, selected...)

	return c.apply(st, call, NormalizeGuardContracts(contracts)), true
}

func (result ResultSummary) contractsForScalar(
	value Scalar,
	wantEqual bool,
) ([]GuardContract, bool) {
	switch value.Kind {
	case ScalarBool:
		if value.Text != boolTrueText && value.Text != boolFalseText {
			return nil, false
		}

		wantTrue := value.Text == boolTrueText
		if !wantEqual {
			wantTrue = !wantTrue
		}

		if wantTrue {
			return result.WhenTrue, true
		}

		return result.WhenFalse, true
	case ScalarNil:
		if wantEqual {
			return result.WhenNil, true
		}

		return result.WhenNonNil, true
	case ScalarInvalid, ScalarString, ScalarInt:
		return nil, false
	}

	return nil, false
}
