package deadcode

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"
)

func (graph deadCodeGraph) reflectedGenericDecodeTypes(
	l *packageLinter,
	fn *types.Func,
	call *ast.CallExpr,
) []reflectedDecodeTarget {
	if fn == nil {
		return nil
	}

	typeArgs := reflectedGenericCallTypeArgs(l.pkg.TypesInfo, call)
	if typeArgs == nil || typeArgs.Len() == 0 {
		return nil
	}

	// Source inspection wins; name fallback is only for external generic codecs.
	decodes, inspected := graph.reflectedDecodeTypeParamDecodes(fn)
	if !inspected {
		decodes = fallbackGenericDecodeTypeParamDecodes(fn, typeArgs.Len(), l.pkg.FSet)
	}

	return reflectedGenericTargetsForTypeArgs(typeArgs, decodes)
}

func (graph deadCodeGraph) reflectedGenericMarshalTypes(
	l *packageLinter,
	fn *types.Func,
	call *ast.CallExpr,
) []reflectedMarshalTarget {
	if fn == nil {
		return nil
	}

	typeArgs := reflectedGenericCallTypeArgs(l.pkg.TypesInfo, call)
	if typeArgs == nil || typeArgs.Len() == 0 {
		return nil
	}

	marshals, inspected := graph.reflectedMarshalTypeParamUses(fn)
	if !inspected {
		marshals = fallbackGenericMarshalTypeParamUses(fn, l.pkg.FSet)
	}

	replacements := reflectedTypeParamReplacements(fn, typeArgs)

	out := make([]reflectedMarshalTarget, 0, len(marshals))
	for _, marshal := range marshals {
		out = append(out, reflectedMarshalTarget{
			typ:         substituteReflectedTypeParams(marshal.typ, replacements),
			codec:       marshal.codec,
			addressable: reflectedMarshalUnaddressable,
		})
	}

	return out
}

func fallbackGenericDecodeTypeParamDecodes(
	fn *types.Func,
	count int,
	fset *token.FileSet,
) []reflectedTypeParamUse {
	uses, inspected := sourceGenericDecodeTypeParamDecodes(fn, fset)
	if inspected {
		return dedupeComparable(uses, reflectedDedupeMinLen)
	}

	return nameFallbackGenericDecodeTypeParamDecodes(fn, count)
}

func nameFallbackGenericDecodeTypeParamDecodes(
	fn *types.Func,
	count int,
) []reflectedTypeParamUse {
	tag, ok := genericDecodeFallbackTag(fn)
	if !ok {
		return nil
	}

	codec := reflectedCodecUseForTag(tag)

	sig, ok := genericFallbackSignature(fn)
	if !ok || !genericDecodeHasEncodedInput(sig) {
		return nil
	}

	out := make([]reflectedTypeParamUse, 0, count)
	indexes := genericTypeParamIndexes(fn)

	collectFallbackGenericDecodeParamDecodes(sig, codec, indexes, &out)

	if resultTag, ok := genericDecodeExplicitFallbackTag(fn); ok {
		collectFallbackGenericDecodeResultDecodes(
			sig,
			reflectedCodecUseForTag(resultTag),
			indexes,
			&out,
		)
	}

	out = dedupeComparable(out, reflectedDedupeMinLen)
	if len(out) == 0 {
		return nil
	}

	return out
}

func fallbackGenericMarshalTypeParamUses(
	fn *types.Func,
	fset *token.FileSet,
) []reflectedMarshalTypeParamUse {
	uses := sourceGenericMarshalTypeParamUses(fn, fset)
	if len(uses) == 0 {
		return nil
	}

	return dedupeReflectedMarshalTypeParamUses(uses)
}

func sourceGenericMarshalTypeParamUses(
	fn *types.Func,
	fset *token.FileSet,
) []reflectedMarshalTypeParamUse {
	scan, ok := sourceGenericCodecScanFor(
		fn,
		fset,
		func(codec reflectedPackageCodec) map[string]reflectedCodecFunc {
			return codec.marshalFuncs
		},
	)
	if !ok {
		return nil
	}

	out := make([]reflectedMarshalTypeParamUse, 0)

	inspectReflectedCalls(scan.decl.Body, func(call *ast.CallExpr) {
		codec, arg, ok := scan.callArg(call)
		if !ok {
			return
		}

		if ident, ok := unparenReflectedExpr(arg).(*ast.Ident); ok {
			addReflectedMarshalTypeParamUse(
				sourceFuncParamTypeAt(scan.paramTypes, scan.decl.Body, ident),
				codec,
				scan.indexes,
				&out,
			)
		}
	})

	return out
}

func genericFallbackSignature(fn *types.Func) (*types.Signature, bool) {
	if fn == nil {
		return nil, false
	}

	sig, ok := fn.Origin().Type().(*types.Signature)

	return sig, ok && sig != nil
}

func genericDecodeHasEncodedInput(sig *types.Signature) bool {
	for index := range tupleLen(sig.Params()) {
		if genericDecodeEncodedInputType(sig.Params().At(index).Type()) {
			return true
		}
	}

	return false
}

func genericDecodeEncodedInputType(typ types.Type) bool {
	typ = types.Unalias(typ)

	switch typ := typ.(type) {
	case *types.Basic:
		return typ.Kind() == types.String
	case *types.Slice:
		elem, _ := types.Unalias(typ.Elem()).(*types.Basic)

		return elem != nil && elem.Kind() == types.Uint8
	default:
		return namedTypeMatches(typ, "io", "Reader")
	}
}

func collectFallbackGenericDecodeParamDecodes(
	sig *types.Signature,
	codec reflectedCodecUse,
	indexes map[*types.TypeParam]int,
	out *[]reflectedTypeParamUse,
) {
	for index := range tupleLen(sig.Params()) {
		typ := sig.Params().At(index).Type()
		if genericDecodeEncodedInputType(typ) {
			continue
		}

		collectFallbackGenericDecodeParamTypeDecodes(typ, codec, indexes, out)
	}
}

func collectFallbackGenericDecodeParamTypeDecodes(
	typ types.Type,
	codec reflectedCodecUse,
	indexes map[*types.TypeParam]int,
	out *[]reflectedTypeParamUse,
) {
	typ = types.Unalias(typ)

	if _, ok := typ.(*types.Pointer); ok {
		collectReflectedSettableTypeParamDecodes(typ, codec, indexes, out)
	}
}

func collectFallbackGenericDecodeResultDecodes(
	sig *types.Signature,
	codec reflectedCodecUse,
	indexes map[*types.TypeParam]int,
	out *[]reflectedTypeParamUse,
) {
	for index := range tupleLen(sig.Results()) {
		collectReflectedSettableTypeParamDecodes(
			sig.Results().At(index).Type(),
			codec,
			indexes,
			out,
		)
	}
}

func reflectedGenericTargetsForTypeArgs(
	typeArgs *types.TypeList,
	decodes []reflectedTypeParamUse,
) []reflectedDecodeTarget {
	if len(decodes) == 0 {
		return nil
	}

	out := make([]reflectedDecodeTarget, 0, len(decodes))
	for _, decode := range decodes {
		if decode.index >= typeArgs.Len() {
			continue
		}

		typ := typeArgs.At(decode.index)
		if decode.pointerOnly && !reflectedDecodeTargetType(typ) {
			continue
		}

		out = append(out, reflectedDecodeTarget{
			typ:    typ,
			codec:  decode.codec,
			mapKey: decode.mapKey,
		})
	}

	return out
}

func genericDecodeFallbackTag(fn *types.Func) (string, bool) {
	name, path, ok := genericCodecFallbackNamePath(fn)
	if !ok {
		return "", false
	}

	if !strings.Contains(name, "decode") && !strings.Contains(name, "unmarshal") {
		return "", false
	}

	if !genericCodecFallbackEvidence(path) {
		return "", false
	}

	return genericFallbackTag(name, path), true
}

func genericDecodeExplicitFallbackTag(fn *types.Func) (string, bool) {
	name, path, ok := genericCodecFallbackNamePath(fn)
	if !ok {
		return "", false
	}

	switch {
	case strings.Contains(name, "xml") || strings.Contains(path, "xml"):
		return reflectedXMLTag, true
	case strings.Contains(name, "yaml") || strings.Contains(path, "yaml"):
		return reflectedYAMLTag, true
	case strings.Contains(name, "json") || strings.Contains(path, "json"):
		return reflectedJSONTag, true
	default:
		return "", false
	}
}

func genericCodecFallbackEvidence(path string) bool {
	codecNames := [...]string{"json", "xml", "yaml", "jsoncodec", "xmlcodec", "yamlcodec"}

	base := genericFallbackPathBase(path)
	for _, token := range codecNames {
		if base == token {
			return true
		}
	}

	return false
}

func genericFallbackPathBase(path string) string {
	index := strings.LastIndex(path, "/")
	if index < 0 {
		return path
	}

	return path[index+1:]
}

func genericFallbackTag(name string, path string) string {
	switch {
	case strings.Contains(name, "xml") || strings.Contains(path, "xml"):
		return reflectedXMLTag
	case strings.Contains(name, "yaml") || strings.Contains(path, "yaml"):
		return reflectedYAMLTag
	default:
		return reflectedJSONTag
	}
}

func genericCodecFallbackNamePath(fn *types.Func) (string, string, bool) {
	if fn == nil {
		return "", "", false
	}

	name := strings.ToLower(fn.Name())

	path := ""
	if fn.Pkg() != nil {
		path = strings.ToLower(fn.Pkg().Path())
	}

	return name, path, true
}

func (graph deadCodeGraph) reflectedDecodeTypeParamDecodes(
	fn *types.Func,
) ([]reflectedTypeParamUse, bool) {
	return graph.reflectedDecodeTypeParamDecodesSeen(fn, make(map[string]struct{}))
}

func (graph deadCodeGraph) reflectedDecodeTypeParamDecodesSeen(
	fn *types.Func,
	funcsSeen map[string]struct{},
) ([]reflectedTypeParamUse, bool) {
	out := make([]reflectedTypeParamUse, 0)

	inspected := graph.inspectReflectedFuncTypeParamUsesSeen(
		fn,
		funcsSeen,
		func(
			pkg *Package,
			call *ast.CallExpr,
			typeParamIndexes map[*types.TypeParam]int,
			scope *ast.BlockStmt,
		) {
			graph.collectReflectedDecodeCallTypeParamDecodes(
				pkg,
				call,
				typeParamIndexes,
				scope,
				funcsSeen,
				&out,
			)
		},
	)
	if !inspected {
		return nil, false
	}

	return dedupeComparable(out, reflectedDedupeMinLen), true
}

func (graph deadCodeGraph) reflectedMarshalTypeParamUses(
	fn *types.Func,
) ([]reflectedMarshalTypeParamUse, bool) {
	return graph.reflectedMarshalTypeParamUsesSeen(fn, make(map[string]struct{}))
}

func (graph deadCodeGraph) reflectedMarshalTypeParamUsesSeen(
	fn *types.Func,
	funcsSeen map[string]struct{},
) ([]reflectedMarshalTypeParamUse, bool) {
	out := make([]reflectedMarshalTypeParamUse, 0)

	inspected := graph.inspectReflectedFuncTypeParamUsesSeen(
		fn,
		funcsSeen,
		func(
			pkg *Package,
			call *ast.CallExpr,
			typeParamIndexes map[*types.TypeParam]int,
			scope *ast.BlockStmt,
		) {
			graph.collectReflectedMarshalCallTypeParamUses(
				pkg,
				call,
				typeParamIndexes,
				scope,
				funcsSeen,
				&out,
			)
		},
	)
	if !inspected {
		return nil, false
	}

	return dedupeReflectedMarshalTypeParamUses(out), true
}

func (graph deadCodeGraph) inspectReflectedFuncTypeParamUsesSeen(
	fn *types.Func,
	funcsSeen map[string]struct{},
	visit func(*Package, *ast.CallExpr, map[*types.TypeParam]int, *ast.BlockStmt),
) bool {
	pkg := graph.packageForFunc(fn)
	if pkg == nil {
		return false
	}

	decl := graph.funcDeclForObject(pkg, fn)
	if decl == nil || decl.Body == nil {
		return false
	}

	typeParamIndexes := genericTypeParamIndexes(genericFuncObject(pkg, decl))
	if len(typeParamIndexes) == 0 {
		return true
	}

	key := deadCodeObjectKey(fn)
	if key != "" {
		if _, ok := funcsSeen[key]; ok {
			return true
		}

		funcsSeen[key] = struct{}{}
		defer delete(funcsSeen, key)
	}

	inspectReflectedCalls(decl.Body, func(call *ast.CallExpr) {
		visit(pkg, call, typeParamIndexes, decl.Body)
	})

	return true
}

func (graph deadCodeGraph) collectReflectedDecodeCallTypeParamDecodes(
	pkg *Package,
	call *ast.CallExpr,
	typeParamIndexes map[*types.TypeParam]int,
	scope *ast.BlockStmt,
	funcsSeen map[string]struct{},
	out *[]reflectedTypeParamUse,
) {
	if fn, codec, ok := reflectedTargetCall(pkg, call, reflectedDecodeFuncCodec); ok {
		argIndex := reflectedDecodeTargetArgIndex(fn, call)
		if argIndex < 0 || argIndex >= len(call.Args) {
			return
		}

		target := call.Args[argIndex]
		collectReflectedDecodeTargetTypeParamDecodes(
			reflectedValueType(pkg.TypesInfo, target),
			codec,
			typeParamIndexes,
			out,
		)

		return
	}

	graph.collectDelegatedReflectedTypeParamDecodes(
		pkg,
		call,
		typeParamIndexes,
		funcsSeen,
		out,
	)
	graph.collectFuncLitArgReflectedTypeParamDecodes(
		pkg,
		call,
		typeParamIndexes,
		scope,
		funcsSeen,
		out,
	)
}

func (graph deadCodeGraph) collectDelegatedReflectedTypeParamDecodes(
	pkg *Package,
	call *ast.CallExpr,
	typeParamIndexes map[*types.TypeParam]int,
	funcsSeen map[string]struct{},
	out *[]reflectedTypeParamUse,
) {
	typeArgs := reflectedGenericCallTypeArgs(pkg.TypesInfo, call)
	if typeArgs == nil || typeArgs.Len() == 0 {
		return
	}

	callee := calledFunc(pkg.TypesInfo, call)

	decodes, inspected := graph.reflectedDecodeTypeParamDecodesSeen(callee, funcsSeen)
	if !inspected {
		decodes = fallbackGenericDecodeTypeParamDecodes(callee, typeArgs.Len(), pkg.FSet)
	}

	for _, decode := range decodes {
		if decode.index >= typeArgs.Len() {
			continue
		}

		typ := typeArgs.At(decode.index)
		if decode.mapKey {
			collectReflectedMapKeyTypeParamDecodes(
				typ,
				decode.codec,
				typeParamIndexes,
				out,
			)

			continue
		}

		if decode.pointerOnly {
			collectReflectedDecodeTargetTypeParamDecodes(
				typ,
				decode.codec,
				typeParamIndexes,
				out,
			)

			continue
		}

		collectReflectedSettableTypeParamDecodes(
			typ,
			decode.codec,
			typeParamIndexes,
			out,
		)
	}
}

func (graph deadCodeGraph) collectReflectedMarshalCallTypeParamUses(
	pkg *Package,
	call *ast.CallExpr,
	typeParamIndexes map[*types.TypeParam]int,
	scope *ast.BlockStmt,
	funcsSeen map[string]struct{},
	out *[]reflectedMarshalTypeParamUse,
) {
	if fn, codec, ok := reflectedTargetCall(pkg, call, reflectedMarshalFuncCodec); ok {
		argIndex := reflectedMarshalTargetArgIndex(fn, call)
		if argIndex < 0 || argIndex >= len(call.Args) {
			return
		}

		addReflectedMarshalTypeParamUse(
			reflectedValueType(pkg.TypesInfo, call.Args[argIndex]),
			codec,
			typeParamIndexes,
			out,
		)

		return
	}

	graph.collectDelegatedReflectedTypeParamMarshals(
		pkg,
		call,
		typeParamIndexes,
		funcsSeen,
		out,
	)
	graph.collectFuncLitArgReflectedTypeParamMarshals(
		pkg,
		call,
		typeParamIndexes,
		scope,
		funcsSeen,
		out,
	)
}

func (graph deadCodeGraph) collectDelegatedReflectedTypeParamMarshals(
	pkg *Package,
	call *ast.CallExpr,
	typeParamIndexes map[*types.TypeParam]int,
	funcsSeen map[string]struct{},
	out *[]reflectedMarshalTypeParamUse,
) {
	typeArgs := reflectedGenericCallTypeArgs(pkg.TypesInfo, call)
	if typeArgs == nil || typeArgs.Len() == 0 {
		return
	}

	callee := calledFunc(pkg.TypesInfo, call)

	marshals, inspected := graph.reflectedMarshalTypeParamUsesSeen(callee, funcsSeen)
	if !inspected {
		marshals = fallbackGenericMarshalTypeParamUses(callee, pkg.FSet)
	}

	replacements := reflectedTypeParamReplacements(callee, typeArgs)
	for _, marshal := range marshals {
		addReflectedMarshalTypeParamUse(
			substituteReflectedTypeParams(marshal.typ, replacements),
			marshal.codec,
			typeParamIndexes,
			out,
		)
	}
}

func addReflectedMarshalTypeParamUse(
	typ types.Type,
	codec reflectedCodecUse,
	typeParamIndexes map[*types.TypeParam]int,
	out *[]reflectedMarshalTypeParamUse,
) {
	if !reflectedTypeContainsParams(typ, typeParamIndexes) {
		return
	}

	*out = append(*out, reflectedMarshalTypeParamUse{
		typ:   typ,
		codec: codec,
	})
}

func dedupeReflectedMarshalTypeParamUses(
	uses []reflectedMarshalTypeParamUse,
) []reflectedMarshalTypeParamUse {
	if len(uses) < reflectedDedupeMinLen {
		return uses
	}

	seen := make(map[string]struct{}, len(uses))

	out := make([]reflectedMarshalTypeParamUse, 0, len(uses))
	for _, use := range uses {
		key := use.codec.tag + "\x00" + use.codec.hookTag + "\x00" + deadCodeTypeString(use.typ)
		if _, ok := seen[key]; ok {
			continue
		}

		seen[key] = struct{}{}

		out = append(out, use)
	}

	return out
}

func genericFuncObject(pkg *Package, decl *ast.FuncDecl) *types.Func {
	if pkg == nil || decl == nil || decl.Name == nil {
		return nil
	}

	obj, _ := pkg.TypesInfo.Defs[decl.Name].(*types.Func)

	return obj
}

func reflectedTargetCall(
	pkg *Package,
	call *ast.CallExpr,
	codecForFunc func(*types.Func) (reflectedCodecUse, bool),
) (*types.Func, reflectedCodecUse, bool) {
	if len(call.Args) == 0 {
		return nil, reflectedCodecUse{}, false
	}

	fn := calledFunc(pkg.TypesInfo, call)
	codec, ok := codecForFunc(fn)

	return fn, codec, ok
}

func (graph deadCodeGraph) packageForFunc(fn *types.Func) *Package {
	if fn == nil || fn.Pkg() == nil {
		return nil
	}

	return graph.packages[fn.Pkg().Path()]
}

func (graph deadCodeGraph) funcDeclForObject(pkg *Package, fn *types.Func) *ast.FuncDecl {
	if pkg == nil || fn == nil {
		return nil
	}

	target := fn.Origin()
	if target == nil {
		target = fn
	}

	if decl, ok := graph.funcDeclCache[target]; ok {
		return decl
	}

	if _, ok := graph.funcDeclMisses[target]; ok {
		return nil
	}

	targetKey := deadCodeObjectKey(fn)

	for _, decl := range pkg.ProductionFuncs {
		if decl == nil || decl.Name == nil {
			continue
		}

		obj, _ := pkg.TypesInfo.Defs[decl.Name].(*types.Func)
		if obj == nil {
			continue
		}

		if obj.Origin() == target || deadCodeObjectKey(obj) == targetKey {
			graph.funcDeclCache[target] = decl

			return decl
		}
	}

	graph.funcDeclMisses[target] = struct{}{}

	return nil
}

func genericTypeParamIndexes(fn *types.Func) map[*types.TypeParam]int {
	if fn == nil {
		return nil
	}

	sig, ok := fn.Origin().Type().(*types.Signature)
	if !ok || sig == nil {
		return nil
	}

	recvParams := sig.RecvTypeParams()

	typeParams := sig.TypeParams()
	if recvParams == nil && typeParams == nil {
		return nil
	}

	out := make(map[*types.TypeParam]int, typeParamListLen(recvParams)+typeParamListLen(typeParams))
	for index := range typeParamListLen(recvParams) {
		out[recvParams.At(index)] = index
	}

	offset := len(out)
	for index := range typeParamListLen(typeParams) {
		out[typeParams.At(index)] = offset + index
	}

	return out
}

func typeParamListLen(list *types.TypeParamList) int {
	if list == nil {
		return 0
	}

	return list.Len()
}

func reflectedGenericCallTypeArgs(info *types.Info, call *ast.CallExpr) *types.TypeList {
	ident := genericCalleeIdent(call.Fun)
	if ident != nil {
		if instance, ok := info.Instances[ident]; ok {
			return instance.TypeArgs
		}
	}

	selector := genericSelectorExpr(call.Fun)
	if selector == nil {
		return nil
	}

	selection := info.Selections[selector]
	if selection == nil {
		return nil
	}

	return namedTypeArgs(selection.Recv())
}

func reflectedValueType(info *types.Info, expr ast.Expr) types.Type {
	if call, ok := unparenReflectedExpr(expr).(*ast.CallExpr); ok &&
		conversionTargetType(info, call) != nil &&
		typeIsInterface(info.TypeOf(call)) {
		return reflectedValueType(info, call.Args[0])
	}

	return info.TypeOf(expr)
}

func unparenReflectedExpr(expr ast.Expr) ast.Expr {
	for {
		paren, ok := expr.(*ast.ParenExpr)
		if !ok {
			return expr
		}

		expr = paren.X
	}
}

func genericSelectorExpr(expr ast.Expr) *ast.SelectorExpr {
	switch expr := expr.(type) {
	case *ast.IndexExpr:
		return genericSelectorExpr(expr.X)
	case *ast.IndexListExpr:
		return genericSelectorExpr(expr.X)
	case *ast.SelectorExpr:
		return expr
	default:
		return nil
	}
}

func namedTypeArgs(typ types.Type) *types.TypeList {
	typ = types.Unalias(typ)
	if ptr, ok := typ.(*types.Pointer); ok {
		typ = types.Unalias(ptr.Elem())
	}

	named, _ := typ.(*types.Named)
	if named == nil {
		return nil
	}

	return named.TypeArgs()
}
