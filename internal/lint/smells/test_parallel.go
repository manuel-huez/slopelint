package smells

import (
	"fmt"
	"go/ast"
	"go/types"
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

	switch {
	case l.callsParallel(fn.Body, param, funcs):
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

// callsParallel matches t.Parallel() among the top-level statements of body,
// directly or in a same-package helper given t. A subtest's Parallel does not
// make its parent parallel, so function literals are not searched.
func (l testParallelCheck) callsParallel(
	body *ast.BlockStmt,
	param types.Object,
	funcs map[types.Object]*ast.FuncDecl,
) bool {
	if param == nil {
		return false
	}

	info := l.pkg.TypesInfo

	for _, stmt := range body.List {
		call, ok := exprStmtCall(stmt)
		if !ok {
			continue
		}

		if selector, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr); ok &&
			isPackageFuncCall(info, call, testingImportPath, "Parallel") &&
			identObject(info, selector.X) == param {
			return true
		}

		if fn, _, ok := calledFunc(info, call); ok && funcs[fn] != nil &&
			l.helperCallsParallel(call, fn, param, funcs[fn]) {
			return true
		}
	}

	return false
}

// helperCallsParallel checks one helper level: the helper's own calls are
// matched only as direct t.Parallel() calls on the parameter given t.
func (l testParallelCheck) helperCallsParallel(
	call *ast.CallExpr,
	fn *types.Func,
	param types.Object,
	helper *ast.FuncDecl,
) bool {
	params := fn.Signature().Params()

	for index, arg := range call.Args {
		if index < params.Len() && identObject(l.pkg.TypesInfo, arg) == param &&
			l.callsParallel(helper.Body, params.At(index), nil) {
			return true
		}
	}

	return false
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
