package deadcode

import "go/ast"

type fmtStringerFlowOps[T any] struct {
	expr       func(T, ast.Node)
	assign     func(T, []ast.Expr, []ast.Expr)
	rangeValue func(T, *ast.RangeStmt)
	returns    func(T, *ast.ReturnStmt)
	empty      func() T
	clone      func(T) T
	merge      func(T, T) T
}

func walkFmtStringerFlowBlock[T any](
	state T,
	stmts []ast.Stmt,
	ops fmtStringerFlowOps[T],
) T {
	for _, stmt := range stmts {
		state = walkFmtStringerFlowStmt(state, stmt, ops)
	}

	return state
}

func walkFmtStringerFlowStmt[T any](
	state T,
	stmt ast.Stmt,
	ops fmtStringerFlowOps[T],
) T {
	switch stmt := stmt.(type) {
	case *ast.AssignStmt:
		ops.expr(state, stmt)
		ops.assign(state, stmt.Lhs, stmt.Rhs)

		return state
	case *ast.DeclStmt:
		walkFmtStringerFlowDecl(state, stmt.Decl, ops)

		return state
	case *ast.ExprStmt:
		ops.expr(state, stmt.X)

		return state
	case *ast.ReturnStmt:
		walkFmtStringerFlowReturn(state, stmt, ops)

		return state
	case *ast.IfStmt:
		return walkFmtStringerFlowIf(state, stmt, ops)
	case *ast.SwitchStmt:
		return walkFmtStringerFlowSwitch(state, stmt, ops)
	case *ast.TypeSwitchStmt:
		return walkFmtStringerFlowTypeSwitch(state, stmt, ops)
	case *ast.ForStmt:
		return walkFmtStringerFlowFor(state, stmt, ops)
	case *ast.RangeStmt:
		return walkFmtStringerFlowRange(state, stmt, ops)
	case *ast.BlockStmt:
		return walkFmtStringerFlowBlock(state, stmt.List, ops)
	}

	ops.expr(state, stmt)

	return state
}

func walkFmtStringerFlowReturn[T any](
	state T,
	stmt *ast.ReturnStmt,
	ops fmtStringerFlowOps[T],
) {
	if ops.returns != nil {
		ops.returns(state, stmt)
		return
	}

	for _, result := range stmt.Results {
		ops.expr(state, result)
	}
}

func walkFmtStringerFlowDecl[T any](
	state T,
	decl ast.Decl,
	ops fmtStringerFlowOps[T],
) {
	for _, valueSpec := range valueSpecsForDecl(decl) {
		for _, value := range valueSpec.Values {
			ops.expr(state, value)
		}

		ops.assign(state, identExprs(valueSpec.Names), valueSpec.Values)
	}
}

func walkFmtStringerFlowIf[T any](
	state T,
	stmt *ast.IfStmt,
	ops fmtStringerFlowOps[T],
) T {
	if stmt.Init != nil {
		state = walkFmtStringerFlowStmt(state, stmt.Init, ops)
	}

	ops.expr(state, stmt.Cond)

	thenState := ops.clone(state)
	elseState := ops.clone(state)

	thenState = walkFmtStringerFlowBlock(thenState, stmt.Body.List, ops)
	if stmt.Else != nil {
		elseState = walkFmtStringerFlowStmt(elseState, stmt.Else, ops)
	}

	return ops.merge(thenState, elseState)
}

func walkFmtStringerFlowSwitch[T any](
	state T,
	stmt *ast.SwitchStmt,
	ops fmtStringerFlowOps[T],
) T {
	if stmt.Init != nil {
		state = walkFmtStringerFlowStmt(state, stmt.Init, ops)
	}

	ops.expr(state, stmt.Tag)

	return walkFmtStringerFlowCases(state, stmt.Body, ops)
}

func walkFmtStringerFlowTypeSwitch[T any](
	state T,
	stmt *ast.TypeSwitchStmt,
	ops fmtStringerFlowOps[T],
) T {
	if stmt.Init != nil {
		state = walkFmtStringerFlowStmt(state, stmt.Init, ops)
	}

	ops.expr(state, stmt.Assign)

	return walkFmtStringerFlowCases(state, stmt.Body, ops)
}

func walkFmtStringerFlowCases[T any](
	state T,
	body *ast.BlockStmt,
	ops fmtStringerFlowOps[T],
) T {
	return walkCaseClauseStates(
		state,
		body,
		ops.empty,
		ops.clone,
		ops.merge,
		func(clauseState T, clause *ast.CaseClause) T {
			for _, expr := range clause.List {
				ops.expr(clauseState, expr)
			}

			return walkFmtStringerFlowBlock(clauseState, clause.Body, ops)
		},
	)
}

func walkCaseClauseStates[T any](
	state T,
	body *ast.BlockStmt,
	empty func() T,
	clone func(T) T,
	merge func(T, T) T,
	walkClause func(T, *ast.CaseClause) T,
) T {
	if body == nil {
		return clone(state)
	}

	merged := empty()
	hasDefault := false

	for _, stmt := range body.List {
		clause, ok := stmt.(*ast.CaseClause)
		if !ok {
			continue
		}

		hasDefault = hasDefault || len(clause.List) == 0
		clauseState := walkClause(clone(state), clause)
		merged = merge(merged, clauseState)
	}

	if !hasDefault {
		merged = merge(merged, state)
	}

	return merged
}

func walkFmtStringerFlowFor[T any](
	state T,
	stmt *ast.ForStmt,
	ops fmtStringerFlowOps[T],
) T {
	if stmt.Init != nil {
		state = walkFmtStringerFlowStmt(state, stmt.Init, ops)
	}

	ops.expr(state, stmt.Cond)

	bodyState := walkFmtStringerFlowBlock(ops.clone(state), stmt.Body.List, ops)
	walkFmtStringerFlowStmt(bodyState, stmt.Post, ops)

	return ops.merge(state, bodyState)
}

func walkFmtStringerFlowRange[T any](
	state T,
	stmt *ast.RangeStmt,
	ops fmtStringerFlowOps[T],
) T {
	ops.expr(state, stmt.X)

	bodyState := ops.clone(state)
	ops.rangeValue(bodyState, stmt)
	bodyState = walkFmtStringerFlowBlock(bodyState, stmt.Body.List, ops)

	return ops.merge(state, bodyState)
}
