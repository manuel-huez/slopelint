package lint

import (
	"go/ast"
)

func (w *pathWalker) execBlock(stmts []ast.Stmt, states []state) flowResult {
	res := flowResult{next: w.budget.normalize(states)}
	for _, stmt := range stmts {
		if len(res.next) == 0 {
			break
		}

		step := w.execStmt(stmt, res.next)
		res.next = step.next
		res.breaks = append(res.breaks, step.breaks...)
		res.continues = append(res.continues, step.continues...)
		res.fallthroughs = append(res.fallthroughs, step.fallthroughs...)
		res.labeledBreaks = mergeLabeledStates(res.labeledBreaks, step.labeledBreaks)
		res.labeledContinues = mergeLabeledStates(res.labeledContinues, step.labeledContinues)
		res.returns = append(res.returns, step.returns...)
	}

	res.next = w.budget.normalize(res.next)
	res.breaks = w.budget.normalize(res.breaks)
	res.continues = w.budget.normalize(res.continues)
	res.fallthroughs = w.budget.normalize(res.fallthroughs)
	res.labeledBreaks = w.budget.normalizeLabeled(res.labeledBreaks)
	res.labeledContinues = w.budget.normalizeLabeled(res.labeledContinues)

	return res
}

func mergeLabeledStates(dst, src map[string][]state) map[string][]state {
	if len(src) == 0 {
		return dst
	}

	if dst == nil {
		dst = make(map[string][]state, len(src))
	}

	for label, states := range src {
		dst[label] = append(dst[label], states...)
	}

	return dst
}

func consumeLabeledStates(in map[string][]state, label string) []state {
	if len(in) == 0 || label == "" {
		return nil
	}

	states := in[label]
	delete(in, label)

	return states
}

type clauseFlow struct {
	next             []state
	continues        []state
	returns          []returnState
	labeledBreaks    map[string][]state
	labeledContinues map[string][]state
}

func (flow *clauseFlow) add(label string, res flowResult) {
	flow.next = append(flow.next, consumeLabeledStates(res.labeledBreaks, label)...)
	flow.next = append(flow.next, res.next...)
	flow.next = append(flow.next, res.breaks...)
	flow.continues = append(flow.continues, res.continues...)
	flow.returns = append(flow.returns, res.returns...)
	flow.labeledBreaks = mergeLabeledStates(flow.labeledBreaks, res.labeledBreaks)
	flow.labeledContinues = mergeLabeledStates(flow.labeledContinues, res.labeledContinues)
}

func (flow clauseFlow) result(budget stateBudget) flowResult {
	return flowResult{
		next:             budget.normalize(flow.next),
		continues:        budget.normalize(flow.continues),
		labeledBreaks:    budget.normalizeLabeled(flow.labeledBreaks),
		labeledContinues: budget.normalizeLabeled(flow.labeledContinues),
		returns:          flow.returns,
	}
}
