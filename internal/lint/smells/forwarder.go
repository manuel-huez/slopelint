package smells

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
)

func (l *Runner) packageCallCountsForFiles(files []*ast.File) map[string]int {
	counts := make(map[string]int)

	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}

			_, key, ok := l.calledFunc(call)
			if !ok {
				return true
			}

			counts[key]++

			return true
		})
	}

	return counts
}

const (
	trivialForwarderDirectStmtCount = 1
	trivialForwarderAssignStmtCount = 2
)

func (l *Runner) checkTrivialForwarders() {
	callCounts := l.productionPackageCallCounts()

	l.forEachProductionFunc(func(fn *ast.FuncDecl) {
		l.checkTrivialForwarder(fn, callCounts)
	})
}

func (l *Runner) checkTrivialForwarder(fn *ast.FuncDecl, callCounts map[string]int) {
	if !isEligibleTrivialForwarderDecl(fn) {
		return
	}

	obj, ok := l.trivialForwarderObject(fn, callCounts)
	if !ok {
		return
	}

	call, ok := l.trivialForwarderBodyCall(fn, obj)
	if !ok {
		return
	}

	if !l.validForwardTarget(obj, call) {
		return
	}

	l.report(
		fn.Name.Pos(),
		"trivial_wrapper",
		fmt.Sprintf(
			`private helper %q only forwards to %q at one production callsite; inline or merge names`,
			fn.Name.Name,
			l.render(call.Fun),
		),
	)

	l.reportGenericNameForTrivialForwarder(fn)
}

func isEligibleTrivialForwarderDecl(fn *ast.FuncDecl) bool {
	if fn == nil || fn.Name == nil || fn.Body == nil || fn.Doc != nil || fn.Recv != nil {
		return false
	}

	if ast.IsExported(fn.Name.Name) || hasTypeParams(fn.Type) {
		return false
	}

	bodyLen := len(fn.Body.List)

	return bodyLen == 1 || bodyLen == 2
}

func hasTypeParams(fnType *ast.FuncType) bool {
	return fnType != nil && fnType.TypeParams != nil && len(fnType.TypeParams.List) != 0
}

func (l *Runner) trivialForwarderObject(
	fn *ast.FuncDecl,
	callCounts map[string]int,
) (*types.Func, bool) {
	for _, stmt := range fn.Body.List {
		if l.hasAttachedComment(stmt) {
			return nil, false
		}
	}

	if l.hasAttachedComment(fn.Body) {
		return nil, false
	}

	obj, ok := l.pkg.TypesInfo.ObjectOf(fn.Name).(*types.Func)
	if !ok || obj == nil {
		return nil, false
	}

	if callCounts[funcObjectKey(obj)] != 1 {
		return nil, false
	}

	return obj, true
}

func (l *Runner) trivialForwarderBodyCall(
	fn *ast.FuncDecl,
	obj *types.Func,
) (*ast.CallExpr, bool) {
	sig, ok := obj.Type().(*types.Signature)
	if !ok || sig == nil {
		return nil, false
	}

	call, ok := l.trivialForwarderCall(fn.Body.List, sig.Results())
	if !ok {
		return nil, false
	}

	if !forwardedCallMatchesParams(
		l.pkg.TypesInfo,
		call,
		funcParamObjects(l.pkg.TypesInfo, fn.Type.Params),
		sig.Variadic(),
	) {
		return nil, false
	}

	return call, true
}

func (l *Runner) validForwardTarget(obj *types.Func, call *ast.CallExpr) bool {
	callee, _, ok := l.calledFunc(call)
	if !ok || obj == nil || callee == nil || callee == obj {
		return false
	}

	return obj.Pkg() != nil && callee.Pkg() != nil
}

func (l *Runner) trivialForwarderCall(
	stmts []ast.Stmt,
	results *types.Tuple,
) (*ast.CallExpr, bool) {
	resultCount := 0
	if results != nil {
		resultCount = results.Len()
	}

	switch len(stmts) {
	case trivialForwarderDirectStmtCount:
		return l.directTrivialForwarderCall(stmts[0], resultCount)
	case trivialForwarderAssignStmtCount:
		return l.assignReturnTrivialForwarderCall(stmts[0], stmts[1], resultCount)
	default:
		return nil, false
	}
}

func (l *Runner) directTrivialForwarderCall(
	stmt ast.Stmt,
	resultCount int,
) (*ast.CallExpr, bool) {
	switch stmt := stmt.(type) {
	case *ast.ReturnStmt:
		if resultCount == 0 || len(stmt.Results) != 1 {
			return nil, false
		}

		call, ok := l.unparen(stmt.Results[0]).(*ast.CallExpr)
		if !ok {
			return nil, false
		}

		return call, true
	case *ast.ExprStmt:
		if resultCount != 0 {
			return nil, false
		}

		call, ok := l.unparen(stmt.X).(*ast.CallExpr)
		if !ok {
			return nil, false
		}

		return call, true
	default:
		return nil, false
	}
}

func (l *Runner) assignReturnTrivialForwarderCall(
	assignStmt ast.Stmt,
	returnStmt ast.Stmt,
	resultCount int,
) (*ast.CallExpr, bool) {
	if resultCount == 0 {
		return nil, false
	}

	ret, ok := returnStmt.(*ast.ReturnStmt)
	if !ok || len(ret.Results) != resultCount {
		return nil, false
	}

	call, temps, ok := l.forwardedCallResultTemps(assignStmt)
	if !ok || len(temps) != resultCount {
		return nil, false
	}

	for idx, result := range ret.Results {
		if !identRefersToObject(l.pkg.TypesInfo, l.unparen(result), temps[idx]) {
			return nil, false
		}
	}

	return call, true
}

func (l *Runner) forwardedCallResultTemps(stmt ast.Stmt) (*ast.CallExpr, []*types.Var, bool) {
	switch stmt := stmt.(type) {
	case *ast.AssignStmt:
		return l.forwardedCallResultTempsFromAssign(stmt)
	case *ast.DeclStmt:
		return l.forwardedCallResultTempsFromDecl(stmt)
	default:
		return nil, nil, false
	}
}

func (l *Runner) forwardedCallResultTempsFromAssign(
	stmt *ast.AssignStmt,
) (*ast.CallExpr, []*types.Var, bool) {
	if stmt == nil || stmt.Tok != token.DEFINE || len(stmt.Rhs) != 1 {
		return nil, nil, false
	}

	call, ok := l.unparen(stmt.Rhs[0]).(*ast.CallExpr)
	if !ok {
		return nil, nil, false
	}

	temps, ok := l.definedVarsForIdents(stmt.Lhs)
	if !ok {
		return nil, nil, false
	}

	return call, temps, true
}

func (l *Runner) forwardedCallResultTempsFromDecl(
	stmt *ast.DeclStmt,
) (*ast.CallExpr, []*types.Var, bool) {
	decl, ok := stmt.Decl.(*ast.GenDecl)
	if !ok || decl == nil || decl.Tok != token.VAR || len(decl.Specs) != 1 {
		return nil, nil, false
	}

	spec, ok := decl.Specs[0].(*ast.ValueSpec)
	if !ok || spec == nil || spec.Type != nil || len(spec.Values) != 1 {
		return nil, nil, false
	}

	call, ok := l.unparen(spec.Values[0]).(*ast.CallExpr)
	if !ok {
		return nil, nil, false
	}

	exprs := make([]ast.Expr, 0, len(spec.Names))
	for _, name := range spec.Names {
		exprs = append(exprs, name)
	}

	temps, ok := l.definedVarsForIdents(exprs)
	if !ok {
		return nil, nil, false
	}

	return call, temps, true
}

func (l *Runner) definedVarsForIdents(exprs []ast.Expr) ([]*types.Var, bool) {
	if len(exprs) == 0 {
		return nil, false
	}

	vars := make([]*types.Var, 0, len(exprs))
	for _, expr := range exprs {
		ident, ok := expr.(*ast.Ident)
		if !ok || ident == nil || ident.Name == "_" {
			return nil, false
		}

		obj, ok := l.pkg.TypesInfo.Defs[ident].(*types.Var)
		if !ok || obj == nil {
			return nil, false
		}

		vars = append(vars, obj)
	}

	return vars, true
}

func funcParamObjects(info *types.Info, fields *ast.FieldList) []*types.Var {
	if fields == nil {
		return nil
	}

	params := make([]*types.Var, 0)

	for _, field := range fields.List {
		if len(field.Names) == 0 {
			return nil
		}

		for _, name := range field.Names {
			obj, ok := info.ObjectOf(name).(*types.Var)
			if !ok || obj == nil {
				return nil
			}

			params = append(params, obj)
		}
	}

	return params
}

func forwardedCallMatchesParams(
	info *types.Info,
	call *ast.CallExpr,
	params []*types.Var,
	variadic bool,
) bool {
	if call == nil {
		return false
	}

	if variadic {
		if len(params) == 0 || len(call.Args) != len(params) || call.Ellipsis == token.NoPos {
			return false
		}
	} else if len(call.Args) != len(params) || call.Ellipsis != token.NoPos {
		return false
	}

	for idx, arg := range call.Args {
		if !exprForwardsParam(info, arg, params[idx]) {
			return false
		}
	}

	return true
}

func exprForwardsParam(info *types.Info, expr ast.Expr, obj types.Object) bool {
	if identRefersToObject(info, expr, obj) {
		return true
	}

	return fieldSelectorOnObject(info, expr, obj) ||
		typeConversionOfObject(info, expr, obj) ||
		zeroArgMethodCallOnObject(info, expr, obj)
}

func fieldSelectorOnObject(info *types.Info, expr ast.Expr, obj types.Object) bool {
	sel, ok := ast.Unparen(expr).(*ast.SelectorExpr)
	if !ok || !identRefersToObject(info, sel.X, obj) {
		return false
	}

	selection := info.Selections[sel]

	return selection != nil && selection.Kind() == types.FieldVal
}

func typeConversionOfObject(info *types.Info, expr ast.Expr, obj types.Object) bool {
	call, ok := ast.Unparen(expr).(*ast.CallExpr)
	if !ok || len(call.Args) != 1 || call.Ellipsis != token.NoPos {
		return false
	}

	tv, ok := info.Types[call.Fun]
	if !ok {
		tv, ok = info.Types[ast.Unparen(call.Fun)]
	}

	if !ok || !tv.IsType() {
		return false
	}

	return identRefersToObject(info, call.Args[0], obj)
}

func zeroArgMethodCallOnObject(info *types.Info, expr ast.Expr, obj types.Object) bool {
	call, ok := ast.Unparen(expr).(*ast.CallExpr)
	if !ok || len(call.Args) != 0 || call.Ellipsis != token.NoPos {
		return false
	}

	sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	if !ok || !identRefersToObject(info, sel.X, obj) {
		return false
	}

	selection := info.Selections[sel]
	if selection == nil || selection.Kind() != types.MethodVal {
		return false
	}

	_, ok = selection.Obj().(*types.Func)

	return ok
}

func identRefersToObject(info *types.Info, expr ast.Expr, obj types.Object) bool {
	ident, ok := ast.Unparen(expr).(*ast.Ident)
	if !ok || ident == nil {
		return false
	}

	return info.ObjectOf(ident) == obj
}
