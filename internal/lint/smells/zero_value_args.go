package smells

import (
	"fmt"
	"go/ast"
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

func (l *Runner) checkZeroValuePrivateArgs() {
	funcs := l.zeroValueArgCandidateFuncs()
	if len(funcs) == 0 {
		return
	}

	callStates := l.zeroValueArgCallStates(funcs)

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

func (l *Runner) zeroValueArgCandidateFuncs() map[string]zeroValueArgFunc {
	out := make(map[string]zeroValueArgFunc)

	l.forEachProductionFunc(func(fn *ast.FuncDecl) {
		if !isEligiblePrivateSmellFunc(fn) || fn.Name.Name == initFuncName {
			return
		}

		obj, ok := l.pkg.TypesInfo.ObjectOf(fn.Name).(*types.Func)
		if !ok || obj == nil {
			return
		}

		params := l.zeroValueArgParams(fn)
		if len(params) == 0 {
			return
		}

		out[funcObjectKey(obj)] = zeroValueArgFunc{
			fn:     fn,
			params: params,
		}
	})

	return out
}

func (l *Runner) zeroValueArgParams(fn *ast.FuncDecl) []zeroValueArgParam {
	params := funcParamObjects(l.pkg.TypesInfo, fn.Type.Params)
	if len(params) == 0 {
		return nil
	}

	out := make([]zeroValueArgParam, 0, len(params))
	reportable := false

	for _, param := range params {
		if param == nil {
			return nil
		}

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

func (l *Runner) zeroValueArgCallStates(
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
				l.countZeroValueArgFuncRef(node, funcs, funcRefs)
			case *ast.CallExpr:
				l.recordZeroValueArgCall(node, funcs, states, directCalls)
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

func (l *Runner) countZeroValueArgFuncRef(
	ident *ast.Ident,
	funcs map[string]zeroValueArgFunc,
	funcRefs map[string]int,
) {
	if ident == nil || l.pkg.TypesInfo.Defs[ident] != nil {
		return
	}

	obj, _ := l.pkg.TypesInfo.Uses[ident].(*types.Func)
	if obj == nil {
		return
	}

	key := funcObjectKey(obj)
	if _, ok := funcs[key]; ok {
		funcRefs[key]++
	}
}

func (l *Runner) recordZeroValueArgCall(
	call *ast.CallExpr,
	funcs map[string]zeroValueArgFunc,
	states map[string][]zeroValueArgState,
	directCalls map[string]int,
) {
	_, key, ok := l.calledFunc(call)
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
		display, ok := l.zeroValueArgDisplay(arg, fn.params[idx].typ)
		if !ok {
			states[key][idx].ok = false
			continue
		}

		if !states[key][idx].seen {
			states[key][idx].display = display
			states[key][idx].seen = true
		}
	}
}

func (l *Runner) zeroValueArgDisplay(arg ast.Expr, typ types.Type) (string, bool) {
	if zeroValueScalarArg(l, arg, typ) || zeroValueCompositeArg(l, arg, typ) {
		return l.render(arg), true
	}

	return "", false
}

func zeroValueScalarArg(l *Runner, arg ast.Expr, typ types.Type) bool {
	value, ok := l.scalarOf(arg)
	if !ok {
		return false
	}

	//exhaustive:ignore non-zero scalar kinds cannot prove a zero argument.
	switch value.kind {
	case scalarNil:
		return typeCanBeNil(typ)
	case scalarString:
		return value.text == "" && typeIsString(typ)
	case scalarInt:
		return value.text == "0" && typeIsInteger(typ)
	}

	return false
}

func zeroValueCompositeArg(l *Runner, arg ast.Expr, paramType types.Type) bool {
	lit, ok := l.unparen(arg).(*ast.CompositeLit)
	if !ok || len(lit.Elts) != 0 {
		return false
	}

	litType := l.pkg.TypesInfo.TypeOf(lit)
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

func typeIsString(typ types.Type) bool {
	basic, ok := types.Unalias(typ).Underlying().(*types.Basic)

	return ok && basic.Info()&types.IsString != 0
}

func typeIsInteger(typ types.Type) bool {
	basic, ok := types.Unalias(typ).Underlying().(*types.Basic)

	return ok && basic.Info()&types.IsInteger != 0
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
