package lint

import (
	"go/ast"
	"go/token"
	"strings"
)

type packageIndex struct {
	files           []*ast.File
	productionFiles []*ast.File
	testFiles       []*ast.File
	productionDecls []ast.Decl
	productionFuncs []*ast.FuncDecl
	productionTypes []*ast.TypeSpec
}

func newPackageIndex(pkg *LoadedPackage) packageIndex {
	if pkg == nil {
		return packageIndex{}
	}

	index := packageIndex{
		files:           make([]*ast.File, 0, len(pkg.Files)),
		productionFiles: make([]*ast.File, 0, len(pkg.Files)),
		testFiles:       make([]*ast.File, 0),
	}

	for _, file := range pkg.Files {
		if file == nil {
			continue
		}

		index.files = append(index.files, file)
		if packageFileIsTest(pkg.FSet, file) {
			index.testFiles = append(index.testFiles, file)
			continue
		}

		index.productionFiles = append(index.productionFiles, file)
		for _, decl := range file.Decls {
			index.productionDecls = append(index.productionDecls, decl)

			switch decl := decl.(type) {
			case *ast.FuncDecl:
				index.productionFuncs = append(index.productionFuncs, decl)
			case *ast.GenDecl:
				if decl.Tok == token.TYPE {
					for _, spec := range decl.Specs {
						typeSpec, ok := spec.(*ast.TypeSpec)
						if ok {
							index.productionTypes = append(index.productionTypes, typeSpec)
						}
					}
				}
			}
		}
	}

	return index
}

func packageFileIsTest(fset *token.FileSet, file *ast.File) bool {
	if file == nil {
		return false
	}

	posFile := fset.File(file.Pos())
	if posFile == nil {
		return false
	}

	return strings.HasSuffix(posFile.Name(), "_test.go")
}
