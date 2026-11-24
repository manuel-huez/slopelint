package smells

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"path/filepath"
	"slices"
	"strings"
)

type functionVarDecl struct {
	name string
	pos  token.Pos
}

// testSupportCheck reports test code that stubs package function variables,
// panics after a fatal call, sleeps, or shares helpers without a test_support
// filename.
type testSupportCheck struct {
	ruleEnv
}

func (l testSupportCheck) checkGlobalFuncStubs() {
	functionVars := l.productionFunctionVars()
	if len(functionVars) == 0 {
		return
	}

	for obj, assignPos := range l.testAssignedFunctionVars(functionVars) {
		decl := functionVars[obj]

		l.report(
			decl.pos,
			"test_global_func_stub",
			fmt.Sprintf(
				`package-level function variable %q is reassigned in tests at %s`,
				decl.name,
				l.positionText(assignPos),
			),
		)
	}
}

func (l testSupportCheck) productionFunctionVars() map[types.Object]functionVarDecl {
	out := make(map[types.Object]functionVarDecl)

	for _, decl := range l.pkg.ProductionDecls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}

		for _, spec := range gen.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}

			for _, name := range valueSpec.Names {
				if ast.IsExported(name.Name) {
					continue
				}

				obj, ok := l.pkg.TypesInfo.Defs[name].(*types.Var)
				if !ok || !typeIsFunctionSignature(obj.Type()) {
					continue
				}

				out[obj] = functionVarDecl{name: name.Name, pos: name.Pos()}
			}
		}
	}

	return out
}

func (l testSupportCheck) testAssignedFunctionVars(
	functionVars map[types.Object]functionVarDecl,
) map[types.Object]token.Pos {
	out := make(map[types.Object]token.Pos)

	for _, file := range l.pkg.Files {
		if !l.fileIsTest(file) {
			continue
		}

		ast.Inspect(file, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok || assign.Tok != token.ASSIGN {
				return true
			}

			for _, lhs := range assign.Lhs {
				ident, ok := ast.Unparen(lhs).(*ast.Ident)
				if !ok {
					continue
				}

				obj := l.pkg.TypesInfo.ObjectOf(ident)
				if _, ok := functionVars[obj]; !ok {
					continue
				}

				if _, seen := out[obj]; !seen {
					out[obj] = ident.Pos()
				}
			}

			return true
		})
	}

	return out
}

func (l testSupportCheck) checkFatalPanics() {
	for _, file := range l.pkg.TestFiles {
		ast.Inspect(file, func(node ast.Node) bool {
			block, ok := node.(*ast.BlockStmt)
			if !ok {
				return true
			}

			for index := 1; index < len(block.List); index++ {
				fatal := testingMethodStmt(l.pkg.TypesInfo, block.List[index-1])
				if fatal != "Fatal" && fatal != "Fatalf" {
					continue
				}

				panicCall, ok := exprStmtCall(block.List[index])
				if !ok || !isBuiltinCall(l.pkg.TypesInfo, panicCall, panicText) {
					continue
				}

				l.report(
					panicCall.Pos(),
					"test_fatal_panic",
					`panic after testing fatal call is unreachable test ceremony`,
				)
			}

			return true
		})
	}
}

func testingMethodStmt(info *types.Info, stmt ast.Stmt) string {
	call, ok := exprStmtCall(stmt)
	if !ok {
		return ""
	}

	fn, _, ok := calledFunc(info, call)
	if !ok || fn.Pkg() == nil || fn.Pkg().Path() != testingImportPath {
		return ""
	}

	return fn.Name()
}

func exprStmtCall(stmt ast.Stmt) (*ast.CallExpr, bool) {
	exprStmt, ok := stmt.(*ast.ExprStmt)
	if !ok {
		return nil, false
	}

	call, ok := ast.Unparen(exprStmt.X).(*ast.CallExpr)

	return call, ok
}

func (l testSupportCheck) checkSleeps() {
	for _, file := range l.pkg.TestFiles {
		// Inside a synctest bubble time.Sleep advances the fake clock, also in
		// helpers the bubble calls, so files that use synctest are not judged.
		if slices.ContainsFunc(file.Imports, func(spec *ast.ImportSpec) bool {
			return spec.Path.Value == `"testing/synctest"`
		}) {
			continue
		}

		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if ok && isPackageFuncCall(l.pkg.TypesInfo, call, "time", "Sleep") {
				l.report(
					call.Pos(),
					"test_sleep",
					`time.Sleep in test code`,
				)
			}

			return true
		})
	}
}

func (l testSupportCheck) checkFilenames() {
	// A package whose import path already says test, such as
	// internal/teststructs, marks every file in it.
	if slices.ContainsFunc(strings.Split(l.pkg.TypesPkg.Path(), "/"), func(part string) bool {
		return strings.Contains(part, "test")
	}) {
		return
	}

	for _, file := range l.pkg.TestSupportFiles {
		if ast.IsGenerated(file) {
			continue
		}

		name := strings.TrimSuffix(filepath.Base(l.fileName(file)), ".go")
		// A delimited marker also permits Go's trailing OS and architecture suffixes.
		if strings.Contains("_"+name+"_", "_test_support_") {
			continue
		}

		l.report(
			file.Package,
			"test_support_filename",
			"shared test-only source lacks the test_support filename marker",
		)
	}
}

const (
	tableTestCaseLimit       = 10
	repeatedFixtureMinLength = 16
	// RepeatedTestFixture is the kind of a fixture literal written three times;
	// code review drops test group findings over the same tests.
	RepeatedTestFixture = "repeated_test_fixture"
)

type tableTestCheck struct {
	ruleEnv
}

func (l tableTestCheck) checkUnnamedLargeTables() {
	for _, file := range l.pkg.Files {
		if !l.fileIsTest(file) {
			continue
		}

		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}

			structType, ok := anonymousStructSliceType(lit.Type)
			if !ok || len(lit.Elts) <= tableTestCaseLimit ||
				structTypeHasCaseNameField(structType) {
				return true
			}

			l.report(
				lit.Pos(),
				"table_test_grouping",
				fmt.Sprintf(
					"table test has %d cases without name/desc field",
					len(lit.Elts),
				),
			)

			return true
		})
	}
}

func anonymousStructSliceType(expr ast.Expr) (*ast.StructType, bool) {
	arrayType, ok := expr.(*ast.ArrayType)
	if !ok {
		return nil, false
	}

	structType, ok := arrayType.Elt.(*ast.StructType)

	return structType, ok
}

func structTypeHasCaseNameField(structType *ast.StructType) bool {
	for _, field := range structType.Fields.List {
		for _, name := range field.Names {
			if caseNameField(name.Name) {
				return true
			}
		}
	}

	return false
}

func caseNameField(name string) bool {
	switch strings.ToLower(name) {
	case "name", "desc", "description":
		return true
	default:
		return false
	}
}

func (l tableTestCheck) checkRepeatedFixtures() {
	const reportCount = 3

	counts := make(map[string]int)

	for _, file := range l.pkg.TestFiles {
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) != 3 || !isTestFixtureWriteCall(l.pkg.TypesInfo, call) {
				return true
			}

			value, ok := l.pkg.TypesInfo.Types[call.Args[2]]
			if !ok || value.Value == nil || value.Value.Kind() != constant.String {
				return true
			}

			contents := constant.StringVal(value.Value)
			if len(contents) < repeatedFixtureMinLength {
				return true
			}

			counts[contents]++
			if counts[contents] == reportCount {
				l.report(
					call.Args[2].Pos(),
					RepeatedTestFixture,
					fmt.Sprintf(
						"test fixture content is repeated %d times",
						reportCount,
					),
				)
			}

			return true
		})
	}
}

func isTestFixtureWriteCall(info *types.Info, call *ast.CallExpr) bool {
	ident, ok := ast.Unparen(call.Fun).(*ast.Ident)
	if !ok || ident.Name != "writeFile" {
		return false
	}

	_, ok = info.ObjectOf(ident).(*types.Func)

	return ok
}

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
