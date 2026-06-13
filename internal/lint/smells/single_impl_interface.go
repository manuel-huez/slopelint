package smells

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
)

type privateInterfaceDecl struct {
	name string
	pos  token.Pos
	obj  *types.TypeName
}

func (l *Runner) checkSingleImplInterfaces() {
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

func (l *Runner) privateInterfaces() []privateInterfaceDecl {
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

	for method := range iface.Methods() {
		if !method.Exported() {
			return false
		}
	}

	return true
}

func (l *Runner) namedConcreteTypes() []*types.Named {
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
