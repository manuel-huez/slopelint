package smells

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
)

const (
	trivialForwarderDirectStmtCount   = 1
	trivialForwarderAssignStmtCount   = 2
	trivialForwarderMaxShortBodyLines = 3
)

type forwarderCheck struct {
	ruleEnv
}

func (l forwarderCheck) check() {
	useCounts := l.productionPackageFuncUseCounts()

	for _, fn := range l.pkg.ProductionFuncs {
		l.checkFunc(fn, useCounts)
	}
}

func (l forwarderCheck) checkFunc(fn *ast.FuncDecl, useCounts map[string]int) {
	if !isEligibleTrivialForwarderDecl(fn) {
		return
	}

	obj, ok := l.forwarderObject(fn)
	if !ok {
		return
	}

	call, ok := forwardedBodyCall(l.pkg.TypesInfo, fn, obj)
	if !ok {
		return
	}

	useCount := useCounts[FuncObjectKey(obj)]
	reason := " at one production use"

	switch {
	case useCount == 0:
		return
	case useCount > 1:
		bodyStart := l.pkg.FSet.Position(fn.Body.List[0].Pos()).Line
		bodyEnd := l.pkg.FSet.Position(fn.Body.List[len(fn.Body.List)-1].End()).Line

		if bodyEnd-bodyStart+1 > trivialForwarderMaxShortBodyLines {
			return
		}

		reason = " in a body of 3 lines or less"
	}

	l.report(
		fn.Name.Pos(),
		"trivial_wrapper",
		fmt.Sprintf(
			`%s %q only forwards to %q%s`,
			trivialForwarderSubject(fn),
			fn.Name.Name,
			l.render(call.Fun),
			reason,
		),
	)

	if !ast.IsExported(fn.Name.Name) {
		reportGenericNameForHelper(l.findingSink, fn, "only forwards")
	}
}

func trivialForwarderSubject(fn *ast.FuncDecl) string {
	if fn.Recv != nil && ast.IsExported(fn.Name.Name) {
		return "exported method"
	}

	return "private helper"
}

// isEligibleTrivialForwarderDecl skips generic functions, exported functions
// (public API), and codec hooks that encoding packages call by name.
func isEligibleTrivialForwarderDecl(fn *ast.FuncDecl) bool {
	if fn.Body == nil ||
		hasTypeParams(fn.Type) ||
		fn.Recv == nil && ast.IsExported(fn.Name.Name) ||
		fn.Recv != nil && exportedCodecHookMethod(fn.Name.Name) {
		return false
	}

	bodyLen := len(fn.Body.List)

	return bodyLen == trivialForwarderDirectStmtCount || bodyLen == trivialForwarderAssignStmtCount
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
	return fnType.TypeParams.NumFields() != 0
}

func (l forwarderCheck) forwarderObject(fn *ast.FuncDecl) (*types.Func, bool) {
	for _, stmt := range fn.Body.List {
		if l.hasAttachedComment(stmt, nil) {
			return nil, false
		}
	}

	if l.hasAttachedComment(fn.Body, fn.Doc) {
		return nil, false
	}

	if !ast.IsExported(fn.Name.Name) {
		return privateFuncObject(l.pkg.TypesInfo, fn)
	}

	if funcUsesPrivateReceiverMember(l.pkg.TypesInfo, fn) {
		return nil, false
	}

	return funcDeclObject(l.pkg.TypesInfo, fn)
}

func funcUsesPrivateReceiverMember(info *types.Info, fn *ast.FuncDecl) bool {
	receiver := funcReceiverObject(info, fn.Recv)
	if receiver == nil {
		return false
	}

	usesPrivate := false

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok || !identRefersToObject(info, ast.Unparen(sel.X), receiver) {
			return true
		}

		selection := info.Selections[sel]
		if selection != nil && !ast.IsExported(selection.Obj().Name()) {
			usesPrivate = true
			return false
		}

		return true
	})

	return usesPrivate
}

// forwardedBodyCall returns the call that fn's body only forwards to, with
// fn's inputs passed through unchanged. Forwarder, single-use helper, and
// behavior clone checks share it so they agree on what a forwarder is.
func forwardedBodyCall(
	info *types.Info,
	fn *ast.FuncDecl,
	obj *types.Func,
) (*ast.CallExpr, bool) {
	sig, ok := obj.Type().(*types.Signature)
	if !ok {
		return nil, false
	}

	var call *ast.CallExpr

	resultCount := sig.Results().Len()

	switch stmts := fn.Body.List; len(stmts) {
	case trivialForwarderDirectStmtCount:
		call, ok = directForwardedCall(stmts[0], resultCount)
	case trivialForwarderAssignStmtCount:
		call, ok = assignReturnForwardedCall(info, stmts[0], stmts[1], resultCount)
	default:
		return nil, false
	}

	if !ok || !forwardedCallMatchesFuncInputs(info, call, fn, sig.Variadic()) {
		return nil, false
	}

	callee, _, ok := calledFunc(info, call)
	if !ok || callee == obj || obj.Pkg() == nil || callee.Pkg() == nil {
		return nil, false
	}

	return call, true
}

func directForwardedCall(stmt ast.Stmt, resultCount int) (*ast.CallExpr, bool) {
	switch stmt := stmt.(type) {
	case *ast.ReturnStmt:
		if resultCount == 0 || len(stmt.Results) != 1 {
			return nil, false
		}

		call, ok := ast.Unparen(stmt.Results[0]).(*ast.CallExpr)

		return call, ok
	case *ast.ExprStmt:
		if resultCount != 0 {
			return nil, false
		}

		call, ok := ast.Unparen(stmt.X).(*ast.CallExpr)

		return call, ok
	default:
		return nil, false
	}
}

func assignReturnForwardedCall(
	info *types.Info,
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

	call, names, ok := singleCallDefinition(assignStmt)
	if !ok {
		return nil, false
	}

	temps, ok := definedVars(info, names)
	if !ok || len(temps) != resultCount {
		return nil, false
	}

	for idx, result := range ret.Results {
		if !identRefersToObject(info, ast.Unparen(result), temps[idx]) {
			return nil, false
		}
	}

	return call, true
}

func singleCallDefinition(stmt ast.Stmt) (*ast.CallExpr, []ast.Expr, bool) {
	var (
		value ast.Expr
		names []ast.Expr
	)

	switch stmt := stmt.(type) {
	case *ast.AssignStmt:
		if stmt.Tok != token.DEFINE || len(stmt.Rhs) != 1 {
			return nil, nil, false
		}

		value, names = stmt.Rhs[0], stmt.Lhs
	case *ast.DeclStmt:
		spec, ok := singleUntypedVarSpec(stmt)
		if !ok {
			return nil, nil, false
		}

		value = spec.Values[0]
		for _, name := range spec.Names {
			names = append(names, name)
		}
	default:
		return nil, nil, false
	}

	call, ok := ast.Unparen(value).(*ast.CallExpr)

	return call, names, ok
}

func singleUntypedVarSpec(stmt *ast.DeclStmt) (*ast.ValueSpec, bool) {
	decl, ok := stmt.Decl.(*ast.GenDecl)
	if !ok || decl.Tok != token.VAR || len(decl.Specs) != 1 {
		return nil, false
	}

	spec, ok := decl.Specs[0].(*ast.ValueSpec)

	return spec, ok && spec.Type == nil && len(spec.Values) == 1
}

func definedVars(info *types.Info, exprs []ast.Expr) ([]*types.Var, bool) {
	if len(exprs) == 0 {
		return nil, false
	}

	vars := make([]*types.Var, 0, len(exprs))
	for _, expr := range exprs {
		ident, ok := expr.(*ast.Ident)
		if !ok || ident.Name == "_" {
			return nil, false
		}

		obj, ok := info.Defs[ident].(*types.Var)
		if !ok {
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
			if !ok {
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

	return ok && info.ObjectOf(ident) == obj
}
