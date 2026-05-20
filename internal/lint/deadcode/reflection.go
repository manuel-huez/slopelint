package deadcode

import (
	"go/ast"
	"go/token"
	"go/types"
	"reflect"
	"strings"
)

type reflectedDecodeTarget struct {
	typ types.Type
	tag string
}

type reflectedTypeParamDecode struct {
	index       int
	tag         string
	pointerOnly bool
}

type reflectedTypeParamDecodeContext uint8

const (
	reflectedJSONTag      = "json"
	reflectedXMLTag       = "xml"
	reflectedYAMLTag      = "yaml"
	reflectedDedupeMinLen = 2
)

const (
	reflectedDecodeTargetContext reflectedTypeParamDecodeContext = iota
	reflectedSettableDecodeContext
)

func (graph deadCodeGraph) reflectedFieldUses(
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
		l.addReflectedStructFieldUses(out, target.typ, target.tag, call, make(map[string]struct{}))
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

	target := call.Args[len(call.Args)-1]

	typ := l.pkg.TypesInfo.TypeOf(target)
	if !reflectedDecodeTargetType(typ) {
		return nil
	}

	return []reflectedDecodeTarget{{typ: typ, tag: tag}}
}

func reflectedDecodeFuncTag(fn *types.Func) (string, bool) {
	if fn == nil || fn.Pkg() == nil {
		return "", false
	}

	// Only known reflection decoders set fields by name; local Decode funcs use normal edges.
	switch fn.Pkg().Path() {
	case "encoding/gob":
		return "", reflectedDecodeFuncName(fn.Name())
	case "encoding/json":
		return reflectedJSONTag, reflectedDecodeFuncName(fn.Name())
	case "encoding/xml":
		return reflectedXMLTag, reflectedDecodeFuncName(fn.Name())
	case "github.com/goccy/go-yaml",
		"gopkg.in/yaml.v2",
		"gopkg.in/yaml.v3":
		return reflectedYAMLTag, reflectedDecodeFuncName(fn.Name())
	case "sigs.k8s.io/yaml":
		return reflectedJSONTag, reflectedDecodeFuncName(fn.Name())
	}

	return "", false
}

func reflectedDecodeFuncName(name string) bool {
	return name == "Decode" || name == "Unmarshal"
}

func reflectedDecodeTargetType(typ types.Type) bool {
	if typ == nil {
		return false
	}

	_, ok := types.Unalias(typ).(*types.Pointer)

	return ok
}

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
		decodes = fallbackGenericDecodeTypeParamDecodes(fn, typeArgs.Len())
	}

	return reflectedGenericDecodeTypeArgs(typeArgs, decodes)
}

func fallbackGenericDecodeTypeParamDecodes(
	fn *types.Func,
	count int,
) []reflectedTypeParamDecode {
	tag, ok := genericDecodeFallbackTag(fn)
	if !ok {
		return nil
	}

	sig, ok := genericDecodeFallbackSignature(fn)
	if !ok || !genericDecodeHasEncodedInput(sig) {
		return nil
	}

	out := make([]reflectedTypeParamDecode, 0, count)
	indexes := genericTypeParamIndexes(fn)

	collectFallbackGenericDecodeParamDecodes(sig, tag, indexes, &out)

	if resultTag, ok := genericDecodeExplicitFallbackTag(fn); ok {
		collectFallbackGenericDecodeResultDecodes(sig, resultTag, indexes, &out)
	}

	out = dedupeReflectedTypeParamDecodes(out)
	if len(out) == 0 {
		return nil
	}

	return out
}

func genericDecodeFallbackSignature(fn *types.Func) (*types.Signature, bool) {
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
		return typ.String() == "io.Reader"
	}
}

func collectFallbackGenericDecodeParamDecodes(
	sig *types.Signature,
	tag string,
	indexes map[*types.TypeParam]int,
	out *[]reflectedTypeParamDecode,
) {
	for index := range tupleLen(sig.Params()) {
		typ := sig.Params().At(index).Type()
		if genericDecodeEncodedInputType(typ) {
			continue
		}

		collectFallbackGenericDecodeParamTypeDecodes(typ, tag, indexes, out)
	}
}

func collectFallbackGenericDecodeParamTypeDecodes(
	typ types.Type,
	tag string,
	indexes map[*types.TypeParam]int,
	out *[]reflectedTypeParamDecode,
) {
	typ = types.Unalias(typ)

	if _, ok := typ.(*types.Pointer); ok {
		collectReflectedSettableTypeParamDecodes(typ, tag, indexes, out)
	}
}

func collectFallbackGenericDecodeResultDecodes(
	sig *types.Signature,
	tag string,
	indexes map[*types.TypeParam]int,
	out *[]reflectedTypeParamDecode,
) {
	for index := range tupleLen(sig.Results()) {
		collectReflectedSettableTypeParamDecodes(sig.Results().At(index).Type(), tag, indexes, out)
	}
}

func reflectedGenericDecodeTypeArgs(
	typeArgs *types.TypeList,
	decodes []reflectedTypeParamDecode,
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
			typ: typ,
			tag: decode.tag,
		})
	}

	return out
}

func genericDecodeFallbackTag(fn *types.Func) (string, bool) {
	name, path, ok := genericDecodeFallbackNamePath(fn)
	if !ok {
		return "", false
	}

	switch {
	case strings.Contains(name, "xml") || strings.Contains(path, "xml"):
		return reflectedXMLTag, true
	case strings.Contains(name, "yaml") || strings.Contains(path, "yaml"):
		return reflectedYAMLTag, true
	default:
		return reflectedJSONTag, true
	}
}

func genericDecodeExplicitFallbackTag(fn *types.Func) (string, bool) {
	name, path, ok := genericDecodeFallbackNamePath(fn)
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

func genericDecodeFallbackNamePath(fn *types.Func) (string, string, bool) {
	if fn == nil {
		return "", "", false
	}

	name := strings.ToLower(fn.Name())
	if !strings.Contains(name, "decode") && !strings.Contains(name, "unmarshal") {
		return "", "", false
	}

	path := ""
	if fn.Pkg() != nil {
		path = strings.ToLower(fn.Pkg().Path())
	}

	return name, path, true
}

func (graph deadCodeGraph) reflectedDecodeTypeParamDecodes(
	fn *types.Func,
) ([]reflectedTypeParamDecode, bool) {
	return graph.reflectedDecodeTypeParamDecodesSeen(fn, make(map[string]struct{}))
}

func (graph deadCodeGraph) reflectedDecodeTypeParamDecodesSeen(
	fn *types.Func,
	funcsSeen map[string]struct{},
) ([]reflectedTypeParamDecode, bool) {
	pkg := graph.packageForFunc(fn)
	if pkg == nil {
		return nil, false
	}

	decl := genericFuncDecl(pkg, fn)
	if decl == nil || decl.Body == nil {
		return nil, false
	}

	typeParamIndexes := genericTypeParamIndexes(genericFuncObject(pkg, decl))
	if len(typeParamIndexes) == 0 {
		return nil, true
	}

	key := deadCodeObjectKey(fn)
	if key != "" {
		if _, ok := funcsSeen[key]; ok {
			return nil, true
		}

		funcsSeen[key] = struct{}{}
		defer delete(funcsSeen, key)
	}

	out := make([]reflectedTypeParamDecode, 0)

	ast.Inspect(decl.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		graph.collectReflectedDecodeCallTypeParamDecodes(
			pkg,
			call,
			typeParamIndexes,
			funcsSeen,
			&out,
		)

		return true
	})

	return dedupeReflectedTypeParamDecodes(out), true
}

func (graph deadCodeGraph) collectReflectedDecodeCallTypeParamDecodes(
	pkg *Package,
	call *ast.CallExpr,
	typeParamIndexes map[*types.TypeParam]int,
	funcsSeen map[string]struct{},
	out *[]reflectedTypeParamDecode,
) {
	if tag, ok := reflectedDecodeTargetCall(pkg, call); ok {
		target := call.Args[len(call.Args)-1]
		collectReflectedDecodeTargetTypeParamDecodes(
			pkg.TypesInfo.TypeOf(target),
			tag,
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
}

func (graph deadCodeGraph) collectDelegatedReflectedTypeParamDecodes(
	pkg *Package,
	call *ast.CallExpr,
	typeParamIndexes map[*types.TypeParam]int,
	funcsSeen map[string]struct{},
	out *[]reflectedTypeParamDecode,
) {
	typeArgs := reflectedGenericCallTypeArgs(pkg.TypesInfo, call)
	if typeArgs == nil || typeArgs.Len() == 0 {
		return
	}

	callee := calledFunc(pkg.TypesInfo, call)

	decodes, inspected := graph.reflectedDecodeTypeParamDecodesSeen(callee, funcsSeen)
	if !inspected {
		decodes = fallbackGenericDecodeTypeParamDecodes(callee, typeArgs.Len())
	}

	for _, decode := range decodes {
		if decode.index >= typeArgs.Len() {
			continue
		}

		typ := typeArgs.At(decode.index)
		if decode.pointerOnly {
			collectReflectedDecodeTargetTypeParamDecodes(
				typ,
				decode.tag,
				typeParamIndexes,
				out,
			)

			continue
		}

		collectReflectedSettableTypeParamDecodes(
			typ,
			decode.tag,
			typeParamIndexes,
			out,
		)
	}
}

func dedupeReflectedTypeParamDecodes(
	decodes []reflectedTypeParamDecode,
) []reflectedTypeParamDecode {
	if len(decodes) < reflectedDedupeMinLen {
		return decodes
	}

	seen := make(map[reflectedTypeParamDecode]struct{}, len(decodes))

	out := make([]reflectedTypeParamDecode, 0, len(decodes))
	for _, decode := range decodes {
		if _, ok := seen[decode]; ok {
			continue
		}

		seen[decode] = struct{}{}
		out = append(out, decode)
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

func reflectedDecodeTargetCall(pkg *Package, call *ast.CallExpr) (string, bool) {
	if len(call.Args) == 0 {
		return "", false
	}

	return reflectedDecodeFuncTag(calledFunc(pkg.TypesInfo, call))
}

func (graph deadCodeGraph) packageForFunc(fn *types.Func) *Package {
	if fn == nil || fn.Pkg() == nil {
		return nil
	}

	return graph.packages[fn.Pkg().Path()]
}

func genericFuncDecl(pkg *Package, fn *types.Func) *ast.FuncDecl {
	targetKey := deadCodeObjectKey(fn)

	for _, decl := range pkg.ProductionFuncs {
		if decl == nil || decl.Name == nil {
			continue
		}

		obj, _ := pkg.TypesInfo.Defs[decl.Name].(*types.Func)
		if obj != nil && deadCodeObjectKey(obj) == targetKey {
			return decl
		}
	}

	return nil
}

func genericTypeParamIndexes(fn *types.Func) map[*types.TypeParam]int {
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

func collectReflectedDecodeTargetTypeParamDecodes(
	typ types.Type,
	tag string,
	typeParamIndexes map[*types.TypeParam]int,
	out *[]reflectedTypeParamDecode,
) {
	collectReflectedTypeParamDecodes(
		typ,
		tag,
		typeParamIndexes,
		out,
		make(map[string]struct{}),
		reflectedDecodeTargetContext,
	)
}

func collectReflectedSettableTypeParamDecodes(
	typ types.Type,
	tag string,
	typeParamIndexes map[*types.TypeParam]int,
	out *[]reflectedTypeParamDecode,
) {
	collectReflectedTypeParamDecodes(
		typ,
		tag,
		typeParamIndexes,
		out,
		make(map[string]struct{}),
		reflectedSettableDecodeContext,
	)
}

func collectReflectedTypeParamDecodes(
	typ types.Type,
	tag string,
	typeParamIndexes map[*types.TypeParam]int,
	out *[]reflectedTypeParamDecode,
	seen map[string]struct{},
	context reflectedTypeParamDecodeContext,
) {
	if typ == nil {
		return
	}

	typ = types.Unalias(typ)
	if collectReflectedDirectTypeParam(
		typ,
		tag,
		typeParamIndexes,
		out,
		context,
	) {
		return
	}

	collectReflectedCompositeTypeParamDecodes(
		typ,
		tag,
		typeParamIndexes,
		out,
		seen,
		context,
	)
}

func collectReflectedDirectTypeParam(
	typ types.Type,
	tag string,
	typeParamIndexes map[*types.TypeParam]int,
	out *[]reflectedTypeParamDecode,
	context reflectedTypeParamDecodeContext,
) bool {
	typeParam, ok := typ.(*types.TypeParam)
	if !ok {
		return false
	}

	if index, ok := typeParamIndexes[typeParam]; ok {
		*out = append(*out, reflectedTypeParamDecode{
			index:       index,
			tag:         tag,
			pointerOnly: context == reflectedDecodeTargetContext,
		})
	}

	return true
}

func collectReflectedCompositeTypeParamDecodes(
	typ types.Type,
	tag string,
	typeParamIndexes map[*types.TypeParam]int,
	out *[]reflectedTypeParamDecode,
	seen map[string]struct{},
	context reflectedTypeParamDecodeContext,
) {
	switch typ := typ.(type) {
	case *types.Pointer:
		collectReflectedTypeParamDecodes(
			typ.Elem(),
			tag,
			typeParamIndexes,
			out,
			seen,
			reflectedSettableDecodeContext,
		)
	case *types.Slice:
		if context == reflectedSettableDecodeContext {
			collectReflectedTypeParamDecodes(
				typ.Elem(),
				tag,
				typeParamIndexes,
				out,
				seen,
				reflectedSettableDecodeContext,
			)
		}
	case *types.Array:
		if context == reflectedSettableDecodeContext {
			collectReflectedTypeParamDecodes(
				typ.Elem(),
				tag,
				typeParamIndexes,
				out,
				seen,
				reflectedSettableDecodeContext,
			)
		}
	case *types.Map:
		if context == reflectedSettableDecodeContext {
			collectReflectedTypeParamDecodes(
				typ.Elem(),
				tag,
				typeParamIndexes,
				out,
				seen,
				reflectedSettableDecodeContext,
			)
		}
	case *types.Named:
		if context == reflectedSettableDecodeContext {
			collectReflectedNamedTypeParamDecodes(typ, tag, typeParamIndexes, out, seen)
		}
	case *types.Struct:
		if context == reflectedSettableDecodeContext {
			collectReflectedStructTypeParamDecodes(typ, tag, typeParamIndexes, out, seen)
		}
	}
}

func collectReflectedNamedTypeParamDecodes(
	typ *types.Named,
	tag string,
	typeParamIndexes map[*types.TypeParam]int,
	out *[]reflectedTypeParamDecode,
	seen map[string]struct{},
) {
	key := typ.String()
	if _, ok := seen[key]; ok {
		return
	}

	seen[key] = struct{}{}
	for index := range typ.TypeArgs().Len() {
		collectReflectedTypeParamDecodes(
			typ.TypeArgs().At(index),
			tag,
			typeParamIndexes,
			out,
			seen,
			reflectedSettableDecodeContext,
		)
	}

	collectReflectedTypeParamDecodes(
		typ.Underlying(),
		tag,
		typeParamIndexes,
		out,
		seen,
		reflectedSettableDecodeContext,
	)
	delete(seen, key)
}

func collectReflectedStructTypeParamDecodes(
	typ *types.Struct,
	tag string,
	typeParamIndexes map[*types.TypeParam]int,
	out *[]reflectedTypeParamDecode,
	seen map[string]struct{},
) {
	for index := range typ.NumFields() {
		collectReflectedTypeParamDecodes(
			typ.Field(index).Type(),
			tag,
			typeParamIndexes,
			out,
			seen,
			reflectedSettableDecodeContext,
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

func (l *packageLinter) addReflectedStructFieldUses(
	out map[string]struct{},
	typ types.Type,
	tag string,
	call *ast.CallExpr,
	seen map[string]struct{},
) {
	if elem, ok := reflectedContainerElem(typ); ok {
		l.addReflectedStructFieldUses(out, elem, tag, call, seen)

		return
	}

	named, structType := reflectedNamedStructType(typ)
	if structType == nil {
		return
	}

	if reflectedTypeHasCustomUnmarshalHook(typ, tag) {
		return
	}

	seenKey, ok := enterReflectedNamedType(seen, named)
	if !ok {
		return
	}

	l.addReflectedStructFields(out, named, structType, tag, call, seen)

	if seenKey != "" {
		delete(seen, seenKey)
	}
}

func reflectedContainerElem(typ types.Type) (types.Type, bool) {
	typ = types.Unalias(typ)

	switch typ := typ.(type) {
	case *types.Pointer:
		return typ.Elem(), true
	case *types.Slice:
		return typ.Elem(), true
	case *types.Array:
		return typ.Elem(), true
	case *types.Map:
		return typ.Elem(), true
	default:
		return nil, false
	}
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

func (l *packageLinter) addReflectedStructFields(
	out map[string]struct{},
	named *types.Named,
	structType *types.Struct,
	tag string,
	call *ast.CallExpr,
	seen map[string]struct{},
) {
	for index := range structType.NumFields() {
		field := structType.Field(index)
		if field == nil || reflectedIgnoredStructField(structType.Tag(index), tag) {
			continue
		}

		if field.Exported() && named != nil {
			for _, owner := range l.reflectedStructFieldOwners(named, tag, call) {
				addStructFieldUse(out, owner, field.Name())
			}
		}

		if field.Exported() || field.Anonymous() {
			l.addReflectedStructFieldUses(out, field.Type(), tag, call, seen)
		}
	}
}

func (l *packageLinter) reflectedStructFieldOwners(
	named *types.Named,
	tag string,
	call *ast.CallExpr,
) []*types.Named {
	if named == nil {
		return nil
	}

	out := []*types.Named{named}
	if target := l.reflectedAliasTargetNamed(named, tag, call); target != nil && target != named {
		out = append(out, target)
	}

	return out
}

func (l *packageLinter) reflectedAliasTargetNamed(
	named *types.Named,
	tag string,
	call *ast.CallExpr,
) *types.Named {
	if l == nil || l.pkg == nil || named == nil || named.Obj() == nil || call == nil {
		return nil
	}

	methodName := reflectedUnmarshalHookName(tag)
	if methodName == "" {
		return nil
	}

	fn := l.reflectedAliasContextFunc(methodName, call)

	receiver := l.reflectedCustomUnmarshalReceiver(fn)
	if receiver == nil {
		return nil
	}

	target := l.reflectedAliasTargetNamedFromNode(named, fn.Body)
	if sameNamedType(receiver, target) {
		return target
	}

	return nil
}

func (l *packageLinter) reflectedAliasContextFunc(
	methodName string,
	call *ast.CallExpr,
) *ast.FuncDecl {
	if methodName == "" || call == nil {
		return nil
	}

	for _, decl := range l.pkg.ProductionDecls {
		fn, _ := decl.(*ast.FuncDecl)
		if reflectedFuncDeclContainsCall(fn, methodName, call) {
			return fn
		}
	}

	return nil
}

func reflectedFuncDeclContainsCall(
	fn *ast.FuncDecl,
	methodName string,
	call *ast.CallExpr,
) bool {
	return fn != nil &&
		fn.Name != nil &&
		fn.Name.Name == methodName &&
		nodeContainsPos(fn.Body, call.Pos())
}

func (l *packageLinter) reflectedCustomUnmarshalReceiver(fn *ast.FuncDecl) *types.Named {
	if fn == nil || fn.Recv == nil || len(fn.Recv.List) == 0 {
		return nil
	}

	return namedDeadCodeType(l.pkg.TypesInfo.TypeOf(fn.Recv.List[0].Type))
}

func sameNamedType(left *types.Named, right *types.Named) bool {
	if left == nil || right == nil || left.Obj() == nil || right.Obj() == nil {
		return false
	}

	return deadCodeObjectKey(left.Obj()) == deadCodeObjectKey(right.Obj())
}

func nodeContainsPos(node ast.Node, pos token.Pos) bool {
	return node != nil && pos >= node.Pos() && pos <= node.End()
}

func (l *packageLinter) reflectedAliasTargetNamedFromNode(
	named *types.Named,
	node ast.Node,
) *types.Named {
	var target *types.Named

	ast.Inspect(node, func(n ast.Node) bool {
		typeSpec, ok := n.(*ast.TypeSpec)
		if !ok {
			return true
		}

		target = l.reflectedAliasTargetNamedFromSpec(named, typeSpec)

		return target == nil
	})

	return target
}

func (l *packageLinter) reflectedAliasTargetNamedFromSpec(
	named *types.Named,
	typeSpec *ast.TypeSpec,
) *types.Named {
	if typeSpec == nil {
		return nil
	}

	obj, _ := l.pkg.TypesInfo.Defs[typeSpec.Name].(*types.TypeName)
	if obj == nil || obj.Type() != named {
		return nil
	}

	target := namedDeadCodeType(l.pkg.TypesInfo.TypeOf(typeSpec.Type))
	if target != nil && deadCodeStructType(target) != nil {
		return target
	}

	return nil
}

func reflectedIgnoredStructField(structTag string, tag string) bool {
	if tag == "" {
		return false
	}

	return reflect.StructTag(structTag).Get(tag) == "-"
}

func reflectedTypeHasCustomUnmarshalHook(typ types.Type, tag string) bool {
	name := reflectedUnmarshalHookName(tag)
	if name == "" {
		return false
	}

	named := namedDeadCodeType(typ)
	if named == nil {
		return false
	}

	if reflectedTypeHasMethod(named, name) {
		return true
	}

	return reflectedTypeHasMethod(types.NewPointer(named), name)
}

func reflectedUnmarshalHookName(tag string) string {
	switch tag {
	case reflectedJSONTag:
		return "UnmarshalJSON"
	case reflectedXMLTag:
		return "UnmarshalXML"
	case reflectedYAMLTag:
		return "UnmarshalYAML"
	default:
		return ""
	}
}

func reflectedTypeHasMethod(typ types.Type, name string) bool {
	obj, _, _ := types.LookupFieldOrMethod(typ, true, nil, name)
	_, ok := obj.(*types.Func)

	return ok
}
