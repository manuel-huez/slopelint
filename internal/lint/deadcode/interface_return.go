package deadcode

import (
	"go/ast"
	"go/types"
)

func (s ifaceScan) assignInterfaceMethodUses(
	assign *ast.AssignStmt,
) map[string]struct{} {
	if assign == nil {
		return nil
	}

	out := make(map[string]struct{})

	if len(assign.Rhs) == 1 {
		targets := make([]types.Type, 0, len(assign.Lhs))
		for _, lhs := range assign.Lhs {
			targets = append(targets, s.info.TypeOf(lhs))
		}

		s.addInterfaceMethodsForValueTargets(out, assign.Rhs[0], targets)

		return out
	}

	for index, rhs := range assign.Rhs {
		if index >= len(assign.Lhs) {
			continue
		}

		s.addInterfaceMethodsForValue(out, rhs, s.info.TypeOf(assign.Lhs[index]))
	}

	return out
}

func (s ifaceScan) funcDeclReturnInterfaceMethodUses(
	fn *ast.FuncDecl,
) map[string]struct{} {
	if fn == nil || fn.Body == nil {
		return nil
	}

	if fn.Name == nil {
		return nil
	}

	obj, ok := s.info.Defs[fn.Name].(*types.Func)
	if !ok || obj == nil {
		return nil
	}

	sig, ok := obj.Type().(*types.Signature)
	if !ok || sig == nil {
		return nil
	}

	return s.returnInterfaceMethodUses(fn.Body, sig)
}

func (s ifaceScan) funcLitReturnInterfaceMethodUses(
	lit *ast.FuncLit,
) map[string]struct{} {
	if lit == nil || lit.Body == nil {
		return nil
	}

	sig, ok := s.info.TypeOf(lit).(*types.Signature)
	if !ok || sig == nil {
		return nil
	}

	return s.returnInterfaceMethodUses(lit.Body, sig)
}

func (s ifaceScan) returnInterfaceMethodUses(
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
			if len(node.Results) == 1 && len(targets) > 1 {
				s.addInterfaceMethodsForValueTargets(out, node.Results[0], targets)

				return true
			}

			for index, result := range node.Results {
				if index >= len(targets) {
					continue
				}

				s.addInterfaceMethodsForValue(out, result, targets[index])
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
