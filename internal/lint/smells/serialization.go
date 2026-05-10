package smells

import (
	"fmt"
	"go/ast"
	"go/types"
)

const (
	marshalJSONMethodName = "MarshalJSON"
	marshalTextMethodName = "MarshalText"
	stringMethodName      = "String"
	encodingJSONPkgPath   = "encoding/json"
)

func (l *Runner) checkRedundantJSONMarshalText() {
	textMarshalers := l.valueTextMarshalerTypes()
	if len(textMarshalers) == 0 {
		return
	}

	for _, file := range l.pkg.Files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name == nil || fn.Name.Name != marshalJSONMethodName {
				continue
			}

			typeName, ok := l.redundantJSONMarshalTextMethod(fn, textMarshalers)
			if !ok {
				continue
			}

			l.report(
				fn.Name.Pos(),
				"serialization_ceremony",
				fmt.Sprintf(
					`%s.%s only marshals String while MarshalText exists; remove MarshalJSON and let encoding/json use MarshalText`,
					typeName,
					marshalJSONMethodName,
				),
			)
		}
	}
}

func (l *Runner) valueTextMarshalerTypes() map[string]struct{} {
	out := make(map[string]struct{})

	for _, file := range l.pkg.Files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name == nil || fn.Name.Name != marshalTextMethodName {
				continue
			}

			typeName, ok := valueReceiverTypeName(fn)
			if !ok {
				continue
			}

			out[typeName] = struct{}{}
		}
	}

	return out
}

func (l *Runner) redundantJSONMarshalTextMethod(
	fn *ast.FuncDecl,
	textMarshalers map[string]struct{},
) (string, bool) {
	typeName, ok := valueReceiverTypeName(fn)
	if !ok {
		return "", false
	}

	if _, ok := textMarshalers[typeName]; !ok {
		return "", false
	}

	recvObj, ok := receiverVarObject(l.pkg.TypesInfo, fn)
	if !ok {
		return "", false
	}

	if !l.marshalJSONOnlyMarshalsString(fn.Body, recvObj) {
		return "", false
	}

	return typeName, true
}

func valueReceiverTypeName(fn *ast.FuncDecl) (string, bool) {
	if fn == nil || fn.Recv == nil || len(fn.Recv.List) != 1 {
		return "", false
	}

	ident, ok := fn.Recv.List[0].Type.(*ast.Ident)
	if !ok || ident == nil {
		return "", false
	}

	return ident.Name, true
}

func receiverVarObject(info *types.Info, fn *ast.FuncDecl) (*types.Var, bool) {
	if fn == nil || fn.Recv == nil || len(fn.Recv.List) != 1 {
		return nil, false
	}

	field := fn.Recv.List[0]
	if len(field.Names) != 1 || field.Names[0] == nil {
		return nil, false
	}

	obj, ok := info.Defs[field.Names[0]].(*types.Var)
	if !ok || obj == nil {
		return nil, false
	}

	return obj, true
}

func (l *Runner) marshalJSONOnlyMarshalsString(
	body *ast.BlockStmt,
	recvObj *types.Var,
) bool {
	if body == nil || len(body.List) != 1 {
		return false
	}

	ret, ok := body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return false
	}

	call, ok := l.unparen(ret.Results[0]).(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return false
	}

	if !l.isPackageFuncCall(call, encodingJSONPkgPath, "Marshal") {
		return false
	}

	return l.isReceiverStringCall(call.Args[0], recvObj)
}

func (l *Runner) isReceiverStringCall(expr ast.Expr, recvObj *types.Var) bool {
	call, ok := l.unparen(expr).(*ast.CallExpr)
	if !ok || len(call.Args) != 0 {
		return false
	}

	sel, ok := l.unparen(call.Fun).(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != stringMethodName {
		return false
	}

	return identRefersToObject(l.pkg.TypesInfo, l.unparen(sel.X), recvObj)
}
