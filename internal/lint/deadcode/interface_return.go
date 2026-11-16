package deadcode

import (
	"go/ast"
	"go/types"
)

func assignInterfaceMethodUses(
	l *packageLinter,
	assign *ast.AssignStmt,
) map[string]struct{} {
	if assign == nil {
		return nil
	}

	out := make(map[string]struct{})

	if len(assign.Rhs) == 1 {
		targets := make([]types.Type, 0, len(assign.Lhs))
		for _, lhs := range assign.Lhs {
			targets = append(targets, l.pkg.TypesInfo.TypeOf(lhs))
		}

		addInterfaceMethodsForValueTargets(l, out, assign.Rhs[0], targets)

		return out
	}

	for index, rhs := range assign.Rhs {
		if index >= len(assign.Lhs) {
			continue
		}

		addInterfaceMethodsForValue(l, out, rhs, l.pkg.TypesInfo.TypeOf(assign.Lhs[index]))
	}

	return out
}

func funcDeclReturnInterfaceMethodUses(
	l *packageLinter,
	fn *ast.FuncDecl,
) map[string]struct{} {
	if fn == nil || fn.Body == nil {
		return nil
	}

	if fn.Name == nil {
		return nil
	}

	obj, ok := l.pkg.TypesInfo.Defs[fn.Name].(*types.Func)
	if !ok || obj == nil {
		return nil
	}

	sig, ok := obj.Type().(*types.Signature)
	if !ok || sig == nil {
		return nil
	}

	return returnInterfaceMethodUses(l, fn.Body, sig)
}

func funcLitReturnInterfaceMethodUses(
	l *packageLinter,
	lit *ast.FuncLit,
) map[string]struct{} {
	if lit == nil || lit.Body == nil {
		return nil
	}

	sig, ok := l.pkg.TypesInfo.TypeOf(lit).(*types.Signature)
	if !ok || sig == nil {
		return nil
	}

	return returnInterfaceMethodUses(l, lit.Body, sig)
}

func returnInterfaceMethodUses(
	l *packageLinter,
	body *ast.BlockStmt,
	sig *types.Signature,
) map[string]struct{} {
	results := sig.Results()
	if body == nil || results == nil || results.Len() == 0 {
		return nil
	}

	out := make(map[string]struct{})
	targets := tupleTypes(results)

	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			for index, result := range node.Results {
				if index >= len(targets) {
					continue
				}

				addInterfaceMethodsForValue(l, out, result, targets[index])
			}
		}

		return true
	})

	return out
}

func tupleTypes(tuple *types.Tuple) []types.Type {
	out := make([]types.Type, 0, tuple.Len())
	for variable := range tuple.Variables() {
		out = append(out, variable.Type())
	}

	return out
}
