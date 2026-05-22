package deadcode

import (
	"go/ast"
	"go/token"
	"go/types"
)

func (graph deadCodeGraph) collectFuncLitArgReflectedTypeParamDecodes(
	pkg *Package,
	call *ast.CallExpr,
	typeParamIndexes map[*types.TypeParam]int,
	scope *ast.BlockStmt,
	funcsSeen map[string]struct{},
	out *[]reflectedTypeParamUse,
) {
	graph.inspectInvokedReflectedFuncLitArgCalls(pkg, scope, call, funcsSeen, func(
		nested *ast.CallExpr,
		nestedScope *ast.BlockStmt,
	) {
		graph.collectReflectedDecodeCallTypeParamDecodes(
			pkg,
			nested,
			typeParamIndexes,
			nestedScope,
			funcsSeen,
			out,
		)
	})
}

func (graph deadCodeGraph) collectFuncLitArgReflectedTypeParamMarshals(
	pkg *Package,
	call *ast.CallExpr,
	typeParamIndexes map[*types.TypeParam]int,
	scope *ast.BlockStmt,
	funcsSeen map[string]struct{},
	out *[]reflectedMarshalTypeParamUse,
) {
	graph.inspectInvokedReflectedFuncLitArgCalls(pkg, scope, call, funcsSeen, func(
		nested *ast.CallExpr,
		nestedScope *ast.BlockStmt,
	) {
		graph.collectReflectedMarshalCallTypeParamUses(
			pkg,
			nested,
			typeParamIndexes,
			nestedScope,
			funcsSeen,
			out,
		)
	})
}

func (graph deadCodeGraph) inspectInvokedReflectedFuncLitArgCalls(
	pkg *Package,
	scope *ast.BlockStmt,
	call *ast.CallExpr,
	funcsSeen map[string]struct{},
	visit func(*ast.CallExpr, *ast.BlockStmt),
) {
	if scope == nil {
		return
	}

	indexes := graph.invokedFuncParamIndexes(calledFunc(pkg.TypesInfo, call), funcsSeen)
	for index := range indexes {
		if index >= len(call.Args) {
			continue
		}

		for _, lit := range reflectedFuncLitArgs(pkg, scope, call.Args[index], call.Pos()) {
			if lit == nil || lit.Body == nil {
				continue
			}

			ast.Inspect(lit.Body, func(n ast.Node) bool {
				if _, ok := n.(*ast.FuncLit); ok {
					return false
				}

				nested, ok := n.(*ast.CallExpr)
				if ok {
					visit(nested, lit.Body)
				}

				return true
			})
		}
	}
}

func reflectedFuncLitArgs(
	pkg *Package,
	scope *ast.BlockStmt,
	arg ast.Expr,
	before token.Pos,
) []*ast.FuncLit {
	if lit, ok := unparenReflectedExpr(arg).(*ast.FuncLit); ok {
		return []*ast.FuncLit{lit}
	}

	ident, ok := unparenReflectedExpr(arg).(*ast.Ident)
	if !ok || pkg == nil || pkg.TypesInfo == nil {
		return nil
	}

	obj := pkg.TypesInfo.ObjectOf(ident)
	if obj == nil {
		return nil
	}

	return assignedFuncLitsBefore(pkg.TypesInfo, scope, obj, before)
}

func assignedFuncLitsBefore(
	info *types.Info,
	scope *ast.BlockStmt,
	target types.Object,
	before token.Pos,
) []*ast.FuncLit {
	return reflectedFuncLitStateBlock(
		info,
		scope.List,
		target,
		before,
		reflectedFuncLitState{},
	).lits
}

type reflectedFuncLitState struct {
	lits []*ast.FuncLit
	seen map[*ast.FuncLit]struct{}
}

func reflectedFuncLitStateBlock(
	info *types.Info,
	stmts []ast.Stmt,
	target types.Object,
	before token.Pos,
	state reflectedFuncLitState,
) reflectedFuncLitState {
	for _, stmt := range stmts {
		if stmt == nil || stmt.Pos() >= before {
			continue
		}

		state = reflectedFuncLitStateStmt(info, stmt, target, before, state)
	}

	return state
}

func reflectedFuncLitStateStmt(
	info *types.Info,
	stmt ast.Stmt,
	target types.Object,
	before token.Pos,
	state reflectedFuncLitState,
) reflectedFuncLitState {
	switch stmt := stmt.(type) {
	case *ast.AssignStmt:
		return state.assign(info, stmt, target, before)
	case *ast.DeclStmt:
		return state.decl(info, stmt.Decl, target, before)
	case *ast.IfStmt:
		return reflectedFuncLitStateIf(info, stmt, target, before, state)
	case *ast.SwitchStmt:
		return reflectedFuncLitStateSwitch(info, stmt, target, before, state)
	case *ast.TypeSwitchStmt:
		return reflectedFuncLitStateTypeSwitch(info, stmt, target, before, state)
	case *ast.BlockStmt:
		return reflectedFuncLitStateBlock(info, stmt.List, target, before, state)
	default:
		return state
	}
}

func reflectedFuncLitStateIf(
	info *types.Info,
	stmt *ast.IfStmt,
	target types.Object,
	before token.Pos,
	state reflectedFuncLitState,
) reflectedFuncLitState {
	if stmt.Init != nil {
		state = reflectedFuncLitStateStmt(info, stmt.Init, target, before, state)
	}

	thenState := reflectedFuncLitStateBlock(info, stmt.Body.List, target, before, state)

	elseState := state
	if stmt.Else != nil {
		elseState = reflectedFuncLitStateStmt(info, stmt.Else, target, before, state)
	}

	return mergeReflectedFuncLitStates(thenState, elseState)
}

func reflectedFuncLitStateSwitch(
	info *types.Info,
	stmt *ast.SwitchStmt,
	target types.Object,
	before token.Pos,
	state reflectedFuncLitState,
) reflectedFuncLitState {
	if stmt.Init != nil {
		state = reflectedFuncLitStateStmt(info, stmt.Init, target, before, state)
	}

	return reflectedFuncLitStateCaseClauses(info, stmt.Body, target, before, state)
}

func reflectedFuncLitStateTypeSwitch(
	info *types.Info,
	stmt *ast.TypeSwitchStmt,
	target types.Object,
	before token.Pos,
	state reflectedFuncLitState,
) reflectedFuncLitState {
	if stmt.Init != nil {
		state = reflectedFuncLitStateStmt(info, stmt.Init, target, before, state)
	}

	if stmt.Assign != nil {
		state = reflectedFuncLitStateStmt(info, stmt.Assign, target, before, state)
	}

	return reflectedFuncLitStateCaseClauses(info, stmt.Body, target, before, state)
}

func reflectedFuncLitStateCaseClauses(
	info *types.Info,
	body *ast.BlockStmt,
	target types.Object,
	before token.Pos,
	state reflectedFuncLitState,
) reflectedFuncLitState {
	if body == nil {
		return state
	}

	states := make([]reflectedFuncLitState, 0, len(body.List)+1)
	hasDefault := false

	for _, stmt := range body.List {
		clause, ok := stmt.(*ast.CaseClause)
		if !ok || clause.Pos() >= before {
			continue
		}

		if clause.List == nil {
			hasDefault = true
		}

		states = append(
			states,
			reflectedFuncLitStateBlock(info, clause.Body, target, before, state),
		)
	}

	if !hasDefault {
		states = append(states, state)
	}

	return mergeReflectedFuncLitStates(states...)
}

func (state reflectedFuncLitState) assign(
	info *types.Info,
	assign *ast.AssignStmt,
	target types.Object,
	before token.Pos,
) reflectedFuncLitState {
	for index, lhs := range assign.Lhs {
		if reflectedExprObject(info, lhs) != target {
			continue
		}

		state = reflectedFuncLitStateForValue(
			assign.Pos(),
			reflectedExprAt(assign.Rhs, index),
			before,
		)
	}

	return state
}

func (state reflectedFuncLitState) decl(
	info *types.Info,
	decl ast.Decl,
	target types.Object,
	before token.Pos,
) reflectedFuncLitState {
	for _, spec := range valueSpecsForDecl(decl) {
		for index, name := range spec.Names {
			if info.Defs[name] != target {
				continue
			}

			state = reflectedFuncLitStateForValue(
				spec.Pos(),
				reflectedExprAt(spec.Values, index),
				before,
			)
		}
	}

	return state
}

func reflectedFuncLitStateForValue(
	pos token.Pos,
	value ast.Expr,
	before token.Pos,
) reflectedFuncLitState {
	if pos >= before {
		return reflectedFuncLitState{}
	}

	lit, _ := unparenReflectedExpr(value).(*ast.FuncLit)
	if lit == nil {
		return reflectedFuncLitState{}
	}

	return reflectedFuncLitState{}.add(lit)
}

func mergeReflectedFuncLitStates(states ...reflectedFuncLitState) reflectedFuncLitState {
	var out reflectedFuncLitState

	for _, state := range states {
		for _, lit := range state.lits {
			out = out.add(lit)
		}
	}

	return out
}

func (state reflectedFuncLitState) add(lit *ast.FuncLit) reflectedFuncLitState {
	if lit == nil {
		return state
	}

	if state.seen == nil {
		state.seen = make(map[*ast.FuncLit]struct{})
	}

	if _, ok := state.seen[lit]; ok {
		return state
	}

	state.seen[lit] = struct{}{}
	state.lits = append(state.lits, lit)

	return state
}

func reflectedExprAt(exprs []ast.Expr, index int) ast.Expr {
	if index >= len(exprs) {
		return nil
	}

	return exprs[index]
}

func reflectedExprObject(info *types.Info, expr ast.Expr) types.Object {
	ident, ok := unparenReflectedExpr(expr).(*ast.Ident)
	if !ok || info == nil {
		return nil
	}

	return info.ObjectOf(ident)
}

func (graph deadCodeGraph) invokedFuncParamIndexes(
	fn *types.Func,
	funcsSeen map[string]struct{},
) map[int]struct{} {
	pkg, decl, _, ok := graph.reflectedWrapperFunc(fn)
	if !ok {
		return nil
	}

	sig, _ := fn.Type().(*types.Signature)

	paramIndexes := sourceFuncParamObjectIndexes(pkg.TypesInfo, sig, decl)
	if len(paramIndexes) == 0 {
		return nil
	}

	key := deadCodeObjectKey(fn)
	if key != "" {
		if _, ok := funcsSeen[key]; ok {
			return nil
		}

		funcsSeen[key] = struct{}{}
		defer delete(funcsSeen, key)
	}

	out := make(map[int]struct{})

	inspectReflectedBodyCalls(decl.Body, func(call *ast.CallExpr) {
		graph.collectInvokedFuncParamIndexes(pkg, call, paramIndexes, funcsSeen, out)
	})

	return out
}

func (graph deadCodeGraph) collectInvokedFuncParamIndexes(
	pkg *Package,
	call *ast.CallExpr,
	paramIndexes map[types.Object]int,
	funcsSeen map[string]struct{},
	out map[int]struct{},
) {
	if index, ok := directInvokedFuncParamIndex(pkg, call, paramIndexes); ok {
		out[index] = struct{}{}

		return
	}

	calleeIndexes := graph.invokedFuncParamIndexes(calledFunc(pkg.TypesInfo, call), funcsSeen)
	for calleeIndex := range calleeIndexes {
		if calleeIndex >= len(call.Args) {
			continue
		}

		if index, ok := reflectedWrapperParamIndex(pkg, call.Args[calleeIndex], paramIndexes); ok {
			out[index] = struct{}{}
		}
	}
}

func directInvokedFuncParamIndex(
	pkg *Package,
	call *ast.CallExpr,
	paramIndexes map[types.Object]int,
) (int, bool) {
	ident, ok := unparenReflectedExpr(call.Fun).(*ast.Ident)
	if !ok {
		return 0, false
	}

	index, ok := paramIndexes[pkg.TypesInfo.ObjectOf(ident)]

	return index, ok
}
