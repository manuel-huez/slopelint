package lint

import (
	"fmt"
	"go/ast"
)

func (l *linter) execIfStmt(stmt *ast.IfStmt, states []state) flowResult {
	states = l.execOptionalStmt(stmt.Init, states)
	if len(states) == 0 {
		return flowResult{}
	}

	l.checkBooleanSubexpressions(states, stmt.Cond)

	if tri, because := l.truthAcross(states, stmt.Cond); tri == triTrue {
		l.reportCondition(stmt.Cond, true, because)
	} else if tri == triFalse {
		l.reportCondition(stmt.Cond, false, because)
	}

	thenStates := l.refineStates(states, stmt.Cond, true)
	elseStates := l.refineStates(states, stmt.Cond, false)

	thenRes := l.execBlock(stmt.Body.List, thenStates)

	var elseRes flowResult
	if stmt.Else != nil {
		elseRes = l.execElse(stmt.Else, elseStates)
	} else {
		elseRes = flowResult{next: elseStates}
	}

	return flowResult{
		next:         l.normalizeStates(append(thenRes.next, elseRes.next...)),
		breaks:       l.normalizeStates(append(thenRes.breaks, elseRes.breaks...)),
		continues:    l.normalizeStates(append(thenRes.continues, elseRes.continues...)),
		fallthroughs: l.normalizeStates(append(thenRes.fallthroughs, elseRes.fallthroughs...)),
		labeledBreaks: l.normalizeLabeledStates(
			l.mergeLabeledStates(thenRes.labeledBreaks, elseRes.labeledBreaks),
		),
		labeledContinues: l.normalizeLabeledStates(
			l.mergeLabeledStates(thenRes.labeledContinues, elseRes.labeledContinues),
		),
		returns: append(thenRes.returns, elseRes.returns...),
	}
}

func (l *linter) execElse(stmt ast.Stmt, states []state) flowResult {
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		return l.execBlock(s.List, states)
	case *ast.IfStmt:
		return l.execIfStmt(s, states)
	default:
		return l.execStmt(stmt, states)
	}
}

func (l *linter) execForStmt(stmt *ast.ForStmt, states []state) flowResult {
	return l.execForStmtWithLabel(stmt, "", states)
}

func (l *linter) execForStmtWithLabel(stmt *ast.ForStmt, label string, states []state) flowResult {
	states = l.execOptionalStmt(stmt.Init, states)
	if len(states) == 0 {
		return flowResult{}
	}

	enterStates := states
	exitStates := []state(nil)

	if stmt.Cond != nil {
		l.checkBooleanSubexpressions(states, stmt.Cond)

		if tri, because := l.truthAcross(states, stmt.Cond); tri == triFalse {
			l.reportCondition(stmt.Cond, false, because)
		} else if tri == triTrue {
			l.reportCondition(stmt.Cond, true, because)
		}

		enterStates = l.refineStates(states, stmt.Cond, true)
		exitStates = l.refineStates(states, stmt.Cond, false)
	}

	loopInvalidations := l.loopInvalidationsForLoop(stmt.Body.List, stmt.Post)
	bodyRes := l.execBlock(
		stmt.Body.List,
		l.applyPrefixInvalidations(enterStates, loopInvalidations),
	)

	iterStates := l.normalizeStates(append(bodyRes.next, bodyRes.continues...))
	iterStates = append(iterStates, consumeLabeledStates(bodyRes.labeledContinues, label)...)
	iterStates = l.normalizeStates(iterStates)

	if stmt.Post != nil {
		iterStates = l.execStmt(stmt.Post, iterStates).next
	}

	// The loop may execute multiple times. Preserve precision inside one iteration,
	// then widen when approximating the state after additional iterations.
	var afterLoop []state

	afterLoop = append(afterLoop, exitStates...)
	afterLoop = append(afterLoop, bodyRes.breaks...)
	afterLoop = append(afterLoop, consumeLabeledStates(bodyRes.labeledBreaks, label)...)

	if stmt.Cond != nil && len(iterStates) != 0 {
		widened := l.wipeFacts(iterStates)
		afterLoop = append(afterLoop, l.refineStates(widened, stmt.Cond, false)...)
	}

	return flowResult{
		next:             l.normalizeStates(afterLoop),
		labeledBreaks:    l.normalizeLabeledStates(bodyRes.labeledBreaks),
		labeledContinues: l.normalizeLabeledStates(bodyRes.labeledContinues),
		returns:          bodyRes.returns,
	}
}

func (l *linter) execOptionalStmt(stmt ast.Stmt, states []state) []state {
	if stmt != nil {
		states = l.execStmt(stmt, states).next
	}

	return l.normalizeStates(states)
}

func (l *linter) execRangeStmt(stmt *ast.RangeStmt, states []state) flowResult {
	return l.execRangeStmtWithLabel(stmt, "", states)
}

func (l *linter) execRangeStmtWithLabel(
	stmt *ast.RangeStmt,
	label string,
	states []state,
) flowResult {
	states = l.invalidateForExprSideEffects(states, stmt.X)
	enterStates := l.normalizeStates(states)
	loopInvalidations := l.loopInvalidationsForLoop(stmt.Body.List, nil)
	enterStates = l.applyPrefixInvalidations(enterStates, loopInvalidations)

	bodyStart := make([]state, 0, len(enterStates))
	for _, st0 := range enterStates {
		st := st0.clone()
		if stmt.Key != nil {
			l.invalidateLHS(&st, stmt.Key)
		}

		if stmt.Value != nil {
			l.invalidateLHS(&st, stmt.Value)
		}

		bodyStart = append(bodyStart, st)
	}

	bodyRes := l.execBlock(stmt.Body.List, bodyStart)
	iterStates := l.normalizeStates(append(bodyRes.next, bodyRes.continues...))
	iterStates = append(iterStates, consumeLabeledStates(bodyRes.labeledContinues, label)...)
	iterStates = l.applyPrefixInvalidations(iterStates, loopInvalidations)

	// A range loop may execute zero times, so the incoming state is always an exit state.
	out := append([]state{}, states...)
	out = append(out, bodyRes.breaks...)
	out = append(out, consumeLabeledStates(bodyRes.labeledBreaks, label)...)
	out = append(out, iterStates...)

	return flowResult{
		next:             l.normalizeStates(out),
		labeledBreaks:    l.normalizeLabeledStates(bodyRes.labeledBreaks),
		labeledContinues: l.normalizeLabeledStates(bodyRes.labeledContinues),
		returns:          bodyRes.returns,
	}
}

func (l *linter) execSwitchStmt(stmt *ast.SwitchStmt, states []state) flowResult {
	return l.execSwitchStmtWithLabel(stmt, "", states)
}

func (l *linter) prepareSwitchClauseStates(
	stmt *ast.SwitchStmt,
	clause *ast.CaseClause,
	remaining []state,
	carriedFallthrough []state,
) ([]state, []state, bool, bool) {
	hasExplicitCase := len(clause.List) > 0

	caseStates := append([]state{}, carriedFallthrough...)

	if !hasExplicitCase {
		caseStates = append(caseStates, remaining...)

		return caseStates, remaining, false, true
	}

	if len(remaining) == 0 && len(carriedFallthrough) == 0 {
		l.report(
			clause.Case,
			"unreachable_case",
			fmt.Sprintf(
				"switch case %q is unreachable here; previous cases already cover all reachable values",
				l.renderCaseClauseHeader(clause),
			),
		)

		return nil, remaining, true, false
	}

	if len(remaining) == 0 {
		return caseStates, remaining, false, false
	}

	tri, because := l.truthAcrossCase(remaining, stmt.Tag, clause.List)
	if tri == triFalse && len(carriedFallthrough) == 0 {
		l.reportCase(clause, because)
	}

	caseStates = append(
		caseStates,
		l.refineStatesCase(remaining, stmt.Tag, clause.List, true)...,
	)

	return caseStates, l.refineStatesCase(remaining, stmt.Tag, clause.List, false), false, false
}

func (l *linter) execSwitchStmtWithLabel(
	stmt *ast.SwitchStmt,
	label string,
	states []state,
) flowResult {
	if stmt.Init != nil {
		states = l.execStmt(stmt.Init, states).next
	}

	states = l.normalizeStates(states)
	if len(states) == 0 {
		return flowResult{}
	}

	remaining := states

	var acc clauseFlow

	carriedFallthrough := make([]state, 0)
	defaultSeen := false

	for _, raw := range stmt.Body.List {
		clause := raw.(*ast.CaseClause)

		caseStates, nextRemaining, skipClause, isDefault := l.prepareSwitchClauseStates(
			stmt,
			clause,
			remaining,
			carriedFallthrough,
		)

		if isDefault {
			defaultSeen = true
		}

		if skipClause {
			continue
		}

		remaining = nextRemaining

		caseStates = l.normalizeStates(caseStates)
		caseRes := l.execBlock(clause.Body, caseStates)
		acc.add(l, label, caseRes)
		carriedFallthrough = l.normalizeStates(caseRes.fallthroughs)
	}

	if !defaultSeen {
		acc.next = append(acc.next, remaining...)
	}

	return acc.result(l)
}

func (l *linter) execTypeSwitchStmt(stmt *ast.TypeSwitchStmt, states []state) flowResult {
	return l.execTypeSwitchStmtWithLabel(stmt, "", states)
}

func (l *linter) execTypeSwitchStmtWithLabel(
	stmt *ast.TypeSwitchStmt,
	label string,
	states []state,
) flowResult {
	if stmt.Init != nil {
		states = l.execStmt(stmt.Init, states).next
	}

	if stmt.Assign != nil {
		states = l.execStmt(stmt.Assign, states).next
	}

	states = l.normalizeStates(states)
	if len(states) == 0 {
		return flowResult{}
	}

	var acc clauseFlow

	defaultSeen := false

	for _, raw := range stmt.Body.List {
		clause := raw.(*ast.CaseClause)
		if len(clause.List) == 0 {
			defaultSeen = true
		}

		caseRes := l.execBlock(clause.Body, states)
		acc.add(l, label, caseRes)
	}

	if !defaultSeen {
		acc.next = append(acc.next, states...)
	}

	return acc.result(l)
}

func (l *linter) execSelectStmt(stmt *ast.SelectStmt, states []state) flowResult {
	return l.execSelectStmtWithLabel(stmt, "", states)
}

func (l *linter) execSelectStmtWithLabel(
	stmt *ast.SelectStmt,
	label string,
	states []state,
) flowResult {
	states = l.normalizeStates(states)
	if len(states) == 0 {
		return flowResult{}
	}

	var acc clauseFlow

	for _, raw := range stmt.Body.List {
		clause := raw.(*ast.CommClause)
		clauseStates := states

		if clause.Comm != nil {
			clauseStates = l.execStmt(clause.Comm, clauseStates).next
		}

		caseRes := l.execBlock(clause.Body, clauseStates)
		acc.add(l, label, caseRes)
	}

	return acc.result(l)
}
