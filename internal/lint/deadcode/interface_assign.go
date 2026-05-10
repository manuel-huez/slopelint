package deadcode

import (
	"go/ast"
	"go/types"
)

func (graph deadCodeGraph) addInterfaceMethodsForValueTargets(
	l *packageLinter,
	out map[string]struct{},
	expr ast.Expr,
	targets []types.Type,
) {
	if tuple, ok := l.pkg.TypesInfo.TypeOf(expr).(*types.Tuple); ok {
		for index, target := range targets {
			if index >= tuple.Len() {
				continue
			}

			graph.addInterfaceMethodsForType(l, out, tuple.At(index).Type(), target)
		}

		return
	}

	for _, target := range targets {
		graph.addInterfaceMethodsForValue(l, out, expr, target)
	}
}

func (graph deadCodeGraph) addInterfaceMethodsForValue(
	l *packageLinter,
	out map[string]struct{},
	expr ast.Expr,
	target types.Type,
) {
	for key := range graph.interfaceMethodsForValue(l, expr, target) {
		out[key] = struct{}{}
	}
}

func (graph deadCodeGraph) interfaceMethodsForValue(
	l *packageLinter,
	expr ast.Expr,
	target types.Type,
) map[string]struct{} {
	out := make(map[string]struct{})
	graph.addInterfaceMethodsForType(l, out, l.pkg.TypesInfo.TypeOf(expr), target)

	return out
}

func (graph deadCodeGraph) addInterfaceMethodsForType(
	l *packageLinter,
	out map[string]struct{},
	source types.Type,
	target types.Type,
) {
	if source == nil || target == nil || typeIsInterface(source) {
		return
	}

	iface, ok := types.Unalias(target).Underlying().(*types.Interface)
	if !ok || iface.NumMethods() == 0 || !types.AssignableTo(source, target) {
		return
	}

	for index := range iface.NumMethods() {
		method := iface.Method(index)
		if method == nil {
			continue
		}

		obj, _, _ := types.LookupFieldOrMethod(source, true, l.pkg.TypesPkg, method.Name())

		fn, ok := obj.(*types.Func)
		if !ok || fn == nil {
			continue
		}

		out[deadCodeObjectKey(fn)] = struct{}{}
	}
}

func typeIsInterface(typ types.Type) bool {
	if typ == nil {
		return false
	}

	_, ok := types.Unalias(typ).Underlying().(*types.Interface)

	return ok
}
