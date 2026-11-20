package deadcode

import (
	"go/ast"
	"go/token"
	"go/types"
)

// useCollector finds what a node uses: identifiers, selections, struct
// literal fields, interface methods a value may reach, fmt String methods and
// reflected codec fields. Its parts keep caches for the whole run.
type useCollector struct {
	ignored map[*Package]map[token.Pos]struct{}
	iface   ifaceUses
	fmt     fmtConcreteFlow
	reflect reflectionUses
}

func (uses *useCollector) usesFrom(
	l *packageLinter,
	node ast.Node,
) map[string]struct{} {
	out := make(map[string]struct{})

	for key := range uses.fmt.fmtStringerFlowUses(l, node) {
		out[key] = struct{}{}
	}

	ast.Inspect(node, func(n ast.Node) bool {
		if n == nil {
			return true
		}

		uses.addNonIdentUses(l, out, n)

		ident, ok := n.(*ast.Ident)
		if !ok {
			return true
		}

		if _, ignored := uses.ignored[l.pkg][ident.Pos()]; ignored {
			return true
		}

		for key := range l.ifaceScan().genericInstanceInterfaceMethodUses(ident) {
			out[key] = struct{}{}
		}

		obj := canonicalDeadCodeObject(l.pkg.TypesInfo.Uses[ident])
		if obj == nil {
			return true
		}

		key := deadCodeObjectKey(obj)
		if key != "" {
			out[key] = struct{}{}
		}

		return true
	})

	return out
}

func (uses *useCollector) addNonIdentUses(
	l *packageLinter,
	out map[string]struct{},
	node ast.Node,
) {
	for key := range uses.iface.interfaceMethodUses(l, node) {
		out[key] = struct{}{}
	}

	switch node := node.(type) {
	case *ast.CallExpr:
		for key := range uses.reflect.reflectedUses(l, node) {
			out[key] = struct{}{}
		}
	case *ast.CompositeLit:
		for key := range l.compositeLitFieldUses(node) {
			out[key] = struct{}{}
		}
	case *ast.SelectorExpr:
		l.addSelectionUse(out, node)
	}
}

func (l *packageLinter) addSelectionUse(
	out map[string]struct{},
	selector *ast.SelectorExpr,
) {
	selection := l.pkg.TypesInfo.Selections[selector]
	if selection == nil {
		return
	}

	switch obj := selection.Obj().(type) {
	case *types.Func:
		out[deadCodeObjectKey(obj)] = struct{}{}
	case *types.Var:
		if !obj.IsField() {
			return
		}

		key := selectionStructFieldKey(selection)
		if key != "" {
			out[key] = struct{}{}
		}
	}
}

func (l *packageLinter) compositeLitFieldUses(
	lit *ast.CompositeLit,
) map[string]struct{} {
	typ := l.pkg.TypesInfo.TypeOf(lit)
	named := namedDeadCodeType(typ)

	structType := deadCodeStructType(typ)
	if named == nil || structType == nil {
		return nil
	}

	out := make(map[string]struct{})

	forEachCompositeLitStructField(
		lit,
		structType,
		func(_ ast.Expr, _ *types.Var, fieldName string) {
			if fieldName == "" {
				return
			}

			addStructFieldUse(out, named, fieldName)
		},
	)

	return out
}

func addStructFieldUse(
	out map[string]struct{},
	named *types.Named,
	fieldName string,
) {
	key := deadCodeStructFieldKeyFromNamed(named, fieldName)
	if key == "" {
		return
	}

	out[key] = struct{}{}
}

func selectionStructFieldKey(selection *types.Selection) string {
	obj, ok := selection.Obj().(*types.Var)
	if !ok || !obj.IsField() {
		return ""
	}

	return deadCodeStructFieldKeyFromNamed(selectionStructFieldOwner(selection), obj.Name())
}

func selectionStructFieldOwner(selection *types.Selection) *types.Named {
	typ := selection.Recv()
	indexPath := selection.Index()

	for indexPosition, fieldIndex := range indexPath {
		named := namedDeadCodeType(typ)

		structType := deadCodeStructType(typ)
		if structType == nil || fieldIndex < 0 || fieldIndex >= structType.NumFields() {
			return nil
		}

		if indexPosition == len(indexPath)-1 {
			return named
		}

		typ = structType.Field(fieldIndex).Type()
	}

	return nil
}
