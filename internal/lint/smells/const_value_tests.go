package smells

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"strings"
	"unicode"
	"unicode/utf8"
)

const packageAssertionTestingArgCount = 1

type fixedTestValue struct {
	fixed  bool
	fields map[string]fixedTestValue
}

type constValueTestScan struct {
	runner        *Runner
	fn            *ast.FuncDecl
	aliases       map[types.Object]fixedTestValue
	staticWrites  map[types.Object]struct{}
	staticValues  map[types.Object]fixedTestValue
	staticReturns map[string]fixedTestValue
	assertions    int
	restatements  int
}

func (l *Runner) checkConstValueTests() {
	packageScan := constValueTestScan{
		runner:        l,
		aliases:       make(map[types.Object]fixedTestValue),
		staticValues:  make(map[types.Object]fixedTestValue),
		staticReturns: make(map[string]fixedTestValue),
	}
	packageScan.collectStaticValues()
	packageScan.collectStaticReturns()

	for _, file := range l.pkg.TestFiles {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !l.isRunnableTest(fn) {
				continue
			}

			scan := constValueTestScan{
				runner:        l,
				fn:            fn,
				aliases:       make(map[types.Object]fixedTestValue),
				staticWrites:  make(map[types.Object]struct{}),
				staticValues:  packageScan.staticValues,
				staticReturns: packageScan.staticReturns,
			}
			scan.collectFixedAliases()
			ast.Inspect(fn.Body, scan.inspectAssertion)

			if scan.assertions == 0 || scan.assertions != scan.restatements {
				continue
			}

			l.report(
				fn.Name.Pos(),
				"const_value_test",
				fmt.Sprintf(
					`test %q only checks fixed package values against fixed expectations; remove it or test behavior that consumes the value`,
					fn.Name.Name,
				),
			)
		}
	}
}

func (scan *constValueTestScan) collectStaticValues() {
	candidates := make(map[types.Object]ast.Expr)

	for _, decl := range scan.runner.pkg.ProductionDecls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}

		for _, spec := range gen.Specs {
			values, ok := spec.(*ast.ValueSpec)
			if !ok || len(values.Names) != len(values.Values) {
				continue
			}

			for index, name := range values.Names {
				obj, ok := scan.runner.pkg.TypesInfo.Defs[name].(*types.Var)
				if ok && fixedPackageValueType(obj.Type()) {
					candidates[obj] = values.Values[index]
				}
			}
		}
	}

	for _, file := range scan.runner.pkg.ProductionFiles {
		ast.Inspect(file, func(node ast.Node) bool {
			scan.invalidateWrittenStaticValue(node, candidates)

			return true
		})
	}

	scan.resolveStaticValues(candidates)
}

func (scan *constValueTestScan) resolveStaticValues(candidates map[types.Object]ast.Expr) {
	for {
		changed := false

		for obj, expr := range candidates {
			if _, exists := scan.staticValues[obj]; exists {
				continue
			}

			value := scan.fixedValue(expr)
			if !value.fixed {
				continue
			}

			scan.staticValues[obj] = value
			changed = true
		}

		if !changed {
			return
		}
	}
}

func fixedPackageValueType(valueType types.Type) bool {
	switch underlying := valueType.Underlying().(type) {
	case *types.Basic:
		return underlying.Info()&types.IsConstType != 0
	case *types.Struct:
		return true
	default:
		return false
	}
}

func (scan *constValueTestScan) invalidateWrittenStaticValue(
	node ast.Node,
	candidates map[types.Object]ast.Expr,
) {
	var targets []ast.Expr

	switch node := node.(type) {
	case *ast.AssignStmt:
		targets = node.Lhs
	case *ast.IncDecStmt:
		targets = []ast.Expr{node.X}
	case *ast.RangeStmt:
		targets = []ast.Expr{node.Key, node.Value}
	case *ast.UnaryExpr:
		if node.Op == token.AND {
			targets = []ast.Expr{node.X}
		}
	case *ast.CallExpr:
		targets = scan.pointerReceiverTargets(node)
	}

	for _, target := range targets {
		ident := assignedRootIdent(target)
		if ident == nil {
			continue
		}

		delete(candidates, scan.runner.pkg.TypesInfo.ObjectOf(ident))
	}
}

func (scan *constValueTestScan) pointerReceiverTargets(call *ast.CallExpr) []ast.Expr {
	selector, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	if !ok {
		return nil
	}

	selection := scan.runner.pkg.TypesInfo.Selections[selector]
	if selection == nil {
		return nil
	}

	signature, ok := selection.Obj().Type().(*types.Signature)
	if !ok || signature.Recv() == nil {
		return nil
	}

	if _, pointerReceiver := signature.Recv().Type().(*types.Pointer); pointerReceiver {
		return []ast.Expr{selector.X}
	}

	return nil
}

func (scan *constValueTestScan) collectStaticReturns() {
	candidates := make(map[string]ast.Expr)

	for _, fn := range scan.runner.pkg.ProductionFuncs {
		if fn.Body == nil || len(fn.Body.List) != 1 {
			continue
		}

		statement, ok := fn.Body.List[0].(*ast.ReturnStmt)
		if !ok || len(statement.Results) != 1 {
			continue
		}

		obj, ok := scan.runner.pkg.TypesInfo.Defs[fn.Name].(*types.Func)
		if ok {
			candidates[funcObjectKey(obj)] = statement.Results[0]
		}
	}

	// Resolve chains such as Source.Name -> sourceName -> "source" without
	// treating field getters or other runtime expressions as fixed.
	for {
		changed := false

		for key, expr := range candidates {
			if _, exists := scan.staticReturns[key]; exists {
				continue
			}

			value := scan.fixedValue(expr)
			if !value.fixed {
				continue
			}

			scan.staticReturns[key] = value
			changed = true
		}

		if !changed {
			return
		}
	}
}

func (l *Runner) isRunnableTest(fn *ast.FuncDecl) bool {
	if fn == nil || fn.Body == nil || fn.Recv != nil ||
		!goTestName(fn.Name.Name) {
		return false
	}

	return l.hasTestingTSignature(fn.Name)
}

func (l *Runner) hasTestingTSignature(name *ast.Ident) bool {
	obj, ok := l.pkg.TypesInfo.Defs[name].(*types.Func)
	if !ok || obj == nil {
		return false
	}

	sig, ok := obj.Type().(*types.Signature)
	if !ok || sig.Params().Len() != 1 || sig.Results().Len() != 0 {
		return false
	}

	ptr, ok := sig.Params().At(0).Type().(*types.Pointer)
	if !ok {
		return false
	}

	named, ok := ptr.Elem().(*types.Named)

	return ok && named.Obj().Name() == "T" && named.Obj().Pkg() != nil &&
		named.Obj().Pkg().Path() == testingImportPath
}

func goTestName(name string) bool {
	const prefix = "Test"

	if !strings.HasPrefix(name, prefix) || len(name) == len(prefix) {
		return false
	}

	first, _ := utf8.DecodeRuneInString(name[len(prefix):])

	return !unicode.IsLower(first)
}

func (scan *constValueTestScan) collectFixedAliases() {
	candidates := make(map[types.Object]ast.Expr)
	writes := make(map[types.Object]int)

	ast.Inspect(scan.fn.Body, func(node ast.Node) bool {
		scan.collectAliasCandidate(node, candidates, writes)

		return true
	})
	scan.resolveFixedAliases(candidates)
}

func (scan *constValueTestScan) collectAliasCandidate(
	node ast.Node,
	candidates map[types.Object]ast.Expr,
	writes map[types.Object]int,
) {
	switch node := node.(type) {
	case *ast.AssignStmt:
		scan.collectAssignmentAliases(node.Lhs, node.Rhs, candidates, writes)
	case *ast.ValueSpec:
		lhs := make([]ast.Expr, len(node.Names))
		for index, name := range node.Names {
			lhs[index] = name
		}

		scan.collectAssignmentAliases(lhs, node.Values, candidates, writes)
	case *ast.IncDecStmt:
		scan.recordAliasWrite(node.X, nil, candidates, writes)
	case *ast.RangeStmt:
		scan.recordAliasWrite(node.Key, nil, candidates, writes)
		scan.recordAliasWrite(node.Value, nil, candidates, writes)
	case *ast.UnaryExpr:
		if node.Op == token.AND {
			scan.recordAliasWrite(node.X, nil, candidates, writes)
		}
	case *ast.CallExpr:
		scan.recordPointerReceiverWrite(node, candidates, writes)
	}
}

func (scan *constValueTestScan) collectAssignmentAliases(
	lhs []ast.Expr,
	rhs []ast.Expr,
	candidates map[types.Object]ast.Expr,
	writes map[types.Object]int,
) {
	if len(lhs) != len(rhs) {
		for _, expr := range lhs {
			scan.recordAliasWrite(expr, nil, candidates, writes)
		}

		return
	}

	for index, expr := range lhs {
		scan.recordAliasWrite(expr, rhs[index], candidates, writes)
	}
}

func (scan *constValueTestScan) recordPointerReceiverWrite(
	call *ast.CallExpr,
	candidates map[types.Object]ast.Expr,
	writes map[types.Object]int,
) {
	for _, target := range scan.pointerReceiverTargets(call) {
		scan.recordAliasWrite(target, nil, candidates, writes)
	}
}

func (scan *constValueTestScan) resolveFixedAliases(candidates map[types.Object]ast.Expr) {
	for {
		changed := false

		for obj, expr := range candidates {
			if _, exists := scan.aliases[obj]; exists {
				continue
			}

			value := scan.fixedValue(expr)
			if !value.fixed {
				continue
			}

			scan.aliases[obj] = value
			changed = true
		}

		if !changed {
			return
		}
	}
}

func (scan *constValueTestScan) recordAliasWrite(
	expr ast.Expr,
	value ast.Expr,
	candidates map[types.Object]ast.Expr,
	writes map[types.Object]int,
) {
	ident := assignedRootIdent(expr)
	if ident == nil {
		return
	}

	obj := scan.runner.pkg.TypesInfo.ObjectOf(ident)
	if _, fixed := scan.staticValues[obj]; fixed {
		scan.staticWrites[obj] = struct{}{}
	}

	if obj == nil || obj.Pos() < scan.fn.Pos() || obj.Pos() > scan.fn.End() {
		return
	}

	writes[obj]++
	if writes[obj] == 1 && value != nil {
		candidates[obj] = value
		return
	}

	delete(candidates, obj)
}

func assignedRootIdent(expr ast.Expr) *ast.Ident {
	for expr != nil {
		switch target := ast.Unparen(expr).(type) {
		case *ast.Ident:
			return target
		case *ast.SelectorExpr:
			expr = target.X
		case *ast.IndexExpr:
			expr = target.X
		case *ast.IndexListExpr:
			expr = target.X
		case *ast.StarExpr:
			expr = target.X
		default:
			return nil
		}
	}

	return nil
}

func (scan *constValueTestScan) inspectAssertion(node ast.Node) bool {
	switch node := node.(type) {
	case *ast.IfStmt:
		if !scan.hasTestingFailure(node.Body) {
			return true
		}

		scan.assertions++

		value := scan.fixedValue(node.Cond)
		if value.fixed {
			scan.restatements++
		}
	case *ast.CallExpr:
		fn := scan.assertionFunc(node)
		if fn == nil {
			return true
		}

		scan.assertions++
		if scan.assertionCallRestates(node, fn.Name()) {
			scan.restatements++
		}
	}

	return true
}

func (scan *constValueTestScan) hasTestingFailure(block *ast.BlockStmt) bool {
	found := false

	ast.Inspect(block, func(node ast.Node) bool {
		if found {
			return false
		}

		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}

		fn := scan.runner.funcObject(call.Fun)
		found = fn != nil && fn.Pkg() != nil && fn.Pkg().Path() == testingImportPath &&
			testingFailureName(fn.Name())

		return !found
	})

	return found
}

func (scan *constValueTestScan) assertionFunc(call *ast.CallExpr) *types.Func {
	fn := scan.runner.funcObject(call.Fun)
	if fn == nil || fn.Pkg() == nil || fn.Pkg() == scan.runner.pkg.TypesPkg {
		return nil
	}

	switch fn.Pkg().Path() {
	case "github.com/stretchr/testify/assert",
		"github.com/stretchr/testify/require",
		"gotest.tools/v3/assert":
		return fn
	default:
		return nil
	}
}

func (scan *constValueTestScan) assertionCallRestates(call *ast.CallExpr, name string) bool {
	firstValueIndex := packageAssertionTestingArgCount
	if len(call.Args) <= firstValueIndex {
		return false
	}

	first := scan.fixedValue(call.Args[firstValueIndex])
	if unaryAssertionName(name) {
		return first.fixed
	}

	secondValueIndex := firstValueIndex + 1
	if len(call.Args) <= secondValueIndex {
		return false
	}

	second := scan.fixedValue(call.Args[secondValueIndex])

	return first.fixed && second.fixed
}

func unaryAssertionName(name string) bool {
	switch name {
	case "Empty", "False", "Nil", "NotEmpty", "NotNil", "NotZero", "True", "Zero":
		return true
	default:
		return false
	}
}

func (scan *constValueTestScan) fixedValue(expr ast.Expr) fixedTestValue {
	switch expr := ast.Unparen(expr).(type) {
	case *ast.BasicLit:
		return fixedTestValue{fixed: true}
	case *ast.Ident:
		return scan.fixedIdentValue(expr)
	case *ast.SelectorExpr:
		return scan.fixedSelectorValue(expr)
	case *ast.UnaryExpr:
		return scan.fixedUnaryValue(expr)
	case *ast.BinaryExpr:
		return mergeFixedTestValues(scan.fixedValue(expr.X), scan.fixedValue(expr.Y))
	case *ast.CompositeLit:
		return scan.fixedCompositeValue(expr)
	case *ast.CallExpr:
		return scan.fixedCallValue(expr)
	default:
		return fixedTestValue{}
	}
}

func (scan *constValueTestScan) fixedIdentValue(ident *ast.Ident) fixedTestValue {
	obj := scan.runner.pkg.TypesInfo.ObjectOf(ident)
	if value, ok := scan.aliases[obj]; ok {
		return value
	}

	if _, written := scan.staticWrites[obj]; written {
		return fixedTestValue{}
	}

	if value, ok := scan.staticValues[obj]; ok {
		return value
	}

	switch obj.(type) {
	case *types.Const, *types.Nil:
		return fixedTestValue{fixed: true}
	default:
		return fixedTestValue{}
	}
}

func (scan *constValueTestScan) fixedSelectorValue(selector *ast.SelectorExpr) fixedTestValue {
	if _, ok := scan.runner.pkg.TypesInfo.ObjectOf(selector.Sel).(*types.Const); ok {
		return fixedTestValue{fixed: true}
	}

	base := scan.fixedValue(selector.X)

	field, ok := base.fields[selector.Sel.Name]
	if !ok {
		return fixedTestValue{}
	}

	return field
}

func (scan *constValueTestScan) fixedUnaryValue(expr *ast.UnaryExpr) fixedTestValue {
	if expr.Op == token.AND || expr.Op == token.ARROW {
		return fixedTestValue{}
	}

	return scan.fixedValue(expr.X)
}

func (scan *constValueTestScan) fixedCompositeValue(expr *ast.CompositeLit) fixedTestValue {
	compositeType := scan.runner.pkg.TypesInfo.TypeOf(expr)
	if compositeType == nil {
		return fixedTestValue{}
	}

	structType, ok := compositeType.Underlying().(*types.Struct)
	if !ok {
		return fixedTestValue{}
	}

	value := fixedTestValue{fixed: true, fields: make(map[string]fixedTestValue)}

	for index, element := range expr.Elts {
		name, fieldExpr, ok := compositeField(structType, index, element)
		if !ok {
			return fixedTestValue{}
		}

		fieldValue := scan.fixedValue(fieldExpr)
		if !fieldValue.fixed {
			return fixedTestValue{}
		}

		value.fields[name] = fieldValue
	}

	for field := range structType.Fields() {
		if _, exists := value.fields[field.Name()]; !exists {
			value.fields[field.Name()] = fixedTestValue{fixed: true}
		}
	}

	return value
}

func compositeField(
	structType *types.Struct,
	index int,
	element ast.Expr,
) (string, ast.Expr, bool) {
	field, keyed := element.(*ast.KeyValueExpr)
	if !keyed {
		if index >= structType.NumFields() {
			return "", nil, false
		}

		return structType.Field(index).Name(), element, true
	}

	name, ok := field.Key.(*ast.Ident)
	if !ok {
		return "", nil, false
	}

	return name.Name, field.Value, true
}

func (scan *constValueTestScan) fixedCallValue(call *ast.CallExpr) fixedTestValue {
	if fn := scan.runner.funcObject(call.Fun); fn != nil {
		if value, ok := scan.staticReturns[funcObjectKey(fn)]; ok {
			return value
		}

		if fixedStringPredicate(fn) {
			return scan.fixedCallArgs(call.Args)
		}
	}

	name := callName(call.Fun)
	if name == nil {
		return fixedTestValue{}
	}

	obj := scan.runner.pkg.TypesInfo.ObjectOf(name)
	switch obj := obj.(type) {
	case *types.Builtin:
		if !constantBuiltinName(obj.Name()) {
			return fixedTestValue{}
		}

		return scan.fixedCallArgs(call.Args)
	case *types.TypeName:
		return scan.fixedCallArgs(call.Args)
	default:
		return fixedTestValue{}
	}
}

func fixedStringPredicate(fn *types.Func) bool {
	if fn.Pkg() == nil || fn.Pkg().Path() != stringsImportPath {
		return false
	}

	switch fn.Name() {
	case "Contains", "ContainsAny", "ContainsRune", "HasPrefix", "HasSuffix":
		return true
	default:
		return false
	}
}

func (scan *constValueTestScan) fixedCallArgs(args []ast.Expr) fixedTestValue {
	value := fixedTestValue{fixed: true}
	for _, arg := range args {
		value = mergeFixedTestValues(value, scan.fixedValue(arg))
	}

	return value
}

func constantBuiltinName(name string) bool {
	switch name {
	case "cap", "complex", "imag", "len", "max", "min", "real":
		return true
	default:
		return false
	}
}

func mergeFixedTestValues(left, right fixedTestValue) fixedTestValue {
	return fixedTestValue{
		fixed: left.fixed && right.fixed,
	}
}

func callName(expr ast.Expr) *ast.Ident {
	switch expr := ast.Unparen(expr).(type) {
	case *ast.Ident:
		return expr
	case *ast.SelectorExpr:
		return expr.Sel
	default:
		return nil
	}
}

func testingFailureName(name string) bool {
	switch name {
	case "Error", "Errorf", "Fail", "FailNow", "Fatal", "Fatalf":
		return true
	default:
		return false
	}
}
