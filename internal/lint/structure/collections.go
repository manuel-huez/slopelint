package structure

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"slices"
)

func (l *Runner) checkRedundantAppendLenGuard(stmt ast.Stmt) {
	match, ok := l.redundantAppendLenGuard(stmt)
	if !ok {
		return
	}

	l.report(
		match.pos,
		"append_ceremony",
		fmt.Sprintf(
			`len guard before append(%s...) is redundant; append no-ops for empty variadic slices`,
			match.source,
		),
	)
}

func (l *Runner) redundantAppendLenGuard(stmt ast.Stmt) (appendLenGuardMatch, bool) {
	ifStmt, ok := l.plainIfStmt(stmt)
	if !ok {
		return appendLenGuardMatch{}, false
	}

	source, ok := l.nonEmptyLenGuardSource(ifStmt.Cond)
	if !ok {
		return appendLenGuardMatch{}, false
	}

	assign, ok := singleAssignStmt(ifStmt.Body.List)
	if !ok || !l.assignAppendsGuardedSlice(assign, source) {
		return appendLenGuardMatch{}, false
	}

	return appendLenGuardMatch{pos: ifStmt.If, source: source}, true
}

func (l *Runner) plainIfStmt(stmt ast.Stmt) (*ast.IfStmt, bool) {
	ifStmt, ok := stmt.(*ast.IfStmt)
	if !ok {
		return nil, false
	}

	if ifStmt.Init != nil || ifStmt.Else != nil || l.hasAttachedComment(ifStmt) {
		return nil, false
	}

	return ifStmt, true
}

func singleAssignStmt(stmts []ast.Stmt) (*ast.AssignStmt, bool) {
	if len(stmts) != 1 {
		return nil, false
	}

	assign, ok := stmts[0].(*ast.AssignStmt)

	return assign, ok
}

func (l *Runner) assignAppendsGuardedSlice(assign *ast.AssignStmt, source string) bool {
	call, ok := l.appendAssignCall(assign)
	if !ok {
		return false
	}

	if exprHasCalls(assign.Lhs[0]) || exprHasCalls(call.Args[0]) || exprHasCalls(call.Args[1]) {
		return false
	}

	return l.render(assign.Lhs[0]) == l.render(call.Args[0]) && l.render(call.Args[1]) == source
}

func (l *Runner) appendAssignCall(assign *ast.AssignStmt) (*ast.CallExpr, bool) {
	if assign.Tok != token.ASSIGN || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
		return nil, false
	}

	call, ok := l.unparen(assign.Rhs[0]).(*ast.CallExpr)
	if !ok {
		return nil, false
	}

	if call.Ellipsis == token.NoPos || len(call.Args) != 2 || !l.isBuiltinCall(call, "append") {
		return nil, false
	}

	return call, true
}

func (l *Runner) nonEmptyLenGuardSource(expr ast.Expr) (string, bool) {
	binary, ok := l.unparen(expr).(*ast.BinaryExpr)
	if !ok {
		return "", false
	}

	if source, limit, ok := l.lenCompareOperand(binary.X, binary.Y); ok {
		return source, lenCompareProvesNonEmpty(binary.Op, limit)
	}

	if source, limit, ok := l.lenCompareOperand(binary.Y, binary.X); ok {
		return source, lenCompareProvesNonEmpty(reverseOrderedOp(binary.Op), limit)
	}

	return "", false
}

func (l *Runner) lenCompareOperand(lenExpr ast.Expr, limitExpr ast.Expr) (string, int64, bool) {
	call, ok := l.unparen(lenExpr).(*ast.CallExpr)
	if !ok || len(call.Args) != 1 || !l.isBuiltinCall(call, "len") {
		return "", 0, false
	}

	limitScalar, ok := l.scalarOf(limitExpr)
	if !ok || limitScalar.kind != scalarInt {
		return "", 0, false
	}

	limit, ok := scalarIntValue(limitScalar)
	if !ok {
		return "", 0, false
	}

	return l.render(call.Args[0]), limit, true
}

func lenCompareProvesNonEmpty(op token.Token, limit int64) bool {
	//exhaustive:ignore token.Token includes operators irrelevant to len guards.
	switch op {
	case token.GTR:
		return limit == 0
	case token.GEQ:
		return limit == 1
	case token.NEQ:
		return limit == 0
	default:
		return false
	}
}

func (l *Runner) isBuiltinCall(call *ast.CallExpr, name string) bool {
	id, ok := l.unparen(call.Fun).(*ast.Ident)
	if !ok || id.Name != name {
		return false
	}

	obj, ok := l.pkg.TypesInfo.ObjectOf(id).(*types.Builtin)

	return ok && obj != nil && obj.Name() == name
}
func (l *Runner) checkDuplicateAdjacentRangeLoop(stmts []ast.Stmt, idx int) {
	if idx == 0 {
		return
	}

	current, ok := l.rangeLoopShape(stmts[idx])
	if !ok {
		return
	}

	prior, ok := l.rangeLoopShape(stmts[idx-1])
	if !ok || current.key != prior.key {
		return
	}

	l.report(
		current.pos,
		"loop_ceremony",
		"adjacent range loop repeats previous loop body; merge ranges or collapse shared input list",
	)
}

func (l *Runner) rangeLoopShape(stmt ast.Stmt) (rangeLoopShape, bool) {
	loop, ok := stmt.(*ast.RangeStmt)
	if !ok || loop.Body == nil || l.hasAttachedComment(loop) {
		return rangeLoopShape{}, false
	}

	if len(loop.Body.List) == 0 || len(loop.Body.List) > rangeLoopMaxStmts {
		return rangeLoopShape{}, false
	}

	if rangeLoopBodyHasComplexControl(loop.Body.List) {
		return rangeLoopShape{}, false
	}

	keyName, keyObj := l.rangeLoopIdent(loop.Key)

	valueName, valueObj := l.rangeLoopIdent(loop.Value)
	if valueObj == nil || !nodeUsesObject(loop.Body, valueObj, l.pkg.TypesInfo) {
		return rangeLoopShape{}, false
	}

	rendered := l.renderStmtList(loop.Body.List)

	rendered = normalizeRenderedIdentifier(rendered, valueName, "$value")
	if keyObj != nil {
		rendered = normalizeRenderedIdentifier(rendered, keyName, "$key")
	}

	return rangeLoopShape{key: rendered, pos: loop.For}, true
}

func (l *Runner) rangeLoopIdent(expr ast.Expr) (string, types.Object) {
	id, ok := l.unparen(expr).(*ast.Ident)
	if !ok || id.Name == "_" {
		return "", nil
	}

	obj := l.pkg.TypesInfo.ObjectOf(id)
	if obj == nil {
		return "", nil
	}

	return id.Name, obj
}

func rangeLoopBodyHasComplexControl(stmts []ast.Stmt) bool {
	return slices.ContainsFunc(stmts, rangeLoopStmtHasComplexControl)
}

func rangeLoopStmtHasComplexControl(stmt ast.Stmt) bool {
	complex := false

	ast.Inspect(stmt, func(n ast.Node) bool {
		if complex {
			return false
		}

		switch n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt,
			*ast.SelectStmt, *ast.BranchStmt, *ast.ReturnStmt, *ast.GoStmt, *ast.DeferStmt:
			complex = true
			return false
		}

		return true
	})

	return complex
}
