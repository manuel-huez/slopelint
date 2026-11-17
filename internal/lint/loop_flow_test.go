package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRangeLoopDoesNotKeepInitialNilFactAcrossIterations(t *testing.T) {
	t.Parallel()

	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func f(values []int) {
	var best *int
	for _, value := range values {
		if best == nil {
			copy := value
			best = &copy
			continue
		}
	}
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `condition "best == nil" is always true here`) {
		t.Fatalf("range loop should not keep initial nil fact across iterations, got:\n%s", joined)
	}
}

func TestRangeLoopLocalHelperWriteInvalidatesAfterLoopFact(t *testing.T) {
	t.Parallel()

	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

import (
	"errors"
	"sync"
)

var errBoom = errors.New("boom")

func f(values []int) error {
	var (
		firstErr error
		once     sync.Once
	)

	fail := func(err error) {
		if err == nil { return }
		once.Do(func() {
			firstErr = err
		})
	}

	for _, value := range values {
		if value > 0 {
			fail(errBoom)
		}
	}

	if firstErr != nil {
		return firstErr
	}

	return nil
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `condition "firstErr != nil" is always false here`) {
		t.Fatalf("range loop helper write should invalidate after-loop fact, got:\n%s", joined)
	}
}

func TestRangeLoopTracksLocalHelperClosureWrites(t *testing.T) {
	t.Parallel()

	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

import "sync"

func f(values []int) {
	var (
		firstErr error
		once     sync.Once
	)

	fail := func(err error) {
		if err == nil { return }
		once.Do(func() {
			firstErr = err
		})
	}

	for _, value := range values {
		if value > 0 {
			fail(nil)
		}
	}

	_ = firstErr
}
`)

	pkg := loadOnePackageForTest(t, tmp)

	rangeStmt := firstRangeStmt(pkg.Files)
	if rangeStmt == nil {
		t.Fatal("expected range loop")
	}

	firstErrObj := namedObject(pkg.Files, pkg.TypesInfo, "firstErr")
	if firstErrObj == nil {
		t.Fatal("expected firstErr object")
	}

	q := &exprQuery{
		info:          pkg.TypesInfo,
		localFuncLits: collectLocalFuncLits(pkg.Files, pkg.TypesInfo),
	}

	invalidations := loopInvalidations(q, rangeStmt.Body.List, nil)
	if invalidations[symbolForObject(firstErrObj).key] != invalidateFullPrefix {
		t.Fatalf(
			"expected local helper closure write to invalidate firstErr, got %#v",
			invalidations,
		)
	}
}

func TestRangeLoopDoesNotDiscardBodyEffectsAtExit(t *testing.T) {
	t.Parallel()

	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func f(values []string) {
	candidate := ""
	for _, value := range values {
		if value == "" {
			continue
		}
		candidate = value
	}

	if candidate != "" { println("maybe") }
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `condition "candidate != \"\"" is always false here`) {
		t.Fatalf("range loop exit should include body effects, got:\n%s", joined)
	}
}

func TestRunAnalysisRangeLoopDoesNotKeepInitialFacts(t *testing.T) {
	t.Parallel()

	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func f(values []string) {
	candidate := ""
	for _, value := range values {
		if candidate == "" {
			candidate = value
			continue
		}
	}
}
`)

	pkg := loadOnePackageForTest(t, tmp)
	pass, _ := newAnalysisTestPass(pkg, nil)

	issues, err := RunAnalysis(pass, Options{MaxStates: 32})
	if err != nil {
		t.Fatalf("run analysis: %v", err)
	}

	joined := joinMessages(issues)
	if strings.Contains(joined, `condition "candidate == \"\"" is always true here`) {
		t.Fatalf("run analysis should not keep initial loop fact, got:\n%s", joined)
	}
}

func TestStateHashIncludesAliasAndBindingsWithoutFacts(t *testing.T) {
	t.Parallel()

	empty := newState()
	withAlias := linkAlias(newState(), "pkg:a:1", "pkg:b:2")

	if empty.hash() == withAlias.hash() {
		t.Fatalf("alias-only state collapsed into empty hash %q", empty.hash())
	}

	withBinding := newState()
	withBinding.bindings["pkg:ok:3"] = resultBinding{roots: []string{"pkg:req:4"}}

	if empty.hash() == withBinding.hash() {
		t.Fatalf("binding-only state collapsed into empty hash %q", empty.hash())
	}
}
