package smells

import (
	"fmt"
	"go/ast"
	"go/types"
)

const resultWrapperFieldCount = 2

func (l *Runner) checkInternalResultWrappers() {
	methodCounts := l.methodCountsByReceiverName()
	storedTypes := l.resultWrapperStorageTypes()

	l.forEachProductionTypeSpec(func(typeSpec *ast.TypeSpec) {
		if !l.isInternalResultWrapper(typeSpec, methodCounts) {
			return
		}

		if _, stored := storedTypes[l.pkg.TypesInfo.TypeOf(typeSpec.Name)]; stored {
			return
		}

		l.report(
			typeSpec.Name.Pos(),
			"result_wrapper",
			fmt.Sprintf(
				`private result wrapper %q only carries value plus status; return ordinary Go results`,
				typeSpec.Name.Name,
			),
		)
	})
}

func (l *Runner) resultWrapperStorageTypes() map[types.Type]struct{} {
	stored := make(map[types.Type]struct{})
	add := func(typ types.Type) {
		for {
			typ = types.Unalias(typ)

			pointer, ok := typ.(*types.Pointer)
			if !ok {
				stored[typ] = struct{}{}
				return
			}

			typ = pointer.Elem()
		}
	}

	// Storage needs one value; splitting it into separate Go results loses that shape.
	l.forEachProductionDecl(func(decl ast.Decl) {
		ast.Inspect(decl, func(node ast.Node) bool {
			expr, ok := node.(ast.Expr)
			if !ok {
				return true
			}

			typ := l.pkg.TypesInfo.TypeOf(expr)
			if typ == nil {
				return true
			}

			switch typ := typ.Underlying().(type) {
			case *types.Chan:
				add(typ.Elem())
			case *types.Map:
				add(typ.Key())
				add(typ.Elem())
			case *types.Slice:
				add(typ.Elem())
			case *types.Array:
				add(typ.Elem())
			case *types.Struct:
				for field := range typ.Fields() {
					add(field.Type())
				}
			}

			return true
		})
	})

	return stored
}

func (l *Runner) isInternalResultWrapper(
	typeSpec *ast.TypeSpec,
	methodCounts map[string]int,
) bool {
	if typeSpec == nil || typeSpec.Name == nil || ast.IsExported(typeSpec.Name.Name) {
		return false
	}

	if !identifierHasWord(typeSpec.Name.Name, "result") &&
		!identifierHasWord(typeSpec.Name.Name, "response") &&
		!identifierHasWord(typeSpec.Name.Name, "outcome") {
		return false
	}

	if methodCounts[typeSpec.Name.Name] != 0 {
		return false
	}

	st, ok := typeSpec.Type.(*ast.StructType)
	if !ok || st.Fields == nil || len(st.Fields.List) != resultWrapperFieldCount {
		return false
	}

	if !resultWrapperFieldsArePlain(st.Fields.List) {
		return false
	}

	return l.resultWrapperReturnedByPrivateFunc(typeSpec.Name.Name)
}

func resultWrapperFieldsArePlain(fields []*ast.Field) bool {
	statusFields := 0

	for _, field := range fields {
		if field.Tag != nil || len(field.Names) != 1 || field.Names[0] == nil {
			return false
		}

		name := field.Names[0].Name
		if name == "ok" || name == "err" || name == "error" {
			statusFields++
		}
	}

	return statusFields == 1
}

func (l *Runner) resultWrapperReturnedByPrivateFunc(typeName string) bool {
	found := false

	l.forEachProductionFunc(func(fn *ast.FuncDecl) {
		if found || fn.Name == nil || ast.IsExported(fn.Name.Name) {
			return
		}

		found = funcResultsContainIdent(fn.Type.Results, typeName)
	})

	return found
}

func funcResultsContainIdent(results *ast.FieldList, typeName string) bool {
	if results == nil {
		return false
	}

	for _, field := range results.List {
		if ident, ok := field.Type.(*ast.Ident); ok && ident.Name == typeName {
			return true
		}
	}

	return false
}
