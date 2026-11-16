package deadcode

import (
	"go/ast"
	"go/token"
	"go/types"
	"slices"
)

// declIndex adds each production declaration as a dead-code candidate or,
// when it must stay live, as a root use.
type declIndex struct {
	graph *deadCodeGraph
	uses  *useCollector
}

func (index declIndex) addPackageDecls(l *packageLinter, mode deadCodeMode) {
	l.forEachProductionDecl(func(decl ast.Decl) {
		if _, ok := l.pkg.GeneratedDecls[decl]; ok {
			index.addRootUses(l, decl)
			return
		}

		index.addDecl(l, decl, mode)
	})
}

// addRootUses records node as live. Its uses are collected right away:
// fmt and reflection caches filled here only see candidates indexed so far,
// and later results can depend on them. indexEdges recomputes root edges.
func (index declIndex) addRootUses(l *packageLinter, node ast.Node) {
	index.graph.rootUses = append(index.graph.rootUses, deadCodeRootUse{pkg: l.pkg, node: node})
	index.graph.addRootUseEdges(index.uses, l.pkg, node)
}

func (index declIndex) addDecl(l *packageLinter, decl ast.Decl, mode deadCodeMode) {
	switch decl := decl.(type) {
	case *ast.FuncDecl:
		index.addFuncDecl(l, decl, mode)
	case *ast.GenDecl:
		index.addGenDecl(l, decl, mode)
	}
}

func (index declIndex) addFuncDecl(
	l *packageLinter,
	fn *ast.FuncDecl,
	mode deadCodeMode,
) {
	obj, _ := l.pkg.TypesInfo.Defs[fn.Name].(*types.Func)

	if l.keepFuncLive(fn, obj, mode) {
		index.addRootUses(l, fn)
		return
	}

	index.addFuncCandidate(l.pkg, obj, fn, deadCodeFuncKind(fn))
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
	if fn.Body == nil || fn.Name.Name == initFuncName {
		return false
	}

	if l.pkg.Name == mainPkgName && fn.Name.Name == mainPkgName {
		return false
	}

	return reportableDeadCodeNameForMode(fn.Name.Name, mode)
}

func deadCodeFuncKind(fn *ast.FuncDecl) string {
	if fn.Recv != nil {
		return "method"
	}

	return "function"
}

var errorHookParamTypeNames = map[string]string{
	"Is": "error",
	"As": "any",
}

func isErrorHookMethod(fn *types.Func) bool {
	sig := fn.Type().(*types.Signature)

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
	if typeAssignableToError(receiver) {
		return true
	}

	if _, ok := receiver.(*types.Pointer); ok {
		return false
	}

	return typeAssignableToError(types.NewPointer(receiver))
}

func typeAssignableToError(typ types.Type) bool {
	return types.AssignableTo(typ, types.Universe.Lookup("error").Type())
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
	return types.Identical(typ, types.Universe.Lookup(name).Type())
}

func (index declIndex) addFuncCandidate(
	pkg *Package,
	obj *types.Func,
	fn *ast.FuncDecl,
	kind string,
) {
	origin := obj.Origin()

	owner := ""
	if receiver := deadCodeFuncReceiver(origin); receiver != nil {
		owner = deadCodeObjectKey(receiver.Obj())
	}

	index.graph.candidates[deadCodeObjectKey(origin)] = deadCodeDecl{
		obj:      origin,
		node:     fn,
		owner:    owner,
		name:     fn.Name.Name,
		kind:     kind,
		pos:      fn.Name.Pos(),
		exported: ast.IsExported(fn.Name.Name),
		pkg:      pkg,
	}
}

func (index declIndex) addGenDecl(
	l *packageLinter,
	decl *ast.GenDecl,
	mode deadCodeMode,
) {
	for _, spec := range decl.Specs {
		switch spec := spec.(type) {
		case *ast.TypeSpec:
			index.addTypeSpec(l, spec, mode)
		case *ast.ValueSpec:
			index.addValueSpec(l, decl.Tok, spec, mode)
		}
	}
}

func (index declIndex) addTypeSpec(
	l *packageLinter,
	spec *ast.TypeSpec,
	mode deadCodeMode,
) {
	obj, _ := l.pkg.TypesInfo.Defs[spec.Name].(*types.TypeName)
	if obj == nil {
		index.addRootUses(l, spec)
		return
	}

	if reportableDeadCodeNameForMode(spec.Name.Name, mode) {
		index.graph.candidates[deadCodeObjectKey(obj)] = deadCodeDecl{
			obj:      obj,
			node:     spec,
			name:     spec.Name.Name,
			kind:     "type",
			pos:      spec.Name.Pos(),
			exported: ast.IsExported(spec.Name.Name),
			pkg:      l.pkg,
		}
	} else {
		index.addRootUses(l, spec)
	}

	index.addStructFieldCandidates(l, spec, mode)
}

func (index declIndex) addStructFieldCandidates(
	l *packageLinter,
	spec *ast.TypeSpec,
	mode deadCodeMode,
) {
	if spec.Assign.IsValid() {
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

			index.graph.candidates[deadCodeStructFieldKey(l.pkg.ImportPath, spec.Name.Name, name.Name)] = deadCodeDecl{
				obj:      obj,
				node:     field,
				owner:    deadCodeObjectKey(l.pkg.TypesInfo.Defs[spec.Name]),
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
	if !reportableDeadCodeNameForMode(name.Name, mode) {
		return nil, false
	}

	obj, _ := l.pkg.TypesInfo.Defs[name].(*types.Var)

	return obj, obj != nil && obj.IsField()
}

func (index declIndex) addValueSpec(
	l *packageLinter,
	tok token.Token,
	spec *ast.ValueSpec,
	mode deadCodeMode,
) {
	sideEffect := tok == token.VAR && slices.ContainsFunc(spec.Values, exprHasCalls)
	rootSpec := false

	for _, name := range spec.Names {
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

		index.graph.candidates[deadCodeObjectKey(obj)] = deadCodeDecl{
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
		index.addRootUses(l, spec)
	}
}

func reportableDeadCodeNameForMode(name string, mode deadCodeMode) bool {
	if name == "" || name == "_" {
		return false
	}

	return mode == deadCodeRepo || !ast.IsExported(name)
}
