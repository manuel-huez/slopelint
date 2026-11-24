package flow

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"strconv"
	"strings"

	smellcheck "github.com/manuel-huez/slopelint/internal/lint/smells"
)

const contractPrefix = "slopelint:ensures"
const nilText = "nil"

type GuardContract struct {
	Target ContractTarget
	Value  Scalar
	WantEq bool
}

type ContractTarget struct {
	Param    int
	Recv     bool
	Variadic bool
	Path     []string
}

type contractBinding struct {
	param    int
	recv     bool
	variadic bool
}

func collectContracts(pkg *Package, report reportFunc) map[string][]GuardContract {
	explicit := make(map[string][]GuardContract)

	for _, file := range pkg.Files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Doc == nil {
				continue
			}

			key := smellcheck.FuncObjectKey(pkg.TypesInfo.ObjectOf(fn.Name).(*types.Func))
			bindings := contractBindingsForFunc(fn)

			for _, comment := range fn.Doc.List {
				if contract, ok := parseGuardContract(comment.Text, bindings); ok {
					explicit[key] = append(explicit[key], contract)
					continue
				}

				if _, prefixed := trimContractPrefix(comment.Text); prefixed {
					report(
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

func parseGuardContract(text string, bindings map[string]contractBinding) (GuardContract, bool) {
	body, ok := trimContractPrefix(text)
	if !ok {
		return GuardContract{}, false
	}

	// "!=" wins when both appear, so `a != "=="` keeps its literal.
	op, wantEq := "!=", false

	opIndex := strings.Index(body, op)
	if opIndex < 0 {
		op, wantEq = "==", true
		opIndex = strings.Index(body, op)
	}

	if opIndex < 0 {
		return GuardContract{}, false
	}

	// Empty sides fail below: no binding has an empty name or Scalar text.
	right := strings.TrimSpace(body[opIndex+len(op):])

	parts := strings.Split(strings.TrimSpace(body[:opIndex]), ".")
	if len(parts) == 0 || parts[0] == "" {
		return GuardContract{}, false
	}

	binding, ok := bindings[parts[0]]
	if !ok {
		return GuardContract{}, false
	}

	value, ok := parseContractScalar(right)
	if !ok {
		return GuardContract{}, false
	}

	return GuardContract{
		Target: ContractTarget{
			Param:    binding.param,
			Recv:     binding.recv,
			Variadic: binding.variadic,
			Path:     append([]string(nil), parts[1:]...),
		},
		Value:  value,
		WantEq: wantEq,
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

func parseContractScalar(text string) (Scalar, bool) {
	switch text {
	case nilText:
		return Scalar{Kind: ScalarNil, Text: nilText}, true
	case boolTrueText, boolFalseText:
		return Scalar{Kind: ScalarBool, Text: text}, true
	}

	if unquoted, err := strconv.Unquote(text); err == nil {
		return Scalar{Kind: ScalarString, Text: unquoted}, true
	}

	if _, err := strconv.ParseInt(text, 10, 64); err == nil {
		return Scalar{Kind: ScalarInt, Text: text}, true
	}

	return Scalar{}, false
}

func (q *exprQuery) contractEvidenceText(
	sym symbol,
	contract GuardContract,
	call *ast.CallExpr,
) string {
	op := "!="
	if contract.WantEq {
		op = "=="
	}

	return fmt.Sprintf(
		"%s ensures %s %s %s",
		q.render(call.Fun),
		sym.name,
		op,
		contract.Value.String(),
	)
}

func (q *exprQuery) calledFunc(call *ast.CallExpr) (*types.Func, string, bool) {
	switch fun := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		obj, ok := q.info.ObjectOf(fun).(*types.Func)
		if !ok {
			return nil, "", false
		}

		return obj, smellcheck.FuncObjectKey(obj), true
	case *ast.SelectorExpr:
		if sel := q.info.Selections[fun]; sel != nil {
			if obj, ok := sel.Obj().(*types.Func); ok {
				return obj, smellcheck.FuncObjectKey(obj), true
			}
		}

		obj, ok := q.info.ObjectOf(fun.Sel).(*types.Func)
		if !ok {
			return nil, "", false
		}

		return obj, smellcheck.FuncObjectKey(obj), true
	default:
		return nil, "", false
	}
}

func (q *exprQuery) symbolForContractTarget(
	call *ast.CallExpr,
	target ContractTarget,
) (symbol, bool) {
	var baseExpr ast.Expr

	if target.Recv {
		sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
		if !ok {
			return symbol{}, false
		}

		baseExpr = sel.X
	} else {
		if target.Param < 0 || target.Param >= len(call.Args) {
			return symbol{}, false
		}

		if target.Variadic && call.Ellipsis == token.NoPos {
			return symbol{}, false
		}

		baseExpr = call.Args[target.Param]
	}

	base, ok := q.symbolOf(baseExpr)
	if !ok {
		return symbol{}, false
	}

	return q.symbolForPath(base, target.Path)
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
func applyContractSequence[T any](
	initial state,
	contracts []T,
	normalize func([]state) []state,
	apply func(state, T) []state,
) []state {
	if len(contracts) == 0 {
		return []state{initial}
	}

	current := []state{initial}
	for _, contract := range contracts {
		nextStates := make([]state, 0, len(current))
		for _, currentState := range current {
			nextStates = append(nextStates, apply(currentState, contract)...)
		}

		current = normalize(nextStates)
		if len(current) == 0 {
			return nil
		}
	}

	return current
}
