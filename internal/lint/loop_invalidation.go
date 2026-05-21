package lint

import (
	"go/ast"
	"go/token"
	"go/types"
)

type prefixInvalidation uint8

const (
	invalidateDescendantsOnly prefixInvalidation = iota + 1
	invalidateFullPrefix
)

const (
	builtinCopy   = "copy"
	builtinClear  = "clear"
	builtinDelete = "delete"
	builtinAppend = "append"
	builtinClose  = "close"
)

type prefixInvalidations map[string]prefixInvalidation

func (l *linter) loopInvalidationsForLoop(
	body []ast.Stmt,
	post ast.Stmt,
) prefixInvalidations {
	out := make(prefixInvalidations)
	seen := make(map[*ast.FuncLit]struct{})

	for _, stmt := range body {
		l.collectStmtInvalidations(stmt, out, seen)
	}

	if post != nil {
		l.collectStmtInvalidations(post, out, seen)
	}

	if len(out) == 0 {
		return nil
	}

	return out
}

func (l *linter) applyPrefixInvalidations(
	states []state,
	invalidations prefixInvalidations,
) []state {
	if len(states) == 0 || len(invalidations) == 0 {
		return l.normalizeStates(states)
	}

	out := make([]state, 0, len(states))
	for _, st0 := range states {
		st := st0.clone()

		for prefix, mode := range invalidations {
			if mode == invalidateFullPrefix {
				l.invalidatePrefix(&st, prefix)
				continue
			}

			l.invalidateDescendants(&st, prefix)
		}

		out = append(out, st)
	}

	return l.normalizeStates(out)
}

func (invalidations prefixInvalidations) addFull(prefix string) {
	if prefix == "" {
		return
	}

	invalidations[prefix] = invalidateFullPrefix
}

func (invalidations prefixInvalidations) addDescendants(prefix string) {
	if prefix == "" || invalidations[prefix] == invalidateFullPrefix {
		return
	}

	invalidations[prefix] = invalidateDescendantsOnly
}

func (l *linter) collectStmtInvalidations(
	stmt ast.Stmt,
	invalidations prefixInvalidations,
	seen map[*ast.FuncLit]struct{},
) {
	ast.Inspect(stmt, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.AssignStmt:
			for _, lhs := range n.Lhs {
				l.collectLHSInvalidations(lhs, invalidations)
			}
		case *ast.IncDecStmt:
			l.collectLHSInvalidations(n.X, invalidations)
		case *ast.RangeStmt:
			if n.Key != nil {
				l.collectLHSInvalidations(n.Key, invalidations)
			}

			if n.Value != nil {
				l.collectLHSInvalidations(n.Value, invalidations)
			}
		case *ast.CallExpr:
			l.collectCallInvalidations(n, invalidations, seen)
		case *ast.SendStmt:
			l.collectExprInvalidations(n.Chan, invalidations, seen)
			l.collectExprInvalidations(n.Value, invalidations, seen)
		}

		return true
	})
}

func (l *linter) collectExprInvalidations(
	expr ast.Expr,
	invalidations prefixInvalidations,
	seen map[*ast.FuncLit]struct{},
) {
	ast.Inspect(expr, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.CallExpr:
			l.collectCallInvalidations(n, invalidations, seen)
		}

		return true
	})
}

func (l *linter) collectCallInvalidations(
	call *ast.CallExpr,
	invalidations prefixInvalidations,
	seen map[*ast.FuncLit]struct{},
) {
	if call == nil {
		return
	}

	if tv, ok := l.pkg.TypesInfo.Types[call.Fun]; ok && tv.IsType() {
		return
	}

	addFull := func(expr ast.Expr) {
		for root := range l.rootsInExpr(expr) {
			invalidations.addFull(root)
		}
	}

	addDescendants := func(expr ast.Expr) {
		for root := range l.rootsInExpr(expr) {
			invalidations.addDescendants(root)
		}
	}

	l.collectCallTargetInvalidations(call, invalidations, seen, addDescendants)
	l.collectCallArgInvalidations(call, invalidations, seen, addFull, addDescendants)
	l.collectBuiltinCallInvalidations(call, addDescendants)
}

func (l *linter) collectCallTargetInvalidations(
	call *ast.CallExpr,
	invalidations prefixInvalidations,
	seen map[*ast.FuncLit]struct{},
	addDescendants func(ast.Expr),
) {
	if lit, ok := l.unparen(call.Fun).(*ast.FuncLit); ok {
		l.collectFuncLitInvalidations(lit, invalidations, seen)
	} else if lit, ok := l.localFuncLitForExpr(call.Fun); ok {
		l.collectFuncLitInvalidations(lit, invalidations, seen)
	}

	sel, ok := l.unparen(call.Fun).(*ast.SelectorExpr)
	if !ok {
		return
	}

	if s := l.pkg.TypesInfo.Selections[sel]; s != nil &&
		s.Kind() == types.MethodVal && isPointerLike(s.Recv()) {
		addDescendants(sel.X)
	}
}

func (l *linter) collectCallArgInvalidations(
	call *ast.CallExpr,
	invalidations prefixInvalidations,
	seen map[*ast.FuncLit]struct{},
	addFull func(ast.Expr),
	addDescendants func(ast.Expr),
) {
	for _, arg := range call.Args {
		if lit, ok := l.unparen(arg).(*ast.FuncLit); ok {
			l.collectFuncLitInvalidations(lit, invalidations, seen)
			continue
		}

		if lit, ok := l.localFuncLitForExpr(arg); ok {
			l.collectFuncLitInvalidations(lit, invalidations, seen)
			continue
		}

		if unary, ok := l.unparen(arg).(*ast.UnaryExpr); ok && unary.Op == token.AND {
			addFull(unary.X)
			continue
		}

		tv, ok := l.pkg.TypesInfo.Types[arg]
		if ok && isPointerLike(tv.Type) {
			addDescendants(arg)
		}
	}
}

func (l *linter) collectBuiltinCallInvalidations(
	call *ast.CallExpr,
	addDescendants func(ast.Expr),
) {
	ident, ok := l.unparen(call.Fun).(*ast.Ident)
	if !ok {
		return
	}

	obj, ok := l.pkg.TypesInfo.ObjectOf(ident).(*types.Builtin)
	if !ok || obj == nil || !isMutatingBuiltin(obj.Name()) {
		return
	}

	for _, arg := range call.Args {
		addDescendants(arg)
	}
}

func (l *linter) collectFuncLitInvalidations(
	lit *ast.FuncLit,
	invalidations prefixInvalidations,
	seen map[*ast.FuncLit]struct{},
) {
	if lit == nil || lit.Body == nil {
		return
	}

	if _, ok := seen[lit]; ok {
		return
	}

	seen[lit] = struct{}{}

	for _, stmt := range lit.Body.List {
		l.collectStmtInvalidations(stmt, invalidations, seen)
	}
}

func (l *linter) collectLHSInvalidations(lhs ast.Expr, invalidations prefixInvalidations) {
	if sym, ok := l.symbolOf(lhs); ok {
		invalidations.addFull(sym.key)

		return
	}

	for root := range l.rootsInExpr(lhs) {
		invalidations.addFull(root)
	}
}

func isMutatingBuiltin(name string) bool {
	switch name {
	case builtinCopy, builtinClear, builtinDelete, builtinAppend, builtinClose:
		return true
	default:
		return false
	}
}
