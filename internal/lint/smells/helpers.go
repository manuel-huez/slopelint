package smells

import (
	"fmt"
	"go/ast"
	"slices"
)

func (l *Runner) reportGenericNameForHelper(fn *ast.FuncDecl, reason string) {
	if fn == nil || fn.Name == nil || !genericPrivateName(fn.Name.Name) {
		return
	}

	l.report(
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

func isEligiblePrivateSmellFunc(fn *ast.FuncDecl) bool {
	return fn != nil &&
		fn.Name != nil &&
		fn.Body != nil &&
		fn.Doc == nil &&
		fn.Recv == nil &&
		!ast.IsExported(fn.Name.Name) &&
		!hasTypeParams(fn.Type)
}

const initFuncName = "init"

func (l *Runner) productionPackageFuncUseCounts() map[string]int {
	if l.funcUseCounts == nil {
		l.funcUseCounts = l.packageFuncUseCountsForFiles(l.pkg.ProductionFiles)
	}

	return l.funcUseCounts
}

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

func (l *Runner) methodCountsByReceiverName() map[string]int {
	counts := make(map[string]int)

	l.forEachProductionFunc(func(fn *ast.FuncDecl) {
		if fn.Recv == nil || len(fn.Recv.List) != 1 {
			return
		}

		name, ok := receiverTypeName(fn.Recv.List[0].Type)
		if ok {
			counts[name]++
		}
	})

	return counts
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
	return slices.Contains(splitIdentifierWords(name), want)
}
