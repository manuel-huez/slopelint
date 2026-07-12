package lint

import (
	"go/ast"
	"go/token"
	"go/types"
)

//nolint:cyclop // Statement dispatch intentionally mirrors Go AST forms.
func (l *linter) execStmt(stmt ast.Stmt, states []state) flowResult {
	states = l.normalizeStates(states)

	switch stmt := stmt.(type) {
	case *ast.BlockStmt:
		return l.execBlock(stmt.List, states)
	case *ast.EmptyStmt:
		return flowResult{next: states}
	case *ast.ExprStmt:
		return flowResult{next: l.execExprStmt(stmt, states)}
	case *ast.LabeledStmt:
		return l.execLabeledStmt(stmt, states)
	case *ast.AssignStmt:
		return flowResult{next: l.execAssignStmt(stmt, states)}
	case *ast.DeclStmt:
		return flowResult{next: l.execDeclStmt(stmt, states)}
	case *ast.IncDecStmt:
		return flowResult{next: l.execIncDecStmt(stmt, states)}
	case *ast.ReturnStmt:
		return l.execReturnStmt(stmt, states)
	case *ast.BranchStmt:
		//exhaustive:ignore token.Token includes many non-branch values irrelevant here.
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
		return l.execIfStmt(stmt, states)
	case *ast.ForStmt:
		return l.execForStmt(stmt, states)
	case *ast.RangeStmt:
		return l.execRangeStmt(stmt, states)
	case *ast.SwitchStmt:
		return l.execSwitchStmt(stmt, states)
	case *ast.TypeSwitchStmt:
		return l.execTypeSwitchStmt(stmt, states)
	case *ast.SelectStmt:
		return l.execSelectStmt(stmt, states)
	case *ast.GoStmt:
		return flowResult{next: l.execGoStmt(stmt, states)}
	case *ast.DeferStmt:
		return flowResult{next: states}
	case *ast.SendStmt:
		return flowResult{next: l.execSendStmt(stmt, states)}
	default:
		// Keep the tool conservative: if we do not model a statement, drop accumulated facts.
		return flowResult{next: l.wipeFacts(states)}
	}
}

func (l *linter) execLabeledStmt(stmt *ast.LabeledStmt, states []state) flowResult {
	label := stmt.Label.Name

	switch inner := stmt.Stmt.(type) {
	case *ast.ForStmt:
		return l.execForStmtWithLabel(inner, label, states)
	case *ast.RangeStmt:
		return l.execRangeStmtWithLabel(inner, label, states)
	case *ast.SwitchStmt:
		return l.execSwitchStmtWithLabel(inner, label, states)
	case *ast.TypeSwitchStmt:
		return l.execTypeSwitchStmtWithLabel(inner, label, states)
	case *ast.SelectStmt:
		return l.execSelectStmtWithLabel(inner, label, states)
	default:
		return l.execStmt(inner, states)
	}
}

func (l *linter) execExprStmt(stmt *ast.ExprStmt, states []state) []state {
	if call, ok := l.unparen(stmt.X).(*ast.CallExpr); ok && l.callNeverReturns(call) {
		l.invalidateForExprSideEffects(states, stmt.X)
		return nil
	}

	return l.invalidateForExprSideEffects(states, stmt.X)
}

func (l *linter) execGoStmt(stmt *ast.GoStmt, states []state) []state {
	return l.invalidateForCall(states, stmt.Call)
}

func (l *linter) callNeverReturns(call *ast.CallExpr) bool {
	id, ok := l.unparen(call.Fun).(*ast.Ident)
	if !ok {
		return false
	}

	obj, ok := l.pkg.TypesInfo.ObjectOf(id).(*types.Builtin)
	if !ok || obj == nil {
		return false
	}

	return obj.Name() == panicText
}

func (l *linter) execReturnStmt(stmt *ast.ReturnStmt, states []state) flowResult {
	out := make([]returnState, 0, len(states))
	for _, st0 := range states {
		st := st0
		for _, result := range stmt.Results {
			st = l.invalidateForExprSideEffectsOne(st, result)
		}

		returns := l.classifyReturnStates(st, stmt.Results)
		out = append(out, returns...)
	}

	return flowResult{returns: out}
}

func (l *linter) execSendStmt(stmt *ast.SendStmt, states []state) []state {
	states = l.invalidateForExprSideEffects(states, stmt.Chan)
	states = l.invalidateForExprSideEffects(states, stmt.Value)

	return states
}

func (l *linter) execIncDecStmt(stmt *ast.IncDecStmt, states []state) []state {
	states = l.invalidateForExprSideEffects(states, stmt.X)
	for i := range states {
		st := states[i].clone()
		l.invalidateLHS(&st, stmt.X)
		states[i] = st
	}

	return l.normalizeStates(states)
}

func (l *linter) execDeclStmt(stmt *ast.DeclStmt, states []state) []state {
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

			st, reachable = l.execValueSpec(st, st0, vs)
			if !reachable {
				break
			}
		}

		if reachable {
			out = append(out, st)
		}
	}

	return l.normalizeStates(out)
}

func (l *linter) execValueSpec(st state, source state, spec *ast.ValueSpec) (state, bool) {
	for _, value := range spec.Values {
		st = l.invalidateForExprSideEffectsOne(st, value)
	}

	if call, ok := l.singleCall(spec.Values); ok {
		next, ok := l.applyCallEffects(st, call)
		if !ok {
			return state{}, false
		}

		st = next
		l.bindCallResultsToIdents(&st, call, spec.Names)
	}

	for index, name := range spec.Names {
		if name.Name == "_" {
			continue
		}

		sym, ok := l.symbolOf(name)
		if !ok {
			continue
		}

		l.invalidatePrefix(&st, sym.key)

		if len(spec.Values) == len(spec.Names) {
			st = l.assignValue(st, sym, spec.Values[index], source)
			continue
		}

		zero, ok := zeroScalarOfType(sym.typ)
		if !ok {
			continue
		}

		if next, ok := l.setExact(
			st,
			sym,
			zero,
			evidence{pos: name.Pos(), text: l.relationText(sym, "==", zero)},
		); ok {
			st = next
		}
	}

	return st, true
}

func (l *linter) singleCall(exprs []ast.Expr) (*ast.CallExpr, bool) {
	if len(exprs) != 1 {
		return nil, false
	}

	call, ok := l.unparen(exprs[0]).(*ast.CallExpr)
	if !ok {
		return nil, false
	}

	return call, true
}

func (l *linter) applyCallEffects(st state, call *ast.CallExpr) (state, bool) {
	nextStates := l.applySummaryContracts(st, call, l.summaryForCall(call).always)
	if len(nextStates) == 0 {
		return state{}, false
	}

	return nextStates[0], true
}

func (l *linter) bindCallResultsToExprs(st *state, call *ast.CallExpr, exprs []ast.Expr) {
	for idx, expr := range exprs {
		sym, ok := l.symbolOf(expr)
		if !ok {
			continue
		}

		if binding, ok := l.instantiateBindingForCall(call, idx); ok {
			st.bindings[sym.key] = binding
		}
	}
}

func (l *linter) bindCallResultsToIdents(st *state, call *ast.CallExpr, names []*ast.Ident) {
	for idx, name := range names {
		sym, ok := l.symbolOf(name)
		if !ok {
			continue
		}

		if binding, ok := l.instantiateBindingForCall(call, idx); ok {
			st.bindings[sym.key] = binding
		}
	}
}

func (l *linter) execAssignStmt(stmt *ast.AssignStmt, states []state) []state {
	out := make([]state, 0, len(states))
	for _, st0 := range states {
		st := st0.clone()
		for _, rhs := range stmt.Rhs {
			st = l.invalidateForExprSideEffectsOne(st, rhs)
		}

		for _, lhs := range stmt.Lhs {
			l.invalidateLHS(&st, lhs)
		}

		if call, ok := l.singleCall(stmt.Rhs); ok {
			next, ok := l.applyCallEffects(st, call)
			if !ok {
				continue
			}

			st = next
			l.bindCallResultsToExprs(&st, call, stmt.Lhs)
		}

		if len(stmt.Lhs) == len(stmt.Rhs) {
			for i := range stmt.Lhs {
				sym, ok := l.symbolOf(stmt.Lhs[i])
				if !ok {
					continue
				}

				st = l.assignValue(st, sym, stmt.Rhs[i], st0)
			}
		}

		out = append(out, st)
	}

	return l.normalizeStates(out)
}

func (l *linter) assignValue(dst state, lhs symbol, rhs ast.Expr, rhsState state) state {
	if rhsSym, ok := l.valueSymbolOf(rhs); ok {
		l.copyFacts(&dst, lhs, rhsSym, rhsState)

		if binding, ok := rhsState.bindings[rhsSym.key]; ok {
			dst.bindings[lhs.key] = binding.clone()
		}

		return linkAlias(dst, lhs.key, rhsSym.key)
	}

	if value, ok := l.scalarOf(rhs); ok {
		if next, ok := l.setExact(
			dst,
			lhs,
			value,
			evidence{pos: rhs.Pos(), text: l.relationText(lhs, "==", value)},
		); ok {
			return next
		}

		return dst
	}

	return dst
}
