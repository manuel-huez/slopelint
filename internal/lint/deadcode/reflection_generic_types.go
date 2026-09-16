package deadcode

import (
	"go/ast"
	"go/types"
)

func reflectedTypeParamReplacements(
	fn *types.Func,
	typeArgs *types.TypeList,
) map[*types.TypeParam]types.Type {
	if typeArgs == nil || typeArgs.Len() == 0 {
		return nil
	}

	indexes := genericTypeParamIndexes(fn)
	if len(indexes) == 0 {
		return nil
	}

	out := make(map[*types.TypeParam]types.Type, len(indexes))
	for param, index := range indexes {
		if index < typeArgs.Len() {
			out[param] = typeArgs.At(index)
		}
	}

	return out
}

func substituteReflectedTypeParams(
	typ types.Type,
	replacements map[*types.TypeParam]types.Type,
) types.Type {
	if len(replacements) == 0 {
		return typ
	}

	return substituteReflectedTypeParamsSeen(typ, replacements, make(map[string]struct{}))
}

func substituteReflectedTypeParamsSeen(
	typ types.Type,
	replacements map[*types.TypeParam]types.Type,
	seen map[string]struct{},
) types.Type {
	if typ == nil {
		return nil
	}

	typ = types.Unalias(typ)
	switch typ := typ.(type) {
	case *types.TypeParam:
		if replacement, ok := replacements[typ]; ok {
			return replacement
		}

		return typ
	case *types.Pointer:
		return types.NewPointer(
			substituteReflectedTypeParamsSeen(typ.Elem(), replacements, seen),
		)
	case *types.Slice:
		return types.NewSlice(
			substituteReflectedTypeParamsSeen(typ.Elem(), replacements, seen),
		)
	case *types.Array:
		return types.NewArray(
			substituteReflectedTypeParamsSeen(typ.Elem(), replacements, seen),
			typ.Len(),
		)
	case *types.Map:
		return types.NewMap(
			substituteReflectedTypeParamsSeen(typ.Key(), replacements, seen),
			substituteReflectedTypeParamsSeen(typ.Elem(), replacements, seen),
		)
	case *types.Named:
		return substituteReflectedNamedTypeParams(typ, replacements, seen)
	case *types.Struct:
		return substituteReflectedStructTypeParams(typ, replacements, seen)
	default:
		return typ
	}
}

func substituteReflectedNamedTypeParams(
	typ *types.Named,
	replacements map[*types.TypeParam]types.Type,
	seen map[string]struct{},
) types.Type {
	key := deadCodeTypeString(typ)
	if _, ok := seen[key]; ok {
		return typ
	}

	seen[key] = struct{}{}
	defer delete(seen, key)

	if typ.TypeArgs().Len() == 0 {
		return substituteReflectedNamedUnderlyingTypeParams(typ, replacements, seen)
	}

	typeArgs := make([]types.Type, 0, typ.TypeArgs().Len())
	for arg := range typ.TypeArgs().Types() {
		typeArgs = append(
			typeArgs,
			substituteReflectedTypeParamsSeen(arg, replacements, seen),
		)
	}

	instantiated, err := types.Instantiate(nil, typ.Origin(), typeArgs, false)
	if err != nil {
		return typ
	}

	return instantiated
}

func substituteReflectedNamedUnderlyingTypeParams(
	typ *types.Named,
	replacements map[*types.TypeParam]types.Type,
	seen map[string]struct{},
) types.Type {
	if !reflectedTypeContainsReplacedParams(typ.Underlying(), replacements) {
		return typ
	}

	return substituteReflectedTypeParamsSeen(typ.Underlying(), replacements, seen)
}

type reflectedTypeParamMatcher func(*types.TypeParam) bool

func reflectedTypeContainsReplacedParams(
	typ types.Type,
	replacements map[*types.TypeParam]types.Type,
) bool {
	if len(replacements) == 0 {
		return false
	}

	return reflectedTypeContainsMatchingParam(
		typ,
		func(param *types.TypeParam) bool {
			_, ok := replacements[param]

			return ok
		},
		make(map[string]struct{}),
	)
}

func reflectedTypeContainsParams(
	typ types.Type,
	typeParamIndexes map[*types.TypeParam]int,
) bool {
	if len(typeParamIndexes) == 0 {
		return false
	}

	return reflectedTypeContainsMatchingParam(
		typ,
		func(param *types.TypeParam) bool {
			_, ok := typeParamIndexes[param]

			return ok
		},
		make(map[string]struct{}),
	)
}

func reflectedTypeContainsMatchingParam(
	typ types.Type,
	matches reflectedTypeParamMatcher,
	seen map[string]struct{},
) bool {
	if typ == nil || matches == nil {
		return false
	}

	typ = types.Unalias(typ)
	if param, ok := typ.(*types.TypeParam); ok {
		return matches(param)
	}

	key := deadCodeTypeString(typ)
	if _, ok := seen[key]; ok {
		return false
	}

	seen[key] = struct{}{}
	defer delete(seen, key)

	if elem, ok := reflectedSequentialContainerElem(typ); ok {
		return reflectedTypeContainsMatchingParam(elem, matches, seen)
	}

	switch typ := typ.(type) {
	case *types.Map:
		return reflectedMapTypeContainsMatchingParam(typ, matches, seen)
	case *types.Named:
		return reflectedNamedTypeContainsMatchingParam(typ, matches, seen)
	case *types.Struct:
		return reflectedStructTypeContainsMatchingParam(typ, matches, seen)
	default:
		return false
	}
}

func reflectedMapTypeContainsMatchingParam(
	typ *types.Map,
	matches reflectedTypeParamMatcher,
	seen map[string]struct{},
) bool {
	return reflectedTypeContainsMatchingParam(typ.Key(), matches, seen) ||
		reflectedTypeContainsMatchingParam(typ.Elem(), matches, seen)
}

func reflectedNamedTypeContainsMatchingParam(
	typ *types.Named,
	matches reflectedTypeParamMatcher,
	seen map[string]struct{},
) bool {
	for arg := range typ.TypeArgs().Types() {
		if reflectedTypeContainsMatchingParam(arg, matches, seen) {
			return true
		}
	}

	return reflectedTypeContainsMatchingParam(typ.Underlying(), matches, seen)
}

func reflectedStructTypeContainsMatchingParam(
	typ *types.Struct,
	matches reflectedTypeParamMatcher,
	seen map[string]struct{},
) bool {
	for field := range typ.Fields() {
		if reflectedTypeContainsMatchingParam(field.Type(), matches, seen) {
			return true
		}
	}

	return false
}

func substituteReflectedStructTypeParams(
	typ *types.Struct,
	replacements map[*types.TypeParam]types.Type,
	seen map[string]struct{},
) types.Type {
	fields := make([]*types.Var, 0, typ.NumFields())
	tags := make([]string, 0, typ.NumFields())

	for index := range typ.NumFields() {
		field := typ.Field(index)
		fields = append(fields, types.NewField(
			field.Pos(),
			field.Pkg(),
			field.Name(),
			substituteReflectedTypeParamsSeen(field.Type(), replacements, seen),
			field.Embedded(),
		))
		tags = append(tags, typ.Tag(index))
	}

	return types.NewStruct(fields, tags)
}

func collectReflectedTypeParamDecodes(
	typ types.Type,
	codec reflectedCodecUse,
	typeParamIndexes map[*types.TypeParam]int,
	out *[]reflectedTypeParamUse,
	context reflectedTypeParamUseContext,
) {
	collectReflectedTypeParamUses(
		typ,
		codec,
		typeParamIndexes,
		out,
		make(map[string]struct{}),
		context,
	)
}

func collectReflectedTypeParamUses(
	typ types.Type,
	codec reflectedCodecUse,
	typeParamIndexes map[*types.TypeParam]int,
	out *[]reflectedTypeParamUse,
	seen map[string]struct{},
	context reflectedTypeParamUseContext,
) {
	if typ == nil {
		return
	}

	typ = types.Unalias(typ)
	if collectReflectedDirectTypeParam(
		typ,
		codec,
		typeParamIndexes,
		out,
		context,
	) {
		return
	}

	collectReflectedCompositeTypeParamUses(
		typ,
		codec,
		typeParamIndexes,
		out,
		seen,
		context,
	)
}

func collectReflectedDirectTypeParam(
	typ types.Type,
	codec reflectedCodecUse,
	typeParamIndexes map[*types.TypeParam]int,
	out *[]reflectedTypeParamUse,
	context reflectedTypeParamUseContext,
) bool {
	typeParam, ok := typ.(*types.TypeParam)
	if !ok {
		return false
	}

	if index, ok := typeParamIndexes[typeParam]; ok {
		*out = append(*out, reflectedTypeParamUse{
			index:       index,
			codec:       codec,
			pointerOnly: context == reflectedDecodeTargetContext,
			mapKey:      context == reflectedTextDecodeContext,
		})
	}

	return true
}

func collectReflectedCompositeTypeParamUses(
	typ types.Type,
	codec reflectedCodecUse,
	typeParamIndexes map[*types.TypeParam]int,
	out *[]reflectedTypeParamUse,
	seen map[string]struct{},
	context reflectedTypeParamUseContext,
) {
	switch typ := typ.(type) {
	case *types.Pointer:
		collectReflectedPointerTypeParamUses(typ, codec, typeParamIndexes, out, seen, context)
	case *types.Slice:
		collectReflectedContainerElemTypeParamUses(
			typ.Elem(),
			codec,
			typeParamIndexes,
			out,
			seen,
			context,
		)
	case *types.Array:
		collectReflectedContainerElemTypeParamUses(
			typ.Elem(),
			codec,
			typeParamIndexes,
			out,
			seen,
			context,
		)
	case *types.Map:
		collectReflectedMapTypeParamUses(typ, codec, typeParamIndexes, out, seen, context)
	case *types.Named:
		collectReflectedNamedCompositeTypeParamUses(
			typ,
			codec,
			typeParamIndexes,
			out,
			seen,
			context,
		)
	case *types.Struct:
		collectReflectedStructCompositeTypeParamUses(
			typ,
			codec,
			typeParamIndexes,
			out,
			seen,
			context,
		)
	}
}

func collectReflectedPointerTypeParamUses(
	typ *types.Pointer,
	codec reflectedCodecUse,
	typeParamIndexes map[*types.TypeParam]int,
	out *[]reflectedTypeParamUse,
	seen map[string]struct{},
	context reflectedTypeParamUseContext,
) {
	nextContext := context
	if context == reflectedDecodeTargetContext {
		nextContext = reflectedSettableDecodeContext
	}

	collectReflectedTypeParamUses(
		typ.Elem(),
		codec,
		typeParamIndexes,
		out,
		seen,
		nextContext,
	)
}

func collectReflectedContainerElemTypeParamUses(
	typ types.Type,
	codec reflectedCodecUse,
	typeParamIndexes map[*types.TypeParam]int,
	out *[]reflectedTypeParamUse,
	seen map[string]struct{},
	context reflectedTypeParamUseContext,
) {
	if context == reflectedDecodeTargetContext {
		return
	}

	collectReflectedTypeParamUses(typ, codec, typeParamIndexes, out, seen, context)
}

func collectReflectedMapTypeParamUses(
	typ *types.Map,
	codec reflectedCodecUse,
	typeParamIndexes map[*types.TypeParam]int,
	out *[]reflectedTypeParamUse,
	seen map[string]struct{},
	context reflectedTypeParamUseContext,
) {
	if context != reflectedDecodeTargetContext {
		collectReflectedTypeParamUses(
			typ.Key(),
			codec,
			typeParamIndexes,
			out,
			seen,
			reflectedTextDecodeContext,
		)
	}

	collectReflectedContainerElemTypeParamUses(
		typ.Elem(),
		codec,
		typeParamIndexes,
		out,
		seen,
		context,
	)
}

func collectReflectedNamedCompositeTypeParamUses(
	typ *types.Named,
	codec reflectedCodecUse,
	typeParamIndexes map[*types.TypeParam]int,
	out *[]reflectedTypeParamUse,
	seen map[string]struct{},
	context reflectedTypeParamUseContext,
) {
	if context == reflectedDecodeTargetContext {
		return
	}

	collectReflectedNamedTypeParamUses(typ, codec, typeParamIndexes, out, seen, context)
}

func collectReflectedStructCompositeTypeParamUses(
	typ *types.Struct,
	codec reflectedCodecUse,
	typeParamIndexes map[*types.TypeParam]int,
	out *[]reflectedTypeParamUse,
	seen map[string]struct{},
	context reflectedTypeParamUseContext,
) {
	if context == reflectedDecodeTargetContext {
		return
	}

	collectReflectedStructTypeParamUses(typ, codec, typeParamIndexes, out, seen, context)
}

func collectReflectedNamedTypeParamUses(
	typ *types.Named,
	codec reflectedCodecUse,
	typeParamIndexes map[*types.TypeParam]int,
	out *[]reflectedTypeParamUse,
	seen map[string]struct{},
	context reflectedTypeParamUseContext,
) {
	key := deadCodeTypeString(typ)
	if _, ok := seen[key]; ok {
		return
	}

	seen[key] = struct{}{}
	for arg := range typ.TypeArgs().Types() {
		collectReflectedTypeParamUses(
			arg,
			codec,
			typeParamIndexes,
			out,
			seen,
			context,
		)
	}

	collectReflectedTypeParamUses(
		typ.Underlying(),
		codec,
		typeParamIndexes,
		out,
		seen,
		context,
	)
	delete(seen, key)
}

func collectReflectedStructTypeParamUses(
	typ *types.Struct,
	codec reflectedCodecUse,
	typeParamIndexes map[*types.TypeParam]int,
	out *[]reflectedTypeParamUse,
	seen map[string]struct{},
	context reflectedTypeParamUseContext,
) {
	for field := range typ.Fields() {
		collectReflectedTypeParamUses(
			field.Type(),
			codec,
			typeParamIndexes,
			out,
			seen,
			context,
		)
	}
}

func genericCalleeIdent(expr ast.Expr) *ast.Ident {
	switch expr := expr.(type) {
	case *ast.IndexExpr:
		return genericCalleeIdent(expr.X)
	case *ast.IndexListExpr:
		return genericCalleeIdent(expr.X)
	case *ast.Ident:
		return expr
	case *ast.SelectorExpr:
		return expr.Sel
	default:
		return nil
	}
}
