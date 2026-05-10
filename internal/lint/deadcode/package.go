package deadcode

import (
	"go/ast"
	"go/token"
	"go/types"
)

const (
	initFuncName = "init"
	mainPkgName  = "main"
)

// Finding is one dead-code diagnostic with its source file set.
type Finding struct {
	Pos     token.Pos
	Kind    string
	Message string
	FSet    *token.FileSet
}

// Package contains the package data needed by dead-code reachability.
type Package struct {
	ImportPath      string
	Name            string
	FSet            *token.FileSet
	TypesPkg        *types.Package
	TypesInfo       *types.Info
	ProductionDecls []ast.Decl
	ProductionFuncs []*ast.FuncDecl
}

type packageLinter struct {
	pkg *Package
}

func newPackageLinter(pkg *Package) *packageLinter {
	return &packageLinter{pkg: pkg}
}

func (l *packageLinter) forEachProductionDecl(fn func(ast.Decl)) {
	for _, decl := range l.pkg.ProductionDecls {
		fn(decl)
	}
}

func (l *packageLinter) forEachProductionFunc(fn func(*ast.FuncDecl)) {
	for _, decl := range l.pkg.ProductionFuncs {
		fn(decl)
	}
}

func exprHasCalls(expr ast.Expr) bool {
	hasCall := false

	ast.Inspect(expr, func(n ast.Node) bool {
		if _, ok := n.(*ast.CallExpr); ok {
			hasCall = true

			return false
		}

		return true
	})

	return hasCall
}
