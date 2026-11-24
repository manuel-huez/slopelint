package flow

import (
	"fmt"
	"go/ast"
)

func (w *pathWalker) execIfStmt(stmt *ast.IfStmt, states []state) flowResult {
	states = w.execOptionalStmt(stmt.Init, states)
	if len(states) == 0 {
		return flowResult{}
	}

	w.checkBooleanSubexpressions(states, stmt.Cond)

	if tri, because := w.cond.truthAcross(states, stmt.Cond); tri == triTrue {
		w.reportCondition(stmt.Cond, true, because)
	} else if tri == triFalse {
		w.reportCondition(stmt.Cond, false, because)
	}

	thenStates := w.cond.refineStates(states, stmt.Cond, true)
	elseStates := w.cond.refineStates(states, stmt.Cond, false)

	thenRes := w.execBlock(stmt.Body.List, thenStates)

	var elseRes flowResult
	if stmt.Else != nil {
		elseRes = w.execElse(stmt.Else, elseStates)
	} else {
		elseRes = flowResult{next: elseStates}
	}

	return flowResult{
		next:         w.budget.normalize(append(thenRes.next, elseRes.next...)),
		breaks:       w.budget.normalize(append(thenRes.breaks, elseRes.breaks...)),
		continues:    w.budget.normalize(append(thenRes.continues, elseRes.continues...)),
		fallthroughs: w.budget.normalize(append(thenRes.fallthroughs, elseRes.fallthroughs...)),
		labeledBreaks: w.budget.normalizeLabeled(
			mergeLabeledStates(thenRes.labeledBreaks, elseRes.labeledBreaks),
		),
		labeledContinues: w.budget.normalizeLabeled(
			mergeLabeledStates(thenRes.labeledContinues, elseRes.labeledContinues),
		),
		returns: append(thenRes.returns, elseRes.returns...),
	}
}

func (w *pathWalker) execElse(stmt ast.Stmt, states []state) flowResult {
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		return w.execBlock(s.List, states)
	case *ast.IfStmt:
		return w.execIfStmt(s, states)
	default:
		return w.execStmt(stmt, states)
	}
}

func (w *pathWalker) execForStmt(stmt *ast.ForStmt, label string, states []state) flowResult {
	states = w.execOptionalStmt(stmt.Init, states)
	if len(states) == 0 {
		return flowResult{}
	}

	enterStates := states
	exitStates := []state(nil)
	writes := loopInvalidations(w.q, stmt.Body.List, stmt.Post)

	if stmt.Cond != nil {
		// The condition runs again after each iteration, so judge it without
		// the facts about what the body and post statement write.
		head := w.budget.normalize(writes.apply(states))
		w.checkBooleanSubexpressions(head, stmt.Cond)

		if tri, because := w.cond.truthAcross(head, stmt.Cond); tri == triFalse {
			w.reportCondition(stmt.Cond, false, because)
		} else if tri == triTrue {
			w.reportCondition(stmt.Cond, true, because)
		}

		enterStates = w.cond.refineStates(states, stmt.Cond, true)
		exitStates = w.cond.refineStates(states, stmt.Cond, false)
	}

	bodyRes := w.execBlock(stmt.Body.List, w.budget.normalize(writes.apply(enterStates)))

	iterStates := w.budget.normalize(append(bodyRes.next, bodyRes.continues...))
	iterStates = append(iterStates, consumeLabeledStates(bodyRes.labeledContinues, label)...)
	iterStates = w.budget.normalize(iterStates)

	if stmt.Post != nil {
		iterStates = w.execStmt(stmt.Post, iterStates).next
	}

	// The loop may execute multiple times. Preserve precision inside one iteration,
	// then widen when approximating the state after additional iterations.
	var afterLoop []state

	afterLoop = append(afterLoop, exitStates...)
	afterLoop = append(afterLoop, bodyRes.breaks...)
	afterLoop = append(afterLoop, consumeLabeledStates(bodyRes.labeledBreaks, label)...)

	if stmt.Cond != nil && len(iterStates) != 0 {
		widened := wipeFacts(iterStates)
		afterLoop = append(afterLoop, w.cond.refineStates(widened, stmt.Cond, false)...)
	}

	return flowResult{
		next:             w.budget.normalize(afterLoop),
		labeledBreaks:    w.budget.normalizeLabeled(bodyRes.labeledBreaks),
		labeledContinues: w.budget.normalizeLabeled(bodyRes.labeledContinues),
		returns:          bodyRes.returns,
	}
}

func (w *pathWalker) execOptionalStmt(stmt ast.Stmt, states []state) []state {
	if stmt != nil {
		states = w.execStmt(stmt, states).next
	}

	return w.budget.normalize(states)
}

func (w *pathWalker) execRangeStmt(
	stmt *ast.RangeStmt,
	label string,
	states []state,
) flowResult {
	states = w.inv.forExprSideEffects(states, stmt.X)
	enterStates := w.budget.normalize(states)
	writes := loopInvalidations(w.q, stmt.Body.List, nil)
	enterStates = w.budget.normalize(writes.apply(enterStates))

	bodyStart := make([]state, 0, len(enterStates))
	for _, st0 := range enterStates {
		st := st0.clone()
		if stmt.Key != nil {
			w.inv.invalidateLHS(&st, stmt.Key)
		}

		if stmt.Value != nil {
			w.inv.invalidateLHS(&st, stmt.Value)
		}

		bodyStart = append(bodyStart, st)
	}

	bodyRes := w.execBlock(stmt.Body.List, bodyStart)
	iterStates := w.budget.normalize(append(bodyRes.next, bodyRes.continues...))
	iterStates = append(iterStates, consumeLabeledStates(bodyRes.labeledContinues, label)...)
	iterStates = w.budget.normalize(writes.apply(iterStates))

	// A range loop may execute zero times, so the incoming state is always an exit state.
	out := append([]state{}, states...)
	out = append(out, bodyRes.breaks...)
	out = append(out, consumeLabeledStates(bodyRes.labeledBreaks, label)...)
	out = append(out, iterStates...)

	return flowResult{
		next:             w.budget.normalize(out),
		labeledBreaks:    w.budget.normalizeLabeled(bodyRes.labeledBreaks),
		labeledContinues: w.budget.normalizeLabeled(bodyRes.labeledContinues),
		returns:          bodyRes.returns,
	}
}

func (w *pathWalker) execSwitchStmt(
	stmt *ast.SwitchStmt,
	label string,
	states []state,
) flowResult {
	states = w.execOptionalStmt(stmt.Init, states)
	if len(states) == 0 {
		return flowResult{}
	}

	remaining := states

	var acc clauseFlow

	carriedFallthrough := make([]state, 0)
	defaultSeen := false

	for _, raw := range stmt.Body.List {
		clause := raw.(*ast.CaseClause)

		caseStates := append([]state{}, carriedFallthrough...)

		switch {
		case len(clause.List) == 0:
			defaultSeen = true

			caseStates = append(caseStates, remaining...)
		case len(remaining) == 0 && len(carriedFallthrough) == 0:
			w.report(
				clause.Case,
				"unreachable_case",
				fmt.Sprintf(
					"switch case %q is unreachable here; previous cases already cover all reachable values",
					w.q.renderCaseClauseHeader(clause),
				),
			)

			continue
		case len(remaining) > 0:
			tri, because := truthAcrossStates(remaining, func(st state) (triState, *evidence) {
				return w.cond.truthCase(st, stmt.Tag, clause.List)
			})
			if tri == triFalse && len(carriedFallthrough) == 0 {
				w.reportCase(clause, because)
			}

			caseStates = append(
				caseStates,
				w.cond.refineStatesCase(remaining, stmt.Tag, clause.List, true)...,
			)
			remaining = w.cond.refineStatesCase(remaining, stmt.Tag, clause.List, false)
		}

		caseRes := w.execBlock(clause.Body, w.budget.normalize(caseStates))
		acc.add(label, caseRes)
		carriedFallthrough = w.budget.normalize(caseRes.fallthroughs)
	}

	if !defaultSeen {
		acc.addNext(remaining)
	}

	return acc.result(w.budget)
}

func (w *pathWalker) execTypeSwitchStmt(
	stmt *ast.TypeSwitchStmt,
	label string,
	states []state,
) flowResult {
	if stmt.Init != nil {
		states = w.execStmt(stmt.Init, states).next
	}

	if stmt.Assign != nil {
		states = w.execStmt(stmt.Assign, states).next
	}

	states = w.budget.normalize(states)
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

		caseRes := w.execBlock(clause.Body, states)
		acc.add(label, caseRes)
	}

	if !defaultSeen {
		acc.addNext(states)
	}

	return acc.result(w.budget)
}

func (w *pathWalker) execSelectStmt(
	stmt *ast.SelectStmt,
	label string,
	states []state,
) flowResult {
	states = w.budget.normalize(states)
	if len(states) == 0 {
		return flowResult{}
	}

	var acc clauseFlow

	for _, raw := range stmt.Body.List {
		clause := raw.(*ast.CommClause)
		clauseStates := states

		if clause.Comm != nil {
			clauseStates = w.execStmt(clause.Comm, clauseStates).next
		}

		caseRes := w.execBlock(clause.Body, clauseStates)
		acc.add(label, caseRes)
	}

	return acc.result(w.budget)
}
