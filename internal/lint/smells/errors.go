package smells

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"slices"
	"strings"
)

func (l *Runner) checkProductionErrorPanics() {
	l.forEachProductionFunc(func(fn *ast.FuncDecl) {
		if fn == nil || fn.Body == nil || fn.Name == nil {
			return
		}

		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.FuncLit:
				return false
			case *ast.CallExpr:
				if !l.isPanicWithError(node) {
					return true
				}

				l.report(
					node.Pos(),
					"prod_must_panic",
					fmt.Sprintf(
						`production function %q panics with error %q; return error or prove the invariant before this call`,
						fn.Name.Name,
						l.render(node.Args[0]),
					),
				)
			}

			return true
		})
	})
}

func (l *Runner) isPanicWithError(call *ast.CallExpr) bool {
	if call == nil || len(call.Args) != 1 {
		return false
	}

	if !l.isBuiltinCall(call, panicText) {
		return false
	}

	return isErrorType(l.pkg.TypesInfo.TypeOf(call.Args[0]))
}

const (
	errorsPkgPath    = "errors"
	errorsIsFuncName = "Is"
)

type sentinelReturn struct {
	text        string
	pos         token.Pos
	assignedEnd token.Pos
	errObj      types.Object
}

type errorSentinelPolarity uint8

const (
	errorSentinelPositive errorSentinelPolarity = iota
	errorSentinelNegated
)

type errorSentinelCondition struct {
	text     string
	errObj   types.Object
	polarity errorSentinelPolarity
}

func (l *Runner) checkSentinelErrorBreaks() {
	l.forEachProductionFunc(func(fn *ast.FuncDecl) {
		if fn == nil || fn.Body == nil {
			return
		}

		suppressed := l.suppressedErrorSentinels(fn.Body)
		if len(suppressed) == 0 {
			return
		}

		for _, returned := range l.callbackSentinelReturns(fn.Body) {
			if returned.errObj == nil {
				continue
			}

			suppressionPos, ok := suppressed[returned.errObj][returned.text]
			if !ok || suppressionPos <= returned.assignedEnd || l.objectAssignedBetween(
				fn.Body,
				returned.errObj,
				returned.assignedEnd,
				suppressionPos,
			) {
				continue
			}

			l.report(
				returned.pos,
				"sentinel_error_break",
				fmt.Sprintf(
					`callback returns sentinel error %q for control flow and caller suppresses it; use an explicit stop signal instead`,
					returned.text,
				),
			)
		}
	})
}

func (l *Runner) suppressedErrorSentinels(
	body *ast.BlockStmt,
) map[types.Object]map[string]token.Pos {
	out := make(map[types.Object]map[string]token.Pos)

	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.IfStmt:
			for _, condition := range l.errorSentinelConditions(
				node.Cond,
				errorSentinelPositive,
			) {
				if !l.ifSuppressesErrorSentinel(node, condition) {
					continue
				}

				if out[condition.errObj] == nil {
					out[condition.errObj] = make(map[string]token.Pos)
				}

				prior := out[condition.errObj][condition.text]

				if prior == token.NoPos || node.Pos() < prior {
					out[condition.errObj][condition.text] = node.Pos()
				}
			}

			return true
		}

		return true
	})

	return out
}

func (l *Runner) errorSentinelConditions(
	expr ast.Expr,
	polarity errorSentinelPolarity,
) []errorSentinelCondition {
	expr = l.unparen(expr)

	switch node := expr.(type) {
	case *ast.UnaryExpr:
		if node.Op != token.NOT {
			return nil
		}

		return l.errorSentinelConditions(node.X, oppositeErrorSentinelPolarity(polarity))
	case *ast.BinaryExpr:
		if node.Op != token.LAND && node.Op != token.LOR {
			return nil
		}

		conditions := l.errorSentinelConditions(node.X, polarity)
		conditions = append(conditions, l.errorSentinelConditions(node.Y, polarity)...)

		return conditions
	case *ast.CallExpr:
		if len(node.Args) != 2 || !l.isErrorsIsCall(node) {
			return nil
		}

		errObj := l.identObject(node.Args[0])
		if errObj == nil {
			return nil
		}

		sentinel := l.errorSentinelText(node.Args[1])
		if sentinel == "" {
			return nil
		}

		return []errorSentinelCondition{{
			text:     sentinel,
			errObj:   errObj,
			polarity: polarity,
		}}
	default:
		return nil
	}
}

func oppositeErrorSentinelPolarity(
	polarity errorSentinelPolarity,
) errorSentinelPolarity {
	if polarity == errorSentinelPositive {
		return errorSentinelNegated
	}

	return errorSentinelPositive
}

func (l *Runner) ifSuppressesErrorSentinel(
	ifStmt *ast.IfStmt,
	condition errorSentinelCondition,
) bool {
	if condition.errObj == nil || condition.text == "" {
		return false
	}

	if condition.polarity == errorSentinelNegated {
		return l.blockHasReturnMatching(
			ifStmt.Body,
			func(ret *ast.ReturnStmt) bool {
				return l.returnIncludesObject(ret, condition.errObj)
			},
		)
	}

	return l.blockHasReturnMatching(ifStmt.Body, func(ret *ast.ReturnStmt) bool {
		return !l.returnIncludesObject(ret, condition.errObj) &&
			returnSuppressesError(ret)
	})
}

func (l *Runner) blockHasReturnMatching(
	block *ast.BlockStmt,
	match func(*ast.ReturnStmt) bool,
) bool {
	if block == nil || match == nil {
		return false
	}

	found := false

	ast.Inspect(block, func(n ast.Node) bool {
		if found {
			return false
		}

		switch node := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			if match(node) {
				found = true
				return false
			}
		}

		return true
	})

	return found
}

func (l *Runner) returnIncludesObject(ret *ast.ReturnStmt, obj types.Object) bool {
	if ret == nil || obj == nil {
		return false
	}

	for _, result := range ret.Results {
		if l.identObject(result) == obj {
			return true
		}
	}

	return false
}

func returnSuppressesError(ret *ast.ReturnStmt) bool {
	if ret == nil {
		return false
	}

	if len(ret.Results) == 0 {
		return true
	}

	return slices.ContainsFunc(ret.Results, func(result ast.Expr) bool {
		ident, ok := result.(*ast.Ident)

		return ok && ident != nil && ident.Name == nilText
	})
}

func (l *Runner) callbackSentinelReturns(body *ast.BlockStmt) []sentinelReturn {
	out := make([]sentinelReturn, 0)

	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.AssignStmt:
			out = append(out, l.callbackSentinelReturnsFromAssign(node)...)
			return true
		default:
			return true
		}
	})

	return out
}

func (l *Runner) callbackSentinelReturnsFromAssign(
	assign *ast.AssignStmt,
) []sentinelReturn {
	out := make([]sentinelReturn, 0)

	for rhsIndex, rhs := range assign.Rhs {
		errObj := l.assignedErrorObject(assign, rhsIndex)
		if errObj == nil {
			continue
		}

		for _, returned := range l.callbackSentinelReturnsInExpr(rhs) {
			returned.errObj = errObj
			returned.assignedEnd = assign.End()
			out = append(out, returned)
		}
	}

	return out
}

func (l *Runner) objectAssignedBetween(
	body *ast.BlockStmt,
	obj types.Object,
	after token.Pos,
	before token.Pos,
) bool {
	found := false

	inspectNodesBetween(body, after, before, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.AssignStmt:
			for _, lhs := range node.Lhs {
				if l.identObject(lhs) == obj {
					found = true
					return false
				}
			}
		case *ast.IncDecStmt:
			found = l.identObject(node.X) == obj
		}

		return !found
	})

	return found
}

func (l *Runner) assignedErrorObject(assign *ast.AssignStmt, rhsIndex int) types.Object {
	if assign == nil || rhsIndex < 0 || rhsIndex >= len(assign.Rhs) {
		return nil
	}

	if len(assign.Rhs) == 1 {
		for _, lhs := range assign.Lhs {
			obj := l.identObject(lhs)
			if obj != nil && isErrorType(obj.Type()) {
				return obj
			}
		}

		return nil
	}

	if rhsIndex >= len(assign.Lhs) {
		return nil
	}

	obj := l.identObject(assign.Lhs[rhsIndex])
	if obj == nil || !isErrorType(obj.Type()) {
		return nil
	}

	return obj
}

func (l *Runner) callbackSentinelReturnsInExpr(expr ast.Expr) []sentinelReturn {
	out := make([]sentinelReturn, 0)

	ast.Inspect(expr, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.CallExpr:
			for _, arg := range node.Args {
				lit, ok := l.unparen(arg).(*ast.FuncLit)
				if !ok || lit.Body == nil {
					continue
				}

				out = append(out, l.callbackSentinelReturnsInFuncLit(lit)...)
			}
		}

		return true
	})

	return out
}

func (l *Runner) callbackSentinelReturnsInFuncLit(lit *ast.FuncLit) []sentinelReturn {
	out := make([]sentinelReturn, 0)

	ast.Inspect(lit.Body, func(inner ast.Node) bool {
		switch node := inner.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			if len(node.Results) != 1 {
				return true
			}

			sentinel := l.errorSentinelText(node.Results[0])
			if sentinel == "" {
				return true
			}

			out = append(out, sentinelReturn{
				text: sentinel,
				pos:  node.Results[0].Pos(),
			})
		}

		return true
	})

	return out
}

func (l *Runner) identObject(expr ast.Expr) types.Object {
	ident, ok := l.unparen(expr).(*ast.Ident)
	if !ok || ident == nil {
		return nil
	}

	return l.pkg.TypesInfo.ObjectOf(ident)
}

func (l *Runner) isErrorsIsCall(call *ast.CallExpr) bool {
	return l.isPackageFuncCall(call, errorsPkgPath, errorsIsFuncName)
}

func (l *Runner) errorSentinelText(expr ast.Expr) string {
	expr = l.unparen(expr)

	if ident, ok := expr.(*ast.Ident); ok && ident != nil {
		if ident.Name == "nil" || ident.Name == "err" {
			return ""
		}
	}

	if !isErrorType(l.pkg.TypesInfo.TypeOf(expr)) {
		return ""
	}

	rendered := l.render(expr)
	if rendered == "" || strings.Contains(rendered, "\n") {
		return ""
	}

	return rendered
}
