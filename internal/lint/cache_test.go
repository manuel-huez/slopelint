package lint

import (
	"encoding/json"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/analysis"
)

const analysisCacheDiagnosticSource = `package sample

func f(s string) {
	if s == "" { return }
	if s == "" { println("bad") }
}
`

func writeAnalysisCacheDiagnosticFile(t *testing.T, path string) {
	t.Helper()

	writeFile(t, path, analysisCacheDiagnosticSource)
}

func TestAnalysisCacheSchemaIncludesConstValueTestRule(t *testing.T) {
	t.Parallel()

	root, err := analysisCacheRoot(t.TempDir())
	if err != nil {
		t.Fatalf("analysisCacheRoot: %v", err)
	}

	if !strings.HasSuffix(root, "analysis-v9") {
		t.Fatalf("analysis cache root = %q, want schema 8 suffix", root)
	}
}

func TestRunAnalysisCachesUnchangedPackage(t *testing.T) {
	tmp := newTestModule(t)
	cacheDir := t.TempDir()
	writeAnalysisCacheDiagnosticFile(t, filepath.Join(tmp, "sample.go"))

	pkgs := loadPackagesForTest(t, tmp)
	pkg := mustPackage(t, pkgs, "example.com/sample")

	pass1, state1 := newAnalysisTestPass(pkg, nil)

	issues1, err := RunAnalysis(pass1, Options{
		MaxStates:    32,
		CacheEnabled: true,
		cacheDir:     cacheDir,
	})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}

	var hits []string

	pass2, state2 := newAnalysisTestPass(pkg, nil)

	issues2, err := RunAnalysis(pass2, Options{
		MaxStates:    32,
		CacheEnabled: true,
		cacheDir:     cacheDir,
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

func TestLintRepositoryCachesStandaloneResult(t *testing.T) {
	tmp := newTestModule(t)
	cacheDir := t.TempDir()
	writeAnalysisCacheDiagnosticFile(t, filepath.Join(tmp, "sample.go"))

	issues1, err := LintRepository([]string{allPackagesPattern}, tmp, Options{
		MaxStates:    32,
		CacheEnabled: true,
		cacheDir:     cacheDir,
	}, nil)
	if err != nil {
		t.Fatalf("first repo lint: %v", err)
	}

	digest1 := joinMessages(issues1)
	if !strings.Contains(digest1, `condition "s == \"\"" is always false here`) {
		t.Fatalf("expected first run diagnostic, got:\n%s", digest1)
	}

	var hits []string

	issues2, err := LintRepository([]string{allPackagesPattern}, tmp, Options{
		MaxStates:    32,
		CacheEnabled: true,
		cacheDir:     cacheDir,
		CacheHitHook: func(importPath string) {
			hits = append(hits, importPath)
		},
	}, nil)
	if err != nil {
		t.Fatalf("cached repo lint: %v", err)
	}

	if len(hits) != 1 || hits[0] != repoAnalysisCacheHitName {
		t.Fatalf("expected standalone repo cache hit, got %v", hits)
	}

	if got := joinMessages(issues2); got != digest1 {
		t.Fatalf("cached issues changed:\nwant:\n%s\n\ngot:\n%s", digest1, got)
	}

	if len(issues2) == 0 || !strings.Contains(FormatIssuePosition(issues2[0]), "sample.go:") {
		t.Fatalf("cached issue lost source position: %#v", issues2)
	}
}

func TestLintRepositoryCacheHitSkipsMaintenanceSweep(t *testing.T) {
	tmp := newTestModule(t)
	cacheDir := t.TempDir()
	writeAnalysisCacheDiagnosticFile(t, filepath.Join(tmp, "sample.go"))

	opts := Options{MaxStates: 32, CacheEnabled: true, cacheDir: cacheDir}
	if _, err := LintRepository([]string{allPackagesPattern}, tmp, opts, nil); err != nil {
		t.Fatalf("first repo lint: %v", err)
	}

	marker := filepath.Join(cacheDir, cachePruneMarkerName)
	if err := os.Remove(marker); err != nil {
		t.Fatalf("remove maintenance marker: %v", err)
	}

	if _, err := LintRepository([]string{allPackagesPattern}, tmp, opts, nil); err != nil {
		t.Fatalf("cached repo lint: %v", err)
	}

	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("hot cache hit ran maintenance: %v", err)
	}
}

func TestLintRepositoryCacheSharesLinkedWorktree(t *testing.T) {
	primary := newTestModule(t)
	cacheDir := t.TempDir()
	writeAnalysisCacheDiagnosticFile(t, filepath.Join(primary, "sample.go"))
	initTestGitRepository(t, primary)

	first, err := LintRepository(
		[]string{allPackagesPattern},
		primary,
		Options{MaxStates: 32, CacheEnabled: true, cacheDir: cacheDir},
		nil,
	)
	if err != nil {
		t.Fatalf("primary lint: %v", err)
	}

	linked := addTestGitWorktree(t, primary)

	var hits []string

	second, err := LintRepository(
		[]string{allPackagesPattern},
		linked,
		Options{
			MaxStates:    32,
			CacheEnabled: true,
			cacheDir:     cacheDir,
			CacheHitHook: func(name string) { hits = append(hits, name) },
		},
		nil,
	)
	if err != nil {
		t.Fatalf("linked lint: %v", err)
	}

	if !slices.Equal(hits, []string{repoAnalysisCacheHitName}) {
		t.Fatalf("linked worktree cache hits = %v, want repo", hits)
	}

	if joinMessages(second) != joinMessages(first) {
		t.Fatalf("linked worktree diagnostics changed")
	}

	linkedPrefix := linked + string(filepath.Separator)
	if got := FormatIssuePosition(second[0]); !strings.HasPrefix(got, linkedPrefix) {
		t.Fatalf("linked diagnostic path = %q, want prefix %q", got, linked)
	}
}

func TestStandalonePackageCacheSurvivesSourceRename(t *testing.T) {
	tmp := newTestModule(t)
	cacheDir := t.TempDir()
	original := filepath.Join(tmp, "original.go")
	renamed := filepath.Join(tmp, "renamed.go")

	writeAnalysisCacheDiagnosticFile(t, original)

	opts := Options{MaxStates: 32, CacheEnabled: true, cacheDir: cacheDir}

	first := lintLoadedPackages(loadPackagesForTest(t, tmp), opts)
	if err := os.Rename(original, renamed); err != nil {
		t.Fatalf("rename source: %v", err)
	}

	var hits []string

	opts.CacheHitHook = func(importPath string) { hits = append(hits, importPath) }
	second := lintLoadedPackages(loadPackagesForTest(t, tmp), opts)

	if !slices.Equal(hits, []string{"example.com/sample"}) {
		t.Fatalf("renamed package cache hits = %v", hits)
	}

	if joinMessages(second) != joinMessages(first) {
		t.Fatalf("renamed source diagnostics changed")
	}

	if got := FormatIssuePosition(second[0]); !strings.HasPrefix(got, renamed+":") {
		t.Fatalf("renamed diagnostic path = %q, want %q", got, renamed)
	}
}

func TestLintRepositoryCacheTracksDependencies(t *testing.T) {
	const usePackagePattern = "./use"

	tmp := newTestModule(t)
	cacheDir := t.TempDir()
	writeFile(t, filepath.Join(tmp, "dep", "dep.go"), `package dep

type Item struct { Name string }
`)
	writeFile(t, filepath.Join(tmp, "use", "use.go"), `package use

import "example.com/sample/dep"

func Name(item dep.Item) string { return item.Name }
`)

	opts := Options{MaxStates: 32, CacheEnabled: true, cacheDir: cacheDir}
	if _, err := LintRepository([]string{usePackagePattern}, tmp, opts, nil); err != nil {
		t.Fatalf("first repo lint: %v", err)
	}

	var hits []string

	opts.CacheHitHook = func(name string) { hits = append(hits, name) }
	if _, err := LintRepository([]string{usePackagePattern}, tmp, opts, nil); err != nil {
		t.Fatalf("cached repo lint: %v", err)
	}

	if len(hits) != 1 || hits[0] != repoAnalysisCacheHitName {
		t.Fatalf("unchanged dependency cache hits = %v, want repo", hits)
	}

	writeFile(t, filepath.Join(tmp, "dep", "dep.go"), `package dep

type Item struct {
	Name string
	Age int
}
`)

	hits = nil

	if _, err := LintRepository([]string{usePackagePattern}, tmp, opts, nil); err != nil {
		t.Fatalf("dependency-changed repo lint: %v", err)
	}

	if len(hits) != 0 {
		t.Fatalf("dependency export change cache hits = %v, want none", hits)
	}
}

func TestStandalonePackageCacheRecomputesOnlyAffectedPackages(t *testing.T) {
	tmp := newTestModule(t)
	cacheDir := t.TempDir()
	depPath := filepath.Join(tmp, "dep", "dep.go")

	writeFile(t, depPath, `package dep

type Value struct { Name string }

func Present(value *Value) bool { return value != nil }
`)
	writeFile(t, filepath.Join(tmp, "use", "use.go"), `package use

import "example.com/sample/dep"

func Name(value *dep.Value) string {
	if !dep.Present(value) { return "" }
	if value == nil { return "" }
	return value.Name
}
`)
	writeFile(t, filepath.Join(tmp, "other", "other.go"), `package other

func Value() int { return 1 }
`)

	opts := Options{MaxStates: 32, CacheEnabled: true, cacheDir: cacheDir}

	first, err := LintRepository([]string{allPackagesPattern}, tmp, opts, nil)
	if err != nil {
		t.Fatalf("first repo lint: %v", err)
	}

	writeFile(t, depPath, `// Package dep owns shared value guards.
package dep

type Value struct { Name string }

func Present(value *Value) bool { return value != nil }
`)

	var hits []string

	opts.CacheHitHook = func(importPath string) { hits = append(hits, importPath) }

	second, err := LintRepository([]string{allPackagesPattern}, tmp, opts, nil)
	if err != nil {
		t.Fatalf("comment-edit repo lint: %v", err)
	}

	sort.Strings(hits)

	wantHits := []string{"example.com/sample/other", "example.com/sample/use"}
	if !slices.Equal(hits, wantHits) {
		t.Fatalf("comment edit cache hits = %v, want %v", hits, wantHits)
	}

	if joinMessages(first) != joinMessages(second) {
		t.Fatalf(
			"comment edit changed diagnostics:\nfirst:\n%s\n\nsecond:\n%s",
			joinMessages(first),
			joinMessages(second),
		)
	}

	writeFile(t, depPath, `package dep

type Value struct { Name string }

func Present(value *Value) bool { return value != nil && value.Name != "" }
`)

	hits = nil

	if _, err := LintRepository([]string{allPackagesPattern}, tmp, opts, nil); err != nil {
		t.Fatalf("summary-edit repo lint: %v", err)
	}

	if !slices.Equal(hits, []string{"example.com/sample/other"}) {
		t.Fatalf("summary edit cache hits = %v, want only unrelated package", hits)
	}
}

func initTestGitRepository(t *testing.T, dir string) string {
	t.Helper()

	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "slopelint@example.com"},
		{"config", "user.name", "slopelint test"},
	} {
		cmd := exec.Command("git", args...)

		cmd.Dir = dir
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}

	commitTestGitRepository(t, dir, ".", "seed")

	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir

	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("git HEAD: %v", err)
	}

	return strings.TrimSpace(string(output))
}

func addTestGitWorktree(t *testing.T, primary string) string {
	t.Helper()

	linked := filepath.Join(t.TempDir(), "linked")
	add := exec.Command("git", "worktree", "add", "--detach", linked, "HEAD")

	add.Dir = primary
	if output, err := add.CombinedOutput(); err != nil {
		t.Fatalf("add linked worktree: %v: %s", err, output)
	}

	t.Cleanup(func() {
		remove := exec.Command("git", "worktree", "remove", "--force", linked)

		remove.Dir = primary
		if output, err := remove.CombinedOutput(); err != nil {
			t.Errorf("remove linked worktree: %v: %s", err, output)
		}
	})

	return linked
}

func commitTestGitRepository(t *testing.T, dir, path, message string) {
	t.Helper()

	for _, args := range [][]string{{"add", path}, {"commit", "-m", message}} {
		cmd := exec.Command("git", args...)

		cmd.Dir = dir
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
}

func TestRunAnalysisInvalidatesCacheWhenImportedFactsChange(t *testing.T) {
	tmp := newTestModule(t)
	cacheDir := t.TempDir()
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

	pkgs := loadPackagesForTest(t, tmp)
	guardPkg := mustPackage(t, pkgs, "example.com/sample/guard")
	usePkg := mustPackage(t, pkgs, "example.com/sample/use")

	guardPass1, guardState1 := newAnalysisTestPass(guardPkg, nil)
	if _, err := RunAnalysis(guardPass1, Options{
		MaxStates:    32,
		CacheEnabled: true,
		cacheDir:     cacheDir,
	}); err != nil {
		t.Fatalf("guard first run: %v", err)
	}

	usePass1, _ := newAnalysisTestPass(usePkg, guardState1.exported)

	issues1, err := RunAnalysis(usePass1, Options{
		MaxStates:    32,
		CacheEnabled: true,
		cacheDir:     cacheDir,
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

	pkgs = loadPackagesForTest(t, tmp)
	guardPkg = mustPackage(t, pkgs, "example.com/sample/guard")
	usePkg = mustPackage(t, pkgs, "example.com/sample/use")

	guardPass2, guardState2 := newAnalysisTestPass(guardPkg, nil)
	if _, err := RunAnalysis(guardPass2, Options{
		MaxStates:    32,
		CacheEnabled: true,
		cacheDir:     cacheDir,
	}); err != nil {
		t.Fatalf("guard second run: %v", err)
	}

	usePass2, _ := newAnalysisTestPass(usePkg, guardState2.exported)

	issues2, err := RunAnalysis(usePass2, Options{
		MaxStates:    32,
		CacheEnabled: true,
		cacheDir:     cacheDir,
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

func mustPackage(t *testing.T, pkgs []*LoadedPackage, importPath string) *LoadedPackage {
	t.Helper()

	for _, pkg := range pkgs {
		if pkg.ImportPath == importPath {
			return pkg
		}
	}

	t.Fatalf("missing package %s", importPath)

	return nil
}

func issuesDigest(fset *token.FileSet, issues []Issue) string {
	sortIssues(issues)

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
