package deadcode

import (
	"go/ast"
	"go/token"
	"go/types"
)

func (l *packageLinter) reflectedStructFieldOwners(
	named *types.Named,
	codec reflectedCodecUse,
	call *ast.CallExpr,
	mode reflectedStructFieldUseMode,
) []*types.Named {
	if named == nil {
		return nil
	}

	out := []*types.Named{named}
	if target := l.reflectedAliasTargetNamed(
		named,
		codec,
		call,
		mode,
	); target != nil &&
		target != named {
		out = append(out, target)
	}

	if target := l.reflectedAliasSourceNamedFromConversion(named, call); target != nil &&
		target != named {
		out = append(out, target)
	}

	return out
}

func (l *packageLinter) reflectedAliasTargetNamed(
	named *types.Named,
	codec reflectedCodecUse,
	call *ast.CallExpr,
	mode reflectedStructFieldUseMode,
) *types.Named {
	if l == nil || l.pkg == nil || named == nil || named.Obj() == nil || call == nil {
		return nil
	}

	for _, methodName := range reflectedHookNames(codec.hookTag, mode, reflectedValueHook) {
		fn := l.reflectedAliasContextFunc(methodName, call)

		receiver := l.reflectedCustomHookReceiver(fn)
		if receiver == nil {
			continue
		}

		target := l.reflectedAliasTargetNamedForFunc(named, fn)
		if sameNamedType(receiver, target) {
			return target
		}
	}

	return nil
}

func (l *packageLinter) reflectedAliasSourceNamedFromConversion(
	named *types.Named,
	call *ast.CallExpr,
) *types.Named {
	if call == nil {
		return nil
	}

	target := l.reflectedAliasTargetNamedFromCallContext(named, call)
	if target == nil {
		return nil
	}

	var source *types.Named

	ast.Inspect(call, func(n ast.Node) bool {
		conv, ok := n.(*ast.CallExpr)
		if !ok || len(conv.Args) != 1 {
			return true
		}

		if !sameNamedType(namedDeadCodeType(l.pkg.TypesInfo.TypeOf(conv)), named) {
			return true
		}

		candidate := namedDeadCodeType(reflectedValueType(l.pkg.TypesInfo, conv.Args[0]))
		if sameNamedType(candidate, target) {
			source = candidate
		}

		return source == nil
	})

	return source
}

func (l *packageLinter) reflectedAliasTargetNamedFromCallContext(
	named *types.Named,
	call *ast.CallExpr,
) *types.Named {
	if fn := l.deadCodeCallContextFunc(call); fn != nil {
		if target := l.reflectedAliasTargetNamedFromNode(named, fn.Body); target != nil {
			return target
		}
	}

	return l.reflectedAliasTargetNamedFromPackage(named)
}

func (l *packageLinter) deadCodeCallContextFunc(call *ast.CallExpr) *ast.FuncDecl {
	if call == nil {
		return nil
	}

	for _, fn := range l.pkg.ProductionFuncs {
		if nodeContainsPos(fn.Body, call.Pos()) {
			return fn
		}
	}

	return nil
}

func (l *packageLinter) reflectedAliasTargetNamedForFunc(
	named *types.Named,
	fn *ast.FuncDecl,
) *types.Named {
	if target := l.reflectedAliasTargetNamedFromNode(named, fn.Body); target != nil {
		return target
	}

	return l.reflectedAliasTargetNamedFromPackage(named)
}

func (l *packageLinter) reflectedAliasContextFunc(
	methodName string,
	call *ast.CallExpr,
) *ast.FuncDecl {
	if methodName == "" || call == nil {
		return nil
	}

	for _, decl := range l.pkg.ProductionDecls {
		fn, _ := decl.(*ast.FuncDecl)
		if reflectedFuncDeclContainsCall(fn, methodName, call) {
			return fn
		}
	}

	return nil
}

func reflectedFuncDeclContainsCall(
	fn *ast.FuncDecl,
	methodName string,
	call *ast.CallExpr,
) bool {
	return fn != nil &&
		fn.Name != nil &&
		fn.Name.Name == methodName &&
		nodeContainsPos(fn.Body, call.Pos())
}

func (l *packageLinter) reflectedCustomHookReceiver(fn *ast.FuncDecl) *types.Named {
	if fn == nil || fn.Recv == nil || len(fn.Recv.List) == 0 {
		return nil
	}

	return namedDeadCodeType(l.pkg.TypesInfo.TypeOf(fn.Recv.List[0].Type))
}

func sameNamedType(left *types.Named, right *types.Named) bool {
	if left == nil || right == nil || left.Obj() == nil || right.Obj() == nil {
		return false
	}

	return deadCodeObjectKey(left.Obj()) == deadCodeObjectKey(right.Obj())
}

func nodeContainsPos(node ast.Node, pos token.Pos) bool {
	return node != nil && pos >= node.Pos() && pos <= node.End()
}

func (l *packageLinter) reflectedAliasTargetNamedFromNode(
	named *types.Named,
	node ast.Node,
) *types.Named {
	var target *types.Named

	ast.Inspect(node, func(n ast.Node) bool {
		typeSpec, ok := n.(*ast.TypeSpec)
		if !ok {
			return true
		}

		target = l.reflectedAliasTargetNamedFromSpec(named, typeSpec)

		return target == nil
	})

	return target
}

func (l *packageLinter) reflectedAliasTargetNamedFromPackage(
	named *types.Named,
) *types.Named {
	for _, decl := range l.pkg.ProductionDecls {
		general, _ := decl.(*ast.GenDecl)
		if general == nil {
			continue
		}

		for _, spec := range general.Specs {
			typeSpec, _ := spec.(*ast.TypeSpec)
			if target := l.reflectedAliasTargetNamedFromSpec(named, typeSpec); target != nil {
				return target
			}
		}
	}

	return nil
}

func (l *packageLinter) reflectedAliasTargetNamedFromSpec(
	named *types.Named,
	typeSpec *ast.TypeSpec,
) *types.Named {
	if typeSpec == nil {
		return nil
	}

	obj, _ := l.pkg.TypesInfo.Defs[typeSpec.Name].(*types.TypeName)
	if obj == nil || obj.Type() != named {
		return nil
	}

	target := namedDeadCodeType(l.pkg.TypesInfo.TypeOf(typeSpec.Type))
	if target != nil && deadCodeStructType(target) != nil {
		return target
	}

	return nil
}
