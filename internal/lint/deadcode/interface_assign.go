package deadcode

import (
	"go/ast"
	"go/types"
)

func (graph deadCodeGraph) addInterfaceMethodsForValueTargets(
	l *packageLinter,
	out map[string]struct{},
	expr ast.Expr,
	targets []types.Type,
) {
	if tuple, ok := l.pkg.TypesInfo.TypeOf(expr).(*types.Tuple); ok {
		for index, target := range targets {
			if index >= tuple.Len() {
				continue
			}

			graph.addInterfaceMethodsForType(l, out, tuple.At(index).Type(), target)
		}

		return
	}

	for _, target := range targets {
		graph.addInterfaceMethodsForValue(l, out, expr, target)
	}
}

func (graph deadCodeGraph) addInterfaceMethodsForValue(
	l *packageLinter,
	out map[string]struct{},
	expr ast.Expr,
	target types.Type,
) {
	for key := range graph.interfaceMethodsForValue(l, expr, target) {
		out[key] = struct{}{}
	}
}

func (graph deadCodeGraph) interfaceMethodsForValue(
	l *packageLinter,
	expr ast.Expr,
	target types.Type,
) map[string]struct{} {
	out := make(map[string]struct{})
	graph.addInterfaceMethodsForType(l, out, l.pkg.TypesInfo.TypeOf(expr), target)

	return out
}

func (graph deadCodeGraph) typeAssertInterfaceMethodUses(
	l *packageLinter,
	expr *ast.TypeAssertExpr,
) map[string]struct{} {
	if expr == nil || expr.Type == nil {
		return nil
	}

	source := l.pkg.TypesInfo.TypeOf(expr.X)
	target := l.pkg.TypesInfo.TypeOf(expr.Type)
	out := make(map[string]struct{})

	graph.addInterfaceMethodsForAssertedType(l, out, source, target)

	return out
}

func (graph deadCodeGraph) typeSwitchInterfaceMethodUses(
	l *packageLinter,
	stmt *ast.TypeSwitchStmt,
) map[string]struct{} {
	if stmt == nil || stmt.Body == nil {
		return nil
	}

	source := typeSwitchSourceType(l.pkg.TypesInfo, stmt.Assign)
	if source == nil {
		return nil
	}

	out := make(map[string]struct{})

	for _, item := range stmt.Body.List {
		clause, ok := item.(*ast.CaseClause)
		if !ok {
			continue
		}

		for _, expr := range clause.List {
			graph.addInterfaceMethodsForAssertedType(
				l,
				out,
				source,
				l.pkg.TypesInfo.TypeOf(expr),
			)
		}
	}

	return out
}

func typeSwitchSourceType(info *types.Info, stmt ast.Stmt) types.Type {
	var expr *ast.TypeAssertExpr

	switch stmt := stmt.(type) {
	case *ast.ExprStmt:
		expr, _ = stmt.X.(*ast.TypeAssertExpr)
	case *ast.AssignStmt:
		if len(stmt.Rhs) != 1 {
			return nil
		}

		expr, _ = stmt.Rhs[0].(*ast.TypeAssertExpr)
	}

	if expr == nil {
		return nil
	}

	return info.TypeOf(expr.X)
}

func (graph deadCodeGraph) addInterfaceMethodsForAssertedType(
	l *packageLinter,
	out map[string]struct{},
	source types.Type,
	target types.Type,
) {
	if source == nil || target == nil || !typeIsInterface(target) {
		return
	}

	if !typeIsInterface(source) {
		graph.addInterfaceMethodsForType(l, out, source, target)
		return
	}

	for _, receiver := range graph.candidateReceiverTypes() {
		if !typeSatisfiesInterface(l, receiver, source) ||
			!typeSatisfiesInterface(l, receiver, target) {
			continue
		}

		graph.addInterfaceMethodsForType(l, out, receiver, target)
	}
}

func (graph deadCodeGraph) candidateReceiverTypes() []types.Type {
	seen := make(map[string]struct{})
	receivers := make([]types.Type, 0)

	for _, decl := range graph.candidates {
		receiver := deadCodeMethodReceiverType(decl.obj)
		if receiver == nil {
			continue
		}

		key := types.TypeString(receiver, nil)
		if _, ok := seen[key]; ok {
			continue
		}

		seen[key] = struct{}{}

		receivers = append(receivers, receiver)
	}

	return receivers
}

func deadCodeMethodReceiverType(obj types.Object) types.Type {
	fn, ok := obj.(*types.Func)
	if !ok || fn == nil {
		return nil
	}

	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig == nil || sig.Recv() == nil {
		return nil
	}

	return sig.Recv().Type()
}

func (graph deadCodeGraph) addInterfaceMethodsForType(
	l *packageLinter,
	out map[string]struct{},
	source types.Type,
	target types.Type,
) {
	if source == nil || target == nil || typeIsInterface(source) {
		return
	}

	iface, ok := types.Unalias(target).Underlying().(*types.Interface)
	if !ok || iface.NumMethods() == 0 || !typeSatisfiesInterface(l, source, target) {
		return
	}

	for method := range iface.Methods() {
		if method == nil {
			continue
		}

		fn := lookupInterfaceMethodImplementation(l, source, method)
		if fn == nil {
			continue
		}

		out[deadCodeObjectKey(fn)] = struct{}{}
	}
}

func typeSatisfiesInterface(l *packageLinter, source types.Type, target types.Type) bool {
	if types.AssignableTo(source, target) {
		return true
	}

	iface, ok := types.Unalias(target).Underlying().(*types.Interface)
	if !ok {
		return false
	}

	for method := range iface.Methods() {
		if lookupInterfaceMethodImplementation(l, source, method) == nil {
			return false
		}
	}

	return true
}

func lookupInterfaceMethodImplementation(
	l *packageLinter,
	source types.Type,
	method *types.Func,
) *types.Func {
	if method == nil {
		return nil
	}

	obj, _, _ := types.LookupFieldOrMethod(
		source,
		true,
		interfaceMethodLookupPackage(l.pkg.TypesPkg, method),
		method.Name(),
	)

	fn, ok := obj.(*types.Func)
	if !ok || fn == nil || !methodSignatureMatches(fn, method) {
		return nil
	}

	return fn
}

func methodSignatureMatches(have *types.Func, want *types.Func) bool {
	haveSig, ok := have.Type().(*types.Signature)
	if !ok || haveSig == nil {
		return false
	}

	wantSig, ok := want.Type().(*types.Signature)
	if !ok || wantSig == nil {
		return false
	}

	if haveSig.Variadic() != wantSig.Variadic() ||
		tupleLen(haveSig.Params()) != tupleLen(wantSig.Params()) ||
		tupleLen(haveSig.Results()) != tupleLen(wantSig.Results()) {
		return false
	}

	for index := range tupleLen(haveSig.Params()) {
		if !deadCodeTypesEquivalent(
			haveSig.Params().At(index).Type(),
			wantSig.Params().At(index).Type(),
		) {
			return false
		}
	}

	for index := range tupleLen(haveSig.Results()) {
		if !deadCodeTypesEquivalent(
			haveSig.Results().At(index).Type(),
			wantSig.Results().At(index).Type(),
		) {
			return false
		}
	}

	return true
}

func deadCodeTypesEquivalent(left types.Type, right types.Type) bool {
	if types.Identical(left, right) {
		return true
	}

	return deadCodeTypeString(left) == deadCodeTypeString(right)
}

func deadCodeTypeString(typ types.Type) string {
	return types.TypeString(types.Unalias(typ), func(pkg *types.Package) string {
		if pkg == nil {
			return ""
		}

		return pkg.Path()
	})
}

func interfaceMethodLookupPackage(current *types.Package, method *types.Func) *types.Package {
	if method == nil || method.Exported() {
		return current
	}

	return method.Pkg()
}

func typeIsInterface(typ types.Type) bool {
	if typ == nil {
		return false
	}

	_, ok := types.Unalias(typ).Underlying().(*types.Interface)

	return ok
}
