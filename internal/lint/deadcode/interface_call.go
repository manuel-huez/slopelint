package deadcode

import (
	"go/ast"
	"go/types"
)

func (graph deadCodeGraph) callInterfaceMethodUses(
	l *packageLinter,
	call *ast.CallExpr,
) map[string]struct{} {
	if call == nil {
		return nil
	}

	if target := conversionTargetType(l.pkg.TypesInfo, call); target != nil {
		return graph.interfaceMethodsForValue(l, call.Args[0], target)
	}

	sig := callSignature(l.pkg.TypesInfo, call)
	if sig == nil {
		return nil
	}

	return graph.callArgInterfaceMethodUses(l, call, sig)
}

func (graph deadCodeGraph) callArgInterfaceMethodUses(
	l *packageLinter,
	call *ast.CallExpr,
	sig *types.Signature,
) map[string]struct{} {
	out := make(map[string]struct{})
	params := sig.Params()

	if params == nil || params.Len() == 0 {
		return out
	}

	for index, arg := range call.Args {
		target, ok := callParamTargetType(sig, index)
		if !ok {
			continue
		}

		graph.addInterfaceMethodsForValue(l, out, arg, target)
	}

	return out
}

func callParamTargetType(sig *types.Signature, argIndex int) (types.Type, bool) {
	params := sig.Params()
	paramIndex := argIndex

	if sig.Variadic() && argIndex >= params.Len()-1 {
		paramIndex = params.Len() - 1
	}

	if paramIndex >= params.Len() {
		return nil, false
	}

	target := params.At(paramIndex).Type()
	if sig.Variadic() && argIndex >= params.Len()-1 {
		if slice, ok := types.Unalias(target).Underlying().(*types.Slice); ok {
			target = slice.Elem()
		}
	}

	return target, true
}

func conversionTargetType(info *types.Info, call *ast.CallExpr) types.Type {
	if call == nil || len(call.Args) != 1 {
		return nil
	}

	if _, ok := call.Fun.(*ast.FuncType); ok {
		return info.TypeOf(call.Fun)
	}

	target := info.TypeOf(call.Fun)
	if target == nil {
		return nil
	}

	if _, ok := types.Unalias(target).Underlying().(*types.Interface); !ok {
		return nil
	}

	return target
}

func callSignature(info *types.Info, call *ast.CallExpr) *types.Signature {
	if call == nil {
		return nil
	}

	typ := info.TypeOf(call.Fun)
	if typ == nil {
		return nil
	}

	if sig, ok := types.Unalias(typ).Underlying().(*types.Signature); ok {
		return sig
	}

	return nil
}
