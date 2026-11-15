package deadcode

import (
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
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
	// GeneratedDecls holds production decls from generated files. They are
	// never reported, and what they use stays live.
	GeneratedDecls map[ast.Decl]struct{}
	// TrimmedDirs maps -trimpath directories in export-data positions to
	// package directories on disk. Nil when positions are already absolute.
	TrimmedDirs map[string]string
}

// sourceFilename maps a -trimpath position filename to its file on disk.
func (pkg *Package) sourceFilename(filename string) string {
	if filename == "" || filepath.IsAbs(filename) {
		return filename
	}

	if dir, ok := pkg.TrimmedDirs[filepath.Dir(filename)]; ok {
		return filepath.Join(dir, filepath.Base(filename))
	}

	return filename
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
