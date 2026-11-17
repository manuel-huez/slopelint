package smells

import (
	"go/ast"
	"go/types"
	"strings"
)

// testFailureScan looks for any way a runnable test can fail. A testing value
// that escapes to other code counts, because that code may fail the test.
type testFailureScan struct {
	info  *types.Info
	tb    *types.Interface
	found bool
}

// testLacksFailurePath reports test bodies that can never fail. A test that
// skips in its first statement never runs the rest, so it is not reported.
func testLacksFailurePath(info *types.Info, testingPkg *types.Package, body *ast.BlockStmt) bool {
	tbName := testingPkg.Scope().Lookup("TB")
	if tbName == nil {
		return false
	}

	tb, ok := tbName.Type().Underlying().(*types.Interface)
	if !ok ||
		len(body.List) > 0 && strings.HasPrefix(testingMethodStmt(info, body.List[0]), "Skip") {
		return false
	}

	scan := testFailureScan{info: info, tb: tb}
	ast.Inspect(body, scan.inspect)

	return !scan.found
}

func (scan *testFailureScan) inspect(node ast.Node) bool {
	switch node := node.(type) {
	case *ast.CallExpr:
		scan.found = scan.found || scan.stopsProcess(node)
		scan.escape(node.Args...)
	case *ast.SelectorExpr:
		scan.inspectMethod(node)
	case *ast.CompositeLit:
		for _, element := range node.Elts {
			if field, ok := element.(*ast.KeyValueExpr); ok {
				element = field.Value
			}

			scan.escape(element)
		}
	case *ast.AssignStmt:
		scan.escape(node.Rhs...)
	case *ast.ValueSpec:
		scan.escape(node.Values...)
	case *ast.ReturnStmt:
		scan.escape(node.Results...)
	case *ast.SendStmt:
		scan.escape(node.Value)
	}

	return !scan.found
}

// stopsProcess matches panics and exits. os.Exit counts because helper-process
// tests report to the parent test through the exit code.
func (scan *testFailureScan) stopsProcess(call *ast.CallExpr) bool {
	if isBuiltinCall(scan.info, call, panicText) {
		return true
	}

	fn, _, ok := calledFunc(scan.info, call)
	if !ok || fn.Pkg() == nil {
		return false
	}

	switch fn.Pkg().Path() {
	case "log":
		return strings.HasPrefix(fn.Name(), "Fatal") || strings.HasPrefix(fn.Name(), "Panic")
	case "os":
		return fn.Name() == "Exit"
	default:
		return false
	}
}

// inspectMethod matches calls and method values on testing values, so both
// t.Fatal(...) and check := t.Errorf count.
func (scan *testFailureScan) inspectMethod(selector *ast.SelectorExpr) {
	selection := scan.info.Selections[selector]
	if selection == nil || selection.Kind() == types.FieldVal ||
		!scan.testingValue(scan.info.TypeOf(selector.X)) {
		return
	}

	method := selection.Obj()
	if method.Pkg() == nil || method.Pkg().Path() != testingImportPath ||
		testingFailureName(method.Name()) {
		scan.found = true
	}
}

// escape marks testing values, and functions that take one, handed to other
// code. Function literals are skipped because ast.Inspect scans their bodies.
func (scan *testFailureScan) escape(exprs ...ast.Expr) {
	for _, expr := range exprs {
		if _, literal := ast.Unparen(expr).(*ast.FuncLit); literal {
			continue
		}

		valueType := scan.info.TypeOf(expr)
		if scan.testingValue(valueType) || scan.takesTestingValue(valueType) {
			scan.found = true
		}
	}
}

func (scan *testFailureScan) takesTestingValue(valueType types.Type) bool {
	if valueType == nil {
		return false
	}

	signature, ok := valueType.Underlying().(*types.Signature)
	if !ok {
		return false
	}

	for param := range signature.Params().Variables() {
		if scan.testingValue(param.Type()) {
			return true
		}
	}

	return false
}

func (scan *testFailureScan) testingValue(valueType types.Type) bool {
	switch valueType.(type) {
	case nil, *types.Basic, *types.Tuple:
		// Implements is unspecified for untyped and invalid types.
		return false
	default:
		return types.Implements(valueType, scan.tb)
	}
}
