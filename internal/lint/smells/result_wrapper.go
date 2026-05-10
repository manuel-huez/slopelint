package smells

import (
	"fmt"
	"go/ast"
)

const resultWrapperFieldCount = 2

func (l *Runner) checkInternalResultWrappers() {
	methodCounts := l.methodCountsByReceiverName()

	l.forEachProductionTypeSpec(func(typeSpec *ast.TypeSpec) {
		if !l.isInternalResultWrapper(typeSpec, methodCounts) {
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
