package deadcode

import (
	"go/ast"
	"go/types"
)

func (graph deadCodeGraph) genericInstanceInterfaceMethodUses(
	l *packageLinter,
	ident *ast.Ident,
) map[string]struct{} {
	if ident == nil {
		return nil
	}

	instance, ok := l.pkg.TypesInfo.Instances[ident]
	if !ok || instance.TypeArgs == nil || instance.TypeArgs.Len() == 0 {
		return nil
	}

	typeParams := genericObjectTypeParams(l.pkg.TypesInfo.Uses[ident])
	if typeParams == nil || typeParams.Len() == 0 {
		return nil
	}

	out := make(map[string]struct{})
	for index := range min(typeParams.Len(), instance.TypeArgs.Len()) {
		graph.addInterfaceMethodsForType(
			l,
			out,
			instance.TypeArgs.At(index),
			typeParams.At(index).Constraint(),
		)
	}

	return out
}

func genericObjectTypeParams(obj types.Object) *types.TypeParamList {
	switch obj := obj.(type) {
	case *types.Func:
		sig, ok := obj.Type().(*types.Signature)
		if !ok || sig == nil {
			return nil
		}

		return sig.TypeParams()
	case *types.TypeName:
		named, ok := obj.Type().(*types.Named)
		if !ok || named == nil {
			return nil
		}

		return named.TypeParams()
	default:
		return nil
	}
}
