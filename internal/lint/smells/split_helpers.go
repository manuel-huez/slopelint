package smells

import (
	"fmt"
	"go/ast"
	"go/types"
)

// SplitHelperCandidate marks a private function that one other function in
// the same file calls once, with no other use. Candidates are not findings:
// code review reads the helper with its caller and judges whether it has a
// job of its own or is a piece of the caller cut out to shorten it. The
// message states facts about the call for that review.
const SplitHelperCandidate = "split_helper_candidate"

type splitHelperCheck struct {
	ruleEnv
}

func (l splitHelperCheck) check() {
	info := l.pkg.TypesInfo
	useCounts := l.productionPackageFuncUseCounts()
	helpers := make(map[*types.Func]*ast.FuncDecl)

	l.forEachHandwrittenProductionFunc(func(fn *ast.FuncDecl) {
		if obj, ok := privateFuncObject(info, fn); ok && useCounts[funcObjectKey(obj)] == 1 {
			helpers[obj.Origin()] = fn
		}
	})

	l.forEachHandwrittenProductionFunc(func(caller *ast.FuncDecl) {
		ast.Inspect(caller, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}

			obj := funcObject(info, ast.Unparen(call.Fun))
			if obj == nil {
				return true
			}

			if helper := helpers[obj.Origin()]; helper != nil && l.splitFrom(helper, caller) {
				l.report(helper.Name.Pos(), SplitHelperCandidate, fmt.Sprintf(
					"only %s calls it, once; the call passes %d of %s's variables, and it returns %d values",
					caller.Name.Name,
					l.callerVariables(call, caller),
					caller.Name.Name,
					funcParamCount(helper.Type.Results),
				))
			}

			return true
		})
	})
}

// splitFrom reports whether helper could be a piece of caller: another
// function in the same file, and a plain function or a method of the same
// type. A method of another type already has an owner.
func (l splitHelperCheck) splitFrom(helper, caller *ast.FuncDecl) bool {
	if helper == caller || l.pkg.FSet.File(helper.Pos()) != l.pkg.FSet.File(caller.Pos()) {
		return false
	}

	if helper.Recv == nil {
		return true
	}

	helperType, ok := receiverTypeName(helper.Recv.List[0].Type)

	if caller.Recv == nil || !ok {
		return false
	}

	callerType, ok := receiverTypeName(caller.Recv.List[0].Type)

	return ok && helperType == callerType
}

// callerVariables counts the caller's parameters and locals that the call
// passes, the state a cut-out piece needs back from its caller.
func (l splitHelperCheck) callerVariables(call *ast.CallExpr, caller *ast.FuncDecl) int {
	seen := make(map[types.Object]bool)

	for _, arg := range call.Args {
		ast.Inspect(arg, func(node ast.Node) bool {
			ident, ok := node.(*ast.Ident)
			if !ok {
				return true
			}

			if obj, ok := l.pkg.TypesInfo.Uses[ident].(*types.Var); ok &&
				obj.Pos() >= caller.Pos() && obj.Pos() < caller.End() {
				seen[obj] = true
			}

			return true
		})
	}

	return len(seen)
}
