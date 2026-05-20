package structure

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"strings"
)

const (
	lookupLoopMaxBodyStmts = 4
	membershipScanMinArgs  = 2
)

type loopScope struct {
	body          *ast.BlockStmt
	source        ast.Expr
	value         types.Object
	loopVars      map[types.Object]struct{}
	dependentVars map[types.Object]struct{}
	mutatedVars   map[types.Object]struct{}
	loopStart     token.Pos
	loopEnd       token.Pos
}

type loopBinding struct {
	targets []ast.Expr
	values  []ast.Expr
}

func (l *Runner) checkRangeLoopPerformance(loop *ast.RangeStmt) {
	if loop == nil || loop.Body == nil {
		return
	}

	scope := l.loopScope(loop.Body, loop.X, loop.Key, loop.Value)
	l.checkLoopPerformance(scope)
	l.checkNestedLookupLoops(scope)
	l.checkPairwiseComparisonLoops(scope)
}

func (l *Runner) checkForLoopPerformance(loop *ast.ForStmt) {
	if loop == nil || loop.Body == nil {
		return
	}

	scope := l.loopScope(loop.Body, nil, l.loopDefinedObjects(loop.Init)...)
	l.checkLoopPerformance(scope)
}

func (l *Runner) loopScope(body *ast.BlockStmt, source ast.Expr, vars ...ast.Expr) loopScope {
	objects := make(map[types.Object]struct{})

	for _, expr := range vars {
		if obj := l.objectForLoopVar(expr); obj != nil {
			objects[obj] = struct{}{}
		}
	}

	scope := loopScope{
		body:          body,
		source:        source,
		value:         l.objectForLoopVar(lastExpr(vars)),
		loopVars:      objects,
		dependentVars: make(map[types.Object]struct{}),
		mutatedVars:   make(map[types.Object]struct{}),
		loopStart:     body.Lbrace,
		loopEnd:       body.Rbrace,
	}

	l.collectLoopObjectFacts(&scope)

	return scope
}

func lastExpr(exprs []ast.Expr) ast.Expr {
	if len(exprs) == 0 {
		return nil
	}

	return exprs[len(exprs)-1]
}

func (l *Runner) loopDefinedObjects(stmt ast.Stmt) []ast.Expr {
	assign, ok := stmt.(*ast.AssignStmt)
	if !ok || assign.Tok != token.DEFINE {
		return nil
	}

	out := make([]ast.Expr, 0, len(assign.Lhs))
	out = append(out, assign.Lhs...)

	return out
}

func (l *Runner) objectForLoopVar(expr ast.Expr) types.Object {
	ident, ok := l.unparen(expr).(*ast.Ident)
	if !ok || ident.Name == "_" {
		return nil
	}

	return l.pkg.TypesInfo.ObjectOf(ident)
}

func (l *Runner) objectForMutationRoot(expr ast.Expr) types.Object {
	switch expr := l.unparen(expr).(type) {
	case *ast.Ident:
		return l.objectForLoopVar(expr)
	case *ast.IndexExpr:
		return l.objectForMutationRoot(expr.X)
	case *ast.SelectorExpr:
		return l.objectForMutationRoot(expr.X)
	case *ast.StarExpr:
		return l.objectForMutationRoot(expr.X)
	case *ast.UnaryExpr:
		if expr.Op == token.AND {
			return l.objectForMutationRoot(expr.X)
		}

		return nil
	default:
		return nil
	}
}

func (l *Runner) checkLoopPerformance(scope loopScope) {
	if scope.body == nil {
		return
	}

	l.inspectCurrentLoop(scope.body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		l.checkLoopMembershipScan(scope, call)
		l.checkLoopSort(scope, call)
		l.checkLoopExternalCall(scope, call)
		l.checkLoopInvariantCall(scope, call)

		return true
	})
}

func (l *Runner) checkLoopMembershipScan(scope loopScope, call *ast.CallExpr) {
	collection, helper, ok := l.membershipScanCall(call)
	if !ok || !l.invariantLoopExpr(scope, collection) {
		return
	}

	l.report(
		call.Pos(),
		"loop_membership_scan",
		fmt.Sprintf(
			`%s scans invariant collection %s inside loop; build a set before the loop`,
			helper,
			l.render(collection),
		),
	)
}

func (l *Runner) membershipScanCall(call *ast.CallExpr) (ast.Expr, string, bool) {
	name, pkgPath, ok := l.callPackageFunc(call)
	if !ok || pkgPath != "slices" {
		return nil, "", false
	}

	if len(call.Args) < membershipScanMinArgs {
		return nil, "", false
	}

	switch name {
	case "Contains", "Index":
		return call.Args[0], "slices." + name, true
	default:
		return nil, "", false
	}
}

func (l *Runner) checkLoopSort(scope loopScope, call *ast.CallExpr) {
	target, helper, ok := l.sortCallTarget(call)
	if !ok ||
		!l.invariantLoopExpr(scope, target) ||
		l.nodeUsesLoopDependentObjects(scope, call) {
		return
	}

	l.report(
		call.Pos(),
		"loop_sort",
		fmt.Sprintf(
			`%s orders %s inside loop; sort once outside or maintain order incrementally`,
			helper,
			l.render(target),
		),
	)
}

func (l *Runner) sortCallTarget(call *ast.CallExpr) (ast.Expr, string, bool) {
	name, pkgPath, ok := l.callPackageFunc(call)
	if !ok || len(call.Args) == 0 {
		return nil, "", false
	}

	if pkgPath == "sort" {
		switch name {
		case "Float64s", "Ints", "Slice", "SliceStable", "Sort", "Stable", "Strings":
			return call.Args[0], "sort." + name, true
		}
	}

	if pkgPath == "slices" {
		switch name {
		case "Sort", "SortFunc", "SortStableFunc":
			return call.Args[0], "slices." + name, true
		}
	}

	return nil, "", false
}

func (l *Runner) checkLoopExternalCall(scope loopScope, call *ast.CallExpr) {
	label, ok := l.externalCallLabel(call)
	if !ok || !l.nodeUsesLoopDependentObjects(scope, call) {
		return
	}

	if l.nodeInTestFile(call) {
		return
	}

	l.report(
		call.Pos(),
		"loop_external_call",
		label+` inside loop can become N+1 work; batch fetch or move request outside loop`,
	)
}

func (l *Runner) externalCallLabel(call *ast.CallExpr) (string, bool) {
	name, pkgPath, ok := l.callPackageFunc(call)
	if !ok {
		return "", false
	}

	if pkgPath == "net/http" {
		switch name {
		case "Do", "Get", "Head", "Post", "PostForm":
			return "network call " + l.render(call.Fun), true
		}
	}

	if pkgPath == "database/sql" {
		switch name {
		case "Exec", "ExecContext", "Query", "QueryContext", "QueryRow", "QueryRowContext":
			return "database call " + l.render(call.Fun), true
		}
	}

	return "", false
}

func (l *Runner) nodeInTestFile(node ast.Node) bool {
	if node == nil || l.pkg.FSet == nil {
		return false
	}

	return strings.HasSuffix(l.pkg.FSet.Position(node.Pos()).Filename, "_test.go")
}

func (l *Runner) checkLoopInvariantCall(scope loopScope, call *ast.CallExpr) {
	label, ok := l.invariantWorkCallLabel(call)
	if !ok || !l.invariantLoopExpr(scope, call) {
		return
	}

	l.report(
		call.Pos(),
		"loop_invariant_work",
		label+` is recomputed inside loop without loop inputs; compute it before the loop`,
	)
}

func (l *Runner) invariantWorkCallLabel(call *ast.CallExpr) (string, bool) {
	name, pkgPath, ok := l.callPackageFunc(call)
	if !ok {
		return "", false
	}

	if pkgPath == "regexp" {
		switch name {
		case "Compile", "CompilePOSIX", "MustCompile", "MustCompilePOSIX":
			return "regexp." + name, true
		}
	}

	if pkgPath == "strings" && name == "NewReplacer" {
		return "strings.NewReplacer", true
	}

	if pkgPath == "text/template" || pkgPath == "html/template" {
		switch name {
		case "Must", "ParseFiles", "ParseFS", "ParseGlob":
			return shortPackagePath(pkgPath) + "." + name, true
		}
	}

	return "", false
}

func (l *Runner) checkNestedLookupLoops(scope loopScope) {
	outerValue := l.singleLoopValueObject(scope)
	if outerValue == nil {
		return
	}

	l.forEachInnerRangeValue(scope, func(inner *ast.RangeStmt, innerValue types.Object) {
		if len(inner.Body.List) > lookupLoopMaxBodyStmts {
			return
		}

		if l.sameRenderedExpr(scope.source, inner.X) || !l.invariantLoopExpr(scope, inner.X) {
			return
		}

		if !l.lookupLoopHasMatchBreak(inner.Body, outerValue, innerValue) {
			return
		}

		l.report(
			inner.For,
			"nested_lookup_loop",
			fmt.Sprintf(
				`nested range scans %s for each %s item; build a lookup map before the outer loop`,
				l.render(inner.X),
				l.render(scope.source),
			),
		)
	})
}

func (l *Runner) checkPairwiseComparisonLoops(scope loopScope) {
	outerValue := l.singleLoopValueObject(scope)
	if outerValue == nil {
		return
	}

	l.forEachInnerRangeValue(scope, func(inner *ast.RangeStmt, innerValue types.Object) {
		if !l.sameRenderedExpr(scope.source, inner.X) || !l.invariantLoopExpr(scope, inner.X) {
			return
		}

		if !l.bodyComparesObjects(inner.Body, outerValue, innerValue) {
			return
		}

		l.report(
			inner.For,
			"pairwise_comparison_loop",
			fmt.Sprintf(
				`nested range compares pairs from %s; consider sort/two-pointer, sweep line, or bucketing for growing inputs`,
				l.render(scope.source),
			),
		)
	})
}

func (l *Runner) forEachInnerRangeValue(
	scope loopScope,
	fn func(*ast.RangeStmt, types.Object),
) {
	for _, stmt := range scope.body.List {
		inner, ok := stmt.(*ast.RangeStmt)
		if !ok || inner.Body == nil {
			continue
		}

		innerValue := l.rangeValueObject(inner)
		if innerValue == nil {
			continue
		}

		fn(inner, innerValue)
	}
}

func (l *Runner) lookupLoopHasMatchBreak(
	body *ast.BlockStmt,
	outerValue types.Object,
	innerValue types.Object,
) bool {
	if body == nil || len(body.List) != 1 {
		return false
	}

	ifStmt, ok := body.List[0].(*ast.IfStmt)
	if !ok || ifStmt.Init != nil || ifStmt.Else != nil || ifStmt.Body == nil {
		return false
	}

	if !l.exprComparesObjects(ifStmt.Cond, outerValue, innerValue) {
		return false
	}

	return blockHasBranch(ifStmt.Body, token.BREAK) || blockHasReturn(ifStmt.Body)
}

func (l *Runner) bodyComparesObjects(
	body *ast.BlockStmt,
	left types.Object,
	right types.Object,
) bool {
	found := false

	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}

		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.BinaryExpr:
			found = l.binaryComparesObjects(n, left, right)
			return !found
		}

		return true
	})

	return found
}

func (l *Runner) exprComparesObjects(expr ast.Expr, left types.Object, right types.Object) bool {
	found := false

	ast.Inspect(expr, func(n ast.Node) bool {
		if found {
			return false
		}

		binary, ok := n.(*ast.BinaryExpr)
		if !ok {
			return true
		}

		found = l.binaryComparesObjects(binary, left, right)

		return !found
	})

	return found
}

func (l *Runner) binaryComparesObjects(
	expr *ast.BinaryExpr,
	left types.Object,
	right types.Object,
) bool {
	if expr == nil || !comparisonToken(expr.Op) {
		return false
	}

	leftX := nodeUsesObject(expr.X, left, l.pkg.TypesInfo)
	rightX := nodeUsesObject(expr.X, right, l.pkg.TypesInfo)
	leftY := nodeUsesObject(expr.Y, left, l.pkg.TypesInfo)
	rightY := nodeUsesObject(expr.Y, right, l.pkg.TypesInfo)

	return leftX && rightY || rightX && leftY
}

func comparisonToken(op token.Token) bool {
	//exhaustive:ignore only comparison tokens matter for loop object comparisons.
	switch op {
	case token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ:
		return true
	default:
		return false
	}
}

func (l *Runner) singleLoopValueObject(scope loopScope) types.Object {
	return scope.value
}

func (l *Runner) rangeValueObject(loop *ast.RangeStmt) types.Object {
	if loop == nil {
		return nil
	}

	return l.objectForLoopVar(loop.Value)
}

func blockHasBranch(body *ast.BlockStmt, tok token.Token) bool {
	found := false

	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}

		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.BranchStmt:
			found = n.Tok == tok
			return !found
		}

		return true
	})

	return found
}

func blockHasReturn(body *ast.BlockStmt) bool {
	found := false

	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}

		switch n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			found = true
			return false
		}

		return true
	})

	return found
}

func (l *Runner) invariantLoopExpr(scope loopScope, node ast.Node) bool {
	return node != nil &&
		!l.nodeUsesLoopDependentObjects(scope, node) &&
		!l.nodeUsesMutatedObjects(scope, node) &&
		!l.nodeUsesObjectDeclaredInsideLoop(scope, node)
}

func (l *Runner) nodeUsesLoopObjects(scope loopScope, node ast.Node) bool {
	return l.nodeUsesAnyObject(node, scope.loopVars)
}

func (l *Runner) nodeUsesLoopDependentObjects(scope loopScope, node ast.Node) bool {
	return l.nodeUsesLoopObjects(scope, node) ||
		l.nodeUsesAnyObject(node, scope.dependentVars)
}

func (l *Runner) nodeUsesMutatedObjects(scope loopScope, node ast.Node) bool {
	return l.nodeUsesAnyObject(node, scope.mutatedVars)
}

func (l *Runner) nodeUsesAnyObject(
	node ast.Node,
	objects map[types.Object]struct{},
) bool {
	if len(objects) == 0 || node == nil {
		return false
	}

	found := false

	ast.Inspect(node, func(n ast.Node) bool {
		if found {
			return false
		}

		ident, ok := n.(*ast.Ident)
		if !ok {
			return true
		}

		_, found = objects[l.pkg.TypesInfo.ObjectOf(ident)]

		return !found
	})

	return found
}

func (l *Runner) collectLoopObjectFacts(scope *loopScope) {
	if scope == nil || scope.body == nil {
		return
	}

	// Loop-local aliases can hide per-iteration inputs; propagate simple assignment deps.
	bindings := l.currentLoopBindings(scope.body)
	l.addLoopMutatedObjects(scope, bindings)
	l.addLoopCallMutatedObjects(scope)
	l.addLoopDependentObjects(scope, bindings)
}

func (l *Runner) currentLoopBindings(body *ast.BlockStmt) []loopBinding {
	bindings := make([]loopBinding, 0)

	l.inspectCurrentLoop(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			bindings = append(bindings, loopBinding{
				targets: n.Lhs,
				values:  n.Rhs,
			})
		case *ast.ValueSpec:
			targets := make([]ast.Expr, 0, len(n.Names))
			for _, name := range n.Names {
				targets = append(targets, name)
			}

			bindings = append(bindings, loopBinding{
				targets: targets,
				values:  n.Values,
			})
		}

		return true
	})

	return bindings
}

func (l *Runner) addLoopMutatedObjects(
	scope *loopScope,
	bindings []loopBinding,
) {
	for _, binding := range bindings {
		for _, lhs := range binding.targets {
			obj := l.objectForMutationRoot(lhs)
			if obj != nil {
				scope.mutatedVars[obj] = struct{}{}
			}
		}
	}
}

func (l *Runner) addLoopCallMutatedObjects(scope *loopScope) {
	l.inspectCurrentLoop(scope.body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !l.callArgsMayMutate(call) {
			return true
		}

		for _, arg := range call.Args {
			obj := l.objectForMutationRoot(arg)
			if obj != nil {
				scope.mutatedVars[obj] = struct{}{}
			}
		}

		return true
	})
}

func (l *Runner) callArgsMayMutate(call *ast.CallExpr) bool {
	if l.pureBuiltinCall(call) {
		return false
	}

	if _, _, ok := l.membershipScanCall(call); ok {
		return false
	}

	if _, _, ok := l.sortCallTarget(call); ok {
		return false
	}

	if _, ok := l.invariantWorkCallLabel(call); ok {
		return false
	}

	return true
}

func (l *Runner) pureBuiltinCall(call *ast.CallExpr) bool {
	ident, ok := l.unparen(call.Fun).(*ast.Ident)
	if !ok {
		return false
	}

	obj, _ := l.pkg.TypesInfo.ObjectOf(ident).(*types.Builtin)
	if obj == nil {
		return false
	}

	switch obj.Name() {
	case "cap", "complex", "imag", builtinLenName, "max", "min",
		"panic", "print", "println", "real", "recover":
		return true
	default:
		return false
	}
}

func (l *Runner) addLoopDependentObjects(
	scope *loopScope,
	bindings []loopBinding,
) {
	for {
		changed := false

		for _, binding := range bindings {
			if !l.bindingUsesLoopDependency(*scope, binding) {
				continue
			}

			for _, lhs := range binding.targets {
				obj := l.objectForMutationRoot(lhs)
				if obj == nil {
					continue
				}

				if _, ok := scope.dependentVars[obj]; ok {
					continue
				}

				scope.dependentVars[obj] = struct{}{}
				changed = true
			}
		}

		if !changed {
			return
		}
	}
}

func (l *Runner) bindingUsesLoopDependency(
	scope loopScope,
	binding loopBinding,
) bool {
	for _, rhs := range binding.values {
		if l.nodeUsesLoopDependentObjects(scope, rhs) {
			return true
		}
	}

	return false
}

func (l *Runner) nodeUsesObjectDeclaredInsideLoop(scope loopScope, node ast.Node) bool {
	found := false

	ast.Inspect(node, func(n ast.Node) bool {
		if found {
			return false
		}

		ident, ok := n.(*ast.Ident)
		if !ok {
			return true
		}

		obj := l.pkg.TypesInfo.ObjectOf(ident)
		if obj == nil {
			return true
		}

		pos := obj.Pos()
		found = pos > scope.loopStart && pos < scope.loopEnd

		return !found
	})

	return found
}

func (l *Runner) sameRenderedExpr(left ast.Expr, right ast.Expr) bool {
	if left == nil || right == nil {
		return false
	}

	return l.render(l.unparen(left)) == l.render(l.unparen(right))
}

func (l *Runner) inspectCurrentLoop(body *ast.BlockStmt, fn func(ast.Node) bool) {
	if body == nil {
		return
	}

	ast.Inspect(body, func(n ast.Node) bool {
		if n == nil {
			return true
		}

		switch n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ForStmt, *ast.RangeStmt:
			return n == body
		default:
			return fn(n)
		}
	})
}

func (l *Runner) callPackageFunc(call *ast.CallExpr) (string, string, bool) {
	fn := l.funcObject(call.Fun)
	if fn == nil || fn.Pkg() == nil {
		return "", "", false
	}

	return fn.Name(), fn.Pkg().Path(), true
}

func (l *Runner) funcObject(expr ast.Expr) *types.Func {
	switch expr := l.unparen(expr).(type) {
	case *ast.IndexExpr:
		return l.funcObject(expr.X)
	case *ast.IndexListExpr:
		return l.funcObject(expr.X)
	case *ast.SelectorExpr:
		return l.selectorFuncObject(expr)
	case *ast.Ident:
		obj, _ := l.pkg.TypesInfo.ObjectOf(expr).(*types.Func)

		return obj
	default:
		return nil
	}
}

func (l *Runner) selectorFuncObject(expr *ast.SelectorExpr) *types.Func {
	obj := l.pkg.TypesInfo.ObjectOf(expr.Sel)
	if selection := l.pkg.TypesInfo.Selections[expr]; selection != nil {
		obj = selection.Obj()
	}

	fn, _ := obj.(*types.Func)

	return fn
}

func shortPackagePath(path string) string {
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		return path[idx+1:]
	}

	return path
}
