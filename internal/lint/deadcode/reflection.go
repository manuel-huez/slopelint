package deadcode

import (
	"go/ast"
	"go/types"
)

type reflectedDecodeTarget struct {
	typ    types.Type
	codec  reflectedCodecUse
	mapKey bool
}

type reflectedMarshalTarget struct {
	typ         types.Type
	codec       reflectedCodecUse
	addressable reflectedMarshalAddressability
}

type reflectedTypeParamUse struct {
	index       int
	codec       reflectedCodecUse
	pointerOnly bool
	mapKey      bool
}

type reflectedMarshalTypeParamUse struct {
	typ   types.Type
	codec reflectedCodecUse
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
	targets := graph.reflectedDecodeTargetTypes(l, fn, call)

	targets = append(targets, graph.reflectedGenericDecodeTypes(l, fn, call)...)
	for _, target := range targets {
		if target.mapKey {
			if !l.addReflectedDecodeHookUse(
				out,
				target.typ,
				target.codec,
				reflectedMapKeyHook,
			) &&
				reflectedMapKeyFallbackField(target.codec.hookTag) {
				graph.addReflectedDecodeUses(
					l,
					out,
					target.typ,
					target.codec,
					call,
					make(map[string]struct{}),
				)
			}

			continue
		}

		graph.addReflectedDecodeUses(
			l,
			out,
			target.typ,
			target.codec,
			call,
			make(map[string]struct{}),
		)
	}

	marshalTargets := graph.reflectedMarshalTargetTypes(l, fn, call)

	marshalTargets = append(marshalTargets, graph.reflectedGenericMarshalTypes(l, fn, call)...)
	for _, target := range marshalTargets {
		graph.addReflectedMarshalUses(
			l,
			out,
			target.typ,
			target.codec,
			call,
			make(map[string]struct{}),
			target.addressable,
		)
	}

	return out
}

func inspectReflectedBodyCalls(body *ast.BlockStmt, visit func(*ast.CallExpr)) {
	if body == nil || visit == nil {
		return
	}

	ast.Inspect(body, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}

		call, ok := n.(*ast.CallExpr)
		if ok {
			visit(call)
		}

		return true
	})
}

func (graph deadCodeGraph) reflectedDecodeTargetTypes(
	l *packageLinter,
	fn *types.Func,
	call *ast.CallExpr,
) []reflectedDecodeTarget {
	codec, ok := reflectedDecodeFuncCodec(fn)
	if !ok || len(call.Args) == 0 {
		return graph.reflectedWrapperDecodeTargetTypes(l, fn, call)
	}

	argIndex := reflectedDecodeTargetArgIndex(fn, call)
	if argIndex < 0 || argIndex >= len(call.Args) {
		return nil
	}

	target := call.Args[argIndex]

	typ := reflectedValueType(l.pkg.TypesInfo, target)
	if !reflectedDecodeTargetType(typ) {
		return nil
	}

	return []reflectedDecodeTarget{{typ: typ, codec: codec}}
}

func (graph deadCodeGraph) reflectedMarshalTargetTypes(
	l *packageLinter,
	fn *types.Func,
	call *ast.CallExpr,
) []reflectedMarshalTarget {
	codec, ok := reflectedMarshalFuncCodec(fn)
	if !ok || len(call.Args) == 0 {
		return graph.reflectedWrapperMarshalTargetTypes(l, fn, call)
	}

	argIndex := reflectedMarshalTargetArgIndex(fn, call)
	if argIndex < 0 || argIndex >= len(call.Args) {
		return nil
	}

	return []reflectedMarshalTarget{{
		typ:         reflectedValueType(l.pkg.TypesInfo, call.Args[argIndex]),
		codec:       codec,
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
	codec reflectedCodecUse,
	call *ast.CallExpr,
	seen map[string]struct{},
) {
	if l.addReflectedDecodeHookUse(out, typ, codec, reflectedValueHook) {
		return
	}

	if elem, ok := reflectedSequentialContainerElem(typ); ok {
		graph.addReflectedDecodeUses(l, out, elem, codec, call, seen)

		return
	}

	if key, elem, ok := reflectedMapTypes(typ); ok {
		if !l.addReflectedDecodeHookUse(out, key, codec, reflectedMapKeyHook) &&
			reflectedMapKeyFallbackField(codec.hookTag) {
			graph.addReflectedDecodeUses(l, out, key, codec, call, seen)
		}

		graph.addReflectedDecodeUses(l, out, elem, codec, call, seen)

		return
	}

	graph.addReflectedNamedStructUses(
		l,
		out,
		typ,
		codec,
		call,
		seen,
		reflectedDecodeStructFields,
		reflectedMarshalUnaddressable,
	)
}

func (graph deadCodeGraph) addReflectedMarshalUses(
	l *packageLinter,
	out map[string]struct{},
	typ types.Type,
	codec reflectedCodecUse,
	call *ast.CallExpr,
	seen map[string]struct{},
	addressable reflectedMarshalAddressability,
) {
	hook := reflectedTypeMarshalHookMethod(
		typ,
		reflectedHookNames(codec.hookTag, reflectedMarshalStructFields, reflectedValueHook),
		codec.hookTag,
		reflectedMarshalHookAddressability(typ, codec.tag, addressable),
	)
	if hook != nil {
		addReflectedHookFuncUse(out, hook)
		graph.addReflectedYAMLMarshalReturnUses(l, out, hook, typ, codec.tag, call, seen)

		return
	}

	if elem, elemAddressable, ok := reflectedMarshalSequentialContainerElem(
		typ,
		addressable,
	); ok {
		graph.addReflectedMarshalUses(l, out, elem, codec, call, seen, elemAddressable)

		return
	}

	if key, elem, ok := reflectedMapTypes(typ); ok {
		graph.addReflectedMarshalMapKeyUses(l, out, key, codec, call, seen)
		graph.addReflectedMarshalUses(
			l,
			out,
			elem,
			codec,
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
		codec,
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
	codec reflectedCodecUse,
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

	graph.addReflectedStructFields(l, out, named, structType, codec, call, seen, mode, addressable)

	if seenKey != "" {
		delete(seen, seenKey)
	}
}

func (l *packageLinter) addReflectedDecodeHookUse(
	out map[string]struct{},
	typ types.Type,
	codec reflectedCodecUse,
	context reflectedHookContext,
) bool {
	return l.addReflectedHookUse(
		out,
		typ,
		reflectedHookNames(codec.hookTag, reflectedDecodeStructFields, context),
		codec.hookTag,
	)
}

func (graph deadCodeGraph) addReflectedMarshalMapKeyUses(
	l *packageLinter,
	out map[string]struct{},
	typ types.Type,
	codec reflectedCodecUse,
	call *ast.CallExpr,
	seen map[string]struct{},
) {
	if l.addReflectedMarshalHookUse(
		out,
		typ,
		reflectedHookNames(codec.hookTag, reflectedMarshalStructFields, reflectedMapKeyHook),
		codec.hookTag,
		reflectedMarshalUnaddressable,
	) {
		return
	}

	if reflectedMapKeyFallbackField(codec.hookTag) {
		graph.addReflectedMarshalUses(
			l,
			out,
			typ,
			codec,
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
	codec reflectedCodecUse,
	call *ast.CallExpr,
	seen map[string]struct{},
	mode reflectedStructFieldUseMode,
	addressable reflectedMarshalAddressability,
) {
	owners := l.reflectedStructFieldOwners(named, codec, call, mode)
	graph.addReflectedStructFieldsWithOwners(
		l,
		out,
		owners,
		structType,
		codec,
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
	codec reflectedCodecUse,
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

		fieldTag := reflectedStructFieldTag(structType.Tag(index), codec.tag)
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
					codec,
					mode,
					addressable,
				)

				continue
			}

			graph.addReflectedStructFieldNestedUses(
				l,
				out,
				field.Type(),
				codec,
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
	codec reflectedCodecUse,
	mode reflectedStructFieldUseMode,
	addressable reflectedMarshalAddressability,
) {
	switch mode {
	case reflectedDecodeStructFields:
		l.addReflectedHookUse(
			out,
			typ,
			reflectedHookNames(codec.hookTag, reflectedDecodeStructFields, reflectedAttrHook),
			codec.hookTag,
		)
	case reflectedMarshalStructFields:
		l.addReflectedMarshalHookUse(
			out,
			typ,
			reflectedHookNames(codec.hookTag, reflectedMarshalStructFields, reflectedAttrHook),
			codec.hookTag,
			addressable,
		)
	}
}

func (graph deadCodeGraph) addReflectedStructFieldNestedUses(
	l *packageLinter,
	out map[string]struct{},
	typ types.Type,
	codec reflectedCodecUse,
	call *ast.CallExpr,
	seen map[string]struct{},
	mode reflectedStructFieldUseMode,
	addressable reflectedMarshalAddressability,
) {
	switch mode {
	case reflectedDecodeStructFields:
		graph.addReflectedDecodeUses(l, out, typ, codec, call, seen)
	case reflectedMarshalStructFields:
		graph.addReflectedMarshalUses(l, out, typ, codec, call, seen, addressable)
	}
}

func (l *packageLinter) addReflectedHookUse(
	out map[string]struct{},
	typ types.Type,
	names []string,
	hookTag string,
) bool {
	fn := reflectedTypeHookMethod(typ, names, hookTag)
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
	hookTag string,
	addressable reflectedMarshalAddressability,
) bool {
	fn := reflectedTypeMarshalHookMethod(typ, names, hookTag, addressable)
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

func reflectedTypeHookMethod(typ types.Type, names []string, hookTag string) *types.Func {
	named := namedDeadCodeType(typ)
	if named == nil {
		return nil
	}

	for _, name := range names {
		if fn := reflectedTypeMethod(named, name); reflectedHookMethodSignature(fn, hookTag, name) {
			return fn
		}

		if fn := reflectedTypeMethod(
			types.NewPointer(named),
			name,
		); reflectedHookMethodSignature(fn, hookTag, name) {
			return fn
		}
	}

	return nil
}

func reflectedTypeMarshalHookMethod(
	typ types.Type,
	names []string,
	hookTag string,
	addressable reflectedMarshalAddressability,
) *types.Func {
	typ = types.Unalias(typ)
	if ptr, ok := typ.(*types.Pointer); ok {
		return reflectedTypeMethodSetMethod(ptr, names, hookTag, reflectedMarshalUnaddressable)
	}

	named, _ := typ.(*types.Named)
	if named == nil {
		return nil
	}

	if fn := reflectedTypeMethodSetMethod(
		named,
		names,
		hookTag,
		reflectedMarshalUnaddressable,
	); fn != nil {
		return fn
	}

	if addressable == reflectedMarshalAddressable {
		return reflectedTypeMethodSetMethod(named, names, hookTag, reflectedMarshalAddressable)
	}

	return nil
}

func reflectedTypeMethodSetMethod(
	typ types.Type,
	names []string,
	hookTag string,
	addressable reflectedMarshalAddressability,
) *types.Func {
	for _, name := range names {
		obj, _, _ := types.LookupFieldOrMethod(
			typ,
			addressable == reflectedMarshalAddressable,
			nil,
			name,
		)
		if fn, _ := obj.(*types.Func); reflectedHookMethodSignature(fn, hookTag, name) {
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
