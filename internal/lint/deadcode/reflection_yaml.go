package deadcode

import (
	"go/ast"
	"go/types"
)

func (graph deadCodeGraph) addReflectedYAMLMarshalReturnUses(
	out map[string]struct{},
	hook *types.Func,
	typ types.Type,
	tag string,
	call *ast.CallExpr,
	seen map[string]struct{},
) {
	if tag != reflectedYAMLTag || hook == nil || hook.Name() != reflectedMarshalYAMLHook {
		return
	}

	receiver := namedDeadCodeType(typ)
	if receiver == nil {
		return
	}

	hookL := graph.packageLinterForFunc(hook)
	if hookL == nil {
		return
	}

	decl := hookL.reflectedFuncDeclForFunc(hook)
	if decl == nil || decl.Body == nil {
		return
	}

	graph.addReflectedYAMLMarshalReturnDeclUses(
		hookL,
		out,
		decl,
		receiver,
		call,
		seen,
		make(map[string]struct{}),
	)
}

func (graph deadCodeGraph) addReflectedYAMLMarshalReturnExprUses(
	l *packageLinter,
	out map[string]struct{},
	expr ast.Expr,
	receiver *types.Named,
	fn *ast.FuncDecl,
	call *ast.CallExpr,
	seen map[string]struct{},
	funcsSeen map[string]struct{},
) {
	if graph.addReflectedYAMLMarshalReturnCallUses(
		l,
		out,
		expr,
		receiver,
		call,
		seen,
		funcsSeen,
	) {
		return
	}

	graph.addReflectedYAMLMarshalReturnTypeUses(
		l,
		out,
		reflectedValueType(l.pkg.TypesInfo, expr),
		receiver,
		fn,
		call,
		seen,
	)
}

func (graph deadCodeGraph) addReflectedYAMLMarshalReturnCallUses(
	l *packageLinter,
	out map[string]struct{},
	expr ast.Expr,
	receiver *types.Named,
	call *ast.CallExpr,
	seen map[string]struct{},
	funcsSeen map[string]struct{},
) bool {
	returnCall, ok := unparenReflectedExpr(expr).(*ast.CallExpr)
	if !ok {
		return false
	}

	fn := calledFunc(l.pkg.TypesInfo, returnCall)
	if fn == nil {
		return false
	}

	decl := l.reflectedFuncDeclForFunc(fn)
	if decl == nil || decl.Body == nil {
		return false
	}

	key := deadCodeObjectKey(fn)
	if key != "" {
		if _, ok := funcsSeen[key]; ok {
			return true
		}

		funcsSeen[key] = struct{}{}
		defer delete(funcsSeen, key)
	}

	graph.addReflectedYAMLMarshalReturnDeclUses(l, out, decl, receiver, call, seen, funcsSeen)

	return true
}

func (graph deadCodeGraph) addReflectedYAMLMarshalReturnDeclUses(
	l *packageLinter,
	out map[string]struct{},
	decl *ast.FuncDecl,
	receiver *types.Named,
	call *ast.CallExpr,
	seen map[string]struct{},
	funcsSeen map[string]struct{},
) {
	scan := yamlMarshalReturnScan{
		graph:       graph,
		l:           l,
		out:         out,
		decl:        decl,
		receiver:    receiver,
		call:        call,
		seen:        seen,
		funcsSeen:   funcsSeen,
		namedResult: yamlMarshalRepresentationResult(l.pkg.TypesInfo, decl),
	}
	scan.block(emptyYAMLMarshalReturnState(), decl.Body.List)
}

type yamlMarshalReturnState struct {
	values map[fmtStringerVarRef][]ast.Expr
	slices map[fmtStringerVarRef][]ast.Expr
}

type yamlMarshalReturnScan struct {
	graph       deadCodeGraph
	l           *packageLinter
	out         map[string]struct{}
	decl        *ast.FuncDecl
	receiver    *types.Named
	call        *ast.CallExpr
	seen        map[string]struct{}
	funcsSeen   map[string]struct{}
	namedResult *types.Var
}

func (scan yamlMarshalReturnScan) block(
	state yamlMarshalReturnState,
	stmts []ast.Stmt,
) yamlMarshalReturnState {
	for _, stmt := range stmts {
		state = scan.stmt(state, stmt)
	}

	return state
}

func (scan yamlMarshalReturnScan) stmt(
	state yamlMarshalReturnState,
	stmt ast.Stmt,
) yamlMarshalReturnState {
	switch stmt := stmt.(type) {
	case *ast.AssignStmt:
		scan.updateAssignState(state, stmt.Lhs, stmt.Rhs)
	case *ast.DeclStmt:
		scan.updateDeclState(state, stmt.Decl)
	case *ast.ReturnStmt:
		scan.returnStmt(state, stmt)
	case *ast.IfStmt:
		return scan.ifStmt(state, stmt)
	case *ast.SwitchStmt:
		return scan.switchStmt(state, stmt)
	case *ast.TypeSwitchStmt:
		return scan.typeSwitchStmt(state, stmt)
	case *ast.ForStmt:
		return scan.forStmt(state, stmt)
	case *ast.RangeStmt:
		return scan.rangeStmt(state, stmt)
	case *ast.BlockStmt:
		return scan.block(state, stmt.List)
	}

	return state
}

func (scan yamlMarshalReturnScan) ifStmt(
	state yamlMarshalReturnState,
	stmt *ast.IfStmt,
) yamlMarshalReturnState {
	if stmt.Init != nil {
		state = scan.stmt(state, stmt.Init)
	}

	thenState := cloneYAMLMarshalReturnState(state)
	elseState := cloneYAMLMarshalReturnState(state)

	thenState = scan.block(thenState, stmt.Body.List)
	if stmt.Else != nil {
		elseState = scan.stmt(elseState, stmt.Else)
	}

	return mergeYAMLMarshalReturnStates(thenState, elseState)
}

func (scan yamlMarshalReturnScan) switchStmt(
	state yamlMarshalReturnState,
	stmt *ast.SwitchStmt,
) yamlMarshalReturnState {
	if stmt.Init != nil {
		state = scan.stmt(state, stmt.Init)
	}

	return scan.caseClauses(state, stmt.Body)
}

func (scan yamlMarshalReturnScan) typeSwitchStmt(
	state yamlMarshalReturnState,
	stmt *ast.TypeSwitchStmt,
) yamlMarshalReturnState {
	if stmt.Init != nil {
		state = scan.stmt(state, stmt.Init)
	}

	return scan.caseClauses(state, stmt.Body)
}

func (scan yamlMarshalReturnScan) caseClauses(
	state yamlMarshalReturnState,
	body *ast.BlockStmt,
) yamlMarshalReturnState {
	return walkCaseClauseStates(
		state,
		body,
		emptyYAMLMarshalReturnState,
		cloneYAMLMarshalReturnState,
		mergeYAMLMarshalReturnStates,
		func(clauseState yamlMarshalReturnState, clause *ast.CaseClause) yamlMarshalReturnState {
			return scan.block(clauseState, clause.Body)
		},
	)
}

func (scan yamlMarshalReturnScan) forStmt(
	state yamlMarshalReturnState,
	stmt *ast.ForStmt,
) yamlMarshalReturnState {
	if stmt.Init != nil {
		state = scan.stmt(state, stmt.Init)
	}

	bodyState := scan.block(cloneYAMLMarshalReturnState(state), stmt.Body.List)
	if stmt.Post != nil {
		bodyState = scan.stmt(bodyState, stmt.Post)
	}

	return mergeYAMLMarshalReturnStates(state, bodyState)
}

func (scan yamlMarshalReturnScan) rangeStmt(
	state yamlMarshalReturnState,
	stmt *ast.RangeStmt,
) yamlMarshalReturnState {
	bodyState := cloneYAMLMarshalReturnState(state)

	rangeExprs, _ := scan.sliceExprsForState(state, stmt.X)
	if len(rangeExprs) > 0 {
		scan.setValueState(bodyState, stmt.Value, rangeExprs)
	}

	bodyState = scan.block(bodyState, stmt.Body.List)

	return mergeYAMLMarshalReturnStates(state, bodyState)
}

func (scan yamlMarshalReturnScan) setValueState(
	state yamlMarshalReturnState,
	expr ast.Expr,
	exprs []ast.Expr,
) {
	ref, ok := fmtStringerVarForExpr(scan.l.pkg.TypesInfo, expr)
	if !ok {
		return
	}

	state.values[ref] = exprs
	delete(state.slices, ref)
}

func (scan yamlMarshalReturnScan) updateDeclState(
	state yamlMarshalReturnState,
	decl ast.Decl,
) {
	for _, valueSpec := range valueSpecsForDecl(decl) {
		scan.updateAssignState(state, identExprs(valueSpec.Names), valueSpec.Values)
	}
}

func (scan yamlMarshalReturnScan) updateAssignState(
	state yamlMarshalReturnState,
	left []ast.Expr,
	right []ast.Expr,
) {
	snapshot := cloneYAMLMarshalReturnState(state)

	for index, lhs := range left {
		ref, ok := fmtStringerVarForExpr(scan.l.pkg.TypesInfo, lhs)
		if !ok {
			continue
		}

		rhs := fmtStringerRHSExpr(right, len(left), index)
		if rhs == nil {
			delete(state.values, ref)
			delete(state.slices, ref)

			continue
		}

		if exprs, ok := scan.sliceExprsForState(snapshot, rhs); ok {
			delete(state.values, ref)
			state.slices[ref] = exprs

			continue
		}

		state.values[ref] = scan.exprsForState(snapshot, rhs)
		delete(state.slices, ref)
	}
}

func (scan yamlMarshalReturnScan) returnStmt(
	state yamlMarshalReturnState,
	stmt *ast.ReturnStmt,
) {
	for _, expr := range yamlMarshalReturnExprs(
		scan.l.pkg.TypesInfo,
		scan.namedResult,
		state,
		stmt,
	) {
		scan.graph.addReflectedYAMLMarshalReturnExprUses(
			scan.l,
			scan.out,
			expr,
			scan.receiver,
			scan.decl,
			scan.call,
			scan.seen,
			scan.funcsSeen,
		)
	}
}

func yamlMarshalReturnExprs(
	info *types.Info,
	namedResult *types.Var,
	state yamlMarshalReturnState,
	stmt *ast.ReturnStmt,
) []ast.Expr {
	if len(stmt.Results) == 0 {
		if namedResult == nil {
			return nil
		}

		return state.values[fmtStringerVarRef{obj: namedResult}]
	}

	return yamlMarshalExprsForState(info, state, stmt.Results[0])
}

func (scan yamlMarshalReturnScan) exprsForState(
	state yamlMarshalReturnState,
	expr ast.Expr,
) []ast.Expr {
	return yamlMarshalExprsForState(scan.l.pkg.TypesInfo, state, expr)
}

func (scan yamlMarshalReturnScan) sliceExprsForState(
	state yamlMarshalReturnState,
	expr ast.Expr,
) ([]ast.Expr, bool) {
	if ref, ok := fmtStringerVarForExpr(scan.l.pkg.TypesInfo, expr); ok {
		exprs, ok := state.slices[ref]

		return exprs, ok
	}

	switch expr := unparenReflectedExpr(expr).(type) {
	case *ast.CompositeLit:
		if !yamlMarshalSliceType(scan.l.pkg.TypesInfo.TypeOf(expr)) {
			return nil, false
		}

		return scan.compositeSliceExprsForState(state, expr), true
	case *ast.CallExpr:
		return scan.appendSliceExprsForState(state, expr)
	default:
		return nil, false
	}
}

func (scan yamlMarshalReturnScan) compositeSliceExprsForState(
	state yamlMarshalReturnState,
	lit *ast.CompositeLit,
) []ast.Expr {
	out := make([]ast.Expr, 0, len(lit.Elts))
	for _, elt := range lit.Elts {
		out = append(out, scan.exprsForState(state, flowElementValue(elt))...)
	}

	return dedupeYAMLMarshalReturnExprs(out)
}

func (scan yamlMarshalReturnScan) appendSliceExprsForState(
	state yamlMarshalReturnState,
	call *ast.CallExpr,
) ([]ast.Expr, bool) {
	if !fmtStringerAppendCall(scan.l.pkg.TypesInfo, call) {
		return nil, false
	}

	return fmtStringerAppendArgValues(
		call,
		func(expr ast.Expr) ([]ast.Expr, bool) {
			return scan.sliceExprsForState(state, expr)
		},
		func(expr ast.Expr) ([]ast.Expr, bool) {
			return scan.exprsForState(state, expr), true
		},
		dedupeYAMLMarshalReturnExprs,
	), true
}

func yamlMarshalExprsForState(
	info *types.Info,
	state yamlMarshalReturnState,
	expr ast.Expr,
) []ast.Expr {
	if ref, ok := fmtStringerVarForExpr(info, expr); ok {
		if exprs := state.values[ref]; len(exprs) > 0 {
			return exprs
		}
	}

	if lit, ok := unparenReflectedExpr(expr).(*ast.CompositeLit); ok {
		return yamlMarshalCompositeExprsForState(info, state, lit)
	}

	return []ast.Expr{expr}
}

func yamlMarshalCompositeExprsForState(
	info *types.Info,
	state yamlMarshalReturnState,
	lit *ast.CompositeLit,
) []ast.Expr {
	out := []ast.Expr{lit}
	for _, elt := range lit.Elts {
		if keyed, ok := unparenReflectedExpr(elt).(*ast.KeyValueExpr); ok {
			out = append(out, yamlMarshalExprsForState(info, state, keyed.Key)...)
			out = append(out, yamlMarshalExprsForState(info, state, keyed.Value)...)

			continue
		}

		out = append(out, yamlMarshalExprsForState(info, state, elt)...)
	}

	return dedupeYAMLMarshalReturnExprs(out)
}

func yamlMarshalSliceType(typ types.Type) bool {
	switch types.Unalias(typ).Underlying().(type) {
	case *types.Array, *types.Slice:
		return true
	default:
		return false
	}
}

func dedupeYAMLMarshalReturnExprs(exprs []ast.Expr) []ast.Expr {
	if len(exprs) < reflectedDedupeMinLen {
		return exprs
	}

	seen := make(map[ast.Expr]struct{}, len(exprs))

	out := make([]ast.Expr, 0, len(exprs))
	for _, expr := range exprs {
		if _, ok := seen[expr]; ok {
			continue
		}

		seen[expr] = struct{}{}
		out = append(out, expr)
	}

	return out
}

func cloneYAMLMarshalReturnState(state yamlMarshalReturnState) yamlMarshalReturnState {
	return yamlMarshalReturnState{
		values: cloneYAMLMarshalReturnMap(state.values),
		slices: cloneYAMLMarshalReturnMap(state.slices),
	}
}

func emptyYAMLMarshalReturnState() yamlMarshalReturnState {
	return yamlMarshalReturnState{
		values: make(map[fmtStringerVarRef][]ast.Expr),
		slices: make(map[fmtStringerVarRef][]ast.Expr),
	}
}

func cloneYAMLMarshalReturnMap(
	source map[fmtStringerVarRef][]ast.Expr,
) map[fmtStringerVarRef][]ast.Expr {
	out := make(map[fmtStringerVarRef][]ast.Expr, len(source))
	for key, exprs := range source {
		out[key] = append([]ast.Expr(nil), exprs...)
	}

	return out
}

func mergeYAMLMarshalReturnStates(
	first yamlMarshalReturnState,
	second yamlMarshalReturnState,
) yamlMarshalReturnState {
	out := cloneYAMLMarshalReturnState(first)
	for key, exprs := range second.values {
		out.values[key] = dedupeYAMLMarshalReturnExprs(append(out.values[key], exprs...))
	}

	for key, exprs := range second.slices {
		out.slices[key] = dedupeYAMLMarshalReturnExprs(append(out.slices[key], exprs...))
	}

	return out
}

func yamlMarshalRepresentationResult(info *types.Info, decl *ast.FuncDecl) *types.Var {
	if decl == nil || decl.Type == nil || decl.Type.Results == nil ||
		len(decl.Type.Results.List) == 0 {
		return nil
	}

	first := decl.Type.Results.List[0]
	if len(first.Names) == 0 {
		return nil
	}

	obj, _ := info.Defs[first.Names[0]].(*types.Var)

	return obj
}

func (graph deadCodeGraph) addReflectedYAMLMarshalReturnTypeUses(
	l *packageLinter,
	out map[string]struct{},
	typ types.Type,
	receiver *types.Named,
	fn *ast.FuncDecl,
	call *ast.CallExpr,
	seen map[string]struct{},
) {
	if typ == nil {
		return
	}

	if elem, ok := reflectedSequentialContainerElem(typ); ok {
		graph.addReflectedYAMLMarshalReturnTypeUses(l, out, elem, receiver, fn, call, seen)

		return
	}

	if key, elem, ok := reflectedMapTypes(typ); ok {
		graph.addReflectedYAMLMarshalReturnTypeUses(l, out, key, receiver, fn, call, seen)
		graph.addReflectedYAMLMarshalReturnTypeUses(l, out, elem, receiver, fn, call, seen)

		return
	}

	named, structType := reflectedNamedStructType(typ)
	if named == nil || structType == nil {
		return
	}

	target := l.reflectedAliasTargetNamedForFunc(named, fn)
	if !sameNamedType(receiver, target) {
		return
	}

	graph.addReflectedStructFieldsWithOwners(
		l,
		out,
		[]*types.Named{receiver},
		structType,
		reflectedCodecUseForTag(reflectedYAMLTag),
		call,
		seen,
		reflectedMarshalStructFields,
		reflectedMarshalUnaddressable,
	)
}

func (graph deadCodeGraph) packageLinterForFunc(fn *types.Func) *packageLinter {
	pkg := graph.packageForFunc(fn)
	if pkg == nil {
		return nil
	}

	return newPackageLinter(pkg)
}

func (l *packageLinter) reflectedFuncDeclForFunc(fn *types.Func) *ast.FuncDecl {
	if fn == nil {
		return nil
	}

	key := deadCodeObjectKey(fn)
	if key == "" {
		return nil
	}

	for _, decl := range l.pkg.ProductionFuncs {
		obj, _ := l.pkg.TypesInfo.Defs[decl.Name].(*types.Func)
		if obj != nil && deadCodeObjectKey(obj) == key {
			return decl
		}
	}

	return nil
}
