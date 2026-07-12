package structure

import (
	"go/ast"
	"go/token"
	"go/types"
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

type boolIfThenReturnMatch struct {
	cond      ast.Expr
	whenTrue  bool
	whenFalse bool
	pos       token.Pos
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
	key    string
	source string
	pos    token.Pos
}

type appendLenGuardMatch struct {
	pos    token.Pos
	source string
}

type rangeGuard struct {
	source string
	hasLen bool
	hasNil bool
}

type rangeGuardMatch struct {
	pos    token.Pos
	source string
	guard  string
}

type emptyRangeGuardMatch struct {
	pos    token.Pos
	source string
	guard  string
}

type objectUseSummary struct {
	reads  int
	unsafe bool
}

func (l *Runner) scanStructuralBlock(stmts []ast.Stmt, ctx blockContext) {
	for idx, stmt := range stmts {
		l.checkRedundantBoolReturn(stmts, idx)
		l.checkRedundantReturnGuardRun(stmts, idx)
		l.checkSingleUseTempAlias(stmts, idx)
		l.checkRedundantAppendLenGuard(stmt)
		l.checkRedundantRangeGuard(stmt)
		l.checkEmptyRangeReturnGuard(stmts, idx, ctx)
		l.checkNestedFinalIfPyramid(stmts, idx, ctx)
		l.checkDuplicateAdjacentRangeLoop(stmts, idx)
		l.scanStructuralStmt(stmt)
	}
}

func (l *Runner) scanStructuralStmt(stmt ast.Stmt) {
	switch stmt := stmt.(type) {
	case *ast.BlockStmt:
		l.scanStructuralBlock(stmt.List, blockContext{})
	case *ast.IfStmt:
		l.scanStructuralIfStmt(stmt)
	case *ast.ForStmt:
		l.checkForLoopPerformance(stmt)
		l.scanStructuralBlock(stmt.Body.List, blockContext{})
	case *ast.RangeStmt:
		l.checkRangeLoopPerformance(stmt)
		l.scanStructuralBlock(stmt.Body.List, blockContext{})
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

func (l *Runner) scanStructuralIfStmt(stmt *ast.IfStmt) {
	l.checkIdenticalIfBranches(stmt)
	l.scanStructuralBlock(stmt.Body.List, blockContext{})

	if stmt.Else != nil {
		l.scanStructuralStmt(stmt.Else)
	}
}

func (l *Runner) scanStructuralSwitchStmt(stmt *ast.SwitchStmt) {
	l.checkIdenticalSwitchBranches(stmt)
	l.checkExhaustiveBoolDefault(stmt)
	l.scanCaseClauseBodies(stmt.Body.List)
}

func (l *Runner) scanCaseClauseBodies(list []ast.Stmt) {
	for _, raw := range list {
		clause, ok := raw.(*ast.CaseClause)
		if !ok {
			continue
		}

		l.scanStructuralBlock(clause.Body, blockContext{})
	}
}

func (l *Runner) scanCommClauseBodies(list []ast.Stmt) {
	for _, raw := range list {
		clause, ok := raw.(*ast.CommClause)
		if !ok {
			continue
		}

		l.scanStructuralBlock(clause.Body, blockContext{})
	}
}
