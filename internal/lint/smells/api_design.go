package smells

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"slices"

	"github.com/manuel-huez/slopelint/internal/lint/outline"
)

const (
	optionsOverkillMaxUses  = 2
	resultWrapperFieldCount = 2
)

// optionsOverkillCheck reports private constructors that take functional
// options but have at most two production uses.
type optionsOverkillCheck struct {
	ruleEnv
}

func (l optionsOverkillCheck) check() {
	useCounts := l.productionPackageFuncUseCounts()

	for _, fn := range l.pkg.ProductionFuncs {
		obj, ok := privateFuncWithFunctionalOptions(l.pkg.TypesInfo, fn, useCounts)
		if !ok {
			continue
		}

		l.report(
			fn.Name.Pos(),
			"api_overkill",
			fmt.Sprintf(
				`private API %q uses functional options for %d production uses`,
				fn.Name.Name,
				useCounts[FuncObjectKey(obj)],
			),
		)
	}
}

func privateFuncWithFunctionalOptions(
	info *types.Info,
	fn *ast.FuncDecl,
	useCounts map[string]int,
) (*types.Func, bool) {
	if fn.Recv != nil || fn.Doc != nil || !privateConstructorName(fn.Name.Name) {
		return nil, false
	}

	obj, ok := privateFuncObject(info, fn)
	if !ok {
		return nil, false
	}

	count := useCounts[FuncObjectKey(obj)]
	if count == 0 || count > optionsOverkillMaxUses {
		return nil, false
	}

	if !funcHasFunctionalOptionParam(info, fn.Type.Params) {
		return nil, false
	}

	return obj, true
}

func privateConstructorName(name string) bool {
	words := outline.Words(name)
	if len(words) == 0 {
		return false
	}

	return words[0] == "new" || words[0] == "build"
}

func funcHasFunctionalOptionParam(info *types.Info, fields *ast.FieldList) bool {
	for _, field := range fields.List {
		ellipsis, ok := field.Type.(*ast.Ellipsis)
		if !ok {
			continue
		}

		if typeIsFunctionSignature(info.TypeOf(ellipsis.Elt)) {
			return true
		}
	}

	return false
}

func typeIsFunctionSignature(typ types.Type) bool {
	if typ == nil {
		return false
	}

	if _, ok := types.Unalias(typ).Underlying().(*types.Signature); ok {
		return true
	}

	return false
}

type resultWrapperCheck struct {
	ruleEnv
}

func (l resultWrapperCheck) check() {
	methodCounts := l.methodCountsByReceiverName()
	storedTypes := l.storageTypes()

	for _, typeSpec := range l.pkg.ProductionTypes {
		if !l.isInternalResultWrapper(typeSpec, methodCounts) {
			continue
		}

		if _, stored := storedTypes[l.pkg.TypesInfo.TypeOf(typeSpec.Name)]; stored {
			continue
		}

		l.report(
			typeSpec.Name.Pos(),
			"result_wrapper",
			fmt.Sprintf(
				`private result wrapper %q only carries value plus status`,
				typeSpec.Name.Name,
			),
		)
	}
}

func (l resultWrapperCheck) methodCountsByReceiverName() map[string]int {
	counts := make(map[string]int)

	for _, fn := range l.pkg.ProductionFuncs {
		if fn.Recv == nil || len(fn.Recv.List) != 1 {
			continue
		}

		if name, ok := receiverTypeName(fn.Recv.List[0].Type); ok {
			counts[name]++
		}
	}

	return counts
}

func (l resultWrapperCheck) storageTypes() map[types.Type]struct{} {
	stored := make(map[types.Type]struct{})
	add := func(typ types.Type) {
		for {
			typ = types.Unalias(typ)

			pointer, ok := typ.(*types.Pointer)
			if !ok {
				stored[typ] = struct{}{}
				return
			}

			typ = pointer.Elem()
		}
	}

	// Storage needs one value; splitting it into separate Go results loses that shape.
	for _, decl := range l.pkg.ProductionDecls {
		ast.Inspect(decl, func(node ast.Node) bool {
			expr, ok := node.(ast.Expr)
			if !ok {
				return true
			}

			typ := l.pkg.TypesInfo.TypeOf(expr)
			if typ == nil {
				return true
			}

			switch typ := typ.Underlying().(type) {
			case *types.Chan:
				add(typ.Elem())
			case *types.Map:
				add(typ.Key())
				add(typ.Elem())
			case *types.Slice:
				add(typ.Elem())
			case *types.Array:
				add(typ.Elem())
			case *types.Struct:
				for field := range typ.Fields() {
					add(field.Type())
				}
			}

			return true
		})
	}

	return stored
}

func (l resultWrapperCheck) isInternalResultWrapper(
	typeSpec *ast.TypeSpec,
	methodCounts map[string]int,
) bool {
	if ast.IsExported(typeSpec.Name.Name) {
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
	if !ok || len(st.Fields.List) != resultWrapperFieldCount {
		return false
	}

	if !resultWrapperFieldsArePlain(st.Fields.List) {
		return false
	}

	return l.returnedByPrivateFunc(typeSpec.Name.Name)
}

func resultWrapperFieldsArePlain(fields []*ast.Field) bool {
	statusFields := 0

	for _, field := range fields {
		if field.Tag != nil || len(field.Names) != 1 {
			return false
		}

		name := field.Names[0].Name
		if name == "ok" || name == "err" || name == "error" {
			statusFields++
		}
	}

	return statusFields == 1
}

func (l resultWrapperCheck) returnedByPrivateFunc(typeName string) bool {
	for _, fn := range l.pkg.ProductionFuncs {
		if !ast.IsExported(fn.Name.Name) && funcResultsContainIdent(fn.Type.Results, typeName) {
			return true
		}
	}

	return false
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
	return slices.Contains(outline.Words(name), want)
}

type privateInterfaceDecl struct {
	name string
	pos  token.Pos
	obj  *types.TypeName
}

type singleImplCheck struct {
	ruleEnv
}

func (l singleImplCheck) check() {
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
				`private interface %q has one in-package implementation %q`,
				ifaceDecl.name,
				implName,
			),
		)
	}
}

func (l singleImplCheck) privateInterfaces() []privateInterfaceDecl {
	out := make([]privateInterfaceDecl, 0)

	for _, typeSpec := range l.pkg.ProductionTypes {
		if ast.IsExported(typeSpec.Name.Name) {
			continue
		}

		if _, ok := typeSpec.Type.(*ast.InterfaceType); !ok {
			continue
		}

		obj, ok := l.pkg.TypesInfo.Defs[typeSpec.Name].(*types.TypeName)
		if !ok {
			continue
		}

		out = append(out, privateInterfaceDecl{
			name: typeSpec.Name.Name,
			pos:  typeSpec.Name.Pos(),
			obj:  obj,
		})
	}

	return out
}

func interfaceEligibleForSingleImpl(iface *types.Interface) bool {
	if iface.NumEmbeddeds() != 0 || iface.NumMethods() == 0 {
		return false
	}

	hasPackagePrivateMethod := false

	for method := range iface.Methods() {
		if !method.Exported() {
			hasPackagePrivateMethod = true
		}
	}

	// An exported-only method set can always be implemented by types from other packages.
	return hasPackagePrivateMethod
}

func (l singleImplCheck) namedConcreteTypes() []*types.Named {
	out := make([]*types.Named, 0)

	for _, file := range l.pkg.Files {
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}

			for _, spec := range gen.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}

				obj, ok := l.pkg.TypesInfo.Defs[typeSpec.Name].(*types.TypeName)
				if !ok {
					continue
				}

				named, ok := obj.Type().(*types.Named)
				if !ok {
					continue
				}

				if _, isIface := named.Underlying().(*types.Interface); !isIface {
					out = append(out, named)
				}
			}
		}
	}

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
