package smells

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"slices"
	"strings"
)

type errorCheck struct {
	ruleEnv
}

func (l errorCheck) checkPanics() {
	for _, fn := range l.pkg.ProductionFuncs {
		if fn.Body == nil {
			continue
		}

		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.FuncLit:
				return false
			case *ast.CallExpr:
				if len(node.Args) != 1 || !isBuiltinCall(l.pkg.TypesInfo, node, panicText) ||
					!isErrorType(l.pkg.TypesInfo.TypeOf(node.Args[0])) {
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
	}
}

// DroppedErrorCandidate marks an `if err != nil` that drops the error. The
// message is the error variable name. Candidates are not findings: the
// standalone code review asks the model whether the fallback is safe.
const DroppedErrorCandidate = "dropped_error_candidate"

func (l errorCheck) checkSwallowed() {
	l.forEachHandwrittenProductionFunc(func(fn *ast.FuncDecl) {
		l.checkSwallowedIn(fn.Body)
	})
}

// checkSwallowedIn checks one function scope. Nested literals are their own
// scope.
func (l errorCheck) checkSwallowedIn(body *ast.BlockStmt) {
	if body == nil {
		return
	}

	info := l.pkg.TypesInfo
	following := followingStatements(body)

	ast.Inspect(body, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.FuncLit:
			l.checkSwallowedIn(node.Body)
			return false
		case *ast.IfStmt:
			errObj := nonNilErrorCheck(info, node.Cond)
			if errObj != nil && blockDiscardsError(info, node.Body) &&
				!droppedErrorReadLater(info, body, node, errObj, following[node]) {
				l.report(node.Pos(), DroppedErrorCandidate, errObj.Name())
			}
		}

		return true
	})
}

func followingStatements(body *ast.BlockStmt) map[*ast.IfStmt][]ast.Stmt {
	out := make(map[*ast.IfStmt][]ast.Stmt)

	ast.Inspect(body, func(node ast.Node) bool {
		var list []ast.Stmt

		switch node := node.(type) {
		case *ast.FuncLit:
			return false
		case *ast.BlockStmt:
			list = node.List
		case *ast.CaseClause:
			list = node.Body
		case *ast.CommClause:
			list = node.Body
		}

		for index, stmt := range list {
			if ifStmt, ok := stmt.(*ast.IfStmt); ok {
				out[ifStmt] = list[index+1:]
			}
		}

		return true
	})

	return out
}

// droppedErrorReadLater reports whether the checked error value can still be
// read after ifStmt. The answer is conservative: named results, outer
// variables, errors declared outside the innermost loop around ifStmt (a later
// iteration can read them), and errors a closure captures or whose address is
// taken all count as read.
func droppedErrorReadLater(
	info *types.Info,
	body *ast.BlockStmt,
	ifStmt *ast.IfStmt,
	errObj types.Object,
	following []ast.Stmt,
) bool {
	if errObj.Pos() < body.Pos() || errObj.Pos() > body.End() ||
		errorEscapesScope(info, body, ifStmt, errObj) {
		return true
	}

	var first *ast.Ident

	inspectNodesBetween(body, ifStmt.End(), body.End(), func(node ast.Node) bool {
		if ident, ok := node.(*ast.Ident); ok && info.ObjectOf(ident) == errObj {
			first = ident
		}

		return first == nil
	})

	return first != nil && !overwritesDroppedError(info, ifStmt, first, errObj, following)
}

// errorEscapesScope reports whether errObj lives across iterations of the
// innermost loop around ifStmt, is captured by a function literal, or has its
// address taken.
func errorEscapesScope(
	info *types.Info,
	body *ast.BlockStmt,
	ifStmt *ast.IfStmt,
	errObj types.Object,
) bool {
	escapes := false

	ast.Inspect(body, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.FuncLit:
			escapes = escapes || nodeUsesObject(node.Body, errObj, info)
			return false
		case *ast.UnaryExpr:
			escapes = escapes || node.Op == token.AND && identObject(info, node.X) == errObj
		case *ast.ForStmt, *ast.RangeStmt:
			// Declared inside the innermost loop means inside every enclosing one.
			if node.Pos() <= ifStmt.Pos() && ifStmt.End() <= node.End() {
				escapes = escapes || errObj.Pos() < node.Pos() || errObj.Pos() > node.End()
			}
		}

		return !escapes
	})

	return escapes
}

// overwritesDroppedError reports whether the first later mention is a plain
// overwrite such as `_, err = g()` by a following statement in the same block,
// reached without jumps.
func overwritesDroppedError(
	info *types.Info,
	ifStmt *ast.IfStmt,
	first *ast.Ident,
	errObj types.Object,
	following []ast.Stmt,
) bool {
	if containsBranch(ifStmt.Body) {
		return false
	}

	for _, stmt := range following {
		if stmt.End() <= first.Pos() {
			if containsBranch(stmt) {
				return false
			}

			continue
		}

		if outer, ok := stmt.(*ast.IfStmt); ok && outer.Init != nil {
			stmt = outer.Init
		}

		assign, ok := stmt.(*ast.AssignStmt)

		return ok && slices.Contains(assign.Lhs, ast.Expr(first)) &&
			!slices.ContainsFunc(assign.Rhs, func(rhs ast.Expr) bool {
				return nodeUsesObject(rhs, errObj, info)
			})
	}

	return false
}

func containsBranch(node ast.Node) bool {
	found := false

	ast.Inspect(node, func(node ast.Node) bool {
		_, branch := node.(*ast.BranchStmt)
		found = found || branch

		return !found
	})

	return found
}

// nonNilErrorCheck returns the error variable of an exact `err != nil` condition.
func nonNilErrorCheck(info *types.Info, cond ast.Expr) types.Object {
	binary, ok := ast.Unparen(cond).(*ast.BinaryExpr)
	if !ok || binary.Op != token.NEQ || !info.Types[binary.Y].IsNil() {
		return nil
	}

	obj, ok := identObject(info, binary.X).(*types.Var)
	if !ok || !isErrorType(obj.Type()) {
		return nil
	}

	return obj
}

// blockDiscardsError reports whether the block has no error value, stop call,
// or `return ..., false` contract. Any use of the checked error is itself an
// error value. Nested function literals run later or elsewhere, so they do not
// count.
func blockDiscardsError(info *types.Info, block *ast.BlockStmt) bool {
	falseObj := types.Universe.Lookup(boolFalseText)
	discards := true

	ast.Inspect(block, func(node ast.Node) bool {
		if !discards {
			return false
		}

		switch node := node.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			last := len(node.Results) - 1
			discards = last < 0 || identObject(info, node.Results[last]) != falseObj
		case *ast.CallExpr:
			discards = !isBuiltinCall(info, node, panicText) && !isProcessStopCall(info, node) &&
				!isErrorValue(info, node)
		case ast.Expr:
			discards = !isErrorValue(info, node)
		}

		return discards
	})

	return discards
}

func isErrorValue(info *types.Info, expr ast.Expr) bool {
	value, ok := info.Types[expr]

	return ok && value.IsValue() && !value.IsNil() && isErrorType(value.Type)
}

func isProcessStopCall(info *types.Info, call *ast.CallExpr) bool {
	fn, _, ok := calledFunc(info, call)
	if !ok || fn.Pkg() == nil {
		return false
	}

	switch fn.Pkg().Path() {
	case "os":
		return fn.Name() == "Exit"
	case "log":
		return strings.HasPrefix(fn.Name(), "Fatal")
	default:
		return false
	}
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

func (l errorCheck) checkSentinelBreaks() {
	for _, fn := range l.pkg.ProductionFuncs {
		if fn.Body == nil {
			continue
		}

		suppressed := l.suppressedErrorSentinels(fn.Body)
		if len(suppressed) == 0 {
			continue
		}

		for _, returned := range l.callbackSentinelReturns(fn.Body) {
			suppressionPos, ok := suppressed[returned.errObj][returned.text]
			if !ok || suppressionPos <= returned.assignedEnd || objectAssignedBetween(
				l.pkg.TypesInfo,
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
	}
}

func (l errorCheck) suppressedErrorSentinels(
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
				if !ifSuppressesErrorSentinel(l.pkg.TypesInfo, node, condition) {
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
		}

		return true
	})

	return out
}

func (l errorCheck) errorSentinelConditions(
	expr ast.Expr,
	polarity errorSentinelPolarity,
) []errorSentinelCondition {
	expr = ast.Unparen(expr)

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
		if len(node.Args) != 2 ||
			!isPackageFuncCall(l.pkg.TypesInfo, node, errorsPkgPath, errorsIsFuncName) {
			return nil
		}

		errObj := identObject(l.pkg.TypesInfo, node.Args[0])
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

// ifSuppressesErrorSentinel reports whether the sentinel case is swallowed: an
// `errors.Is` branch returns without the error, or a `!errors.Is` branch
// returns the error so the sentinel case falls through.
func ifSuppressesErrorSentinel(
	info *types.Info,
	ifStmt *ast.IfStmt,
	condition errorSentinelCondition,
) bool {
	return blockHasReturnMatching(ifStmt.Body, func(ret *ast.ReturnStmt) bool {
		includes := returnIncludesObject(info, ret, condition.errObj)
		if condition.polarity == errorSentinelNegated {
			return includes
		}

		return !includes && returnSuppressesError(ret)
	})
}

func blockHasReturnMatching(
	block *ast.BlockStmt,
	match func(*ast.ReturnStmt) bool,
) bool {
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

func returnIncludesObject(info *types.Info, ret *ast.ReturnStmt, obj types.Object) bool {
	for _, result := range ret.Results {
		if identObject(info, result) == obj {
			return true
		}
	}

	return false
}

func returnSuppressesError(ret *ast.ReturnStmt) bool {
	if len(ret.Results) == 0 {
		return true
	}

	return slices.ContainsFunc(ret.Results, func(result ast.Expr) bool {
		ident, ok := result.(*ast.Ident)

		return ok && ident.Name == nilText
	})
}

func (l errorCheck) callbackSentinelReturns(body *ast.BlockStmt) []sentinelReturn {
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

func (l errorCheck) callbackSentinelReturnsFromAssign(
	assign *ast.AssignStmt,
) []sentinelReturn {
	out := make([]sentinelReturn, 0)

	for rhsIndex, rhs := range assign.Rhs {
		errObj := assignedErrorObject(l.pkg.TypesInfo, assign, rhsIndex)
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

func objectAssignedBetween(
	info *types.Info,
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
				if identObject(info, lhs) == obj {
					found = true
					return false
				}
			}
		case *ast.IncDecStmt:
			found = identObject(info, node.X) == obj
		}

		return !found
	})

	return found
}

func assignedErrorObject(info *types.Info, assign *ast.AssignStmt, rhsIndex int) types.Object {
	if len(assign.Rhs) == 1 {
		for _, lhs := range assign.Lhs {
			obj := identObject(info, lhs)
			if obj != nil && isErrorType(obj.Type()) {
				return obj
			}
		}

		return nil
	}

	if rhsIndex >= len(assign.Lhs) {
		return nil
	}

	obj := identObject(info, assign.Lhs[rhsIndex])
	if obj == nil || !isErrorType(obj.Type()) {
		return nil
	}

	return obj
}

func (l errorCheck) callbackSentinelReturnsInExpr(expr ast.Expr) []sentinelReturn {
	out := make([]sentinelReturn, 0)

	ast.Inspect(expr, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.CallExpr:
			for _, arg := range node.Args {
				if lit, ok := ast.Unparen(arg).(*ast.FuncLit); ok {
					out = append(out, l.callbackSentinelReturnsInFuncLit(lit)...)
				}
			}
		}

		return true
	})

	return out
}

func (l errorCheck) callbackSentinelReturnsInFuncLit(lit *ast.FuncLit) []sentinelReturn {
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

func (l errorCheck) errorSentinelText(expr ast.Expr) string {
	expr = ast.Unparen(expr)

	if ident, ok := expr.(*ast.Ident); ok {
		if ident.Name == "nil" || ident.Name == "err" {
			return ""
		}
	}

	if !isErrorType(l.pkg.TypesInfo.TypeOf(expr)) {
		return ""
	}

	rendered := l.render(expr)
	if strings.Contains(rendered, "\n") {
		return ""
	}

	return rendered
}
