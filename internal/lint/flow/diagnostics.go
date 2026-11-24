package flow

import (
	"fmt"
	"go/ast"
	"go/token"
)

type reportFunc func(pos token.Pos, kind, msg string)

func discardFinding(token.Pos, string, string) {}

func (w *pathWalker) checkBooleanSubexpressions(states []state, expr ast.Expr) {
	switch expr := ast.Unparen(expr).(type) {
	case *ast.UnaryExpr:
		if expr.Op == token.NOT {
			w.checkBooleanSubexpressions(states, expr.X)
		}
	case *ast.BinaryExpr:
		// token.Token includes operators not meaningful for this expression type.
		//exhaustive:ignore
		switch expr.Op {
		case token.LAND:
			w.reportRedundantBooleanOperand(states, expr.X, "left side of &&")
			w.checkBooleanSubexpressions(states, expr.X)

			leftTrue := w.cond.refineStates(states, expr.X, true)
			if len(leftTrue) > 0 {
				if tri, because := w.cond.truthAcross(leftTrue, expr.Y); tri == triTrue {
					w.reportRedundantSubexpr(
						expr.Y,
						"right side of && is always true after the left side",
						because,
					)
				} else if tri == triFalse {
					w.reportRedundantSubexpr(
						expr.Y,
						"right side of && is always false after the left side",
						because,
					)
				}

				w.checkBooleanSubexpressions(leftTrue, expr.Y)
			}
		case token.LOR:
			w.reportRedundantBooleanOperand(states, expr.X, "left side of ||")
			w.checkBooleanSubexpressions(states, expr.X)

			leftFalse := w.cond.refineStates(states, expr.X, false)
			if len(leftFalse) > 0 {
				if tri, because := w.cond.truthAcross(leftFalse, expr.Y); tri == triTrue {
					w.reportRedundantSubexpr(
						expr.Y,
						"right side of || is always true after the left side",
						because,
					)
				} else if tri == triFalse {
					w.reportRedundantSubexpr(
						expr.Y,
						"right side of || is always false after the left side",
						because,
					)
				}

				w.checkBooleanSubexpressions(leftFalse, expr.Y)
			}
		}
	}
}

func (w *pathWalker) reportRedundantBooleanOperand(
	states []state,
	expr ast.Expr,
	headline string,
) {
	tri, because := w.cond.truthAcross(states, expr)
	if because == nil {
		return
	}

	switch tri {
	case triTrue:
		w.reportRedundantSubexpr(expr, headline+" is always true", because)
	case triFalse:
		w.reportRedundantSubexpr(expr, headline+" is always false", because)
	case triUnknown:
		return
	}
}

func (w *pathWalker) reportCondition(expr ast.Expr, alwaysTrue bool, because *evidence) {
	msg := fmt.Sprintf(
		"condition %q is always %s here",
		w.q.render(expr),
		map[bool]string{true: boolTrueText, false: boolFalseText}[alwaysTrue],
	)
	if because != nil {
		msg += "; " + w.q.becauseText(*because)
	}

	w.report(expr.Pos(), "redundant_condition", msg)
}

func (w *pathWalker) reportCase(clause *ast.CaseClause, because *evidence) {
	msg := fmt.Sprintf("switch case %q can never match here", w.q.renderCaseClauseHeader(clause))
	if because != nil {
		msg += "; " + w.q.becauseText(*because)
	}

	w.report(clause.Case, "unreachable_case", msg)
}

func (w *pathWalker) reportRedundantSubexpr(expr ast.Expr, headline string, because *evidence) {
	msg := fmt.Sprintf("%s: %q", headline, w.q.render(expr))
	if because != nil {
		msg += "; " + w.q.becauseText(*because)
	}

	w.report(expr.Pos(), "redundant_subexpression", msg)
}

func relationText(sym symbol, op string, value Scalar) string {
	return fmt.Sprintf("%s %s %s", sym.name, op, value.String())
}
