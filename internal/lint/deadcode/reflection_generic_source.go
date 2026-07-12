package deadcode

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strconv"
	"strings"
)

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
