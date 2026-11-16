package structure

import (
	"go/ast"
)

// blockFinding is a finding at one statement-list position. points is how
// many simplifications it counts for in the function complexity score.
type blockFinding struct {
	Finding

	points int
}

func (l *Runner) blockFindingsAt(stmts []ast.Stmt, idx int, ctx blockContext) []blockFinding {
	var out []blockFinding

	add := func(finding Finding, ok bool) {
		if ok {
			out = append(out, blockFinding{Finding: finding, points: 1})
		}
	}

	add(boolRules{l.q}.boolChoiceIfAt(stmts, idx))

	if finding, guards, ok := (branchRules{l.q}).returnGuardRunAt(stmts, idx); ok {
		out = append(out, blockFinding{Finding: finding, points: guards})
	}

	add(tempAliasRules{l.q}.singleUseAt(stmts, idx))
	add(collectionRules{l.q}.appendLenGuard(stmts[idx]))
	add(collectionRules{l.q}.rangeGuard(stmts[idx]))
	add(collectionRules{l.q}.emptyRangeReturnGuard(stmts, idx, ctx))
	add(branchRules{l.q}.finalIfPyramidAt(stmts, idx, ctx))

	return out
}

func (l *Runner) stmtFindings(stmt ast.Stmt) []Finding {
	switch stmt := stmt.(type) {
	case *ast.IfStmt:
		if finding, ok := (branchRules{l.q}).identicalIf(stmt); ok {
			return []Finding{finding}
		}
	case *ast.ForStmt:
		return loopRules{l.q}.forLoop(stmt)
	case *ast.RangeStmt:
		return loopRules{l.q}.rangeLoop(stmt)
	case *ast.SwitchStmt:
		out := branchRules{l.q}.identicalSwitchCases(stmt)
		if finding, ok := redundantBoolDefault(l.q.pkg.TypesInfo, stmt); ok {
			out = append(out, finding)
		}

		return out
	}

	return nil
}

// stmtWalker visits a function body in source order. atIndex sees each
// statement-list position, then atStmt sees the statement there before its
// nested lists. Else-if and labeled statements sit outside any list, so they
// reach atStmt only. Nested lists get an empty blockContext.
type stmtWalker struct {
	atIndex func(stmts []ast.Stmt, idx int, ctx blockContext)
	atStmt  func(stmt ast.Stmt)
}

func (w stmtWalker) walkList(stmts []ast.Stmt, ctx blockContext) {
	for idx, stmt := range stmts {
		w.atIndex(stmts, idx, ctx)
		w.walkStmt(stmt)
	}
}

func (w stmtWalker) walkStmt(stmt ast.Stmt) {
	w.atStmt(stmt)

	switch stmt := stmt.(type) {
	case *ast.BlockStmt:
		w.walkList(stmt.List, blockContext{})
	case *ast.IfStmt:
		w.walkList(stmt.Body.List, blockContext{})

		if stmt.Else != nil {
			w.walkStmt(stmt.Else)
		}
	case *ast.ForStmt:
		w.walkList(stmt.Body.List, blockContext{})
	case *ast.RangeStmt:
		w.walkList(stmt.Body.List, blockContext{})
	case *ast.SwitchStmt:
		w.walkClauses(stmt.Body)
	case *ast.TypeSwitchStmt:
		w.walkClauses(stmt.Body)
	case *ast.SelectStmt:
		w.walkClauses(stmt.Body)
	case *ast.LabeledStmt:
		w.walkStmt(stmt.Stmt)
	}
}

func (w stmtWalker) walkClauses(body *ast.BlockStmt) {
	for _, raw := range body.List {
		switch clause := raw.(type) {
		case *ast.CaseClause:
			w.walkList(clause.Body, blockContext{})
		case *ast.CommClause:
			w.walkList(clause.Body, blockContext{})
		}
	}
}
