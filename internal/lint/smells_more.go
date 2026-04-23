package lint

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"slices"
	"strings"
)

const (
	duplicateValidationMinimumGuards = 3
	privateHelperMaxBodyStmts        = 3
	privateHelperMaxParams           = 2
	optionsOverkillMaxCallsites      = 2
	resultWrapperFieldCount          = 2
)

type validationLadder struct {
	key    string
	fnName string
	pos    token.Pos
}

type privateInterfaceDecl struct {
	name string
	pos  token.Pos
	obj  *types.TypeName
}

func (l *linter) checkDuplicateValidationLadders() {
	seen := make(map[string]validationLadder)

	l.forEachProductionFunc(func(fn *ast.FuncDecl) {
		if fn.Body == nil || fn.Name == nil {
			return
		}

		ladder, ok := l.validationLadderForFunc(fn)
		if !ok {
			return
		}

		prior, dup := seen[ladder.key]
		if !dup {
			seen[ladder.key] = ladder
			return
		}

		l.report(
			ladder.pos,
			"duplicate_validation",
			fmt.Sprintf(
				`validation ladder in %q duplicates %q; extract shared validation`,
				ladder.fnName,
				prior.fnName,
			),
		)
	})
}

func (l *linter) validationLadderForFunc(fn *ast.FuncDecl) (validationLadder, bool) {
	parts := make([]string, 0, duplicateValidationMinimumGuards)

	var first token.Pos

	for _, stmt := range fn.Body.List {
		guard, ok := l.validationGuardShape(stmt)
		if !ok {
			break
		}

		if first == token.NoPos {
			first = stmt.Pos()
		}

		parts = append(parts, guard)
	}

	if len(parts) < duplicateValidationMinimumGuards {
		return validationLadder{}, false
	}

	return validationLadder{
		key:    strings.Join(parts, "\n"),
		fnName: fn.Name.Name,
		pos:    first,
	}, true
}

func (l *linter) validationGuardShape(stmt ast.Stmt) (string, bool) {
	if l.hasAttachedComment(stmt) {
		return "", false
	}

	ifStmt, ok := stmt.(*ast.IfStmt)
	if !ok || ifStmt.Init != nil || ifStmt.Else != nil {
		return "", false
	}

	if !l.isValidationPredicate(ifStmt.Cond) {
		return "", false
	}

	if len(ifStmt.Body.List) != 1 {
		return "", false
	}

	ret, ok := ifStmt.Body.List[0].(*ast.ReturnStmt)
	if !ok {
		return "", false
	}

	return l.render(ifStmt.Cond) + " => " + l.render(ret), true
}

func (l *linter) isValidationPredicate(expr ast.Expr) bool {
	expr = l.unparen(expr)

	binary, ok := expr.(*ast.BinaryExpr)
	if !ok || !isValidationCompareOp(binary.Op) {
		return false
	}

	if _, _, ok := l.symbolScalar(binary.X, binary.Y); ok {
		return true
	}

	if _, _, ok := l.symbolScalar(binary.Y, binary.X); ok {
		return true
	}

	return false
}

func isValidationCompareOp(op token.Token) bool {
	return op == token.EQL ||
		op == token.NEQ ||
		op == token.LSS ||
		op == token.LEQ ||
		op == token.GTR ||
		op == token.GEQ
}

func (l *linter) checkSingleUsePrivateHelpers() {
	callCounts := l.productionPackageCallCounts()

	l.forEachProductionFunc(func(fn *ast.FuncDecl) {
		if !l.isSingleUsePrivateHelper(fn, callCounts) {
			return
		}

		l.report(
			fn.Name.Pos(),
			"abstraction_overkill",
			fmt.Sprintf(
				`private helper %q has one production callsite and a tiny body; inline or give it a stronger role`,
				fn.Name.Name,
			),
		)

		l.reportGenericNameForSingleUseHelper(fn)
	})
}

func (l *linter) isSingleUsePrivateHelper(
	fn *ast.FuncDecl,
	callCounts map[string]int,
) bool {
	if !isEligiblePrivateSmellFunc(fn) || fn.Name.Name == "init" {
		return false
	}

	if funcParamCount(fn.Type.Results) != 0 ||
		funcParamCount(fn.Type.Params) > privateHelperMaxParams {
		return false
	}

	obj, ok := l.pkg.TypesInfo.ObjectOf(fn.Name).(*types.Func)
	if !ok || obj == nil || callCounts[funcObjectKey(obj)] != 1 {
		return false
	}

	if len(fn.Body.List) == 0 || len(fn.Body.List) > privateHelperMaxBodyStmts {
		return false
	}

	if l.privateHelperOverlapsTrivialForwarder(fn, obj) {
		return false
	}

	return l.privateHelperBodyIsTiny(fn.Body)
}

func (l *linter) privateHelperOverlapsTrivialForwarder(fn *ast.FuncDecl, obj *types.Func) bool {
	call, ok := l.trivialForwarderBodyCall(fn, obj)
	if !ok {
		return false
	}

	return l.samePackageForwardTarget(obj, call)
}

func (l *linter) privateHelperBodyIsTiny(body *ast.BlockStmt) bool {
	if l.hasAttachedComment(body) {
		return false
	}

	for _, stmt := range body.List {
		if l.hasAttachedComment(stmt) || !privateHelperStmtIsTiny(stmt) {
			return false
		}
	}

	return true
}

func privateHelperStmtIsTiny(stmt ast.Stmt) bool {
	if privateHelperStmtHasComplexNode(stmt) {
		return false
	}

	switch stmt := stmt.(type) {
	case *ast.ExprStmt, *ast.AssignStmt, *ast.SendStmt, *ast.IncDecStmt:
		return true
	case *ast.DeclStmt:
		decl, ok := stmt.Decl.(*ast.GenDecl)
		return ok && decl.Tok == token.VAR
	default:
		return false
	}
}

func privateHelperStmtHasComplexNode(stmt ast.Stmt) bool {
	complex := false

	ast.Inspect(stmt, func(n ast.Node) bool {
		if complex {
			return false
		}

		switch n.(type) {
		case *ast.FuncLit, *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt,
			*ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt,
			*ast.GoStmt, *ast.DeferStmt:
			complex = true
			return false
		default:
			return true
		}
	})

	return complex
}

func (l *linter) checkSingleImplInterfaces() {
	interfaces := l.privateInterfaces()
	if len(interfaces) == 0 {
		return
	}

	impls := l.namedConcreteTypes()

	for _, ifaceDecl := range interfaces {
		iface, ok := ifaceDecl.obj.Type().Underlying().(*types.Interface)
		if !ok || !interfaceEligibleForSingleImpl(iface) {
			continue
		}

		implName, ok := singleInterfaceImplementation(iface, impls)
		if !ok {
			continue
		}

		l.report(
			ifaceDecl.pos,
			"abstraction_overkill",
			fmt.Sprintf(
				`private interface %q has one in-package implementation %q; use concrete type unless substitution is needed`,
				ifaceDecl.name,
				implName,
			),
		)
	}
}

func (l *linter) privateInterfaces() []privateInterfaceDecl {
	out := make([]privateInterfaceDecl, 0)

	l.forEachProductionTypeSpec(func(typeSpec *ast.TypeSpec) {
		if typeSpec.Name == nil || ast.IsExported(typeSpec.Name.Name) {
			return
		}

		if _, ok := typeSpec.Type.(*ast.InterfaceType); !ok {
			return
		}

		obj, ok := l.pkg.TypesInfo.Defs[typeSpec.Name].(*types.TypeName)
		if !ok || obj == nil {
			return
		}

		out = append(out, privateInterfaceDecl{
			name: typeSpec.Name.Name,
			pos:  typeSpec.Name.Pos(),
			obj:  obj,
		})
	})

	return out
}

func interfaceEligibleForSingleImpl(iface *types.Interface) bool {
	if iface == nil || iface.NumEmbeddeds() != 0 || iface.NumMethods() == 0 {
		return false
	}

	for idx := range iface.NumMethods() {
		if !iface.Method(idx).Exported() {
			return false
		}
	}

	return true
}

func (l *linter) namedConcreteTypes() []*types.Named {
	out := make([]*types.Named, 0)

	l.forEachProductionTypeSpec(func(typeSpec *ast.TypeSpec) {
		if typeSpec.Name == nil {
			return
		}

		obj, ok := l.pkg.TypesInfo.Defs[typeSpec.Name].(*types.TypeName)
		if !ok || obj == nil {
			return
		}

		named, ok := obj.Type().(*types.Named)
		if !ok {
			return
		}

		if _, isIface := named.Underlying().(*types.Interface); isIface {
			return
		}

		out = append(out, named)
	})

	return out
}

func singleInterfaceImplementation(
	iface *types.Interface,
	candidates []*types.Named,
) (string, bool) {
	var found string

	for _, named := range candidates {
		if !types.Implements(named, iface) && !types.Implements(types.NewPointer(named), iface) {
			continue
		}

		if found != "" {
			return "", false
		}

		found = named.Obj().Name()
	}

	return found, found != ""
}

func (l *linter) checkOptionsOverkill() {
	callCounts := l.productionPackageCallCounts()

	l.forEachProductionFunc(func(fn *ast.FuncDecl) {
		obj, ok := l.privateFuncWithFunctionalOptions(fn, callCounts)
		if !ok {
			return
		}

		l.report(
			fn.Name.Pos(),
			"api_overkill",
			fmt.Sprintf(
				`private API %q uses functional options for %d production callsites; pass config directly`,
				fn.Name.Name,
				callCounts[funcObjectKey(obj)],
			),
		)
	})
}

func (l *linter) privateFuncWithFunctionalOptions(
	fn *ast.FuncDecl,
	callCounts map[string]int,
) (*types.Func, bool) {
	if !isEligiblePrivateSmellFunc(fn) || !privateConstructorName(fn.Name.Name) {
		return nil, false
	}

	obj, ok := l.pkg.TypesInfo.ObjectOf(fn.Name).(*types.Func)
	if !ok || obj == nil {
		return nil, false
	}

	count := callCounts[funcObjectKey(obj)]
	if count == 0 || count > optionsOverkillMaxCallsites {
		return nil, false
	}

	if !funcHasFunctionalOptionParam(l.pkg.TypesInfo, fn.Type.Params) {
		return nil, false
	}

	return obj, true
}

func privateConstructorName(name string) bool {
	return strings.HasPrefix(name, "new") || strings.HasPrefix(name, "build")
}

func funcHasFunctionalOptionParam(info *types.Info, fields *ast.FieldList) bool {
	if fields == nil {
		return false
	}

	for _, field := range fields.List {
		ellipsis, ok := field.Type.(*ast.Ellipsis)
		if !ok {
			continue
		}

		if typeIsFunctionOption(info.TypeOf(ellipsis.Elt)) {
			return true
		}
	}

	return false
}

func typeIsFunctionOption(typ types.Type) bool {
	if typ == nil {
		return false
	}

	if _, ok := typ.Underlying().(*types.Signature); ok {
		return true
	}

	return false
}

func (l *linter) checkInternalResultWrappers() {
	methodCounts := l.methodCountsByReceiverName()

	l.forEachProductionTypeSpec(func(typeSpec *ast.TypeSpec) {
		if !l.isInternalResultWrapper(typeSpec, methodCounts) {
			return
		}

		l.report(
			typeSpec.Name.Pos(),
			"result_wrapper",
			fmt.Sprintf(
				`private result wrapper %q only carries value plus status; return ordinary Go results`,
				typeSpec.Name.Name,
			),
		)
	})
}

func (l *linter) isInternalResultWrapper(
	typeSpec *ast.TypeSpec,
	methodCounts map[string]int,
) bool {
	if typeSpec == nil || typeSpec.Name == nil || ast.IsExported(typeSpec.Name.Name) {
		return false
	}

	if !identifierHasWord(typeSpec.Name.Name, "result") &&
		!identifierHasWord(typeSpec.Name.Name, "response") &&
		!identifierHasWord(typeSpec.Name.Name, "outcome") {
		return false
	}

	if methodCounts[typeSpec.Name.Name] != 0 {
		return false
	}

	st, ok := typeSpec.Type.(*ast.StructType)
	if !ok || st.Fields == nil || len(st.Fields.List) != resultWrapperFieldCount {
		return false
	}

	if !resultWrapperFieldsArePlain(st.Fields.List) {
		return false
	}

	return l.resultWrapperReturnedByPrivateFunc(typeSpec.Name.Name)
}

func resultWrapperFieldsArePlain(fields []*ast.Field) bool {
	statusFields := 0

	for _, field := range fields {
		if field.Tag != nil || len(field.Names) != 1 || field.Names[0] == nil {
			return false
		}

		name := field.Names[0].Name
		if name == "ok" || name == "err" || name == "error" {
			statusFields++
		}
	}

	return statusFields == 1
}

func (l *linter) resultWrapperReturnedByPrivateFunc(typeName string) bool {
	found := false

	l.forEachProductionFunc(func(fn *ast.FuncDecl) {
		if found || fn.Name == nil || ast.IsExported(fn.Name.Name) {
			return
		}

		found = funcResultsContainIdent(fn.Type.Results, typeName)
	})

	return found
}

func funcResultsContainIdent(results *ast.FieldList, typeName string) bool {
	if results == nil {
		return false
	}

	for _, field := range results.List {
		if ident, ok := field.Type.(*ast.Ident); ok && ident.Name == typeName {
			return true
		}
	}

	return false
}

func (l *linter) reportGenericNameForTrivialForwarder(fn *ast.FuncDecl) {
	if fn == nil || fn.Name == nil || !genericPrivateName(fn.Name.Name) {
		return
	}

	l.report(
		fn.Name.Pos(),
		"generic_naming",
		fmt.Sprintf(
			`private helper %q has generic name and only forwards; rename or inline`,
			fn.Name.Name,
		),
	)
}

func (l *linter) reportGenericNameForSingleUseHelper(fn *ast.FuncDecl) {
	if fn == nil || fn.Name == nil || !genericPrivateName(fn.Name.Name) {
		return
	}

	l.report(
		fn.Name.Pos(),
		"generic_naming",
		fmt.Sprintf(
			`private helper %q has generic name and one tiny callsite; rename or inline`,
			fn.Name.Name,
		),
	)
}

func genericPrivateName(name string) bool {
	for _, word := range splitIdentifierWords(name) {
		switch word {
		case "helper", "manager", "processor", "util", "utils", "base", "impl":
			return true
		default:
			continue
		}
	}

	return false
}

func isEligiblePrivateSmellFunc(fn *ast.FuncDecl) bool {
	return fn != nil &&
		fn.Name != nil &&
		fn.Body != nil &&
		fn.Doc == nil &&
		fn.Recv == nil &&
		!ast.IsExported(fn.Name.Name) &&
		!hasTypeParams(fn.Type)
}

func (l *linter) productionPackageCallCounts() map[string]int {
	return l.packageCallCountsForFiles(false)
}

func (l *linter) forEachProductionFunc(fn func(*ast.FuncDecl)) {
	l.forEachProductionDecl(func(decl ast.Decl) {
		funcDecl, ok := decl.(*ast.FuncDecl)
		if ok {
			fn(funcDecl)
		}
	})
}

func (l *linter) forEachProductionTypeSpec(fn func(*ast.TypeSpec)) {
	l.forEachProductionDecl(func(decl ast.Decl) {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			return
		}

		for _, spec := range gen.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if ok {
				fn(typeSpec)
			}
		}
	})
}

func (l *linter) forEachProductionDecl(fn func(ast.Decl)) {
	for _, file := range l.pkg.Files {
		if l.fileIsTest(file) {
			continue
		}

		for _, decl := range file.Decls {
			fn(decl)
		}
	}
}

func (l *linter) fileIsTest(file *ast.File) bool {
	if file == nil {
		return false
	}

	posFile := l.pkg.FSet.File(file.Pos())
	if posFile == nil {
		return false
	}

	return strings.HasSuffix(posFile.Name(), "_test.go")
}

func funcParamCount(fields *ast.FieldList) int {
	if fields == nil {
		return 0
	}

	count := 0

	for _, field := range fields.List {
		if len(field.Names) == 0 {
			count++
			continue
		}

		count += len(field.Names)
	}

	return count
}

func (l *linter) methodCountsByReceiverName() map[string]int {
	counts := make(map[string]int)

	l.forEachProductionFunc(func(fn *ast.FuncDecl) {
		if fn.Recv == nil || len(fn.Recv.List) != 1 {
			return
		}

		name, ok := receiverTypeName(fn.Recv.List[0].Type)
		if ok {
			counts[name]++
		}
	})

	return counts
}

func receiverTypeName(expr ast.Expr) (string, bool) {
	switch expr := expr.(type) {
	case *ast.Ident:
		return expr.Name, true
	case *ast.StarExpr:
		return receiverTypeName(expr.X)
	default:
		return "", false
	}
}

func identifierHasWord(name, want string) bool {
	return slices.Contains(splitIdentifierWords(name), want)
}
