package lint

import (
	"fmt"
	"go/ast"
	"go/token"
	"strings"
)

func (l *linter) checkBooleanSubexpressions(states []state, expr ast.Expr) {
	switch expr := l.unparen(expr).(type) {
	case *ast.UnaryExpr:
		if expr.Op == token.NOT {
			l.checkBooleanSubexpressions(states, expr.X)
		}
	case *ast.BinaryExpr:
		//exhaustive:ignore token.Token includes operators not meaningful for this expression type.
		switch expr.Op {
		case token.LAND:
			l.reportRedundantBooleanOperand(states, expr.X, "left side of &&")
			l.checkBooleanSubexpressions(states, expr.X)

			leftTrue := l.refineStates(states, expr.X, true)
			if len(leftTrue) > 0 {
				if tri, because := l.truthAcross(leftTrue, expr.Y); tri == triTrue {
					l.reportRedundantSubexpr(
						expr.Y,
						"right side of && is always true after the left side",
						because,
					)
				} else if tri == triFalse {
					l.reportRedundantSubexpr(
						expr.Y,
						"right side of && is always false after the left side",
						because,
					)
				}

				l.checkBooleanSubexpressions(leftTrue, expr.Y)
			}
		case token.LOR:
			l.reportRedundantBooleanOperand(states, expr.X, "left side of ||")
			l.checkBooleanSubexpressions(states, expr.X)

			leftFalse := l.refineStates(states, expr.X, false)
			if len(leftFalse) > 0 {
				if tri, because := l.truthAcross(leftFalse, expr.Y); tri == triTrue {
					l.reportRedundantSubexpr(
						expr.Y,
						"right side of || is always true after the left side",
						because,
					)
				} else if tri == triFalse {
					l.reportRedundantSubexpr(
						expr.Y,
						"right side of || is always false after the left side",
						because,
					)
				}

				l.checkBooleanSubexpressions(leftFalse, expr.Y)
			}
		}
	}
}

func (l *linter) reportRedundantBooleanOperand(
	states []state,
	expr ast.Expr,
	headline string,
) {
	tri, because := l.truthAcross(states, expr)
	if because == nil {
		return
	}

	switch tri {
	case triTrue:
		l.reportRedundantSubexpr(expr, headline+" is always true", because)
	case triFalse:
		l.reportRedundantSubexpr(expr, headline+" is always false", because)
	case triUnknown:
		return
	}
}

func (l *linter) reportCondition(expr ast.Expr, alwaysTrue bool, because *evidence) {
	msg := fmt.Sprintf(
		"condition %q is always %s here",
		l.render(expr),
		map[bool]string{true: boolTrueText, false: boolFalseText}[alwaysTrue],
	)
	if because != nil {
		msg += "; " + l.becauseText(*because)
	}

	l.report(expr.Pos(), "redundant_condition", msg)
}

func (l *linter) reportCase(clause *ast.CaseClause, because *evidence) {
	msg := fmt.Sprintf("switch case %q can never match here", l.renderCaseClauseHeader(clause))
	if because != nil {
		msg += "; " + l.becauseText(*because)
	}

	l.report(clause.Case, "unreachable_case", msg)
}

func (l *linter) reportRedundantSubexpr(expr ast.Expr, headline string, because *evidence) {
	msg := fmt.Sprintf("%s: %q", headline, l.render(expr))
	if because != nil {
		msg += "; " + l.becauseText(*because)
	}

	l.report(expr.Pos(), "redundant_subexpression", msg)
}

func (l *linter) becauseText(ev evidence) string {
	pos := l.pkg.FSet.Position(ev.pos)
	if pos.IsValid() {
		return fmt.Sprintf(
			"reachable paths already establish %q at %s:%d",
			ev.text,
			pos.Filename,
			pos.Line,
		)
	}

	return fmt.Sprintf("reachable paths already establish %q", ev.text)
}

func (l *linter) report(pos token.Pos, kind, msg string) {
	if l.suppressReports > 0 {
		return
	}

	p := l.pkg.FSet.Position(pos)

	key := fmt.Sprintf("%s:%d:%d:%s:%s", p.Filename, p.Line, p.Column, kind, msg)
	if _, dup := l.reported[key]; dup {
		return
	}

	l.reported[key] = struct{}{}
	l.issues = append(l.issues, Issue{
		Pos:     pos,
		Kind:    kind,
		Message: msg,
		fset:    l.pkg.FSet,
	})
}

func (l *linter) withReportsSuppressed(fn func()) {
	l.suppressReports++

	defer func() {
		l.suppressReports--
	}()

	fn()
}

func (l *linter) render(node ast.Node) string {
	if node == nil {
		return ""
	}

	if s, ok := l.renderCache[node]; ok {
		return s
	}

	s := renderNode(l.pkg.FSet, node)
	l.renderCache[node] = s

	return s
}

func (l *linter) renderCaseClauseHeader(clause *ast.CaseClause) string {
	if len(clause.List) == 0 {
		return "default"
	}

	parts := make([]string, 0, len(clause.List))
	for _, expr := range clause.List {
		parts = append(parts, l.render(expr))
	}

	return strings.Join(parts, ", ")
}

func (l *linter) relationText(sym symbol, op string, value scalar) string {
	return fmt.Sprintf("%s %s %s", sym.name, op, value.String())
}
