package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRunAnalysisInvalidatesCacheWhenImportedFactsChange(t *testing.T) {
	t.Parallel()

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
