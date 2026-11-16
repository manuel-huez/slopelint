package smells

import (
	"fmt"
	"go/ast"
)

func reportGenericNameForHelper(sink *findingSink, fn *ast.FuncDecl, reason string) {
	if !genericPrivateName(fn.Name.Name) {
		return
	}

	sink.report(
		fn.Name.Pos(),
		"generic_naming",
		fmt.Sprintf(
			`private helper %q has generic name and %s; rename or inline`,
			fn.Name.Name,
			reason,
		),
	)
}

func genericPrivateName(name string) bool {
	for _, word := range splitIdentifierWords(name) {
		switch word {
		case "helper", "manager", "processor", "util", "utils", "base", "impl", "wrapper":
			return true
		default:
			continue
		}
	}

	return false
}

const initFuncName = "init"

func funcParamCount(fields *ast.FieldList) int {
	if fields == nil {
		return 0
	}

	count := 0

	for _, field := range fields.List {
		if len(field.Names) == 0 {
			count++
			continue
		}

		count += len(field.Names)
	}

	return count
}
