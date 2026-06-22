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

	forEachCompositeLitStructField(lit, structType, func(elt ast.Expr, field *types.Var, _ string) {
		var target types.Type
		if field != nil {
			target = field.Type()
		}

		graph.addInterfaceMethodsForValue(l, out, elt, target)
	})

	return out
}

func forEachCompositeLitStructField(
	lit *ast.CompositeLit,
	structType *types.Struct,
	visit func(ast.Expr, *types.Var, string),
) {
	if structType == nil {
		return
	}

	positionalIndex := 0

	for _, elt := range lit.Elts {
		if keyValue, ok := elt.(*ast.KeyValueExpr); ok {
			visit(
				keyValue.Value,
				compositeLitField(structType, keyValue.Key),
				compositeLitFieldName(keyValue.Key),
			)

			continue
		}

		if positionalIndex < structType.NumFields() {
			field := structType.Field(positionalIndex)
			visit(elt, field, field.Name())
		}

		positionalIndex++
	}
}

func compositeLitField(structType *types.Struct, key ast.Expr) *types.Var {
	ident, ok := key.(*ast.Ident)
	if !ok || ident == nil {
		return nil
	}

	for field := range structType.Fields() {
		if field != nil && field.Name() == ident.Name {
			return field
		}
	}

	return nil
}

func compositeLitFieldName(key ast.Expr) string {
	ident, ok := key.(*ast.Ident)
	if !ok || ident == nil {
		return ""
	}

	return ident.Name
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
