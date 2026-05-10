package deadcode

import (
	"go/ast"
	"go/types"
)

func (graph *deadCodeGraph) addRootUses(l *packageLinter, node ast.Node) {
	for key := range graph.usesFrom(l, node) {
		graph.roots[key] = struct{}{}
	}
}

func (graph deadCodeGraph) usesFrom(
	l *packageLinter,
	node ast.Node,
) map[string]struct{} {
	out := make(map[string]struct{})

	ast.Inspect(node, func(n ast.Node) bool {
		if n == nil {
			return true
		}

		for key := range graph.interfaceMethodUses(l, n) {
			out[key] = struct{}{}
		}

		if lit, ok := n.(*ast.CompositeLit); ok {
			for key := range graph.compositeLitFieldUses(l, lit) {
				out[key] = struct{}{}
			}
		}

		if selector, ok := n.(*ast.SelectorExpr); ok {
			graph.addSelectionUse(l, out, selector)
			return true
		}

		ident, ok := n.(*ast.Ident)
		if !ok || ident == nil {
			return true
		}

		if graph.identIgnored(l.pkg, ident.Pos()) {
			return true
		}

		obj := canonicalDeadCodeObject(l.pkg.TypesInfo.Uses[ident])
		if obj != nil {
			key := deadCodeObjectKey(obj)
			if key != "" {
				out[key] = struct{}{}
			}
		}

		return true
	})

	return out
}

func (graph deadCodeGraph) addSelectionUse(
	l *packageLinter,
	out map[string]struct{},
	selector *ast.SelectorExpr,
) {
	if selector == nil {
		return
	}

	selection := l.pkg.TypesInfo.Selections[selector]
	if selection == nil {
		return
	}

	switch obj := selection.Obj().(type) {
	case *types.Func:
		if obj == nil {
			return
		}

		out[deadCodeObjectKey(obj)] = struct{}{}
	case *types.Var:
		if obj == nil || !obj.IsField() {
			return
		}

		key := selectionStructFieldKey(selection)
		if key != "" {
			out[key] = struct{}{}
		}
	}
}

func (graph deadCodeGraph) compositeLitFieldUses(
	l *packageLinter,
	lit *ast.CompositeLit,
) map[string]struct{} {
	if lit == nil {
		return nil
	}

	typ := l.pkg.TypesInfo.TypeOf(lit)
	named := namedDeadCodeType(typ)

	structType := deadCodeStructType(typ)
	if named == nil || structType == nil {
		return nil
	}

	out := make(map[string]struct{})
	positionalIndex := 0

	for _, elt := range lit.Elts {
		if keyValue, ok := elt.(*ast.KeyValueExpr); ok {
			ident, ok := keyValue.Key.(*ast.Ident)
			if ok && ident != nil {
				addStructFieldUse(out, named, ident.Name)
			}

			continue
		}

		if positionalIndex < structType.NumFields() {
			addStructFieldUse(out, named, structType.Field(positionalIndex).Name())
		}

		positionalIndex++
	}

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
	if selection == nil {
		return ""
	}

	obj, ok := selection.Obj().(*types.Var)
	if !ok || obj == nil || !obj.IsField() {
		return ""
	}

	return deadCodeStructFieldKeyFromNamed(selectionStructFieldOwner(selection), obj.Name())
}

func selectionStructFieldOwner(selection *types.Selection) *types.Named {
	if selection == nil {
		return nil
	}

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
