package lint

import (
	"encoding/json"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/analysis"
)

func TestRunAnalysisCachesUnchangedPackage(t *testing.T) {
	tmp := t.TempDir()
	cacheDir := t.TempDir()

	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func f(s string) {
	if s == "" { return }
	if s == "" { println("bad") }
}
`)

	pkgs := loadPackagesInDir(t, tmp)
	pkg := mustPackage(t, pkgs, "example.com/sample")

	pass1, state1 := newAnalysisTestPass(pkg, nil)

	issues1, err := RunAnalysis(pass1, Options{
		MaxStates:    32,
		CacheEnabled: true,
		CacheDir:     cacheDir,
	})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}

	var hits []string

	pass2, state2 := newAnalysisTestPass(pkg, nil)

	issues2, err := RunAnalysis(pass2, Options{
		MaxStates:    32,
		CacheEnabled: true,
		CacheDir:     cacheDir,
		CacheHitHook: func(importPath string) {
			hits = append(hits, importPath)
		},
	})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}

	if len(hits) != 1 || hits[0] != pkg.ImportPath {
		t.Fatalf("expected cache hit for %s, got %v", pkg.ImportPath, hits)
	}

	if got, want := issuesDigest(pkg.FSet, issues2), issuesDigest(pkg.FSet, issues1); got != want {
		t.Fatalf("cached issues changed:\nwant:\n%s\n\ngot:\n%s", want, got)
	}

	if got, want := factsDigest(state2.exported), factsDigest(state1.exported); got != want {
		t.Fatalf("cached exported facts changed:\nwant:\n%s\n\ngot:\n%s", want, got)
	}

	if entries := countCacheEntries(t, cacheDir); entries == 0 {
		t.Fatalf("expected cache files in %s", cacheDir)
	}
}

func TestRunAnalysisInvalidatesCacheWhenImportedFactsChange(t *testing.T) {
	tmp := t.TempDir()
	cacheDir := t.TempDir()

	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "guard", "guard.go"), `package guard

import "errors"

type Req struct { Name string }

func Valid(req *Req) bool {
	if req == nil { return false }
	if req.Name == "" { return false }
	return true
}

func Check(req *Req) error {
	if req == nil { return errors.New("bad") }
	if req.Name == "" { return errors.New("bad") }
	return nil
}
`)
	writeFile(t, filepath.Join(tmp, "use", "use.go"), `package use

import "example.com/sample/guard"

func f(req *guard.Req) {
	ok := guard.Valid(req)
	if !ok { return }
	if req == nil { println("bad") }
	if req.Name == "" { println("bad") }
}
`)

	pkgs := loadPackagesInDir(t, tmp)
	guardPkg := mustPackage(t, pkgs, "example.com/sample/guard")
	usePkg := mustPackage(t, pkgs, "example.com/sample/use")

	guardPass1, guardState1 := newAnalysisTestPass(guardPkg, nil)
	if _, err := RunAnalysis(guardPass1, Options{
		MaxStates:    32,
		CacheEnabled: true,
		CacheDir:     cacheDir,
	}); err != nil {
		t.Fatalf("guard first run: %v", err)
	}

	usePass1, _ := newAnalysisTestPass(usePkg, guardState1.exported)

	issues1, err := RunAnalysis(usePass1, Options{
		MaxStates:    32,
		CacheEnabled: true,
		CacheDir:     cacheDir,
	})
	if err != nil {
		t.Fatalf("use first run: %v", err)
	}

	firstDigest := issuesDigest(usePkg.FSet, issues1)
	if !strings.Contains(firstDigest, `condition "req.Name == \"\"" is always false here`) {
		t.Fatalf("expected name diagnostic in first run:\n%s", firstDigest)
	}

	writeFile(t, filepath.Join(tmp, "guard", "guard.go"), `package guard

import "errors"

type Req struct { Name string }

func Valid(req *Req) bool {
	if req == nil { return false }
	return true
}

func Check(req *Req) error {
	if req == nil { return errors.New("bad") }
	return nil
}
`)

	pkgs = loadPackagesInDir(t, tmp)
	guardPkg = mustPackage(t, pkgs, "example.com/sample/guard")
	usePkg = mustPackage(t, pkgs, "example.com/sample/use")

	guardPass2, guardState2 := newAnalysisTestPass(guardPkg, nil)
	if _, err := RunAnalysis(guardPass2, Options{
		MaxStates:    32,
		CacheEnabled: true,
		CacheDir:     cacheDir,
	}); err != nil {
		t.Fatalf("guard second run: %v", err)
	}

	usePass2, _ := newAnalysisTestPass(usePkg, guardState2.exported)

	issues2, err := RunAnalysis(usePass2, Options{
		MaxStates:    32,
		CacheEnabled: true,
		CacheDir:     cacheDir,
		CacheHitHook: func(importPath string) {
			if importPath == usePkg.ImportPath {
				t.Fatalf("expected imported-fact change to invalidate use cache")
			}
		},
	})
	if err != nil {
		t.Fatalf("use second run: %v", err)
	}

	secondDigest := issuesDigest(usePkg.FSet, issues2)
	if strings.Contains(secondDigest, `condition "req.Name == \"\"" is always false here`) {
		t.Fatalf(
			"expected cached diagnostics to be invalidated after imported fact change:\n%s",
			secondDigest,
		)
	}

	if !strings.Contains(secondDigest, `condition "req == nil" is always false here`) {
		t.Fatalf("expected nil diagnostic to remain after imported fact change:\n%s", secondDigest)
	}
}

type analysisTestState struct {
	exported map[string]*callSummaryFact
}

func newAnalysisTestPass(
	pkg *LoadedPackage,
	imported map[string]*callSummaryFact,
) (*analysis.Pass, *analysisTestState) {
	state := &analysisTestState{
		exported: make(map[string]*callSummaryFact),
	}

	pass := &analysis.Pass{
		Fset:      pkg.FSet,
		Files:     pkg.Files,
		Pkg:       pkg.TypesPkg,
		TypesInfo: pkg.TypesInfo,
		Report:    func(analysis.Diagnostic) {},
		ReadFile:  os.ReadFile,
		ImportObjectFact: func(obj types.Object, fact analysis.Fact) bool {
			fn, ok := obj.(*types.Func)
			if !ok || fn == nil {
				return false
			}

			cached := imported[funcObjectKey(fn)]

			dst, ok := fact.(*callSummaryFact)
			if !ok || cached == nil {
				return false
			}

			*dst = *cloneCallSummaryFact(cached)

			return true
		},
		ExportObjectFact: func(obj types.Object, fact analysis.Fact) {
			fn, ok := obj.(*types.Func)
			if !ok || fn == nil {
				return
			}

			summary, ok := fact.(*callSummaryFact)
			if !ok || summary == nil {
				return
			}

			state.exported[funcObjectKey(fn)] = cloneCallSummaryFact(summary)
		},
		AllObjectFacts: func() []analysis.ObjectFact {
			if len(imported) == 0 {
				return nil
			}

			referenced := referencedImportedFuncs(pkg, imported)

			out := make([]analysis.ObjectFact, 0, len(referenced))
			for key, fn := range referenced {
				out = append(out, analysis.ObjectFact{
					Object: fn,
					Fact:   cloneCallSummaryFact(imported[key]),
				})
			}

			return out
		},
	}

	return pass, state
}

func referencedImportedFuncs(
	pkg *LoadedPackage,
	imported map[string]*callSummaryFact,
) map[string]*types.Func {
	out := make(map[string]*types.Func)

	for _, file := range pkg.Files {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}

			recordImportedFunc(out, imported, referencedFuncForCall(pkg, call))

			return true
		})
	}

	return out
}

func referencedFuncForCall(pkg *LoadedPackage, call *ast.CallExpr) *types.Func {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return funcObject(pkg.TypesInfo.ObjectOf(fun))
	case *ast.SelectorExpr:
		if sel := pkg.TypesInfo.Selections[fun]; sel != nil {
			return funcObject(sel.Obj())
		}

		return funcObject(pkg.TypesInfo.Uses[fun.Sel])
	default:
		return nil
	}
}

func recordImportedFunc(
	out map[string]*types.Func,
	imported map[string]*callSummaryFact,
	fn *types.Func,
) {
	if fn == nil {
		return
	}

	key := funcObjectKey(fn)
	if imported[key] == nil {
		return
	}

	out[key] = fn
}

func funcObject(obj types.Object) *types.Func {
	fn, ok := obj.(*types.Func)
	if !ok || fn == nil {
		return nil
	}

	return fn
}

func loadPackagesInDir(t *testing.T, dir string) []*LoadedPackage {
	t.Helper()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}

	defer func() {
		if err := os.Chdir(wd); err != nil {
			t.Fatalf("restore cwd %s: %v", wd, err)
		}
	}()

	pkgs, err := LoadPackages([]string{"./..."})
	if err != nil {
		t.Fatalf("load packages in %s: %v", dir, err)
	}

	return pkgs
}

func mustPackage(t *testing.T, pkgs []*LoadedPackage, importPath string) *LoadedPackage {
	t.Helper()

	for _, pkg := range pkgs {
		if pkg.ImportPath == importPath {
			return pkg
		}
	}

	t.Fatalf("missing package %s", importPath)
	panic("unreachable")
}

func issuesDigest(fset *token.FileSet, issues []Issue) string {
	sortIssues(fset, issues)

	lines := make([]string, 0, len(issues))
	for _, issue := range issues {
		pos := fset.Position(issue.Pos)
		lines = append(lines, pos.Filename+":"+issue.Message)
	}

	return strings.Join(lines, "\n")
}

func factsDigest(facts map[string]*callSummaryFact) string {
	if len(facts) == 0 {
		return ""
	}

	keys := make([]string, 0, len(facts))
	for key := range facts {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		data, err := json.Marshal(cloneCallSummaryFact(facts[key]))
		if err != nil {
			lines = append(lines, key+":marshal-error")
			continue
		}

		lines = append(lines, key+":"+string(data))
	}

	return strings.Join(lines, "\n")
}

func countCacheEntries(t *testing.T, dir string) int {
	t.Helper()

	count := 0

	err := filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if !d.IsDir() && strings.HasSuffix(d.Name(), ".json") {
			count++
		}

		return nil
	})
	if err != nil {
		t.Fatalf("walk cache dir %s: %v", dir, err)
	}

	return count
}
