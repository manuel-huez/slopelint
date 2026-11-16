package smells

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
)

const (
	ownerFileLineLimit = 1000
	funcDeclLineLimit  = 100
	funcLitLineLimit   = 40
	funcParamLimit     = 6
	funcResultLimit    = 3
	typeMethodLimit    = 40
)

// sizeLimitDeleteFirst leads size advice: removing unneeded code often fixes
// the size, and splitting first would spread dead code across new owners.
const sizeLimitDeleteFirst = "first delete code that is no longer needed (legacy compatibility layers, redundant checks, defensive code for cases that cannot happen)"

type sizeLimitCheck struct {
	ruleEnv
}

func (l sizeLimitCheck) checkOwnerFiles() {
	for _, file := range l.pkg.Files {
		if ast.IsGenerated(file) {
			continue
		}

		tokenFile := l.pkg.FSet.File(file.Pos())
		if tokenFile.LineCount() <= ownerFileLineLimit {
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

func (l sizeLimitCheck) checkFunctions() {
	l.forEachHandwrittenProductionFunc(l.checkFunction)
}

func (l sizeLimitCheck) checkFunction(fn *ast.FuncDecl) {
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

	if lines := nodeLineSpan(l.pkg.FSet, fn); lines > funcDeclLineLimit {
		l.report(fn.Name.Pos(), "long_function", fmt.Sprintf(
			"function %q has %d lines; "+sizeLimitDeleteFirst+", then split the rest by responsibility",
			fn.Name.Name,
			lines,
		))
	}

	ast.Inspect(fn.Body, func(node ast.Node) bool {
		lit, ok := node.(*ast.FuncLit)
		if ok && nodeLineSpan(l.pkg.FSet, lit) > funcLitLineLimit {
			l.report(lit.Pos(), "long_func_literal", fmt.Sprintf(
				"function literal has %d lines; "+sizeLimitDeleteFirst+", then extract the rest into a named function",
				nodeLineSpan(l.pkg.FSet, lit),
			))
		}

		return true
	})
}

func (l sizeLimitCheck) checkReceivers() {
	methods := make(map[*types.TypeName]int)
	files := make(map[*types.TypeName]map[*token.File]struct{})

	l.forEachHandwrittenProductionFunc(func(fn *ast.FuncDecl) {
		recv := l.pkg.TypesInfo.Defs[fn.Name].(*types.Func).Signature().Recv()
		if recv == nil {
			return
		}

		typ := types.Unalias(recv.Type())
		if ptr, ok := typ.(*types.Pointer); ok {
			typ = types.Unalias(ptr.Elem())
		}

		// Origin maps generic receivers such as Set[T] to their base type.
		owner := typ.(*types.Named).Origin().Obj()
		methods[owner]++

		if files[owner] == nil {
			files[owner] = make(map[*token.File]struct{})
		}

		files[owner][l.pkg.FSet.File(fn.Pos())] = struct{}{}
	})

	for _, spec := range l.pkg.ProductionTypes {
		owner, _ := l.pkg.TypesInfo.Defs[spec.Name].(*types.TypeName)
		if methods[owner] <= typeMethodLimit {
			continue
		}

		l.report(spec.Name.Pos(), "god_receiver", fmt.Sprintf(
			"type %q has %d methods across %d files; "+sizeLimitDeleteFirst+", then split the rest by responsibility so each new type owns its own state; moving methods to functions that take the type as a parameter is not a fix",
			spec.Name.Name,
			methods[owner],
			len(files[owner]),
		))
	}
}

func nodeLineSpan(fset *token.FileSet, node ast.Node) int {
	return fset.Position(node.End()).Line - fset.Position(node.Pos()).Line + 1
}
