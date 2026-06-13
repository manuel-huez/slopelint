package lint

import (
	"go/ast"
	"go/types"
	"os"
	"path/filepath"
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

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	defer func() {
		if err := os.Chdir(oldWD); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	}()

	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	pkgs, err := LoadPackages([]string{allPackagesPattern})
	if err != nil {
		t.Fatalf("load packages: %v", err)
	}

	return LintPackages(pkgs, opts)
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

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	defer func() {
		if err := os.Chdir(oldWD); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	}()

	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	pkgs, err := LoadPackages([]string{allPackagesPattern})
	if err != nil {
		t.Fatalf("load packages: %v", err)
	}

	if len(pkgs) != 1 {
		t.Fatalf("loaded %d packages, want 1", len(pkgs))
	}

	return pkgs[0]
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
