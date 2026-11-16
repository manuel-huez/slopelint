package lint

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"strconv"
	"strings"
)

const contractPrefix = "slopelint:ensures"
const nilText = "nil"

type guardContract struct {
	target contractTarget
	value  scalar
	wantEq bool
}

type contractTarget struct {
	param    int
	recv     bool
	variadic bool
	path     []string
}

type contractBinding struct {
	param    int
	recv     bool
	variadic bool
}

func collectContracts(pkg *LoadedPackage, sink *issueSink) map[string][]guardContract {
	explicit := make(map[string][]guardContract)

	for _, file := range pkg.Files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Doc == nil {
				continue
			}

			key := funcObjectKey(pkg.TypesInfo.ObjectOf(fn.Name).(*types.Func))
			bindings := contractBindingsForFunc(fn)

			for _, comment := range fn.Doc.List {
				if contract, ok := parseGuardContract(comment.Text, bindings); ok {
					explicit[key] = append(explicit[key], contract)
					continue
				}

				if _, prefixed := trimContractPrefix(comment.Text); prefixed {
					sink.report(
						comment.Pos(),
						"invalid_contract",
						fmt.Sprintf("invalid %s contract %q", contractPrefix, comment.Text),
					)
				}
			}
		}
	}

	return explicit
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
		for fieldIndex, field := range fn.Type.Params.List {
			_, variadic := field.Type.(*ast.Ellipsis)
			variadic = variadic && fieldIndex == len(fn.Type.Params.List)-1

			for _, name := range field.Names {
				out[name.Name] = contractBinding{param: paramIndex, variadic: variadic}
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

	// "!=" wins when both appear, so `a != "=="` keeps its literal.
	op, wantEq := "!=", false

	opIndex := strings.Index(body, op)
	if opIndex < 0 {
		op, wantEq = "==", true
		opIndex = strings.Index(body, op)
	}

	if opIndex < 0 {
		return guardContract{}, false
	}

	// Empty sides fail below: no binding has an empty name or scalar text.
	right := strings.TrimSpace(body[opIndex+len(op):])

	parts := strings.Split(strings.TrimSpace(body[:opIndex]), ".")
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
			param:    binding.param,
			recv:     binding.recv,
			variadic: binding.variadic,
			path:     append([]string(nil), parts[1:]...),
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
	if trimmed, ok := strings.CutPrefix(text, contractPrefix); ok {
		return strings.TrimSpace(trimmed), true
	}

	return "", false
}

func parseContractScalar(text string) (scalar, bool) {
	switch text {
	case nilText:
		return scalar{kind: scalarNil, text: nilText}, true
	case boolTrueText, boolFalseText:
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
	obj = obj.Origin()

	pkgPath := ""
	if obj.Pkg() != nil {
		pkgPath = obj.Pkg().Path()
	}

	return pkgPath + "|" + obj.FullName()
}

func (q *exprQuery) contractEvidenceText(
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
		q.render(call.Fun),
		sym.name,
		op,
		contract.value.String(),
	)
}

func (q *exprQuery) calledFunc(call *ast.CallExpr) (*types.Func, string, bool) {
	switch fun := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		obj, ok := q.info.ObjectOf(fun).(*types.Func)
		if !ok {
			return nil, "", false
		}

		return obj, funcObjectKey(obj), true
	case *ast.SelectorExpr:
		if sel := q.info.Selections[fun]; sel != nil {
			if obj, ok := sel.Obj().(*types.Func); ok {
				return obj, funcObjectKey(obj), true
			}
		}

		obj, ok := q.info.ObjectOf(fun.Sel).(*types.Func)
		if !ok {
			return nil, "", false
		}

		return obj, funcObjectKey(obj), true
	default:
		return nil, "", false
	}
}

func (q *exprQuery) symbolForContractTarget(
	call *ast.CallExpr,
	target contractTarget,
) (symbol, bool) {
	var baseExpr ast.Expr

	if target.recv {
		sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
		if !ok {
			return symbol{}, false
		}

		baseExpr = sel.X
	} else {
		if target.param < 0 || target.param >= len(call.Args) {
			return symbol{}, false
		}

		if target.variadic && call.Ellipsis == token.NoPos {
			return symbol{}, false
		}

		baseExpr = call.Args[target.param]
	}

	base, ok := q.symbolOf(baseExpr)
	if !ok {
		return symbol{}, false
	}

	return q.symbolForPath(base, target.path)
}

func (q *exprQuery) symbolForPath(base symbol, path []string) (symbol, bool) {
	current := base

	for _, field := range path {
		if field == lenPathSegment {
			current = lenSymbolForBase(current)
			continue
		}

		fieldType, ok := q.lookupFieldType(current.typ, field)
		if !ok {
			return symbol{}, false
		}

		current = current.child(field, fieldType)
	}

	return current, true
}

func (q *exprQuery) lookupFieldType(t types.Type, name string) (types.Type, bool) {
	obj, _, _ := types.LookupFieldOrMethod(t, true, q.typesPkg, name)

	field, ok := obj.(*types.Var)
	if !ok || !field.IsField() {
		return nil, false
	}

	return field.Type(), true
}
