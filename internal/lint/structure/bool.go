package structure

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
)

func (l *Runner) checkRedundantBoolReturn(stmts []ast.Stmt, idx int) {
	stmt, ok := stmts[idx].(*ast.IfStmt)
	if !ok || stmt.Init != nil {
		return
	}

	if stmt.Else != nil {
		l.reportBoolIfElseCeremony(stmt)
		return
	}

	l.reportBoolIfThenReturnCeremony(stmts, idx, stmt)
}

func (l *Runner) reportBoolIfElseCeremony(stmt *ast.IfStmt) bool {
	thenAction, elseAction, ok := l.boolIfElsePair(stmt)
	if !ok {
		return false
	}

	return l.reportBoolActionPair(stmt.If, stmt.Cond, thenAction, elseAction)
}

func (l *Runner) boolIfElsePair(
	stmt *ast.IfStmt,
) (boolBranchAction, boolBranchAction, bool) {
	elseBlock, ok := stmt.Else.(*ast.BlockStmt)
	if !ok {
		return boolBranchAction{}, boolBranchAction{}, false
	}

	thenAction, elseAction, ok := l.boolBranchPair(stmt.Body.List, elseBlock.List)
	if !ok {
		return boolBranchAction{}, boolBranchAction{}, false
	}

	if !l.commentsMatch(stmt.Body.Pos(), stmt.Body.End(), elseBlock.Pos(), elseBlock.End()) {
		return boolBranchAction{}, boolBranchAction{}, false
	}

	return thenAction, elseAction, true
}

func (l *Runner) reportBoolIfThenReturnCeremony(
	stmts []ast.Stmt,
	idx int,
	stmt *ast.IfStmt,
) bool {
	match, ok := l.boolIfThenReturnMatch(stmts, idx, stmt)
	if !ok {
		return false
	}

	return l.reportBoolReturnCeremony(match.pos, match.cond, match.whenTrue, match.whenFalse)
}

func (l *Runner) boolIfThenReturnMatch(
	stmts []ast.Stmt,
	idx int,
	stmt *ast.IfStmt,
) (boolIfThenReturnMatch, bool) {
	if idx+1 >= len(stmts) {
		return boolIfThenReturnMatch{}, false
	}

	thenAction, ok := l.boolBranchAction(stmt.Body.List)
	if !ok || thenAction.kind != boolBranchReturn {
		return boolIfThenReturnMatch{}, false
	}

	nextReturn, ok := stmts[idx+1].(*ast.ReturnStmt)
	if !ok {
		return boolIfThenReturnMatch{}, false
	}

	nextValue, ok := l.singleBoolReturnValue(nextReturn)
	if !ok {
		return boolIfThenReturnMatch{}, false
	}

	if !l.commentsMatch(stmt.Body.Pos(), stmt.Body.End(), nextReturn.Pos(), nextReturn.End()) {
		return boolIfThenReturnMatch{}, false
	}

	return boolIfThenReturnMatch{
		cond:      stmt.Cond,
		whenTrue:  thenAction.value,
		whenFalse: nextValue,
		pos:       stmt.If,
	}, true
}

func (l *Runner) boolBranchPair(
	thenStmts []ast.Stmt,
	elseStmts []ast.Stmt,
) (boolBranchAction, boolBranchAction, bool) {
	thenAction, ok := l.boolBranchAction(thenStmts)
	if !ok {
		return boolBranchAction{}, boolBranchAction{}, false
	}

	elseAction, ok := l.boolBranchAction(elseStmts)
	if !ok || thenAction.kind != elseAction.kind {
		return boolBranchAction{}, boolBranchAction{}, false
	}

	return thenAction, elseAction, true
}

func (l *Runner) reportBoolActionPair(
	pos token.Pos,
	cond ast.Expr,
	thenAction boolBranchAction,
	elseAction boolBranchAction,
) bool {
	switch thenAction.kind {
	case boolBranchInvalid:
		return false
	case boolBranchReturn:
		return l.reportBoolReturnCeremony(pos, cond, thenAction.value, elseAction.value)
	case boolBranchAssign:
		if thenAction.targetID == "" || thenAction.targetID != elseAction.targetID {
			return false
		}

		return l.reportBoolAssignCeremony(
			pos,
			cond,
			thenAction.targetID,
			thenAction.value,
			elseAction.value,
		)
	}

	return false
}

func (l *Runner) reportBoolReturnCeremony(
	pos token.Pos,
	cond ast.Expr,
	whenTrue bool,
	whenFalse bool,
) bool {
	replacement, ok := l.boolReplacementText(cond, whenTrue, whenFalse, "return ")
	if !ok {
		return false
	}

	l.report(
		pos,
		"boolean_ceremony",
		fmt.Sprintf(`if returns boolean literals; replace with %q`, replacement),
	)

	return true
}

func (l *Runner) reportBoolAssignCeremony(
	pos token.Pos,
	cond ast.Expr,
	targetID string,
	whenTrue bool,
	whenFalse bool,
) bool {
	replacement, ok := l.boolReplacementText(cond, whenTrue, whenFalse, targetID+" = ")
	if !ok {
		return false
	}

	l.report(
		pos,
		"boolean_ceremony",
		fmt.Sprintf(`if assigns boolean literals; replace with %q`, replacement),
	)

	return true
}

func (l *Runner) commentsMatch(
	firstStart token.Pos,
	firstEnd token.Pos,
	secondStart token.Pos,
	secondEnd token.Pos,
) bool {
	return sameStrings(
		l.commentTextsInRange(firstStart, firstEnd),
		l.commentTextsInRange(secondStart, secondEnd),
	)
}

func (l *Runner) boolBranchAction(stmts []ast.Stmt) (boolBranchAction, bool) {
	if len(stmts) != 1 {
		return boolBranchAction{}, false
	}

	switch stmt := stmts[0].(type) {
	case *ast.ReturnStmt:
		value, ok := l.singleBoolReturnValue(stmt)
		if !ok {
			return boolBranchAction{}, false
		}

		return boolBranchAction{kind: boolBranchReturn, value: value}, true
	case *ast.AssignStmt:
		if stmt.Tok != token.ASSIGN || len(stmt.Lhs) != 1 || len(stmt.Rhs) != 1 {
			return boolBranchAction{}, false
		}

		if exprHasCalls(stmt.Lhs[0]) {
			return boolBranchAction{}, false
		}

		value, ok := l.boolLiteralValue(stmt.Rhs[0])
		if !ok {
			return boolBranchAction{}, false
		}

		return boolBranchAction{
			kind:     boolBranchAssign,
			value:    value,
			targetID: l.render(stmt.Lhs[0]),
		}, true
	default:
		return boolBranchAction{}, false
	}
}

func (l *Runner) singleBoolReturnValue(stmt *ast.ReturnStmt) (bool, bool) {
	if len(stmt.Results) != 1 {
		return false, false
	}

	return l.boolLiteralValue(stmt.Results[0])
}

func (l *Runner) boolLiteralValue(expr ast.Expr) (bool, bool) {
	value, ok := l.scalarOf(expr)
	if !ok || value.kind != scalarBool {
		return false, false
	}

	return value.text == boolTrueText, true
}

func (l *Runner) boolReplacementText(
	cond ast.Expr,
	whenTrue bool,
	whenFalse bool,
	prefix string,
) (string, bool) {
	switch {
	case whenTrue && !whenFalse:
		return prefix + l.render(cond), true
	case !whenTrue && whenFalse:
		return prefix + "!(" + l.render(cond) + ")", true
	default:
		return "", false
	}
}
func (l *Runner) boolSwitchCoverage(stmt *ast.SwitchStmt) (boolSwitchCoverage, bool) {
	var coverage boolSwitchCoverage

	if !forEachSwitchCase(stmt, func(clause *ast.CaseClause) {
		coverage.defaultClause = clause
	}, func(list []ast.Expr) bool {
		return l.addBoolCaseCoverage(list, &coverage)
	}) {
		return boolSwitchCoverage{}, false
	}

	return coverage, true
}

func (l *Runner) addBoolCaseCoverage(list []ast.Expr, coverage *boolSwitchCoverage) bool {
	for _, expr := range list {
		if !l.addBoolCaseValue(expr, coverage) {
			return false
		}
	}

	return true
}

func (l *Runner) addBoolCaseValue(expr ast.Expr, coverage *boolSwitchCoverage) bool {
	value, ok := l.scalarOf(expr)
	if !ok || value.kind != scalarBool {
		return false
	}

	switch value.text {
	case boolTrueText:
		coverage.coveredTrue = true
	case boolFalseText:
		coverage.coveredFalse = true
	default:
		return false
	}

	return true
}

func (coverage boolSwitchCoverage) exhaustive() bool {
	return coverage.defaultClause != nil && coverage.coveredTrue && coverage.coveredFalse
}
func (l *Runner) checkExhaustiveBoolDefault(stmt *ast.SwitchStmt) bool {
	if stmt.Tag == nil {
		return false
	}

	basic, ok := types.Unalias(l.pkg.TypesInfo.TypeOf(stmt.Tag)).Underlying().(*types.Basic)
	if !ok || basic.Info()&types.IsBoolean == 0 {
		return false
	}

	coverage, ok := l.boolSwitchCoverage(stmt)
	if !ok || !coverage.exhaustive() {
		return false
	}

	if isImpossibleStatePanic(coverage.defaultClause.Body, l.pkg.TypesInfo) {
		return false
	}

	l.report(
		coverage.defaultClause.Case,
		"redundant_default",
		"default case is redundant; bool switch already covers true and false",
	)

	return true
}
