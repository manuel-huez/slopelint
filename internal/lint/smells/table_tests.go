package smells

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/types"
	"strings"
)

const tableTestCaseLimit = 10
const repeatedFixtureMinLength = 16

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

func (l *Runner) checkRepeatedTestFixtures() {
	const reportCount = 3

	counts := make(map[string]int)

	for _, file := range l.pkg.TestFiles {
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) != 3 || !l.testFixtureWriteCall(call) {
				return true
			}

			value, ok := l.pkg.TypesInfo.Types[call.Args[2]]
			if !ok || value.Value == nil || value.Value.Kind() != constant.String {
				return true
			}

			contents := constant.StringVal(value.Value)
			if len(contents) < repeatedFixtureMinLength {
				return true
			}

			counts[contents]++
			if counts[contents] == reportCount {
				l.report(
					call.Args[2].Pos(),
					"repeated_test_fixture",
					fmt.Sprintf(
						"test fixture content is repeated %d times; centralize it in the owning test builder",
						reportCount,
					),
				)
			}

			return true
		})
	}
}

func (l *Runner) testFixtureWriteCall(call *ast.CallExpr) bool {
	ident, ok := l.unparen(call.Fun).(*ast.Ident)
	if !ok || ident.Name != "writeFile" {
		return false
	}

	fn, ok := l.pkg.TypesInfo.ObjectOf(ident).(*types.Func)

	return ok && fn != nil
}
