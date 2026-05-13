package smells

import (
	"fmt"
	"go/ast"
	"strings"
)

const tableTestCaseLimit = 10

func (l *Runner) checkUnnamedLargeTableTests() {
	for _, file := range l.pkg.Files {
		if !l.fileIsTest(file) {
			continue
		}

		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}

			l.checkUnnamedLargeTableTest(lit)

			return true
		})
	}
}

func (l *Runner) checkUnnamedLargeTableTest(lit *ast.CompositeLit) {
	structType, ok := anonymousStructSliceType(lit.Type)
	if !ok || len(lit.Elts) <= tableTestCaseLimit || structTypeHasCaseNameField(structType) {
		return
	}

	l.report(
		lit.Pos(),
		"table_test_grouping",
		fmt.Sprintf(
			"table test has %d cases without name/desc field; add case names so failures identify scenarios",
			len(lit.Elts),
		),
	)
}

func anonymousStructSliceType(expr ast.Expr) (*ast.StructType, bool) {
	arrayType, ok := expr.(*ast.ArrayType)
	if !ok {
		return nil, false
	}

	structType, ok := arrayType.Elt.(*ast.StructType)

	return structType, ok
}

func structTypeHasCaseNameField(structType *ast.StructType) bool {
	if structType == nil || structType.Fields == nil {
		return false
	}

	for _, field := range structType.Fields.List {
		for _, name := range field.Names {
			if name != nil && caseNameField(name.Name) {
				return true
			}
		}
	}

	return false
}

func caseNameField(name string) bool {
	switch strings.ToLower(name) {
	case "name", "desc", "description":
		return true
	default:
		return false
	}
}
