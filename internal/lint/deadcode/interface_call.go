package deadcode

import (
	"go/ast"
	"go/constant"
	"go/types"
	"strings"
)

func (graph deadCodeGraph) callInterfaceMethodUses(
	l *packageLinter,
	call *ast.CallExpr,
) map[string]struct{} {
	if call == nil {
		return nil
	}

	out := make(map[string]struct{})
	if target := conversionTargetType(l.pkg.TypesInfo, call); target != nil {
		graph.addInterfaceMethodsForValue(l, out, call.Args[0], target)
	}

	if sig := callSignature(l.pkg.TypesInfo, call); sig != nil {
		for key := range graph.callArgInterfaceMethodUses(l, call, sig) {
			out[key] = struct{}{}
		}

		for key := range graph.knownOptionalInterfaceMethodUses(l, call, sig) {
			out[key] = struct{}{}
		}
	}

	for key := range graph.fmtStringerMethodUses(l, call) {
		out[key] = struct{}{}
	}

	return out
}

func (graph deadCodeGraph) callArgInterfaceMethodUses(
	l *packageLinter,
	call *ast.CallExpr,
	sig *types.Signature,
) map[string]struct{} {
	out := make(map[string]struct{})
	params := sig.Params()

	if params == nil || params.Len() == 0 {
		return out
	}

	for index, arg := range call.Args {
		target, ok := callParamTargetType(sig, index)
		if !ok {
			continue
		}

		graph.addInterfaceMethodsForValue(l, out, arg, target)
	}

	return out
}

func (graph deadCodeGraph) knownOptionalInterfaceMethodUses(
	l *packageLinter,
	call *ast.CallExpr,
	sig *types.Signature,
) map[string]struct{} {
	if !knownOptionalByteReaderCall(calledFunc(l.pkg.TypesInfo, call)) {
		return nil
	}

	out := make(map[string]struct{})

	for index, arg := range call.Args {
		target, ok := callParamTargetType(sig, index)
		if !ok || !namedTypeMatches(target, "io", "Reader") {
			continue
		}

		byteReader := siblingNamedInterface(target, "io", "Reader", "ByteReader")
		if byteReader == nil {
			continue
		}

		graph.addInterfaceMethodsForValue(l, out, arg, byteReader)
	}

	return out
}

func knownOptionalByteReaderCall(fn *types.Func) bool {
	return fn != nil &&
		fn.Pkg() != nil &&
		fn.Pkg().Path() == "github.com/ulikunitz/xz/lzma" &&
		fn.Name() == "NewReader"
}

func siblingNamedInterface(
	typ types.Type,
	pkgPath string,
	sourceName string,
	targetName string,
) types.Type {
	named, ok := namedType(typ)
	if !ok || !namedTypeMatches(named, pkgPath, sourceName) {
		return nil
	}

	obj, _ := named.Obj().Pkg().Scope().Lookup(targetName).(*types.TypeName)
	if obj == nil {
		return nil
	}

	target := obj.Type()
	if _, ok := types.Unalias(target).Underlying().(*types.Interface); !ok {
		return nil
	}

	return target
}

func namedTypeMatches(typ types.Type, pkgPath string, name string) bool {
	named, ok := namedType(typ)
	if !ok || named.Obj() == nil || named.Obj().Pkg() == nil {
		return false
	}

	return named.Obj().Pkg().Path() == pkgPath && named.Obj().Name() == name
}

func namedType(typ types.Type) (*types.Named, bool) {
	if typ == nil {
		return nil, false
	}

	named, ok := types.Unalias(typ).(*types.Named)

	return named, ok
}

func callParamTargetType(sig *types.Signature, argIndex int) (types.Type, bool) {
	params := sig.Params()
	paramIndex := argIndex

	if sig.Variadic() && argIndex >= params.Len()-1 {
		paramIndex = params.Len() - 1
	}

	if paramIndex >= params.Len() {
		return nil, false
	}

	target := params.At(paramIndex).Type()
	if sig.Variadic() && argIndex >= params.Len()-1 {
		if slice, ok := types.Unalias(target).Underlying().(*types.Slice); ok {
			target = slice.Elem()
		}
	}

	return target, true
}

func conversionTargetType(info *types.Info, call *ast.CallExpr) types.Type {
	if call == nil || len(call.Args) != 1 {
		return nil
	}

	if _, ok := call.Fun.(*ast.FuncType); ok {
		return info.TypeOf(call.Fun)
	}

	target := info.TypeOf(call.Fun)
	if target == nil {
		return nil
	}

	if _, ok := types.Unalias(target).Underlying().(*types.Interface); !ok {
		return nil
	}

	return target
}

func callSignature(info *types.Info, call *ast.CallExpr) *types.Signature {
	if call == nil {
		return nil
	}

	typ := info.TypeOf(call.Fun)
	if typ == nil {
		return nil
	}

	if sig, ok := types.Unalias(typ).Underlying().(*types.Signature); ok {
		return sig
	}

	return nil
}

func (graph deadCodeGraph) fmtStringerMethodUses(
	l *packageLinter,
	call *ast.CallExpr,
) map[string]struct{} {
	obj := calledFunc(l.pkg.TypesInfo, call)
	if obj == nil || obj.Pkg() == nil || obj.Pkg().Path() != "fmt" {
		return nil
	}

	argIndexes := fmtStringerArgIndexes(l.pkg.TypesInfo, call, obj.Name())
	if len(argIndexes) == 0 {
		return nil
	}

	out := make(map[string]struct{})

	for argIndex := range argIndexes {
		if argIndex < 0 || argIndex >= len(call.Args) {
			continue
		}

		graph.addStringMethodsForValue(l, out, call.Args[argIndex])
	}

	return out
}

func calledFunc(info *types.Info, call *ast.CallExpr) *types.Func {
	if call == nil {
		return nil
	}

	return calledFuncExpr(info, call.Fun)
}

func calledFuncExpr(info *types.Info, expr ast.Expr) *types.Func {
	switch fun := expr.(type) {
	case *ast.Ident:
		obj, _ := info.Uses[fun].(*types.Func)

		return obj
	case *ast.SelectorExpr:
		if selection := info.Selections[fun]; selection != nil {
			obj, _ := selection.Obj().(*types.Func)

			return obj
		}

		obj, _ := info.Uses[fun.Sel].(*types.Func)

		return obj
	case *ast.IndexExpr:
		return calledFuncExpr(info, fun.X)
	case *ast.IndexListExpr:
		return calledFuncExpr(info, fun.X)
	default:
		return nil
	}
}

func fmtStringerArgIndexes(
	info *types.Info,
	call *ast.CallExpr,
	name string,
) map[int]struct{} {
	if firstArg, ok := fmtUnformattedFirstArg(name); ok {
		return argIndexRange(firstArg, len(call.Args))
	}

	formatIndex, ok := fmtFormatArgIndex(name)
	if !ok || formatIndex >= len(call.Args) {
		return nil
	}

	firstValueArg := formatIndex + 1

	format, ok := constantStringValue(info, call.Args[formatIndex])
	if !ok {
		return argIndexRange(firstValueArg, len(call.Args))
	}

	return fmtStringerFormattedArgIndexes(format, firstValueArg, len(call.Args))
}

func fmtUnformattedFirstArg(name string) (int, bool) {
	switch name {
	case "Append", "Appendln", "Fprint", "Fprintln":
		return 1, true
	case "Print", "Println", "Sprint", "Sprintln":
		return 0, true
	default:
		return 0, false
	}
}

func fmtFormatArgIndex(name string) (int, bool) {
	switch name {
	case "Appendf", "Fprintf":
		return 1, true
	case "Errorf", "Printf", "Sprintf":
		return 0, true
	default:
		return 0, false
	}
}

func constantStringValue(info *types.Info, expr ast.Expr) (string, bool) {
	value := info.Types[expr].Value
	if value == nil || value.Kind() != constant.String {
		return "", false
	}

	return constant.StringVal(value), true
}

func fmtStringerFormattedArgIndexes(
	format string,
	firstArg int,
	argCount int,
) map[int]struct{} {
	if formatHasExplicitIndexes(format) {
		return argIndexRange(firstArg, argCount)
	}

	out := make(map[int]struct{})
	argIndex := firstArg

	for index := 0; index < len(format); {
		verb, sharp, valueArgIndex, nextArgIndex, ok := nextFmtDirective(
			format,
			&index,
			argIndex,
		)
		if !ok {
			index++
			continue
		}

		if fmtVerbUsesStringer(verb, sharp) {
			out[valueArgIndex] = struct{}{}
		}

		argIndex = nextArgIndex
		index++
	}

	return out
}

func nextFmtDirective(
	format string,
	index *int,
	argIndex int,
) (byte, bool, int, int, bool) {
	if format[*index] != '%' {
		return 0, false, argIndex, argIndex, false
	}

	*index++
	if *index >= len(format) || format[*index] == '%' {
		return 0, false, argIndex, argIndex, false
	}

	sharp := false

	for *index < len(format) && strings.ContainsRune("#+-0 ", rune(format[*index])) {
		if format[*index] == '#' {
			sharp = true
		}

		*index++
	}

	*index, argIndex = skipFmtWidthOrPrecision(format, *index, argIndex)
	if *index < len(format) && format[*index] == '.' {
		*index, argIndex = skipFmtWidthOrPrecision(format, *index+1, argIndex)
	}

	if *index >= len(format) {
		return 0, false, argIndex, argIndex, false
	}

	return format[*index], sharp, argIndex, argIndex + 1, true
}

func formatHasExplicitIndexes(format string) bool {
	for i := range len(format) - 1 {
		if format[i] == '%' && format[i+1] == '[' {
			return true
		}
	}

	return false
}

func skipFmtWidthOrPrecision(format string, index int, argIndex int) (int, int) {
	if index >= len(format) {
		return index, argIndex
	}

	if format[index] == '*' {
		return index + 1, argIndex + 1
	}

	for index < len(format) && format[index] >= '0' && format[index] <= '9' {
		index++
	}

	return index, argIndex
}

func fmtVerbUsesStringer(verb byte, sharp bool) bool {
	switch verb {
	case 's', 'q', 'x', 'X':
		return true
	case 'v':
		return !sharp
	default:
		return false
	}
}

func argIndexRange(firstArg int, argCount int) map[int]struct{} {
	out := make(map[int]struct{})
	for index := firstArg; index < argCount; index++ {
		out[index] = struct{}{}
	}

	return out
}

func (graph deadCodeGraph) addStringMethodsForValue(
	l *packageLinter,
	out map[string]struct{},
	expr ast.Expr,
) {
	typ := l.pkg.TypesInfo.TypeOf(expr)
	if tuple, ok := typ.(*types.Tuple); ok {
		for index := range tuple.Len() {
			graph.addStringMethodsForType(l, out, tuple.At(index).Type())
		}

		return
	}

	graph.addStringMethodsForType(l, out, typ)
}

func (graph deadCodeGraph) addStringMethodsForType(
	l *packageLinter,
	out map[string]struct{},
	typ types.Type,
) {
	if typ == nil {
		return
	}

	if typeIsInterface(typ) {
		for _, receiver := range graph.candidateReceiverTypes() {
			if types.AssignableTo(receiver, typ) {
				addStringMethodForType(l, out, receiver)
			}
		}

		return
	}

	addStringMethodForType(l, out, typ)
}

func addStringMethodForType(
	l *packageLinter,
	out map[string]struct{},
	typ types.Type,
) {
	obj, _, _ := types.LookupFieldOrMethod(typ, false, l.pkg.TypesPkg, "String")

	fn, ok := obj.(*types.Func)
	if !ok || fn == nil || !stringMethodSignature(fn) {
		return
	}

	out[deadCodeObjectKey(fn)] = struct{}{}
}

func stringMethodSignature(fn *types.Func) bool {
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig == nil || tupleLen(sig.Params()) != 0 || tupleLen(sig.Results()) != 1 {
		return false
	}

	result, ok := types.Unalias(sig.Results().At(0).Type()).Underlying().(*types.Basic)

	return ok && result.Kind() == types.String
}
