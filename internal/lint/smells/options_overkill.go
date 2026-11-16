package smells

import (
	"fmt"
	"go/ast"
	"go/types"
)

const optionsOverkillMaxUses = 2

// optionsOverkillCheck reports private constructors that take functional
// options but have at most two production uses.
type optionsOverkillCheck struct {
	ruleEnv
}

func (l optionsOverkillCheck) check() {
	useCounts := l.productionPackageFuncUseCounts()

	for _, fn := range l.pkg.ProductionFuncs {
		obj, ok := privateFuncWithFunctionalOptions(l.pkg.TypesInfo, fn, useCounts)
		if !ok {
			continue
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
	}
}

func privateFuncWithFunctionalOptions(
	info *types.Info,
	fn *ast.FuncDecl,
	useCounts map[string]int,
) (*types.Func, bool) {
	if fn.Recv != nil || fn.Doc != nil || !privateConstructorName(fn.Name.Name) {
		return nil, false
	}

	obj, ok := privateFuncObject(info, fn)
	if !ok {
		return nil, false
	}

	count := useCounts[funcObjectKey(obj)]
	if count == 0 || count > optionsOverkillMaxUses {
		return nil, false
	}

	if !funcHasFunctionalOptionParam(info, fn.Type.Params) {
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
