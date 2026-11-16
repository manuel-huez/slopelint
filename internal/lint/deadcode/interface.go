package deadcode

import (
	"go/ast"
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
	switch node := node.(type) {
	case *ast.CallExpr:
		return callInterfaceMethodUses(l, node)
	case *ast.TypeSwitchStmt:
		return iface.typeSwitchInterfaceMethodUses(l, node)
	case *ast.TypeAssertExpr:
		return iface.typeAssertInterfaceMethodUses(l, node)
	case *ast.FuncDecl:
		return funcDeclReturnInterfaceMethodUses(l, node)
	case *ast.FuncLit:
		return funcLitReturnInterfaceMethodUses(l, node)
	case *ast.AssignStmt:
		return assignInterfaceMethodUses(l, node)
	case *ast.ValueSpec:
		return valueSpecInterfaceMethodUses(l, node)
	case *ast.CompositeLit:
		return compositeLitInterfaceMethodUses(l, node)
	default:
		return nil
	}
}
