package smells

import (
	"fmt"
	"github.com/manuel-huez/slopelint/internal/lint/outline"
	"go/ast"
	"go/token"
	"go/types"
	"strconv"
)

// apiCheck reports overbuilt function signatures: predicate names without
// bool results, bool mode parameters, (value, ok, error) results, and unused
// private parameters.
type apiCheck struct {
	ruleEnv
}

func (l apiCheck) checkPredicateSignatures() {
	for _, file := range l.pkg.Files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !isIsPredicateName(fn.Name.Name) {
				continue
			}

			obj, ok := funcDeclObject(l.pkg.TypesInfo, fn)
			if !ok {
				continue
			}

			sig, ok := obj.Type().(*types.Signature)
			if !ok || isPredicateSignature(sig) {
				continue
			}

			l.report(
				fn.Name.Pos(),
				"predicate_signature",
				fmt.Sprintf(
					`predicate-like function %q should return bool or (bool, error)`,
					fn.Name.Name,
				),
			)
		}
	}
}

func isPredicateSignature(sig *types.Signature) bool {
	results := sig.Results()
	if results == nil || results.Len() == 0 || results.Len() > 2 {
		return false
	}

	if !isBoolType(results.At(0).Type()) {
		return false
	}

	return results.Len() == 1 || isErrorType(results.At(1).Type())
}

type boolModeParam struct {
	name  string
	index int
	pos   token.Pos
}

func (l apiCheck) checkBoolModeParams() {
	paramsByFunc := l.privateBoolModeParams()
	if len(paramsByFunc) == 0 {
		return
	}

	reported := make(map[string]struct{})

	for _, decl := range l.pkg.ProductionDecls {
		ast.Inspect(decl, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}

			fn, key, ok := calledFunc(l.pkg.TypesInfo, call)
			if !ok {
				return true
			}

			for _, param := range paramsByFunc[key] {
				if param.index >= len(call.Args) {
					continue
				}

				arg, isIdent := ast.Unparen(call.Args[param.index]).(*ast.Ident)
				if !isIdent || arg.Name != boolTrueText && arg.Name != boolFalseText {
					continue
				}

				reportKey := key + ":" + strconv.Itoa(param.index)
				if _, dup := reported[reportKey]; dup {
					continue
				}

				reported[reportKey] = struct{}{}

				l.reportBoolModeParam(fn.Name(), param)
			}

			return true
		})
	}
}

func (l apiCheck) privateBoolModeParams() map[string][]boolModeParam {
	out := make(map[string][]boolModeParam)

	for _, fn := range l.pkg.ProductionFuncs {
		obj, ok := privateFuncObject(l.pkg.TypesInfo, fn)
		if !ok {
			continue
		}

		if params := boolModeParams(l.pkg.TypesInfo, fn.Type.Params); len(params) != 0 {
			out[funcObjectKey(obj)] = params
		}
	}

	return out
}

func boolModeParams(info *types.Info, fields *ast.FieldList) []boolModeParam {
	out := make([]boolModeParam, 0)
	index := 0

	for _, field := range fields.List {
		if _, variadic := field.Type.(*ast.Ellipsis); variadic {
			index += max(len(field.Names), 1)
			continue
		}

		count := max(len(field.Names), 1)
		isBool := isBoolType(info.TypeOf(field.Type))

		for idx := range count {
			name := ""
			pos := field.Type.Pos()

			if idx < len(field.Names) {
				name = field.Names[idx].Name
				pos = field.Names[idx].Pos()
			}

			if isBool && boolModeParamNameEligible(name) {
				out = append(out, boolModeParam{
					name:  name,
					index: index,
					pos:   pos,
				})
			}

			index++
		}
	}

	return out
}

func (l apiCheck) reportBoolModeParam(fnName string, param boolModeParam) {
	name := param.name
	if name == "" {
		name = fmt.Sprintf("#%d", param.index+1)
	}

	l.report(
		param.pos,
		"bool_mode_param",
		fmt.Sprintf(
			`private function %q has bool mode parameter %q called with boolean literals; split named operations or use a typed mode`,
			fnName,
			name,
		),
	)
}

func boolModeParamNameEligible(name string) bool {
	if name == "" {
		return true
	}

	words := outline.Words(name)
	if len(words) == 0 {
		return true
	}

	switch words[0] {
	case "always", "include", "want":
		return false
	}

	return words[len(words)-1] != "only"
}

const (
	optionalResultTripleResultCount = 3
	optionalResultTripleValueIndex  = 0
	optionalResultTripleOKIndex     = 1
	optionalResultTripleErrorIndex  = 2
)

func (l apiCheck) checkOptionalResultTriples() {
	for _, fn := range l.pkg.ProductionFuncs {
		obj, ok := privateFuncObject(l.pkg.TypesInfo, fn)
		if !ok {
			continue
		}

		sig, ok := obj.Type().(*types.Signature)
		if !ok || !signatureReturnsValueOKError(sig) {
			continue
		}

		l.report(
			fn.Name.Pos(),
			"optional_result_triple",
			fmt.Sprintf(
				`private function %q returns (value, ok, error); make absence part of the error/zero-value contract or return a named state type`,
				fn.Name.Name,
			),
		)
	}
}

func signatureReturnsValueOKError(sig *types.Signature) bool {
	results := sig.Results()
	if results == nil || results.Len() != optionalResultTripleResultCount {
		return false
	}

	return !isBoolType(results.At(optionalResultTripleValueIndex).Type()) &&
		isBoolType(results.At(optionalResultTripleOKIndex).Type()) &&
		isErrorType(results.At(optionalResultTripleErrorIndex).Type())
}

func (l apiCheck) checkUnusedPrivateParams() {
	for _, fn := range l.pkg.ProductionFuncs {
		if fn.Body == nil || ast.IsExported(fn.Name.Name) {
			continue
		}

		for _, field := range fn.Type.Params.List {
			for _, name := range field.Names {
				if name.Name == "_" {
					continue
				}

				obj := l.pkg.TypesInfo.ObjectOf(name)
				if obj == nil || nodeUsesObject(fn.Body, obj, l.pkg.TypesInfo) {
					continue
				}

				l.report(
					name.Pos(),
					"unused_private_param",
					fmt.Sprintf(
						`private function %q does not use parameter %q; remove it and update callers or name it _ when signature shape is required`,
						fn.Name.Name,
						name.Name,
					),
				)
			}
		}
	}
}

func nodeUsesObject(node ast.Node, target types.Object, info *types.Info) bool {
	used := false

	ast.Inspect(node, func(node ast.Node) bool {
		if used {
			return false
		}

		ident, ok := node.(*ast.Ident)
		if ok && info.ObjectOf(ident) == target {
			used = true
			return false
		}

		return true
	})

	return used
}
