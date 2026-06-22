package deadcode

import (
	"go/ast"
	"go/types"
)

type reflectedWrapperKind uint8

const (
	reflectedWrapperDecode reflectedWrapperKind = iota
	reflectedWrapperMarshal
)

type reflectedWrapperArgUse struct {
	index  int
	codec  reflectedCodecUse
	mapKey bool
}

type reflectedWrapperSummary struct {
	uses      []reflectedWrapperArgUse
	inspected bool
}

func (graph deadCodeGraph) reflectedWrapperDecodeTargetTypes(
	l *packageLinter,
	fn *types.Func,
	call *ast.CallExpr,
) []reflectedDecodeTarget {
	uses, inspected := graph.reflectedWrapperArgUses(fn, reflectedWrapperDecode)
	if !inspected || len(uses) == 0 {
		return nil
	}

	out := make([]reflectedDecodeTarget, 0, len(uses))
	for _, use := range uses {
		if use.index >= len(call.Args) {
			continue
		}

		typ := reflectedValueType(l.pkg.TypesInfo, call.Args[use.index])
		if !use.mapKey && !reflectedDecodeTargetType(typ) {
			continue
		}

		out = append(out, reflectedDecodeTarget{
			typ:    typ,
			codec:  use.codec,
			mapKey: use.mapKey,
		})
	}

	return out
}

func (graph deadCodeGraph) reflectedWrapperMarshalTargetTypes(
	l *packageLinter,
	fn *types.Func,
	call *ast.CallExpr,
) []reflectedMarshalTarget {
	uses, inspected := graph.reflectedWrapperArgUses(fn, reflectedWrapperMarshal)
	if !inspected || len(uses) == 0 {
		return nil
	}

	out := make([]reflectedMarshalTarget, 0, len(uses))
	for _, use := range uses {
		if use.index >= len(call.Args) {
			continue
		}

		out = append(out, reflectedMarshalTarget{
			typ:         reflectedValueType(l.pkg.TypesInfo, call.Args[use.index]),
			codec:       use.codec,
			addressable: reflectedMarshalUnaddressable,
		})
	}

	return out
}

func (graph deadCodeGraph) reflectedWrapperArgUses(
	fn *types.Func,
	kind reflectedWrapperKind,
) ([]reflectedWrapperArgUse, bool) {
	uses, inspected, _ := graph.reflectedWrapperArgUsesSeen(
		fn,
		make(map[string]struct{}),
		kind,
	)

	return uses, inspected
}

func (graph deadCodeGraph) reflectedWrapperArgUsesSeen(
	fn *types.Func,
	funcsSeen map[string]struct{},
	kind reflectedWrapperKind,
) ([]reflectedWrapperArgUse, bool, bool) {
	if fn == nil {
		return nil, false, true
	}

	key := deadCodeObjectKey(fn)
	cache := graph.reflectedWrapperCache(kind)

	if key != "" {
		if cached, ok := cache[key]; ok {
			return cached.uses, cached.inspected, true
		}

		if _, ok := funcsSeen[key]; ok {
			return nil, true, false
		}
	}

	pkg, decl, paramIndexes, ok := graph.reflectedWrapperFunc(fn)
	if !ok {
		graph.cacheReflectedWrapperMiss(key, cache)

		return nil, false, true
	}

	if key != "" {
		funcsSeen[key] = struct{}{}
		defer delete(funcsSeen, key)
	}

	out := make([]reflectedWrapperArgUse, 0)
	complete := true

	inspectReflectedCalls(decl.Body, func(call *ast.CallExpr) {
		if !graph.collectReflectedWrapperCall(
			pkg,
			call,
			paramIndexes,
			funcsSeen,
			kind,
			&out,
		) {
			complete = false
		}
	})

	out = dedupeComparable(out, reflectedDedupeMinLen)
	if complete {
		graph.cacheReflectedWrapperUses(key, cache, out)
	}

	return out, true, complete
}

func (graph deadCodeGraph) reflectedWrapperCache(
	kind reflectedWrapperKind,
) map[string]reflectedWrapperSummary {
	switch kind {
	case reflectedWrapperDecode:
		return graph.reflectedWrapperDecodeCache
	case reflectedWrapperMarshal:
		return graph.reflectedWrapperMarshalCache
	}

	return nil
}

func (graph deadCodeGraph) cacheReflectedWrapperMiss(
	key string,
	cache map[string]reflectedWrapperSummary,
) {
	if key == "" || cache == nil {
		return
	}

	cache[key] = reflectedWrapperSummary{}
}

func (graph deadCodeGraph) cacheReflectedWrapperUses(
	key string,
	cache map[string]reflectedWrapperSummary,
	uses []reflectedWrapperArgUse,
) {
	if key == "" || cache == nil {
		return
	}

	cache[key] = reflectedWrapperSummary{
		uses:      uses,
		inspected: true,
	}
}

func (graph deadCodeGraph) reflectedWrapperFunc(
	fn *types.Func,
) (*Package, *ast.FuncDecl, map[types.Object]int, bool) {
	pkg := graph.packageForFunc(fn)
	if pkg == nil {
		return nil, nil, nil, false
	}

	decl := graph.funcDeclForObject(pkg, fn)
	if decl == nil || decl.Body == nil {
		return nil, nil, nil, false
	}

	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig == nil {
		return nil, nil, nil, false
	}

	paramIndexes := sourceFuncParamObjectIndexes(pkg.TypesInfo, sig, decl)
	if len(paramIndexes) == 0 {
		return nil, nil, nil, false
	}

	return pkg, decl, paramIndexes, true
}

func (graph deadCodeGraph) collectReflectedWrapperCall(
	pkg *Package,
	call *ast.CallExpr,
	paramIndexes map[types.Object]int,
	funcsSeen map[string]struct{},
	kind reflectedWrapperKind,
	out *[]reflectedWrapperArgUse,
) bool {
	if graph.collectDirectReflectedWrapperCall(pkg, call, paramIndexes, kind, out) {
		return true
	}

	return graph.collectDelegatedReflectedWrapperCall(
		pkg,
		call,
		paramIndexes,
		funcsSeen,
		kind,
		out,
	)
}

func (graph deadCodeGraph) collectDirectReflectedWrapperCall(
	pkg *Package,
	call *ast.CallExpr,
	paramIndexes map[types.Object]int,
	kind reflectedWrapperKind,
	out *[]reflectedWrapperArgUse,
) bool {
	switch kind {
	case reflectedWrapperDecode:
		codecFn, codec, ok := reflectedTargetCall(pkg, call, reflectedDecodeFuncCodec)
		if !ok {
			return false
		}

		argIndex := reflectedDecodeTargetArgIndex(codecFn, call)
		if argIndex < 0 || argIndex >= len(call.Args) {
			return true
		}

		target := call.Args[argIndex]
		if index, ok := reflectedWrapperParamIndex(pkg, target, paramIndexes); ok {
			*out = append(*out, reflectedWrapperArgUse{index: index, codec: codec})
		}
	case reflectedWrapperMarshal:
		codecFn, codec, ok := reflectedTargetCall(pkg, call, reflectedMarshalFuncCodec)
		if !ok {
			return false
		}

		argIndex := reflectedMarshalTargetArgIndex(codecFn, call)
		if argIndex < 0 || argIndex >= len(call.Args) {
			return true
		}

		if index, ok := reflectedWrapperParamIndex(pkg, call.Args[argIndex], paramIndexes); ok {
			*out = append(*out, reflectedWrapperArgUse{index: index, codec: codec})
		}
	}

	return true
}

func (graph deadCodeGraph) collectDelegatedReflectedWrapperCall(
	pkg *Package,
	call *ast.CallExpr,
	paramIndexes map[types.Object]int,
	funcsSeen map[string]struct{},
	kind reflectedWrapperKind,
	out *[]reflectedWrapperArgUse,
) bool {
	if !reflectedWrapperCallPassesParam(pkg, call, paramIndexes) {
		return true
	}

	callee := calledFunc(pkg.TypesInfo, call)

	uses, inspected, complete := graph.reflectedWrapperArgUsesSeen(callee, funcsSeen, kind)
	if !inspected {
		return true
	}

	for _, use := range uses {
		if use.index >= len(call.Args) {
			continue
		}

		if index, ok := reflectedWrapperParamIndex(
			pkg,
			call.Args[use.index],
			paramIndexes,
		); ok {
			*out = append(*out, reflectedWrapperArgUse{
				index:  index,
				codec:  use.codec,
				mapKey: use.mapKey,
			})
		}
	}

	return complete
}

func reflectedWrapperCallPassesParam(
	pkg *Package,
	call *ast.CallExpr,
	paramIndexes map[types.Object]int,
) bool {
	for _, arg := range call.Args {
		if _, ok := reflectedWrapperParamIndex(pkg, arg, paramIndexes); ok {
			return true
		}
	}

	return false
}

func reflectedWrapperParamIndex(
	pkg *Package,
	expr ast.Expr,
	paramIndexes map[types.Object]int,
) (int, bool) {
	switch expr := unparenReflectedExpr(expr).(type) {
	case *ast.Ident:
		index, ok := paramIndexes[pkg.TypesInfo.ObjectOf(expr)]

		return index, ok
	case *ast.CallExpr:
		if len(expr.Args) == 1 &&
			conversionTargetType(pkg.TypesInfo, expr) != nil &&
			typeIsInterface(pkg.TypesInfo.TypeOf(expr)) {
			return reflectedWrapperParamIndex(pkg, expr.Args[0], paramIndexes)
		}
	}

	return 0, false
}
