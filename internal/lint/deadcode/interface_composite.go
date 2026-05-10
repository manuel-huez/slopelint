package deadcode

import (
	"go/ast"
	"go/types"
)

func (graph deadCodeGraph) valueSpecInterfaceMethodUses(
	l *packageLinter,
	spec *ast.ValueSpec,
) map[string]struct{} {
	if spec == nil || spec.Type == nil {
		return nil
	}

	target := l.pkg.TypesInfo.TypeOf(spec.Type)
	if target == nil {
		return nil
	}

	out := make(map[string]struct{})

	targets := make([]types.Type, max(len(spec.Names), 1))
	for index := range targets {
		targets[index] = target
	}

	for _, value := range spec.Values {
		graph.addInterfaceMethodsForValueTargets(l, out, value, targets)
	}

	return out
}

func (graph deadCodeGraph) compositeLitInterfaceMethodUses(
	l *packageLinter,
	lit *ast.CompositeLit,
) map[string]struct{} {
	if lit == nil {
		return nil
	}

	elemType := compositeElementType(l.pkg.TypesInfo.TypeOf(lit.Type))
	if elemType == nil {
		return nil
	}

	out := make(map[string]struct{})

	for _, elt := range lit.Elts {
		if keyValue, ok := elt.(*ast.KeyValueExpr); ok {
			elt = keyValue.Value
		}

		graph.addInterfaceMethodsForValue(l, out, elt, elemType)
	}

	return out
}

func compositeElementType(typ types.Type) types.Type {
	if typ == nil {
		return nil
	}

	switch underlying := types.Unalias(typ).Underlying().(type) {
	case *types.Array:
		return underlying.Elem()
	case *types.Slice:
		return underlying.Elem()
	case *types.Map:
		return underlying.Elem()
	default:
		return nil
	}
}
