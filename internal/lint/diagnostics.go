package lint

import (
	"fmt"
	"go/ast"
	"go/token"
)

// issueSink collects issues in report order and drops exact repeats.
type issueSink struct {
	fset     *token.FileSet
	issues   []Issue
	reported map[string]struct{}
}

func newIssueSink(fset *token.FileSet) *issueSink {
	return &issueSink{fset: fset, issues: make([]Issue, 0), reported: make(map[string]struct{})}
}

func (s *issueSink) report(pos token.Pos, kind, msg string) {
	p := s.fset.Position(pos)

	key := fmt.Sprintf("%s:%d:%d:%s:%s", p.Filename, p.Line, p.Column, kind, msg)
	if _, dup := s.reported[key]; dup {
		return
	}

	s.reported[key] = struct{}{}
	s.issues = append(s.issues, Issue{
		Pos:     pos,
		Kind:    kind,
		Message: msg,
		fset:    s.fset,
	})
}

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

	w.sink.report(expr.Pos(), "redundant_condition", msg)
}

func (w *pathWalker) reportCase(clause *ast.CaseClause, because *evidence) {
	msg := fmt.Sprintf("switch case %q can never match here", w.q.renderCaseClauseHeader(clause))
	if because != nil {
		msg += "; " + w.q.becauseText(*because)
	}

	w.sink.report(clause.Case, "unreachable_case", msg)
}

func (w *pathWalker) reportRedundantSubexpr(expr ast.Expr, headline string, because *evidence) {
	msg := fmt.Sprintf("%s: %q", headline, w.q.render(expr))
	if because != nil {
		msg += "; " + w.q.becauseText(*because)
	}

	w.sink.report(expr.Pos(), "redundant_subexpression", msg)
}

func relationText(sym symbol, op string, value scalar) string {
	return fmt.Sprintf("%s %s %s", sym.name, op, value.String())
}
