package deadcode

import (
	"go/ast"
	"go/constant"
	"go/types"
	"strings"
)

func (graph deadCodeGraph) callInterfaceMethodUses(
	l *packageLinter,
	call *ast.CallExpr,
) map[string]struct{} {
	if call == nil {
		return nil
	}

	out := make(map[string]struct{})
	if target := conversionTargetType(l.pkg.TypesInfo, call); target != nil {
		graph.addInterfaceMethodsForValue(l, out, call.Args[0], target)
	}

	if sig := callSignature(l.pkg.TypesInfo, call); sig != nil {
		for key := range graph.callArgInterfaceMethodUses(l, call, sig) {
			out[key] = struct{}{}
		}

		for key := range graph.knownOptionalInterfaceMethodUses(l, call, sig) {
			out[key] = struct{}{}
		}
	}

	return out
}

func (graph deadCodeGraph) callArgInterfaceMethodUses(
	l *packageLinter,
	call *ast.CallExpr,
	sig *types.Signature,
) map[string]struct{} {
	out := make(map[string]struct{})
	params := sig.Params()

	if params == nil || params.Len() == 0 {
		return out
	}

	for index, arg := range call.Args {
		target, ok := callParamTargetType(sig, index)
		if !ok {
			continue
		}

		graph.addInterfaceMethodsForValue(l, out, arg, target)
	}

	return out
}

func (graph deadCodeGraph) knownOptionalInterfaceMethodUses(
	l *packageLinter,
	call *ast.CallExpr,
	sig *types.Signature,
) map[string]struct{} {
	fn := calledFunc(l.pkg.TypesInfo, call)
	if fn == nil ||
		fn.Pkg() == nil ||
		fn.Pkg().Path() != "github.com/ulikunitz/xz/lzma" ||
		fn.Name() != "NewReader" {
		return nil
	}

	out := make(map[string]struct{})

	for index, arg := range call.Args {
		target, ok := callParamTargetType(sig, index)
		if !ok || !namedTypeMatches(target, "io", "Reader") {
			continue
		}

		byteReader := siblingNamedInterface(target, "io", "Reader", "ByteReader")
		if byteReader == nil {
			continue
		}

		graph.addInterfaceMethodsForValue(l, out, arg, byteReader)
	}

	return out
}

func siblingNamedInterface(
	typ types.Type,
	pkgPath string,
	sourceName string,
	targetName string,
) types.Type {
	named, ok := namedType(typ)
	if !ok || !namedTypeMatches(named, pkgPath, sourceName) {
		return nil
	}

	obj, _ := named.Obj().Pkg().Scope().Lookup(targetName).(*types.TypeName)
	if obj == nil {
		return nil
	}

	target := obj.Type()
	if _, ok := types.Unalias(target).Underlying().(*types.Interface); !ok {
		return nil
	}

	return target
}

func namedTypeMatches(typ types.Type, pkgPath string, name string) bool {
	named, ok := namedType(typ)
	if !ok || named.Obj() == nil || named.Obj().Pkg() == nil {
		return false
	}

	return named.Obj().Pkg().Path() == pkgPath && named.Obj().Name() == name
}

func namedType(typ types.Type) (*types.Named, bool) {
	if typ == nil {
		return nil, false
	}

	named, ok := types.Unalias(typ).(*types.Named)

	return named, ok
}

func callParamTargetType(sig *types.Signature, argIndex int) (types.Type, bool) {
	params := sig.Params()
	paramIndex := argIndex

	if sig.Variadic() && argIndex >= params.Len()-1 {
		paramIndex = params.Len() - 1
	}

	if paramIndex >= params.Len() {
		return nil, false
	}

	target := params.At(paramIndex).Type()
	if sig.Variadic() && argIndex >= params.Len()-1 {
		if slice, ok := types.Unalias(target).Underlying().(*types.Slice); ok {
			target = slice.Elem()
		}
	}

	return target, true
}

func conversionTargetType(info *types.Info, call *ast.CallExpr) types.Type {
	if call == nil || len(call.Args) != 1 {
		return nil
	}

	if _, ok := call.Fun.(*ast.FuncType); ok {
		return info.TypeOf(call.Fun)
	}

	target := info.TypeOf(call.Fun)
	if target == nil {
		return nil
	}

	if _, ok := types.Unalias(target).Underlying().(*types.Interface); !ok {
		return nil
	}

	return target
}

func callSignature(info *types.Info, call *ast.CallExpr) *types.Signature {
	if call == nil {
		return nil
	}

	typ := info.TypeOf(call.Fun)
	if typ == nil {
		return nil
	}

	if sig, ok := types.Unalias(typ).Underlying().(*types.Signature); ok {
		return sig
	}

	return nil
}

func calledFunc(info *types.Info, call *ast.CallExpr) *types.Func {
	if call == nil {
		return nil
	}

	return calledFuncExpr(info, call.Fun)
}

func calledFuncExpr(info *types.Info, expr ast.Expr) *types.Func {
	switch fun := expr.(type) {
	case *ast.Ident:
		obj, _ := info.Uses[fun].(*types.Func)

		return obj
	case *ast.SelectorExpr:
		if selection := info.Selections[fun]; selection != nil {
			obj, _ := selection.Obj().(*types.Func)

			return obj
		}

		obj, _ := info.Uses[fun.Sel].(*types.Func)

		return obj
	case *ast.IndexExpr:
		return calledFuncExpr(info, fun.X)
	case *ast.IndexListExpr:
		return calledFuncExpr(info, fun.X)
	default:
		return nil
	}
}

func fmtStringerArgIndexes(
	info *types.Info,
	call *ast.CallExpr,
	name string,
) map[int]struct{} {
	if firstArg, ok := fmtUnformattedFirstArg(name); ok {
		return argIndexRange(firstArg, len(call.Args))
	}

	formatIndex, ok := fmtFormatArgIndex(name)
	if !ok || formatIndex >= len(call.Args) {
		return nil
	}

	firstValueArg := formatIndex + 1

	format, ok := constantStringValue(info, call.Args[formatIndex])
	if !ok {
		return argIndexRange(firstValueArg, len(call.Args))
	}

	return fmtStringerFormattedArgIndexes(format, firstValueArg, len(call.Args))
}

func fmtUnformattedFirstArg(name string) (int, bool) {
	switch name {
	case "Append", "Appendln", "Fprint", "Fprintln":
		return 1, true
	case "Print", "Println", "Sprint", "Sprintln":
		return 0, true
	default:
		return 0, false
	}
}

func fmtFormatArgIndex(name string) (int, bool) {
	switch name {
	case "Appendf", "Fprintf":
		return 1, true
	case "Errorf", "Printf", "Sprintf":
		return 0, true
	default:
		return 0, false
	}
}

func constantStringValue(info *types.Info, expr ast.Expr) (string, bool) {
	value := info.Types[expr].Value
	if value == nil || value.Kind() != constant.String {
		return "", false
	}

	return constant.StringVal(value), true
}

func fmtStringerFormattedArgIndexes(
	format string,
	firstArg int,
	argCount int,
) map[int]struct{} {
	if formatHasExplicitIndexes(format) {
		return argIndexRange(firstArg, argCount)
	}

	out := make(map[int]struct{})
	argIndex := firstArg

	for index := 0; index < len(format); {
		verb, sharp, valueArgIndex, nextArgIndex, ok := nextFmtDirective(
			format,
			&index,
			argIndex,
		)
		if !ok {
			index++
			continue
		}

		if fmtVerbUsesStringer(verb, sharp) {
			out[valueArgIndex] = struct{}{}
		}

		argIndex = nextArgIndex
		index++
	}

	return out
}

func nextFmtDirective(
	format string,
	index *int,
	argIndex int,
) (byte, bool, int, int, bool) {
	if format[*index] != '%' {
		return 0, false, argIndex, argIndex, false
	}

	*index++
	if *index >= len(format) || format[*index] == '%' {
		return 0, false, argIndex, argIndex, false
	}

	sharp := false

	for *index < len(format) && strings.ContainsRune("#+-0 ", rune(format[*index])) {
		if format[*index] == '#' {
			sharp = true
		}

		*index++
	}

	*index, argIndex = skipFmtWidthOrPrecision(format, *index, argIndex)
	if *index < len(format) && format[*index] == '.' {
		*index, argIndex = skipFmtWidthOrPrecision(format, *index+1, argIndex)
	}

	if *index >= len(format) {
		return 0, false, argIndex, argIndex, false
	}

	return format[*index], sharp, argIndex, argIndex + 1, true
}

func formatHasExplicitIndexes(format string) bool {
	for i := range len(format) - 1 {
		if format[i] == '%' && format[i+1] == '[' {
			return true
		}
	}

	return false
}

func skipFmtWidthOrPrecision(format string, index int, argIndex int) (int, int) {
	if index >= len(format) {
		return index, argIndex
	}

	if format[index] == '*' {
		return index + 1, argIndex + 1
	}

	for index < len(format) && format[index] >= '0' && format[index] <= '9' {
		index++
	}

	return index, argIndex
}

func fmtVerbUsesStringer(verb byte, sharp bool) bool {
	switch verb {
	case 's', 'q', 'x', 'X':
		return true
	case 'v':
		return !sharp
	default:
		return false
	}
}

func argIndexRange(firstArg int, argCount int) map[int]struct{} {
	out := make(map[int]struct{})
	for index := firstArg; index < argCount; index++ {
		out[index] = struct{}{}
	}

	return out
}

func (graph deadCodeGraph) addStringMethodsForType(
	l *packageLinter,
	out map[string]struct{},
	typ types.Type,
) {
	if typ == nil {
		return
	}

	if iface, ok := types.Unalias(typ).Underlying().(*types.Interface); ok {
		if !interfaceRequiresStringMethod(iface) {
			return
		}

		for _, receiver := range graph.candidateReceiverTypes() {
			if types.AssignableTo(receiver, typ) {
				addStringMethodForType(l, out, receiver)
			}
		}

		return
	}

	addStringMethodForType(l, out, typ)
}

type fmtStringerParamUse struct {
	index    int
	variadic bool
	slice    bool
}

type fmtStringerVarRef struct {
	obj  *types.Var
	root types.Object
	path string
}

type fmtStringerParamState struct {
	aliases map[fmtStringerVarRef][]fmtStringerParamUse
	slices  map[fmtStringerVarRef][]fmtStringerParamUse
}

type fmtStringerResultState struct {
	values map[int][]fmtStringerParamUse
	slices map[int][]fmtStringerParamUse
}

const (
	fmtPackagePath          = "fmt"
	fmtStringerDedupeMinLen = 2
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
	return fmtStringerFlowOps[fmtStringerParamState]{
		expr: func(state fmtStringerParamState, node ast.Node) {
			graph.collectFmtStringerParamExprUses(info, state, node, funcsSeen, seen, out)
		},
		assign: func(state fmtStringerParamState, left []ast.Expr, right []ast.Expr) {
			graph.updateFmtStringerAssignAliases(info, state, left, right, funcsSeen)
		},
		rangeValue: func(state fmtStringerParamState, stmt *ast.RangeStmt) {
			rangeUses := fmtStringerSliceParamUsesForExpr(info, state.aliases, state.slices, stmt.X)
			if len(rangeUses) > 0 {
				setFmtStringerForwardedVarUses(
					info,
					state.aliases,
					state.slices,
					stmt.Value,
					rangeUses,
				)
			}
		},
		empty: emptyFmtStringerParamState,
		clone: cloneFmtStringerParamState,
		merge: mergeFmtStringerParamStates,
	}
}

func (graph deadCodeGraph) collectFmtStringerParamExprUses(
	info *types.Info,
	state fmtStringerParamState,
	node ast.Node,
	funcsSeen map[string]struct{},
	seen map[fmtStringerParamUse]struct{},
	out *[]fmtStringerParamUse,
) {
	if node == nil {
		return
	}

	ast.Inspect(node, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}

		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

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

		return true
	})
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

func emptyFmtStringerParamState() fmtStringerParamState {
	return fmtStringerParamState{
		aliases: make(map[fmtStringerVarRef][]fmtStringerParamUse),
		slices:  make(map[fmtStringerVarRef][]fmtStringerParamUse),
	}
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

func mergeFmtStringerParamStates(
	first fmtStringerParamState,
	second fmtStringerParamState,
) fmtStringerParamState {
	return fmtStringerParamState{
		aliases: mergeFmtStringerParamUseMaps(first.aliases, second.aliases),
		slices:  mergeFmtStringerParamUseMaps(first.slices, second.slices),
	}
}

func mergeFmtStringerParamUseMaps(
	first map[fmtStringerVarRef][]fmtStringerParamUse,
	second map[fmtStringerVarRef][]fmtStringerParamUse,
) map[fmtStringerVarRef][]fmtStringerParamUse {
	out := cloneFmtStringerParamUseMap(first)
	for key, uses := range second {
		out[key] = dedupeFmtStringerParamUses(append(out[key], uses...))
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

	return dedupeFmtStringerParamUses(out)
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
	return fmtStringerFlowOps[fmtStringerParamState]{
		expr: func(fmtStringerParamState, ast.Node) {},
		assign: func(state fmtStringerParamState, left []ast.Expr, right []ast.Expr) {
			graph.updateFmtStringerAssignAliases(info, state, left, right, funcsSeen)
		},
		rangeValue: func(state fmtStringerParamState, stmt *ast.RangeStmt) {
			rangeUses := fmtStringerSliceParamUsesForExpr(info, state.aliases, state.slices, stmt.X)
			if len(rangeUses) > 0 {
				setFmtStringerForwardedVarUses(
					info,
					state.aliases,
					state.slices,
					stmt.Value,
					rangeUses,
				)
			}
		},
		returns: func(state fmtStringerParamState, stmt *ast.ReturnStmt) {
			graph.collectFmtStringerResultUses(info, state, stmt, namedResults, out, funcsSeen)
		},
		empty: emptyFmtStringerParamState,
		clone: cloneFmtStringerParamState,
		merge: mergeFmtStringerParamStates,
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
	state.values[index] = dedupeFmtStringerParamUses(append(state.values[index], uses...))
}

func (state fmtStringerResultState) addSliceUses(index int, uses []fmtStringerParamUse) {
	state.slices[index] = dedupeFmtStringerParamUses(append(state.slices[index], uses...))
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

	argIndexes := fmtStringerArgIndexes(info, call, fn.Name())

	out := make([]fmtStringerParamUse, 0, len(argIndexes))
	for argIndex := range argIndexes {
		if argIndex < 0 || argIndex >= len(call.Args) {
			continue
		}

		arg := call.Args[argIndex]
		if call.Ellipsis.IsValid() && argIndex == len(call.Args)-1 {
			uses := fmtStringerSliceParamUsesForExpr(info, aliases, sliceAliases, arg)
			if len(uses) > 0 {
				out = append(out, uses...)
				continue
			}
		}

		out = append(out, fmtStringerParamUsesForExpr(info, aliases, arg)...)
	}

	return dedupeFmtStringerParamUses(out)
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

	return dedupeFmtStringerParamUses(out)
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

	if use.slice {
		return fmtStringerCallerSliceParamUses(info, aliases, sliceAliases, call, use)
	}

	if !use.variadic {
		return fmtStringerParamUsesForExpr(info, aliases, call.Args[use.index])
	}

	if call.Ellipsis.IsValid() && use.index == len(call.Args)-1 {
		return fmtStringerCallerVariadicSliceUses(
			info,
			aliases,
			sliceAliases,
			call.Args[use.index],
		)
	}

	out := make([]fmtStringerParamUse, 0, len(call.Args)-use.index)
	for index := use.index; index < len(call.Args); index++ {
		out = append(out, fmtStringerParamUsesForExpr(info, aliases, call.Args[index])...)
	}

	return dedupeFmtStringerParamUses(out)
}

func fmtStringerCallerSliceParamUses(
	info *types.Info,
	aliases map[fmtStringerVarRef][]fmtStringerParamUse,
	sliceAliases map[fmtStringerVarRef][]fmtStringerParamUse,
	call *ast.CallExpr,
	use fmtStringerParamUse,
) []fmtStringerParamUse {
	if !use.variadic {
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

	out := make([]fmtStringerParamUse, 0, len(call.Args)-use.index)
	for index := use.index; index < len(call.Args); index++ {
		out = append(out, fmtStringerParamUsesForExpr(info, aliases, call.Args[index])...)
	}

	return dedupeFmtStringerParamUses(out)
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

	return dedupeFmtStringerParamUses(out)
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

	out := make([]fmtStringerParamUse, 0, len(call.Args))
	if len(call.Args) > 0 {
		out = append(
			out,
			fmtStringerSliceParamUsesForExpr(info, aliases, sliceAliases, call.Args[0])...,
		)
	}

	for index := 1; index < len(call.Args); index++ {
		arg := call.Args[index]
		if call.Ellipsis.IsValid() && index == len(call.Args)-1 {
			out = append(
				out,
				fmtStringerSliceParamUsesForExpr(info, aliases, sliceAliases, arg)...,
			)

			continue
		}

		out = append(out, fmtStringerParamUsesForExpr(info, aliases, arg)...)
	}

	return dedupeFmtStringerParamUses(out)
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

func dedupeFmtStringerParamUses(uses []fmtStringerParamUse) []fmtStringerParamUse {
	if len(uses) < fmtStringerDedupeMinLen {
		return uses
	}

	seen := make(map[fmtStringerParamUse]struct{}, len(uses))

	out := make([]fmtStringerParamUse, 0, len(uses))
	for _, use := range uses {
		if _, ok := seen[use]; ok {
			continue
		}

		seen[use] = struct{}{}
		out = append(out, use)
	}

	return out
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
