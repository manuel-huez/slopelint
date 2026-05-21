package deadcode

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strconv"
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
		decodes = fallbackGenericDecodeTypeParamDecodes(fn, typeArgs.Len())
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
			tag:         marshal.tag,
			addressable: reflectedMarshalUnaddressable,
		})
	}

	return out
}

func fallbackGenericDecodeTypeParamDecodes(
	fn *types.Func,
	count int,
) []reflectedTypeParamUse {
	tag, ok := genericDecodeFallbackTag(fn)
	if !ok {
		return nil
	}

	sig, ok := genericFallbackSignature(fn)
	if !ok || !genericDecodeHasEncodedInput(sig) {
		return nil
	}

	out := make([]reflectedTypeParamUse, 0, count)
	indexes := genericTypeParamIndexes(fn)

	collectFallbackGenericDecodeParamDecodes(sig, tag, indexes, &out)

	if resultTag, ok := genericDecodeExplicitFallbackTag(fn); ok {
		collectFallbackGenericDecodeResultDecodes(sig, resultTag, indexes, &out)
	}

	out = dedupeReflectedTypeParamUses(out)
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
	sig, ok := genericFallbackSignature(fn)
	if !ok || fset == nil {
		return nil
	}

	filename := fset.Position(fn.Pos()).Filename
	if filename == "" {
		return nil
	}

	fileFSet := token.NewFileSet()

	file, err := parser.ParseFile(fileFSet, filename, nil, 0)
	if err != nil {
		return nil
	}

	decl := sourceFuncDecl(file, fileFSet, fn, fset)
	if decl == nil || decl.Body == nil {
		return nil
	}

	importTags := sourceMarshalImportTags(file)
	paramTypes := sourceFuncParamTypes(sig, decl)
	indexes := genericTypeParamIndexes(fn)

	out := make([]reflectedMarshalTypeParamUse, 0)

	ast.Inspect(decl.Body, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}

		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		tag, ok := sourceMarshalCallTag(importTags, call)
		if !ok || len(call.Args) == 0 {
			return true
		}

		if ident, ok := unparenReflectedExpr(call.Args[0]).(*ast.Ident); ok {
			addReflectedMarshalTypeParamUse(paramTypes[ident.Name], tag, indexes, &out)
		}

		return true
	})

	return out
}

func sourceFuncDecl(
	file *ast.File,
	fileFSet *token.FileSet,
	fn *types.Func,
	fset *token.FileSet,
) *ast.FuncDecl {
	if fn == nil || fileFSet == nil || fset == nil {
		return nil
	}

	target := fset.Position(fn.Pos())
	targetReceiver := sourceFuncReceiverName(fn)
	candidates := make([]*ast.FuncDecl, 0, 1)

	for _, decl := range file.Decls {
		decl, ok := decl.(*ast.FuncDecl)
		if !ok || !sourceFuncDeclCandidate(decl, fn.Name(), targetReceiver) {
			continue
		}

		candidates = append(candidates, decl)

		pos := fileFSet.Position(decl.Name.Pos())
		if target.Line != 0 && pos.Line == target.Line && pos.Column == target.Column {
			return decl
		}
	}

	if len(candidates) == 1 {
		return candidates[0]
	}

	return nil
}

func sourceFuncDeclCandidate(
	decl *ast.FuncDecl,
	name string,
	receiverName string,
) bool {
	return decl != nil &&
		decl.Name != nil &&
		decl.Name.Name == name &&
		sourceFuncDeclReceiverName(decl) == receiverName
}

func sourceFuncReceiverName(fn *types.Func) string {
	sig, _ := fn.Type().(*types.Signature)
	if sig == nil || sig.Recv() == nil {
		return ""
	}

	named := namedDeadCodeType(sig.Recv().Type())
	if named == nil || named.Obj() == nil {
		return ""
	}

	return named.Obj().Name()
}

func sourceFuncDeclReceiverName(decl *ast.FuncDecl) string {
	if decl == nil || decl.Recv == nil || len(decl.Recv.List) == 0 {
		return ""
	}

	return sourceTypeName(decl.Recv.List[0].Type)
}

func sourceTypeName(expr ast.Expr) string {
	switch expr := unparenReflectedExpr(expr).(type) {
	case *ast.Ident:
		return expr.Name
	case *ast.StarExpr:
		return sourceTypeName(expr.X)
	case *ast.IndexExpr:
		return sourceTypeName(expr.X)
	case *ast.IndexListExpr:
		return sourceTypeName(expr.X)
	case *ast.SelectorExpr:
		return expr.Sel.Name
	default:
		return ""
	}
}

func sourceMarshalImportTags(file *ast.File) map[string]string {
	out := make(map[string]string)

	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}

		tag, ok := sourceMarshalImportTag(path)
		if !ok {
			continue
		}

		name := sourceImportName(spec, path)
		if name != "" {
			out[name] = tag
		}
	}

	return out
}

func sourceMarshalImportTag(path string) (string, bool) {
	if codec, ok := reflectedPackageCodecs[path]; ok && codec.marshal {
		return codec.tag, true
	}

	return "", false
}

func sourceImportName(spec *ast.ImportSpec, path string) string {
	if spec.Name != nil && spec.Name.Name != "." && spec.Name.Name != "_" {
		return spec.Name.Name
	}

	switch {
	case path == "encoding/json":
		return "json"
	case path == "encoding/xml":
		return "xml"
	case strings.Contains(path, "yaml"):
		return "yaml"
	default:
		return genericFallbackPathBase(path)
	}
}

func sourceFuncParamTypes(
	sig *types.Signature,
	decl *ast.FuncDecl,
) map[string]types.Type {
	out := make(map[string]types.Type)
	if decl.Type == nil || decl.Type.Params == nil {
		return out
	}

	index := 0

	for _, field := range decl.Type.Params.List {
		for _, name := range field.Names {
			if index >= tupleLen(sig.Params()) {
				return out
			}

			out[name.Name] = sig.Params().At(index).Type()
			index++
		}

		if len(field.Names) == 0 {
			index++
		}
	}

	return out
}

func sourceMarshalCallTag(
	importTags map[string]string,
	call *ast.CallExpr,
) (string, bool) {
	selector, ok := unparenReflectedExpr(call.Fun).(*ast.SelectorExpr)
	if !ok || !sourceMarshalFuncName(selector.Sel.Name) {
		return "", false
	}

	ident, ok := selector.X.(*ast.Ident)
	if !ok {
		return "", false
	}

	tag, ok := importTags[ident.Name]

	return tag, ok
}

func sourceMarshalFuncName(name string) bool {
	switch name {
	case "Marshal", "MarshalIndent":
		return true
	default:
		return false
	}
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
		return typ.String() == "io.Reader"
	}
}

func collectFallbackGenericDecodeParamDecodes(
	sig *types.Signature,
	tag string,
	indexes map[*types.TypeParam]int,
	out *[]reflectedTypeParamUse,
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
	out *[]reflectedTypeParamUse,
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
	out *[]reflectedTypeParamUse,
) {
	for index := range tupleLen(sig.Results()) {
		collectReflectedSettableTypeParamDecodes(sig.Results().At(index).Type(), tag, indexes, out)
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
			tag:    decode.tag,
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

	if !genericCodecFallbackEvidence(name, path) {
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

func genericCodecFallbackEvidence(name string, path string) bool {
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
		) {
			graph.collectReflectedDecodeCallTypeParamDecodes(
				pkg,
				call,
				typeParamIndexes,
				funcsSeen,
				&out,
			)
		},
	)
	if !inspected {
		return nil, false
	}

	return dedupeReflectedTypeParamUses(out), true
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
		) {
			graph.collectReflectedMarshalCallTypeParamUses(
				pkg,
				call,
				typeParamIndexes,
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
	visit func(*Package, *ast.CallExpr, map[*types.TypeParam]int),
) bool {
	pkg := graph.packageForFunc(fn)
	if pkg == nil {
		return false
	}

	decl := genericFuncDecl(pkg, fn)
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

	ast.Inspect(decl.Body, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}

		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		visit(pkg, call, typeParamIndexes)

		return true
	})

	return true
}

func (graph deadCodeGraph) collectReflectedDecodeCallTypeParamDecodes(
	pkg *Package,
	call *ast.CallExpr,
	typeParamIndexes map[*types.TypeParam]int,
	funcsSeen map[string]struct{},
	out *[]reflectedTypeParamUse,
) {
	if fn, tag, ok := reflectedDecodeTargetCall(pkg, call); ok {
		target := call.Args[reflectedDecodeTargetArgIndex(fn, call)]
		collectReflectedDecodeTargetTypeParamDecodes(
			reflectedValueType(pkg.TypesInfo, target),
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
	out *[]reflectedTypeParamUse,
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
		if decode.mapKey {
			collectReflectedMapKeyTypeParamDecodes(
				typ,
				decode.tag,
				typeParamIndexes,
				out,
			)

			continue
		}

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

func (graph deadCodeGraph) collectReflectedMarshalCallTypeParamUses(
	pkg *Package,
	call *ast.CallExpr,
	typeParamIndexes map[*types.TypeParam]int,
	funcsSeen map[string]struct{},
	out *[]reflectedMarshalTypeParamUse,
) {
	if tag, ok := reflectedMarshalTargetCall(pkg, call); ok {
		addReflectedMarshalTypeParamUse(
			reflectedValueType(pkg.TypesInfo, call.Args[0]),
			tag,
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
			marshal.tag,
			typeParamIndexes,
			out,
		)
	}
}

func dedupeReflectedTypeParamUses(
	decodes []reflectedTypeParamUse,
) []reflectedTypeParamUse {
	if len(decodes) < reflectedDedupeMinLen {
		return decodes
	}

	seen := make(map[reflectedTypeParamUse]struct{}, len(decodes))

	out := make([]reflectedTypeParamUse, 0, len(decodes))
	for _, decode := range decodes {
		if _, ok := seen[decode]; ok {
			continue
		}

		seen[decode] = struct{}{}
		out = append(out, decode)
	}

	return out
}

func addReflectedMarshalTypeParamUse(
	typ types.Type,
	tag string,
	typeParamIndexes map[*types.TypeParam]int,
	out *[]reflectedMarshalTypeParamUse,
) {
	if !reflectedTypeContainsParams(typ, typeParamIndexes) {
		return
	}

	*out = append(*out, reflectedMarshalTypeParamUse{
		typ: typ,
		tag: tag,
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
		key := use.tag + "\x00" + use.typ.String()
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

func reflectedDecodeTargetCall(pkg *Package, call *ast.CallExpr) (*types.Func, string, bool) {
	if len(call.Args) == 0 {
		return nil, "", false
	}

	fn := calledFunc(pkg.TypesInfo, call)
	tag, ok := reflectedDecodeFuncTag(fn)

	return fn, tag, ok
}

func reflectedMarshalTargetCall(pkg *Package, call *ast.CallExpr) (string, bool) {
	if len(call.Args) == 0 {
		return "", false
	}

	fn := calledFunc(pkg.TypesInfo, call)
	tag, ok := reflectedMarshalFuncTag(fn)

	return tag, ok
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
	key := typ.String()
	if _, ok := seen[key]; ok {
		return typ
	}

	seen[key] = struct{}{}
	defer delete(seen, key)

	if typ.TypeArgs().Len() == 0 {
		return substituteReflectedNamedUnderlyingTypeParams(typ, replacements, seen)
	}

	typeArgs := make([]types.Type, 0, typ.TypeArgs().Len())
	for index := range typ.TypeArgs().Len() {
		typeArgs = append(
			typeArgs,
			substituteReflectedTypeParamsSeen(typ.TypeArgs().At(index), replacements, seen),
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

	key := typ.String()
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
	for index := range typ.TypeArgs().Len() {
		if reflectedTypeContainsMatchingParam(typ.TypeArgs().At(index), matches, seen) {
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
	for index := range typ.NumFields() {
		if reflectedTypeContainsMatchingParam(typ.Field(index).Type(), matches, seen) {
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

func collectReflectedDecodeTargetTypeParamDecodes(
	typ types.Type,
	tag string,
	typeParamIndexes map[*types.TypeParam]int,
	out *[]reflectedTypeParamUse,
) {
	collectReflectedTypeParamUses(
		typ,
		tag,
		typeParamIndexes,
		out,
		make(map[string]struct{}),
		reflectedDecodeTargetContext,
	)
}

func collectReflectedMapKeyTypeParamDecodes(
	typ types.Type,
	tag string,
	typeParamIndexes map[*types.TypeParam]int,
	out *[]reflectedTypeParamUse,
) {
	collectReflectedTypeParamUses(
		typ,
		tag,
		typeParamIndexes,
		out,
		make(map[string]struct{}),
		reflectedTextDecodeContext,
	)
}

func collectReflectedSettableTypeParamDecodes(
	typ types.Type,
	tag string,
	typeParamIndexes map[*types.TypeParam]int,
	out *[]reflectedTypeParamUse,
) {
	collectReflectedTypeParamUses(
		typ,
		tag,
		typeParamIndexes,
		out,
		make(map[string]struct{}),
		reflectedSettableDecodeContext,
	)
}

func collectReflectedTypeParamUses(
	typ types.Type,
	tag string,
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
		tag,
		typeParamIndexes,
		out,
		context,
	) {
		return
	}

	collectReflectedCompositeTypeParamUses(
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
			tag:         tag,
			pointerOnly: context == reflectedDecodeTargetContext,
			mapKey:      context == reflectedTextDecodeContext,
		})
	}

	return true
}

func collectReflectedCompositeTypeParamUses(
	typ types.Type,
	tag string,
	typeParamIndexes map[*types.TypeParam]int,
	out *[]reflectedTypeParamUse,
	seen map[string]struct{},
	context reflectedTypeParamUseContext,
) {
	switch typ := typ.(type) {
	case *types.Pointer:
		collectReflectedPointerTypeParamUses(typ, tag, typeParamIndexes, out, seen, context)
	case *types.Slice:
		collectReflectedContainerElemTypeParamUses(
			typ.Elem(),
			tag,
			typeParamIndexes,
			out,
			seen,
			context,
		)
	case *types.Array:
		collectReflectedContainerElemTypeParamUses(
			typ.Elem(),
			tag,
			typeParamIndexes,
			out,
			seen,
			context,
		)
	case *types.Map:
		collectReflectedMapTypeParamUses(typ, tag, typeParamIndexes, out, seen, context)
	case *types.Named:
		collectReflectedNamedCompositeTypeParamUses(typ, tag, typeParamIndexes, out, seen, context)
	case *types.Struct:
		collectReflectedStructCompositeTypeParamUses(typ, tag, typeParamIndexes, out, seen, context)
	}
}

func collectReflectedPointerTypeParamUses(
	typ *types.Pointer,
	tag string,
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
		tag,
		typeParamIndexes,
		out,
		seen,
		nextContext,
	)
}

func collectReflectedContainerElemTypeParamUses(
	typ types.Type,
	tag string,
	typeParamIndexes map[*types.TypeParam]int,
	out *[]reflectedTypeParamUse,
	seen map[string]struct{},
	context reflectedTypeParamUseContext,
) {
	if context == reflectedDecodeTargetContext {
		return
	}

	collectReflectedTypeParamUses(typ, tag, typeParamIndexes, out, seen, context)
}

func collectReflectedMapTypeParamUses(
	typ *types.Map,
	tag string,
	typeParamIndexes map[*types.TypeParam]int,
	out *[]reflectedTypeParamUse,
	seen map[string]struct{},
	context reflectedTypeParamUseContext,
) {
	if context != reflectedDecodeTargetContext {
		collectReflectedTypeParamUses(
			typ.Key(),
			tag,
			typeParamIndexes,
			out,
			seen,
			reflectedTextDecodeContext,
		)
	}

	collectReflectedContainerElemTypeParamUses(
		typ.Elem(),
		tag,
		typeParamIndexes,
		out,
		seen,
		context,
	)
}

func collectReflectedNamedCompositeTypeParamUses(
	typ *types.Named,
	tag string,
	typeParamIndexes map[*types.TypeParam]int,
	out *[]reflectedTypeParamUse,
	seen map[string]struct{},
	context reflectedTypeParamUseContext,
) {
	if context == reflectedDecodeTargetContext {
		return
	}

	collectReflectedNamedTypeParamUses(typ, tag, typeParamIndexes, out, seen, context)
}

func collectReflectedStructCompositeTypeParamUses(
	typ *types.Struct,
	tag string,
	typeParamIndexes map[*types.TypeParam]int,
	out *[]reflectedTypeParamUse,
	seen map[string]struct{},
	context reflectedTypeParamUseContext,
) {
	if context == reflectedDecodeTargetContext {
		return
	}

	collectReflectedStructTypeParamUses(typ, tag, typeParamIndexes, out, seen, context)
}

func collectReflectedNamedTypeParamUses(
	typ *types.Named,
	tag string,
	typeParamIndexes map[*types.TypeParam]int,
	out *[]reflectedTypeParamUse,
	seen map[string]struct{},
	context reflectedTypeParamUseContext,
) {
	key := typ.String()
	if _, ok := seen[key]; ok {
		return
	}

	seen[key] = struct{}{}
	for index := range typ.TypeArgs().Len() {
		collectReflectedTypeParamUses(
			typ.TypeArgs().At(index),
			tag,
			typeParamIndexes,
			out,
			seen,
			context,
		)
	}

	collectReflectedTypeParamUses(
		typ.Underlying(),
		tag,
		typeParamIndexes,
		out,
		seen,
		context,
	)
	delete(seen, key)
}

func collectReflectedStructTypeParamUses(
	typ *types.Struct,
	tag string,
	typeParamIndexes map[*types.TypeParam]int,
	out *[]reflectedTypeParamUse,
	seen map[string]struct{},
	context reflectedTypeParamUseContext,
) {
	for index := range typ.NumFields() {
		collectReflectedTypeParamUses(
			typ.Field(index).Type(),
			tag,
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
