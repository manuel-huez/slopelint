package deadcode

import (
	"go/ast"
)

func (graph deadCodeGraph) interfaceMethodUses(
	l *packageLinter,
	node ast.Node,
) map[string]struct{} {
	switch node := node.(type) {
	case *ast.CallExpr:
		return graph.callInterfaceMethodUses(l, node)
	case *ast.FuncDecl:
		return graph.funcDeclReturnInterfaceMethodUses(l, node)
	case *ast.FuncLit:
		return graph.funcLitReturnInterfaceMethodUses(l, node)
	case *ast.AssignStmt:
		return graph.assignInterfaceMethodUses(l, node)
	case *ast.ValueSpec:
		return graph.valueSpecInterfaceMethodUses(l, node)
	case *ast.CompositeLit:
		return graph.compositeLitInterfaceMethodUses(l, node)
	default:
		return nil
	}
}
