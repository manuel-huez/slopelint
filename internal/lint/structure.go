package lint

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"strings"
)

type boolBranchKind uint8

const (
	boolBranchInvalid boolBranchKind = iota
	boolBranchReturn
	boolBranchAssign
)

type boolBranchAction struct {
	kind     boolBranchKind
	value    bool
	targetID string
}

type switchBranchShape struct {
	clause   *ast.CaseClause
	key      string
	comments []string
}

type boolSwitchCoverage struct {
	defaultClause *ast.CaseClause
	coveredTrue   bool
	coveredFalse  bool
}

func (l *linter) scanStructuralBlock(stmts []ast.Stmt) {
	for idx, stmt := range stmts {
		l.checkRedundantBoolReturn(stmts, idx)
		l.scanStructuralStmt(stmt)
	}
}

func (l *linter) scanStructuralStmt(stmt ast.Stmt) {
	switch stmt := stmt.(type) {
	case *ast.BlockStmt:
		l.scanStructuralBlock(stmt.List)
	case *ast.IfStmt:
		l.scanStructuralIfStmt(stmt)
	case *ast.ForStmt:
		l.scanStructuralBlock(stmt.Body.List)
	case *ast.RangeStmt:
		l.scanStructuralBlock(stmt.Body.List)
	case *ast.SwitchStmt:
		l.scanStructuralSwitchStmt(stmt)
	case *ast.TypeSwitchStmt:
		l.scanCaseClauseBodies(stmt.Body.List)
	case *ast.SelectStmt:
		l.scanCommClauseBodies(stmt.Body.List)
	case *ast.LabeledStmt:
		l.scanStructuralStmt(stmt.Stmt)
	}
}

func (l *linter) scanStructuralIfStmt(stmt *ast.IfStmt) {
	l.checkIdenticalIfBranches(stmt)
	l.scanStructuralBlock(stmt.Body.List)

	if stmt.Else != nil {
		l.scanStructuralStmt(stmt.Else)
	}
}

func (l *linter) scanStructuralSwitchStmt(stmt *ast.SwitchStmt) {
	l.checkIdenticalSwitchBranches(stmt)
	l.checkExhaustiveDefensiveDefault(stmt)
	l.scanCaseClauseBodies(stmt.Body.List)
}

func (l *linter) scanCaseClauseBodies(list []ast.Stmt) {
	for _, raw := range list {
		clause, ok := raw.(*ast.CaseClause)
		if !ok {
			continue
		}

		l.scanStructuralBlock(clause.Body)
	}
}

func (l *linter) scanCommClauseBodies(list []ast.Stmt) {
	for _, raw := range list {
		clause, ok := raw.(*ast.CommClause)
		if !ok {
			continue
		}

		l.scanStructuralBlock(clause.Body)
	}
}

func (l *linter) checkRedundantBoolReturn(stmts []ast.Stmt, idx int) {
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

func (l *linter) reportBoolIfElseCeremony(stmt *ast.IfStmt) bool {
	elseBlock, ok := stmt.Else.(*ast.BlockStmt)
	if !ok {
		return false
	}

	thenAction, elseAction, ok := l.boolBranchPair(stmt.Body.List, elseBlock.List)
	if !ok {
		return false
	}

	if !l.commentsMatch(stmt.Body.Pos(), stmt.Body.End(), elseBlock.Pos(), elseBlock.End()) {
		return false
	}

	return l.reportBoolActionPair(stmt.If, stmt.Cond, thenAction, elseAction)
}

func (l *linter) reportBoolIfThenReturnCeremony(
	stmts []ast.Stmt,
	idx int,
	stmt *ast.IfStmt,
) bool {
	if idx+1 >= len(stmts) {
		return false
	}

	thenAction, ok := l.boolBranchAction(stmt.Body.List)
	if !ok || thenAction.kind != boolBranchReturn {
		return false
	}

	nextReturn, ok := stmts[idx+1].(*ast.ReturnStmt)
	if !ok {
		return false
	}

	nextValue, ok := l.singleBoolReturnValue(nextReturn)
	if !ok {
		return false
	}

	if !l.commentsMatch(stmt.Body.Pos(), stmt.Body.End(), nextReturn.Pos(), nextReturn.End()) {
		return false
	}

	return l.reportBoolReturnCeremony(stmt.If, stmt.Cond, thenAction.value, nextValue)
}

func (l *linter) boolBranchPair(
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

func (l *linter) reportBoolActionPair(
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
	default:
		return false
	}
}

func (l *linter) reportBoolReturnCeremony(
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

func (l *linter) reportBoolAssignCeremony(
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

func (l *linter) commentsMatch(
	firstStart token.Pos,
	firstEnd token.Pos,
	secondStart token.Pos,
	secondEnd token.Pos,
) bool {
	return sameCommentTexts(
		l.commentTextsInRange(firstStart, firstEnd),
		l.commentTextsInRange(secondStart, secondEnd),
	)
}

func (l *linter) boolBranchAction(stmts []ast.Stmt) (boolBranchAction, bool) {
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

func (l *linter) singleBoolReturnValue(stmt *ast.ReturnStmt) (bool, bool) {
	if len(stmt.Results) != 1 {
		return false, false
	}

	return l.boolLiteralValue(stmt.Results[0])
}

func (l *linter) boolLiteralValue(expr ast.Expr) (bool, bool) {
	value, ok := l.scalarOf(expr)
	if !ok || value.kind != scalarBool {
		return false, false
	}

	return value.text == boolTrueText, true
}

func (l *linter) boolReplacementText(
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

func (l *linter) checkIdenticalIfBranches(stmt *ast.IfStmt) {
	if stmt.Init != nil {
		return
	}

	elseBlock, ok := stmt.Else.(*ast.BlockStmt)
	if !ok {
		return
	}

	if !stmtListsEqual(stmt.Body.List, elseBlock.List, l.renderStmtList) {
		return
	}

	if !sameCommentTexts(
		l.commentTextsInRange(stmt.Body.Pos(), stmt.Body.End()),
		l.commentTextsInRange(elseBlock.Pos(), elseBlock.End()),
	) {
		return
	}

	if stmtListDefinesTopLevelNames(stmt.Body.List) ||
		stmtListDefinesTopLevelNames(elseBlock.List) {
		return
	}

	l.report(
		stmt.If,
		"control_flow_merge",
		"if and else branches are identical; drop condition or hoist shared body",
	)
}

func (l *linter) checkIdenticalSwitchBranches(stmt *ast.SwitchStmt) {
	if stmt.Body == nil {
		return
	}

	seen := make(map[string][]switchBranchShape)

	for _, raw := range stmt.Body.List {
		clause, ok := raw.(*ast.CaseClause)
		if !ok || clause == nil {
			continue
		}

		if switchClauseHasFallthrough(clause) {
			continue
		}

		key := l.renderStmtList(clause.Body)
		shape := switchBranchShape{
			clause:   clause,
			key:      key,
			comments: l.commentTextsInRange(clause.Case, clause.End()),
		}

		for _, prior := range seen[key] {
			if !sameCommentTexts(shape.comments, prior.comments) {
				continue
			}

			l.report(
				clause.Case,
				"control_flow_merge",
				fmt.Sprintf(
					"switch case %q duplicates case %q; merge case lists",
					l.renderCaseClauseHeader(clause),
					l.renderCaseClauseHeader(prior.clause),
				),
			)

			goto nextClause
		}

		seen[key] = append(seen[key], shape)

	nextClause:
	}
}

func (l *linter) checkExhaustiveDefensiveDefault(stmt *ast.SwitchStmt) {
	if stmt.Tag == nil || !isBoolType(l.pkg.TypesInfo.TypeOf(stmt.Tag)) {
		return
	}

	coverage, ok := l.boolSwitchCoverage(stmt)
	if !ok || !coverage.exhaustive() {
		return
	}

	if isImpossibleStatePanic(coverage.defaultClause.Body, l.pkg.TypesInfo) {
		return
	}

	l.report(
		coverage.defaultClause.Case,
		"redundant_default",
		"default case is redundant; bool switch already covers true and false",
	)
}

func (l *linter) boolSwitchCoverage(stmt *ast.SwitchStmt) (boolSwitchCoverage, bool) {
	var coverage boolSwitchCoverage

	for _, raw := range stmt.Body.List {
		clause, ok := raw.(*ast.CaseClause)
		if !ok || clause == nil {
			continue
		}

		if len(clause.List) == 0 {
			coverage.defaultClause = clause
			continue
		}

		if !l.addBoolCaseCoverage(clause.List, &coverage) {
			return boolSwitchCoverage{}, false
		}
	}

	return coverage, true
}

func (l *linter) addBoolCaseCoverage(list []ast.Expr, coverage *boolSwitchCoverage) bool {
	for _, expr := range list {
		if !l.addBoolCaseValue(expr, coverage) {
			return false
		}
	}

	return true
}

func (l *linter) addBoolCaseValue(expr ast.Expr, coverage *boolSwitchCoverage) bool {
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

func (l *linter) renderStmtList(stmts []ast.Stmt) string {
	if len(stmts) == 0 {
		return "{}"
	}

	parts := make([]string, 0, len(stmts))
	for _, stmt := range stmts {
		parts = append(parts, l.render(stmt))
	}

	return strings.Join(parts, "\n")
}

func (l *linter) commentTextsInRange(start, end token.Pos) []string {
	if start == token.NoPos || end == token.NoPos {
		return nil
	}

	file := l.pkg.FSet.File(start)
	if file == nil {
		return nil
	}

	out := make([]string, 0)

	for _, astFile := range l.pkg.Files {
		for _, group := range astFile.Comments {
			if l.pkg.FSet.File(group.Pos()) != file {
				continue
			}

			if group.Pos() < start || group.End() > end {
				continue
			}

			text := normalizeCommentText(group.Text())
			if text == "" {
				continue
			}

			out = append(out, text)
		}
	}

	return out
}

func normalizeCommentText(text string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
}

func sameCommentTexts(left, right []string) bool {
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

	return ok && obj != nil && obj.Name() == "panic"
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
