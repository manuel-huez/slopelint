package lint

import (
	"fmt"
	"go/ast"
	"go/types"
	"strconv"
	"strings"
)

const contractPrefix = "defenselint:ensures"
const nilText = "nil"

type guardContract struct {
	target contractTarget
	value  scalar
	wantEq bool
}

type contractTarget struct {
	param int
	recv  bool
	path  []string
}

type contractBinding struct {
	param int
	recv  bool
}

func (l *linter) collectContracts() {
	l.contracts = make(map[string][]guardContract)

	for _, file := range l.pkg.Files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Doc == nil {
				continue
			}

			obj, ok := l.pkg.TypesInfo.Defs[fn.Name].(*types.Func)
			if !ok || obj == nil {
				continue
			}

			bindings := contractBindingsForFunc(fn)
			if len(bindings) == 0 {
				continue
			}

			key := funcObjectKey(obj)

			for _, comment := range fn.Doc.List {
				contract, ok := parseGuardContract(comment.Text, bindings)
				if !ok {
					continue
				}

				l.contracts[key] = append(l.contracts[key], contract)
			}
		}
	}
}

func contractBindingsForFunc(fn *ast.FuncDecl) map[string]contractBinding {
	out := make(map[string]contractBinding)

	if fn.Recv != nil {
		for _, field := range fn.Recv.List {
			for _, name := range field.Names {
				out[name.Name] = contractBinding{recv: true}
			}
		}
	}

	paramIndex := 0

	if fn.Type.Params != nil {
		for _, field := range fn.Type.Params.List {
			for _, name := range field.Names {
				out[name.Name] = contractBinding{param: paramIndex}
				paramIndex++
			}
		}
	}

	return out
}

func parseGuardContract(text string, bindings map[string]contractBinding) (guardContract, bool) {
	body, ok := trimContractPrefix(text)
	if !ok {
		return guardContract{}, false
	}

	left, right, wantEq, ok := splitContractBody(body)
	if !ok {
		return guardContract{}, false
	}

	parts := strings.Split(left, ".")
	if len(parts) == 0 || parts[0] == "" {
		return guardContract{}, false
	}

	binding, ok := bindings[parts[0]]
	if !ok {
		return guardContract{}, false
	}

	value, ok := parseContractScalar(right)
	if !ok {
		return guardContract{}, false
	}

	return guardContract{
		target: contractTarget{
			param: binding.param,
			recv:  binding.recv,
			path:  append([]string(nil), parts[1:]...),
		},
		value:  value,
		wantEq: wantEq,
	}, true
}

func trimContractPrefix(text string) (string, bool) {
	for _, prefix := range []string{"//", "/*"} {
		if trimmed, ok := strings.CutPrefix(text, prefix); ok {
			text = trimmed
		}
	}

	text = strings.TrimSpace(strings.TrimSuffix(text, "*/"))
	if !strings.HasPrefix(text, contractPrefix) {
		return "", false
	}

	return strings.TrimSpace(strings.TrimPrefix(text, contractPrefix)), true
}

func splitContractBody(body string) (string, string, bool, bool) {
	for _, op := range []struct {
		text   string
		wantEq bool
	}{
		{text: "!=", wantEq: false},
		{text: "==", wantEq: true},
	} {
		idx := strings.Index(body, op.text)
		if idx < 0 {
			continue
		}

		left := strings.TrimSpace(body[:idx])

		right := strings.TrimSpace(body[idx+len(op.text):])
		if left == "" || right == "" {
			return "", "", false, false
		}

		return left, right, op.wantEq, true
	}

	return "", "", false, false
}

func parseContractScalar(text string) (scalar, bool) {
	switch text {
	case nilText:
		return scalar{kind: scalarNil, text: nilText}, true
	case "true", "false":
		return scalar{kind: scalarBool, text: text}, true
	}

	if unquoted, err := strconv.Unquote(text); err == nil {
		return scalar{kind: scalarString, text: unquoted}, true
	}

	if _, err := strconv.ParseInt(text, 10, 64); err == nil {
		return scalar{kind: scalarInt, text: text}, true
	}

	return scalar{}, false
}

func funcObjectKey(obj *types.Func) string {
	pkgPath := ""
	if obj.Pkg() != nil {
		pkgPath = obj.Pkg().Path()
	}

	return fmt.Sprintf("%s:%s:%d", pkgPath, obj.Name(), obj.Pos())
}

func (l *linter) applyCallContracts(st state, call *ast.CallExpr) []state {
	key, ok := l.calledFuncKey(call)
	if !ok {
		return []state{st}
	}

	contracts := l.contracts[key]
	if len(contracts) == 0 {
		return []state{st}
	}

	current := []state{st}
	for _, contract := range contracts {
		nextStates := make([]state, 0, len(current))
		for _, currentState := range current {
			sym, ok := l.symbolForContractTarget(call, contract.target)
			if !ok {
				nextStates = append(nextStates, currentState)
				continue
			}

			ev := evidence{
				pos:  call.Pos(),
				text: l.contractEvidenceText(sym, contract, call),
			}

			var (
				next state
				ok2  bool
			)

			if contract.wantEq {
				next, ok2 = l.setExact(currentState, sym, contract.value, ev)
			} else {
				next, ok2 = l.addNot(currentState, sym, contract.value, ev)
			}

			if ok2 {
				nextStates = append(nextStates, next)
			}
		}

		current = l.normalizeStates(nextStates)
		if len(current) == 0 {
			return nil
		}
	}

	return current
}

func (l *linter) contractEvidenceText(
	sym symbol,
	contract guardContract,
	call *ast.CallExpr,
) string {
	op := "!="
	if contract.wantEq {
		op = "=="
	}

	return fmt.Sprintf(
		"%s ensures %s %s %s",
		l.render(call.Fun),
		sym.name,
		op,
		contract.value.String(),
	)
}

func (l *linter) calledFuncKey(call *ast.CallExpr) (string, bool) {
	switch fun := l.unparen(call.Fun).(type) {
	case *ast.Ident:
		obj, ok := l.pkg.TypesInfo.ObjectOf(fun).(*types.Func)
		if !ok || obj == nil {
			return "", false
		}

		return funcObjectKey(obj), true
	case *ast.SelectorExpr:
		if sel := l.pkg.TypesInfo.Selections[fun]; sel != nil {
			if obj, ok := sel.Obj().(*types.Func); ok && obj != nil {
				return funcObjectKey(obj), true
			}
		}

		obj, ok := l.pkg.TypesInfo.ObjectOf(fun.Sel).(*types.Func)
		if !ok || obj == nil {
			return "", false
		}

		return funcObjectKey(obj), true
	default:
		return "", false
	}
}

func (l *linter) symbolForContractTarget(call *ast.CallExpr, target contractTarget) (symbol, bool) {
	var baseExpr ast.Expr

	if target.recv {
		sel, ok := l.unparen(call.Fun).(*ast.SelectorExpr)
		if !ok {
			return symbol{}, false
		}

		baseExpr = sel.X
	} else {
		if target.param < 0 || target.param >= len(call.Args) {
			return symbol{}, false
		}

		baseExpr = call.Args[target.param]
	}

	base, ok := l.symbolOf(baseExpr)
	if !ok {
		return symbol{}, false
	}

	return l.symbolForPath(base, target.path)
}

func (l *linter) symbolForPath(base symbol, path []string) (symbol, bool) {
	current := base
	for _, field := range path {
		fieldType, ok := l.lookupFieldType(current.typ, field)
		if !ok {
			return symbol{}, false
		}

		current = current.child(field, fieldType)
	}

	return current, true
}

func (l *linter) lookupFieldType(t types.Type, name string) (types.Type, bool) {
	obj, _, _ := types.LookupFieldOrMethod(t, true, l.pkg.TypesPkg, name)

	field, ok := obj.(*types.Var)
	if !ok || !field.IsField() {
		return nil, false
	}

	return field.Type(), true
}
