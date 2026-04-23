package lint

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"slices"
	"strings"
	"unicode"
)

type boolBranchKind uint8

const (
	boolBranchInvalid boolBranchKind = iota
	boolBranchReturn
	boolBranchAssign
)

const (
	ancestorStackCap  = 8
	identifierWordCap = 4
	rangeLoopMaxStmts = 3
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

type tempAliasDecl struct {
	name *ast.Ident
	obj  types.Object
	rhs  ast.Expr
}

type rangeLoopShape struct {
	key string
	pos token.Pos
}

type appendLenGuardMatch struct {
	pos    token.Pos
	source string
}

type objectUseSummary struct {
	reads  int
	unsafe bool
}

func (l *linter) scanStructuralBlock(stmts []ast.Stmt) {
	for idx, stmt := range stmts {
		l.checkRedundantBoolReturn(stmts, idx)
		l.checkSingleUseTempAlias(stmts, idx)
		l.checkRedundantAppendLenGuard(stmt)
		l.checkDuplicateAdjacentRangeLoop(stmts, idx)
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
	}

	return false
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
			sameCommentTexts(shape.comments, prior.comments) {
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

func (l *linter) checkExhaustiveDefensiveDefault(stmt *ast.SwitchStmt) {
	if stmt.Tag == nil {
		return
	}

	if l.checkExhaustiveBoolDefault(stmt) {
		return
	}

	l.checkExhaustiveConstSetDefault(stmt)
}

func (l *linter) checkExhaustiveBoolDefault(stmt *ast.SwitchStmt) bool {
	if !isBoolType(l.pkg.TypesInfo.TypeOf(stmt.Tag)) {
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

func (l *linter) checkSingleUseTempAlias(stmts []ast.Stmt, idx int) {
	if idx+1 >= len(stmts) {
		return
	}

	decl, ok := l.tempAliasDeclFromStmt(stmts[idx])
	if !ok || l.hasAttachedComment(stmts[idx]) {
		return
	}

	if !tempAliasNameLooksDisposable(decl.name.Name, decl.rhs) {
		return
	}

	nextUse := l.objectUseSummary(stmts[idx+1], decl.obj)
	if nextUse.unsafe || nextUse.reads != 1 {
		return
	}

	for _, later := range stmts[idx+2:] {
		if nodeUsesObject(later, decl.obj, l.pkg.TypesInfo) {
			return
		}
	}

	l.report(
		decl.name.Pos(),
		"temp_alias",
		fmt.Sprintf(
			`local %q only renames cheap expression %q for one use; inline expression`,
			decl.name.Name,
			l.render(decl.rhs),
		),
	)
}

func (l *linter) checkRedundantAppendLenGuard(stmt ast.Stmt) {
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

func (l *linter) redundantAppendLenGuard(stmt ast.Stmt) (appendLenGuardMatch, bool) {
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

func (l *linter) plainIfStmt(stmt ast.Stmt) (*ast.IfStmt, bool) {
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

func (l *linter) assignAppendsGuardedSlice(assign *ast.AssignStmt, source string) bool {
	call, ok := l.appendAssignCall(assign)
	if !ok {
		return false
	}

	if exprHasCalls(assign.Lhs[0]) || exprHasCalls(call.Args[0]) || exprHasCalls(call.Args[1]) {
		return false
	}

	return l.render(assign.Lhs[0]) == l.render(call.Args[0]) && l.render(call.Args[1]) == source
}

func (l *linter) appendAssignCall(assign *ast.AssignStmt) (*ast.CallExpr, bool) {
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

func (l *linter) nonEmptyLenGuardSource(expr ast.Expr) (string, bool) {
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

func (l *linter) lenCompareOperand(lenExpr ast.Expr, limitExpr ast.Expr) (string, int64, bool) {
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

func (l *linter) isBuiltinCall(call *ast.CallExpr, name string) bool {
	id, ok := l.unparen(call.Fun).(*ast.Ident)
	if !ok || id.Name != name {
		return false
	}

	obj, ok := l.pkg.TypesInfo.ObjectOf(id).(*types.Builtin)

	return ok && obj != nil && obj.Name() == name
}

func (l *linter) checkDuplicateAdjacentRangeLoop(stmts []ast.Stmt, idx int) {
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

func (l *linter) rangeLoopShape(stmt ast.Stmt) (rangeLoopShape, bool) {
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

func (l *linter) rangeLoopIdent(expr ast.Expr) (string, types.Object) {
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

func (l *linter) tempAliasDeclFromStmt(stmt ast.Stmt) (tempAliasDecl, bool) {
	assign, ok := stmt.(*ast.AssignStmt)
	if ok {
		return l.tempAliasDeclFromAssign(assign)
	}

	decl, ok := stmt.(*ast.DeclStmt)
	if ok {
		return l.tempAliasDeclFromDecl(decl)
	}

	return tempAliasDecl{}, false
}

func (l *linter) tempAliasDeclFromAssign(stmt *ast.AssignStmt) (tempAliasDecl, bool) {
	if stmt.Tok != token.DEFINE || len(stmt.Lhs) != 1 || len(stmt.Rhs) != 1 {
		return tempAliasDecl{}, false
	}

	name, ok := stmt.Lhs[0].(*ast.Ident)
	if !ok {
		return tempAliasDecl{}, false
	}

	return l.tempAliasDecl(name, stmt.Rhs[0])
}

func (l *linter) tempAliasDeclFromDecl(stmt *ast.DeclStmt) (tempAliasDecl, bool) {
	decl, ok := stmt.Decl.(*ast.GenDecl)
	if !ok || decl.Tok != token.VAR || len(decl.Specs) != 1 {
		return tempAliasDecl{}, false
	}

	spec, ok := decl.Specs[0].(*ast.ValueSpec)
	if !ok || spec.Type != nil || len(spec.Names) != 1 || len(spec.Values) != 1 {
		return tempAliasDecl{}, false
	}

	return l.tempAliasDecl(spec.Names[0], spec.Values[0])
}

func (l *linter) tempAliasDecl(name *ast.Ident, rhs ast.Expr) (tempAliasDecl, bool) {
	if name == nil || name.Name == "_" {
		return tempAliasDecl{}, false
	}

	obj, ok := l.pkg.TypesInfo.ObjectOf(name).(*types.Var)
	if !ok || obj == nil || obj.IsField() {
		return tempAliasDecl{}, false
	}

	rhs = l.unparen(rhs)
	if !l.isCheapTempAliasExpr(rhs) {
		return tempAliasDecl{}, false
	}

	return tempAliasDecl{name: name, obj: obj, rhs: rhs}, true
}

func (l *linter) isCheapTempAliasExpr(expr ast.Expr) bool {
	expr = l.unparen(expr)

	switch expr := expr.(type) {
	case *ast.Ident:
		if expr.Name == "_" {
			return false
		}

		obj := l.pkg.TypesInfo.ObjectOf(expr)
		if obj == nil {
			return false
		}

		if _, ok := obj.(*types.PkgName); ok {
			return false
		}

		_, isFn := types.Unalias(l.pkg.TypesInfo.TypeOf(expr)).Underlying().(*types.Signature)

		return !isFn
	case *ast.SelectorExpr:
		sel := l.pkg.TypesInfo.Selections[expr]
		if sel == nil || sel.Kind() != types.FieldVal {
			return false
		}

		_, isFn := types.Unalias(l.pkg.TypesInfo.TypeOf(expr)).Underlying().(*types.Signature)

		return !isFn && l.isCheapTempAliasExpr(expr.X)
	default:
		return false
	}
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

func (l *linter) hasAttachedComment(node ast.Node) bool {
	if node == nil {
		return false
	}

	file := l.pkg.FSet.File(node.Pos())
	if file == nil {
		return false
	}

	start := l.pkg.FSet.Position(node.Pos())
	end := l.pkg.FSet.Position(node.End())

	for _, astFile := range l.pkg.Files {
		for _, group := range astFile.Comments {
			if l.pkg.FSet.File(group.Pos()) != file {
				continue
			}

			groupStart := l.pkg.FSet.Position(group.Pos())
			groupEnd := l.pkg.FSet.Position(group.End())

			if groupStart.Line >= start.Line && groupStart.Line <= end.Line {
				return true
			}

			if groupStart.Line == end.Line {
				return true
			}

			if groupEnd.Line == start.Line-1 {
				return true
			}
		}
	}

	return false
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

func (l *linter) objectUseSummary(node ast.Node, target types.Object) objectUseSummary {
	var out objectUseSummary

	inspectWithAncestors(node, func(n ast.Node, ancestors []ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			if nodeUsesObject(n.Body, target, l.pkg.TypesInfo) {
				out.unsafe = true
			}

			return false
		case *ast.Ident:
			if l.pkg.TypesInfo.ObjectOf(n) != target {
				return !out.unsafe
			}

			if objectUseIsUnsafe(n, ancestors) {
				out.unsafe = true
				return false
			}

			out.reads++
		}

		return !out.unsafe
	})

	return out
}

func objectUseIsUnsafe(ident *ast.Ident, ancestors []ast.Node) bool {
	for i := len(ancestors) - 1; i >= 0; i-- {
		switch node := ancestors[i].(type) {
		case *ast.UnaryExpr:
			if node.Op == token.AND && nodeContains(node.X, ident) {
				return true
			}
		case *ast.IncDecStmt:
			return true
		case *ast.AssignStmt:
			if nodeContainedInExprList(ident, node.Lhs) {
				return true
			}
		case *ast.RangeStmt:
			if nodeContains(node.Key, ident) || nodeContains(node.Value, ident) {
				return true
			}
		}
	}

	return false
}

func inspectWithAncestors(node ast.Node, fn func(ast.Node, []ast.Node) bool) {
	ancestors := make([]ast.Node, 0, ancestorStackCap)

	ast.Inspect(node, func(n ast.Node) bool {
		if n == nil {
			ancestors = ancestors[:len(ancestors)-1]
			return false
		}

		keep := fn(n, ancestors)
		if keep {
			ancestors = append(ancestors, n)
		}

		return keep
	})
}

func nodeUsesObject(node ast.Node, target types.Object, info *types.Info) bool {
	found := false

	ast.Inspect(node, func(n ast.Node) bool {
		if found {
			return false
		}

		ident, ok := n.(*ast.Ident)
		if !ok {
			return true
		}

		found = info.ObjectOf(ident) == target

		return !found
	})

	return found
}

func nodeContainedInExprList(target ast.Node, exprs []ast.Expr) bool {
	for _, expr := range exprs {
		if nodeContains(expr, target) {
			return true
		}
	}

	return false
}

func nodeContains(root, target ast.Node) bool {
	if root == nil || target == nil {
		return false
	}

	found := false

	ast.Inspect(root, func(n ast.Node) bool {
		if found || n == nil {
			return !found
		}

		if n == target {
			found = true
			return false
		}

		return true
	})

	return found
}

func tempAliasNameLooksDisposable(name string, rhs ast.Expr) bool {
	leaf, ok := tempAliasLeafName(rhs)
	if !ok {
		return false
	}

	nameWords := stripAliasNoise(splitIdentifierWords(name))
	leafWords := stripAliasNoise(splitIdentifierWords(leaf))

	return len(nameWords) != 0 && sameWords(nameWords, leafWords)
}

func tempAliasLeafName(expr ast.Expr) (string, bool) {
	switch expr := expr.(type) {
	case *ast.Ident:
		return expr.Name, true
	case *ast.SelectorExpr:
		return expr.Sel.Name, true
	default:
		return "", false
	}
}

func splitIdentifierWords(name string) []string {
	if name == "" {
		return nil
	}

	runes := []rune(name)
	start := 0
	words := make([]string, 0, identifierWordCap)

	for idx := 1; idx < len(runes); idx++ {
		nextStart, split := nextIdentifierWordStart(runes, idx)
		if split {
			appendIdentifierWord(runes, start, idx, &words)
			start = nextStart
		}
	}

	appendIdentifierWord(runes, start, len(runes), &words)

	return words
}

func nextIdentifierWordStart(runes []rune, idx int) (int, bool) {
	prev := runes[idx-1]
	curr := runes[idx]

	switch {
	case curr == '_':
		return idx + 1, true
	case prev == '_',
		unicode.IsLower(prev) && unicode.IsUpper(curr),
		unicode.IsUpper(prev) && unicode.IsUpper(curr) &&
			idx+1 < len(runes) && unicode.IsLower(runes[idx+1]):
		return idx, true
	default:
		return 0, false
	}
}

func appendIdentifierWord(runes []rune, start, end int, words *[]string) {
	if end <= start {
		return
	}

	word := strings.ToLower(string(runes[start:end]))
	if word == "" {
		return
	}

	*words = append(*words, word)
}

func stripAliasNoise(words []string) []string {
	if len(words) == 0 {
		return nil
	}

	out := make([]string, 0, len(words))
	for _, word := range words {
		switch word {
		case "alias", "copy", "temp", "tmp", "val", "value":
			continue
		default:
			out = append(out, word)
		}
	}

	return out
}

func sameWords(left, right []string) bool {
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

func normalizeRenderedIdentifier(text string, name string, replacement string) string {
	if name == "" {
		return text
	}

	var out strings.Builder

	runes := []rune(text)

	for idx := 0; idx < len(runes); {
		ch := runes[idx]
		if !isIdentifierStart(ch) {
			out.WriteRune(ch)

			idx++

			continue
		}

		start := idx

		idx++

		for idx < len(runes) && isIdentifierPart(runes[idx]) {
			idx++
		}

		token := string(runes[start:idx])
		if token == name {
			out.WriteString(replacement)
			continue
		}

		out.WriteString(token)
	}

	return out.String()
}

func isIdentifierStart(ch rune) bool {
	return ch == '_' || unicode.IsLetter(ch)
}

func isIdentifierPart(ch rune) bool {
	return isIdentifierStart(ch) || unicode.IsDigit(ch)
}
