package structure

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
)

const (
	complexitySimplificationMinScore = 3
	scatteredGuardMinTotal           = 3
	scatteredGuardMinLate            = 2
)

// simplificationScore counts behavior-preserving simplification points over
// the whole body, with the same checks the scan reports one by one.
func (l *Runner) simplificationScore(body *ast.BlockStmt, ctx blockContext) (Finding, bool) {
	score := 0

	stmtWalker{
		atIndex: func(stmts []ast.Stmt, idx int, ctx blockContext) {
			for _, finding := range l.blockFindingsAt(stmts, idx, ctx) {
				score += finding.points
			}
		},
		atStmt: func(stmt ast.Stmt) {
			if ifStmt, ok := stmt.(*ast.IfStmt); ok {
				if _, ok := (branchRules{l.q}).identicalIf(ifStmt); ok {
					score++
				}
			}
		},
	}.walkList(body.List, ctx)

	if score < complexitySimplificationMinScore {
		return Finding{}, false
	}

	return Finding{
		Pos:  body.Lbrace,
		Kind: "complexity_simplification",
		Message: fmt.Sprintf(
			`function has %d behavior-preserving simplification points; remove redundant branches/guards before adding more control flow`,
			score,
		),
	}, true
}

// inputGuardScan checks whether a function's input guards are grouped.
// temps holds the function inputs plus locals computed only from them.
type inputGuardScan struct {
	q       *pkgQuery
	effects *validationEffects
	temps   map[types.Object]struct{}
	results []types.Type
}

func (s inputGuardScan) scattered(body *ast.BlockStmt) (Finding, bool) {
	seenWork := false
	total, late := 0, 0
	pos := token.NoPos

	for _, stmt := range body.List {
		if guard, ok := s.inputReturnGuard(stmt); ok {
			total++

			if seenWork {
				late++

				if pos == token.NoPos {
					pos = guard.If
				}
			}

			continue
		}

		if s.isInputPrepStmt(stmt) {
			s.addPrepObjects(stmt)
			continue
		}

		if s.prepFailureGuard(stmt) {
			continue
		}

		seenWork = seenWork || s.effects.statementEffect(stmt, body) == validationWork
	}

	if total < scatteredGuardMinTotal || late < scatteredGuardMinLate {
		return Finding{}, false
	}

	return Finding{
		Pos:  pos,
		Kind: "guard_complexity",
		Message: fmt.Sprintf(
			`%d of %d input guard returns are interleaved with other work; keep validation guards together or split function`,
			late,
			total,
		),
	}, true
}

func (s inputGuardScan) failureGuard(stmt ast.Stmt) (*ast.IfStmt, bool) {
	ifStmt, ok := s.q.plainIfStmt(stmt)
	if !ok || len(ifStmt.Body.List) != 1 {
		return nil, false
	}

	ret, ok := ifStmt.Body.List[0].(*ast.ReturnStmt)
	if !ok || !s.failureReturn(ret) {
		return nil, false
	}

	return ifStmt, true
}

func (s inputGuardScan) inputReturnGuard(stmt ast.Stmt) (*ast.IfStmt, bool) {
	ifStmt, ok := s.failureGuard(stmt)
	if !ok ||
		!s.validationExpr(ifStmt.Cond) ||
		!nodeUsesAnyObject(s.q.pkg.TypesInfo, ifStmt.Cond, s.temps) {
		return nil, false
	}

	return ifStmt, true
}

func (s inputGuardScan) failureReturn(stmt *ast.ReturnStmt) bool {
	if len(stmt.Results) == 0 {
		return true
	}

	// Type-checked code has one result per result type, or one call that
	// returns them all, so index stays in range.
	for index, expr := range stmt.Results {
		if returnResultMayBeFailure(s.q.pkg.TypesInfo, expr, s.results[index]) {
			return true
		}
	}

	return false
}

func returnResultMayBeFailure(info *types.Info, expr ast.Expr, resultType types.Type) bool {
	if isNilExpr(expr) || !typeCanCarryFailure(resultType) {
		return false
	}

	exprType := info.TypeOf(expr)

	return types.AssignableTo(exprType, resultType) || typeCanCarryFailure(exprType)
}

func typeCanCarryFailure(typ types.Type) bool {
	if typeImplementsError(typ) {
		return true
	}

	if ptr, ok := types.Unalias(typ).Underlying().(*types.Pointer); ok {
		return typeImplementsError(ptr)
	}

	return false
}

func typeImplementsError(typ types.Type) bool {
	errType, ok := types.Universe.Lookup("error").Type().Underlying().(*types.Interface)

	return ok && types.Implements(typ, errType)
}

// validationExpr reports whether expr only reads inputs, validation temps,
// constants and pure calls.
func (s inputGuardScan) validationExpr(expr ast.Expr) bool {
	expr = ast.Unparen(expr)

	switch expr := expr.(type) {
	case *ast.Ident:
		return s.validationIdent(expr)
	case *ast.BasicLit:
		return true
	case *ast.SelectorExpr:
		return s.validationSelector(expr)
	case *ast.CallExpr:
		return s.validationCall(expr)
	case *ast.UnaryExpr:
		return expr.Op == token.NOT && s.validationExpr(expr.X)
	case *ast.BinaryExpr:
		// Only boolean and comparison operators form validation guards.
		//exhaustive:ignore
		switch expr.Op {
		case token.LAND, token.LOR,
			token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ:
			return s.validationExpr(expr.X) && s.validationExpr(expr.Y)
		default:
			return false
		}
	default:
		return isNilOrScalarConst(s.q.pkg.TypesInfo, expr)
	}
}

func (s inputGuardScan) validationIdent(ident *ast.Ident) bool {
	switch ident.Name {
	case boolTrueText, boolFalseText, nilText:
		return true
	case "err", "error":
		return false
	}

	obj := s.q.pkg.TypesInfo.ObjectOf(ident)
	if _, ok := s.temps[obj]; ok {
		return true
	}

	_, ok := obj.(*types.Const)

	return ok
}

func (s inputGuardScan) validationSelector(expr *ast.SelectorExpr) bool {
	info := s.q.pkg.TypesInfo

	if _, ok := info.ObjectOf(expr.Sel).(*types.Const); ok {
		return true
	}

	selection := info.Selections[expr]
	if selection == nil || selection.Kind() != types.FieldVal {
		return false
	}

	return s.validationExpr(expr.X)
}

func (s inputGuardScan) validationCall(call *ast.CallExpr) bool {
	if s.effects.callEffect(call) != validationPure {
		return false
	}

	if selector, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr); ok {
		if selection := s.q.pkg.TypesInfo.Selections[selector]; selection != nil &&
			selection.Kind() == types.MethodVal &&
			!s.validationExpr(selector.X) {
			return false
		}
	}

	for _, arg := range call.Args {
		if !s.validationExpr(arg) {
			return false
		}
	}

	return true
}

// prepFailureGuard matches a guard that rejects a failed prep step, such as
// `if err != nil` or `if !ok` on a validation temp.
func (s inputGuardScan) prepFailureGuard(stmt ast.Stmt) bool {
	ifStmt, ok := s.failureGuard(stmt)
	if !ok {
		return false
	}

	switch expr := ast.Unparen(ifStmt.Cond).(type) {
	case *ast.UnaryExpr:
		return expr.Op == token.NOT && s.prepBoolIdent(expr.X)
	case *ast.BinaryExpr:
		return s.prepFailureBinary(expr)
	default:
		return false
	}
}

func (s inputGuardScan) prepFailureBinary(expr *ast.BinaryExpr) bool {
	if expr.Op != token.NEQ && expr.Op != token.EQL {
		return false
	}

	if s.prepErrorIdent(expr.X) && isNilExpr(expr.Y) ||
		s.prepErrorIdent(expr.Y) && isNilExpr(expr.X) {
		return expr.Op == token.NEQ
	}

	if s.prepBoolIdent(expr.X) && isFalseIdent(expr.Y) ||
		s.prepBoolIdent(expr.Y) && isFalseIdent(expr.X) {
		return expr.Op == token.EQL
	}

	return false
}

func (s inputGuardScan) prepErrorIdent(expr ast.Expr) bool {
	typ, ok := s.prepIdentType(expr)

	return ok && typeImplementsError(typ)
}

func (s inputGuardScan) prepBoolIdent(expr ast.Expr) bool {
	typ, ok := s.prepIdentType(expr)
	if !ok {
		return false
	}

	basic, ok := types.Unalias(typ).Underlying().(*types.Basic)

	return ok && basic.Kind() == types.Bool
}

func (s inputGuardScan) prepIdentType(expr ast.Expr) (types.Type, bool) {
	ident, ok := ast.Unparen(expr).(*ast.Ident)
	if !ok {
		return nil, false
	}

	info := s.q.pkg.TypesInfo
	if _, ok := s.temps[info.ObjectOf(ident)]; !ok {
		return nil, false
	}

	return info.TypeOf(ident), true
}

func isFalseIdent(expr ast.Expr) bool {
	ident, ok := ast.Unparen(expr).(*ast.Ident)

	return ok && ident.Name == boolFalseText
}

func (s inputGuardScan) deferredCleanup(stmt ast.Stmt) bool {
	switch stmt := stmt.(type) {
	case *ast.DeferStmt:
		// The body runs on return; only its receiver and arguments run before guards.
		for _, arg := range stmt.Call.Args {
			if !s.validationExpr(arg) {
				return false
			}
		}

		switch fun := ast.Unparen(stmt.Call.Fun).(type) {
		case *ast.FuncLit, *ast.Ident:
			return true
		case *ast.SelectorExpr:
			return s.validationExpr(fun.X)
		default:
			return false
		}
	case *ast.IfStmt:
		_, plain := s.q.plainIfStmt(stmt)
		if !plain || len(stmt.Body.List) != 1 || !s.validationExpr(stmt.Cond) {
			return false
		}

		_, deferred := stmt.Body.List[0].(*ast.DeferStmt)

		return deferred && s.deferredCleanup(stmt.Body.List[0])
	}

	return false
}

func (s inputGuardScan) isInputPrepStmt(stmt ast.Stmt) bool {
	if s.deferredCleanup(stmt) {
		return true
	}

	assign, ok := stmt.(*ast.AssignStmt)
	if !ok || len(assign.Rhs) == 0 {
		return false
	}

	info := s.q.pkg.TypesInfo

	for _, lhs := range assign.Lhs {
		ident, ok := ast.Unparen(lhs).(*ast.Ident)
		if !ok {
			return false
		}

		if obj := info.ObjectOf(ident); obj != nil && obj.Parent() == s.q.pkg.TypesPkg.Scope() {
			return false
		}
	}

	for _, rhs := range assign.Rhs {
		if !s.validationExpr(rhs) || !nodeUsesAnyObject(info, rhs, s.temps) {
			return false
		}
	}

	return true
}

func (s inputGuardScan) addPrepObjects(stmt ast.Stmt) {
	assign, ok := stmt.(*ast.AssignStmt)
	if !ok {
		return
	}

	for _, lhs := range assign.Lhs {
		ident, ok := ast.Unparen(lhs).(*ast.Ident)
		if !ok || ident.Name == "_" {
			continue
		}

		s.temps[s.q.pkg.TypesInfo.ObjectOf(ident)] = struct{}{}
	}
}
