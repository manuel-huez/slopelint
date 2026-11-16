package smells

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"
)

type functionVarDecl struct {
	name string
	pos  token.Pos
}

// testSupportCheck reports test code that stubs package function variables,
// panics after a fatal call, or shares helpers without a test_support filename.
type testSupportCheck struct {
	ruleEnv
}

func (l testSupportCheck) checkGlobalFuncStubs() {
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

func (l testSupportCheck) productionFunctionVars() map[types.Object]functionVarDecl {
	out := make(map[types.Object]functionVarDecl)

	for _, decl := range l.pkg.ProductionDecls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}

		for _, spec := range gen.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}

			for _, name := range valueSpec.Names {
				if ast.IsExported(name.Name) {
					continue
				}

				obj, ok := l.pkg.TypesInfo.Defs[name].(*types.Var)
				if !ok || !typeIsFunctionSignature(obj.Type()) {
					continue
				}

				out[obj] = functionVarDecl{name: name.Name, pos: name.Pos()}
			}
		}
	}

	return out
}

func (l testSupportCheck) testAssignedFunctionVars(
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
				ident, ok := ast.Unparen(lhs).(*ast.Ident)
				if !ok {
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

func (l testSupportCheck) checkFatalPanics() {
	for _, file := range l.pkg.TestFiles {
		ast.Inspect(file, func(node ast.Node) bool {
			block, ok := node.(*ast.BlockStmt)
			if !ok {
				return true
			}

			for index := 1; index < len(block.List); index++ {
				if !isTestingFatalStmt(l.pkg.TypesInfo, block.List[index-1]) {
					continue
				}

				panicCall, ok := exprStmtCall(block.List[index])
				if !ok || !isBuiltinCall(l.pkg.TypesInfo, panicCall, panicText) {
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

func isTestingFatalStmt(info *types.Info, stmt ast.Stmt) bool {
	call, ok := exprStmtCall(stmt)
	if !ok {
		return false
	}

	fn, _, ok := calledFunc(info, call)

	return ok && fn.Pkg() != nil && fn.Pkg().Path() == testingImportPath &&
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

func (l testSupportCheck) checkFilenames() {
	for _, file := range l.pkg.TestSupportFiles {
		if ast.IsGenerated(file) {
			continue
		}

		name := strings.TrimSuffix(filepath.Base(l.fileName(file)), ".go")
		// A delimited marker also permits Go's trailing OS and architecture suffixes.
		if strings.Contains("_"+name+"_", "_test_support_") {
			continue
		}

		l.report(
			file.Package,
			"test_support_filename",
			"shared test-only source must use a test_support filename marker (for example helpers_test_support.go); keep .go so other packages' tests can import it",
		)
	}
}
