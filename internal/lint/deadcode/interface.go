package deadcode

import (
	"go/ast"
	"go/types"
)

// ifaceUses finds interface methods a value may reach. When the dynamic type
// is unknown, it checks the receiver types of candidate methods.
type ifaceUses struct {
	candidates map[string]deadCodeDecl
}

func (iface ifaceUses) interfaceMethodUses(
	l *packageLinter,
	node ast.Node,
) map[string]struct{} {
	scan := l.ifaceScan()

	switch node := node.(type) {
	case *ast.CallExpr:
		return scan.callInterfaceMethodUses(node)
	case *ast.TypeSwitchStmt:
		return iface.typeSwitchInterfaceMethodUses(l, node)
	case *ast.TypeAssertExpr:
		return iface.typeAssertInterfaceMethodUses(l, node)
	case *ast.FuncDecl:
		return scan.funcDeclReturnInterfaceMethodUses(node)
	case *ast.FuncLit:
		return scan.funcLitReturnInterfaceMethodUses(node)
	case *ast.AssignStmt:
		return scan.assignInterfaceMethodUses(node)
	case *ast.ValueSpec:
		return scan.valueSpecInterfaceMethodUses(node)
	case *ast.CompositeLit:
		return scan.compositeLitInterfaceMethodUses(node)
	default:
		return nil
	}
}

// ifaceScan finds the methods that interface conversions, calls and
// assertions in one package keep alive. It needs only the package's types.
type ifaceScan struct {
	info *types.Info
	pkg  *types.Package
}

func (l *packageLinter) ifaceScan() ifaceScan {
	return ifaceScan{info: l.pkg.TypesInfo, pkg: l.pkg.TypesPkg}
}

func flowElementValue(expr ast.Expr) ast.Expr {
	if keyed, ok := unparenReflectedExpr(expr).(*ast.KeyValueExpr); ok {
		return keyed.Value
	}

	return expr
}

func (s ifaceScan) genericInstanceInterfaceMethodUses(
	ident *ast.Ident,
) map[string]struct{} {
	if ident == nil {
		return nil
	}

	instance, ok := s.info.Instances[ident]
	if !ok || instance.TypeArgs == nil || instance.TypeArgs.Len() == 0 {
		return nil
	}

	typeParams := genericObjectTypeParams(s.info.Uses[ident])
	if typeParams == nil || typeParams.Len() == 0 {
		return nil
	}

	out := make(map[string]struct{})
	for index := range min(typeParams.Len(), instance.TypeArgs.Len()) {
		s.addInterfaceMethodsForType(
			out,
			instance.TypeArgs.At(index),
			typeParams.At(index).Constraint(),
		)
	}

	return out
}

func genericObjectTypeParams(obj types.Object) *types.TypeParamList {
	switch obj := obj.(type) {
	case *types.Func:
		sig, ok := obj.Type().(*types.Signature)
		if !ok || sig == nil {
			return nil
		}

		return sig.TypeParams()
	case *types.TypeName:
		named, ok := obj.Type().(*types.Named)
		if !ok || named == nil {
			return nil
		}

		return named.TypeParams()
	default:
		return nil
	}
}

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
