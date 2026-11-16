package structure

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
)

// boolBranchAction is a branch that only returns or assigns a bool literal.
// target is empty for a return.
type boolBranchAction struct {
	value  bool
	target string
}

type boolRules struct {
	q *pkgQuery
}

func (r boolRules) boolChoiceIfAt(stmts []ast.Stmt, idx int) (Finding, bool) {
	stmt, ok := stmts[idx].(*ast.IfStmt)
	if !ok || stmt.Init != nil {
		return Finding{}, false
	}

	var other ast.Node

	var otherAction boolBranchAction

	if stmt.Else != nil {
		elseBlock, isBlock := stmt.Else.(*ast.BlockStmt)
		if !isBlock {
			return Finding{}, false
		}

		other = elseBlock
		otherAction, ok = r.branchAction(elseBlock.List)
	} else {
		if idx+1 >= len(stmts) {
			return Finding{}, false
		}

		next, isReturn := stmts[idx+1].(*ast.ReturnStmt)
		if !isReturn {
			return Finding{}, false
		}

		other = next
		otherAction.value, ok = singleBoolReturnValue(r.q.pkg.TypesInfo, next)
	}

	if !ok {
		return Finding{}, false
	}

	thenAction, ok := r.branchAction(stmt.Body.List)
	if !ok || thenAction.target != otherAction.target || !r.q.commentsMatch(stmt.Body, other) {
		return Finding{}, false
	}

	return r.ceremonyFinding(stmt, thenAction, otherAction)
}

func (r boolRules) ceremonyFinding(
	stmt *ast.IfStmt,
	whenTrue boolBranchAction,
	whenFalse boolBranchAction,
) (Finding, bool) {
	prefix, verb := "return ", "returns"
	if whenTrue.target != "" {
		prefix, verb = whenTrue.target+" = ", "assigns"
	}

	var replacement string

	switch {
	case whenTrue.value && !whenFalse.value:
		replacement = prefix + r.q.render(stmt.Cond)
	case !whenTrue.value && whenFalse.value:
		replacement = prefix + "!(" + r.q.render(stmt.Cond) + ")"
	default:
		return Finding{}, false
	}

	return Finding{
		Pos:     stmt.If,
		Kind:    "boolean_ceremony",
		Message: fmt.Sprintf(`if %s boolean literals; replace with %q`, verb, replacement),
	}, true
}

func (r boolRules) branchAction(stmts []ast.Stmt) (boolBranchAction, bool) {
	if len(stmts) != 1 {
		return boolBranchAction{}, false
	}

	info := r.q.pkg.TypesInfo

	switch stmt := stmts[0].(type) {
	case *ast.ReturnStmt:
		value, ok := singleBoolReturnValue(info, stmt)

		return boolBranchAction{value: value}, ok
	case *ast.AssignStmt:
		if stmt.Tok != token.ASSIGN || len(stmt.Lhs) != 1 || len(stmt.Rhs) != 1 ||
			exprHasCalls(stmt.Lhs[0]) {
			return boolBranchAction{}, false
		}

		value, ok := constBool(info, stmt.Rhs[0])

		return boolBranchAction{value: value, target: r.q.render(stmt.Lhs[0])}, ok
	default:
		return boolBranchAction{}, false
	}
}

func singleBoolReturnValue(info *types.Info, stmt *ast.ReturnStmt) (bool, bool) {
	if len(stmt.Results) != 1 {
		return false, false
	}

	return constBool(info, stmt.Results[0])
}

func redundantBoolDefault(info *types.Info, stmt *ast.SwitchStmt) (Finding, bool) {
	if stmt.Tag == nil {
		return Finding{}, false
	}

	basic, ok := types.Unalias(info.TypeOf(stmt.Tag)).Underlying().(*types.Basic)
	if !ok || basic.Info()&types.IsBoolean == 0 {
		return Finding{}, false
	}

	defaultClause, ok := defaultOfExhaustiveBoolSwitch(info, stmt)
	if !ok || isImpossibleStatePanic(info, defaultClause.Body) {
		return Finding{}, false
	}

	return Finding{
		Pos:     defaultClause.Case,
		Kind:    "redundant_default",
		Message: "default case is redundant; bool switch already covers true and false",
	}, true
}

func defaultOfExhaustiveBoolSwitch(info *types.Info, stmt *ast.SwitchStmt) (*ast.CaseClause, bool) {
	var defaultClause *ast.CaseClause

	covered := map[bool]bool{}

	for _, raw := range stmt.Body.List {
		clause, ok := raw.(*ast.CaseClause)
		if !ok {
			continue
		}

		if len(clause.List) == 0 {
			defaultClause = clause
			continue
		}

		for _, expr := range clause.List {
			value, ok := constBool(info, expr)
			if !ok {
				return nil, false
			}

			covered[value] = true
		}
	}

	return defaultClause, defaultClause != nil && covered[true] && covered[false]
}

func isImpossibleStatePanic(info *types.Info, body []ast.Stmt) bool {
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

	return ok && obj.Name() == panicText
}
