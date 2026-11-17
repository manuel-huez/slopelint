package smells

import (
	"fmt"
	"github.com/manuel-huez/slopelint/internal/lint/outline"
	"go/ast"
	"go/types"
	"slices"
)

const resultWrapperFieldCount = 2

type resultWrapperCheck struct {
	ruleEnv
}

func (l resultWrapperCheck) check() {
	methodCounts := l.methodCountsByReceiverName()
	storedTypes := l.storageTypes()

	for _, typeSpec := range l.pkg.ProductionTypes {
		if !l.isInternalResultWrapper(typeSpec, methodCounts) {
			continue
		}

		if _, stored := storedTypes[l.pkg.TypesInfo.TypeOf(typeSpec.Name)]; stored {
			continue
		}

		l.report(
			typeSpec.Name.Pos(),
			"result_wrapper",
			fmt.Sprintf(
				`private result wrapper %q only carries value plus status; return ordinary Go results`,
				typeSpec.Name.Name,
			),
		)
	}
}

func (l resultWrapperCheck) methodCountsByReceiverName() map[string]int {
	counts := make(map[string]int)

	for _, fn := range l.pkg.ProductionFuncs {
		if fn.Recv == nil || len(fn.Recv.List) != 1 {
			continue
		}

		if name, ok := receiverTypeName(fn.Recv.List[0].Type); ok {
			counts[name]++
		}
	}

	return counts
}

func (l resultWrapperCheck) storageTypes() map[types.Type]struct{} {
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
	for _, decl := range l.pkg.ProductionDecls {
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
	}

	return stored
}

func (l resultWrapperCheck) isInternalResultWrapper(
	typeSpec *ast.TypeSpec,
	methodCounts map[string]int,
) bool {
	if ast.IsExported(typeSpec.Name.Name) {
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
	if !ok || len(st.Fields.List) != resultWrapperFieldCount {
		return false
	}

	if !resultWrapperFieldsArePlain(st.Fields.List) {
		return false
	}

	return l.returnedByPrivateFunc(typeSpec.Name.Name)
}

func resultWrapperFieldsArePlain(fields []*ast.Field) bool {
	statusFields := 0

	for _, field := range fields {
		if field.Tag != nil || len(field.Names) != 1 {
			return false
		}

		name := field.Names[0].Name
		if name == "ok" || name == "err" || name == "error" {
			statusFields++
		}
	}

	return statusFields == 1
}

func (l resultWrapperCheck) returnedByPrivateFunc(typeName string) bool {
	for _, fn := range l.pkg.ProductionFuncs {
		if !ast.IsExported(fn.Name.Name) && funcResultsContainIdent(fn.Type.Results, typeName) {
			return true
		}
	}

	return false
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

func receiverTypeName(expr ast.Expr) (string, bool) {
	switch expr := expr.(type) {
	case *ast.Ident:
		return expr.Name, true
	case *ast.StarExpr:
		return receiverTypeName(expr.X)
	default:
		return "", false
	}
}

func identifierHasWord(name, want string) bool {
	return slices.Contains(outline.Words(name), want)
}
