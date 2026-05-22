package deadcode

import (
	"go/ast"
	"go/token"
	"go/types"
)

type sourceFuncParamBinding struct {
	ident *ast.Ident
	name  string
	typ   types.Type
	index int
}

func sourceFuncParamBindings(
	sig *types.Signature,
	decl *ast.FuncDecl,
) []sourceFuncParamBinding {
	if sig == nil || decl.Type == nil || decl.Type.Params == nil {
		return nil
	}

	out := make([]sourceFuncParamBinding, 0, tupleLen(sig.Params()))
	index := 0

	for _, field := range decl.Type.Params.List {
		for _, name := range field.Names {
			if index >= tupleLen(sig.Params()) {
				return out
			}

			out = append(out, sourceFuncParamBinding{
				ident: name,
				name:  name.Name,
				typ:   sig.Params().At(index).Type(),
				index: index,
			})
			index++
		}

		if len(field.Names) == 0 {
			index++
		}
	}

	return out
}

func sourceFuncParamTypes(
	sig *types.Signature,
	decl *ast.FuncDecl,
) map[string]types.Type {
	out := make(map[string]types.Type)
	for _, binding := range sourceFuncParamBindings(sig, decl) {
		out[binding.name] = binding.typ
	}

	return out
}

func sourceFuncParamTypeAt(
	paramTypes map[string]types.Type,
	body *ast.BlockStmt,
	ident *ast.Ident,
) types.Type {
	if ident == nil {
		return nil
	}

	typ := paramTypes[ident.Name]
	if typ == nil || sourceParamNameShadowedAt(body, ident.Name, ident.Pos()) {
		return nil
	}

	return typ
}

func sourceParamNameShadowedAt(
	body *ast.BlockStmt,
	name string,
	pos token.Pos,
) bool {
	if body == nil {
		return false
	}

	return sourceParamNameShadowedInStmtList(
		body.List,
		name,
		pos,
		sourceParamShadowState{root: true},
	)
}

type sourceParamShadowState struct {
	root     bool
	shadowed bool
}

func sourceParamNameShadowedInStmtList(
	stmts []ast.Stmt,
	name string,
	pos token.Pos,
	state sourceParamShadowState,
) bool {
	for _, stmt := range stmts {
		if stmt == nil || stmt.Pos() >= pos {
			continue
		}

		if nodeContainsPos(stmt, pos) {
			return sourceParamNameShadowedInStmt(stmt, name, pos, state)
		}

		if !state.root && sourceStmtDeclaresNameInScope(stmt, name) {
			state.shadowed = true
		}
	}

	return state.shadowed
}

func sourceParamNameShadowedInStmt(
	stmt ast.Stmt,
	name string,
	pos token.Pos,
	state sourceParamShadowState,
) bool {
	switch stmt := stmt.(type) {
	case *ast.BlockStmt:
		return sourceParamNameShadowedInNestedStmtList(stmt.List, name, pos, state)
	case *ast.IfStmt:
		return sourceParamNameShadowedInIf(stmt, name, pos, state)
	case *ast.ForStmt:
		return sourceParamNameShadowedInFor(stmt, name, pos, state)
	case *ast.RangeStmt:
		return sourceParamNameShadowedInRange(stmt, name, pos, state)
	case *ast.SwitchStmt:
		return sourceParamNameShadowedInSwitch(stmt, name, pos, state)
	case *ast.TypeSwitchStmt:
		return sourceParamNameShadowedInTypeSwitch(stmt, name, pos, state)
	case *ast.SelectStmt:
		return sourceParamNameShadowedInClauses(stmt.Body, name, pos, state)
	case *ast.CaseClause:
		return sourceParamNameShadowedInNestedStmtList(stmt.Body, name, pos, state)
	case *ast.CommClause:
		return sourceParamNameShadowedInNestedStmtList(stmt.Body, name, pos, state)
	default:
		return state.shadowed
	}
}

func sourceParamNameShadowedInNestedStmtList(
	stmts []ast.Stmt,
	name string,
	pos token.Pos,
	state sourceParamShadowState,
) bool {
	state.root = false

	return sourceParamNameShadowedInStmtList(stmts, name, pos, state)
}

func sourceParamNameShadowedInIf(
	stmt *ast.IfStmt,
	name string,
	pos token.Pos,
	state sourceParamShadowState,
) bool {
	if next, result, ok := sourceParamNameShadowedInScopedBody(
		stmt.Init,
		stmt.Body,
		name,
		pos,
		state,
	); ok {
		return result
	} else {
		state = next
	}

	if stmt.Else != nil && nodeContainsPos(stmt.Else, pos) {
		return sourceParamNameShadowedInStmt(stmt.Else, name, pos, state)
	}

	return state.shadowed
}

func sourceParamNameShadowedInFor(
	stmt *ast.ForStmt,
	name string,
	pos token.Pos,
	state sourceParamShadowState,
) bool {
	_, result, _ := sourceParamNameShadowedInScopedBody(stmt.Init, stmt.Body, name, pos, state)

	return result
}

func sourceParamNameShadowedInScopedBody(
	init ast.Stmt,
	body *ast.BlockStmt,
	name string,
	pos token.Pos,
	state sourceParamShadowState,
) (sourceParamShadowState, bool, bool) {
	next, done := sourceShadowedAfterScopedStmt(init, name, pos, state)
	if done {
		return next, next.shadowed, true
	}

	if nodeContainsPos(body, pos) {
		return next, sourceParamNameShadowedInNestedStmtList(body.List, name, pos, next), true
	}

	return next, next.shadowed, false
}

func sourceParamNameShadowedInRange(
	stmt *ast.RangeStmt,
	name string,
	pos token.Pos,
	state sourceParamShadowState,
) bool {
	if nodeContainsPos(stmt.X, pos) {
		return state.shadowed
	}

	if stmt.Tok == token.DEFINE &&
		(sourceExprName(stmt.Key) == name || sourceExprName(stmt.Value) == name) {
		state.shadowed = true
	}

	if nodeContainsPos(stmt.Body, pos) {
		return sourceParamNameShadowedInNestedStmtList(stmt.Body.List, name, pos, state)
	}

	return state.shadowed
}

func sourceParamNameShadowedInSwitch(
	stmt *ast.SwitchStmt,
	name string,
	pos token.Pos,
	state sourceParamShadowState,
) bool {
	next, done := sourceShadowedAfterScopedStmt(stmt.Init, name, pos, state)
	if done {
		return next.shadowed
	}

	if nodeContainsPos(stmt.Tag, pos) {
		return next.shadowed
	}

	return sourceParamNameShadowedInClauses(stmt.Body, name, pos, next)
}

func sourceParamNameShadowedInTypeSwitch(
	stmt *ast.TypeSwitchStmt,
	name string,
	pos token.Pos,
	state sourceParamShadowState,
) bool {
	next, done := sourceShadowedAfterScopedStmt(stmt.Init, name, pos, state)
	if done {
		return next.shadowed
	}

	next, done = sourceShadowedAfterScopedStmt(stmt.Assign, name, pos, next)
	if done {
		return next.shadowed
	}

	return sourceParamNameShadowedInClauses(stmt.Body, name, pos, next)
}

func sourceParamNameShadowedInClauses(
	body *ast.BlockStmt,
	name string,
	pos token.Pos,
	state sourceParamShadowState,
) bool {
	if body == nil {
		return state.shadowed
	}

	for _, stmt := range body.List {
		if stmt != nil && nodeContainsPos(stmt, pos) {
			return sourceParamNameShadowedInStmt(stmt, name, pos, state)
		}
	}

	return state.shadowed
}

func sourceShadowedAfterScopedStmt(
	stmt ast.Stmt,
	name string,
	pos token.Pos,
	state sourceParamShadowState,
) (sourceParamShadowState, bool) {
	if nodeContainsPos(stmt, pos) {
		return state, true
	}

	if sourceStmtDeclaresNameInScope(stmt, name) {
		state.shadowed = true
	}

	return state, false
}

func sourceStmtDeclaresNameInScope(stmt ast.Stmt, name string) bool {
	switch stmt := stmt.(type) {
	case *ast.AssignStmt:
		return stmt.Tok == token.DEFINE && sourceExprListHasName(stmt.Lhs, name)
	case *ast.DeclStmt:
		for _, spec := range valueSpecsForDecl(stmt.Decl) {
			for _, ident := range spec.Names {
				if ident.Name == name {
					return true
				}
			}
		}
	}

	return false
}

func sourceExprListHasName(exprs []ast.Expr, name string) bool {
	for _, expr := range exprs {
		if sourceExprName(expr) == name {
			return true
		}
	}

	return false
}

func sourceExprName(expr ast.Expr) string {
	ident, _ := unparenReflectedExpr(expr).(*ast.Ident)
	if ident == nil {
		return ""
	}

	return ident.Name
}

func sourceFuncParamObjectIndexes(
	info *types.Info,
	sig *types.Signature,
	decl *ast.FuncDecl,
) map[types.Object]int {
	out := make(map[types.Object]int)
	if info == nil {
		return out
	}

	for _, binding := range sourceFuncParamBindings(sig, decl) {
		if obj := info.Defs[binding.ident]; obj != nil {
			out[obj] = binding.index
		}
	}

	return out
}
