package flow

import (
	"go/ast"
	"go/token"
)

type pathWalker struct {
	q      *exprQuery
	cond   conditionEval
	inv    invalidator
	calls  *callFacts
	budget stateBudget
	report reportFunc
}

func newPathWalker(q *exprQuery, calls *callFacts, report reportFunc) *pathWalker {
	return &pathWalker{
		q:      q,
		cond:   conditionEval{q: q, calls: calls, budget: calls.budget},
		inv:    invalidator{q: q, calls: calls, budget: calls.budget},
		calls:  calls,
		budget: calls.budget,
		report: report,
	}
}

//nolint:cyclop // Statement dispatch intentionally mirrors Go AST forms.
func (w *pathWalker) execStmt(stmt ast.Stmt, states []state) flowResult {
	states = w.budget.normalize(states)

	switch stmt := stmt.(type) {
	case *ast.BlockStmt:
		return w.execBlock(stmt.List, states)
	case *ast.EmptyStmt:
		return flowResult{next: states}
	case *ast.ExprStmt:
		return flowResult{next: w.execExprStmt(stmt, states)}
	case *ast.LabeledStmt:
		return w.execLabeledStmt(stmt, states)
	case *ast.AssignStmt:
		return flowResult{next: w.execAssignStmt(stmt, states)}
	case *ast.DeclStmt:
		return flowResult{next: w.execDeclStmt(stmt, states)}
	case *ast.IncDecStmt:
		return flowResult{next: w.execIncDecStmt(stmt, states)}
	case *ast.ReturnStmt:
		return w.execReturnStmt(stmt, states)
	case *ast.BranchStmt:
		// token.Token includes many non-branch values irrelevant here.
		//exhaustive:ignore
		switch stmt.Tok {
		case token.BREAK:
			if stmt.Label != nil {
				return flowResult{
					labeledBreaks: map[string][]state{stmt.Label.Name: states},
				}
			}

			return flowResult{breaks: states}
		case token.CONTINUE:
			if stmt.Label != nil {
				return flowResult{
					labeledContinues: map[string][]state{stmt.Label.Name: states},
				}
			}

			return flowResult{continues: states}
		case token.FALLTHROUGH:
			return flowResult{fallthroughs: states}
		default:
			return flowResult{}
		}
	case *ast.IfStmt:
		return w.execIfStmt(stmt, states)
	case *ast.ForStmt:
		return w.execForStmt(stmt, "", states)
	case *ast.RangeStmt:
		return w.execRangeStmt(stmt, "", states)
	case *ast.SwitchStmt:
		return w.execSwitchStmt(stmt, "", states)
	case *ast.TypeSwitchStmt:
		return w.execTypeSwitchStmt(stmt, "", states)
	case *ast.SelectStmt:
		return w.execSelectStmt(stmt, "", states)
	case *ast.GoStmt:
		return flowResult{next: w.inv.forCall(states, stmt.Call)}
	case *ast.DeferStmt:
		return flowResult{next: states}
	case *ast.SendStmt:
		return flowResult{next: w.execSendStmt(stmt, states)}
	default:
		// Keep the tool conservative: if we do not model a statement, drop accumulated facts.
		return flowResult{next: wipeFacts(states)}
	}
}

func (w *pathWalker) execLabeledStmt(stmt *ast.LabeledStmt, states []state) flowResult {
	label := stmt.Label.Name

	switch inner := stmt.Stmt.(type) {
	case *ast.ForStmt:
		return w.execForStmt(inner, label, states)
	case *ast.RangeStmt:
		return w.execRangeStmt(inner, label, states)
	case *ast.SwitchStmt:
		return w.execSwitchStmt(inner, label, states)
	case *ast.TypeSwitchStmt:
		return w.execTypeSwitchStmt(inner, label, states)
	case *ast.SelectStmt:
		return w.execSelectStmt(inner, label, states)
	default:
		return w.execStmt(inner, states)
	}
}

func (w *pathWalker) execExprStmt(stmt *ast.ExprStmt, states []state) []state {
	if call, ok := ast.Unparen(stmt.X).(*ast.CallExpr); ok && w.q.callNeverReturns(call) {
		return nil
	}

	return w.inv.forExprSideEffects(states, stmt.X)
}

func (w *pathWalker) execReturnStmt(stmt *ast.ReturnStmt, states []state) flowResult {
	out := make([]returnState, 0, len(states))
	for _, st0 := range states {
		st := st0
		for _, result := range stmt.Results {
			st = w.inv.forExprSideEffectsOne(st, result)
		}

		returns := w.cond.classifyReturnStates(st, stmt.Results)
		out = append(out, returns...)
	}

	return flowResult{returns: out}
}

func (w *pathWalker) execSendStmt(stmt *ast.SendStmt, states []state) []state {
	states = w.inv.forExprSideEffects(states, stmt.Chan)
	states = w.inv.forExprSideEffects(states, stmt.Value)

	return states
}

func (w *pathWalker) execIncDecStmt(stmt *ast.IncDecStmt, states []state) []state {
	states = w.inv.forExprSideEffects(states, stmt.X)
	for i := range states {
		st := states[i].clone()
		w.inv.invalidateLHS(&st, stmt.X)
		states[i] = st
	}

	return w.budget.normalize(states)
}

func (w *pathWalker) execDeclStmt(stmt *ast.DeclStmt, states []state) []state {
	gen, ok := stmt.Decl.(*ast.GenDecl)
	if !ok || gen.Tok != token.VAR {
		return states
	}

	out := make([]state, 0, len(states))
	for _, st0 := range states {
		st := st0.clone()
		reachable := true

		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}

			st, reachable = w.execValueSpec(st, st0, vs)
			if !reachable {
				break
			}
		}

		if reachable {
			out = append(out, st)
		}
	}

	return w.budget.normalize(out)
}

func (w *pathWalker) execValueSpec(st state, source state, spec *ast.ValueSpec) (state, bool) {
	for _, value := range spec.Values {
		st = w.inv.forExprSideEffectsOne(st, value)
	}

	if call, ok := singleCall(spec.Values); ok {
		next, ok := w.calls.applyAlways(st, call)
		if !ok {
			return state{}, false
		}

		st = next

		names := make([]ast.Expr, len(spec.Names))
		for index, name := range spec.Names {
			names[index] = name
		}

		w.bindCallResults(&st, call, names)
	}

	for index, name := range spec.Names {
		if name.Name == "_" {
			continue
		}

		sym, ok := w.q.symbolOf(name)
		if !ok {
			continue
		}

		st.invalidatePrefix(sym.key)

		if len(spec.Values) == len(spec.Names) {
			st = w.assignValue(st, sym, spec.Values[index], source)
			continue
		}

		zero, ok := zeroScalarOfType(sym.typ)
		if !ok {
			continue
		}

		if next, ok := setExact(
			st,
			sym,
			zero,
			evidence{pos: name.Pos(), text: relationText(sym, "==", zero)},
		); ok {
			st = next
		}
	}

	return st, true
}

func singleCall(exprs []ast.Expr) (*ast.CallExpr, bool) {
	if len(exprs) != 1 {
		return nil, false
	}

	call, ok := ast.Unparen(exprs[0]).(*ast.CallExpr)
	if !ok {
		return nil, false
	}

	return call, true
}

func (w *pathWalker) bindCallResults(st *state, call *ast.CallExpr, exprs []ast.Expr) {
	for idx, expr := range exprs {
		sym, ok := w.q.symbolOf(expr)
		if !ok {
			continue
		}

		if binding, ok := w.calls.bindingForCall(call, idx); ok {
			st.bindings[sym.key] = binding
		}
	}
}

func (w *pathWalker) execAssignStmt(stmt *ast.AssignStmt, states []state) []state {
	out := make([]state, 0, len(states))
	for _, st0 := range states {
		st := st0.clone()
		for _, rhs := range stmt.Rhs {
			st = w.inv.forExprSideEffectsOne(st, rhs)
		}

		for _, lhs := range stmt.Lhs {
			w.inv.invalidateLHS(&st, lhs)
		}

		if call, ok := singleCall(stmt.Rhs); ok {
			next, ok := w.calls.applyAlways(st, call)
			if !ok {
				continue
			}

			st = next
			w.bindCallResults(&st, call, stmt.Lhs)
		}

		if len(stmt.Lhs) == len(stmt.Rhs) {
			for i := range stmt.Lhs {
				sym, ok := w.q.symbolOf(stmt.Lhs[i])
				if !ok {
					continue
				}

				st = w.assignValue(st, sym, stmt.Rhs[i], st0)
			}
		}

		out = append(out, st)
	}

	return w.budget.normalize(out)
}

func (w *pathWalker) assignValue(dst state, lhs symbol, rhs ast.Expr, rhsState state) state {
	if rhsSym, ok := w.q.valueSymbolOf(rhs); ok {
		dst.copyFactsFrom(rhsState, lhs, rhsSym)

		if binding, ok := rhsState.bindings[rhsSym.key]; ok {
			dst.bindings[lhs.key] = binding.clone()
		}

		return linkAlias(dst, lhs.key, rhsSym.key)
	}

	if value, ok := w.q.scalarOf(rhs); ok {
		if next, ok := setExact(
			dst,
			lhs,
			value,
			evidence{pos: rhs.Pos(), text: relationText(lhs, "==", value)},
		); ok {
			return next
		}

		return dst
	}

	return dst
}
