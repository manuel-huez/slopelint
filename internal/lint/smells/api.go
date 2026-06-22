package smells

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"strconv"
)

func (l *Runner) checkPredicateReturnSignatures() {
	l.forEachPackageFunc(func(fn *ast.FuncDecl) {
		if fn.Name == nil || !isIsPredicateName(fn.Name.Name) {
			return
		}

		obj, ok := l.pkg.TypesInfo.ObjectOf(fn.Name).(*types.Func)
		if !ok || obj == nil {
			return
		}

		sig, ok := obj.Type().(*types.Signature)
		if !ok || isPredicateSignature(sig) {
			return
		}

		l.report(
			fn.Name.Pos(),
			"predicate_signature",
			fmt.Sprintf(
				`predicate-like function %q should return bool or (bool, error)`,
				fn.Name.Name,
			),
		)
	})
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

func (l *Runner) checkBoolModeParams() {
	paramsByFunc := l.privateBoolModeParams()
	if len(paramsByFunc) == 0 {
		return
	}

	l.reportBoolModeLiteralCalls(paramsByFunc)
}

func (l *Runner) privateBoolModeParams() map[string][]boolModeParam {
	out := make(map[string][]boolModeParam)

	l.forEachProductionFunc(func(fn *ast.FuncDecl) {
		obj, ok := l.privateFuncObject(fn)
		if !ok {
			return
		}

		params := l.boolModeParams(fn.Type.Params)
		if len(params) == 0 {
			return
		}

		out[funcObjectKey(obj)] = params
	})

	return out
}

func (l *Runner) boolModeParams(fields *ast.FieldList) []boolModeParam {
	if fields == nil {
		return nil
	}

	out := make([]boolModeParam, 0)
	index := 0

	for _, field := range fields.List {
		if _, variadic := field.Type.(*ast.Ellipsis); variadic {
			index += max(len(field.Names), 1)
			continue
		}

		count := max(len(field.Names), 1)
		isBool := isBoolType(l.pkg.TypesInfo.TypeOf(field.Type))

		for idx := range count {
			name := ""
			pos := field.Type.Pos()

			if idx < len(field.Names) && field.Names[idx] != nil {
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

func (l *Runner) reportBoolModeLiteralCalls(paramsByFunc map[string][]boolModeParam) {
	reported := make(map[string]struct{})

	l.forEachProductionDecl(func(decl ast.Decl) {
		ast.Inspect(decl, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}

			fn, key, ok := l.calledFunc(call)
			if !ok {
				return true
			}

			for _, param := range paramsByFunc[key] {
				if param.index >= len(call.Args) ||
					!isBoolLiteral(l.unparen(call.Args[param.index])) {
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
	})
}

func (l *Runner) reportBoolModeParam(fnName string, param boolModeParam) {
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

func isBoolLiteral(expr ast.Expr) bool {
	ident, ok := expr.(*ast.Ident)
	if !ok || ident == nil {
		return false
	}

	return ident.Name == boolTrueText || ident.Name == boolFalseText
}

func boolModeParamNameEligible(name string) bool {
	if name == "" {
		return true
	}

	words := splitIdentifierWords(name)
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

func (l *Runner) checkOptionalResultTriples() {
	l.forEachProductionFunc(func(fn *ast.FuncDecl) {
		obj, ok := l.privateFuncObject(fn)
		if !ok {
			return
		}

		sig, ok := obj.Type().(*types.Signature)
		if !ok || !signatureReturnsValueOKError(sig) {
			return
		}

		l.report(
			fn.Name.Pos(),
			"optional_result_triple",
			fmt.Sprintf(
				`private function %q returns (value, ok, error); make absence part of the error/zero-value contract or return a named state type`,
				fn.Name.Name,
			),
		)
	})
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

func (l *Runner) privateFuncObject(fn *ast.FuncDecl) (*types.Func, bool) {
	if fn == nil ||
		fn.Name == nil ||
		fn.Body == nil ||
		ast.IsExported(fn.Name.Name) ||
		hasTypeParams(fn.Type) ||
		fn.Name.Name == initFuncName {
		return nil, false
	}

	return l.funcDeclObject(fn)
}

func (l *Runner) funcDeclObject(fn *ast.FuncDecl) (*types.Func, bool) {
	if fn == nil || fn.Name == nil {
		return nil, false
	}

	obj, ok := l.pkg.TypesInfo.ObjectOf(fn.Name).(*types.Func)

	return obj, ok && obj != nil
}

func (l *Runner) isPackageFuncCall(call *ast.CallExpr, pkgPath, name string) bool {
	fn, _, ok := l.calledFunc(call)
	if !ok || fn == nil || fn.Pkg() == nil {
		return false
	}

	return fn.Pkg().Path() == pkgPath && fn.Name() == name
}
