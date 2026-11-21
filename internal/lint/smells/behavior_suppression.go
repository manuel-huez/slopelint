package smells

import (
	"go/ast"
	"go/token"
	"go/types"
	"slices"
)

func (b behaviorPackage) behaviorGenerated(node ast.Node) bool {
	for _, file := range b.pkg.ProductionFiles {
		if file.Pos() <= node.Pos() && node.End() <= file.End() {
			return ast.IsGenerated(file)
		}
	}

	return false
}

func (b behaviorPackage) behaviorAutoSuppressedClosures() map[*ast.FuncLit]struct{} {
	suppressed := make(map[*ast.FuncLit]struct{})

	for _, file := range b.pkg.ProductionFiles {
		if ast.IsGenerated(file) {
			continue
		}

		ast.PreorderStack(file, nil, func(node ast.Node, stack []ast.Node) bool {
			lit, ok := node.(*ast.FuncLit)
			if !ok {
				return true
			}

			if behaviorDeferredCleanupClosure(b.pkg.TypesInfo, lit, stack) ||
				behaviorSortComparatorClosure(b.pkg.TypesInfo, lit, stack) {
				suppressed[lit] = struct{}{}
			}

			return true
		})
	}

	return suppressed
}

func behaviorDeferredCleanupClosure(info *types.Info, lit *ast.FuncLit, stack []ast.Node) bool {
	if len(stack) < 2 || len(lit.Body.List) != 1 {
		return false
	}

	call, ok := stack[len(stack)-1].(*ast.CallExpr)
	if !ok || ast.Unparen(call.Fun) != lit {
		return false
	}

	deferStmt, ok := stack[len(stack)-2].(*ast.DeferStmt)
	if !ok || deferStmt.Call != call {
		return false
	}

	switch stmt := lit.Body.List[0].(type) {
	case *ast.ExprStmt:
		cleanup, ok := ast.Unparen(stmt.X).(*ast.CallExpr)

		return ok && behaviorCleanupCall(info, cleanup)
	case *ast.AssignStmt:
		return behaviorCleanupErrorAssignment(info, stmt)
	default:
		return false
	}
}

func behaviorCleanupCall(info *types.Info, call *ast.CallExpr) bool {
	fn, _, ok := calledFunc(info, call)
	if !ok {
		return false
	}

	switch fn.Name() {
	case "Cancel", "Close", "Release", "Stop", "Unlock":
		return true
	default:
		return false
	}
}

func behaviorCleanupErrorAssignment(info *types.Info, stmt *ast.AssignStmt) bool {
	if stmt.Tok != token.ASSIGN || len(stmt.Lhs) != 1 || len(stmt.Rhs) != 1 {
		return false
	}

	lhs, ok := ast.Unparen(stmt.Lhs[0]).(*ast.Ident)
	if !ok {
		return false
	}

	call, ok := ast.Unparen(stmt.Rhs[0]).(*ast.CallExpr)
	if !ok {
		return false
	}

	if lhs.Name == "_" {
		return behaviorCleanupCall(info, call)
	}

	return behaviorCleanupJoinAssignment(info, lhs, call)
}

func behaviorCleanupJoinAssignment(info *types.Info, lhs *ast.Ident, call *ast.CallExpr) bool {
	if !isPackageFuncCall(info, call, errorsPkgPath, "Join") {
		return false
	}

	lhsObject := info.ObjectOf(lhs)
	hasAccumulator := false
	hasCleanup := false

	for _, arg := range call.Args {
		ast.Inspect(arg, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.Ident:
				hasAccumulator = hasAccumulator || info.ObjectOf(node) == lhsObject
			case *ast.CallExpr:
				hasCleanup = hasCleanup || behaviorCleanupCall(info, node)
			}

			return true
		})
	}

	return lhsObject != nil && hasAccumulator && hasCleanup
}

func behaviorSortComparatorClosure(info *types.Info, lit *ast.FuncLit, stack []ast.Node) bool {
	if len(stack) == 0 || len(lit.Body.List) != 1 {
		return false
	}

	ret, ok := lit.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return false
	}

	comparison, ok := ast.Unparen(ret.Results[0]).(*ast.BinaryExpr)
	if !ok || !slices.Contains(
		[]token.Token{token.LSS, token.GTR, token.LEQ, token.GEQ},
		comparison.Op,
	) {
		return false
	}

	call, ok := stack[len(stack)-1].(*ast.CallExpr)
	if !ok || !behaviorCallHasArg(call, lit) {
		return false
	}

	return isPackageFuncCall(info, call, "sort", "Slice") ||
		isPackageFuncCall(info, call, "sort", "SliceStable")
}

func behaviorCallHasArg(call *ast.CallExpr, target ast.Expr) bool {
	for _, arg := range call.Args {
		if ast.Unparen(arg) == target {
			return true
		}
	}

	return false
}

func behaviorInterfaces(pkgs []*Package) []*types.Interface {
	seen := make(map[*types.Interface]struct{})
	interfaces := make([]*types.Interface, 0)

	add := func(typ types.Type) {
		if typ == nil {
			return
		}

		iface, ok := types.Unalias(typ).Underlying().(*types.Interface)
		if !ok || !iface.IsMethodSet() {
			return
		}

		iface.Complete()

		if _, exists := seen[iface]; exists {
			return
		}

		seen[iface] = struct{}{}
		interfaces = append(interfaces, iface)
	}

	for _, pkg := range pkgs {
		for _, spec := range pkg.ProductionTypes {
			if obj := pkg.TypesInfo.Defs[spec.Name]; obj != nil {
				add(obj.Type())
			}
		}

		for _, value := range pkg.TypesInfo.Types {
			add(value.Type)
		}
	}

	return interfaces
}

func behaviorInterfaceForwarder(
	info *types.Info,
	function behaviorSSAFunction,
	interfaces []*types.Interface,
) bool {
	decl, ok := function.syntax.(*ast.FuncDecl)
	if !ok || function.obj == nil || len(decl.Body.List) != 1 {
		return false
	}

	sig, ok := function.obj.Type().(*types.Signature)
	if !ok || sig.Recv() == nil || !behaviorMethodRequiredByInterface(
		function.obj,
		sig.Recv().Type(),
		interfaces,
	) {
		return false
	}

	_, ok = forwardedBodyCall(info, decl, function.obj)

	return ok
}

func behaviorMethodRequiredByInterface(
	method *types.Func,
	receiver types.Type,
	interfaces []*types.Interface,
) bool {
	for _, iface := range interfaces {
		if !types.Implements(receiver, iface) {
			continue
		}

		for ifaceMethod := range iface.Methods() {
			if ifaceMethod.Name() == method.Name() {
				return true
			}
		}
	}

	return false
}
