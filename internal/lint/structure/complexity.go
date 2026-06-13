package structure

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"strings"
)

const (
	complexitySimplificationMinScore = 3
	scatteredGuardMinTotal           = 3
	scatteredGuardMinLate            = 2
)

type scatteredGuardSummary struct {
	pos   token.Pos
	total int
	late  int
}

func copyObjectSet(in map[types.Object]struct{}) map[types.Object]struct{} {
	out := make(map[types.Object]struct{}, len(in))
	for obj := range in {
		out[obj] = struct{}{}
	}

	return out
}

func (l *Runner) checkFunctionComplexity(body *ast.BlockStmt, ctx blockContext) {
	if body == nil {
		return
	}

	l.checkBehaviorPreservingComplexity(body.List, ctx, body.Lbrace)
	l.checkScatteredInputGuards(body, ctx)
}

func (l *Runner) checkBehaviorPreservingComplexity(
	stmts []ast.Stmt,
	ctx blockContext,
	pos token.Pos,
) {
	score := l.simplificationScoreForBlock(stmts, ctx)
	if score < complexitySimplificationMinScore {
		return
	}

	l.report(
		pos,
		"complexity_simplification",
		fmt.Sprintf(
			`function has %d behavior-preserving simplification points; remove redundant branches/guards before adding more control flow`,
			score,
		),
	)
}

func (l *Runner) simplificationScoreForBlock(stmts []ast.Stmt, ctx blockContext) int {
	score := 0

	for idx, stmt := range stmts {
		if run, ok := l.redundantReturnGuardRun(stmts, idx); ok {
			score += run.guards
		}

		if l.boolIfCeremony(stmts, idx) {
			score++
		}

		if _, ok := l.singleUseTempAlias(stmts, idx); ok {
			score++
		}

		if _, ok := l.redundantAppendLenGuard(stmt); ok {
			score++
		}

		if _, ok := l.redundantRangeGuard(stmt); ok {
			score++
		}

		if _, ok := l.emptyRangeReturnGuard(stmts, idx, ctx); ok {
			score++
		}

		if _, ok := l.nestedFinalIfPyramidAt(stmts, idx, ctx); ok {
			score++
		}

		if _, ok := l.duplicateAdjacentRangeLoop(stmts, idx); ok {
			score++
		}

		score += l.simplificationScoreForStmt(stmt)
	}

	return score
}

func (l *Runner) simplificationScoreForStmt(stmt ast.Stmt) int {
	switch stmt := stmt.(type) {
	case *ast.BlockStmt:
		return l.simplificationScoreForBody(stmt)
	case *ast.IfStmt:
		return l.simplificationScoreForIf(stmt)
	case *ast.ForStmt:
		return l.simplificationScoreForBody(stmt.Body)
	case *ast.RangeStmt:
		return l.simplificationScoreForBody(stmt.Body)
	case *ast.SwitchStmt:
		return l.simplificationScoreForCaseBody(stmt.Body)
	case *ast.TypeSwitchStmt:
		return l.simplificationScoreForCaseBody(stmt.Body)
	case *ast.SelectStmt:
		return l.simplificationScoreForCommBody(stmt.Body)
	case *ast.LabeledStmt:
		return l.simplificationScoreForStmt(stmt.Stmt)
	default:
		return 0
	}
}

func (l *Runner) simplificationScoreForBody(body *ast.BlockStmt) int {
	if body == nil {
		return 0
	}

	return l.simplificationScoreForBlock(body.List, blockContext{})
}

func (l *Runner) simplificationScoreForIf(stmt *ast.IfStmt) int {
	score := 0
	if l.identicalIfBranches(stmt) {
		score++
	}

	score += l.simplificationScoreForBody(stmt.Body)
	if stmt.Else != nil {
		score += l.simplificationScoreForStmt(stmt.Else)
	}

	return score
}

func (l *Runner) simplificationScoreForCaseBody(body *ast.BlockStmt) int {
	if body == nil {
		return 0
	}

	return l.simplificationScoreForCaseClauses(body.List)
}

func (l *Runner) simplificationScoreForCommBody(body *ast.BlockStmt) int {
	if body == nil {
		return 0
	}

	return l.simplificationScoreForCommClauses(body.List)
}

func (l *Runner) simplificationScoreForCaseClauses(stmts []ast.Stmt) int {
	score := 0

	for _, raw := range stmts {
		clause, ok := raw.(*ast.CaseClause)
		if !ok || clause == nil {
			continue
		}

		score += l.simplificationScoreForBlock(clause.Body, blockContext{})
	}

	return score
}

func (l *Runner) simplificationScoreForCommClauses(stmts []ast.Stmt) int {
	score := 0

	for _, raw := range stmts {
		clause, ok := raw.(*ast.CommClause)
		if !ok || clause == nil {
			continue
		}

		score += l.simplificationScoreForBlock(clause.Body, blockContext{})
	}

	return score
}

func (l *Runner) boolIfCeremony(stmts []ast.Stmt, idx int) bool {
	stmt, ok := stmts[idx].(*ast.IfStmt)
	if !ok || stmt.Init != nil {
		return false
	}

	if stmt.Else != nil {
		thenAction, elseAction, ok := l.boolIfElsePair(stmt)
		if !ok {
			return false
		}

		return l.boolActionPairSimplifiable(stmt.Cond, thenAction, elseAction)
	}

	match, ok := l.boolIfThenReturnMatch(stmts, idx, stmt)
	if !ok {
		return false
	}

	_, ok = l.boolReplacementText(match.cond, match.whenTrue, match.whenFalse, "return ")

	return ok
}

func (l *Runner) boolActionPairSimplifiable(
	cond ast.Expr,
	thenAction boolBranchAction,
	elseAction boolBranchAction,
) bool {
	switch thenAction.kind {
	case boolBranchInvalid:
		return false
	case boolBranchReturn:
		_, ok := l.boolReplacementText(cond, thenAction.value, elseAction.value, "return ")

		return ok
	case boolBranchAssign:
		if thenAction.targetID == "" || thenAction.targetID != elseAction.targetID {
			return false
		}

		_, ok := l.boolReplacementText(
			cond,
			thenAction.value,
			elseAction.value,
			thenAction.targetID+" = ",
		)

		return ok
	}

	return false
}

func (l *Runner) nestedFinalIfPyramidAt(
	stmts []ast.Stmt,
	idx int,
	ctx blockContext,
) (nestedIfPyramid, bool) {
	if !ctx.functionBody || ctx.functionHasResults || idx != len(stmts)-1 {
		return nestedIfPyramid{}, false
	}

	return l.nestedFinalIfPyramid(stmts[idx])
}

func (l *Runner) checkScatteredInputGuards(body *ast.BlockStmt, ctx blockContext) {
	summary, ok := l.scatteredInputGuardReturns(body, ctx)
	if !ok {
		return
	}

	l.report(
		summary.pos,
		"guard_complexity",
		fmt.Sprintf(
			`%d of %d input guard returns are interleaved with other work; keep validation guards together or split function`,
			summary.late,
			summary.total,
		),
	)
}

func (l *Runner) scatteredInputGuardReturns(
	body *ast.BlockStmt,
	ctx blockContext,
) (scatteredGuardSummary, bool) {
	seenWork := false
	validationTemps := copyObjectSet(ctx.functionInputs)

	var summary scatteredGuardSummary

	for _, stmt := range body.List {
		guard, ok := l.inputReturnGuard(stmt, body.Lbrace, validationTemps, ctx.functionResults)
		if ok {
			summary.total++
			if seenWork {
				summary.late++
				if summary.pos == token.NoPos {
					summary.pos = guard.If
				}
			}

			continue
		}

		if l.inputValidationPrepStmt(stmt, body.Lbrace, validationTemps) {
			l.addValidationPrepObjects(validationTemps, stmt)
			continue
		}

		if l.validationPrepFailureGuard(stmt, validationTemps, ctx.functionResults) {
			continue
		}

		seenWork = true
	}

	if summary.total < scatteredGuardMinTotal || summary.late < scatteredGuardMinLate {
		return scatteredGuardSummary{}, false
	}

	return summary, true
}

func (l *Runner) inputReturnGuard(
	stmt ast.Stmt,
	bodyStart token.Pos,
	validationTemps map[types.Object]struct{},
	resultTypes []types.Type,
) (*ast.IfStmt, bool) {
	ifStmt, ok := l.plainIfStmt(stmt)
	if !ok || len(ifStmt.Body.List) != 1 {
		return nil, false
	}

	ret, ok := ifStmt.Body.List[0].(*ast.ReturnStmt)
	if !ok || !l.validationFailureReturn(ret, resultTypes) {
		return nil, false
	}

	if !l.inputValidationExpr(ifStmt.Cond, bodyStart, validationTemps) {
		return nil, false
	}

	if !l.inputValidationReferencesTrackedObject(ifStmt.Cond, validationTemps) {
		return nil, false
	}

	return ifStmt, true
}

func (l *Runner) validationFailureReturn(
	stmt *ast.ReturnStmt,
	resultTypes []types.Type,
) bool {
	if len(stmt.Results) == 0 {
		return true
	}

	for index, expr := range stmt.Results {
		if l.returnResultMayBeFailure(expr, resultTypeAt(resultTypes, index)) {
			return true
		}
	}

	return false
}

func resultTypeAt(resultTypes []types.Type, index int) types.Type {
	if index < 0 || index >= len(resultTypes) {
		return nil
	}

	return resultTypes[index]
}

func (l *Runner) returnResultMayBeFailure(expr ast.Expr, resultType types.Type) bool {
	if l.isNilExpr(expr) || !typeCanCarryFailure(resultType) {
		return false
	}

	exprType := l.pkg.TypesInfo.TypeOf(expr)
	if exprType == nil {
		return false
	}

	return types.AssignableTo(exprType, resultType) || typeCanCarryFailure(exprType)
}

func typeCanCarryFailure(typ types.Type) bool {
	if typ == nil {
		return false
	}

	if typeImplementsError(typ) {
		return true
	}

	if ptr, ok := types.Unalias(typ).Underlying().(*types.Pointer); ok {
		return typeImplementsError(ptr)
	}

	return false
}

func typeImplementsError(typ types.Type) bool {
	if typ == nil {
		return false
	}

	errObj := types.Universe.Lookup("error")
	errType, ok := errObj.Type().Underlying().(*types.Interface)

	return ok && types.Implements(typ, errType)
}

func (l *Runner) inputValidationExpr(
	expr ast.Expr,
	bodyStart token.Pos,
	validationTemps map[types.Object]struct{},
) bool {
	expr = l.unparen(expr)

	switch expr := expr.(type) {
	case *ast.Ident:
		return l.inputValidationIdent(expr, bodyStart, validationTemps)
	case *ast.BasicLit:
		return true
	case *ast.SelectorExpr:
		return l.inputValidationSelector(expr, bodyStart, validationTemps)
	case *ast.CallExpr:
		return l.inputValidationCall(expr, bodyStart, validationTemps)
	case *ast.UnaryExpr:
		return expr.Op == token.NOT && l.inputValidationExpr(
			expr.X,
			bodyStart,
			validationTemps,
		)
	case *ast.BinaryExpr:
		return l.inputValidationBinary(expr, bodyStart, validationTemps)
	default:
		value, ok := l.scalarOf(expr)

		return ok && value.kind != 0
	}
}

func (l *Runner) inputValidationReferencesTrackedObject(
	expr ast.Expr,
	validationTemps map[types.Object]struct{},
) bool {
	if expr == nil || len(validationTemps) == 0 {
		return false
	}

	found := false

	ast.Inspect(expr, func(n ast.Node) bool {
		if found {
			return false
		}

		ident, ok := n.(*ast.Ident)
		if !ok {
			return true
		}

		_, found = validationTemps[l.pkg.TypesInfo.ObjectOf(ident)]

		return !found
	})

	return found
}

func (l *Runner) inputValidationCall(
	call *ast.CallExpr,
	bodyStart token.Pos,
	validationTemps map[types.Object]struct{},
) bool {
	if call == nil {
		return false
	}

	if l.inputValidationLenCall(call, bodyStart, validationTemps) {
		return true
	}

	return l.inputValidationMethodCall(call, bodyStart, validationTemps)
}

func (l *Runner) inputValidationLenCall(
	call *ast.CallExpr,
	bodyStart token.Pos,
	validationTemps map[types.Object]struct{},
) bool {
	if len(call.Args) != 1 {
		return false
	}

	ident, ok := l.unparen(call.Fun).(*ast.Ident)
	if !ok {
		return false
	}

	builtin, ok := l.pkg.TypesInfo.ObjectOf(ident).(*types.Builtin)
	if !ok || builtin.Name() != builtinLenName {
		return false
	}

	return l.inputValidationExpr(call.Args[0], bodyStart, validationTemps)
}

func (l *Runner) inputValidationMethodCall(
	call *ast.CallExpr,
	bodyStart token.Pos,
	validationTemps map[types.Object]struct{},
) bool {
	if len(call.Args) != 0 {
		return false
	}

	selector, ok := l.unparen(call.Fun).(*ast.SelectorExpr)
	if !ok || (selector.Sel.Name != "IsZero" && !strings.HasPrefix(selector.Sel.Name, "Is")) {
		return false
	}

	selection := l.pkg.TypesInfo.Selections[selector]
	if selection == nil || selection.Kind() != types.MethodVal {
		return false
	}

	return l.inputValidationExpr(selector.X, bodyStart, validationTemps)
}

func (l *Runner) inputValidationBinary(
	expr *ast.BinaryExpr,
	bodyStart token.Pos,
	validationTemps map[types.Object]struct{},
) bool {
	//exhaustive:ignore only boolean and comparison operators form validation guards.
	switch expr.Op {
	case token.LAND, token.LOR,
		token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ:
		return l.inputValidationExpr(expr.X, bodyStart, validationTemps) &&
			l.inputValidationExpr(expr.Y, bodyStart, validationTemps)
	default:
		return false
	}
}

func (l *Runner) inputValidationIdent(
	ident *ast.Ident,
	bodyStart token.Pos,
	validationTemps map[types.Object]struct{},
) bool {
	if ident == nil {
		return false
	}

	obj := l.pkg.TypesInfo.ObjectOf(ident)

	switch ident.Name {
	case boolTrueText, boolFalseText, nilText:
		return true
	case "err", "error":
		return false
	}

	if _, ok := validationTemps[obj]; ok {
		return true
	}

	_, ok := obj.(*types.Const)

	return ok
}

func (l *Runner) inputValidationSelector(
	expr *ast.SelectorExpr,
	bodyStart token.Pos,
	validationTemps map[types.Object]struct{},
) bool {
	if expr == nil {
		return false
	}

	if obj, ok := l.pkg.TypesInfo.ObjectOf(expr.Sel).(*types.Const); ok && obj != nil {
		return true
	}

	selection := l.pkg.TypesInfo.Selections[expr]
	if selection == nil || selection.Kind() != types.FieldVal {
		return false
	}

	return l.inputValidationExpr(expr.X, bodyStart, validationTemps)
}

func (l *Runner) validationPrepFailureGuard(
	stmt ast.Stmt,
	validationTemps map[types.Object]struct{},
	resultTypes []types.Type,
) bool {
	if len(validationTemps) == 0 {
		return false
	}

	ifStmt, ok := l.plainIfStmt(stmt)
	if !ok || len(ifStmt.Body.List) != 1 {
		return false
	}

	ret, ok := ifStmt.Body.List[0].(*ast.ReturnStmt)
	if !ok || !l.validationFailureReturn(ret, resultTypes) {
		return false
	}

	return l.validationPrepFailureExpr(ifStmt.Cond, validationTemps)
}

func (l *Runner) validationPrepFailureExpr(
	expr ast.Expr,
	validationTemps map[types.Object]struct{},
) bool {
	expr = l.unparen(expr)

	switch expr := expr.(type) {
	case *ast.UnaryExpr:
		return expr.Op == token.NOT &&
			l.validationPrepBoolIdent(expr.X, validationTemps)
	case *ast.BinaryExpr:
		return l.validationPrepFailureBinary(expr, validationTemps)
	default:
		return false
	}
}

func (l *Runner) validationPrepFailureBinary(
	expr *ast.BinaryExpr,
	validationTemps map[types.Object]struct{},
) bool {
	if expr.Op != token.NEQ && expr.Op != token.EQL {
		return false
	}

	if l.validationPrepErrorIdent(expr.X, validationTemps) &&
		l.isNilIdent(expr.Y) {
		return expr.Op == token.NEQ
	}

	if l.validationPrepErrorIdent(expr.Y, validationTemps) &&
		l.isNilIdent(expr.X) {
		return expr.Op == token.NEQ
	}

	if l.validationPrepBoolIdent(expr.X, validationTemps) &&
		l.isBoolLiteral(expr.Y, false) {
		return expr.Op == token.EQL
	}

	if l.validationPrepBoolIdent(expr.Y, validationTemps) &&
		l.isBoolLiteral(expr.X, false) {
		return expr.Op == token.EQL
	}

	return false
}

func (l *Runner) validationPrepErrorIdent(
	expr ast.Expr,
	validationTemps map[types.Object]struct{},
) bool {
	typ, ok := l.validationPrepIdentType(expr, validationTemps)
	if !ok {
		return false
	}

	return typeImplementsError(typ)
}

func (l *Runner) validationPrepBoolIdent(
	expr ast.Expr,
	validationTemps map[types.Object]struct{},
) bool {
	typ, ok := l.validationPrepIdentType(expr, validationTemps)
	if !ok {
		return false
	}

	basic, ok := types.Unalias(typ).Underlying().(*types.Basic)

	return ok && basic.Kind() == types.Bool
}

func (l *Runner) validationPrepIdentType(
	expr ast.Expr,
	validationTemps map[types.Object]struct{},
) (types.Type, bool) {
	ident, ok := l.unparen(expr).(*ast.Ident)
	if !ok {
		return nil, false
	}

	obj := l.pkg.TypesInfo.ObjectOf(ident)
	if _, ok := validationTemps[obj]; !ok {
		return nil, false
	}

	return l.pkg.TypesInfo.TypeOf(ident), true
}

func (l *Runner) isNilIdent(expr ast.Expr) bool {
	ident, ok := l.unparen(expr).(*ast.Ident)

	return ok && ident.Name == nilText
}

func (l *Runner) isBoolLiteral(expr ast.Expr, want bool) bool {
	ident, ok := l.unparen(expr).(*ast.Ident)
	if !ok {
		return false
	}

	if want {
		return ident.Name == boolTrueText
	}

	return ident.Name == boolFalseText
}

func (l *Runner) inputValidationPrepStmt(
	stmt ast.Stmt,
	bodyStart token.Pos,
	validationTemps map[types.Object]struct{},
) bool {
	assign, ok := stmt.(*ast.AssignStmt)
	if !ok || len(assign.Rhs) == 0 {
		return false
	}

	for _, rhs := range assign.Rhs {
		if !l.inputValidationPrepExpr(rhs, bodyStart, validationTemps) {
			return false
		}

		if !l.inputValidationReferencesTrackedObject(rhs, validationTemps) {
			return false
		}
	}

	return true
}

func (l *Runner) inputValidationPrepExpr(
	expr ast.Expr,
	bodyStart token.Pos,
	validationTemps map[types.Object]struct{},
) bool {
	expr = l.unparen(expr)

	if l.inputValidationExpr(expr, bodyStart, validationTemps) {
		return true
	}

	call, ok := expr.(*ast.CallExpr)
	if !ok || !l.inputValidationPrepCall(call, bodyStart, validationTemps) {
		return false
	}

	for _, arg := range call.Args {
		if !l.inputValidationPrepExpr(arg, bodyStart, validationTemps) {
			return false
		}
	}

	return true
}

func (l *Runner) inputValidationPrepCall(
	call *ast.CallExpr,
	bodyStart token.Pos,
	validationTemps map[types.Object]struct{},
) bool {
	if call == nil {
		return false
	}

	switch fun := l.unparen(call.Fun).(type) {
	case *ast.Ident:
		return inputValidationPrepFuncName(fun.Name)
	case *ast.SelectorExpr:
		if !inputValidationPrepFuncName(fun.Sel.Name) {
			return false
		}

		if selection := l.pkg.TypesInfo.Selections[fun]; selection != nil {
			return l.inputValidationExpr(fun.X, bodyStart, validationTemps)
		}

		return true
	default:
		return false
	}
}

func (l *Runner) addValidationPrepObjects(
	validationTemps map[types.Object]struct{},
	stmt ast.Stmt,
) {
	assign, ok := stmt.(*ast.AssignStmt)
	if !ok {
		return
	}

	for _, lhs := range assign.Lhs {
		ident, ok := l.unparen(lhs).(*ast.Ident)
		if !ok || ident.Name == "_" {
			continue
		}

		if obj := l.pkg.TypesInfo.ObjectOf(ident); obj != nil {
			validationTemps[obj] = struct{}{}
		}
	}
}

func inputValidationPrepFuncName(name string) bool {
	lower := strings.ToLower(name)

	return strings.HasPrefix(lower, "normalize") ||
		strings.HasPrefix(lower, "parse") ||
		strings.HasPrefix(lower, "key") ||
		strings.HasPrefix(name, "New") ||
		strings.HasPrefix(name, "Trim") ||
		strings.HasPrefix(name, "To")
}
