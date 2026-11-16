package structure

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"iter"
	"slices"
	"strings"
)

const (
	lookupLoopMaxBodyStmts = 4
	membershipScanMinArgs  = 2
)

type loopScope struct {
	info          *types.Info
	body          *ast.BlockStmt
	source        ast.Expr
	value         types.Object
	loopVars      map[types.Object]struct{}
	dependentVars map[types.Object]struct{}
	mutatedVars   map[types.Object]struct{}
}

type loopBinding struct {
	targets []ast.Expr
	values  []ast.Expr
}

type loopRules struct {
	q *pkgQuery
}

func (r loopRules) rangeLoop(loop *ast.RangeStmt) []Finding {
	scope := newLoopScope(r.q.pkg.TypesInfo, loop.Body, loop.X, loop.Key, loop.Value)
	out := r.loopCalls(scope)
	out = append(out, r.nestedLookupLoops(scope)...)

	return append(out, r.pairwiseComparisonLoops(scope)...)
}

func (r loopRules) forLoop(loop *ast.ForStmt) []Finding {
	var vars []ast.Expr
	if assign, ok := loop.Init.(*ast.AssignStmt); ok && assign.Tok == token.DEFINE {
		vars = assign.Lhs
	}

	return r.loopCalls(newLoopScope(r.q.pkg.TypesInfo, loop.Body, nil, vars...))
}

func newLoopScope(
	info *types.Info,
	body *ast.BlockStmt,
	source ast.Expr,
	vars ...ast.Expr,
) loopScope {
	scope := loopScope{
		info:          info,
		body:          body,
		source:        source,
		loopVars:      make(map[types.Object]struct{}),
		dependentVars: make(map[types.Object]struct{}),
		mutatedVars:   make(map[types.Object]struct{}),
	}

	for _, expr := range vars {
		if obj := objectForLoopVar(info, expr); obj != nil {
			scope.loopVars[obj] = struct{}{}
		}
	}

	if len(vars) != 0 {
		scope.value = objectForLoopVar(info, vars[len(vars)-1])
	}

	// Loop-local aliases can hide per-iteration inputs; propagate simple assignment deps.
	bindings := currentLoopBindings(body)
	scope.addMutatedObjects(bindings)
	scope.addCallMutatedObjects()
	scope.addDependentObjects(bindings)

	return scope
}

func objectForLoopVar(info *types.Info, expr ast.Expr) types.Object {
	ident, ok := ast.Unparen(expr).(*ast.Ident)
	if !ok || ident.Name == "_" {
		return nil
	}

	return info.ObjectOf(ident)
}

// objectForMutationRoot returns the variable a write through expr changes,
// such as x for x.f[i] or *x.
func objectForMutationRoot(info *types.Info, expr ast.Expr) types.Object {
	switch expr := ast.Unparen(expr).(type) {
	case *ast.Ident:
		return objectForLoopVar(info, expr)
	case *ast.IndexExpr:
		return objectForMutationRoot(info, expr.X)
	case *ast.SelectorExpr:
		return objectForMutationRoot(info, expr.X)
	case *ast.StarExpr:
		return objectForMutationRoot(info, expr.X)
	case *ast.UnaryExpr:
		if expr.Op == token.AND {
			return objectForMutationRoot(info, expr.X)
		}

		return nil
	default:
		return nil
	}
}

func (r loopRules) loopCalls(scope loopScope) []Finding {
	var out []Finding

	inspectCurrentLoop(scope.body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			out = r.appendLoopCallFindings(out, scope, call)
		}

		return true
	})

	return out
}

func (r loopRules) appendLoopCallFindings(
	out []Finding,
	scope loopScope,
	call *ast.CallExpr,
) []Finding {
	info := scope.info

	if collection, helper, ok := membershipScanCall(info, call); ok && scope.invariant(collection) {
		out = append(out, Finding{
			Pos:  call.Pos(),
			Kind: "loop_membership_scan",
			Message: fmt.Sprintf(
				`%s scans invariant collection %s inside loop; build a set before the loop`,
				helper,
				r.q.render(collection),
			),
		})
	}

	if target, helper, ok := sortCallTarget(info, call); ok &&
		scope.invariant(target) && !scope.usesDependent(call) {
		out = append(out, Finding{
			Pos:  call.Pos(),
			Kind: "loop_sort",
			Message: fmt.Sprintf(
				`%s orders %s inside loop; sort once outside or maintain order incrementally`,
				helper,
				r.q.render(target),
			),
		})
	}

	if kind, ok := externalCallKind(info, call); ok && scope.usesDependent(call) &&
		!strings.HasSuffix(r.q.pkg.FSet.Position(call.Pos()).Filename, "_test.go") {
		out = append(out, Finding{
			Pos:  call.Pos(),
			Kind: "loop_external_call",
			Message: kind + " " + r.q.render(
				call.Fun,
			) + ` inside loop can become N+1 work; batch fetch or move request outside loop`,
		})
	}

	if label, ok := invariantWorkCallLabel(info, call); ok && scope.invariant(call) {
		out = append(out, Finding{
			Pos:     call.Pos(),
			Kind:    "loop_invariant_work",
			Message: label + ` is recomputed inside loop without loop inputs; compute it before the loop`,
		})
	}

	return out
}

func membershipScanCall(info *types.Info, call *ast.CallExpr) (ast.Expr, string, bool) {
	name, pkgPath, ok := callPackageFunc(info, call)
	if !ok || pkgPath != "slices" || len(call.Args) < membershipScanMinArgs {
		return nil, "", false
	}

	switch name {
	case "Contains", "Index":
		return call.Args[0], "slices." + name, true
	default:
		return nil, "", false
	}
}

func sortCallTarget(info *types.Info, call *ast.CallExpr) (ast.Expr, string, bool) {
	name, pkgPath, ok := callPackageFunc(info, call)
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

// externalCallKind names calls that leave the process, such as network or
// database requests.
func externalCallKind(info *types.Info, call *ast.CallExpr) (string, bool) {
	name, pkgPath, ok := callPackageFunc(info, call)
	if !ok {
		return "", false
	}

	if pkgPath == "net/http" {
		// Package and method name alone also match in-memory reads such as Header.Get.
		switch funcObject(info, call.Fun).FullName() {
		case "net/http.Get", "net/http.Head", "net/http.Post", "net/http.PostForm",
			"(*net/http.Client).Do", "(*net/http.Client).Get", "(*net/http.Client).Head",
			"(*net/http.Client).Post", "(*net/http.Client).PostForm":
			return "network call", true
		}
	}

	if pkgPath == "database/sql" {
		switch name {
		case "Exec", "ExecContext", "Query", "QueryContext", "QueryRow", "QueryRowContext":
			return "database call", true
		}
	}

	return "", false
}

func invariantWorkCallLabel(info *types.Info, call *ast.CallExpr) (string, bool) {
	name, pkgPath, ok := callPackageFunc(info, call)
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

func (r loopRules) nestedLookupLoops(scope loopScope) []Finding {
	var out []Finding

	for inner, innerValue := range scope.innerRanges() {
		if len(inner.Body.List) > lookupLoopMaxBodyStmts ||
			r.sameRenderedExpr(scope.source, inner.X) || !scope.invariant(inner.X) ||
			!lookupLoopHasMatchBreak(scope.info, inner.Body, scope.value, innerValue) {
			continue
		}

		out = append(out, Finding{
			Pos:  inner.For,
			Kind: "nested_lookup_loop",
			Message: fmt.Sprintf(
				`nested range scans %s for each %s item; build a lookup map before the outer loop`,
				r.q.render(inner.X),
				r.q.render(scope.source),
			),
		})
	}

	return out
}

func (r loopRules) pairwiseComparisonLoops(scope loopScope) []Finding {
	var out []Finding

	for inner, innerValue := range scope.innerRanges() {
		if !r.sameRenderedExpr(scope.source, inner.X) || !scope.invariant(inner.X) ||
			!bodyComparesObjects(scope.info, inner.Body, scope.value, innerValue) {
			continue
		}

		out = append(out, Finding{
			Pos:  inner.For,
			Kind: "pairwise_comparison_loop",
			Message: fmt.Sprintf(
				`nested range compares pairs from %s; consider sort/two-pointer, sweep line, or bucketing for growing inputs`,
				r.q.render(scope.source),
			),
		})
	}

	return out
}

func (s loopScope) innerRanges() iter.Seq2[*ast.RangeStmt, types.Object] {
	return func(yield func(*ast.RangeStmt, types.Object) bool) {
		if s.value == nil {
			return
		}

		for _, stmt := range s.body.List {
			inner, ok := stmt.(*ast.RangeStmt)
			if !ok {
				continue
			}

			if innerValue := objectForLoopVar(s.info, inner.Value); innerValue != nil &&
				!yield(inner, innerValue) {
				return
			}
		}
	}
}

func lookupLoopHasMatchBreak(
	info *types.Info,
	body *ast.BlockStmt,
	outerValue types.Object,
	innerValue types.Object,
) bool {
	if len(body.List) != 1 {
		return false
	}

	ifStmt, ok := body.List[0].(*ast.IfStmt)
	if !ok || ifStmt.Init != nil || ifStmt.Else != nil {
		return false
	}

	if !exprComparesObjects(info, ifStmt.Cond, outerValue, innerValue) {
		return false
	}

	return blockHasBranch(ifStmt.Body, token.BREAK) || blockHasReturn(ifStmt.Body)
}

func bodyComparesObjects(
	info *types.Info,
	body *ast.BlockStmt,
	left types.Object,
	right types.Object,
) bool {
	return blockContains(body, func(n ast.Node) bool {
		binary, ok := n.(*ast.BinaryExpr)
		return ok && binaryComparesObjects(info, binary, left, right)
	})
}

func exprComparesObjects(
	info *types.Info,
	expr ast.Expr,
	left types.Object,
	right types.Object,
) bool {
	found := false

	ast.Inspect(expr, func(n ast.Node) bool {
		if found {
			return false
		}

		binary, ok := n.(*ast.BinaryExpr)
		if !ok {
			return true
		}

		found = binaryComparesObjects(info, binary, left, right)

		return !found
	})

	return found
}

func binaryComparesObjects(
	info *types.Info,
	expr *ast.BinaryExpr,
	left types.Object,
	right types.Object,
) bool {
	if expr.Op != token.EQL && expr.Op != token.NEQ && expr.Op != token.LSS &&
		expr.Op != token.LEQ && expr.Op != token.GTR && expr.Op != token.GEQ {
		return false
	}

	leftX := nodeUsesObject(expr.X, left, info)
	rightX := nodeUsesObject(expr.X, right, info)
	leftY := nodeUsesObject(expr.Y, left, info)
	rightY := nodeUsesObject(expr.Y, right, info)

	return leftX && rightY || rightX && leftY
}

func blockHasBranch(body *ast.BlockStmt, tok token.Token) bool {
	return blockContains(body, func(n ast.Node) bool {
		branch, ok := n.(*ast.BranchStmt)
		return ok && branch.Tok == tok
	})
}

func blockContains(body *ast.BlockStmt, match func(ast.Node) bool) bool {
	found := false

	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}

		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		default:
			found = match(n)
			return !found
		}
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

// invariant reports whether node gives the same value in every iteration.
func (s loopScope) invariant(node ast.Node) bool {
	return !s.usesDependent(node) &&
		!nodeUsesAnyObject(s.info, node, s.mutatedVars) &&
		!s.usesObjectDeclaredInside(node)
}

func (s loopScope) usesDependent(node ast.Node) bool {
	return nodeUsesAnyObject(s.info, node, s.loopVars) ||
		nodeUsesAnyObject(s.info, node, s.dependentVars)
}

func nodeUsesAnyObject(info *types.Info, node ast.Node, objects map[types.Object]struct{}) bool {
	if len(objects) == 0 {
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

		_, found = objects[info.ObjectOf(ident)]

		return !found
	})

	return found
}

func currentLoopBindings(body *ast.BlockStmt) []loopBinding {
	bindings := make([]loopBinding, 0)

	inspectCurrentLoop(body, func(n ast.Node) bool {
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

func (s *loopScope) addMutatedObjects(bindings []loopBinding) {
	for _, binding := range bindings {
		for _, lhs := range binding.targets {
			if obj := objectForMutationRoot(s.info, lhs); obj != nil {
				s.mutatedVars[obj] = struct{}{}
			}
		}
	}
}

func (s *loopScope) addCallMutatedObjects() {
	inspectCurrentLoop(s.body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !callArgsMayMutate(s.info, call) {
			return true
		}

		for _, arg := range call.Args {
			if obj := objectForMutationRoot(s.info, arg); obj != nil {
				s.mutatedVars[obj] = struct{}{}
			}
		}

		return true
	})
}

// callArgsMayMutate is false for calls known to only read their arguments.
func callArgsMayMutate(info *types.Info, call *ast.CallExpr) bool {
	if pureBuiltinCall(info, call) {
		return false
	}

	if _, _, ok := membershipScanCall(info, call); ok {
		return false
	}

	if _, _, ok := sortCallTarget(info, call); ok {
		return false
	}

	_, ok := invariantWorkCallLabel(info, call)

	return !ok
}

func pureBuiltinCall(info *types.Info, call *ast.CallExpr) bool {
	switch builtinName(info, call) {
	case "cap", "complex", "imag", builtinLenName, "max", "min",
		"panic", "print", "println", "real", "recover":
		return true
	default:
		return false
	}
}

func (s *loopScope) addDependentObjects(bindings []loopBinding) {
	for {
		changed := false

		for _, binding := range bindings {
			if !slices.ContainsFunc(
				binding.values,
				func(rhs ast.Expr) bool { return s.usesDependent(rhs) },
			) {
				continue
			}

			for _, lhs := range binding.targets {
				obj := objectForMutationRoot(s.info, lhs)
				if obj == nil {
					continue
				}

				if _, ok := s.dependentVars[obj]; ok {
					continue
				}

				s.dependentVars[obj] = struct{}{}
				changed = true
			}
		}

		if !changed {
			return
		}
	}
}

func (s loopScope) usesObjectDeclaredInside(node ast.Node) bool {
	found := false

	ast.Inspect(node, func(n ast.Node) bool {
		if found {
			return false
		}

		ident, ok := n.(*ast.Ident)
		if !ok {
			return true
		}

		obj := s.info.ObjectOf(ident)
		if obj == nil {
			return true
		}

		found = obj.Pos() > s.body.Lbrace && obj.Pos() < s.body.Rbrace

		return !found
	})

	return found
}

func (r loopRules) sameRenderedExpr(left ast.Expr, right ast.Expr) bool {
	return r.q.render(ast.Unparen(left)) == r.q.render(ast.Unparen(right))
}

// inspectCurrentLoop visits the loop body without entering nested loops or
// function literals, which run on their own iterations.
func inspectCurrentLoop(body *ast.BlockStmt, fn func(ast.Node) bool) {
	ast.Inspect(body, func(n ast.Node) bool {
		switch n.(type) {
		case nil:
			return true
		case *ast.FuncLit:
			return false
		case *ast.ForStmt, *ast.RangeStmt:
			return n == body
		default:
			return fn(n)
		}
	})
}

func callPackageFunc(info *types.Info, call *ast.CallExpr) (string, string, bool) {
	fn := funcObject(info, call.Fun)
	if fn == nil || fn.Pkg() == nil {
		return "", "", false
	}

	return fn.Name(), fn.Pkg().Path(), true
}

func funcObject(info *types.Info, expr ast.Expr) *types.Func {
	switch expr := ast.Unparen(expr).(type) {
	case *ast.IndexExpr:
		return funcObject(info, expr.X)
	case *ast.IndexListExpr:
		return funcObject(info, expr.X)
	case *ast.SelectorExpr:
		obj := info.ObjectOf(expr.Sel)
		if selection := info.Selections[expr]; selection != nil {
			obj = selection.Obj()
		}

		fn, _ := obj.(*types.Func)

		return fn
	case *ast.Ident:
		fn, _ := info.ObjectOf(expr).(*types.Func)

		return fn
	default:
		return nil
	}
}

func shortPackagePath(path string) string {
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		return path[idx+1:]
	}

	return path
}
