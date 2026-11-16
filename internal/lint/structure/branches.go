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

const (
	nestedIfPyramidMinDepth = 2
	kindControlFlowMerge    = "control_flow_merge"
)

type branchRules struct {
	q *pkgQuery
}

func (r branchRules) identicalIf(stmt *ast.IfStmt) (Finding, bool) {
	if stmt.Init != nil {
		return Finding{}, false
	}

	elseBlock, ok := stmt.Else.(*ast.BlockStmt)
	if !ok ||
		len(stmt.Body.List) != len(elseBlock.List) ||
		r.q.renderStmtList(stmt.Body.List) != r.q.renderStmtList(elseBlock.List) ||
		!r.q.commentsMatch(stmt.Body, elseBlock) ||
		stmtListDefinesTopLevelNames(stmt.Body.List) ||
		stmtListDefinesTopLevelNames(elseBlock.List) {
		return Finding{}, false
	}

	return Finding{
		Pos:     stmt.If,
		Kind:    kindControlFlowMerge,
		Message: "if and else branches are identical; preserve condition evaluation and hoist shared body",
	}, true
}

func (r branchRules) identicalSwitchCases(stmt *ast.SwitchStmt) []Finding {
	var out []Finding

	var prior *ast.CaseClause

	for _, raw := range stmt.Body.List {
		clause, ok := raw.(*ast.CaseClause)
		if !ok || switchClauseHasFallthrough(clause) {
			prior = nil
			continue
		}

		if prior != nil &&
			r.q.renderStmtList(clause.Body) == r.q.renderStmtList(prior.Body) &&
			r.q.commentsMatch(clause, prior) {
			out = append(out, Finding{
				Pos:  clause.Case,
				Kind: kindControlFlowMerge,
				Message: fmt.Sprintf(
					"switch case %q has identical body as previous case %q; merge case lists",
					r.q.renderCaseClauseHeader(clause),
					r.q.renderCaseClauseHeader(prior),
				),
			})
		}

		prior = clause
	}

	return out
}

// returnGuardRunAt matches a run of guards starting at stmts[idx] that all
// return the same values as the return statement right after the run. The
// int result is the number of guards in the run.
func (r branchRules) returnGuardRunAt(stmts []ast.Stmt, idx int) (Finding, int, bool) {
	first, ok := r.plainReturnGuard(stmts, idx)
	if !ok {
		return Finding{}, 0, false
	}

	if prior, ok := r.plainReturnGuard(stmts, idx-1); ok && prior.key == first.key {
		return Finding{}, 0, false
	}

	guards := 1

	for scan := idx + 1; scan < len(stmts); scan++ {
		if ret, ok := stmts[scan].(*ast.ReturnStmt); ok {
			if r.returnStmtKey(ret) != first.key {
				return Finding{}, 0, false
			}

			result := "return"
			if len(ret.Results) != 0 {
				result += " " + strings.ReplaceAll(first.key, "\x00", ", ")
			}

			return Finding{
				Pos:  first.pos,
				Kind: kindControlFlowMerge,
				Message: fmt.Sprintf(
					`%d guard return(s) duplicate following return %q; drop redundant branch checks`,
					guards,
					result,
				),
			}, guards, true
		}

		guard, ok := r.plainReturnGuard(stmts, scan)
		if !ok || guard.key != first.key {
			return Finding{}, 0, false
		}

		guards++
	}

	return Finding{}, 0, false
}

func (r branchRules) plainReturnGuard(stmts []ast.Stmt, idx int) (returnGuard, bool) {
	if idx < 0 || idx >= len(stmts) {
		return returnGuard{}, false
	}

	stmt, ok := r.q.plainIfStmt(stmts[idx])
	if !ok ||
		!returnGuardConditionSafe(r.q.pkg.TypesInfo, stmt.Cond) ||
		len(stmt.Body.List) != 1 {
		return returnGuard{}, false
	}

	ret, ok := stmt.Body.List[0].(*ast.ReturnStmt)
	if !ok {
		return returnGuard{}, false
	}

	return returnGuard{pos: stmt.If, key: r.returnStmtKey(ret)}, true
}

func (r branchRules) returnStmtKey(ret *ast.ReturnStmt) string {
	parts := make([]string, 0, len(ret.Results))
	for _, result := range ret.Results {
		parts = append(parts, r.q.render(result))
	}

	return strings.Join(parts, "\x00")
}

// finalIfPyramidAt matches a chain of nested ifs that ends a function
// without results, which guard clauses can flatten.
func (r branchRules) finalIfPyramidAt(stmts []ast.Stmt, idx int, ctx blockContext) (Finding, bool) {
	if !ctx.functionBody || ctx.functionHasResults || idx != len(stmts)-1 {
		return Finding{}, false
	}

	ifStmt, ok := stmts[idx].(*ast.IfStmt)
	if !ok {
		return Finding{}, false
	}

	depth := r.ifPyramidDepth(ifStmt)
	if depth < nestedIfPyramidMinDepth {
		return Finding{}, false
	}

	return Finding{
		Pos:  ifStmt.If,
		Kind: "control_flow_pyramid",
		Message: fmt.Sprintf(
			`nested if pyramid has %d levels at function end; invert conditions into guard clauses`,
			depth,
		),
	}, true
}

// ifPyramidDepth counts ifs nested as the only statement of each other. It
// is 0 when any level has init, else, a comment or an empty body.
func (r branchRules) ifPyramidDepth(ifStmt *ast.IfStmt) int {
	depth := 0

	for {
		if ifStmt.Init != nil ||
			ifStmt.Else != nil ||
			r.q.hasAttachedComment(ifStmt) ||
			len(ifStmt.Body.List) == 0 {
			return 0
		}

		depth++

		if len(ifStmt.Body.List) != 1 {
			return depth
		}

		next, ok := ifStmt.Body.List[0].(*ast.IfStmt)
		if !ok {
			return depth
		}

		ifStmt = next
	}
}

func returnGuardConditionSafe(info *types.Info, expr ast.Expr) bool {
	switch expr := ast.Unparen(expr).(type) {
	case *ast.Ident:
		return safeGuardIdent(info, expr)
	case *ast.UnaryExpr:
		return expr.Op == token.NOT && returnGuardConditionSafe(info, expr.X)
	case *ast.BinaryExpr:
		return returnGuardBinaryConditionSafe(info, expr)
	default:
		return false
	}
}

func returnGuardBinaryConditionSafe(info *types.Info, expr *ast.BinaryExpr) bool {
	// Only boolean and comparison operators are safe to drop.
	//exhaustive:ignore
	switch expr.Op {
	case token.LAND, token.LOR:
		return returnGuardConditionSafe(info, expr.X) && returnGuardConditionSafe(info, expr.Y)
	case token.EQL, token.NEQ:
		return safeGuardOperand(info, expr.X) &&
			safeGuardOperand(info, expr.Y) &&
			equalityGuardComparisonSafe(info, expr.X, expr.Y)
	case token.LSS, token.LEQ, token.GTR, token.GEQ:
		return safeGuardOperand(info, expr.X) && safeGuardOperand(info, expr.Y)
	default:
		return false
	}
}

func safeGuardOperand(info *types.Info, expr ast.Expr) bool {
	switch expr := ast.Unparen(expr).(type) {
	case *ast.Ident:
		return safeGuardIdent(info, expr)
	case *ast.BasicLit:
		return true
	case *ast.CallExpr:
		if len(expr.Args) != 1 || builtinName(info, expr) != builtinLenName {
			return false
		}

		_, ok := ast.Unparen(expr.Args[0]).(*ast.Ident)

		return ok
	default:
		value := constValue(info, expr)

		return value != nil && value.Kind() != constant.Unknown
	}
}

func safeGuardIdent(info *types.Info, expr *ast.Ident) bool {
	switch expr.Name {
	case boolTrueText, boolFalseText, nilText:
		return true
	}

	switch info.ObjectOf(expr).(type) {
	case *types.PkgName, *types.Builtin, nil:
		return false
	default:
		return true
	}
}

func equalityGuardComparisonSafe(info *types.Info, left ast.Expr, right ast.Expr) bool {
	if isNilExpr(left) || isNilExpr(right) {
		return true
	}

	return typeStrictlyComparable(info.TypeOf(ast.Unparen(left))) &&
		typeStrictlyComparable(info.TypeOf(ast.Unparen(right)))
}

// Strict comparability avoids equality checks that can panic through interface values.
func typeStrictlyComparable(typ types.Type) bool {
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
