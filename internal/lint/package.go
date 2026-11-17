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
	testOnly   bool
	// importMap maps source imports to the go list test variants they load.
	importMap map[string]string
	// trimmedDirs maps -trimpath export-data directories to package dirs.
	trimmedDirs map[string]string
}
