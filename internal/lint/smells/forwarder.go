package smells

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"slices"
)

func (l *Runner) packageFuncUseCountsForFiles(files []*ast.File) map[string]int {
	counts := make(map[string]int)

	for _, file := range files {
		ast.PreorderStack(file, nil, func(n ast.Node, stack []ast.Node) bool {
			expr, ok := n.(ast.Expr)
			if !ok || funcUseExprIsDeclarationOrSelectorName(expr, stack) {
				return true
			}

			obj := l.funcObject(expr)
			if obj == nil {
				return true
			}

			key := funcObjectKey(obj)
			counts[key]++

			return true
		})
	}

	return counts
}

func funcUseExprIsDeclarationOrSelectorName(expr ast.Expr, stack []ast.Node) bool {
	if len(stack) == 0 {
		return false
	}

	parent := stack[len(stack)-1]

	ident, ok := expr.(*ast.Ident)
	if !ok {
		return false
	}

	switch parent := parent.(type) {
	case *ast.FuncDecl:
		return parent.Name == ident
	case *ast.SelectorExpr:
		return parent.Sel == ident
	case *ast.Field:
		return slices.Contains(parent.Names, ident)
	default:
		return false
	}
}

const (
	trivialForwarderDirectStmtCount   = 1
	trivialForwarderAssignStmtCount   = 2
	trivialForwarderMaxShortBodyLines = 3
)

func (l *Runner) checkTrivialForwarders() {
	useCounts := l.productionPackageFuncUseCounts()

	l.forEachProductionFunc(func(fn *ast.FuncDecl) {
		l.checkTrivialForwarder(fn, useCounts)
	})
}

func (l *Runner) checkTrivialForwarder(fn *ast.FuncDecl, useCounts map[string]int) {
	if !isEligibleTrivialForwarderDecl(fn) {
		return
	}

	obj, ok := l.trivialForwarderObject(fn)
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

	useCount := useCounts[funcObjectKey(obj)]
	reason := " at one production use"

	switch {
	case useCount == 0:
		return
	case useCount > 1:
		bodyStart := l.pkg.FSet.Position(fn.Body.List[0].Pos()).Line
		bodyEnd := l.pkg.FSet.Position(fn.Body.List[len(fn.Body.List)-1].End()).Line

		if bodyStart == 0 ||
			bodyEnd == 0 ||
			bodyEnd < bodyStart ||
			bodyEnd-bodyStart+1 > trivialForwarderMaxShortBodyLines {
			return
		}

		reason = " in a body of 3 lines or less"
	}

	l.report(
		fn.Name.Pos(),
		"trivial_wrapper",
		fmt.Sprintf(
			`%s %q only forwards to %q%s; inline or merge names`,
			trivialForwarderSubject(fn),
			fn.Name.Name,
			l.render(call.Fun),
			reason,
		),
	)

	if !ast.IsExported(fn.Name.Name) {
		l.reportGenericNameForHelper(fn, "only forwards")
	}
}

func trivialForwarderSubject(fn *ast.FuncDecl) string {
	if fn.Recv != nil && ast.IsExported(fn.Name.Name) {
		return "exported method"
	}

	return "private helper"
}

func isEligibleTrivialForwarderDecl(fn *ast.FuncDecl) bool {
	if fn == nil {
		return false
	}

	if fn.Name == nil {
		return false
	}

	if fn.Body == nil {
		return false
	}

	if trivialForwarderDeclIsSuppressed(fn) {
		return false
	}

	bodyLen := len(fn.Body.List)
	if bodyLen == trivialForwarderDirectStmtCount {
		return true
	}

	return bodyLen == trivialForwarderAssignStmtCount
}

func trivialForwarderDeclIsSuppressed(fn *ast.FuncDecl) bool {
	if hasTypeParams(fn.Type) {
		return true
	}

	if ast.IsExported(fn.Name.Name) {
		if fn.Recv == nil {
			return true
		}
	}

	if fn.Recv != nil {
		if exportedCodecHookMethod(fn.Name.Name) {
			return true
		}
	}

	return false
}

func exportedCodecHookMethod(name string) bool {
	switch name {
	case "MarshalJSON", "MarshalText", "MarshalYAML", "MarshalXML", "MarshalXMLAttr",
		"UnmarshalJSON", "UnmarshalText", "UnmarshalYAML", "UnmarshalXML", "UnmarshalXMLAttr":
		return true
	default:
		return false
	}
}

func hasTypeParams(fnType *ast.FuncType) bool {
	return fnType != nil && fnType.TypeParams != nil && len(fnType.TypeParams.List) != 0
}

func (l *Runner) trivialForwarderObject(fn *ast.FuncDecl) (*types.Func, bool) {
	for _, stmt := range fn.Body.List {
		if l.hasAttachedComment(stmt, nil) {
			return nil, false
		}
	}

	if l.hasAttachedComment(fn.Body, fn.Doc) {
		return nil, false
	}

	if ast.IsExported(fn.Name.Name) {
		if l.funcUsesPrivateReceiverMember(fn) {
			return nil, false
		}

		obj, ok := l.funcDeclObject(fn)
		if !ok || obj == nil {
			return nil, false
		}

		return obj, true
	}

	return l.privateFuncObject(fn)
}

func (l *Runner) funcUsesPrivateReceiverMember(fn *ast.FuncDecl) bool {
	receiver := funcReceiverObject(l.pkg.TypesInfo, fn.Recv)
	if receiver == nil {
		return false
	}

	usesPrivate := false

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok || !identRefersToObject(l.pkg.TypesInfo, l.unparen(sel.X), receiver) {
			return true
		}

		selection := l.pkg.TypesInfo.Selections[sel]
		if selection != nil && !ast.IsExported(selection.Obj().Name()) {
			usesPrivate = true
			return false
		}

		return true
	})

	return usesPrivate
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

	if !forwardedCallMatchesFuncInputs(
		l.pkg.TypesInfo,
		call,
		fn,
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

func forwardedCallMatchesFuncInputs(
	info *types.Info,
	call *ast.CallExpr,
	fn *ast.FuncDecl,
	variadic bool,
) bool {
	params := funcParamObjects(info, fn.Type.Params)
	if forwardedCallMatchesParamRefs(info, call, params, 0, variadic) {
		return true
	}

	receiver := funcReceiverObject(info, fn.Recv)
	if receiver == nil ||
		!forwardedCallMatchesParamRefs(info, call, params, 1, variadic) {
		return false
	}

	return exprForwardsSource(info, call.Args[0], receiver)
}

func funcReceiverObject(info *types.Info, fields *ast.FieldList) *types.Var {
	if fields == nil || len(fields.List) != 1 || len(fields.List[0].Names) != 1 {
		return nil
	}

	obj, _ := info.ObjectOf(fields.List[0].Names[0]).(*types.Var)

	return obj
}

func forwardedCallMatchesParamRefs(
	info *types.Info,
	call *ast.CallExpr,
	params []*types.Var,
	argOffset int,
	variadic bool,
) bool {
	if call == nil {
		return false
	}

	expectedArgs := len(params) + argOffset
	if variadic {
		if expectedArgs == 0 ||
			len(call.Args) != expectedArgs ||
			call.Ellipsis == token.NoPos {
			return false
		}
	} else if len(call.Args) != expectedArgs || call.Ellipsis != token.NoPos {
		return false
	}

	for idx, param := range params {
		if !exprForwardsSource(info, call.Args[idx+argOffset], param) {
			return false
		}
	}

	return true
}

func exprForwardsSource(info *types.Info, expr ast.Expr, obj types.Object) bool {
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
