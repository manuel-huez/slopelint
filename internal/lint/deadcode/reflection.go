package deadcode

import (
	"go/ast"
	"go/types"
)

type reflectedDecodeTarget struct {
	typ    types.Type
	tag    string
	mapKey bool
}

type reflectedMarshalTarget struct {
	typ         types.Type
	tag         string
	addressable reflectedMarshalAddressability
}

type reflectedTypeParamUse struct {
	index       int
	tag         string
	pointerOnly bool
	mapKey      bool
}

type reflectedMarshalTypeParamUse struct {
	typ types.Type
	tag string
}

type reflectedTypeParamUseContext uint8
type reflectedStructFieldUseMode uint8
type reflectedMarshalAddressability uint8

const (
	reflectedDecodeTargetContext reflectedTypeParamUseContext = iota
	reflectedSettableDecodeContext
	reflectedTextDecodeContext
)

const (
	reflectedDecodeStructFields reflectedStructFieldUseMode = iota
	reflectedMarshalStructFields
)

const (
	reflectedMarshalUnaddressable reflectedMarshalAddressability = iota
	reflectedMarshalAddressable
)

func (graph deadCodeGraph) reflectedUses(
	l *packageLinter,
	call *ast.CallExpr,
) map[string]struct{} {
	fn := calledFunc(l.pkg.TypesInfo, call)
	if fn == nil {
		return nil
	}

	out := make(map[string]struct{})
	targets := reflectedDecodeTargetTypes(l, fn, call)

	targets = append(targets, graph.reflectedGenericDecodeTypes(l, fn, call)...)
	for _, target := range targets {
		if target.mapKey {
			graph.addReflectedDecodeMapKeyUses(
				l,
				out,
				target.typ,
				target.tag,
				call,
				make(map[string]struct{}),
			)

			continue
		}

		graph.addReflectedDecodeUses(
			l,
			out,
			target.typ,
			target.tag,
			call,
			make(map[string]struct{}),
		)
	}

	marshalTargets := reflectedMarshalTargetTypes(l, fn, call)

	marshalTargets = append(marshalTargets, graph.reflectedGenericMarshalTypes(l, fn, call)...)
	for _, target := range marshalTargets {
		graph.addReflectedMarshalUses(
			l,
			out,
			target.typ,
			target.tag,
			call,
			make(map[string]struct{}),
			target.addressable,
		)
	}

	return out
}

func reflectedDecodeTargetTypes(
	l *packageLinter,
	fn *types.Func,
	call *ast.CallExpr,
) []reflectedDecodeTarget {
	tag, ok := reflectedDecodeFuncTag(fn)
	if !ok || len(call.Args) == 0 {
		return nil
	}

	target := call.Args[reflectedDecodeTargetArgIndex(fn, call)]

	typ := reflectedValueType(l.pkg.TypesInfo, target)
	if !reflectedDecodeTargetType(typ) {
		return nil
	}

	return []reflectedDecodeTarget{{typ: typ, tag: tag}}
}

func reflectedMarshalTargetTypes(
	l *packageLinter,
	fn *types.Func,
	call *ast.CallExpr,
) []reflectedMarshalTarget {
	tag, ok := reflectedMarshalFuncTag(fn)
	if !ok || len(call.Args) == 0 {
		return nil
	}

	return []reflectedMarshalTarget{{
		typ:         reflectedValueType(l.pkg.TypesInfo, call.Args[0]),
		tag:         tag,
		addressable: reflectedMarshalUnaddressable,
	}}
}

func reflectedDecodeTargetType(typ types.Type) bool {
	if typ == nil {
		return false
	}

	_, ok := types.Unalias(typ).(*types.Pointer)

	return ok
}

func (graph deadCodeGraph) addReflectedDecodeUses(
	l *packageLinter,
	out map[string]struct{},
	typ types.Type,
	tag string,
	call *ast.CallExpr,
	seen map[string]struct{},
) {
	if elem, ok := reflectedSequentialContainerElem(typ); ok {
		graph.addReflectedDecodeUses(l, out, elem, tag, call, seen)

		return
	}

	if key, elem, ok := reflectedMapTypes(typ); ok {
		graph.addReflectedDecodeMapKeyUses(l, out, key, tag, call, seen)
		graph.addReflectedDecodeUses(l, out, elem, tag, call, seen)

		return
	}

	if l.addReflectedHookUse(
		out,
		typ,
		reflectedHookNames(tag, reflectedDecodeStructFields, reflectedValueHook),
	) {
		return
	}

	graph.addReflectedNamedStructUses(
		l,
		out,
		typ,
		tag,
		call,
		seen,
		reflectedDecodeStructFields,
		reflectedMarshalUnaddressable,
	)
}

func (graph deadCodeGraph) addReflectedDecodeMapKeyUses(
	l *packageLinter,
	out map[string]struct{},
	typ types.Type,
	tag string,
	call *ast.CallExpr,
	seen map[string]struct{},
) {
	if l.addReflectedHookUse(
		out,
		typ,
		reflectedHookNames(tag, reflectedDecodeStructFields, reflectedMapKeyHook),
	) {
		return
	}

	if reflectedMapKeyFallbackField(tag) {
		graph.addReflectedDecodeUses(l, out, typ, tag, call, seen)
	}
}

func (graph deadCodeGraph) addReflectedMarshalUses(
	l *packageLinter,
	out map[string]struct{},
	typ types.Type,
	tag string,
	call *ast.CallExpr,
	seen map[string]struct{},
	addressable reflectedMarshalAddressability,
) {
	hook := reflectedTypeMarshalHookMethod(
		typ,
		reflectedHookNames(tag, reflectedMarshalStructFields, reflectedValueHook),
		reflectedMarshalHookAddressability(typ, tag, addressable),
	)
	if hook != nil {
		addReflectedHookFuncUse(out, hook)
		graph.addReflectedYAMLMarshalReturnUses(l, out, hook, typ, tag, call, seen)

		return
	}

	if elem, elemAddressable, ok := reflectedMarshalSequentialContainerElem(
		typ,
		addressable,
	); ok {
		graph.addReflectedMarshalUses(l, out, elem, tag, call, seen, elemAddressable)

		return
	}

	if key, elem, ok := reflectedMapTypes(typ); ok {
		graph.addReflectedMarshalMapKeyUses(l, out, key, tag, call, seen)
		graph.addReflectedMarshalUses(
			l,
			out,
			elem,
			tag,
			call,
			seen,
			reflectedMarshalUnaddressable,
		)

		return
	}

	graph.addReflectedNamedStructUses(
		l,
		out,
		typ,
		tag,
		call,
		seen,
		reflectedMarshalStructFields,
		addressable,
	)
}

func (graph deadCodeGraph) addReflectedNamedStructUses(
	l *packageLinter,
	out map[string]struct{},
	typ types.Type,
	tag string,
	call *ast.CallExpr,
	seen map[string]struct{},
	mode reflectedStructFieldUseMode,
	addressable reflectedMarshalAddressability,
) {
	named, structType := reflectedNamedStructType(typ)
	if structType == nil {
		return
	}

	seenKey, ok := enterReflectedNamedType(seen, named)
	if !ok {
		return
	}

	graph.addReflectedStructFields(
		l,
		out,
		named,
		structType,
		tag,
		call,
		seen,
		mode,
		addressable,
	)

	if seenKey != "" {
		delete(seen, seenKey)
	}
}

func (graph deadCodeGraph) addReflectedMarshalMapKeyUses(
	l *packageLinter,
	out map[string]struct{},
	typ types.Type,
	tag string,
	call *ast.CallExpr,
	seen map[string]struct{},
) {
	if l.addReflectedMarshalHookUse(
		out,
		typ,
		reflectedHookNames(tag, reflectedMarshalStructFields, reflectedMapKeyHook),
		reflectedMarshalUnaddressable,
	) {
		return
	}

	if reflectedMapKeyFallbackField(tag) {
		graph.addReflectedMarshalUses(
			l,
			out,
			typ,
			tag,
			call,
			seen,
			reflectedMarshalUnaddressable,
		)
	}
}

func reflectedMarshalHookAddressability(
	typ types.Type,
	tag string,
	addressable reflectedMarshalAddressability,
) reflectedMarshalAddressability {
	if tag == reflectedYAMLTag {
		return reflectedMarshalUnaddressable
	}

	return addressable
}

func reflectedMarshalSequentialContainerElem(
	typ types.Type,
	addressable reflectedMarshalAddressability,
) (types.Type, reflectedMarshalAddressability, bool) {
	typ = types.Unalias(typ)

	switch typ := typ.(type) {
	case *types.Pointer:
		return typ.Elem(), reflectedMarshalAddressable, true
	case *types.Slice:
		return typ.Elem(), reflectedMarshalAddressable, true
	case *types.Array:
		return typ.Elem(), addressable, true
	default:
		return nil, reflectedMarshalUnaddressable, false
	}
}

func reflectedSequentialContainerElem(typ types.Type) (types.Type, bool) {
	typ = types.Unalias(typ)

	switch typ := typ.(type) {
	case *types.Pointer:
		return typ.Elem(), true
	case *types.Slice:
		return typ.Elem(), true
	case *types.Array:
		return typ.Elem(), true
	default:
		return nil, false
	}
}

func reflectedMapTypes(typ types.Type) (types.Type, types.Type, bool) {
	mapping, ok := types.Unalias(typ).Underlying().(*types.Map)
	if !ok {
		return nil, nil, false
	}

	return mapping.Key(), mapping.Elem(), true
}

func reflectedNamedStructType(typ types.Type) (*types.Named, *types.Struct) {
	typ = types.Unalias(typ)
	named := namedDeadCodeType(typ)

	return named, deadCodeStructType(typ)
}

func enterReflectedNamedType(
	seen map[string]struct{},
	named *types.Named,
) (string, bool) {
	if named == nil || named.Obj() == nil || named.Obj().Pkg() == nil {
		return "", true
	}

	key := named.Obj().Pkg().Path() + "." + named.Obj().Name()
	if _, ok := seen[key]; ok {
		return "", false
	}

	seen[key] = struct{}{}

	return key, true
}

func (graph deadCodeGraph) addReflectedStructFields(
	l *packageLinter,
	out map[string]struct{},
	named *types.Named,
	structType *types.Struct,
	tag string,
	call *ast.CallExpr,
	seen map[string]struct{},
	mode reflectedStructFieldUseMode,
	addressable reflectedMarshalAddressability,
) {
	owners := l.reflectedStructFieldOwners(named, tag, call, mode)
	graph.addReflectedStructFieldsWithOwners(
		l,
		out,
		owners,
		structType,
		tag,
		call,
		seen,
		mode,
		addressable,
	)
}

func (graph deadCodeGraph) addReflectedStructFieldsWithOwners(
	l *packageLinter,
	out map[string]struct{},
	owners []*types.Named,
	structType *types.Struct,
	tag string,
	call *ast.CallExpr,
	seen map[string]struct{},
	mode reflectedStructFieldUseMode,
	addressable reflectedMarshalAddressability,
) {
	for index := range structType.NumFields() {
		field := structType.Field(index)
		if field == nil {
			continue
		}

		fieldTag := reflectedStructFieldTag(structType.Tag(index), tag)
		if fieldTag.ignored {
			continue
		}

		if field.Exported() {
			for _, owner := range owners {
				addStructFieldUse(out, owner, field.Name())
			}
		}

		if field.Exported() || field.Anonymous() {
			if fieldTag.attr {
				l.addReflectedStructFieldAttrHookUse(
					out,
					field.Type(),
					tag,
					mode,
					addressable,
				)

				continue
			}

			graph.addReflectedStructFieldNestedUses(
				l,
				out,
				field.Type(),
				tag,
				call,
				seen,
				mode,
				addressable,
			)
		}
	}
}

func (l *packageLinter) addReflectedStructFieldAttrHookUse(
	out map[string]struct{},
	typ types.Type,
	tag string,
	mode reflectedStructFieldUseMode,
	addressable reflectedMarshalAddressability,
) {
	switch mode {
	case reflectedDecodeStructFields:
		l.addReflectedHookUse(
			out,
			typ,
			reflectedHookNames(tag, reflectedDecodeStructFields, reflectedAttrHook),
		)
	case reflectedMarshalStructFields:
		l.addReflectedMarshalHookUse(
			out,
			typ,
			reflectedHookNames(tag, reflectedMarshalStructFields, reflectedAttrHook),
			addressable,
		)
	}
}

func (graph deadCodeGraph) addReflectedStructFieldNestedUses(
	l *packageLinter,
	out map[string]struct{},
	typ types.Type,
	tag string,
	call *ast.CallExpr,
	seen map[string]struct{},
	mode reflectedStructFieldUseMode,
	addressable reflectedMarshalAddressability,
) {
	switch mode {
	case reflectedDecodeStructFields:
		graph.addReflectedDecodeUses(l, out, typ, tag, call, seen)
	case reflectedMarshalStructFields:
		graph.addReflectedMarshalUses(l, out, typ, tag, call, seen, addressable)
	}
}

func (l *packageLinter) addReflectedHookUse(
	out map[string]struct{},
	typ types.Type,
	names []string,
) bool {
	fn := reflectedTypeHookMethod(typ, names)
	if fn == nil {
		return false
	}

	key := deadCodeObjectKey(fn)
	if key != "" {
		out[key] = struct{}{}
	}

	return true
}

func (l *packageLinter) addReflectedMarshalHookUse(
	out map[string]struct{},
	typ types.Type,
	names []string,
	addressable reflectedMarshalAddressability,
) bool {
	fn := reflectedTypeMarshalHookMethod(typ, names, addressable)
	if fn == nil {
		return false
	}

	addReflectedHookFuncUse(out, fn)

	return true
}

func addReflectedHookFuncUse(out map[string]struct{}, fn *types.Func) {
	key := deadCodeObjectKey(fn)
	if key != "" {
		out[key] = struct{}{}
	}
}

func reflectedTypeHookMethod(typ types.Type, names []string) *types.Func {
	named := namedDeadCodeType(typ)
	if named == nil {
		return nil
	}

	for _, name := range names {
		if fn := reflectedTypeMethod(named, name); reflectedHookMethodSignature(fn, name) {
			return fn
		}

		if fn := reflectedTypeMethod(
			types.NewPointer(named),
			name,
		); reflectedHookMethodSignature(
			fn,
			name,
		) {
			return fn
		}
	}

	return nil
}

func reflectedTypeMarshalHookMethod(
	typ types.Type,
	names []string,
	addressable reflectedMarshalAddressability,
) *types.Func {
	typ = types.Unalias(typ)
	if ptr, ok := typ.(*types.Pointer); ok {
		return reflectedTypeMethodSetMethod(ptr, names, reflectedMarshalUnaddressable)
	}

	named, _ := typ.(*types.Named)
	if named == nil {
		return nil
	}

	if fn := reflectedTypeMethodSetMethod(named, names, reflectedMarshalUnaddressable); fn != nil {
		return fn
	}

	if addressable == reflectedMarshalAddressable {
		return reflectedTypeMethodSetMethod(named, names, reflectedMarshalAddressable)
	}

	return nil
}

func reflectedTypeMethodSetMethod(
	typ types.Type,
	names []string,
	addressable reflectedMarshalAddressability,
) *types.Func {
	for _, name := range names {
		obj, _, _ := types.LookupFieldOrMethod(
			typ,
			addressable == reflectedMarshalAddressable,
			nil,
			name,
		)
		if fn, _ := obj.(*types.Func); reflectedHookMethodSignature(fn, name) {
			return fn
		}
	}

	return nil
}

func reflectedTypeMethod(typ types.Type, name string) *types.Func {
	obj, _, _ := types.LookupFieldOrMethod(typ, true, nil, name)
	fn, _ := obj.(*types.Func)

	return fn
}
