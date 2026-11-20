package deadcode

import (
	"go/ast"
	"go/types"
)

func (s ifaceScan) valueSpecInterfaceMethodUses(
	spec *ast.ValueSpec,
) map[string]struct{} {
	if spec == nil || spec.Type == nil {
		return nil
	}

	target := s.info.TypeOf(spec.Type)
	if target == nil {
		return nil
	}

	out := make(map[string]struct{})

	targets := make([]types.Type, max(len(spec.Names), 1))
	for index := range targets {
		targets[index] = target
	}

	for _, value := range spec.Values {
		s.addInterfaceMethodsForValueTargets(out, value, targets)
	}

	return out
}

func (s ifaceScan) compositeLitInterfaceMethodUses(
	lit *ast.CompositeLit,
) map[string]struct{} {
	if lit == nil {
		return nil
	}

	out := make(map[string]struct{})
	for key := range s.compositeLitCollectionInterfaceMethodUses(lit) {
		out[key] = struct{}{}
	}

	for key := range s.compositeLitStructInterfaceMethodUses(lit) {
		out[key] = struct{}{}
	}

	return out
}

func (s ifaceScan) compositeLitCollectionInterfaceMethodUses(
	lit *ast.CompositeLit,
) map[string]struct{} {
	elemType := compositeElementType(s.info.TypeOf(lit.Type))
	if elemType == nil {
		return nil
	}

	out := make(map[string]struct{})

	for _, elt := range lit.Elts {
		if keyValue, ok := elt.(*ast.KeyValueExpr); ok {
			elt = keyValue.Value
		}

		s.addInterfaceMethodsForValue(out, elt, elemType)
	}

	return out
}

func (s ifaceScan) compositeLitStructInterfaceMethodUses(
	lit *ast.CompositeLit,
) map[string]struct{} {
	structType := deadCodeStructType(s.info.TypeOf(lit))
	if structType == nil {
		return nil
	}

	out := make(map[string]struct{})

	forEachCompositeLitStructField(lit, structType, func(elt ast.Expr, field *types.Var, _ string) {
		var target types.Type
		if field != nil {
			target = field.Type()
		}

		s.addInterfaceMethodsForValue(out, elt, target)
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
