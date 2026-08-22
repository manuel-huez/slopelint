package lint

import (
	"go/ast"
	"go/token"
	"go/types"
)

// LoadedPackage contains all information the linter needs for one checked package.
type LoadedPackage struct {
	ImportPath string
	Name       string
	Dir        string
	FSet       *token.FileSet
	Files      []*ast.File
	TypesPkg   *types.Package
	TypesInfo  *types.Info
	repoFiles  []string
	buildID    string
}
