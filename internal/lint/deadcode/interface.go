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
