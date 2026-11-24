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

func namedDeadCodeType(typ types.Type) *types.Named {
	typ = dereferenceDeadCodeType(typ)
	if typ == nil {
		return nil
	}

	named, _ := types.Unalias(typ).(*types.Named)

	return named
}

func deadCodeFuncReceiver(fn *types.Func) *types.Named {
	if fn == nil {
		return nil
	}

	sig, _ := fn.Type().(*types.Signature)
	if sig == nil || sig.Recv() == nil {
		return nil
	}

	return namedDeadCodeType(sig.Recv().Type())
}

func deadCodeStructType(typ types.Type) *types.Struct {
	typ = dereferenceDeadCodeType(typ)
	if typ == nil {
		return nil
	}

	if named, ok := types.Unalias(typ).(*types.Named); ok {
		typ = named.Underlying()
	}

	structType, _ := types.Unalias(typ).Underlying().(*types.Struct)

	return structType
}

func dereferenceDeadCodeType(typ types.Type) types.Type {
	if typ == nil {
		return nil
	}

	typ = types.Unalias(typ)
	if ptr, ok := typ.(*types.Pointer); ok {
		return types.Unalias(ptr.Elem())
	}

	return typ
}
