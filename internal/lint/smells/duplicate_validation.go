package smells

import (
	"fmt"
	"go/ast"
	"go/token"
	"strings"
)

const duplicateValidationMinimumGuards = 3

type validationLadder struct {
	key    string
	fnName string
	pos    token.Pos
}

func (l *Runner) checkDuplicateValidationLadders() {
	seen := make(map[string]validationLadder)

	l.forEachProductionFunc(func(fn *ast.FuncDecl) {
		if fn.Body == nil || fn.Name == nil {
			return
		}

		ladder, ok := l.validationLadderForFunc(fn)
		if !ok {
			return
		}

		prior, dup := seen[ladder.key]
		if !dup {
			seen[ladder.key] = ladder
			return
		}

		l.report(
			ladder.pos,
			"duplicate_validation",
			fmt.Sprintf(
				`validation ladder in %q duplicates %q; extract shared validation`,
				ladder.fnName,
				prior.fnName,
			),
		)
	})
}

func (l *Runner) validationLadderForFunc(fn *ast.FuncDecl) (validationLadder, bool) {
	parts := make([]string, 0, duplicateValidationMinimumGuards)

	var first token.Pos

	for _, stmt := range fn.Body.List {
		guard, ok := l.validationGuardShape(stmt)
		if !ok {
			break
		}

		if first == token.NoPos {
			first = stmt.Pos()
		}

		parts = append(parts, guard)
	}

	if len(parts) < duplicateValidationMinimumGuards {
		return validationLadder{}, false
	}

	return validationLadder{
		key:    strings.Join(parts, "\n"),
		fnName: fn.Name.Name,
		pos:    first,
	}, true
}

func (l *Runner) validationGuardShape(stmt ast.Stmt) (string, bool) {
	if l.hasAttachedComment(stmt) {
		return "", false
	}

	ifStmt, ok := stmt.(*ast.IfStmt)
	if !ok || ifStmt.Init != nil || ifStmt.Else != nil {
		return "", false
	}

	if !l.isValidationPredicate(ifStmt.Cond) {
		return "", false
	}

	if len(ifStmt.Body.List) != 1 {
		return "", false
	}

	ret, ok := ifStmt.Body.List[0].(*ast.ReturnStmt)
	if !ok {
		return "", false
	}

	return l.render(ifStmt.Cond) + " => " + l.render(ret), true
}

func (l *Runner) isValidationPredicate(expr ast.Expr) bool {
	expr = l.unparen(expr)

	binary, ok := expr.(*ast.BinaryExpr)
	if !ok || !isValidationCompareOp(binary.Op) {
		return false
	}

	if _, _, ok := l.symbolScalar(binary.X, binary.Y); ok {
		return true
	}

	if _, _, ok := l.symbolScalar(binary.Y, binary.X); ok {
		return true
	}

	return false
}

func isValidationCompareOp(op token.Token) bool {
	return op == token.EQL ||
		op == token.NEQ ||
		op == token.LSS ||
		op == token.LEQ ||
		op == token.GTR ||
		op == token.GEQ
}
