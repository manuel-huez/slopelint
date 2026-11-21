package structure

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"

	"github.com/manuel-huez/slopelint/internal/lint/astutil"
)

const builtinLenName = "len"

type rangeGuard struct {
	source string
	hasLen bool
	hasNil bool
}

type guardShape struct {
	mergeOp   token.Token
	provesLen func(token.Token, int64) bool
	nilOp     token.Token
}

var (
	nonEmptyGuard = guardShape{token.LAND, lenCompareProvesNonEmpty, token.NEQ}
	emptyGuard    = guardShape{token.LOR, lenCompareProvesEmpty, token.EQL}
)

type collectionRules struct {
	q *pkgQuery
}

func (r collectionRules) appendLenGuard(stmt ast.Stmt) (Finding, bool) {
	ifStmt, ok := r.q.plainIfStmt(stmt)
	if !ok {
		return Finding{}, false
	}

	source, ok := r.lenGuardSource(ifStmt.Cond, lenCompareProvesNonEmpty)
	if !ok || len(ifStmt.Body.List) != 1 {
		return Finding{}, false
	}

	assign, ok := ifStmt.Body.List[0].(*ast.AssignStmt)
	if !ok || !r.assignAppendsGuardedSlice(assign, source) {
		return Finding{}, false
	}

	return Finding{
		Pos:  ifStmt.If,
		Kind: "append_ceremony",
		Message: fmt.Sprintf(
			`len guard before append(%s...) is redundant; append no-ops for empty variadic slices`,
			source,
		),
	}, true
}

func (r collectionRules) rangeGuard(stmt ast.Stmt) (Finding, bool) {
	ifStmt, ok := r.q.plainIfStmt(stmt)
	if !ok {
		return Finding{}, false
	}

	loop, ok := singleRangeStmt(ifStmt.Body.List)
	if !ok {
		return Finding{}, false
	}

	guard, ok := r.guardBeforeRange(ifStmt, loop, nonEmptyGuard)
	if !ok {
		return Finding{}, false
	}

	return Finding{
		Pos:  ifStmt.If,
		Kind: "range_ceremony",
		Message: fmt.Sprintf(
			`%s guard before range over %s is redundant; range has no iterations when guarded value is empty`,
			guard.label(),
			guard.source,
		),
	}, true
}

func (r collectionRules) emptyRangeReturnGuard(
	stmts []ast.Stmt,
	idx int,
	ctx blockContext,
) (Finding, bool) {
	if !ctx.functionBody || ctx.functionHasResults || idx+1 != len(stmts)-1 {
		return Finding{}, false
	}

	ifStmt, ok := r.q.plainIfStmt(stmts[idx])
	if !ok || !singleNakedReturn(ifStmt.Body.List) {
		return Finding{}, false
	}

	loop, ok := singleRangeStmt(stmts[idx+1:])
	if !ok {
		return Finding{}, false
	}

	guard, ok := r.guardBeforeRange(ifStmt, loop, emptyGuard)
	if !ok {
		return Finding{}, false
	}

	return Finding{
		Pos:  ifStmt.If,
		Kind: "range_ceremony",
		Message: fmt.Sprintf(
			`%s return guard before final range over %s is redundant; empty range already ends function`,
			guard.label(),
			guard.source,
		),
	}, true
}

// guardBeforeRange matches an if condition that only tests whether the range
// source is empty, so the range alone gives the same behavior.
func (r collectionRules) guardBeforeRange(
	ifStmt *ast.IfStmt,
	loop *ast.RangeStmt,
	shape guardShape,
) (rangeGuard, bool) {
	info := r.q.pkg.TypesInfo

	sourceExpr := ast.Unparen(loop.X)
	if !isVarOrFieldRead(info, sourceExpr) {
		return rangeGuard{}, false
	}

	guard, ok := r.collectionGuardSource(ifStmt.Cond, shape)
	if !ok ||
		guard.source != r.q.render(sourceExpr) ||
		guard.hasLen && !rangeNoopsWhenEmpty(info, loop.X) ||
		guard.hasNil && !rangeNoopsWhenNil(info, loop.X) {
		return rangeGuard{}, false
	}

	return guard, true
}

func singleRangeStmt(stmts []ast.Stmt) (*ast.RangeStmt, bool) {
	if len(stmts) != 1 {
		return nil, false
	}

	loop, ok := stmts[0].(*ast.RangeStmt)

	return loop, ok
}

func singleNakedReturn(stmts []ast.Stmt) bool {
	if len(stmts) != 1 {
		return false
	}

	ret, ok := stmts[0].(*ast.ReturnStmt)

	return ok && len(ret.Results) == 0
}

// assignAppendsGuardedSlice matches `x = append(x, source...)`.
func (r collectionRules) assignAppendsGuardedSlice(assign *ast.AssignStmt, source string) bool {
	if assign.Tok != token.ASSIGN || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
		return false
	}

	call, ok := ast.Unparen(assign.Rhs[0]).(*ast.CallExpr)
	if !ok || call.Ellipsis == token.NoPos || len(call.Args) != 2 ||
		builtinName(r.q.pkg.TypesInfo, call) != "append" {
		return false
	}

	if astutil.HasCall(assign.Lhs[0]) || astutil.HasCall(call.Args[0]) ||
		astutil.HasCall(call.Args[1]) {
		return false
	}

	return r.q.render(assign.Lhs[0]) == r.q.render(call.Args[0]) &&
		r.q.render(call.Args[1]) == source
}

func (r collectionRules) lenGuardSource(
	expr ast.Expr,
	proves func(token.Token, int64) bool,
) (string, bool) {
	binary, ok := ast.Unparen(expr).(*ast.BinaryExpr)
	if !ok {
		return "", false
	}

	if source, limit, ok := r.lenCompareOperand(binary.X, binary.Y); ok {
		return source, proves(binary.Op, limit)
	}

	if source, limit, ok := r.lenCompareOperand(binary.Y, binary.X); ok {
		return source, proves(reverseOrderedOp(binary.Op), limit)
	}

	return "", false
}

func (r collectionRules) lenCompareOperand(
	lenExpr ast.Expr,
	limitExpr ast.Expr,
) (string, int64, bool) {
	info := r.q.pkg.TypesInfo

	call, ok := ast.Unparen(lenExpr).(*ast.CallExpr)
	if !ok || len(call.Args) != 1 || builtinName(info, call) != builtinLenName {
		return "", 0, false
	}

	limit, ok := constInt(info, limitExpr)
	if !ok {
		return "", 0, false
	}

	return r.q.render(ast.Unparen(call.Args[0])), limit, true
}

func lenCompareProvesNonEmpty(op token.Token, limit int64) bool {
	// token.Token includes operators irrelevant to len guards.
	//exhaustive:ignore
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

func lenCompareProvesEmpty(op token.Token, limit int64) bool {
	// token.Token includes operators irrelevant to len guards.
	//exhaustive:ignore
	switch op {
	case token.EQL, token.LEQ:
		return limit == 0
	case token.LSS:
		return limit == 1
	default:
		return false
	}
}

func builtinName(info *types.Info, call *ast.CallExpr) string {
	id, ok := ast.Unparen(call.Fun).(*ast.Ident)
	if !ok {
		return ""
	}

	if builtin, ok := info.ObjectOf(id).(*types.Builtin); ok {
		return builtin.Name()
	}

	return ""
}

func (r collectionRules) collectionGuardSource(expr ast.Expr, shape guardShape) (rangeGuard, bool) {
	expr = ast.Unparen(expr)

	if binary, ok := expr.(*ast.BinaryExpr); ok && binary.Op == shape.mergeOp {
		left, leftOK := r.collectionGuardSource(binary.X, shape)

		right, rightOK := r.collectionGuardSource(binary.Y, shape)
		if !leftOK || !rightOK || left.source != right.source {
			return rangeGuard{}, false
		}

		return rangeGuard{
			source: left.source,
			hasLen: left.hasLen || right.hasLen,
			hasNil: left.hasNil || right.hasNil,
		}, true
	}

	if source, ok := r.lenGuardSource(expr, shape.provesLen); ok {
		return rangeGuard{source: source, hasLen: true}, true
	}

	if source, ok := r.nilGuardSource(expr, shape.nilOp); ok {
		return rangeGuard{source: source, hasNil: true}, true
	}

	return rangeGuard{}, false
}

func (r collectionRules) nilGuardSource(expr ast.Expr, op token.Token) (string, bool) {
	binary, ok := expr.(*ast.BinaryExpr)
	if !ok || binary.Op != op {
		return "", false
	}

	info := r.q.pkg.TypesInfo

	if isNilValue(info, binary.Y) {
		return r.q.render(ast.Unparen(binary.X)), true
	}

	if isNilValue(info, binary.X) {
		return r.q.render(ast.Unparen(binary.Y)), true
	}

	return "", false
}

func (guard rangeGuard) label() string {
	switch {
	case guard.hasLen && guard.hasNil:
		return "nil/len"
	case guard.hasLen:
		return builtinLenName
	case guard.hasNil:
		return "nil"
	default:
		return "empty"
	}
}

func rangeNoopsWhenEmpty(info *types.Info, expr ast.Expr) bool {
	switch typ := types.Unalias(info.TypeOf(expr)).Underlying().(type) {
	case *types.Array, *types.Map, *types.Slice:
		return true
	case *types.Basic:
		return typ.Info()&types.IsString != 0
	default:
		return false
	}
}

func rangeNoopsWhenNil(info *types.Info, expr ast.Expr) bool {
	switch types.Unalias(info.TypeOf(expr)).Underlying().(type) {
	case *types.Map, *types.Slice:
		return true
	default:
		return false
	}
}
