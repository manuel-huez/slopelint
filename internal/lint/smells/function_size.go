package smells

import (
	"go/ast"

	"github.com/manuel-huez/slopelint/internal/lint/outline"
)

// statementCount counts the statements in body, nested blocks and closures
// included. Blocks only group statements, and a switch counts one per case
// plus its longest case: a table of short cases reads case by case.
func statementCount(body *ast.BlockStmt) int {
	if body == nil {
		return 0
	}

	count := 0
	for _, stmt := range body.List {
		count += stmtSize(stmt)
	}

	return count
}

func stmtSize(stmt ast.Stmt) int {
	switch stmt := stmt.(type) {
	case nil, *ast.EmptyStmt:
		return 0
	case *ast.BlockStmt:
		return statementCount(stmt)
	case *ast.SwitchStmt:
		return 1 + stmtSize(stmt.Init) + clauseSize(stmt.Body)
	case *ast.TypeSwitchStmt:
		return 1 + stmtSize(stmt.Init) + clauseSize(stmt.Body)
	case *ast.SelectStmt:
		return 1 + clauseSize(stmt.Body)
	}

	count := 1

	ast.Inspect(stmt, func(node ast.Node) bool {
		inner, ok := node.(ast.Stmt)
		if !ok || inner == stmt {
			return true
		}

		count += stmtSize(inner)

		return false
	})

	return count
}

func clauseSize(body *ast.BlockStmt) int {
	longest := 0

	for _, clause := range body.List {
		var stmts []ast.Stmt

		switch clause := clause.(type) {
		case *ast.CaseClause:
			stmts = clause.Body
		case *ast.CommClause:
			stmts = append([]ast.Stmt{clause.Comm}, clause.Body...)
		}

		longest = max(longest, statementCount(&ast.BlockStmt{List: stmts}))
	}

	return len(body.List) + longest
}

func (l sizeLimitCheck) largestStatements(body *ast.BlockStmt) []outline.StatementBlock {
	if body == nil {
		return nil
	}

	blocks := make([]outline.StatementBlock, 0, len(body.List))

	for _, stmt := range body.List {
		blocks = append(blocks, outline.StatementBlock{
			Line:       l.pkg.FSet.Position(stmt.Pos()).Line,
			Statements: stmtSize(stmt),
		})
	}

	return outline.LargestStatements(blocks)
}

// movableClosures returns the closures in fn that use nothing fn declares,
// so they can move out of it and stop counting toward its size.
func (l sizeLimitCheck) movableClosures(fn *ast.FuncDecl) []outline.StatementBlock {
	if fn.Body == nil {
		return nil
	}

	var closures []outline.StatementBlock

	ast.Inspect(fn.Body, func(node ast.Node) bool {
		lit, ok := node.(*ast.FuncLit)
		if !ok {
			return true
		}

		if l.capturesFrom(lit, fn) {
			return true
		}

		closures = append(closures, outline.StatementBlock{
			Line:       l.pkg.FSet.Position(lit.Pos()).Line,
			Statements: statementCount(lit.Body),
		})

		return false
	})

	return outline.LargestStatements(closures)
}

func (l sizeLimitCheck) capturesFrom(lit *ast.FuncLit, fn *ast.FuncDecl) bool {
	captures := false

	ast.Inspect(lit, func(node ast.Node) bool {
		ident, ok := node.(*ast.Ident)
		if !ok || captures {
			return !captures
		}

		obj := l.pkg.TypesInfo.Uses[ident]
		captures = obj != nil && obj.Pos() >= fn.Pos() && obj.Pos() < fn.End() &&
			(obj.Pos() < lit.Pos() || obj.Pos() >= lit.End())

		return !captures
	})

	return captures
}
