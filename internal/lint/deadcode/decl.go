package deadcode

import (
	"go/ast"
	"go/token"
	"go/types"
	"slices"
)

func (graph *deadCodeGraph) addDecl(l *packageLinter, decl ast.Decl, mode deadCodeMode) {
	switch decl := decl.(type) {
	case *ast.FuncDecl:
		graph.addFuncDecl(l, decl, mode)
	case *ast.GenDecl:
		graph.addGenDecl(l, decl, mode)
	}
}

func (graph *deadCodeGraph) addFuncDecl(
	l *packageLinter,
	fn *ast.FuncDecl,
	mode deadCodeMode,
) {
	if fn == nil || fn.Name == nil {
		return
	}

	obj, _ := l.pkg.TypesInfo.Defs[fn.Name].(*types.Func)

	if l.keepFuncLive(fn, obj, mode) {
		graph.addRootUses(l, fn)
		return
	}

	graph.addFuncCandidate(l.pkg, obj, fn, deadCodeFuncKind(fn))
}

func (l *packageLinter) keepFuncLive(
	fn *ast.FuncDecl,
	obj *types.Func,
	mode deadCodeMode,
) bool {
	return obj == nil ||
		!l.reportableFuncForMode(fn, mode) ||
		isErrorHookMethod(obj) ||
		isMarkerMethod(l, fn, obj)
}

func (l *packageLinter) reportableFuncForMode(
	fn *ast.FuncDecl,
	mode deadCodeMode,
) bool {
	if mode == deadCodePrivate {
		return l.reportableDeadCodeFunc(fn)
	}

	return l.reportableRepoDeadCodeFunc(fn)
}

func deadCodeFuncKind(fn *ast.FuncDecl) string {
	if fn != nil && fn.Recv != nil {
		return "method"
	}

	return "function"
}

var errorHookParamTypeNames = map[string]string{
	"Is": "error",
	"As": "any",
}

func isErrorHookMethod(fn *types.Func) bool {
	if fn == nil {
		return false
	}

	sig, ok := fn.Type().(*types.Signature)
	if !ok {
		return false
	}

	if sig == nil {
		return false
	}

	recv := sig.Recv()
	if recv == nil {
		return false
	}

	if !receiverCanBeError(recv.Type()) {
		return false
	}

	if fn.Name() == "Unwrap" {
		return unwrapHookSignature(sig)
	}

	paramTypeName, ok := errorHookParamTypeNames[fn.Name()]
	if !ok {
		return false
	}

	params := sig.Params()
	if tupleLen(params) != 1 {
		return false
	}

	results := sig.Results()
	if tupleLen(results) != 1 {
		return false
	}

	result, ok := types.Unalias(results.At(0).Type()).Underlying().(*types.Basic)
	if !ok {
		return false
	}

	if result.Kind() != types.Bool {
		return false
	}

	return typeIsUniverseType(params.At(0).Type(), paramTypeName)
}

func receiverCanBeError(receiver types.Type) bool {
	if receiver == nil {
		return false
	}

	if typeAssignableToError(receiver) {
		return true
	}

	if _, ok := receiver.(*types.Pointer); ok {
		return false
	}

	return typeAssignableToError(types.NewPointer(receiver))
}

func typeAssignableToError(typ types.Type) bool {
	obj := types.Universe.Lookup("error")
	if obj == nil {
		return false
	}

	return types.AssignableTo(typ, obj.Type())
}

func unwrapHookSignature(sig *types.Signature) bool {
	if tupleLen(sig.Params()) != 0 || tupleLen(sig.Results()) != 1 {
		return false
	}

	result := sig.Results().At(0).Type()
	if typeIsError(result) {
		return true
	}

	slice, ok := types.Unalias(result).Underlying().(*types.Slice)

	return ok && typeIsError(slice.Elem())
}

func tupleLen(tuple *types.Tuple) int {
	if tuple == nil {
		return 0
	}

	return tuple.Len()
}

func typeIsError(typ types.Type) bool {
	return typeIsUniverseType(typ, "error")
}

func typeIsAny(typ types.Type) bool {
	return typeIsUniverseType(typ, "any")
}

func typeIsUniverseType(typ types.Type, name string) bool {
	obj := types.Universe.Lookup(name)
	if obj == nil {
		return false
	}

	return types.Identical(typ, obj.Type())
}

func (graph *deadCodeGraph) addFuncCandidate(
	pkg *Package,
	obj *types.Func,
	fn *ast.FuncDecl,
	kind string,
) {
	origin := obj.Origin()
	graph.candidates[deadCodeObjectKey(origin)] = deadCodeDecl{
		obj:      origin,
		node:     fn,
		name:     fn.Name.Name,
		kind:     kind,
		pos:      fn.Name.Pos(),
		exported: ast.IsExported(fn.Name.Name),
		pkg:      pkg,
	}
}

func (l *packageLinter) reportableRepoDeadCodeFunc(fn *ast.FuncDecl) bool {
	return l.reportableDeadCodeFuncWithName(fn, reportableRepoDeadCodeName)
}

func (graph *deadCodeGraph) addGenDecl(
	l *packageLinter,
	decl *ast.GenDecl,
	mode deadCodeMode,
) {
	for _, spec := range decl.Specs {
		switch spec := spec.(type) {
		case *ast.TypeSpec:
			graph.addTypeSpec(l, spec, mode)
		case *ast.ValueSpec:
			graph.addValueSpec(l, decl.Tok, spec, mode)
		}
	}
}

func (graph *deadCodeGraph) addTypeSpec(
	l *packageLinter,
	spec *ast.TypeSpec,
	mode deadCodeMode,
) {
	if spec == nil || spec.Name == nil {
		return
	}

	obj, _ := l.pkg.TypesInfo.Defs[spec.Name].(*types.TypeName)
	if obj == nil {
		graph.addRootUses(l, spec)
		return
	}

	if reportableDeadCodeNameForMode(spec.Name.Name, mode) {
		graph.candidates[deadCodeObjectKey(obj)] = deadCodeDecl{
			obj:      obj,
			node:     spec,
			name:     spec.Name.Name,
			kind:     "type",
			pos:      spec.Name.Pos(),
			exported: ast.IsExported(spec.Name.Name),
			pkg:      l.pkg,
		}
	} else {
		graph.addRootUses(l, spec)
	}

	graph.addStructFieldCandidates(l, spec, mode)
}

func (graph *deadCodeGraph) addStructFieldCandidates(
	l *packageLinter,
	spec *ast.TypeSpec,
	mode deadCodeMode,
) {
	if spec == nil || spec.Name == nil || spec.Assign.IsValid() {
		return
	}

	structType, ok := spec.Type.(*ast.StructType)
	if !ok || structType.Fields == nil {
		return
	}

	for _, field := range structType.Fields.List {
		for _, name := range field.Names {
			obj, ok := l.deadCodeStructFieldObject(name, mode)
			if !ok {
				continue
			}

			graph.candidates[deadCodeStructFieldKey(l.pkg.ImportPath, spec.Name.Name, name.Name)] = deadCodeDecl{
				obj:      obj,
				node:     field,
				name:     spec.Name.Name + "." + name.Name,
				kind:     "field",
				pos:      name.Pos(),
				exported: ast.IsExported(name.Name),
				pkg:      l.pkg,
			}
		}
	}
}

func (l *packageLinter) deadCodeStructFieldObject(
	name *ast.Ident,
	mode deadCodeMode,
) (*types.Var, bool) {
	if name == nil ||
		!reportableDeadCodeNameForMode(name.Name, mode) {
		return nil, false
	}

	obj, _ := l.pkg.TypesInfo.Defs[name].(*types.Var)

	return obj, obj != nil && obj.IsField()
}

func (graph *deadCodeGraph) addValueSpec(
	l *packageLinter,
	tok token.Token,
	spec *ast.ValueSpec,
	mode deadCodeMode,
) {
	if spec == nil {
		return
	}

	sideEffect := tok == token.VAR && slices.ContainsFunc(spec.Values, exprHasCalls)
	rootSpec := false

	for _, name := range spec.Names {
		if name == nil {
			continue
		}

		if sideEffect || !reportableDeadCodeNameForMode(name.Name, mode) {
			rootSpec = true
			continue
		}

		obj := l.pkg.TypesInfo.Defs[name]
		if obj == nil {
			rootSpec = true
			continue
		}

		kind := "const"
		if tok == token.VAR {
			kind = "var"
		}

		graph.candidates[deadCodeObjectKey(obj)] = deadCodeDecl{
			obj:      obj,
			node:     spec,
			name:     name.Name,
			kind:     kind,
			pos:      name.Pos(),
			exported: ast.IsExported(name.Name),
			pkg:      l.pkg,
		}
	}

	if rootSpec {
		graph.addRootUses(l, spec)
	}
}

func reportableDeadCodeNameForMode(name string, mode deadCodeMode) bool {
	if mode == deadCodeRepo {
		return reportableRepoDeadCodeName(name)
	}

	return reportableDeadCodeName(name)
}

func reportableRepoDeadCodeName(name string) bool {
	return name != "" && name != "_"
}
