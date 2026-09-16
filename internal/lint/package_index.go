package lint

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
)

type packageIndex struct {
	files            []*ast.File
	productionFiles  []*ast.File
	testFiles        []*ast.File
	testSupportFiles []*ast.File
	productionDecls  []ast.Decl
	productionFuncs  []*ast.FuncDecl
	productionTypes  []*ast.TypeSpec
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

		if pkg.testOnly {
			index.testSupportFiles = append(index.testSupportFiles, file)
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

func classifyTestOnlyPackages(
	targets []*packageMeta,
	byImportPath map[string]*packageMeta,
	patterns []string,
	dir string,
) {
	// Reverse imports are complete only for a module-root ./... scan. Public
	// packages remain production because consumers can exist outside this module.
	if !slices.Contains(patterns, allPackagesPattern) {
		return
	}

	bases := moduleRootPackages(targets, byImportPath, dir)

	var testRoots []string

	for _, base := range bases {
		for _, path := range slices.Concat(base.TestImports, base.XTestImports) {
			// A package's own external tests do not make its API test support.
			if path != base.ImportPath {
				testRoots = append(testRoots, path)
			}
		}
	}

	testReachable := packageImportClosure(bases, testRoots)

	var productionRoots []string

	for path, base := range bases {
		relativePath, withinModule := strings.CutPrefix(path, base.Module.Path+"/")

		internalLibrary := withinModule && base.Name != "main" &&
			strings.Contains("/"+relativePath+"/", "/internal/")
		if !internalLibrary || !testReachable[path] {
			productionRoots = append(productionRoots, path)
		}
	}

	productionReachable := packageImportClosure(bases, productionRoots)

	for _, target := range targets {
		path := target.targetImportPath()
		target.testOnly = testReachable[path] && !productionReachable[path]
	}
}

func moduleRootPackages(
	targets []*packageMeta,
	byImportPath map[string]*packageMeta,
	dir string,
) map[string]*packageMeta {
	absoluteDir, err := filepath.Abs(dir)
	if err != nil {
		return nil
	}

	bases := make(map[string]*packageMeta)

	for _, target := range targets {
		base := byImportPath[target.targetImportPath()]
		if base == nil || base.Module == nil || !base.Module.Main ||
			filepath.Clean(base.Module.Dir) != absoluteDir {
			continue
		}

		bases[base.ImportPath] = base
	}

	return bases
}

func packageImportClosure(packages map[string]*packageMeta, roots []string) map[string]bool {
	reachable := make(map[string]bool)

	for len(roots) > 0 {
		path := roots[len(roots)-1]
		roots = roots[:len(roots)-1]

		pkg := packages[path]
		if pkg == nil || reachable[path] {
			continue
		}

		reachable[path] = true

		roots = append(roots, pkg.Imports...)
	}

	return reachable
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
