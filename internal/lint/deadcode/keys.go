package deadcode

import (
	"go/ast"
	"go/token"
	"go/types"
)

func (l *packageLinter) deadCodeIgnoredIdentPositions() map[token.Pos]struct{} {
	ignored := make(map[token.Pos]struct{})

	l.forEachProductionFunc(func(fn *ast.FuncDecl) {
		if fn.Recv == nil {
			return
		}

		for _, field := range fn.Recv.List {
			ast.Inspect(field.Type, func(n ast.Node) bool {
				ident, ok := n.(*ast.Ident)
				if ok && ident != nil {
					ignored[ident.Pos()] = struct{}{}
				}

				return true
			})
		}
	})

	return ignored
}

func (l *packageLinter) reportableDeadCodeFunc(fn *ast.FuncDecl) bool {
	return l.reportableDeadCodeFuncWithName(fn, reportableDeadCodeName)
}

func (l *packageLinter) reportableDeadCodeFuncWithName(
	fn *ast.FuncDecl,
	reportName func(string) bool,
) bool {
	if fn == nil || fn.Name == nil || fn.Body == nil {
		return false
	}

	if fn.Name.Name == initFuncName {
		return false
	}

	if l.pkg.Name == mainPkgName && fn.Name.Name == mainPkgName {
		return false
	}

	return reportName(fn.Name.Name)
}

func reportableDeadCodeName(name string) bool {
	return name != "" && name != "_" && !ast.IsExported(name)
}

func canonicalDeadCodeObject(obj types.Object) types.Object {
	if fn, ok := obj.(*types.Func); ok && fn != nil {
		return fn.Origin()
	}

	return obj
}

func deadCodeObjectKey(obj types.Object) string {
	obj = canonicalDeadCodeObject(obj)
	if obj == nil {
		return ""
	}

	pkgPath := ""
	if obj.Pkg() != nil {
		pkgPath = obj.Pkg().Path()
	}

	switch obj := obj.(type) {
	case *types.Func:
		return pkgPath + "|func|" + obj.FullName()
	case *types.TypeName:
		if !isPackageLevelDeadCodeObject(obj) {
			return ""
		}

		return pkgPath + "|type|" + obj.Name()
	case *types.Var:
		if obj.IsField() {
			return ""
		}

		if !isPackageLevelDeadCodeObject(obj) {
			return ""
		}

		return pkgPath + "|var|" + obj.Name()
	case *types.Const:
		if !isPackageLevelDeadCodeObject(obj) {
			return ""
		}

		return pkgPath + "|const|" + obj.Name()
	default:
		return pkgPath + "|" + obj.Id()
	}
}

func isPackageLevelDeadCodeObject(obj types.Object) bool {
	return obj != nil &&
		obj.Pkg() != nil &&
		obj.Parent() == obj.Pkg().Scope()
}

func deadCodeStructFieldKeyFromNamed(named *types.Named, fieldName string) string {
	if named == nil || named.Obj() == nil || named.Obj().Pkg() == nil {
		return ""
	}

	return deadCodeStructFieldKey(named.Obj().Pkg().Path(), named.Obj().Name(), fieldName)
}

func deadCodeStructFieldKey(pkgPath string, typeName string, fieldName string) string {
	if pkgPath == "" || typeName == "" || fieldName == "" || fieldName == "_" {
		return ""
	}

	return pkgPath + "|field|" + typeName + "." + fieldName
}
