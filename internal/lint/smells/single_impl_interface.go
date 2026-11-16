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

// singleImplCheck reports private interfaces with one in-package implementation.
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
				`private interface %q has one in-package implementation %q; use concrete type unless substitution is needed`,
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
