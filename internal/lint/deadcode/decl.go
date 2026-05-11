package deadcode

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"
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

	if mode == deadCodePrivate {
		if obj == nil || !l.reportableDeadCodeFunc(fn) || isMarshalHookMethod(fn) {
			graph.addRootUses(l, fn)
			return
		}

		graph.addFuncCandidate(l.pkg, obj, fn, deadCodeFuncKind(fn))

		return
	}

	if obj == nil || !l.reportableRepoDeadCodeFunc(fn) || isMarshalHookMethod(fn) {
		graph.addRootUses(l, fn)
		return
	}

	graph.addFuncCandidate(l.pkg, obj, fn, deadCodeFuncKind(fn))
}

func deadCodeFuncKind(fn *ast.FuncDecl) string {
	if fn != nil && fn.Recv != nil {
		return "method"
	}

	return "function"
}

func isMarshalHookMethod(fn *ast.FuncDecl) bool {
	if fn == nil || fn.Recv == nil || fn.Name == nil {
		return false
	}

	name := fn.Name.Name

	return strings.HasPrefix(name, "Marshal") || strings.HasPrefix(name, "Unmarshal")
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
			if name == nil || !reportableDeadCodeNameForMode(name.Name, mode) {
				continue
			}

			obj, _ := l.pkg.TypesInfo.Defs[name].(*types.Var)
			if obj == nil || !obj.IsField() {
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

func (graph *deadCodeGraph) addValueSpec(
	l *packageLinter,
	tok token.Token,
	spec *ast.ValueSpec,
	mode deadCodeMode,
) {
	if spec == nil {
		return
	}

	sideEffect := tok == token.VAR && valueSpecHasCalls(spec)
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
