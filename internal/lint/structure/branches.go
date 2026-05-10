package structure

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"strings"
)

func (l *Runner) checkIdenticalIfBranches(stmt *ast.IfStmt) {
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
