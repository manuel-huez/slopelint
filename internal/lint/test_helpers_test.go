package lint

import (
	"go/ast"
	"go/types"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	wantFieldPayloadCustom      = `field "Payload.Custom"`
	wantFieldPayloadName        = `field "Payload.Name"`
	wantMethodMarshalJSON       = `method "MarshalJSON"`
	wantMethodMarshalText       = `method "MarshalText"`
	wantMethodMarshalYAML       = `method "MarshalYAML"`
	wantMethodUnmarshalJSON     = `method "UnmarshalJSON"`
	wantMethodUnmarshalText     = `method "UnmarshalText"`
	wantMethodUnmarshalYAML     = `method "UnmarshalYAML"`
	wantRemoveFieldPayloadName  = wantFieldPayloadName + ` is unreachable from repo entrypoints; remove it`
	wantRemoveMethodMarshalJSON = wantMethodMarshalJSON + ` is unreachable from repo entrypoints; remove it`
)

func lintInDir(t *testing.T, dir string) []Issue {
	t.Helper()

	return lintInDirWithOptions(t, dir, Options{MaxStates: 32})
}

func lintInDirWithOptions(t *testing.T, dir string, opts Options) []Issue {
	t.Helper()

	opts.ClosedWorld = true

	return lintLoadedPackages(loadPackagesForTest(t, dir), opts)
}

func newTestModule(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	writeTestGoMod(t, dir, "example.com/sample")

	return dir
}

func newYAMLTestModule(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), `module example.com/sample

go 1.22

require gopkg.in/yaml.v3 v3.0.0

replace gopkg.in/yaml.v3 => ./yaml
`)
	writeTestGoMod(t, filepath.Join(dir, "yaml"), "gopkg.in/yaml.v3")

	return dir
}

func newGoccyJSONTestModule(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), `module example.com/sample

go 1.22

require github.com/goccy/go-json v0.0.0

replace github.com/goccy/go-json => ./gojson
`)
	writeTestGoMod(t, filepath.Join(dir, "gojson"), "github.com/goccy/go-json")

	return dir
}

func newJSONCodecTestModule(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), `module example.com/sample

go 1.22

require example.com/jsoncodec v0.0.0

replace example.com/jsoncodec => ./jsoncodec
`)
	writeTestGoMod(t, filepath.Join(dir, "jsoncodec"), "example.com/jsoncodec")

	return dir
}

func writeTestGoMod(t *testing.T, dir string, modulePath string) {
	t.Helper()

	writeFile(t, filepath.Join(dir, "go.mod"), "module "+modulePath+"\n\ngo 1.22\n")
}

func writeYAMLMarshalStub(t *testing.T, dir string) {
	t.Helper()

	writeFile(t, filepath.Join(dir, "yaml", "yaml.go"), `package yaml

func Marshal(any) ([]byte, error) { return nil, nil }
`)
}

func writeJSONCodecPayloadSave(t *testing.T, dir string) {
	t.Helper()

	writeFile(t, filepath.Join(dir, "lib", "lib.go"), `package lib

import "example.com/jsoncodec"

type Payload struct {
	Name string `+"`json:\"name\"`"+`
}

func Save() ([]byte, error) {
	return jsoncodec.Encode(Payload{})
}
`)
}

func writeTestMain(t *testing.T, dir string, body string) {
	t.Helper()

	writeFile(t, filepath.Join(dir, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
`+body+`
}
`)
}

func writeFile(t *testing.T, path string, contents string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}

	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func joinMessages(issues []Issue) string {
	parts := make([]string, 0, len(issues))
	for _, issue := range issues {
		parts = append(parts, issue.Message)
	}

	return strings.Join(parts, "\n")
}

func issueRelativePaths(t *testing.T, dir string, issues []Issue, kind string) []string {
	t.Helper()

	var paths []string

	for _, issue := range issues {
		if issue.Kind != kind {
			continue
		}

		path, err := filepath.Rel(dir, issuePosition(issue).Filename)
		if err != nil {
			t.Fatal(err)
		}

		paths = append(paths, filepath.ToSlash(path))
	}

	slices.Sort(paths)

	return paths
}

func hasIssueKind(issues []Issue, kind string) bool {
	for _, issue := range issues {
		if issue.Kind == kind {
			return true
		}
	}

	return false
}

func loadOnePackageForTest(t *testing.T, dir string) *LoadedPackage {
	t.Helper()

	pkgs := loadPackagesForTest(t, dir)

	if len(pkgs) != 1 {
		t.Fatalf("loaded %d packages, want 1", len(pkgs))
	}

	return pkgs[0]
}

func loadPackagesForTest(t *testing.T, dir string) []*LoadedPackage {
	t.Helper()

	targets, byImportPath, err := resolvePackageMetadata([]string{allPackagesPattern}, dir)
	if err != nil {
		t.Fatalf("resolve packages: %v", err)
	}

	loadContext := &packageLoadContext{byImportPath: byImportPath}

	pkgs := make([]*LoadedPackage, len(targets))

	err = runPackageJobs(len(targets), func(index int) error {
		pkg, loadErr := loadOne(targets[index], loadContext)
		pkgs[index] = pkg

		return loadErr
	})
	if err != nil {
		t.Fatalf("load packages: %v", err)
	}

	return pkgs
}

func lintLoadedPackages(pkgs []*LoadedPackage, opts Options) []Issue {
	inputs := make([]repoPackageInput, 0, len(pkgs))
	for _, pkg := range pkgs {
		if pkg == nil {
			continue
		}

		imports := make([]string, 0, len(pkg.TypesPkg.Imports()))
		for _, imported := range pkg.TypesPkg.Imports() {
			imports = append(imports, imported.Path())
		}

		files, err := analysisCacheSourceFiles(pkg.repoFiles, pkg.Dir)
		inputs = append(inputs, repoPackageInput{
			testOnly:   pkg.testOnly,
			importPath: pkg.ImportPath,
			name:       pkg.Name,
			dir:        pkg.Dir,
			imports:    imports,
			files:      files,
			sourceErr:  err,
			pkg:        pkg,
		})
	}

	issues, _ := lintRepoPackageInputs(
		inputs,
		opts,
		nil,
		analysisCacheTypeDigests(pkgs),
	)

	return issues
}

func firstRangeStmt(files []*ast.File) *ast.RangeStmt {
	var out *ast.RangeStmt

	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			if out != nil {
				return false
			}

			stmt, ok := n.(*ast.RangeStmt)
			if !ok {
				return true
			}

			out = stmt

			return false
		})
	}

	return out
}

func namedFuncDecl(files []*ast.File, name string) *ast.FuncDecl {
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Name.Name == name {
				return fn
			}
		}
	}

	return nil
}

func namedObject(files []*ast.File, info *types.Info, name string) *types.Var {
	for _, file := range files {
		var out *types.Var

		ast.Inspect(file, func(n ast.Node) bool {
			if out != nil {
				return false
			}

			ident, ok := n.(*ast.Ident)
			if !ok || ident.Name != name {
				return true
			}

			obj, ok := info.ObjectOf(ident).(*types.Var)
			if !ok || obj == nil {
				return true
			}

			out = obj

			return false
		})

		if out != nil {
			return out
		}
	}

	return nil
}
