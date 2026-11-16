package smells

import (
	"fmt"
	"go/ast"
)

const (
	ownerFileLineLimit = 1000
	funcDeclLineLimit  = 100
	funcLitLineLimit   = 40
	funcParamLimit     = 6
	funcResultLimit    = 3
)

// sizeLimitDeleteFirst leads size advice: removing unneeded code often fixes
// the size, and splitting first would spread dead code across new owners.
const sizeLimitDeleteFirst = "first delete code that is no longer needed (legacy compatibility layers, redundant checks, defensive code for cases that cannot happen)"

func (l *Runner) checkOversizedOwnerFiles() {
	for _, file := range l.pkg.Files {
		if file == nil || ast.IsGenerated(file) {
			continue
		}

		tokenFile := l.pkg.FSet.File(file.Pos())
		if tokenFile == nil || tokenFile.LineCount() <= ownerFileLineLimit {
			continue
		}

		l.report(
			file.Package,
			"oversized_owner_file",
			fmt.Sprintf(
				"source file has %d lines; "+sizeLimitDeleteFirst+", then split the rest by responsibility so whole-file review stays tractable",
				tokenFile.LineCount(),
			),
		)
	}
}

func (l *Runner) checkFunctionSizeLimits() {
	l.forEachHandwrittenProductionFunc(l.checkFunctionSize)
}

func (l *Runner) checkFunctionSize(fn *ast.FuncDecl) {
	if params := funcParamCount(fn.Type.Params); params > funcParamLimit {
		l.report(fn.Name.Pos(), "long_param_list", fmt.Sprintf(
			"function %q has %d parameters; "+sizeLimitDeleteFirst+", then group related parameters into a struct or split the function",
			fn.Name.Name,
			params,
		))
	}

	if results := funcParamCount(fn.Type.Results); results > funcResultLimit {
		l.report(fn.Name.Pos(), "long_result_list", fmt.Sprintf(
			"function %q returns %d results; "+sizeLimitDeleteFirst+", then return a named struct",
			fn.Name.Name,
			results,
		))
	}

	if fn.Body == nil {
		return
	}

	if lines := l.nodeLineSpan(fn); lines > funcDeclLineLimit {
		l.report(fn.Name.Pos(), "long_function", fmt.Sprintf(
			"function %q has %d lines; "+sizeLimitDeleteFirst+", then split the rest by responsibility",
			fn.Name.Name,
			lines,
		))
	}

	ast.Inspect(fn.Body, func(node ast.Node) bool {
		lit, ok := node.(*ast.FuncLit)
		if ok && l.nodeLineSpan(lit) > funcLitLineLimit {
			l.report(lit.Pos(), "long_func_literal", fmt.Sprintf(
				"function literal has %d lines; "+sizeLimitDeleteFirst+", then extract the rest into a named function",
				l.nodeLineSpan(lit),
			))
		}

		return true
	})
}

// nodeLineSpan counts lines from the node start to its end, both inclusive.
func (l *Runner) nodeLineSpan(node ast.Node) int {
	return l.pkg.FSet.Position(node.End()).Line - l.pkg.FSet.Position(node.Pos()).Line + 1
}
