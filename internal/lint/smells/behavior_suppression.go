package smells

import (
	"go/ast"
	"go/token"
	"go/types"
	"slices"
	"strings"
)

const behaviorCloneIgnorePrefix = "slopelint:ignore behavior_clone"

func (l *Runner) behaviorCloneSuppressed(node ast.Node) bool {
	if node == nil {
		return false
	}

	return l.behaviorCloneSuppressedRange(node.Pos(), node.End())
}

func (l *Runner) behaviorCloneSuppressedRange(startPos, endPos token.Pos) bool {
	if l.pkg.FSet == nil {
		return false
	}

	nodeFile := l.pkg.FSet.File(startPos)
	if nodeFile == nil {
		return false
	}

	start := l.pkg.FSet.Position(startPos).Line
	end := l.pkg.FSet.Position(endPos).Line

	for _, file := range l.pkg.Files {
		for _, group := range file.Comments {
			if l.pkg.FSet.File(group.Pos()) != nodeFile || !commentTouchesLines(
				l.pkg.FSet.Position(group.Pos()).Line,
				l.pkg.FSet.Position(group.End()).Line,
				start,
				end,
			) {
				continue
			}

			for _, comment := range group.List {
				if behaviorCloneIgnoreComment(comment.Text) {
					return true
				}
			}
		}
	}

	return false
}

func (l *Runner) behaviorCloneSuppressedStatements(stmts []ast.Stmt, start, end int) bool {
	if start < 0 || start >= end || end > len(stmts) {
		return false
	}

	if l.behaviorCloneSuppressedRange(stmts[start].Pos(), stmts[end-1].End()) {
		return true
	}

	nextStart := stmts[start].Pos()
	for idx := start - 1; idx >= 0; idx-- {
		previousEndLine := l.pkg.FSet.Position(stmts[idx].End()).Line
		nextStartLine := l.pkg.FSet.Position(nextStart).Line

		if previousEndLine < nextStartLine-1 {
			break
		}

		if l.behaviorCloneSuppressed(stmts[idx]) {
			return true
		}

		nextStart = stmts[idx].Pos()
	}

	return false
}

func behaviorCloneIgnoreComment(text string) bool {
	text = strings.TrimSpace(text)
	text = strings.TrimSpace(strings.TrimPrefix(text, "//"))
	text = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(text, "/*"), "*/"))

	rest, ok := strings.CutPrefix(text, behaviorCloneIgnorePrefix)
	if !ok {
		return false
	}

	reason, ok := strings.CutPrefix(strings.TrimSpace(rest), "--")

	return ok && strings.TrimSpace(reason) != ""
}

func (l *Runner) behaviorGenerated(node ast.Node) bool {
	if node == nil {
		return false
	}

	for _, file := range l.pkg.ProductionFiles {
		if file.Pos() <= node.Pos() && node.End() <= file.End() {
			return ast.IsGenerated(file)
		}
	}

	return false
}

func (l *Runner) behaviorAutoSuppressedClosures() map[*ast.FuncLit]struct{} {
	suppressed := make(map[*ast.FuncLit]struct{})

	for _, file := range l.pkg.ProductionFiles {
		if ast.IsGenerated(file) {
			continue
		}

		ast.PreorderStack(file, nil, func(node ast.Node, stack []ast.Node) bool {
			lit, ok := node.(*ast.FuncLit)
			if !ok {
				return true
			}

			if l.behaviorDeferredCleanupClosure(lit, stack) ||
				l.behaviorSortComparatorClosure(lit, stack) {
				suppressed[lit] = struct{}{}
			}

			return true
		})
	}

	return suppressed
}

func (l *Runner) behaviorDeferredCleanupClosure(lit *ast.FuncLit, stack []ast.Node) bool {
	if len(stack) < 2 || len(lit.Body.List) != 1 {
		return false
	}

	call, ok := stack[len(stack)-1].(*ast.CallExpr)
	if !ok || ast.Unparen(call.Fun) != lit {
		return false
	}

	deferStmt, ok := stack[len(stack)-2].(*ast.DeferStmt)
	if !ok || deferStmt.Call != call {
		return false
	}

	switch stmt := lit.Body.List[0].(type) {
	case *ast.ExprStmt:
		cleanup, ok := ast.Unparen(stmt.X).(*ast.CallExpr)

		return ok && l.behaviorCleanupCall(cleanup)
	case *ast.AssignStmt:
		return l.behaviorCleanupErrorAssignment(stmt)
	default:
		return false
	}
}

func (l *Runner) behaviorCleanupCall(call *ast.CallExpr) bool {
	fn, _, ok := l.calledFunc(call)
	if !ok {
		return false
	}

	switch fn.Name() {
	case "Cancel", "Close", "Release", "Stop", "Unlock":
		return true
	default:
		return false
	}
}

func (l *Runner) behaviorCleanupErrorAssignment(stmt *ast.AssignStmt) bool {
	if stmt.Tok != token.ASSIGN || len(stmt.Lhs) != 1 || len(stmt.Rhs) != 1 {
		return false
	}

	lhs, ok := ast.Unparen(stmt.Lhs[0]).(*ast.Ident)
	if !ok {
		return false
	}

	call, ok := ast.Unparen(stmt.Rhs[0]).(*ast.CallExpr)
	if !ok {
		return false
	}

	if lhs.Name == "_" {
		return l.behaviorCleanupCall(call)
	}

	return l.behaviorCleanupJoinAssignment(lhs, call)
}

func (l *Runner) behaviorCleanupJoinAssignment(lhs *ast.Ident, call *ast.CallExpr) bool {
	fn, _, ok := l.calledFunc(call)
	if !ok || fn.Pkg() == nil || fn.Pkg().Path() != "errors" || fn.Name() != "Join" {
		return false
	}

	lhsObject := l.pkg.TypesInfo.ObjectOf(lhs)
	hasAccumulator := false
	hasCleanup := false

	for _, arg := range call.Args {
		ast.Inspect(arg, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.Ident:
				hasAccumulator = hasAccumulator || l.pkg.TypesInfo.ObjectOf(node) == lhsObject
			case *ast.CallExpr:
				hasCleanup = hasCleanup || l.behaviorCleanupCall(node)
			}

			return true
		})
	}

	return lhsObject != nil && hasAccumulator && hasCleanup
}

func (l *Runner) behaviorSortComparatorClosure(lit *ast.FuncLit, stack []ast.Node) bool {
	if len(stack) == 0 || len(lit.Body.List) != 1 {
		return false
	}

	ret, ok := lit.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return false
	}

	comparison, ok := ast.Unparen(ret.Results[0]).(*ast.BinaryExpr)
	if !ok || !slices.Contains(
		[]token.Token{token.LSS, token.GTR, token.LEQ, token.GEQ},
		comparison.Op,
	) {
		return false
	}

	call, ok := stack[len(stack)-1].(*ast.CallExpr)
	if !ok || !behaviorCallHasArg(call, lit) {
		return false
	}

	return l.behaviorSortCall(call)
}

func (l *Runner) behaviorSortCall(call *ast.CallExpr) bool {
	fn, _, ok := l.calledFunc(call)

	return ok && fn.Pkg() != nil && fn.Pkg().Path() == "sort" &&
		(fn.Name() == "Slice" || fn.Name() == "SliceStable")
}

func behaviorCallHasArg(call *ast.CallExpr, target ast.Expr) bool {
	for _, arg := range call.Args {
		if ast.Unparen(arg) == target {
			return true
		}
	}

	return false
}

func behaviorInterfaces(pkgs []*Package) []*types.Interface {
	seen := make(map[*types.Interface]struct{})
	interfaces := make([]*types.Interface, 0)

	add := func(typ types.Type) {
		if typ == nil {
			return
		}

		iface, ok := types.Unalias(typ).Underlying().(*types.Interface)
		if !ok || !iface.IsMethodSet() {
			return
		}

		iface.Complete()

		if _, exists := seen[iface]; exists {
			return
		}

		seen[iface] = struct{}{}
		interfaces = append(interfaces, iface)
	}

	for _, pkg := range pkgs {
		for _, spec := range pkg.ProductionTypes {
			if obj := pkg.TypesInfo.Defs[spec.Name]; obj != nil {
				add(obj.Type())
			}
		}

		for _, value := range pkg.TypesInfo.Types {
			add(value.Type)
		}
	}

	return interfaces
}

func (l *Runner) behaviorInterfaceForwarder(
	function behaviorSSAFunction,
	interfaces []*types.Interface,
) bool {
	decl, ok := function.syntax.(*ast.FuncDecl)
	if !ok || function.obj == nil || len(decl.Body.List) != 1 {
		return false
	}

	sig, ok := function.obj.Type().(*types.Signature)
	if !ok || sig.Recv() == nil || !behaviorMethodRequiredByInterface(
		function.obj,
		sig.Recv().Type(),
		interfaces,
	) {
		return false
	}

	call, ok := l.trivialForwarderBodyCall(decl, function.obj)

	return ok && l.validForwardTarget(function.obj, call)
}

func behaviorMethodRequiredByInterface(
	method *types.Func,
	receiver types.Type,
	interfaces []*types.Interface,
) bool {
	for _, iface := range interfaces {
		if !types.Implements(receiver, iface) {
			continue
		}

		for ifaceMethod := range iface.Methods() {
			if ifaceMethod.Name() == method.Name() {
				return true
			}
		}
	}

	return false
}
