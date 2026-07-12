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

func sourceGenericDecodeTypeParamDecodes(
	fn *types.Func,
	fset *token.FileSet,
) ([]reflectedTypeParamUse, bool) {
	scan, ok := sourceGenericCodecScanFor(
		fn,
		fset,
		func(codec reflectedPackageCodec) map[string]reflectedCodecFunc {
			return codec.decodeFuncs
		},
	)
	if !ok {
		return nil, false
	}

	out := make([]reflectedTypeParamUse, 0)

	inspectReflectedCalls(scan.decl.Body, func(call *ast.CallExpr) {
		codec, arg, ok := scan.callArg(call)
		if !ok {
			return
		}

		typ := sourceDecodeTargetType(scan.sig, scan.paramTypes, scan.decl.Body, arg)
		collectReflectedDecodeTargetTypeParamDecodes(typ, codec, scan.indexes, &out)
	})

	return out, true
}

type sourceGenericCodecScan struct {
	sig          *types.Signature
	decl         *ast.FuncDecl
	importCodecs map[string]sourceCodecImport
	paramTypes   map[string]types.Type
	indexes      map[*types.TypeParam]int
}

func sourceGenericCodecScanFor(
	fn *types.Func,
	fset *token.FileSet,
	funcs func(reflectedPackageCodec) map[string]reflectedCodecFunc,
) (sourceGenericCodecScan, bool) {
	sig, ok := genericFallbackSignature(fn)
	if !ok {
		return sourceGenericCodecScan{}, false
	}

	file, decl, ok := sourceFuncFileDecl(fn, fset)
	if !ok {
		return sourceGenericCodecScan{}, false
	}

	return sourceGenericCodecScan{
		sig:          sig,
		decl:         decl,
		importCodecs: sourceCodecImportCodecs(file, funcs),
		paramTypes:   sourceFuncParamTypes(sig, decl),
		indexes:      genericTypeParamIndexes(fn),
	}, true
}

func (scan sourceGenericCodecScan) callArg(
	call *ast.CallExpr,
) (reflectedCodecUse, ast.Expr, bool) {
	codec, argIndex, ok := sourceCodecCall(scan.importCodecs, call)
	if !ok || argIndex < 0 || argIndex >= len(call.Args) {
		return reflectedCodecUse{}, nil, false
	}

	return codec, call.Args[argIndex], true
}

func sourceFuncFileDecl(
	fn *types.Func,
	fset *token.FileSet,
) (*ast.File, *ast.FuncDecl, bool) {
	if fset == nil {
		return nil, nil, false
	}

	filename := fset.Position(fn.Pos()).Filename
	if filename == "" {
		return nil, nil, false
	}

	fileFSet := token.NewFileSet()

	file, err := parser.ParseFile(fileFSet, filename, nil, 0)
	if err != nil {
		return nil, nil, false
	}

	decl := sourceFuncDecl(file, fileFSet, fn, fset)
	if decl == nil || decl.Body == nil {
		return nil, nil, false
	}

	return file, decl, true
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
	named := deadCodeFuncReceiver(fn)
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

type sourceCodecImport struct {
	use   reflectedCodecUse
	funcs map[string]reflectedCodecFunc
}

func sourceCodecImportCodecs(
	file *ast.File,
	funcs func(reflectedPackageCodec) map[string]reflectedCodecFunc,
) map[string]sourceCodecImport {
	out := make(map[string]sourceCodecImport)

	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}

		codec, ok := sourceCodecImportForPath(path, funcs)
		if !ok {
			continue
		}

		name := sourceImportName(spec, path)
		if name != "" {
			out[name] = codec
		}
	}

	return out
}

func sourceCodecImportForPath(
	path string,
	funcs func(reflectedPackageCodec) map[string]reflectedCodecFunc,
) (sourceCodecImport, bool) {
	codec, ok := reflectedPackageCodecs[path]
	if !ok {
		return sourceCodecImport{}, false
	}

	codecFuncs := funcs(codec)
	if len(codecFuncs) == 0 {
		return sourceCodecImport{}, false
	}

	return sourceCodecImport{
		use:   reflectedCodecUseForTag(codec.tag),
		funcs: codecFuncs,
	}, true
}

func sourceImportName(spec *ast.ImportSpec, path string) string {
	if spec.Name != nil && spec.Name.Name != "." && spec.Name.Name != "_" {
		return spec.Name.Name
	}

	switch {
	case path == "encoding/json", path == reflectedGoccyJSON:
		return "json"
	case path == "encoding/xml":
		return "xml"
	case strings.Contains(path, "yaml"):
		return "yaml"
	default:
		return genericFallbackPathBase(path)
	}
}

func sourceCodecCall(
	importCodecs map[string]sourceCodecImport,
	call *ast.CallExpr,
) (reflectedCodecUse, int, bool) {
	selector, ok := unparenReflectedExpr(call.Fun).(*ast.SelectorExpr)
	if !ok {
		return reflectedCodecUse{}, 0, false
	}

	ident, ok := selector.X.(*ast.Ident)
	if !ok {
		return reflectedCodecUse{}, 0, false
	}

	codec, ok := importCodecs[ident.Name]
	if !ok {
		return reflectedCodecUse{}, 0, false
	}

	codecFunc, ok := codec.funcs[selector.Sel.Name]
	if !ok {
		return reflectedCodecUse{}, 0, false
	}

	index := codecFunc.argIndex
	use := codec.use

	if codecFunc.hookTag != "" {
		use.hookTag = codecFunc.hookTag
	}

	if index == reflectedLastArgIndex {
		index = reflectedLastCallArgIndex(call)
	}

	return use, index, true
}

func sourceDecodeTargetType(
	sig *types.Signature,
	paramTypes map[string]types.Type,
	body *ast.BlockStmt,
	expr ast.Expr,
) types.Type {
	typeParams := sourceTypeParamTypes(sig)

	return sourceDecodeTargetExprType(paramTypes, body, typeParams, expr)
}

func sourceDecodeTargetExprType(
	paramTypes map[string]types.Type,
	body *ast.BlockStmt,
	typeParams map[string]*types.TypeParam,
	expr ast.Expr,
) types.Type {
	switch expr := unparenReflectedExpr(expr).(type) {
	case *ast.Ident:
		return sourceValueTypeAt(paramTypes, body, typeParams, expr)
	case *ast.UnaryExpr:
		if expr.Op != token.AND {
			return nil
		}

		typ := sourceDecodeTargetExprType(paramTypes, body, typeParams, expr.X)
		if typ == nil {
			return nil
		}

		return types.NewPointer(typ)
	case *ast.CallExpr:
		if sourceInterfaceConversionCall(expr) {
			return sourceDecodeTargetExprType(paramTypes, body, typeParams, expr.Args[0])
		}

		return sourceNewCallType(typeParams, expr)
	default:
		return nil
	}
}

func sourceValueTypeAt(
	paramTypes map[string]types.Type,
	body *ast.BlockStmt,
	typeParams map[string]*types.TypeParam,
	ident *ast.Ident,
) types.Type {
	if typ := sourceFuncParamTypeAt(paramTypes, body, ident); typ != nil {
		return typ
	}

	return sourceLocalTypeParamTypeAt(paramTypes, body, typeParams, ident)
}

func sourceInterfaceConversionCall(call *ast.CallExpr) bool {
	if call == nil || len(call.Args) != 1 {
		return false
	}

	switch fun := unparenReflectedExpr(call.Fun).(type) {
	case *ast.Ident:
		return fun.Name == "any"
	case *ast.InterfaceType:
		return fun.Methods == nil || len(fun.Methods.List) == 0
	default:
		return false
	}
}

func sourceNewCallType(
	typeParams map[string]*types.TypeParam,
	call *ast.CallExpr,
) types.Type {
	if call == nil || len(call.Args) != 1 {
		return nil
	}

	ident, ok := unparenReflectedExpr(call.Fun).(*ast.Ident)
	if !ok || ident.Name != "new" {
		return nil
	}

	typ := sourceTypeExpr(call.Args[0], typeParams)
	if typ == nil {
		return nil
	}

	return types.NewPointer(typ)
}

func sourceLocalTypeParamTypeAt(
	paramTypes map[string]types.Type,
	body *ast.BlockStmt,
	typeParams map[string]*types.TypeParam,
	ident *ast.Ident,
) types.Type {
	if ident == nil || body == nil {
		return nil
	}

	var (
		typ    types.Type
		writes int
	)

	ast.Inspect(body, func(n ast.Node) bool {
		if n == nil || n.Pos() >= ident.Pos() {
			return false
		}

		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}

		if !sourceNodeCanDeclareForPos(n, body, ident.Pos()) {
			return false
		}

		next, ok := sourceLocalTypeParamDeclTypeAt(paramTypes, body, typeParams, n, ident)
		if !ok {
			return true
		}

		writes++
		typ = next

		return true
	})

	if writes != 1 {
		return nil
	}

	return typ
}

func sourceNodeCanDeclareForPos(n ast.Node, root *ast.BlockStmt, pos token.Pos) bool {
	if n == root {
		return true
	}

	switch n.(type) {
	case *ast.BlockStmt:
		return nodeContainsPos(n, pos)
	case *ast.IfStmt,
		*ast.ForStmt,
		*ast.RangeStmt,
		*ast.SwitchStmt,
		*ast.TypeSwitchStmt,
		*ast.SelectStmt,
		*ast.CaseClause,
		*ast.CommClause:
		return nodeContainsPos(n, pos)
	default:
		return true
	}
}

func sourceLocalTypeParamDeclTypeAt(
	paramTypes map[string]types.Type,
	body *ast.BlockStmt,
	typeParams map[string]*types.TypeParam,
	n ast.Node,
	ident *ast.Ident,
) (types.Type, bool) {
	switch n := n.(type) {
	case *ast.ValueSpec:
		return sourceValueSpecNameType(paramTypes, body, typeParams, n, ident)
	case *ast.AssignStmt:
		return sourceShortAssignNameType(paramTypes, body, typeParams, n, ident)
	default:
		return nil, false
	}
}

func sourceValueSpecNameType(
	paramTypes map[string]types.Type,
	body *ast.BlockStmt,
	typeParams map[string]*types.TypeParam,
	spec *ast.ValueSpec,
	ident *ast.Ident,
) (types.Type, bool) {
	for index, name := range spec.Names {
		if name.Name != ident.Name {
			continue
		}

		if typ := sourceTypeExpr(spec.Type, typeParams); typ != nil {
			return typ, true
		}

		return sourceDecodeTargetExprType(
			paramTypes,
			body,
			typeParams,
			reflectedExprAt(spec.Values, index),
		), true
	}

	return nil, false
}

func sourceShortAssignNameType(
	paramTypes map[string]types.Type,
	body *ast.BlockStmt,
	typeParams map[string]*types.TypeParam,
	assign *ast.AssignStmt,
	ident *ast.Ident,
) (types.Type, bool) {
	if assign.Tok != token.DEFINE {
		return nil, false
	}

	for index, lhs := range assign.Lhs {
		name, _ := unparenReflectedExpr(lhs).(*ast.Ident)
		if name == nil || name.Name != ident.Name {
			continue
		}

		return sourceDecodeTargetExprType(
			paramTypes,
			body,
			typeParams,
			reflectedExprAt(assign.Rhs, index),
		), true
	}

	return nil, false
}

func sourceTypeParamTypes(sig *types.Signature) map[string]*types.TypeParam {
	out := make(map[string]*types.TypeParam)
	if sig == nil {
		return out
	}

	for index := range typeParamListLen(sig.RecvTypeParams()) {
		param := sig.RecvTypeParams().At(index)
		out[param.Obj().Name()] = param
	}

	for index := range typeParamListLen(sig.TypeParams()) {
		param := sig.TypeParams().At(index)
		out[param.Obj().Name()] = param
	}

	return out
}

func sourceTypeExpr(
	expr ast.Expr,
	typeParams map[string]*types.TypeParam,
) types.Type {
	switch expr := unparenReflectedExpr(expr).(type) {
	case *ast.Ident:
		return typeParams[expr.Name]
	case *ast.StarExpr:
		typ := sourceTypeExpr(expr.X, typeParams)
		if typ == nil {
			return nil
		}

		return types.NewPointer(typ)
	case *ast.ArrayType:
		if expr.Len != nil {
			return nil
		}

		elem := sourceTypeExpr(expr.Elt, typeParams)
		if elem == nil {
			return nil
		}

		return types.NewSlice(elem)
	case *ast.MapType:
		key := sourceTypeExpr(expr.Key, typeParams)

		elem := sourceTypeExpr(expr.Value, typeParams)
		if key == nil || elem == nil {
			return nil
		}

		return types.NewMap(key, elem)
	default:
		return nil
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
		key := use.codec.tag + "\x00" + use.codec.hookTag + "\x00" + use.typ.String()
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

func collectReflectedDecodeTargetTypeParamDecodes(
	typ types.Type,
	codec reflectedCodecUse,
	typeParamIndexes map[*types.TypeParam]int,
	out *[]reflectedTypeParamUse,
) {
	collectReflectedTypeParamUses(
		typ,
		codec,
		typeParamIndexes,
		out,
		make(map[string]struct{}),
		reflectedDecodeTargetContext,
	)
}

func collectReflectedMapKeyTypeParamDecodes(
	typ types.Type,
	codec reflectedCodecUse,
	typeParamIndexes map[*types.TypeParam]int,
	out *[]reflectedTypeParamUse,
) {
	collectReflectedTypeParamUses(
		typ,
		codec,
		typeParamIndexes,
		out,
		make(map[string]struct{}),
		reflectedTextDecodeContext,
	)
}

func collectReflectedSettableTypeParamDecodes(
	typ types.Type,
	codec reflectedCodecUse,
	typeParamIndexes map[*types.TypeParam]int,
	out *[]reflectedTypeParamUse,
) {
	collectReflectedTypeParamUses(
		typ,
		codec,
		typeParamIndexes,
		out,
		make(map[string]struct{}),
		reflectedSettableDecodeContext,
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
	key := typ.String()
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
