package structure

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"strings"
)

type returnGuard struct {
	pos token.Pos
	key string
}

type returnGuardRun struct {
	pos    token.Pos
	guards int
	result string
}

type nestedIfPyramid struct {
	pos   token.Pos
	depth int
}

const nestedIfPyramidMinDepth = 2

func (l *Runner) checkIdenticalIfBranches(stmt *ast.IfStmt) {
	if !l.identicalIfBranches(stmt) {
		return
	}

	l.report(
		stmt.If,
		"control_flow_merge",
		"if and else branches are identical; preserve condition evaluation and hoist shared body",
	)
}

func (l *Runner) identicalIfBranches(stmt *ast.IfStmt) bool {
	if stmt.Init != nil {
		return false
	}

	elseBlock, ok := stmt.Else.(*ast.BlockStmt)
	if !ok {
		return false
	}

	if !stmtListsEqual(stmt.Body.List, elseBlock.List, l.renderStmtList) {
		return false
	}

	if !sameStrings(
		l.commentTextsInRange(stmt.Body.Pos(), stmt.Body.End()),
		l.commentTextsInRange(elseBlock.Pos(), elseBlock.End()),
	) {
		return false
	}

	return !stmtListDefinesTopLevelNames(stmt.Body.List) &&
		!stmtListDefinesTopLevelNames(elseBlock.List)
}

func (l *Runner) checkIdenticalSwitchBranches(stmt *ast.SwitchStmt) {
	if stmt.Body == nil {
		return
	}

	var prior *switchBranchShape

	for _, raw := range stmt.Body.List {
		clause, ok := raw.(*ast.CaseClause)
		if !ok || clause == nil {
			prior = nil
			continue
		}

		if switchClauseHasFallthrough(clause) {
			prior = nil
			continue
		}

		shape := switchBranchShape{
			clause:   clause,
			key:      l.renderStmtList(clause.Body),
			comments: l.commentTextsInRange(clause.Case, clause.End()),
		}

		if prior != nil &&
			shape.key == prior.key &&
			sameStrings(shape.comments, prior.comments) {
			l.report(
				clause.Case,
				"control_flow_merge",
				fmt.Sprintf(
					"switch case %q has identical body as previous case %q; merge case lists",
					l.renderCaseClauseHeader(clause),
					l.renderCaseClauseHeader(prior.clause),
				),
			)
		}

		prior = &shape
	}
}

func (l *Runner) checkRedundantReturnGuardRun(stmts []ast.Stmt, idx int) {
	match, ok := l.redundantReturnGuardRun(stmts, idx)
	if !ok {
		return
	}

	l.report(
		match.pos,
		"control_flow_merge",
		fmt.Sprintf(
			`%d guard return(s) duplicate following return %q; drop redundant branch checks`,
			match.guards,
			match.result,
		),
	)
}

func (l *Runner) redundantReturnGuardRun(stmts []ast.Stmt, idx int) (returnGuardRun, bool) {
	first, ok := l.plainReturnGuard(stmts, idx)
	if !ok || l.priorReturnGuardHasSameResult(stmts, idx, first.key) {
		return returnGuardRun{}, false
	}

	guards := 1

	for scan := idx + 1; scan < len(stmts); scan++ {
		if ret, ok := stmts[scan].(*ast.ReturnStmt); ok {
			if l.returnStmtKey(ret) != first.key {
				return returnGuardRun{}, false
			}

			return returnGuardRun{
				pos:    first.pos,
				guards: guards,
				result: l.renderReturnResults(ret),
			}, true
		}

		guard, ok := l.plainReturnGuard(stmts, scan)
		if !ok || guard.key != first.key {
			return returnGuardRun{}, false
		}

		guards++
	}

	return returnGuardRun{}, false
}

func (l *Runner) plainReturnGuard(stmts []ast.Stmt, idx int) (returnGuard, bool) {
	if idx < 0 || idx >= len(stmts) {
		return returnGuard{}, false
	}

	stmt, ok := stmts[idx].(*ast.IfStmt)
	if !ok ||
		stmt.Init != nil ||
		stmt.Else != nil ||
		l.hasAttachedComment(stmt) ||
		!l.returnGuardConditionSafe(stmt.Cond) ||
		len(stmt.Body.List) != 1 {
		return returnGuard{}, false
	}

	ret, ok := stmt.Body.List[0].(*ast.ReturnStmt)
	if !ok {
		return returnGuard{}, false
	}

	return returnGuard{
		pos: stmt.If,
		key: l.returnStmtKey(ret),
	}, true
}

func (l *Runner) priorReturnGuardHasSameResult(stmts []ast.Stmt, idx int, key string) bool {
	prior, ok := l.plainReturnGuard(stmts, idx-1)

	return ok && prior.key == key
}

func (l *Runner) returnStmtKey(ret *ast.ReturnStmt) string {
	if ret == nil {
		return ""
	}

	parts := make([]string, 0, len(ret.Results))
	for _, result := range ret.Results {
		parts = append(parts, l.render(result))
	}

	return strings.Join(parts, "\x00")
}

func (l *Runner) renderReturnResults(ret *ast.ReturnStmt) string {
	if ret == nil || len(ret.Results) == 0 {
		return "return"
	}

	return "return " + strings.Join(strings.Split(l.returnStmtKey(ret), "\x00"), ", ")
}

func (l *Runner) returnGuardConditionSafe(expr ast.Expr) bool {
	expr = l.unparen(expr)

	switch expr := expr.(type) {
	case *ast.Ident:
		return l.safeGuardIdent(expr)
	case *ast.UnaryExpr:
		return expr.Op == token.NOT && l.returnGuardConditionSafe(expr.X)
	case *ast.BinaryExpr:
		return l.returnGuardBinaryConditionSafe(expr)
	default:
		return false
	}
}

func (l *Runner) returnGuardBinaryConditionSafe(expr *ast.BinaryExpr) bool {
	//exhaustive:ignore only boolean and comparison operators are safe to drop.
	switch expr.Op {
	case token.LAND, token.LOR:
		return l.returnGuardConditionSafe(expr.X) && l.returnGuardConditionSafe(expr.Y)
	case token.EQL, token.NEQ:
		return l.safeGuardOperand(expr.X) &&
			l.safeGuardOperand(expr.Y) &&
			l.equalityGuardComparisonSafe(expr.X, expr.Y)
	case token.LSS, token.LEQ, token.GTR, token.GEQ:
		return l.safeGuardOperand(expr.X) && l.safeGuardOperand(expr.Y)
	default:
		return false
	}
}

func (l *Runner) safeGuardOperand(expr ast.Expr) bool {
	expr = l.unparen(expr)

	switch expr := expr.(type) {
	case *ast.Ident:
		return l.safeGuardIdent(expr)
	case *ast.BasicLit:
		return true
	case *ast.CallExpr:
		return l.safeLenCall(expr)
	default:
		return l.safeConstantOperand(expr)
	}
}

func (l *Runner) safeGuardIdent(expr *ast.Ident) bool {
	if expr == nil {
		return false
	}

	switch expr.Name {
	case boolTrueText, boolFalseText, nilText:
		return true
	}

	obj := l.pkg.TypesInfo.ObjectOf(expr)
	if obj == nil {
		return false
	}

	_, isPkg := obj.(*types.PkgName)
	_, isBuiltin := obj.(*types.Builtin)

	return !isPkg && !isBuiltin
}

func (l *Runner) safeLenCall(expr *ast.CallExpr) bool {
	if expr == nil || len(expr.Args) != 1 || !l.isBuiltinCall(expr, builtinLenName) {
		return false
	}

	_, ok := l.unparen(expr.Args[0]).(*ast.Ident)

	return ok
}

func (l *Runner) safeConstantOperand(expr ast.Expr) bool {
	tv, ok := l.pkg.TypesInfo.Types[l.unparen(expr)]

	return ok && tv.Value != nil && tv.Value.Kind() != constant.Unknown
}

func (l *Runner) equalityGuardComparisonSafe(left ast.Expr, right ast.Expr) bool {
	if l.isNilExpr(left) || l.isNilExpr(right) {
		return true
	}

	return l.strictlyComparableExpr(left) && l.strictlyComparableExpr(right)
}

func (l *Runner) isNilExpr(expr ast.Expr) bool {
	id, ok := l.unparen(expr).(*ast.Ident)

	return ok && id.Name == nilText
}

func (l *Runner) strictlyComparableExpr(expr ast.Expr) bool {
	return typeStrictlyComparable(l.pkg.TypesInfo.TypeOf(l.unparen(expr)))
}

// Strict comparability avoids equality checks that can panic through interface values.
func typeStrictlyComparable(typ types.Type) bool {
	if typ == nil {
		return false
	}

	switch typ := types.Unalias(typ).Underlying().(type) {
	case *types.Basic:
		return typ.Kind() != types.Invalid && typ.Kind() != types.UntypedNil
	case *types.Pointer, *types.Chan:
		return true
	case *types.Array:
		return typeStrictlyComparable(typ.Elem())
	case *types.Struct:
		for field := range typ.Fields() {
			if !typeStrictlyComparable(field.Type()) {
				return false
			}
		}

		return true
	default:
		return false
	}
}

func (l *Runner) checkNestedFinalIfPyramid(stmts []ast.Stmt, idx int, ctx blockContext) {
	if !ctx.functionBody || ctx.functionHasResults || idx != len(stmts)-1 {
		return
	}

	match, ok := l.nestedFinalIfPyramid(stmts[idx])
	if !ok {
		return
	}

	l.report(
		match.pos,
		"control_flow_pyramid",
		fmt.Sprintf(
			`nested if pyramid has %d levels at function end; invert conditions into guard clauses`,
			match.depth,
		),
	)
}

func (l *Runner) nestedFinalIfPyramid(stmt ast.Stmt) (nestedIfPyramid, bool) {
	ifStmt, ok := stmt.(*ast.IfStmt)
	if !ok {
		return nestedIfPyramid{}, false
	}

	depth := 0
	pos := ifStmt.If

	for {
		if ifStmt.Init != nil ||
			ifStmt.Else != nil ||
			l.hasAttachedComment(ifStmt) ||
			len(ifStmt.Body.List) == 0 {
			return nestedIfPyramid{}, false
		}

		depth++

		if len(ifStmt.Body.List) != 1 {
			break
		}

		next, ok := ifStmt.Body.List[0].(*ast.IfStmt)
		if !ok {
			break
		}

		ifStmt = next
	}

	if depth < nestedIfPyramidMinDepth {
		return nestedIfPyramid{}, false
	}

	return nestedIfPyramid{pos: pos, depth: depth}, true
}

func forEachSwitchCase(
	stmt *ast.SwitchStmt,
	defaultCase func(*ast.CaseClause),
	nonDefaultCase func([]ast.Expr) bool,
) bool {
	for _, raw := range stmt.Body.List {
		clause, ok := raw.(*ast.CaseClause)
		if !ok || clause == nil {
			continue
		}

		if len(clause.List) == 0 {
			defaultCase(clause)
			continue
		}

		if !nonDefaultCase(clause.List) {
			return false
		}
	}

	return true
}

func (l *Runner) renderStmtList(stmts []ast.Stmt) string {
	if len(stmts) == 0 {
		return "{}"
	}

	parts := make([]string, 0, len(stmts))
	for _, stmt := range stmts {
		parts = append(parts, l.render(stmt))
	}

	return strings.Join(parts, "\n")
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}

	for idx := range left {
		if left[idx] != right[idx] {
			return false
		}
	}

	return true
}

func stmtListsEqual(left, right []ast.Stmt, render func([]ast.Stmt) string) bool {
	if len(left) != len(right) {
		return false
	}

	return render(left) == render(right)
}

func stmtListDefinesTopLevelNames(stmts []ast.Stmt) bool {
	for _, stmt := range stmts {
		switch stmt := stmt.(type) {
		case *ast.AssignStmt:
			if stmt.Tok == token.DEFINE {
				return true
			}
		case *ast.DeclStmt:
			return true
		}
	}

	return false
}

func switchClauseHasFallthrough(clause *ast.CaseClause) bool {
	if len(clause.Body) == 0 {
		return false
	}

	branch, ok := clause.Body[len(clause.Body)-1].(*ast.BranchStmt)

	return ok && branch.Tok == token.FALLTHROUGH
}

func isImpossibleStatePanic(body []ast.Stmt, info *types.Info) bool {
	if len(body) != 1 {
		return false
	}

	exprStmt, ok := body[0].(*ast.ExprStmt)
	if !ok {
		return false
	}

	call, ok := exprStmt.X.(*ast.CallExpr)
	if !ok {
		return false
	}

	id, ok := call.Fun.(*ast.Ident)
	if !ok {
		return false
	}

	obj, ok := info.ObjectOf(id).(*types.Builtin)

	return ok && obj != nil && obj.Name() == panicText
}

func exprHasCalls(expr ast.Expr) bool {
	hasCall := false

	ast.Inspect(expr, func(n ast.Node) bool {
		if hasCall {
			return false
		}

		switch n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.CallExpr:
			hasCall = true
			return false
		}

		return true
	})

	return hasCall
}
