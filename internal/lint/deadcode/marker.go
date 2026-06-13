package deadcode

import (
	"go/ast"
	"go/types"
)

func isMarkerMethod(l *packageLinter, fn *ast.FuncDecl, obj *types.Func) bool {
	sig, ok := markerFuncSignature(fn, obj)
	if l == nil || !ok {
		return false
	}

	return l.methodImplementsMarkerInterface(obj, sig.Recv().Type())
}

func markerFuncSignature(fn *ast.FuncDecl, obj *types.Func) (*types.Signature, bool) {
	if fn == nil ||
		fn.Name == nil ||
		fn.Recv == nil ||
		fn.Body == nil ||
		len(fn.Body.List) != 0 ||
		ast.IsExported(fn.Name.Name) ||
		obj == nil {
		return nil, false
	}

	sig, ok := obj.Type().(*types.Signature)
	if !ok || sig == nil || sig.Recv() == nil || !emptyMarkerSignature(sig) {
		return nil, false
	}

	return sig, true
}

func (l *packageLinter) methodImplementsMarkerInterface(
	obj *types.Func,
	receiver types.Type,
) bool {
	for _, decl := range l.pkg.ProductionDecls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}

		for _, spec := range genDecl.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok || !l.markerInterfaceRequiresMethod(typeSpec, obj, receiver) {
				continue
			}

			return true
		}
	}

	return false
}

func (l *packageLinter) markerInterfaceRequiresMethod(
	spec *ast.TypeSpec,
	obj *types.Func,
	receiver types.Type,
) bool {
	typeName, _ := l.pkg.TypesInfo.Defs[spec.Name].(*types.TypeName)
	if typeName == nil {
		return false
	}

	iface, ok := types.Unalias(typeName.Type()).Underlying().(*types.Interface)
	if !ok || !typeSatisfiesInterface(l, receiver, iface) {
		return false
	}

	for method := range iface.Methods() {
		if method == nil || method.Name() != obj.Name() {
			continue
		}

		sig, ok := method.Type().(*types.Signature)
		if !ok || sig == nil || !emptyMarkerSignature(sig) {
			continue
		}

		return lookupInterfaceMethodImplementation(l, receiver, method) == obj
	}

	return false
}

func emptyMarkerSignature(sig *types.Signature) bool {
	return !sig.Variadic() &&
		tupleLen(sig.Params()) == 0 &&
		tupleLen(sig.Results()) == 0
}
