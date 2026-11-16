package smells

import (
	"fmt"
	"go/ast"
	"go/types"
)

const (
	marshalJSONMethodName  = "MarshalJSON"
	marshalTextMethodName  = "MarshalText"
	stringMethodName       = "String"
	encodingJSONPkgPath    = "encoding/json"
	marshalTextResultCount = 2
)

// serializationCheck reports MarshalJSON methods that repeat MarshalText.
type serializationCheck struct {
	ruleEnv
}

func (l serializationCheck) check() {
	textMarshalers := l.equivalentTextMarshalerTypes()
	if len(textMarshalers) == 0 {
		return
	}

	for _, file := range l.pkg.Files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != marshalJSONMethodName {
				continue
			}

			typeName, ok := redundantJSONMarshalTextMethod(l.pkg.TypesInfo, fn, textMarshalers)
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

func (l serializationCheck) equivalentTextMarshalerTypes() map[string]struct{} {
	out := make(map[string]struct{})

	for _, file := range l.pkg.Files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != marshalTextMethodName {
				continue
			}

			typeName, ok := valueReceiverTypeName(fn)
			if !ok {
				continue
			}

			recvObj := funcReceiverObject(l.pkg.TypesInfo, fn.Recv)
			if recvObj == nil ||
				!marshalTextReturnsReceiverString(l.pkg.TypesInfo, fn.Body, recvObj) {
				continue
			}

			out[typeName] = struct{}{}
		}
	}

	return out
}

func marshalTextReturnsReceiverString(
	info *types.Info,
	body *ast.BlockStmt,
	recvObj *types.Var,
) bool {
	results, ok := singleReturnResults(body, marshalTextResultCount)
	if !ok {
		return false
	}

	nilResult, ok := ast.Unparen(results[1]).(*ast.Ident)
	if !ok || nilResult.Name != nilText {
		return false
	}

	conversion, ok := ast.Unparen(results[0]).(*ast.CallExpr)
	if !ok || len(conversion.Args) != 1 || conversion.Ellipsis.IsValid() {
		return false
	}

	tv, ok := info.Types[ast.Unparen(conversion.Fun)]
	if !ok || !tv.IsType() || !isByteSlice(tv.Type) {
		return false
	}

	return isReceiverStringCall(info, conversion.Args[0], recvObj)
}

func isByteSlice(typ types.Type) bool {
	slice, ok := types.Unalias(typ).Underlying().(*types.Slice)
	if !ok {
		return false
	}

	basic, ok := types.Unalias(slice.Elem()).Underlying().(*types.Basic)

	return ok && basic.Kind() == types.Uint8
}

func redundantJSONMarshalTextMethod(
	info *types.Info,
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

	recvObj := funcReceiverObject(info, fn.Recv)
	if recvObj == nil {
		return "", false
	}

	if !marshalJSONOnlyMarshalsString(info, fn.Body, recvObj) {
		return "", false
	}

	return typeName, true
}

func valueReceiverTypeName(fn *ast.FuncDecl) (string, bool) {
	if fn.Recv == nil || len(fn.Recv.List) != 1 {
		return "", false
	}

	ident, ok := fn.Recv.List[0].Type.(*ast.Ident)
	if !ok {
		return "", false
	}

	return ident.Name, true
}

func marshalJSONOnlyMarshalsString(
	info *types.Info,
	body *ast.BlockStmt,
	recvObj *types.Var,
) bool {
	results, ok := singleReturnResults(body, 1)
	if !ok {
		return false
	}

	call, ok := ast.Unparen(results[0]).(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return false
	}

	if !isPackageFuncCall(info, call, encodingJSONPkgPath, "Marshal") {
		return false
	}

	return isReceiverStringCall(info, call.Args[0], recvObj)
}

func singleReturnResults(body *ast.BlockStmt, count int) ([]ast.Expr, bool) {
	if body == nil || len(body.List) != 1 {
		return nil, false
	}

	ret, ok := body.List[0].(*ast.ReturnStmt)
	if !ok {
		return nil, false
	}

	return ret.Results, len(ret.Results) == count
}

func isReceiverStringCall(info *types.Info, expr ast.Expr, recvObj *types.Var) bool {
	call, ok := ast.Unparen(expr).(*ast.CallExpr)
	if !ok || len(call.Args) != 0 {
		return false
	}

	sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != stringMethodName {
		return false
	}

	return identRefersToObject(info, ast.Unparen(sel.X), recvObj)
}
