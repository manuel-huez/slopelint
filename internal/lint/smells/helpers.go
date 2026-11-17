package smells

import (
	"fmt"
	"github.com/manuel-huez/slopelint/internal/lint/outline"
	"go/ast"
)

func reportGenericNameForHelper(sink *findingSink, fn *ast.FuncDecl, reason string) {
	if !outline.GenericName(fn.Name.Name) {
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
