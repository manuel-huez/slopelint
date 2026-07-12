package deadcode

import (
	"go/ast"
	"go/types"
)

func (graph deadCodeGraph) fmtStringerForwardedParamUses(
	pkg *Package,
	decl *ast.FuncDecl,
	funcsSeen map[string]struct{},
) []fmtStringerParamUse {
	if decl == nil || decl.Name == nil || decl.Body == nil {
		return nil
	}

	obj, _ := pkg.TypesInfo.Defs[decl.Name].(*types.Func)
	if obj == nil {
		return nil
	}

	sig, _ := obj.Type().(*types.Signature)
	if sig == nil || sig.Params() == nil {
		return nil
	}

	key := deadCodeObjectKey(obj)
	if key != "" {
		if cached, ok := graph.fmtStringerForwarded[key]; ok {
			return cached
		}

		if _, ok := funcsSeen[key]; ok {
			return nil
		}

		funcsSeen[key] = struct{}{}
		defer delete(funcsSeen, key)
	}

	state := fmtStringerParamStateForSignature(sig)
	seen := make(map[fmtStringerParamUse]struct{})
	out := make([]fmtStringerParamUse, 0)

	walkFmtStringerFlowBlock(
		state,
		decl.Body.List,
		graph.fmtStringerParamFlowOps(pkg.TypesInfo, funcsSeen, seen, &out),
	)

	if key != "" {
		graph.fmtStringerForwarded[key] = out
	}

	return out
}

func (graph deadCodeGraph) fmtStringerParamFlowOps(
	info *types.Info,
	funcsSeen map[string]struct{},
	seen map[fmtStringerParamUse]struct{},
	out *[]fmtStringerParamUse,
) fmtStringerFlowOps[fmtStringerParamState] {
	ops := graph.fmtStringerBaseParamFlowOps(info, funcsSeen)
	ops.expr = func(state fmtStringerParamState, node ast.Node) {
		graph.collectFmtStringerParamExprUses(info, state, node, funcsSeen, seen, out)
	}

	return ops
}

func (graph deadCodeGraph) collectFmtStringerParamExprUses(
	info *types.Info,
	state fmtStringerParamState,
	node ast.Node,
	funcsSeen map[string]struct{},
	seen map[fmtStringerParamUse]struct{},
	out *[]fmtStringerParamUse,
) {
	inspectReflectedCalls(node, func(call *ast.CallExpr) {
		appendUniqueFmtStringerParamUses(
			seen,
			out,
			graph.fmtStringerForwardedCallParamUses(
				info,
				state.aliases,
				state.slices,
				call,
				funcsSeen,
			),
		)
	})
}

func updateFmtStringerParamRangeValue(
	info *types.Info,
	state fmtStringerParamState,
	stmt *ast.RangeStmt,
) {
	rangeUses := fmtStringerSliceParamUsesForExpr(info, state.aliases, state.slices, stmt.X)
	if len(rangeUses) == 0 {
		return
	}

	setFmtStringerForwardedVarUses(
		info,
		state.aliases,
		state.slices,
		stmt.Value,
		rangeUses,
	)
}

func setFmtStringerForwardedVarUses(
	info *types.Info,
	aliases map[fmtStringerVarRef][]fmtStringerParamUse,
	sliceAliases map[fmtStringerVarRef][]fmtStringerParamUse,
	expr ast.Expr,
	uses []fmtStringerParamUse,
) {
	ref, ok := fmtStringerVarForExpr(info, expr)
	if !ok {
		return
	}

	aliases[ref] = uses
	delete(sliceAliases, ref)
}

func cloneFmtStringerParamState(state fmtStringerParamState) fmtStringerParamState {
	return fmtStringerParamState{
		aliases: cloneFmtStringerParamUseMap(state.aliases),
		slices:  cloneFmtStringerParamUseMap(state.slices),
	}
}

func cloneFmtStringerParamUseMap(
	source map[fmtStringerVarRef][]fmtStringerParamUse,
) map[fmtStringerVarRef][]fmtStringerParamUse {
	out := make(map[fmtStringerVarRef][]fmtStringerParamUse, len(source))
	for key, uses := range source {
		out[key] = append([]fmtStringerParamUse(nil), uses...)
	}

	return out
}

func mergeFmtStringerParamUseMaps(
	first map[fmtStringerVarRef][]fmtStringerParamUse,
	second map[fmtStringerVarRef][]fmtStringerParamUse,
) map[fmtStringerVarRef][]fmtStringerParamUse {
	out := cloneFmtStringerParamUseMap(first)
	for key, uses := range second {
		out[key] = dedupeComparable(append(out[key], uses...), fmtStringerDedupeMinLen)
	}

	return out
}

func appendUniqueFmtStringerParamUses(
	seen map[fmtStringerParamUse]struct{},
	out *[]fmtStringerParamUse,
	uses []fmtStringerParamUse,
) {
	for _, use := range uses {
		if _, ok := seen[use]; ok {
			continue
		}

		seen[use] = struct{}{}
		*out = append(*out, use)
	}
}

func fmtStringerParamStateForSignature(sig *types.Signature) fmtStringerParamState {
	out := fmtStringerParamState{
		aliases: make(map[fmtStringerVarRef][]fmtStringerParamUse),
		slices:  make(map[fmtStringerVarRef][]fmtStringerParamUse),
	}

	for index := range sig.Params().Len() {
		param := sig.Params().At(index)
		if param == nil {
			continue
		}

		use := fmtStringerParamUse{
			index:    index,
			variadic: sig.Variadic() && index == sig.Params().Len()-1,
			slice:    fmtStringerAnySliceType(param.Type()),
		}
		if use.slice {
			out.slices[fmtStringerVarRef{obj: param}] = []fmtStringerParamUse{use}
			continue
		}

		out.aliases[fmtStringerVarRef{obj: param}] = []fmtStringerParamUse{use}
	}

	return out
}

func (graph deadCodeGraph) updateFmtStringerAssignAliases(
	info *types.Info,
	state fmtStringerParamState,
	left []ast.Expr,
	right []ast.Expr,
	funcsSeen map[string]struct{},
) {
	snapshot := cloneFmtStringerParamState(state)

	for index, expr := range left {
		ref, ok := fmtStringerVarForExpr(info, expr)
		if !ok {
			continue
		}

		uses, sliceUses := graph.fmtStringerAssignedParamUses(
			info,
			snapshot,
			right,
			len(left),
			index,
			funcsSeen,
		)
		if len(uses) > 0 {
			state.aliases[ref] = uses
			delete(state.slices, ref)

			continue
		}

		if len(sliceUses) > 0 {
			delete(state.aliases, ref)
			state.slices[ref] = sliceUses

			continue
		}

		delete(state.aliases, ref)
		delete(state.slices, ref)
	}
}

func (graph deadCodeGraph) fmtStringerAssignedParamUses(
	info *types.Info,
	state fmtStringerParamState,
	right []ast.Expr,
	targetCount int,
	index int,
	funcsSeen map[string]struct{},
) ([]fmtStringerParamUse, []fmtStringerParamUse) {
	rhs := fmtStringerRHSExpr(right, targetCount, index)
	if rhs == nil {
		return nil, nil
	}

	if len(right) == 1 {
		resultIndex := 0
		if targetCount > 1 {
			resultIndex = index
		}

		if uses, sliceUses, ok := graph.fmtStringerCallResultParamUses(
			info,
			state,
			rhs,
			resultIndex,
			funcsSeen,
		); ok {
			return uses, sliceUses
		}
	}

	uses := fmtStringerParamUsesForExpr(info, state.aliases, rhs)
	if len(uses) > 0 {
		return uses, nil
	}

	return nil, fmtStringerSliceParamUsesForExpr(info, state.aliases, state.slices, rhs)
}

func (graph deadCodeGraph) fmtStringerCallResultParamUses(
	info *types.Info,
	state fmtStringerParamState,
	expr ast.Expr,
	resultIndex int,
	funcsSeen map[string]struct{},
) ([]fmtStringerParamUse, []fmtStringerParamUse, bool) {
	call, ok := unparenReflectedExpr(expr).(*ast.CallExpr)
	if !ok {
		return nil, nil, false
	}

	fn := calledFunc(info, call)

	calleePkg := graph.packageForFunc(fn)
	if calleePkg == nil {
		return nil, nil, false
	}

	decl := graph.funcDeclForObject(calleePkg, fn)
	if decl == nil {
		return nil, nil, false
	}

	results := graph.fmtStringerForwardedResultUses(calleePkg, decl, funcsSeen)

	return graph.fmtStringerCallerParamUseList(
			info,
			state,
			call,
			results.values[resultIndex],
		),
		graph.fmtStringerCallerParamUseList(
			info,
			state,
			call,
			results.slices[resultIndex],
		),
		true
}

func (graph deadCodeGraph) fmtStringerCallerParamUseList(
	info *types.Info,
	state fmtStringerParamState,
	call *ast.CallExpr,
	uses []fmtStringerParamUse,
) []fmtStringerParamUse {
	out := make([]fmtStringerParamUse, 0, len(uses))
	for _, use := range uses {
		out = append(
			out,
			fmtStringerCallerParamUses(info, state.aliases, state.slices, call, use)...,
		)
	}

	return dedupeComparable(out, fmtStringerDedupeMinLen)
}

func (graph deadCodeGraph) fmtStringerForwardedResultUses(
	pkg *Package,
	decl *ast.FuncDecl,
	funcsSeen map[string]struct{},
) fmtStringerResultState {
	empty := emptyFmtStringerResultState()
	if decl == nil || decl.Name == nil || decl.Body == nil {
		return empty
	}

	obj, _ := pkg.TypesInfo.Defs[decl.Name].(*types.Func)
	if obj == nil {
		return empty
	}

	sig, _ := obj.Type().(*types.Signature)
	if sig == nil || sig.Params() == nil || sig.Results() == nil {
		return empty
	}

	key := deadCodeObjectKey(obj)
	if key != "" {
		if cached, ok := graph.fmtStringerResults[key]; ok {
			return cached
		}

		if _, ok := funcsSeen[key]; ok {
			return empty
		}

		funcsSeen[key] = struct{}{}
		defer delete(funcsSeen, key)
	}

	state := fmtStringerParamStateForSignature(sig)
	namedResults := fmtStringerNamedResults(pkg.TypesInfo, decl, sig.Results().Len())
	out := emptyFmtStringerResultState()

	walkFmtStringerFlowBlock(
		state,
		decl.Body.List,
		graph.fmtStringerResultFlowOps(pkg.TypesInfo, funcsSeen, namedResults, &out),
	)

	if key != "" {
		graph.fmtStringerResults[key] = out
	}

	return out
}

func (graph deadCodeGraph) fmtStringerResultFlowOps(
	info *types.Info,
	funcsSeen map[string]struct{},
	namedResults []*types.Var,
	out *fmtStringerResultState,
) fmtStringerFlowOps[fmtStringerParamState] {
	ops := graph.fmtStringerBaseParamFlowOps(info, funcsSeen)
	ops.expr = func(fmtStringerParamState, ast.Node) {}
	ops.returns = func(state fmtStringerParamState, stmt *ast.ReturnStmt) {
		graph.collectFmtStringerResultUses(info, state, stmt, namedResults, out, funcsSeen)
	}

	return ops
}

func (graph deadCodeGraph) fmtStringerBaseParamFlowOps(
	info *types.Info,
	funcsSeen map[string]struct{},
) fmtStringerFlowOps[fmtStringerParamState] {
	return fmtStringerFlowOps[fmtStringerParamState]{
		assign: func(state fmtStringerParamState, left []ast.Expr, right []ast.Expr) {
			graph.updateFmtStringerAssignAliases(info, state, left, right, funcsSeen)
		},
		rangeValue: func(state fmtStringerParamState, stmt *ast.RangeStmt) {
			updateFmtStringerParamRangeValue(info, state, stmt)
		},
		empty: func() fmtStringerParamState {
			return fmtStringerParamState{
				aliases: make(map[fmtStringerVarRef][]fmtStringerParamUse),
				slices:  make(map[fmtStringerVarRef][]fmtStringerParamUse),
			}
		},
		clone: cloneFmtStringerParamState,
		merge: func(first fmtStringerParamState, second fmtStringerParamState) fmtStringerParamState {
			return fmtStringerParamState{
				aliases: mergeFmtStringerParamUseMaps(first.aliases, second.aliases),
				slices:  mergeFmtStringerParamUseMaps(first.slices, second.slices),
			}
		},
	}
}

func (graph deadCodeGraph) collectFmtStringerResultUses(
	info *types.Info,
	state fmtStringerParamState,
	stmt *ast.ReturnStmt,
	namedResults []*types.Var,
	out *fmtStringerResultState,
	funcsSeen map[string]struct{},
) {
	if len(stmt.Results) == 0 {
		for index, obj := range namedResults {
			if obj == nil {
				continue
			}

			ref := fmtStringerVarRef{obj: obj}
			out.addValueUses(index, state.aliases[ref])
			out.addSliceUses(index, state.slices[ref])
		}

		return
	}

	resultCount := len(stmt.Results)
	if len(stmt.Results) == 1 {
		if tuple, ok := info.TypeOf(stmt.Results[0]).(*types.Tuple); ok {
			resultCount = tuple.Len()
		}
	}

	for index := range resultCount {
		uses, sliceUses := graph.fmtStringerAssignedParamUses(
			info,
			state,
			stmt.Results,
			resultCount,
			index,
			funcsSeen,
		)
		out.addValueUses(index, uses)
		out.addSliceUses(index, sliceUses)
	}
}

func fmtStringerNamedResults(
	info *types.Info,
	decl *ast.FuncDecl,
	count int,
) []*types.Var {
	out := make([]*types.Var, count)
	if decl == nil || decl.Type == nil || decl.Type.Results == nil {
		return out
	}

	index := 0

	for _, field := range decl.Type.Results.List {
		if len(field.Names) == 0 {
			index++
			continue
		}

		for _, name := range field.Names {
			if index >= len(out) {
				return out
			}

			out[index], _ = info.Defs[name].(*types.Var)
			index++
		}
	}

	return out
}

func emptyFmtStringerResultState() fmtStringerResultState {
	return fmtStringerResultState{
		values: make(map[int][]fmtStringerParamUse),
		slices: make(map[int][]fmtStringerParamUse),
	}
}

func (state fmtStringerResultState) addValueUses(index int, uses []fmtStringerParamUse) {
	state.values[index] = dedupeComparable(
		append(state.values[index], uses...),
		fmtStringerDedupeMinLen,
	)
}

func (state fmtStringerResultState) addSliceUses(index int, uses []fmtStringerParamUse) {
	state.slices[index] = dedupeComparable(
		append(state.slices[index], uses...),
		fmtStringerDedupeMinLen,
	)
}

func identExprs(idents []*ast.Ident) []ast.Expr {
	out := make([]ast.Expr, 0, len(idents))
	for _, ident := range idents {
		out = append(out, ident)
	}

	return out
}

func (graph deadCodeGraph) fmtStringerForwardedCallParamUses(
	info *types.Info,
	aliases map[fmtStringerVarRef][]fmtStringerParamUse,
	sliceAliases map[fmtStringerVarRef][]fmtStringerParamUse,
	call *ast.CallExpr,
	funcsSeen map[string]struct{},
) []fmtStringerParamUse {
	fn := calledFunc(info, call)
	if fn == nil || fn.Pkg() == nil {
		return nil
	}

	if fn.Pkg().Path() != fmtPackagePath {
		return graph.fmtStringerDelegatedCallParamUses(
			info,
			aliases,
			sliceAliases,
			call,
			fn,
			funcsSeen,
		)
	}

	out := make([]fmtStringerParamUse, 0, len(call.Args))
	forEachFmtStringerArg(info, call, fn.Name(), func(arg ast.Expr, ellipsis bool) {
		if ellipsis {
			uses := fmtStringerSliceParamUsesForExpr(info, aliases, sliceAliases, arg)
			if len(uses) > 0 {
				out = append(out, uses...)
				return
			}
		}

		out = append(out, fmtStringerParamUsesForExpr(info, aliases, arg)...)
	})

	return dedupeComparable(out, fmtStringerDedupeMinLen)
}

func forEachFmtStringerArg(
	info *types.Info,
	call *ast.CallExpr,
	fnName string,
	visit func(ast.Expr, bool),
) {
	for argIndex := range fmtStringerArgIndexes(info, call, fnName) {
		if argIndex < 0 || argIndex >= len(call.Args) {
			continue
		}

		ellipsis := call.Ellipsis.IsValid() && argIndex == len(call.Args)-1
		visit(call.Args[argIndex], ellipsis)
	}
}

func (graph deadCodeGraph) fmtStringerDelegatedCallParamUses(
	info *types.Info,
	aliases map[fmtStringerVarRef][]fmtStringerParamUse,
	sliceAliases map[fmtStringerVarRef][]fmtStringerParamUse,
	call *ast.CallExpr,
	fn *types.Func,
	funcsSeen map[string]struct{},
) []fmtStringerParamUse {
	calleePkg := graph.packageForFunc(fn)
	if calleePkg == nil {
		return nil
	}

	decl := graph.funcDeclForObject(calleePkg, fn)
	if decl == nil {
		return nil
	}

	calleeUses := graph.fmtStringerForwardedParamUses(calleePkg, decl, funcsSeen)

	out := make([]fmtStringerParamUse, 0, len(calleeUses))
	for _, use := range calleeUses {
		out = append(
			out,
			fmtStringerCallerParamUses(info, aliases, sliceAliases, call, use)...,
		)
	}

	return dedupeComparable(out, fmtStringerDedupeMinLen)
}

func fmtStringerCallerParamUses(
	info *types.Info,
	aliases map[fmtStringerVarRef][]fmtStringerParamUse,
	sliceAliases map[fmtStringerVarRef][]fmtStringerParamUse,
	call *ast.CallExpr,
	use fmtStringerParamUse,
) []fmtStringerParamUse {
	if use.index >= len(call.Args) {
		return nil
	}

	return fmtStringerCallerForwardedArgUses(info, aliases, sliceAliases, call, use)
}

func fmtStringerCallerForwardedArgUses(
	info *types.Info,
	aliases map[fmtStringerVarRef][]fmtStringerParamUse,
	sliceAliases map[fmtStringerVarRef][]fmtStringerParamUse,
	call *ast.CallExpr,
	use fmtStringerParamUse,
) []fmtStringerParamUse {
	if !use.variadic {
		if !use.slice {
			return fmtStringerParamUsesForExpr(info, aliases, call.Args[use.index])
		}

		return fmtStringerCallerVariadicSliceUses(
			info,
			aliases,
			sliceAliases,
			call.Args[use.index],
		)
	}

	if call.Ellipsis.IsValid() && use.index == len(call.Args)-1 {
		return fmtStringerCallerVariadicSliceUses(
			info,
			aliases,
			sliceAliases,
			call.Args[use.index],
		)
	}

	return fmtStringerCallerVariadicArgUses(info, aliases, call, use.index)
}

func fmtStringerCallerVariadicSliceUses(
	info *types.Info,
	aliases map[fmtStringerVarRef][]fmtStringerParamUse,
	sliceAliases map[fmtStringerVarRef][]fmtStringerParamUse,
	expr ast.Expr,
) []fmtStringerParamUse {
	if uses := fmtStringerSliceParamUsesForExpr(info, aliases, sliceAliases, expr); len(uses) > 0 {
		return uses
	}

	return fmtStringerParamUsesForExpr(info, aliases, expr)
}

func fmtStringerCallerVariadicArgUses(
	info *types.Info,
	aliases map[fmtStringerVarRef][]fmtStringerParamUse,
	call *ast.CallExpr,
	startIndex int,
) []fmtStringerParamUse {
	out := make([]fmtStringerParamUse, 0, len(call.Args)-startIndex)
	for index := startIndex; index < len(call.Args); index++ {
		out = append(out, fmtStringerParamUsesForExpr(info, aliases, call.Args[index])...)
	}

	return dedupeComparable(out, fmtStringerDedupeMinLen)
}

func fmtStringerParamUsesForExpr(
	info *types.Info,
	aliases map[fmtStringerVarRef][]fmtStringerParamUse,
	expr ast.Expr,
) []fmtStringerParamUse {
	ref, ok := fmtStringerVarForExpr(info, expr)
	if !ok {
		return nil
	}

	return aliases[ref]
}

func fmtStringerSliceParamUsesForExpr(
	info *types.Info,
	aliases map[fmtStringerVarRef][]fmtStringerParamUse,
	sliceAliases map[fmtStringerVarRef][]fmtStringerParamUse,
	expr ast.Expr,
) []fmtStringerParamUse {
	if ref, ok := fmtStringerVarForExpr(info, expr); ok {
		return sliceAliases[ref]
	}

	switch expr := unparenReflectedExpr(expr).(type) {
	case *ast.CompositeLit:
		return fmtStringerCompositeParamUses(info, aliases, expr)
	case *ast.CallExpr:
		return fmtStringerAppendParamUses(info, aliases, sliceAliases, expr)
	default:
		return nil
	}
}

func fmtStringerCompositeParamUses(
	info *types.Info,
	aliases map[fmtStringerVarRef][]fmtStringerParamUse,
	lit *ast.CompositeLit,
) []fmtStringerParamUse {
	if !fmtStringerAnySliceType(info.TypeOf(lit)) {
		return nil
	}

	out := make([]fmtStringerParamUse, 0, len(lit.Elts))
	for _, elt := range lit.Elts {
		out = append(out, fmtStringerParamUsesForExpr(info, aliases, flowElementValue(elt))...)
	}

	return dedupeComparable(out, fmtStringerDedupeMinLen)
}

func fmtStringerAppendParamUses(
	info *types.Info,
	aliases map[fmtStringerVarRef][]fmtStringerParamUse,
	sliceAliases map[fmtStringerVarRef][]fmtStringerParamUse,
	call *ast.CallExpr,
) []fmtStringerParamUse {
	if !fmtStringerAppendCall(info, call) {
		return nil
	}

	return fmtStringerAppendArgValues(
		call,
		func(expr ast.Expr) ([]fmtStringerParamUse, bool) {
			return fmtStringerSliceParamUsesForExpr(info, aliases, sliceAliases, expr), true
		},
		func(expr ast.Expr) ([]fmtStringerParamUse, bool) {
			return fmtStringerParamUsesForExpr(info, aliases, expr), true
		},
		func(uses []fmtStringerParamUse) []fmtStringerParamUse {
			return dedupeComparable(uses, fmtStringerDedupeMinLen)
		},
	)
}

func fmtStringerAppendArgValues[T any](
	call *ast.CallExpr,
	sliceValues func(ast.Expr) ([]T, bool),
	valueValues func(ast.Expr) ([]T, bool),
	dedupe func([]T) []T,
) []T {
	out := make([]T, 0, len(call.Args))
	if len(call.Args) > 0 {
		if values, ok := sliceValues(call.Args[0]); ok {
			out = append(out, values...)
		}
	}

	for index := 1; index < len(call.Args); index++ {
		arg := call.Args[index]
		if call.Ellipsis.IsValid() && index == len(call.Args)-1 {
			if values, ok := sliceValues(arg); ok {
				out = append(out, values...)
			}

			continue
		}

		if values, ok := valueValues(arg); ok {
			out = append(out, values...)
		}
	}

	return dedupe(out)
}

func fmtStringerVarForExpr(info *types.Info, expr ast.Expr) (fmtStringerVarRef, bool) {
	switch expr := unparenReflectedExpr(expr).(type) {
	case *ast.Ident:
		obj, _ := info.Uses[expr].(*types.Var)
		if obj == nil {
			obj, _ = info.Defs[expr].(*types.Var)
		}

		if obj == nil {
			return fmtStringerVarRef{}, false
		}

		return fmtStringerVarRef{obj: obj}, true
	case *ast.SelectorExpr:
		obj, _ := info.Uses[expr.Sel].(*types.Var)
		if obj == nil {
			return fmtStringerVarRef{}, false
		}

		if !obj.IsField() {
			return fmtStringerVarRef{obj: obj}, true
		}

		root, path, ok := fmtStringerSelectorRootPath(info, expr)
		if !ok {
			return fmtStringerVarRef{}, false
		}

		return fmtStringerVarRef{obj: obj, root: root, path: path}, true
	default:
		return fmtStringerVarRef{}, false
	}
}

func fmtStringerSelectorRootPath(
	info *types.Info,
	expr ast.Expr,
) (types.Object, string, bool) {
	switch expr := unparenReflectedExpr(expr).(type) {
	case *ast.Ident:
		obj, _ := info.ObjectOf(expr).(*types.Var)
		if obj == nil {
			return nil, "", false
		}

		return obj, "", true
	case *ast.SelectorExpr:
		obj, _ := info.Uses[expr.Sel].(*types.Var)
		if obj == nil {
			return nil, "", false
		}

		if !obj.IsField() {
			return obj, "", true
		}

		root, path, ok := fmtStringerSelectorRootPath(info, expr.X)
		if !ok {
			return nil, "", false
		}

		return root, path + "." + expr.Sel.Name, true
	default:
		return nil, "", false
	}
}

func fmtStringerAppendCall(info *types.Info, call *ast.CallExpr) bool {
	ident, _ := unparenReflectedExpr(call.Fun).(*ast.Ident)
	if ident == nil || ident.Name != "append" {
		return false
	}

	_, ok := info.Uses[ident].(*types.Builtin)

	return ok
}

func fmtStringerAnySliceType(typ types.Type) bool {
	slice, ok := types.Unalias(typ).Underlying().(*types.Slice)
	if !ok {
		return false
	}

	iface, ok := types.Unalias(slice.Elem()).Underlying().(*types.Interface)

	return ok && iface.NumMethods() == 0
}

func interfaceRequiresStringMethod(iface *types.Interface) bool {
	if iface == nil {
		return false
	}

	for method := range iface.Methods() {
		if method != nil && method.Name() == "String" && stringMethodSignature(method) {
			return true
		}
	}

	return false
}

func addStringMethodForType(
	l *packageLinter,
	out map[string]struct{},
	typ types.Type,
) {
	obj, _, _ := types.LookupFieldOrMethod(typ, false, l.pkg.TypesPkg, "String")

	fn, ok := obj.(*types.Func)
	if !ok || fn == nil || !stringMethodSignature(fn) {
		return
	}

	out[deadCodeObjectKey(fn)] = struct{}{}
}

func stringMethodSignature(fn *types.Func) bool {
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig == nil || tupleLen(sig.Params()) != 0 || tupleLen(sig.Results()) != 1 {
		return false
	}

	result, ok := types.Unalias(sig.Results().At(0).Type()).Underlying().(*types.Basic)

	return ok && result.Kind() == types.String
}
