package deadcode

import (
	"go/ast"
	"go/types"
)

// fmtConcreteFlow tracks the concrete types that reach fmt calls, so String
// methods of those types stay live.
type fmtConcreteFlow struct {
	iface     ifaceUses
	decls     *funcDeclIndex
	params    fmtParamFlow
	summaries *fmtVarSummaries
}

// fmtVarSummaries holds the concrete types each package-level var may hold.
// built marks the summaries as computed; live limits which functions add to
// them, and is nil before the first liveness pass.
type fmtVarSummaries struct {
	values  map[string][]types.Type
	slices  map[string][]types.Type
	unknown map[string]struct{}
	built   bool
	live    map[string]struct{}
}

type fmtStringerConcreteState struct {
	values  map[fmtStringerVarRef][]types.Type
	slices  map[fmtStringerVarRef][]types.Type
	unknown map[fmtStringerVarRef]struct{}
}

func (flow fmtConcreteFlow) fmtStringerFlowUses(
	l *packageLinter,
	node ast.Node,
) map[string]struct{} {
	var stmts []ast.Stmt

	switch node := node.(type) {
	case *ast.FuncDecl:
		if node.Body == nil {
			return nil
		}

		stmts = node.Body.List
	case *ast.FuncLit:
		stmts = node.Body.List
	case *ast.ValueSpec:
		flow.ensureFmtStringerVarSummaries()

		out := make(map[string]struct{})

		state := emptyFmtStringerConcreteState()
		for _, value := range node.Values {
			flow.collectFmtStringerConcreteExprUses(l, out, state, value)
		}

		return out
	default:
		return nil
	}

	flow.ensureFmtStringerVarSummaries()

	out := make(map[string]struct{})
	flow.collectFmtStringerConcreteBlockUses(l, out, emptyFmtStringerConcreteState(), stmts)

	return out
}

func (flow fmtConcreteFlow) ensureFmtStringerVarSummaries() {
	if flow.summaries.built {
		return
	}

	flow.summaries.built = true
	for range flow.fmtStringerSummaryPasses() {
		changed := false

		for _, pkg := range flow.decls.packages {
			if flow.updateFmtStringerPackageVarSummaries(newPackageLinter(pkg)) {
				changed = true
			}
		}

		if !changed {
			return
		}
	}
}

func (flow fmtConcreteFlow) fmtStringerSummaryPasses() int {
	count := 1

	for _, pkg := range flow.decls.packages {
		for _, decl := range pkg.ProductionDecls {
			for _, spec := range valueSpecsForDecl(decl) {
				count += len(spec.Names)
			}
		}
	}

	return count
}

func (flow fmtConcreteFlow) updateFmtStringerPackageVarSummaries(l *packageLinter) bool {
	changed := false
	baseState := emptyFmtStringerConcreteState()
	vars := make(map[*types.Var]struct{})

	for _, decl := range l.pkg.ProductionDecls {
		for _, spec := range valueSpecsForDecl(decl) {
			for _, name := range spec.Names {
				obj, _ := l.pkg.TypesInfo.Defs[name].(*types.Var)
				if obj == nil {
					continue
				}

				vars[obj] = struct{}{}
			}

			flow.updateFmtStringerConcreteAssigns(
				l.pkg.TypesInfo,
				baseState,
				identExprs(spec.Names),
				spec.Values,
			)
		}
	}

	for _, fn := range l.pkg.ProductionFuncs {
		if fn == nil || fn.Name == nil || fn.Body == nil {
			continue
		}

		if fn.Name.Name == initFuncName {
			baseState = flow.collectFmtStringerConcreteBlockUses(l, nil, baseState, fn.Body.List)
		}
	}

	state := flow.fmtStringerPackageVarSummaryState(l, baseState)
	addFmtStringerPackageVarsForState(vars, state)

	for obj := range vars {
		if flow.summaries.updateFmtStringerPackageVarSummary(state, obj) {
			changed = true
		}
	}

	return changed
}

func (flow fmtConcreteFlow) fmtStringerPackageVarSummaryState(
	l *packageLinter,
	baseState fmtStringerConcreteState,
) fmtStringerConcreteState {
	if flow.summaries.live == nil {
		return baseState
	}

	state := cloneFmtStringerConcreteState(baseState)

	for _, fn := range l.pkg.ProductionFuncs {
		if !fmtStringerLiveSummaryFunc(l.pkg, fn, flow.summaries.live) {
			continue
		}

		next := flow.collectFmtStringerConcreteBlockUses(
			l,
			nil,
			cloneFmtStringerConcreteState(baseState),
			fn.Body.List,
		)
		state = mergeFmtStringerConcreteStates(state, next)
	}

	return state
}

func addFmtStringerPackageVarsForState(
	out map[*types.Var]struct{},
	state fmtStringerConcreteState,
) {
	for ref := range state.values {
		addFmtStringerPackageVar(out, ref)
	}

	for ref := range state.slices {
		addFmtStringerPackageVar(out, ref)
	}

	for ref := range state.unknown {
		addFmtStringerPackageVar(out, ref)
	}
}

func addFmtStringerPackageVar(out map[*types.Var]struct{}, ref fmtStringerVarRef) {
	obj := ref.packageLevelVar()
	if obj != nil {
		out[obj] = struct{}{}
	}
}

func (ref fmtStringerVarRef) packageLevelVar() *types.Var {
	if ref.obj == nil || ref.obj.IsField() || ref.root != nil || ref.path != "" {
		return nil
	}

	if !isPackageLevelDeadCodeObject(ref.obj) {
		return nil
	}

	return ref.obj
}

func fmtStringerLiveSummaryFunc(
	pkg *Package,
	fn *ast.FuncDecl,
	live map[string]struct{},
) bool {
	if pkg == nil || fn == nil || fn.Name == nil || fn.Body == nil {
		return false
	}

	if fn.Name.Name == initFuncName {
		return false
	}

	obj, _ := pkg.TypesInfo.Defs[fn.Name].(*types.Func)
	if obj == nil {
		return false
	}

	_, ok := live[deadCodeObjectKey(obj)]

	return ok
}

func (summaries *fmtVarSummaries) resetFmtStringerVarSummaries(live map[string]struct{}) {
	summaries.live = live
	clear(summaries.values)
	clear(summaries.slices)
	clear(summaries.unknown)
	summaries.built = false
}

func (summaries *fmtVarSummaries) updateFmtStringerPackageVarSummary(
	state fmtStringerConcreteState,
	obj *types.Var,
) bool {
	key := deadCodeObjectKey(obj)
	if key == "" {
		return false
	}

	ref := fmtStringerVarRef{obj: obj}
	if _, ok := state.unknown[ref]; ok {
		return summaries.setFmtStringerVarUnknown(key)
	}

	return summaries.mergeFmtStringerVarSummary(key, state.values[ref], state.slices[ref])
}

func (summaries *fmtVarSummaries) mergeFmtStringerVarSummary(
	key string,
	values []types.Type,
	slices []types.Type,
) bool {
	if _, ok := summaries.unknown[key]; ok {
		return false
	}

	oldValues := summaries.values[key]
	oldSlices := summaries.slices[key]
	nextValues := dedupeFmtStringerConcreteTypes(
		append(append([]types.Type(nil), oldValues...), values...),
	)
	nextSlices := dedupeFmtStringerConcreteTypes(
		append(append([]types.Type(nil), oldSlices...), slices...),
	)

	if fmtStringerTypeSlicesEqual(oldValues, nextValues) &&
		fmtStringerTypeSlicesEqual(oldSlices, nextSlices) {
		return false
	}

	if len(nextValues) == 0 {
		delete(summaries.values, key)
	} else {
		summaries.values[key] = nextValues
	}

	if len(nextSlices) == 0 {
		delete(summaries.slices, key)
	} else {
		summaries.slices[key] = nextSlices
	}

	return true
}

func (summaries *fmtVarSummaries) setFmtStringerVarUnknown(key string) bool {
	if _, ok := summaries.unknown[key]; ok {
		return false
	}

	summaries.unknown[key] = struct{}{}
	delete(summaries.values, key)
	delete(summaries.slices, key)

	return true
}

func emptyFmtStringerConcreteState() fmtStringerConcreteState {
	return fmtStringerConcreteState{
		values:  make(map[fmtStringerVarRef][]types.Type),
		slices:  make(map[fmtStringerVarRef][]types.Type),
		unknown: make(map[fmtStringerVarRef]struct{}),
	}
}

func (flow fmtConcreteFlow) collectFmtStringerConcreteBlockUses(
	l *packageLinter,
	out map[string]struct{},
	state fmtStringerConcreteState,
	stmts []ast.Stmt,
) fmtStringerConcreteState {
	return walkFmtStringerFlowBlock(state, stmts, flow.fmtStringerConcreteFlowOps(l, out))
}

func (flow fmtConcreteFlow) fmtStringerConcreteFlowOps(
	l *packageLinter,
	out map[string]struct{},
) fmtStringerFlowOps[fmtStringerConcreteState] {
	return fmtStringerFlowOps[fmtStringerConcreteState]{
		expr: func(state fmtStringerConcreteState, node ast.Node) {
			flow.collectFmtStringerConcreteExprUses(l, out, state, node)
		},
		assign: func(state fmtStringerConcreteState, left []ast.Expr, right []ast.Expr) {
			flow.updateFmtStringerConcreteAssigns(l.pkg.TypesInfo, state, left, right)
		},
		rangeValue: func(state fmtStringerConcreteState, stmt *ast.RangeStmt) {
			rangeTypes := flow.fmtStringerConcreteSliceTypesForExpr(
				l.pkg.TypesInfo,
				state,
				stmt.X,
			)
			if len(rangeTypes) > 0 {
				setFmtStringerConcreteVarTypes(l.pkg.TypesInfo, state, stmt.Value, rangeTypes)
			}
		},
		empty: emptyFmtStringerConcreteState,
		clone: cloneFmtStringerConcreteState,
		merge: mergeFmtStringerConcreteStates,
	}
}

func setFmtStringerConcreteVarTypes(
	info *types.Info,
	state fmtStringerConcreteState,
	expr ast.Expr,
	types []types.Type,
) {
	ref, ok := fmtStringerVarForExpr(info, expr)
	if !ok {
		return
	}

	state.values[ref] = types
	delete(state.slices, ref)
}

func valueSpecsForDecl(decl ast.Decl) []*ast.ValueSpec {
	gen, _ := decl.(*ast.GenDecl)
	if gen == nil {
		return nil
	}

	out := make([]*ast.ValueSpec, 0, len(gen.Specs))
	for _, spec := range gen.Specs {
		valueSpec, _ := spec.(*ast.ValueSpec)
		if valueSpec != nil {
			out = append(out, valueSpec)
		}
	}

	return out
}

func fmtStringerRHSExpr(values []ast.Expr, targetCount int, index int) ast.Expr {
	if index >= len(values) {
		return nil
	}

	if len(values) == 1 && targetCount > 1 {
		return values[0]
	}

	return values[index]
}

func (flow fmtConcreteFlow) updateFmtStringerConcreteAssigns(
	info *types.Info,
	state fmtStringerConcreteState,
	left []ast.Expr,
	right []ast.Expr,
) {
	snapshot := cloneFmtStringerConcreteState(state)

	for index, expr := range left {
		ref, ok := fmtStringerVarForExpr(info, expr)
		if !ok {
			continue
		}

		rhs := fmtStringerRHSExpr(right, len(left), index)
		if rhs == nil {
			delete(state.values, ref)
			delete(state.slices, ref)
			delete(state.unknown, ref)

			continue
		}

		if typ, ok := fmtStringerAssignedTupleType(info, right, len(left), index); ok {
			setFmtStringerConcreteAssignedType(state, ref, typ)

			continue
		}

		if flow.fmtStringerConcreteExprUnknown(info, snapshot, rhs) {
			delete(state.values, ref)
			delete(state.slices, ref)
			state.unknown[ref] = struct{}{}

			continue
		}

		sliceTypes := flow.fmtStringerConcreteSliceTypesForExpr(info, snapshot, rhs)
		if len(sliceTypes) > 0 {
			delete(state.values, ref)
			delete(state.unknown, ref)
			state.slices[ref] = sliceTypes

			continue
		}

		valueTypes := flow.fmtStringerConcreteTypesForExpr(info, snapshot, rhs)
		if len(valueTypes) > 0 {
			state.values[ref] = valueTypes
			delete(state.slices, ref)
			delete(state.unknown, ref)

			continue
		}

		delete(state.values, ref)
		delete(state.slices, ref)
		delete(state.unknown, ref)
	}
}

func fmtStringerAssignedTupleType(
	info *types.Info,
	values []ast.Expr,
	targetCount int,
	index int,
) (types.Type, bool) {
	if len(values) != 1 || targetCount <= 1 {
		return nil, false
	}

	tuple, ok := info.TypeOf(values[0]).(*types.Tuple)
	if !ok {
		return nil, false
	}

	if index >= tuple.Len() {
		return nil, true
	}

	return tuple.At(index).Type(), true
}

func setFmtStringerConcreteAssignedType(
	state fmtStringerConcreteState,
	ref fmtStringerVarRef,
	typ types.Type,
) {
	delete(state.values, ref)
	delete(state.slices, ref)
	delete(state.unknown, ref)

	if typ == nil {
		return
	}

	if typeIsInterface(typ) || fmtStringerAnySliceType(typ) {
		state.unknown[ref] = struct{}{}

		return
	}

	state.values[ref] = []types.Type{typ}
}

func (flow fmtConcreteFlow) collectFmtStringerConcreteExprUses(
	l *packageLinter,
	out map[string]struct{},
	state fmtStringerConcreteState,
	node ast.Node,
) {
	if out == nil {
		return
	}

	inspectReflectedCalls(node, func(call *ast.CallExpr) {
		flow.addFmtStringerConcreteCallUses(l, out, state, call)
	})
}

func (flow fmtConcreteFlow) addFmtStringerConcreteCallUses(
	l *packageLinter,
	out map[string]struct{},
	state fmtStringerConcreteState,
	call *ast.CallExpr,
) {
	fn := calledFunc(l.pkg.TypesInfo, call)
	if fn == nil || fn.Pkg() == nil {
		return
	}

	if fn.Pkg().Path() != fmtPackagePath {
		flow.addFmtStringerConcreteForwardedCallUses(l, out, state, call, fn)

		return
	}

	forEachFmtStringerArg(l.pkg.TypesInfo, call, fn.Name(), func(arg ast.Expr, ellipsis bool) {
		if ellipsis {
			for _, typ := range flow.fmtStringerConcreteSliceTypesForExpr(l.pkg.TypesInfo, state, arg) {
				flow.iface.addStringMethodsForType(l, out, typ)
			}

			return
		}

		for _, typ := range flow.fmtStringerConcreteTypesForExpr(l.pkg.TypesInfo, state, arg) {
			flow.iface.addStringMethodsForType(l, out, typ)
		}
	})
}

func (flow fmtConcreteFlow) addFmtStringerConcreteForwardedCallUses(
	l *packageLinter,
	out map[string]struct{},
	state fmtStringerConcreteState,
	call *ast.CallExpr,
	fn *types.Func,
) {
	calleePkg := flow.decls.packageForFunc(fn)
	if calleePkg == nil {
		return
	}

	decl := flow.decls.funcDeclForObject(calleePkg, fn)
	if decl == nil {
		return
	}

	uses := flow.params.fmtStringerForwardedParamUses(calleePkg, decl, make(map[string]struct{}))
	for _, use := range uses {
		if use.index >= len(call.Args) {
			continue
		}

		for _, typ := range flow.fmtStringerConcreteForwardedArgTypes(
			l.pkg.TypesInfo,
			state,
			call,
			use,
		) {
			flow.iface.addStringMethodsForType(l, out, typ)
		}
	}
}

func (flow fmtConcreteFlow) fmtStringerConcreteForwardedArgTypes(
	info *types.Info,
	state fmtStringerConcreteState,
	call *ast.CallExpr,
	use fmtStringerParamUse,
) []types.Type {
	if !use.variadic {
		if !use.slice {
			return flow.fmtStringerConcreteTypesForExpr(info, state, call.Args[use.index])
		}

		return flow.fmtStringerConcreteSliceTypesForExpr(info, state, call.Args[use.index])
	}

	if call.Ellipsis.IsValid() && use.index == len(call.Args)-1 {
		return flow.fmtStringerConcreteVariadicSliceTypes(info, state, call.Args[use.index])
	}

	return flow.fmtStringerConcreteVariadicArgTypes(info, state, call, use.index)
}

func (flow fmtConcreteFlow) fmtStringerConcreteVariadicSliceTypes(
	info *types.Info,
	state fmtStringerConcreteState,
	expr ast.Expr,
) []types.Type {
	types := flow.fmtStringerConcreteSliceTypesForExpr(info, state, expr)
	if len(types) > 0 {
		return types
	}

	return flow.fmtStringerConcreteTypesForExpr(info, state, expr)
}

func (flow fmtConcreteFlow) fmtStringerConcreteVariadicArgTypes(
	info *types.Info,
	state fmtStringerConcreteState,
	call *ast.CallExpr,
	startIndex int,
) []types.Type {
	out := make([]types.Type, 0, len(call.Args)-startIndex)
	for index := startIndex; index < len(call.Args); index++ {
		out = append(out, flow.fmtStringerConcreteTypesForExpr(info, state, call.Args[index])...)
	}

	return dedupeFmtStringerConcreteTypes(out)
}

func (flow fmtConcreteFlow) fmtStringerConcreteTypesForExpr(
	info *types.Info,
	state fmtStringerConcreteState,
	expr ast.Expr,
) []types.Type {
	if ref, ok := fmtStringerVarForExpr(info, expr); ok {
		if types := flow.fmtStringerConcreteVarTypes(info, state, expr, ref); len(types) > 0 {
			return types
		}
	}

	typ := reflectedValueType(info, expr)
	if tuple, ok := typ.(*types.Tuple); ok {
		out := make([]types.Type, 0, tuple.Len())
		for variable := range tuple.Variables() {
			item := variable.Type()
			if !typeIsInterface(item) {
				out = append(out, item)
			}
		}

		return out
	}

	if typ == nil || typeIsInterface(typ) {
		return nil
	}

	return []types.Type{typ}
}

func (flow fmtConcreteFlow) fmtStringerConcreteVarTypes(
	info *types.Info,
	state fmtStringerConcreteState,
	expr ast.Expr,
	ref fmtStringerVarRef,
) []types.Type {
	if flow.fmtStringerConcreteVarUnknown(ref, state) {
		return flow.fmtStringerUnknownTypesForType(reflectedValueType(info, expr))
	}

	if types := state.values[ref]; len(types) > 0 {
		return types
	}

	obj := ref.packageLevelVar()
	if obj == nil {
		return nil
	}

	key := deadCodeObjectKey(obj)
	if key == "" {
		return nil
	}

	return flow.summaries.values[key]
}

func (flow fmtConcreteFlow) fmtStringerConcreteExprUnknown(
	info *types.Info,
	state fmtStringerConcreteState,
	expr ast.Expr,
) bool {
	if ref, ok := fmtStringerVarForExpr(info, expr); ok {
		return flow.fmtStringerConcreteVarUnknown(ref, state)
	}

	typ := reflectedValueType(info, expr)

	return typ != nil && typeIsInterface(typ)
}

func (flow fmtConcreteFlow) fmtStringerConcreteVarUnknown(
	ref fmtStringerVarRef,
	state fmtStringerConcreteState,
) bool {
	if _, ok := state.unknown[ref]; ok {
		return true
	}

	obj := ref.packageLevelVar()
	if obj == nil {
		return false
	}

	key := deadCodeObjectKey(obj)
	if key == "" {
		return false
	}

	_, ok := flow.summaries.unknown[key]

	return ok
}

func (flow fmtConcreteFlow) fmtStringerUnknownTypesForType(typ types.Type) []types.Type {
	if typ == nil || !typeIsInterface(typ) {
		return nil
	}

	out := make([]types.Type, 0)

	for _, receiver := range flow.iface.candidateReceiverTypes() {
		if types.AssignableTo(receiver, typ) {
			out = append(out, receiver)
		}
	}

	return out
}

func (flow fmtConcreteFlow) fmtStringerUnknownSliceTypesForType(typ types.Type) []types.Type {
	slice, ok := types.Unalias(typ).Underlying().(*types.Slice)
	if !ok {
		return nil
	}

	return flow.fmtStringerUnknownTypesForType(slice.Elem())
}

func (flow fmtConcreteFlow) fmtStringerConcreteSliceTypesForExpr(
	info *types.Info,
	state fmtStringerConcreteState,
	expr ast.Expr,
) []types.Type {
	ref, ok := fmtStringerVarForExpr(info, expr)
	if !ok {
		return flow.fmtStringerConcreteSliceTypesForNonVarExpr(info, state, expr)
	}

	if flow.fmtStringerConcreteVarUnknown(ref, state) {
		return flow.fmtStringerUnknownSliceTypesForType(reflectedValueType(info, expr))
	}

	if types := state.slices[ref]; len(types) > 0 {
		return types
	}

	if obj := ref.packageLevelVar(); obj != nil {
		key := deadCodeObjectKey(obj)
		if key != "" {
			return flow.summaries.slices[key]
		}
	}

	return nil
}

func (flow fmtConcreteFlow) fmtStringerConcreteSliceTypesForNonVarExpr(
	info *types.Info,
	state fmtStringerConcreteState,
	expr ast.Expr,
) []types.Type {
	switch expr := unparenReflectedExpr(expr).(type) {
	case *ast.CompositeLit:
		return flow.fmtStringerConcreteCompositeTypes(info, state, expr)
	case *ast.CallExpr:
		return flow.fmtStringerConcreteAppendTypes(info, state, expr)
	default:
		return nil
	}
}

func (flow fmtConcreteFlow) fmtStringerConcreteCompositeTypes(
	info *types.Info,
	state fmtStringerConcreteState,
	lit *ast.CompositeLit,
) []types.Type {
	if !fmtStringerAnySliceType(info.TypeOf(lit)) {
		return nil
	}

	out := make([]types.Type, 0, len(lit.Elts))
	for _, elt := range lit.Elts {
		out = append(
			out,
			flow.fmtStringerConcreteTypesForExpr(info, state, flowElementValue(elt))...,
		)
	}

	return dedupeFmtStringerConcreteTypes(out)
}

func (flow fmtConcreteFlow) fmtStringerConcreteAppendTypes(
	info *types.Info,
	state fmtStringerConcreteState,
	call *ast.CallExpr,
) []types.Type {
	if !fmtStringerAppendCall(info, call) {
		return nil
	}

	return fmtStringerAppendArgValues(
		call,
		func(expr ast.Expr) ([]types.Type, bool) {
			return flow.fmtStringerConcreteSliceTypesForExpr(info, state, expr), true
		},
		func(expr ast.Expr) ([]types.Type, bool) {
			return flow.fmtStringerConcreteTypesForExpr(info, state, expr), true
		},
		dedupeFmtStringerConcreteTypes,
	)
}

func cloneFmtStringerConcreteState(state fmtStringerConcreteState) fmtStringerConcreteState {
	return fmtStringerConcreteState{
		values:  cloneFmtStringerConcreteTypeMap(state.values),
		slices:  cloneFmtStringerConcreteTypeMap(state.slices),
		unknown: cloneFmtStringerConcreteUnknowns(state.unknown),
	}
}

func cloneFmtStringerConcreteTypeMap(
	source map[fmtStringerVarRef][]types.Type,
) map[fmtStringerVarRef][]types.Type {
	out := make(map[fmtStringerVarRef][]types.Type, len(source))
	for key, values := range source {
		out[key] = append([]types.Type(nil), values...)
	}

	return out
}

func mergeFmtStringerConcreteStates(
	first fmtStringerConcreteState,
	second fmtStringerConcreteState,
) fmtStringerConcreteState {
	return fmtStringerConcreteState{
		values:  mergeFmtStringerConcreteTypeMaps(first.values, second.values),
		slices:  mergeFmtStringerConcreteTypeMaps(first.slices, second.slices),
		unknown: mergeFmtStringerConcreteUnknowns(first.unknown, second.unknown),
	}
}

func cloneFmtStringerConcreteUnknowns(
	source map[fmtStringerVarRef]struct{},
) map[fmtStringerVarRef]struct{} {
	out := make(map[fmtStringerVarRef]struct{}, len(source))
	for key := range source {
		out[key] = struct{}{}
	}

	return out
}

func mergeFmtStringerConcreteUnknowns(
	first map[fmtStringerVarRef]struct{},
	second map[fmtStringerVarRef]struct{},
) map[fmtStringerVarRef]struct{} {
	out := cloneFmtStringerConcreteUnknowns(first)
	for key := range second {
		out[key] = struct{}{}
	}

	return out
}

func mergeFmtStringerConcreteTypeMaps(
	first map[fmtStringerVarRef][]types.Type,
	second map[fmtStringerVarRef][]types.Type,
) map[fmtStringerVarRef][]types.Type {
	out := cloneFmtStringerConcreteTypeMap(first)
	for key, values := range second {
		out[key] = dedupeFmtStringerConcreteTypes(append(out[key], values...))
	}

	return out
}

func dedupeFmtStringerConcreteTypes(values []types.Type) []types.Type {
	if len(values) < fmtStringerDedupeMinLen {
		return values
	}

	seen := make(map[string]struct{}, len(values))

	out := make([]types.Type, 0, len(values))
	for _, value := range values {
		if value == nil {
			continue
		}

		key := deadCodeTypeString(value)
		if _, ok := seen[key]; ok {
			continue
		}

		seen[key] = struct{}{}

		out = append(out, value)
	}

	return out
}

func fmtStringerTypeSlicesEqual(first []types.Type, second []types.Type) bool {
	if len(first) != len(second) {
		return false
	}

	for index := range first {
		if !types.Identical(first[index], second[index]) {
			return false
		}
	}

	return true
}
