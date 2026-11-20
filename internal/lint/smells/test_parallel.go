package smells

import (
	"fmt"
	"go/ast"
	"go/types"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const serialMarkerPrefix = "//slopelint:serial"

// SerialMarker returns the reason of a //slopelint:serial comment, which
// states why a test does not call t.Parallel().
func SerialMarker(text string) (string, bool) {
	rest, ok := strings.CutPrefix(text, serialMarkerPrefix)
	if !ok {
		return "", false
	}

	if first, _ := utf8.DecodeRuneInString(rest); rest != "" && !unicode.IsSpace(first) {
		return "", false
	}

	return strings.TrimSpace(rest), true
}

// testParallelCheck reports runnable tests that do not call t.Parallel(),
// and serial markers that lack a reason or are no longer needed.
type testParallelCheck struct {
	ruleEnv
}

func (l testParallelCheck) check() {
	funcs := make(map[types.Object]*ast.FuncDecl)

	for _, file := range l.pkg.Files {
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
				funcs[l.pkg.TypesInfo.Defs[fn.Name]] = fn
			}
		}
	}

	for _, file := range l.pkg.TestFiles {
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok &&
				runnableTestPackage(l.pkg.TypesInfo, fn) != nil {
				l.checkTest(fn, funcs)
			}
		}
	}
}

func (l testParallelCheck) checkTest(fn *ast.FuncDecl, funcs map[types.Object]*ast.FuncDecl) {
	var (
		marker *ast.Comment
		reason string
	)

	if fn.Doc != nil {
		for _, comment := range fn.Doc.List {
			if text, ok := SerialMarker(comment.Text); ok {
				marker, reason = comment, text

				break
			}
		}
	}

	var param types.Object
	if names := fn.Type.Params.List[0].Names; len(names) == 1 {
		param = l.pkg.TypesInfo.Defs[names[0]]
	}

	name := fn.Name.Name

	calls := parallelCalls(l.pkg.TypesInfo, fn.Body, param, funcs, l.pkg.ImportedParallelParams)

	switch {
	case calls > 1:
		l.report(fn.Name.Pos(), "test_not_parallel", fmt.Sprintf(
			`test %q calls t.Parallel() more than once, directly or through a helper, and Go panics on the second call; keep only one`,
			name,
		))
	case calls == 1:
		l.checkSubtests(fn, param, funcs)

		if marker != nil {
			l.report(marker.Pos(), "test_not_parallel", fmt.Sprintf(
				`test %q calls t.Parallel(), so its //slopelint:serial marker is stale; remove the marker`,
				name,
			))
		}
	// Go panics when t.Parallel() is combined with t.Setenv or t.Chdir, also
	// when a subtest calls them, so such tests are serial by necessity.
	case l.changesProcessState(fn.Body, funcs, make(map[types.Object]struct{})):
		if marker != nil {
			l.report(marker.Pos(), "test_not_parallel", fmt.Sprintf(
				`test %q uses t.Setenv or t.Chdir, which already forbid t.Parallel(); remove its //slopelint:serial marker`,
				name,
			))
		}
	case marker == nil:
		l.report(fn.Name.Pos(), "test_not_parallel", fmt.Sprintf(
			`test %q does not call t.Parallel(); remove the shared state that forces serial runs and add it, or state the reason with //slopelint:serial <reason>`,
			name,
		))
	case reason == "":
		l.report(marker.Pos(), "test_not_parallel", fmt.Sprintf(
			`test %q has //slopelint:serial without a reason; name the shared state that forces serial runs after the marker`,
			name,
		))
	}
}

// parallelCalls counts the top-level statements of body that call
// t.Parallel() on param, directly or in a helper given param: a same-package
// helper one level deep, or an imported one through its call summary. A
// subtest's Parallel does not make its parent parallel, so function literals
// are not searched.
func parallelCalls(
	info *types.Info,
	body *ast.BlockStmt,
	param types.Object,
	funcs map[types.Object]*ast.FuncDecl,
	imported func(*types.Func) []int,
) int {
	if param == nil {
		return 0
	}

	count := 0

	for _, stmt := range body.List {
		call, ok := exprStmtCall(stmt)
		if !ok {
			continue
		}

		if selector, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr); ok &&
			isPackageFuncCall(info, call, testingImportPath, "Parallel") &&
			identObject(info, selector.X) == param {
			count++

			continue
		}

		if helperCallsParallel(info, call, param, funcs, imported) {
			count++
		}
	}

	return count
}

func helperCallsParallel(
	info *types.Info,
	call *ast.CallExpr,
	param types.Object,
	funcs map[types.Object]*ast.FuncDecl,
	imported func(*types.Func) []int,
) bool {
	fn, _, ok := calledFunc(info, call)
	if !ok {
		return false
	}

	params := fn.Signature().Params()

	for index, arg := range call.Args {
		if identObject(info, arg) != param || index >= params.Len() {
			continue
		}

		if helper := funcs[fn]; helper != nil {
			return parallelCalls(info, helper.Body, params.At(index), nil, imported) > 0
		}

		return imported != nil && slices.Contains(imported(fn), index)
	}

	return false
}

// ParallelParams returns the indexes of fn's parameters that fn calls
// t.Parallel() on, so tests in other packages see helpers that do it.
func ParallelParams(info *types.Info, fn *ast.FuncDecl, imported func(*types.Func) []int) []int {
	obj, ok := info.Defs[fn.Name].(*types.Func)
	if !ok || fn.Body == nil {
		return nil
	}

	var out []int

	params := obj.Signature().Params()
	for index := range params.Len() {
		if parallelCalls(info, fn.Body, params.At(index), nil, imported) > 0 {
			out = append(out, index)
		}
	}

	return out
}

// changesProcessState matches t.Setenv or t.Chdir anywhere in body, or in
// same-package functions it reaches. seen stops recursion through call cycles.
func (l testParallelCheck) changesProcessState(
	body *ast.BlockStmt,
	funcs map[types.Object]*ast.FuncDecl,
	seen map[types.Object]struct{},
) bool {
	found := false

	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if found || !ok {
			return !found
		}

		if isPackageFuncCall(l.pkg.TypesInfo, call, testingImportPath, "Setenv") ||
			isPackageFuncCall(l.pkg.TypesInfo, call, testingImportPath, "Chdir") {
			found = true

			return false
		}

		fn, _, ok := calledFunc(l.pkg.TypesInfo, call)
		if _, visited := seen[fn]; ok && !visited && funcs[fn] != nil {
			seen[fn] = struct{}{}
			found = l.changesProcessState(funcs[fn].Body, funcs, seen)
		}

		return !found
	})

	return found
}

// checkSubtests reports table subtests of a parallel test that do not call
// t.Parallel(). Only t.Run calls in a range loop count: subtests outside a
// loop are often ordered steps. A subtest that writes a variable declared
// outside it shares state with the other cases, so it is not reported.
func (l testParallelCheck) checkSubtests(
	fn *ast.FuncDecl,
	param types.Object,
	funcs map[types.Object]*ast.FuncDecl,
) {
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		loop, ok := node.(*ast.RangeStmt)
		if !ok {
			return true
		}

		for _, stmt := range loop.Body.List {
			call, lit, sub := l.subtest(stmt, param)
			if lit == nil ||
				parallelCalls(
					l.pkg.TypesInfo,
					lit.Body,
					sub,
					funcs,
					l.pkg.ImportedParallelParams,
				) > 0 ||
				l.changesProcessState(lit.Body, funcs, make(map[types.Object]struct{})) ||
				writesOuterVariable(l.pkg.TypesInfo, lit) {
				continue
			}

			l.report(call.Pos(), "test_not_parallel", fmt.Sprintf(
				`subtests of %q do not call t.Parallel(); add it as the first statement of the t.Run function`,
				fn.Name.Name,
			))
		}

		return true
	})
}

// subtest matches param.Run(name, func(t *testing.T) {...}) and returns the
// function literal and its t.
func (l testParallelCheck) subtest(
	stmt ast.Stmt,
	param types.Object,
) (*ast.CallExpr, *ast.FuncLit, types.Object) {
	info := l.pkg.TypesInfo

	call, ok := exprStmtCall(stmt)
	if !ok || len(call.Args) != 2 || !isPackageFuncCall(info, call, testingImportPath, "Run") {
		return nil, nil, nil
	}

	selector, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	lit, isLit := call.Args[1].(*ast.FuncLit)

	if !ok || !isLit || identObject(info, selector.X) != param ||
		len(lit.Type.Params.List) != 1 || len(lit.Type.Params.List[0].Names) != 1 {
		return nil, nil, nil
	}

	return call, lit, info.Defs[lit.Type.Params.List[0].Names[0]]
}

func writesOuterVariable(info *types.Info, lit *ast.FuncLit) bool {
	outer := func(expr ast.Expr) bool {
		obj := info.Uses[assignedRootIdent(expr)]

		return obj != nil && (obj.Pos() < lit.Pos() || obj.Pos() >= lit.End())
	}

	found := false

	ast.Inspect(lit.Body, func(node ast.Node) bool {
		switch stmt := node.(type) {
		case *ast.AssignStmt:
			found = found || slices.ContainsFunc(stmt.Lhs, outer)
		case *ast.IncDecStmt:
			found = found || outer(stmt.X)
		}

		return !found
	})

	return found
}
