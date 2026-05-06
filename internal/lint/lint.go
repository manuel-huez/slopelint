package lint

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"strconv"
	"strings"
)

const (
	boolTrueText  = "true"
	boolFalseText = "false"
	zeroIntText   = "0"
)

// Options controls linter behavior.
type Options struct {
	MaxStates    int
	CacheEnabled bool
	CacheDir     string
}

type linter struct {
	pkg             *LoadedPackage
	maxStates       int
	issues          []Issue
	reported        map[string]struct{}
	suppressReports int
	renderCache     map[ast.Node]string
	explicitFacts   map[string][]guardContract
	inferredFacts   map[string]callSummary
	localFuncLits   map[types.Object]*ast.FuncLit
	externalSummary func(*types.Func) (callSummary, bool)
}

type flowResult struct {
	next             []state
	breaks           []state
	continues        []state
	fallthroughs     []state
	labeledBreaks    map[string][]state
	labeledContinues map[string][]state
	returns          []returnState
}

type returnKind uint8

const (
	returnUnspecified returnKind = iota
	returnBoolTrue
	returnBoolFalse
	returnNil
	returnNonNil
)

type returnState struct {
	state state
	kinds map[int]returnKind
}

func newLinter(pkg *LoadedPackage, opts Options) *linter {
	if opts.MaxStates <= 0 {
		opts.MaxStates = 32
	}

	return &linter{
		pkg:           pkg,
		maxStates:     opts.MaxStates,
		reported:      make(map[string]struct{}),
		issues:        make([]Issue, 0),
		renderCache:   make(map[ast.Node]string),
		explicitFacts: make(map[string][]guardContract),
		inferredFacts: make(map[string]callSummary),
	}
}

func (ret returnState) kindAt(index int) returnKind {
	if ret.kinds == nil {
		return returnUnspecified
	}

	kind, ok := ret.kinds[index]
	if !ok {
		return returnUnspecified
	}

	return kind
}

func cloneReturnKinds(in map[int]returnKind) map[int]returnKind {
	if len(in) == 0 {
		return nil
	}

	out := make(map[int]returnKind, len(in))
	for index, kind := range in {
		out[index] = kind
	}

	return out
}

func (l *linter) run() {
	l.collectContracts()
	l.collectLocalFuncLits()
	l.inferCallSummaries()
	l.analyzeFiles()
}

func (l *linter) analyzeFiles() {
	for _, file := range l.pkg.Files {
		ast.Inspect(file, func(n ast.Node) bool {
			switch fn := n.(type) {
			case *ast.FuncDecl:
				if fn.Body != nil {
					l.analyzeFunction(fn.Body)
				}

				return false
			case *ast.FuncLit:
				l.analyzeFunction(fn.Body)
				return false
			default:
				return true
			}
		})
	}

	l.scanDefaultSmells()
	l.scanPackageSmells()
}

func (l *linter) analyzeFunction(body *ast.BlockStmt) {
	if body == nil {
		return
	}

	if l.hasUnsupportedJumps(body) {
		return
	}

	l.scanStructuralBlock(body.List)
	l.execBlock(body.List, []state{newState()})
}

func (l *linter) hasUnsupportedJumps(body *ast.BlockStmt) bool {
	unsupported := false

	ast.Inspect(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.BranchStmt:
			if n.Tok == token.GOTO {
				unsupported = true
				return false
			}
		}

		return !unsupported
	})

	return unsupported
}

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

	return obj.Name() == "panic"
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

//nolint:cyclop,gocognit // Var declaration handling needs explicit step-wise flow for precision.
func (l *linter) execDeclStmt(stmt *ast.DeclStmt, states []state) []state {
	gen, ok := stmt.Decl.(*ast.GenDecl)
	if !ok || gen.Tok != token.VAR {
		return states
	}

	out := make([]state, 0, len(states))
	for _, st0 := range states {
		st := st0.clone()

		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}

			for _, value := range vs.Values {
				st = l.invalidateForExprSideEffectsOne(st, value)
			}

			if call, ok := l.singleCall(vs.Values); ok {
				next, ok := l.applyCallEffects(st, call)
				if !ok {
					continue
				}

				st = next
				l.bindCallResultsToIdents(&st, call, vs.Names)
			}

			for idx, name := range vs.Names {
				if name.Name == "_" {
					continue
				}

				sym, ok := l.symbolOf(name)
				if !ok {
					continue
				}

				l.invalidatePrefix(&st, sym.key)

				if len(vs.Values) == len(vs.Names) {
					st = l.assignValue(st, sym, vs.Values[idx], st0)
					continue
				}

				if zero, ok := zeroScalarOfType(sym.typ); ok {
					if next, ok := l.setExact(
						st,
						sym,
						zero,
						evidence{pos: name.Pos(), text: l.relationText(sym, "==", zero)},
					); ok {
						st = next
					}
				}
			}
		}

		out = append(out, st)
	}

	return l.normalizeStates(out)
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

//nolint:cyclop,gocognit // Assignment flow needs explicit invalidation and tuple handling for precision.
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

func (l *linter) execIfStmt(stmt *ast.IfStmt, states []state) flowResult {
	if stmt.Init != nil {
		states = l.execStmt(stmt.Init, states).next
	}

	states = l.normalizeStates(states)
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
	if stmt.Init != nil {
		states = l.execStmt(stmt.Init, states).next
	}

	states = l.normalizeStates(states)
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
	out := make([]state, 0)
	conts := make([]state, 0)
	returns := make([]returnState, 0)
	carriedFallthrough := make([]state, 0)
	defaultSeen := false

	var (
		labeledBreaks    map[string][]state
		labeledContinues map[string][]state
	)

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
		out = append(out, consumeLabeledStates(caseRes.labeledBreaks, label)...)
		out = append(out, caseRes.next...)
		out = append(out, caseRes.breaks...)
		conts = append(conts, caseRes.continues...)
		returns = append(returns, caseRes.returns...)
		labeledBreaks = l.mergeLabeledStates(labeledBreaks, caseRes.labeledBreaks)
		labeledContinues = l.mergeLabeledStates(labeledContinues, caseRes.labeledContinues)
		carriedFallthrough = l.normalizeStates(caseRes.fallthroughs)
	}

	if !defaultSeen {
		out = append(out, remaining...)
	}

	return flowResult{
		next:             l.normalizeStates(out),
		continues:        l.normalizeStates(conts),
		labeledBreaks:    l.normalizeLabeledStates(labeledBreaks),
		labeledContinues: l.normalizeLabeledStates(labeledContinues),
		returns:          returns,
	}
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

	out := make([]state, 0)
	conts := make([]state, 0)
	returns := make([]returnState, 0)
	defaultSeen := false

	var (
		labeledBreaks    map[string][]state
		labeledContinues map[string][]state
	)

	for _, raw := range stmt.Body.List {
		clause := raw.(*ast.CaseClause)
		if len(clause.List) == 0 {
			defaultSeen = true
		}

		caseRes := l.execBlock(clause.Body, states)
		out = append(out, consumeLabeledStates(caseRes.labeledBreaks, label)...)
		out = append(out, caseRes.next...)
		out = append(out, caseRes.breaks...)
		conts = append(conts, caseRes.continues...)
		returns = append(returns, caseRes.returns...)
		labeledBreaks = l.mergeLabeledStates(labeledBreaks, caseRes.labeledBreaks)
		labeledContinues = l.mergeLabeledStates(labeledContinues, caseRes.labeledContinues)
	}

	if !defaultSeen {
		out = append(out, states...)
	}

	return flowResult{
		next:             l.normalizeStates(out),
		continues:        l.normalizeStates(conts),
		labeledBreaks:    l.normalizeLabeledStates(labeledBreaks),
		labeledContinues: l.normalizeLabeledStates(labeledContinues),
		returns:          returns,
	}
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

	out := make([]state, 0)
	conts := make([]state, 0)
	returns := make([]returnState, 0)

	var (
		labeledBreaks    map[string][]state
		labeledContinues map[string][]state
	)

	for _, raw := range stmt.Body.List {
		clause := raw.(*ast.CommClause)
		clauseStates := states

		if clause.Comm != nil {
			clauseStates = l.execStmt(clause.Comm, clauseStates).next
		}

		caseRes := l.execBlock(clause.Body, clauseStates)
		out = append(out, consumeLabeledStates(caseRes.labeledBreaks, label)...)
		out = append(out, caseRes.next...)
		out = append(out, caseRes.breaks...)
		conts = append(conts, caseRes.continues...)
		returns = append(returns, caseRes.returns...)
		labeledBreaks = l.mergeLabeledStates(labeledBreaks, caseRes.labeledBreaks)
		labeledContinues = l.mergeLabeledStates(labeledContinues, caseRes.labeledContinues)
	}

	return flowResult{
		next:             l.normalizeStates(out),
		continues:        l.normalizeStates(conts),
		labeledBreaks:    l.normalizeLabeledStates(labeledBreaks),
		labeledContinues: l.normalizeLabeledStates(labeledContinues),
		returns:          returns,
	}
}

func (l *linter) invalidateLHS(st *state, lhs ast.Expr) {
	if sym, ok := l.symbolOf(lhs); ok {
		l.invalidatePrefix(st, sym.key)
		return
	}

	for root := range l.rootsInExpr(lhs) {
		l.invalidatePrefix(st, root)
	}
}

func (l *linter) invalidatePrefix(st *state, prefix string) {
	for key := range st.facts {
		if isSameOrChild(key, prefix) || predicateFactInvalidatedByPrefix(key, prefix) {
			delete(st.facts, key)
		}
	}

	removeAliasPrefix(st, prefix, false)
	removeBindingsForPrefix(st, prefix)
}

func (l *linter) invalidateDescendants(st *state, prefix string) {
	for key := range st.facts {
		if key != prefix &&
			(isSameOrChild(key, prefix) || predicateFactInvalidatedByPrefix(key, prefix)) {
			delete(st.facts, key)
		}
	}

	removeAliasPrefix(st, prefix, true)
	removeBindingsForPrefix(st, prefix)
}

func predicateFactInvalidatedByPrefix(key string, prefix string) bool {
	if !strings.Contains(key, "|"+predicatePathSegmentPrefix) {
		return false
	}

	root, _, ok := strings.Cut(prefix, "|")
	if !ok {
		return false
	}

	return strings.HasPrefix(key, root+"|"+predicatePathSegmentPrefix)
}

func (l *linter) wipeFacts(states []state) []state {
	if len(states) == 0 {
		return nil
	}

	out := make([]state, len(states))
	for i := range out {
		out[i] = newState()
	}

	return out
}

func (l *linter) invalidateForExprSideEffects(states []state, expr ast.Expr) []state {
	out := make([]state, 0, len(states))
	for _, st := range states {
		out = append(out, l.invalidateForExprSideEffectsOne(st, expr))
	}

	return l.normalizeStates(out)
}

func (l *linter) invalidateForExprSideEffectsOne(st state, expr ast.Expr) state {
	out := st.clone()

	ast.Inspect(expr, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.CallExpr:
			out = l.invalidateForCallOne(out, n)
		}

		return true
	})

	return out
}

func (l *linter) invalidateForCall(states []state, call *ast.CallExpr) []state {
	out := make([]state, 0, len(states))
	for _, st := range states {
		out = append(out, l.invalidateForCallOne(st, call))
	}

	return l.normalizeStates(out)
}

func (l *linter) invalidateForFuncLitEffectsSeen(
	st state,
	lit *ast.FuncLit,
	seen map[*ast.FuncLit]struct{},
) state {
	if lit == nil || lit.Body == nil {
		return st
	}

	if _, ok := seen[lit]; ok {
		return st
	}

	seen[lit] = struct{}{}
	out := st.clone()

	ast.Inspect(lit.Body, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}

		if !l.invalidateForFuncLitNode(&out, n, seen) {
			return false
		}

		return true
	})

	return out
}

func (l *linter) invalidateForFuncLitNode(
	out *state,
	node ast.Node,
	seen map[*ast.FuncLit]struct{},
) bool {
	switch n := node.(type) {
	case *ast.AssignStmt:
		for _, lhs := range n.Lhs {
			l.invalidateLHS(out, lhs)
		}
	case *ast.IncDecStmt:
		l.invalidateLHS(out, n.X)
	case *ast.RangeStmt:
		l.invalidateRangeLoopTargets(out, n)
	case *ast.CallExpr:
		*out = l.invalidateForCallOneSeen(*out, n, seen)
	case *ast.SendStmt:
		*out = l.invalidateForExprSideEffectsOne(*out, n.Chan)
		*out = l.invalidateForExprSideEffectsOne(*out, n.Value)
	}

	return true
}

func (l *linter) invalidateRangeLoopTargets(out *state, stmt *ast.RangeStmt) {
	if stmt.Key != nil {
		l.invalidateLHS(out, stmt.Key)
	}

	if stmt.Value != nil {
		l.invalidateLHS(out, stmt.Value)
	}
}

//nolint:cyclop,gocognit // Side-effect invalidation must branch by call shape and argument semantics.
func (l *linter) invalidateForCallOne(st state, call *ast.CallExpr) state {
	return l.invalidateForCallOneSeen(st, call, make(map[*ast.FuncLit]struct{}))
}

//nolint:cyclop,gocognit // Side-effect invalidation must branch by call shape and argument semantics.
func (l *linter) invalidateForCallOneSeen(
	st state,
	call *ast.CallExpr,
	seen map[*ast.FuncLit]struct{},
) state {
	if tv, ok := l.pkg.TypesInfo.Types[call.Fun]; ok && tv.IsType() {
		return st
	}

	out := st.clone()
	addFull := func(expr ast.Expr) {
		for root := range l.rootsInExpr(expr) {
			l.invalidatePrefix(&out, root)
		}
	}
	addDescendants := func(expr ast.Expr) {
		for root := range l.rootsInExpr(expr) {
			l.invalidateDescendants(&out, root)
		}
	}

	if lit, ok := l.unparen(call.Fun).(*ast.FuncLit); ok {
		out = l.invalidateForFuncLitEffectsSeen(out, lit, seen)
	} else if lit, ok := l.localFuncLitForExpr(call.Fun); ok {
		out = l.invalidateForFuncLitEffectsSeen(out, lit, seen)
	}

	if sel, ok := l.unparen(call.Fun).(*ast.SelectorExpr); ok {
		if s := l.pkg.TypesInfo.Selections[sel]; s != nil {
			if s.Kind() == types.MethodVal && isPointerLike(s.Recv()) {
				addDescendants(sel.X)
			}
		}
	}

	for _, arg := range call.Args {
		if lit, ok := l.unparen(arg).(*ast.FuncLit); ok {
			out = l.invalidateForFuncLitEffectsSeen(out, lit, seen)
			continue
		}

		if u, ok := l.unparen(arg).(*ast.UnaryExpr); ok && u.Op == token.AND {
			addFull(u.X)
			continue
		}

		tv, ok := l.pkg.TypesInfo.Types[arg]
		if !ok {
			continue
		}

		if isPointerLike(tv.Type) {
			addDescendants(arg)
		}
	}

	// Handle mutating built-ins with simple heuristics.
	if id, ok := l.unparen(call.Fun).(*ast.Ident); ok {
		if obj, ok := l.pkg.TypesInfo.ObjectOf(id).(*types.Builtin); ok {
			switch obj.Name() {
			case "copy", "clear", "delete", "append", "close":
				for _, arg := range call.Args {
					addDescendants(arg)
				}
			}
		}
	}

	summary := l.summaryForCall(call)

	nextStates := l.applySummaryContracts(out, call, summary.always)
	if len(nextStates) == 0 {
		return newState()
	}

	return nextStates[0]
}

func (l *linter) rootsInExpr(expr ast.Expr) map[string]struct{} {
	roots := make(map[string]struct{})

	ast.Inspect(expr, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case ast.Expr:
			if sym, ok := l.symbolOf(n); ok {
				roots[sym.root] = struct{}{}
			}
		}

		return true
	})

	return roots
}

func (l *linter) copyFacts(dst *state, lhs, rhs symbol, src state) {
	for key, f := range src.facts {
		if key == rhs.key || isSameOrChild(key, rhs.key) {
			newKey := lhs.key + strings.TrimPrefix(key, rhs.key)
			dst.facts[newKey] = f.clone()
		}
	}
}

func (l *linter) normalizeStates(states []state) []state {
	if len(states) == 0 {
		return nil
	}

	seen := make(map[string]state, len(states))
	for _, st := range states {
		seen[st.hash()] = st
	}

	out := make([]state, 0, len(seen))
	for _, st := range seen {
		out = append(out, st)
	}

	if len(out) <= l.maxStates {
		return out
	}

	return []state{l.mergeStates(out)}
}

func (l *linter) mergeStates(states []state) state {
	if len(states) == 0 {
		return newState()
	}

	out := states[0].clone()
	for key, f := range out.facts {
		merged := f.clone()

		for i := 1; i < len(states); i++ {
			other, ok := states[i].facts[key]
			if !ok {
				merged = fact{}
				break
			}

			merged = l.joinFacts(merged, other)
			if merged.empty() {
				break
			}
		}

		if merged.empty() {
			delete(out.facts, key)
		} else {
			out.facts[key] = merged
		}
	}

	out.aliases = intersectAliases(states)
	out.bindings = l.intersectBindings(states)

	return out
}

func (l *linter) joinFacts(a, b fact) fact {
	out := fact{}

	if a.exact != nil && b.exact != nil && a.exact.value == b.exact.value {
		copyExact := *a.exact
		out.exact = &copyExact
	}

	if len(a.not) != 0 && len(b.not) != 0 {
		out.not = make(map[string]evidence)
		for k, ev := range a.not {
			if _, ok := b.not[k]; ok {
				out.not[k] = ev
			}
		}

		if len(out.not) == 0 {
			out.not = nil
		}
	}

	return out
}

func (l *linter) intersectBindings(states []state) map[string]resultBinding {
	if len(states) == 0 {
		return nil
	}

	out := make(map[string]resultBinding)

	for key, binding := range states[0].bindings {
		hash := bindingHash(binding)
		same := true

		for i := 1; i < len(states); i++ {
			other, ok := states[i].bindings[key]
			if !ok || bindingHash(other) != hash {
				same = false
				break
			}
		}

		if same {
			out[key] = binding.clone()
		}
	}

	if len(out) == 0 {
		return nil
	}

	return out
}

func (l *linter) truthAcross(states []state, expr ast.Expr) (triState, *evidence) {
	if len(states) == 0 {
		return triUnknown, nil
	}

	allTrue := true
	allFalse := true

	var because *evidence

	for _, st := range states {
		tri, ev := l.truth(st, expr)
		if tri != triTrue {
			allTrue = false
		}

		if tri != triFalse {
			allFalse = false
		}

		if because == nil && ev != nil {
			copyEv := *ev
			because = &copyEv
		}
	}

	if allTrue {
		return triTrue, because
	}

	if allFalse {
		return triFalse, because
	}

	return triUnknown, nil
}

//nolint:cyclop,gocognit,nestif // Boolean reasoning is intentionally explicit for analyzer correctness.
func (l *linter) truth(st state, expr ast.Expr) (triState, *evidence) {
	expr = l.unparen(expr)
	if scalar, ok := l.scalarOf(expr); ok && scalar.kind == scalarBool {
		if scalar.text == boolTrueText {
			return triTrue, nil
		}

		return triFalse, nil
	}

	if tv, ok := l.pkg.TypesInfo.Types[expr]; ok && tv.Value != nil {
		if scalar, ok := scalarFromConstantValue(tv.Value); ok && scalar.kind == scalarBool {
			if scalar.text == boolTrueText {
				return triTrue, nil
			}

			return triFalse, nil
		}
	}

	switch expr := expr.(type) {
	case *ast.Ident, *ast.SelectorExpr:
		if sym, ok := l.symbolOf(expr); ok && isBoolType(sym.typ) {
			if f, ok := st.facts[sym.key]; ok {
				if f.exact != nil && f.exact.value.kind == scalarBool {
					if f.exact.value.text == boolTrueText {
						copyWhy := f.exact.why
						return triTrue, &copyWhy
					}

					copyWhy := f.exact.why

					return triFalse, &copyWhy
				}
			}
		}
	case *ast.CallExpr:
		if sym, ok := l.predicateCallSymbolOf(expr); ok {
			return l.truthSymbolScalar(
				st,
				sym,
				scalar{kind: scalarBool, text: boolTrueText},
				true,
			)
		}
	case *ast.UnaryExpr:
		if expr.Op == token.NOT {
			tri, ev := l.truth(st, expr.X)
			//exhaustive:ignore triState unknown falls through to conservative unknown.
			switch tri {
			case triTrue:
				return triFalse, ev
			case triFalse:
				return triTrue, ev
			default:
				return triUnknown, nil
			}
		}
	case *ast.BinaryExpr:
		//exhaustive:ignore token.Token includes operators not meaningful for this expression type.
		switch expr.Op {
		case token.LAND:
			left, leftEv := l.truth(st, expr.X)
			if left == triFalse {
				return triFalse, leftEv
			}

			right, rightEv := l.truth(st, expr.Y)
			if right == triFalse {
				return triFalse, rightEv
			}

			if left == triTrue && right == triTrue {
				if leftEv != nil {
					return triTrue, leftEv
				}

				return triTrue, rightEv
			}

			return triUnknown, nil
		case token.LOR:
			left, leftEv := l.truth(st, expr.X)
			if left == triTrue {
				return triTrue, leftEv
			}

			right, rightEv := l.truth(st, expr.Y)
			if right == triTrue {
				return triTrue, rightEv
			}

			if left == triFalse && right == triFalse {
				if leftEv != nil {
					return triFalse, leftEv
				}

				return triFalse, rightEv
			}

			return triUnknown, nil
		case token.EQL, token.NEQ:
			return l.truthCompare(st, expr.X, expr.Y, expr.Op == token.EQL)
		case token.GTR, token.GEQ, token.LSS, token.LEQ:
			return l.truthOrderedCompare(st, expr.X, expr.Y, expr.Op)
		}
	}

	return triUnknown, nil
}

func (l *linter) truthCompare(st state, lhs, rhs ast.Expr, wantEq bool) (triState, *evidence) {
	if sym, scalar, ok := l.symbolScalar(lhs, rhs); ok {
		return l.truthSymbolScalar(st, sym, scalar, wantEq)
	}

	if sym, scalar, ok := l.symbolScalar(rhs, lhs); ok {
		return l.truthSymbolScalar(st, sym, scalar, wantEq)
	}

	if ls, ok := l.scalarOf(lhs); ok {
		if rs, ok := l.scalarOf(rhs); ok {
			equal := ls == rs
			if wantEq == equal {
				return triTrue, nil
			}

			return triFalse, nil
		}
	}

	return triUnknown, nil
}

func (l *linter) truthOrderedCompare(
	st state,
	lhs, rhs ast.Expr,
	op token.Token,
) (triState, *evidence) {
	sym, scalar, ok := l.symbolScalar(lhs, rhs)
	if ok {
		return l.truthSymbolOrdered(st, sym, scalar, op)
	}

	sym, scalar, ok = l.symbolScalar(rhs, lhs)
	if !ok {
		return triUnknown, nil
	}

	return l.truthSymbolOrdered(st, sym, scalar, reverseOrderedOp(op))
}

func (l *linter) truthSymbolScalar(
	st state,
	sym symbol,
	value scalar,
	wantEq bool,
) (triState, *evidence) {
	f, ok := st.facts[sym.key]
	if !ok {
		return triUnknown, nil
	}

	if f.exact != nil {
		equal := f.exact.value == value

		copyWhy := f.exact.why
		if wantEq == equal {
			return triTrue, &copyWhy
		}

		return triFalse, &copyWhy
	}

	if ev, ok := f.not[value.key()]; ok {
		copyWhy := ev
		if wantEq {
			return triFalse, &copyWhy
		}

		return triTrue, &copyWhy
	}

	return triUnknown, nil
}

func (l *linter) truthSymbolOrdered(
	st state,
	sym symbol,
	value scalar,
	op token.Token,
) (triState, *evidence) {
	limit, ok := orderedLenLimit(sym, value)
	if !ok {
		return triUnknown, nil
	}

	if tri, ok := lenTruthFromLowerBound(limit, op); ok {
		return tri, nil
	}

	f, ok := st.facts[sym.key]
	if !ok {
		return triUnknown, nil
	}

	if tri, ev, ok := truthFromExactLenFact(f, limit, op); ok {
		return tri, ev
	}

	return truthFromNonZeroLenFact(f, limit, op)
}

func (l *linter) refineStates(states []state, expr ast.Expr, wantTrue bool) []state {
	out := make([]state, 0, len(states))
	for _, st := range states {
		out = append(out, l.refineState(st, expr, wantTrue)...)
	}

	return l.normalizeStates(out)
}

//nolint:cyclop,gocognit // Refinement intentionally enumerates boolean operators and propagation paths.
func (l *linter) refineState(st state, expr ast.Expr, wantTrue bool) []state {
	expr = l.unparen(expr)
	if tri, _ := l.truth(st, expr); tri == triTrue {
		if wantTrue {
			return []state{st}
		}

		return nil
	} else if tri == triFalse {
		if wantTrue {
			return nil
		}

		return []state{st}
	}

	switch expr := expr.(type) {
	case *ast.UnaryExpr:
		if expr.Op == token.NOT {
			return l.refineState(st, expr.X, !wantTrue)
		}
	case *ast.CallExpr:
		if sym, ok := l.predicateCallSymbolOf(expr); ok {
			states := []state{st}
			if next, ok := l.refineCallExpr(st, expr, wantTrue); ok {
				states = next
			}

			return l.refinePredicateCallStates(states, sym, expr.Pos(), wantTrue)
		}

		if next, ok := l.refineCallExpr(st, expr, wantTrue); ok {
			return next
		}
	case *ast.BinaryExpr:
		//exhaustive:ignore token.Token includes operators not meaningful for this expression type.
		switch expr.Op {
		case token.LAND:
			if wantTrue {
				left := l.refineState(st, expr.X, true)
				return l.refineStates(left, expr.Y, true)
			}

			out := l.refineState(st, expr.X, false)
			leftTrue := l.refineState(st, expr.X, true)
			out = append(out, l.refineStates(leftTrue, expr.Y, false)...)

			return l.normalizeStates(out)
		case token.LOR:
			if !wantTrue {
				left := l.refineState(st, expr.X, false)
				return l.refineStates(left, expr.Y, false)
			}

			out := l.refineState(st, expr.X, true)
			leftFalse := l.refineState(st, expr.X, false)
			out = append(out, l.refineStates(leftFalse, expr.Y, true)...)

			return l.normalizeStates(out)
		case token.EQL:
			if next, ok := l.refineCallScalar(st, expr.X, expr.Y, wantTrue); ok {
				return next
			}

			if sym, scalar, ok := l.symbolScalar(expr.X, expr.Y); ok {
				return l.refineSymbolScalar(st, sym, scalar, wantTrue, expr.Pos())
			}

			if sym, scalar, ok := l.symbolScalar(expr.Y, expr.X); ok {
				return l.refineSymbolScalar(st, sym, scalar, wantTrue, expr.Pos())
			}
		case token.NEQ:
			if next, ok := l.refineCallScalar(st, expr.X, expr.Y, !wantTrue); ok {
				return next
			}

			if sym, scalar, ok := l.symbolScalar(expr.X, expr.Y); ok {
				return l.refineSymbolScalar(st, sym, scalar, !wantTrue, expr.Pos())
			}

			if sym, scalar, ok := l.symbolScalar(expr.Y, expr.X); ok {
				return l.refineSymbolScalar(st, sym, scalar, !wantTrue, expr.Pos())
			}
		case token.GTR, token.GEQ, token.LSS, token.LEQ:
			if sym, scalar, ok := l.symbolScalar(expr.X, expr.Y); ok {
				return l.refineSymbolOrdered(st, sym, scalar, expr.Op, wantTrue, expr.Pos())
			}

			if sym, scalar, ok := l.symbolScalar(expr.Y, expr.X); ok {
				return l.refineSymbolOrdered(
					st,
					sym,
					scalar,
					reverseOrderedOp(expr.Op),
					wantTrue,
					expr.Pos(),
				)
			}
		}
	case *ast.Ident, *ast.SelectorExpr:
		if sym, ok := l.symbolOf(expr); ok && isBoolType(sym.typ) {
			val := scalar{
				kind: scalarBool,
				text: map[bool]string{true: boolTrueText, false: boolFalseText}[wantTrue],
			}

			next, ok := l.setExact(
				st,
				sym,
				val,
				evidence{pos: expr.Pos(), text: l.relationText(sym, "==", val)},
			)
			if !ok {
				return nil
			}

			return l.applyBindingCondition(next, sym, map[bool]returnKind{
				true:  returnBoolTrue,
				false: returnBoolFalse,
			}[wantTrue])
		}
	}

	if wantTrue {
		return []state{st}
	}

	return []state{st}
}

func (l *linter) refinePredicateCallStates(
	states []state,
	sym symbol,
	pos token.Pos,
	wantTrue bool,
) []state {
	value := scalar{kind: scalarBool, text: boolFalseText}
	if wantTrue {
		value.text = boolTrueText
	}

	out := make([]state, 0, len(states))
	for _, st := range states {
		next, ok := l.setExact(
			st,
			sym,
			value,
			evidence{pos: pos, text: l.relationText(sym, "==", value)},
		)
		if ok {
			out = append(out, next)
		}
	}

	return l.normalizeStates(out)
}

func (l *linter) refineSymbolOrdered(
	st state,
	sym symbol,
	value scalar,
	op token.Token,
	wantTrue bool,
	pos token.Pos,
) []state {
	if !isLenSymbol(sym) || value.kind != scalarInt {
		return []state{st}
	}

	limit, ok := scalarIntValue(value)
	if !ok {
		return []state{st}
	}

	if always, ok := lenCompareFromNonNegative(limit, op); ok {
		if wantTrue == always {
			return []state{st}
		}

		return nil
	}

	if next, refined, ok := l.refineLenCompare(st, sym, op, limit, wantTrue, pos); ok {
		if !refined {
			return []state{st}
		}

		return []state{next}
	}

	return []state{st}
}

func (l *linter) refineSymbolScalar(
	st state,
	sym symbol,
	value scalar,
	wantEq bool,
	pos token.Pos,
) []state {
	if wantEq {
		next, ok := l.setExact(
			st,
			sym,
			value,
			evidence{pos: pos, text: l.relationText(sym, "==", value)},
		)
		if !ok {
			return nil
		}

		return l.applyBindingForScalar(next, sym, value, true)
	}

	next, ok := l.addNot(st, sym, value, evidence{pos: pos, text: l.relationText(sym, "!=", value)})
	if !ok {
		return nil
	}

	return l.applyBindingForScalar(next, sym, value, false)
}

func (l *linter) setExact(st state, sym symbol, value scalar, why evidence) (state, bool) {
	out := st.clone()
	keys := aliasClosure(out, sym.key)

	for _, key := range keys {
		f := out.facts[key].clone()
		if f.exact != nil {
			if f.exact.value != value {
				return state{}, false
			}

			continue
		}

		if _, bad := f.not[value.key()]; bad {
			return state{}, false
		}
	}

	for _, key := range keys {
		f := out.facts[key].clone()
		f.exact = &exactFact{value: value, why: why}
		f.not = nil
		out.facts[key] = f
	}

	return out, true
}

func (l *linter) addNot(st state, sym symbol, value scalar, why evidence) (state, bool) {
	out := st.clone()
	keys := aliasClosure(out, sym.key)

	for _, key := range keys {
		f := out.facts[key].clone()
		if f.exact != nil && f.exact.value == value {
			return out, false
		}
	}

	for _, key := range keys {
		f := out.facts[key].clone()
		if f.exact != nil {
			continue
		}

		if f.not == nil {
			f.not = make(map[string]evidence)
		}

		f.not[value.key()] = why
		if isBoolType(sym.typ) && value.kind == scalarBool {
			other := scalar{kind: scalarBool, text: boolTrueText}
			if value.text == boolTrueText {
				other.text = boolFalseText
			}

			f.exact = &exactFact{value: other, why: why}
			f.not = nil
		}

		out.facts[key] = f
	}

	return out, true
}

func (l *linter) truthAcrossCase(
	states []state,
	tag ast.Expr,
	list []ast.Expr,
) (triState, *evidence) {
	if len(states) == 0 {
		return triUnknown, nil
	}

	allTrue := true
	allFalse := true

	var because *evidence

	for _, st := range states {
		tri, ev := l.truthCase(st, tag, list)
		if tri != triTrue {
			allTrue = false
		}

		if tri != triFalse {
			allFalse = false
		}

		if because == nil && ev != nil {
			copyEv := *ev
			because = &copyEv
		}
	}

	if allTrue {
		return triTrue, because
	}

	if allFalse {
		return triFalse, because
	}

	return triUnknown, nil
}

func (l *linter) truthCase(st state, tag ast.Expr, list []ast.Expr) (triState, *evidence) {
	if len(list) == 0 {
		return triTrue, nil
	}

	allFalse := true

	var firstFalse *evidence

	for _, expr := range list {
		var (
			tri triState
			ev  *evidence
		)

		if tag == nil {
			tri, ev = l.truth(st, expr)
		} else {
			tri, ev = l.truthSyntheticCompare(st, tag, expr)
		}

		if tri == triTrue {
			return triTrue, ev
		}

		if tri == triFalse && firstFalse == nil && ev != nil {
			copyEv := *ev
			firstFalse = &copyEv
		}

		if tri != triFalse {
			allFalse = false
		}
	}

	if allFalse {
		return triFalse, firstFalse
	}

	return triUnknown, nil
}

func (l *linter) truthSyntheticCompare(st state, lhs, rhs ast.Expr) (triState, *evidence) {
	if sym, scalar, ok := l.symbolScalar(lhs, rhs); ok {
		return l.truthSymbolScalar(st, sym, scalar, true)
	}

	if sym, scalar, ok := l.symbolScalar(rhs, lhs); ok {
		return l.truthSymbolScalar(st, sym, scalar, true)
	}

	if ls, ok := l.scalarOf(lhs); ok {
		if rs, ok := l.scalarOf(rhs); ok {
			if ls == rs {
				return triTrue, nil
			}

			return triFalse, nil
		}
	}

	return triUnknown, nil
}

func (l *linter) refineStatesCase(
	states []state,
	tag ast.Expr,
	list []ast.Expr,
	wantMatch bool,
) []state {
	if len(list) == 0 {
		if wantMatch {
			return l.normalizeStates(states)
		}

		return nil
	}

	out := make([]state, 0)
	for _, st := range states {
		out = append(out, l.refineStateCase(st, tag, list, wantMatch)...)
	}

	return l.normalizeStates(out)
}

func (l *linter) refineStateCase(st state, tag ast.Expr, list []ast.Expr, wantMatch bool) []state {
	if tag == nil {
		return l.refineStateListAsOr(st, list, wantMatch)
	}
	// switch x { case a, b: } behaves like x == a || x == b.
	branches := make([]ast.Expr, 0, len(list))
	for _, item := range list {
		branches = append(branches, &ast.BinaryExpr{X: tag, Op: token.EQL, Y: item})
	}

	return l.refineStateListAsOr(st, branches, wantMatch)
}

func (l *linter) refineStateListAsOr(st state, exprs []ast.Expr, wantTrue bool) []state {
	if len(exprs) == 0 {
		if wantTrue {
			return []state{st}
		}

		return nil
	}

	if wantTrue {
		var out []state

		currentFalse := []state{st}
		for _, expr := range exprs {
			matched := l.refineStates(currentFalse, expr, true)
			out = append(out, matched...)
			currentFalse = l.refineStates(currentFalse, expr, false)
		}

		return l.normalizeStates(out)
	}

	current := []state{st}
	for _, expr := range exprs {
		current = l.refineStates(current, expr, false)
	}

	return l.normalizeStates(current)
}

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
	l.issues = append(l.issues, Issue{Pos: pos, Kind: kind, Message: msg})
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

func (l *linter) symbolScalar(symExpr, scalarExpr ast.Expr) (symbol, scalar, bool) {
	sym, ok := l.valueSymbolOf(symExpr)
	if !ok {
		return symbol{}, scalar{}, false
	}

	value, ok := l.scalarOf(scalarExpr)
	if !ok {
		return symbol{}, scalar{}, false
	}

	return sym, value, true
}

func (l *linter) valueSymbolOf(expr ast.Expr) (symbol, bool) {
	if sym, ok := l.symbolOf(expr); ok {
		return sym, true
	}

	if sym, ok := l.lenSymbolOf(expr); ok {
		return sym, true
	}

	return l.predicateCallSymbolOf(expr)
}

func (l *linter) scalarOf(expr ast.Expr) (scalar, bool) {
	expr = l.unparen(expr)
	if id, ok := expr.(*ast.Ident); ok && id.Name == "nil" {
		return scalar{kind: scalarNil, text: nilText}, true
	}

	if l.isRuntimeTargetConstant(expr) {
		return scalar{}, false
	}

	if tv, ok := l.pkg.TypesInfo.Types[expr]; ok {
		if tv.Value != nil {
			return scalarFromConstantValue(tv.Value)
		}

		if tv.IsNil() {
			return scalar{kind: scalarNil, text: nilText}, true
		}
	}

	return scalar{}, false
}

func (l *linter) isRuntimeTargetConstant(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}

	obj, ok := l.pkg.TypesInfo.Uses[sel.Sel].(*types.Const)
	if !ok || obj.Pkg() == nil || obj.Pkg().Path() != "runtime" {
		return false
	}

	return obj.Name() == "GOOS" || obj.Name() == "GOARCH"
}

func (l *linter) lenSymbolOf(expr ast.Expr) (symbol, bool) {
	call, ok := l.unparen(expr).(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return symbol{}, false
	}

	id, ok := l.unparen(call.Fun).(*ast.Ident)
	if !ok || id.Name != "len" {
		return symbol{}, false
	}

	obj, ok := l.pkg.TypesInfo.ObjectOf(id).(*types.Builtin)
	if !ok || obj == nil || obj.Name() != "len" {
		return symbol{}, false
	}

	base, ok := l.symbolOf(call.Args[0])
	if !ok {
		return symbol{}, false
	}

	return lenSymbolForBase(base), true
}

func (l *linter) predicateCallSymbolOf(expr ast.Expr) (symbol, bool) {
	call, ok := l.unparen(expr).(*ast.CallExpr)
	if !ok || len(call.Args) != 0 || !isBoolType(l.pkg.TypesInfo.TypeOf(call)) {
		return symbol{}, false
	}

	obj, key, ok := l.calledFunc(call)
	if !ok || obj == nil || !isIsPredicateName(obj.Name()) {
		return symbol{}, false
	}

	sel, ok := l.unparen(call.Fun).(*ast.SelectorExpr)
	if !ok || l.pkg.TypesInfo.Selections[sel] == nil {
		return symbol{}, false
	}

	receiver, ok := l.symbolOf(sel.X)
	if !ok {
		return symbol{}, false
	}

	return symbol{
		key:  receiver.key + "|" + predicatePathSegmentPrefix + strings.ReplaceAll(key, "|", "/"),
		root: receiver.root,
		name: l.render(call),
		typ:  l.pkg.TypesInfo.TypeOf(call),
	}, true
}

func isLenSymbol(sym symbol) bool {
	return strings.HasSuffix(sym.key, "|#len")
}

func lenSymbolForBase(base symbol) symbol {
	return symbol{
		key:  base.key + "|" + lenPathSegment,
		root: base.root,
		name: "len(" + base.name + ")",
		typ:  types.Typ[types.Int],
	}
}

func scalarIntValue(value scalar) (int64, bool) {
	if value.kind != scalarInt {
		return 0, false
	}

	out, err := strconv.ParseInt(value.text, 10, 64)
	if err != nil {
		return 0, false
	}

	return out, true
}

func reverseOrderedOp(op token.Token) token.Token {
	//exhaustive:ignore token.Token includes many non-ordered operators irrelevant here.
	switch op {
	case token.GTR:
		return token.LSS
	case token.GEQ:
		return token.LEQ
	case token.LSS:
		return token.GTR
	case token.LEQ:
		return token.GEQ
	default:
		return op
	}
}

func compareInts(left, right int64, op token.Token) bool {
	//exhaustive:ignore token.Token includes many non-ordered operators irrelevant here.
	switch op {
	case token.GTR:
		return left > right
	case token.GEQ:
		return left >= right
	case token.LSS:
		return left < right
	case token.LEQ:
		return left <= right
	default:
		return false
	}
}

func lenCompareFromNonNegative(limit int64, op token.Token) (bool, bool) {
	//exhaustive:ignore token.Token includes many non-ordered operators irrelevant here.
	switch op {
	case token.GTR:
		if limit < 0 {
			return true, true
		}

		if limit >= 0 {
			return false, false
		}
	case token.GEQ:
		if limit <= 0 {
			return true, true
		}
	case token.LSS:
		if limit <= 0 {
			return false, true
		}
	case token.LEQ:
		if limit < 0 {
			return false, true
		}
	}

	return false, false
}

func lenCompareFromNonZero(limit int64, op token.Token) (bool, bool) {
	//exhaustive:ignore token.Token includes many non-ordered operators irrelevant here.
	switch op {
	case token.GTR:
		if limit == 0 {
			return true, true
		}
	case token.GEQ:
		if limit <= 1 {
			return true, true
		}
	case token.LSS:
		if limit <= 1 {
			return false, true
		}
	case token.LEQ:
		if limit == 0 {
			return false, true
		}
	}

	return false, false
}

func orderedLenLimit(sym symbol, value scalar) (int64, bool) {
	if !isLenSymbol(sym) || value.kind != scalarInt {
		return 0, false
	}

	return scalarIntValue(value)
}

func lenTruthFromLowerBound(limit int64, op token.Token) (triState, bool) {
	always, ok := lenCompareFromNonNegative(limit, op)
	if !ok {
		return triUnknown, false
	}

	if always {
		return triTrue, true
	}

	return triFalse, true
}

func truthFromExactLenFact(f fact, limit int64, op token.Token) (triState, *evidence, bool) {
	if f.exact == nil || f.exact.value.kind != scalarInt {
		return triUnknown, nil, false
	}

	current, ok := scalarIntValue(f.exact.value)
	if !ok {
		return triUnknown, nil, false
	}

	copyWhy := f.exact.why
	if compareInts(current, limit, op) {
		return triTrue, &copyWhy, true
	}

	return triFalse, &copyWhy, true
}

func truthFromNonZeroLenFact(f fact, limit int64, op token.Token) (triState, *evidence) {
	ev, ok := f.not[scalar{kind: scalarInt, text: zeroIntText}.key()]
	if !ok {
		return triUnknown, nil
	}

	always, ok := lenCompareFromNonZero(limit, op)
	if !ok {
		return triUnknown, nil
	}

	copyWhy := ev
	if always {
		return triTrue, &copyWhy
	}

	return triFalse, &copyWhy
}

type lenRefinement uint8

const (
	lenRefineUnknown lenRefinement = iota
	lenRefineExactZero
	lenRefineNotZero
)

type lenCompareRule struct {
	op        token.Token
	limit     int64
	whenTrue  lenRefinement
	whenFalse lenRefinement
}

var lenCompareRules = []lenCompareRule{
	{op: token.GTR, limit: 0, whenTrue: lenRefineNotZero, whenFalse: lenRefineExactZero},
	{op: token.GEQ, limit: 1, whenTrue: lenRefineNotZero, whenFalse: lenRefineExactZero},
	{op: token.LSS, limit: 1, whenTrue: lenRefineExactZero, whenFalse: lenRefineNotZero},
	{op: token.LEQ, limit: 0, whenTrue: lenRefineExactZero, whenFalse: lenRefineNotZero},
}

func lenRefinementForCompare(limit int64, op token.Token, wantTrue bool) lenRefinement {
	for _, rule := range lenCompareRules {
		if rule.op != op || rule.limit != limit {
			continue
		}

		if wantTrue {
			return rule.whenTrue
		}

		return rule.whenFalse
	}

	return lenRefineUnknown
}

func (l *linter) refineLenCompare(
	st state,
	sym symbol,
	op token.Token,
	limit int64,
	wantTrue bool,
	pos token.Pos,
) (state, bool, bool) {
	switch lenRefinementForCompare(limit, op, wantTrue) {
	case lenRefineUnknown:
		return state{}, false, false
	case lenRefineExactZero:
		return l.refineLenToExactZero(st, sym, pos)
	case lenRefineNotZero:
		return l.refineLenToNotZero(st, sym, pos)
	}

	return state{}, false, false
}

func (l *linter) refineLenToExactZero(st state, sym symbol, pos token.Pos) (state, bool, bool) {
	value := scalar{kind: scalarInt, text: zeroIntText}
	next, ok := l.setExact(
		st,
		sym,
		value,
		evidence{pos: pos, text: l.relationText(sym, "==", value)},
	)

	return next, true, ok
}

func (l *linter) refineLenToNotZero(st state, sym symbol, pos token.Pos) (state, bool, bool) {
	value := scalar{kind: scalarInt, text: zeroIntText}
	next, ok := l.addNot(
		st,
		sym,
		value,
		evidence{pos: pos, text: l.relationText(sym, "!=", value)},
	)

	return next, true, ok
}

func (l *linter) symbolOf(expr ast.Expr) (symbol, bool) {
	expr = l.unparen(expr)
	switch expr := expr.(type) {
	case *ast.Ident:
		obj := l.pkg.TypesInfo.ObjectOf(expr)
		if obj == nil {
			return symbol{}, false
		}

		if _, ok := obj.(*types.Var); !ok {
			return symbol{}, false
		}

		return symbolForObject(obj), true
	case *ast.SelectorExpr:
		sel := l.pkg.TypesInfo.Selections[expr]
		if sel == nil || sel.Kind() != types.FieldVal {
			return symbol{}, false
		}

		base, ok := l.symbolOf(expr.X)
		if !ok {
			return symbol{}, false
		}

		return base.child(expr.Sel.Name, sel.Type()), true
	default:
		return symbol{}, false
	}
}

func (l *linter) unparen(expr ast.Expr) ast.Expr {
	for {
		p, ok := expr.(*ast.ParenExpr)
		if !ok {
			return expr
		}

		expr = p.X
	}
}
