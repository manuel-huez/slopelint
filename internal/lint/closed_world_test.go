package lint

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestRepoDeadCodeRequiresClosedWorld(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

func main() {}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

func PublicAPI() {}
`)

	pkgs := loadPackagesForTest(t, tmp)

	openWorld := joinMessages(lintLoadedPackages(pkgs, Options{MaxStates: 32}))
	if strings.Contains(openWorld, `exported function "PublicAPI"`) {
		t.Fatalf("open-world analysis reported exported API dead:\n%s", openWorld)
	}

	pkgs = loadPackagesForTest(t, tmp)

	closedWorld := joinMessages(lintLoadedPackages(pkgs, Options{MaxStates: 32, ClosedWorld: true}))
	if !strings.Contains(closedWorld, `exported function "PublicAPI"`) {
		t.Fatalf("closed-world analysis missed unreachable exported API:\n%s", closedWorld)
	}
}

func TestLintLoadedPackagesPreservesInputOrder(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "a", "a.go"), "package a\n")
	writeFile(t, filepath.Join(tmp, "b", "b.go"), "package b\n")

	pkgs := loadPackagesForTest(t, tmp)
	slices.Reverse(pkgs)

	want := make([]string, len(pkgs))
	for index, pkg := range pkgs {
		want[index] = pkg.ImportPath
	}

	lintLoadedPackages(pkgs, Options{MaxStates: 32})

	got := make([]string, len(pkgs))
	for index, pkg := range pkgs {
		got[index] = pkg.ImportPath
	}

	if !slices.Equal(got, want) {
		t.Fatalf("lintLoadedPackages reordered input: got %q, want %q", got, want)
	}
}
