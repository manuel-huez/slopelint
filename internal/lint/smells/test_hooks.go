package smells

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
)

type functionVarDecl struct {
	name string
	pos  token.Pos
}

func (l *Runner) checkTestGlobalFuncStubs() {
	functionVars := l.productionFunctionVars()
	if len(functionVars) == 0 {
		return
	}

	for obj, assignPos := range l.testAssignedFunctionVars(functionVars) {
		decl := functionVars[obj]

		l.report(
			decl.pos,
			"test_global_func_stub",
			fmt.Sprintf(
				`package-level function variable %q is reassigned in tests at %s; pass dependency through the owning API instead`,
				decl.name,
				l.positionText(assignPos),
			),
		)
	}
}

func (l *Runner) productionFunctionVars() map[types.Object]functionVarDecl {
	out := make(map[types.Object]functionVarDecl)

	l.forEachProductionDecl(func(decl ast.Decl) {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			return
		}

		for _, spec := range gen.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}

			for _, name := range valueSpec.Names {
				if name == nil || ast.IsExported(name.Name) {
					continue
				}

				obj, ok := l.pkg.TypesInfo.Defs[name].(*types.Var)
				if !ok || obj == nil || !typeIsFunctionSignature(obj.Type()) {
					continue
				}

				out[obj] = functionVarDecl{name: name.Name, pos: name.Pos()}
			}
		}
	})

	return out
}

func (l *Runner) testAssignedFunctionVars(
	functionVars map[types.Object]functionVarDecl,
) map[types.Object]token.Pos {
	out := make(map[types.Object]token.Pos)

	for _, file := range l.pkg.Files {
		if !l.fileIsTest(file) {
			continue
		}

		ast.Inspect(file, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok || assign.Tok != token.ASSIGN {
				return true
			}

			for _, lhs := range assign.Lhs {
				ident, ok := l.unparen(lhs).(*ast.Ident)
				if !ok || ident == nil {
					continue
				}

				obj := l.pkg.TypesInfo.ObjectOf(ident)
				if _, ok := functionVars[obj]; !ok {
					continue
				}

				if _, seen := out[obj]; !seen {
					out[obj] = ident.Pos()
				}
			}

			return true
		})
	}

	return out
}

func (l *Runner) checkTestFatalPanics() {
	for _, file := range l.pkg.TestFiles {
		ast.Inspect(file, func(node ast.Node) bool {
			block, ok := node.(*ast.BlockStmt)
			if !ok {
				return true
			}

			for index := 1; index < len(block.List); index++ {
				if !l.testingFatalStmt(block.List[index-1]) {
					continue
				}

				panicCall, ok := exprStmtCall(block.List[index])
				if !ok || !l.isBuiltinCall(panicCall, panicText) {
					continue
				}

				l.report(
					panicCall.Pos(),
					"test_fatal_panic",
					`panic after testing fatal call is unreachable test ceremony; return the required zero value instead`,
				)
			}

			return true
		})
	}
}

func (l *Runner) testingFatalStmt(stmt ast.Stmt) bool {
	call, ok := exprStmtCall(stmt)
	if !ok {
		return false
	}

	fn, _, ok := l.calledFunc(call)

	return ok && fn.Pkg() != nil && fn.Pkg().Path() == "testing" &&
		(fn.Name() == "Fatal" || fn.Name() == "Fatalf")
}

func exprStmtCall(stmt ast.Stmt) (*ast.CallExpr, bool) {
	exprStmt, ok := stmt.(*ast.ExprStmt)
	if !ok {
		return nil, false
	}

	call, ok := ast.Unparen(exprStmt.X).(*ast.CallExpr)

	return call, ok
}
