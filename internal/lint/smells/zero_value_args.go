package smells

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"strconv"
)

type zeroValueArgParam struct {
	name   string
	pos    token.Pos
	typ    types.Type
	report bool
}

type zeroValueArgFunc struct {
	fn     *ast.FuncDecl
	params []zeroValueArgParam
}

type zeroValueArgState struct {
	display string
	ok      bool
	seen    bool
}

// zeroValueArgCheck reports private function parameters that every call site
// fills with the same zero value.
type zeroValueArgCheck struct {
	ruleEnv
}

func (l zeroValueArgCheck) check() {
	funcs := l.candidateFuncs()
	if len(funcs) == 0 {
		return
	}

	callStates := l.callStates(funcs)

	for key, fn := range funcs {
		for idx, state := range callStates[key] {
			if !state.ok || !state.seen {
				continue
			}

			param := fn.params[idx]
			if !param.report {
				continue
			}

			l.report(
				param.pos,
				"api_overkill",
				fmt.Sprintf(
					`private function %q parameter %q is always called with zero value %q; remove the parameter or pass real variation`,
					fn.fn.Name.Name,
					zeroValueArgParamName(param.name, idx),
					state.display,
				),
			)
		}
	}
}

func (l zeroValueArgCheck) candidateFuncs() map[string]zeroValueArgFunc {
	out := make(map[string]zeroValueArgFunc)

	for _, fn := range l.pkg.ProductionFuncs {
		if fn.Recv != nil || fn.Doc != nil {
			continue
		}

		obj, ok := privateFuncObject(l.pkg.TypesInfo, fn)
		if !ok {
			continue
		}

		if params := zeroValueArgParams(l.pkg.TypesInfo, fn); len(params) != 0 {
			out[funcObjectKey(obj)] = zeroValueArgFunc{fn: fn, params: params}
		}
	}

	return out
}

func zeroValueArgParams(info *types.Info, fn *ast.FuncDecl) []zeroValueArgParam {
	params := funcParamObjects(info, fn.Type.Params)
	if len(params) == 0 {
		return nil
	}

	out := make([]zeroValueArgParam, 0, len(params))
	reportable := false

	for _, param := range params {
		report := !isBoolType(param.Type())
		reportable = reportable || report

		out = append(out, zeroValueArgParam{
			name:   param.Name(),
			pos:    param.Pos(),
			typ:    param.Type(),
			report: report,
		})
	}

	if !reportable {
		return nil
	}

	return out
}

func (l zeroValueArgCheck) callStates(
	funcs map[string]zeroValueArgFunc,
) map[string][]zeroValueArgState {
	states := make(map[string][]zeroValueArgState, len(funcs))
	directCalls := make(map[string]int, len(funcs))
	funcRefs := make(map[string]int, len(funcs))

	for key, fn := range funcs {
		paramStates := make([]zeroValueArgState, len(fn.params))
		for idx := range paramStates {
			paramStates[idx].ok = true
		}

		states[key] = paramStates
	}

	for _, file := range l.pkg.ProductionFiles {
		ast.Inspect(file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.Ident:
				countZeroValueArgFuncRef(l.pkg.TypesInfo, node, funcs, funcRefs)
			case *ast.CallExpr:
				l.recordCall(node, funcs, states, directCalls)
			}

			return true
		})
	}

	for key, fn := range funcs {
		if directCalls[key] == 0 || funcRefs[key] != directCalls[key] {
			for idx := range fn.params {
				states[key][idx].ok = false
			}
		}
	}

	return states
}

func countZeroValueArgFuncRef(
	info *types.Info,
	ident *ast.Ident,
	funcs map[string]zeroValueArgFunc,
	funcRefs map[string]int,
) {
	if info.Defs[ident] != nil {
		return
	}

	obj, _ := info.Uses[ident].(*types.Func)
	if obj == nil {
		return
	}

	key := funcObjectKey(obj)
	if _, ok := funcs[key]; ok {
		funcRefs[key]++
	}
}

func (l zeroValueArgCheck) recordCall(
	call *ast.CallExpr,
	funcs map[string]zeroValueArgFunc,
	states map[string][]zeroValueArgState,
	directCalls map[string]int,
) {
	_, key, ok := calledFunc(l.pkg.TypesInfo, call)
	if !ok {
		return
	}

	fn, ok := funcs[key]
	if !ok {
		return
	}

	directCalls[key]++

	if len(call.Args) != len(fn.params) || call.Ellipsis != token.NoPos {
		for idx := range fn.params {
			states[key][idx].ok = false
		}

		return
	}

	for idx, arg := range call.Args {
		typ := fn.params[idx].typ
		if !zeroValueScalarArg(l.pkg.TypesInfo, arg, typ) &&
			!zeroValueCompositeArg(l.pkg.TypesInfo, arg, typ) {
			states[key][idx].ok = false
			continue
		}

		if !states[key][idx].seen {
			states[key][idx].display = l.render(arg)
			states[key][idx].seen = true
		}
	}
}

// zeroValueScalarArg reports nil, "" or 0 passed for a parameter that holds
// that zero value. runtime.GOOS and GOARCH differ per build, so an argument
// that uses them is not fixed.
func zeroValueScalarArg(info *types.Info, arg ast.Expr, typ types.Type) bool {
	arg = ast.Unparen(arg)

	if ident, ok := arg.(*ast.Ident); ok && ident.Name == nilText {
		return typeCanBeNil(typ)
	}

	if runtimeTargetConstIn(info, arg) {
		return false
	}

	tv, ok := info.Types[arg]
	if !ok {
		return false
	}

	if tv.IsNil() {
		return typeCanBeNil(typ)
	}

	return tv.Value != nil && isZeroConstantOf(tv.Value, typ)
}

// isZeroConstantOf reports "" for a string type or 0 for an integer type.
func isZeroConstantOf(value constant.Value, typ types.Type) bool {
	basic, ok := types.Unalias(typ).Underlying().(*types.Basic)
	if !ok {
		return false
	}

	switch value.Kind() {
	case constant.String:
		return constant.StringVal(value) == "" && basic.Info()&types.IsString != 0
	case constant.Int:
		return value.ExactString() == "0" && basic.Info()&types.IsInteger != 0
	case constant.Unknown, constant.Bool, constant.Float, constant.Complex:
		return false
	}

	return false
}

func runtimeTargetConstIn(info *types.Info, expr ast.Expr) bool {
	found := false

	ast.Inspect(expr, func(n ast.Node) bool {
		if found || n == nil {
			return false
		}

		if child, ok := n.(ast.Expr); ok && isRuntimeTargetConst(info, ast.Unparen(child)) {
			found = true
		}

		return !found
	})

	return found
}

func isRuntimeTargetConst(info *types.Info, expr ast.Expr) bool {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}

	constObj, _ := info.Uses[selector.Sel].(*types.Const)

	return constObj != nil &&
		constObj.Pkg() != nil &&
		constObj.Pkg().Path() == "runtime" &&
		(constObj.Name() == "GOOS" || constObj.Name() == "GOARCH")
}

func zeroValueCompositeArg(info *types.Info, arg ast.Expr, paramType types.Type) bool {
	lit, ok := ast.Unparen(arg).(*ast.CompositeLit)
	if !ok || len(lit.Elts) != 0 {
		return false
	}

	litType := info.TypeOf(lit)
	if litType == nil || paramType == nil || !types.AssignableTo(litType, paramType) {
		return false
	}

	switch types.Unalias(litType).Underlying().(type) {
	case *types.Array, *types.Struct:
		return true
	default:
		return false
	}
}

func typeCanBeNil(typ types.Type) bool {
	if typ == nil {
		return false
	}

	switch types.Unalias(typ).Underlying().(type) {
	case *types.Chan,
		*types.Interface,
		*types.Map,
		*types.Pointer,
		*types.Signature,
		*types.Slice:
		return true
	default:
		return false
	}
}

func zeroValueArgParamName(name string, idx int) string {
	if name != "" {
		return name
	}

	return "#" + strconv.Itoa(idx+1)
}
