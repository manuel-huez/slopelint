package smells

import (
	"fmt"
	"go/ast"
	"go/types"
)

const optionsOverkillMaxUses = 2

func (l *Runner) checkOptionsOverkill() {
	useCounts := l.productionPackageFuncUseCounts()

	l.forEachProductionFunc(func(fn *ast.FuncDecl) {
		obj, ok := l.privateFuncWithFunctionalOptions(fn, useCounts)
		if !ok {
			return
		}

		l.report(
			fn.Name.Pos(),
			"api_overkill",
			fmt.Sprintf(
				`private API %q uses functional options for %d production uses; pass config directly`,
				fn.Name.Name,
				useCounts[funcObjectKey(obj)],
			),
		)
	})
}

func (l *Runner) privateFuncWithFunctionalOptions(
	fn *ast.FuncDecl,
	useCounts map[string]int,
) (*types.Func, bool) {
	if !isEligiblePrivateSmellFunc(fn) || !privateConstructorName(fn.Name.Name) {
		return nil, false
	}

	obj, ok := l.pkg.TypesInfo.ObjectOf(fn.Name).(*types.Func)
	if !ok || obj == nil {
		return nil, false
	}

	count := useCounts[funcObjectKey(obj)]
	if count == 0 || count > optionsOverkillMaxUses {
		return nil, false
	}

	if !funcHasFunctionalOptionParam(l.pkg.TypesInfo, fn.Type.Params) {
		return nil, false
	}

	return obj, true
}

func privateConstructorName(name string) bool {
	words := splitIdentifierWords(name)
	if len(words) == 0 {
		return false
	}

	return words[0] == "new" || words[0] == "build"
}

func funcHasFunctionalOptionParam(info *types.Info, fields *ast.FieldList) bool {
	if fields == nil {
		return false
	}

	for _, field := range fields.List {
		ellipsis, ok := field.Type.(*ast.Ellipsis)
		if !ok {
			continue
		}

		if typeIsFunctionSignature(info.TypeOf(ellipsis.Elt)) {
			return true
		}
	}

	return false
}

func typeIsFunctionSignature(typ types.Type) bool {
	if typ == nil {
		return false
	}

	if _, ok := types.Unalias(typ).Underlying().(*types.Signature); ok {
		return true
	}

	return false
}
