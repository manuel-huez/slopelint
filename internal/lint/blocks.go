package lint

import (
	"go/ast"
)

func (l *linter) execBlock(stmts []ast.Stmt, states []state) flowResult {
	res := flowResult{next: l.normalizeStates(states)}
	for _, stmt := range stmts {
		if len(res.next) == 0 {
			break
		}

		step := l.execStmt(stmt, res.next)
		res.next = step.next
		res.breaks = append(res.breaks, step.breaks...)
		res.continues = append(res.continues, step.continues...)
		res.fallthroughs = append(res.fallthroughs, step.fallthroughs...)
		res.labeledBreaks = l.mergeLabeledStates(res.labeledBreaks, step.labeledBreaks)
		res.labeledContinues = l.mergeLabeledStates(res.labeledContinues, step.labeledContinues)
		res.returns = append(res.returns, step.returns...)
	}

	res.next = l.normalizeStates(res.next)
	res.breaks = l.normalizeStates(res.breaks)
	res.continues = l.normalizeStates(res.continues)
	res.fallthroughs = l.normalizeStates(res.fallthroughs)
	res.labeledBreaks = l.normalizeLabeledStates(res.labeledBreaks)
	res.labeledContinues = l.normalizeLabeledStates(res.labeledContinues)

	return res
}

func (l *linter) mergeLabeledStates(dst, src map[string][]state) map[string][]state {
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

func (l *linter) normalizeLabeledStates(in map[string][]state) map[string][]state {
	if len(in) == 0 {
		return nil
	}

	out := make(map[string][]state, len(in))
	for label, states := range in {
		normalized := l.normalizeStates(states)
		if len(normalized) == 0 {
			continue
		}

		out[label] = normalized
	}

	if len(out) == 0 {
		return nil
	}

	return out
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

func (flow *clauseFlow) add(l *linter, label string, res flowResult) {
	flow.next = append(flow.next, consumeLabeledStates(res.labeledBreaks, label)...)
	flow.next = append(flow.next, res.next...)
	flow.next = append(flow.next, res.breaks...)
	flow.continues = append(flow.continues, res.continues...)
	flow.returns = append(flow.returns, res.returns...)
	flow.labeledBreaks = l.mergeLabeledStates(flow.labeledBreaks, res.labeledBreaks)
	flow.labeledContinues = l.mergeLabeledStates(flow.labeledContinues, res.labeledContinues)
}

func (flow clauseFlow) result(l *linter) flowResult {
	return flowResult{
		next:             l.normalizeStates(flow.next),
		continues:        l.normalizeStates(flow.continues),
		labeledBreaks:    l.normalizeLabeledStates(flow.labeledBreaks),
		labeledContinues: l.normalizeLabeledStates(flow.labeledContinues),
		returns:          flow.returns,
	}
}
