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

	out := make(map[string]struct{})
	for key := range graph.compositeLitCollectionInterfaceMethodUses(l, lit) {
		out[key] = struct{}{}
	}

	for key := range graph.compositeLitStructInterfaceMethodUses(l, lit) {
		out[key] = struct{}{}
	}

	return out
}

func (graph deadCodeGraph) compositeLitCollectionInterfaceMethodUses(
	l *packageLinter,
	lit *ast.CompositeLit,
) map[string]struct{} {
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

func (graph deadCodeGraph) compositeLitStructInterfaceMethodUses(
	l *packageLinter,
	lit *ast.CompositeLit,
) map[string]struct{} {
	structType := deadCodeStructType(l.pkg.TypesInfo.TypeOf(lit))
	if structType == nil {
		return nil
	}

	out := make(map[string]struct{})
	positionalIndex := 0

	for _, elt := range lit.Elts {
		if keyValue, ok := elt.(*ast.KeyValueExpr); ok {
			target := compositeLitFieldType(structType, keyValue.Key)
			graph.addInterfaceMethodsForValue(l, out, keyValue.Value, target)

			continue
		}

		if positionalIndex < structType.NumFields() {
			graph.addInterfaceMethodsForValue(
				l,
				out,
				elt,
				structType.Field(positionalIndex).Type(),
			)
		}

		positionalIndex++
	}

	return out
}

func compositeLitFieldType(structType *types.Struct, key ast.Expr) types.Type {
	if structType == nil {
		return nil
	}

	ident, ok := key.(*ast.Ident)
	if !ok || ident == nil {
		return nil
	}

	for index := range structType.NumFields() {
		field := structType.Field(index)
		if field != nil && field.Name() == ident.Name {
			return field.Type()
		}
	}

	return nil
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
