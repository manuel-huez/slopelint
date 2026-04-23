package lint

import (
	"go/ast"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectsRedundantChecksAfterFiltering(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func f(s string, p *int) {
	if s == "" { return }
	if s == "" { println("bad") }
	if p == nil { return }
	if p == nil { println("bad") }
}
`)

	issues := lintInDir(t, tmp)

	joined := joinMessages(issues)
	if !strings.Contains(joined, `condition "s == \"\"" is always false here`) {
		t.Fatalf("expected redundant string check, got:\n%s", joined)
	}

	if !strings.Contains(joined, `condition "p == nil" is always false here`) {
		t.Fatalf("expected redundant nil check, got:\n%s", joined)
	}
}

func TestPointerCallsKeepRootNilnessButInvalidateFields(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type Req struct { Name string }
func mutate(r *Req) { r.Name = "" }

func f(r *Req) {
	if r == nil { return }
	if r.Name == "" { return }
	mutate(r)
	if r == nil { println("bad") }
	if r.Name == "" { println("ok") }
}
`)

	issues := lintInDir(t, tmp)

	joined := joinMessages(issues)
	if !strings.Contains(joined, `condition "r == nil" is always false here`) {
		t.Fatalf("expected root nilness to be preserved, got:\n%s", joined)
	}

	if strings.Contains(joined, `condition "r.Name == \"\"" is always false here`) {
		t.Fatalf("field fact should have been invalidated across mutate call, got:\n%s", joined)
	}
}

func TestSyncOnceClosureWriteInvalidatesLocalFact(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

import "sync"

func f() {
	sendStop := false
	var once sync.Once
	once.Do(func() {
		sendStop = true
	})
	if sendStop { println("ok") }
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `condition "sendStop" is always false here`) {
		t.Fatalf("sync.Once closure write should invalidate local bool fact, got:\n%s", joined)
	}
}

func TestGoFuncClosureWriteInvalidatesOuterFact(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

import "sync"

func f() {
	ready := false
	done := make(chan struct{})
	var once sync.Once
	go func() {
		once.Do(func() {
			ready = true
		})
		close(done)
	}()
	<-done
	if ready { println("ok") }
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `condition "ready" is always false here`) {
		t.Fatalf("goroutine closure write should invalidate outer bool fact, got:\n%s", joined)
	}
}

func TestRangeLoopDoesNotKeepInitialNilFactAcrossIterations(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
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
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
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
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
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
	l := newLinter(pkg, Options{MaxStates: 32})
	l.collectLocalFuncLits()

	rangeStmt := firstRangeStmt(pkg.Files)
	if rangeStmt == nil {
		t.Fatal("expected range loop")
	}

	firstErrObj := namedObject(pkg.Files, pkg.TypesInfo, "firstErr")
	if firstErrObj == nil {
		t.Fatal("expected firstErr object")
	}

	invalidations := l.loopInvalidationsForLoop(rangeStmt.Body.List, nil)
	if invalidations[symbolForObject(firstErrObj).key] != invalidateFullPrefix {
		t.Fatalf(
			"expected local helper closure write to invalidate firstErr, got %#v",
			invalidations,
		)
	}
}

func TestRangeLoopDoesNotDiscardBodyEffectsAtExit(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
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

func TestRangeLoopExitStatesStayUnknownAfterLocalHelperWrites(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
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

	pkg := loadOnePackageForTest(t, tmp)
	l := newLinter(pkg, Options{MaxStates: 32})
	l.collectLocalFuncLits()

	fn := namedFuncDecl(pkg.Files, "f")
	if fn == nil || fn.Body == nil || len(fn.Body.List) < 4 {
		t.Fatal("expected function body with range loop and trailing if")
	}

	res := l.execBlock(fn.Body.List[:3], []state{newState()})

	cond := fn.Body.List[3].(*ast.IfStmt).Cond
	if tri, _ := l.truthAcross(res.next, cond); tri == triFalse {
		hashes := make([]string, 0, len(res.next))
		for _, st := range res.next {
			hashes = append(hashes, st.hash())
		}

		t.Fatalf("range exit states kept firstErr nil exact: %#v", hashes)
	}
}

func TestRunAnalysisRangeLoopDoesNotKeepInitialFacts(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
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

func TestDetectsUnreachableSwitchCases(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type Status string

const (
	Ready Status = "ready"
	Done  Status = "done"
)

func f(s Status) {
	if s == Ready {
		switch s {
		case Ready:
			println("ready")
		case Done:
			println("done")
		}
	}
}
`)

	issues := lintInDir(t, tmp)

	joined := joinMessages(issues)
	if !strings.Contains(joined, `switch case "Done" is unreachable here`) {
		t.Fatalf("expected unreachable switch case, got:\n%s", joined)
	}
}

func TestTracksAliasesBackToOriginalSelector(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type Req struct { Name string }

func f(req Req) {
	name := req.Name
	if name == "" { return }
	if req.Name == "" { println("bad") }
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(joined, `condition "req.Name == \"\"" is always false here`) {
		t.Fatalf("expected alias-backed redundant selector check, got:\n%s", joined)
	}
}

func TestAliasWriteInvalidatesBackPropagation(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type Req struct { Name string }

func f(req Req) {
	name := req.Name
	if name == "" { return }
	req.Name = ""
	if req.Name == "" { println("ok") }
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `condition "req.Name == \"\"" is always false here`) {
		t.Fatalf("write to alias should invalidate original selector fact, got:\n%s", joined)
	}
}

func TestTracksLenBasedRedundantChecks(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func f(items []int) {
	if len(items) == 0 { return }
	if len(items) == 0 { println("bad") }
	if len(items) > 0 { println("known") }
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(joined, `condition "len(items) == 0" is always false here`) {
		t.Fatalf("expected redundant empty len check, got:\n%s", joined)
	}

	if !strings.Contains(joined, `condition "len(items) > 0" is always true here`) {
		t.Fatalf("expected redundant positive len check, got:\n%s", joined)
	}
}

func TestContractCommentsEstablishFactsAfterCall(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type Req struct { Name string }

//defenselint:ensures req != nil
//defenselint:ensures req.Name != ""
func requireReq(req *Req) {}

func f(req *Req) {
	requireReq(req)
	if req == nil { println("bad") }
	if req.Name == "" { println("bad") }
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(joined, `condition "req == nil" is always false here`) {
		t.Fatalf("expected contract-backed nil check, got:\n%s", joined)
	}

	if !strings.Contains(joined, `condition "req.Name == \"\"" is always false here`) {
		t.Fatalf("expected contract-backed field check, got:\n%s", joined)
	}
}

func TestInfersGuardHelperFactsAcrossCallBoundary(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type Req struct { Name string }

func mustReq(req *Req) {
	if req == nil { panic("bad") }
	if req.Name == "" { panic("bad") }
}

func f(req *Req) {
	mustReq(req)
	if req == nil { println("bad") }
	if req.Name == "" { println("bad") }
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(joined, `condition "req == nil" is always false here`) {
		t.Fatalf("expected inferred helper nil check, got:\n%s", joined)
	}

	if !strings.Contains(joined, `condition "req.Name == \"\"" is always false here`) {
		t.Fatalf("expected inferred helper field check, got:\n%s", joined)
	}
}

func TestInfersPredicateFactsAcrossCallBoundary(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type Req struct { Name string }

func valid(req *Req) bool {
	if req == nil { return false }
	if req.Name == "" { return false }
	return true
}

func f(req *Req) {
	if !valid(req) { return }
	if req == nil { println("bad") }
	if req.Name == "" { println("bad") }
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(joined, `condition "req == nil" is always false here`) {
		t.Fatalf("expected predicate-backed nil check, got:\n%s", joined)
	}

	if !strings.Contains(joined, `condition "req.Name == \"\"" is always false here`) {
		t.Fatalf("expected predicate-backed field check, got:\n%s", joined)
	}
}

func TestInfersPredicateFactsAcrossMultipleCalls(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type Req struct { Name string }

func valid(req *Req) bool {
	if req == nil { return false }
	if req.Name == "" { return false }
	return true
}

func valid2(req *Req) bool {
	return valid(req)
}

func f(req *Req) {
	if !valid2(req) { return }
	if req == nil { println("bad") }
	if req.Name == "" { println("bad") }
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(joined, `condition "req == nil" is always false here`) {
		t.Fatalf("expected multi-hop nil check, got:\n%s", joined)
	}

	if !strings.Contains(joined, `condition "req.Name == \"\"" is always false here`) {
		t.Fatalf("expected multi-hop field check, got:\n%s", joined)
	}
}

func TestPropagatesPredicateFactsViaBoolVariable(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type Req struct { Name string }

func valid(req *Req) bool {
	if req == nil { return false }
	if req.Name == "" { return false }
	return true
}

func f(req *Req) {
	ok := valid(req)
	if !ok { return }
	if req == nil { println("bad") }
	if req.Name == "" { println("bad") }
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(joined, `condition "req == nil" is always false here`) {
		t.Fatalf("expected bool-var nil check, got:\n%s", joined)
	}

	if !strings.Contains(joined, `condition "req.Name == \"\"" is always false here`) {
		t.Fatalf("expected bool-var field check, got:\n%s", joined)
	}
}

func TestPropagatesPredicateFactsViaCopiedBoolVariable(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type Req struct { Name string }

func valid(req *Req) bool {
	if req == nil { return false }
	if req.Name == "" { return false }
	return true
}

func f(req *Req) {
	ok := valid(req)
	yes := ok
	if !yes { return }
	if req == nil { println("bad") }
	if req.Name == "" { println("bad") }
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(joined, `condition "req == nil" is always false here`) {
		t.Fatalf("expected copied bool-var nil check, got:\n%s", joined)
	}

	if !strings.Contains(joined, `condition "req.Name == \"\"" is always false here`) {
		t.Fatalf("expected copied bool-var field check, got:\n%s", joined)
	}
}

func TestPropagatesNilFactsViaErrorVariable(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

import "errors"

type Req struct { Name string }

func validate(req *Req) error {
	if req == nil { return errors.New("bad") }
	if req.Name == "" { return errors.New("bad") }
	return nil
}

func f(req *Req) {
	err := validate(req)
	if err != nil { return }
	if req == nil { println("bad") }
	if req.Name == "" { println("bad") }
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(joined, `condition "req == nil" is always false here`) {
		t.Fatalf("expected error-var nil check, got:\n%s", joined)
	}

	if !strings.Contains(joined, `condition "req.Name == \"\"" is always false here`) {
		t.Fatalf("expected error-var field check, got:\n%s", joined)
	}
}

func TestInfersFactsAcrossPackages(t *testing.T) {
	tmp := t.TempDir()
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

func g(req *guard.Req) {
	err := guard.Check(req)
	if err != nil { return }
	if req == nil { println("bad") }
	if req.Name == "" { println("bad") }
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(joined, `condition "req == nil" is always false here`) {
		t.Fatalf("expected cross-package nil check, got:\n%s", joined)
	}

	if !strings.Contains(joined, `condition "req.Name == \"\"" is always false here`) {
		t.Fatalf("expected cross-package field check, got:\n%s", joined)
	}
}

func TestSelectDoesNotBlindWholeFunction(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func f(s string, ch chan int) {
	if s == "" { return }
	select {
	case <-ch:
	default:
	}
	if s == "" { println("bad") }
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(joined, `condition "s == \"\"" is always false here`) {
		t.Fatalf("expected check after select to still be analyzed, got:\n%s", joined)
	}
}

func TestTypeSwitchDoesNotBlindWholeFunction(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func f(s string, v any) {
	if s == "" { return }
	switch v.(type) {
	case int:
		println("int")
	default:
		println("other")
	}
	if s == "" { println("bad") }
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(joined, `condition "s == \"\"" is always false here`) {
		t.Fatalf("expected check after type switch to still be analyzed, got:\n%s", joined)
	}
}

func TestLabeledBreakDoesNotBlindWholeFunction(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func f(n int, s string) {
	if s == "" { return }
Loop:
	for i := 0; i < n; i++ {
		for {
			break Loop
		}
	}
	if s == "" { println("bad") }
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(joined, `condition "s == \"\"" is always false here`) {
		t.Fatalf("expected labeled break function to still be analyzed, got:\n%s", joined)
	}
}

func TestLabeledContinueDoesNotBlindWholeFunction(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func f(items []int, s string) {
	if s == "" { return }
Outer:
	for range items {
		for {
			continue Outer
		}
	}
	if s == "" { println("bad") }
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(joined, `condition "s == \"\"" is always false here`) {
		t.Fatalf("expected labeled continue function to still be analyzed, got:\n%s", joined)
	}
}

func TestFallthroughMergesReachableStates(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func f(s string) {
	switch s {
	case "a":
		fallthrough
	case "b":
		if s == "c" { println("bad") }
		if s == "a" { println("maybe") }
	}
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(joined, `condition "s == \"c\"" is always false here`) {
		t.Fatalf("expected fallthrough clause to merge case states, got:\n%s", joined)
	}

	if strings.Contains(joined, `condition "s == \"a\"" is always false here`) {
		t.Fatalf("fallthrough should keep prior case state reachable, got:\n%s", joined)
	}
}

func TestInfersPredicateFactsThroughFallthroughSwitch(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type Req struct{}

func valid(req *Req, s string) bool {
	if req == nil { return false }
	switch s {
	case "a":
		fallthrough
	case "b":
		return true
	default:
		return false
	}
}

func f(req *Req, s string) {
	ok := valid(req, s)
	if !ok { return }
	if req == nil { println("bad") }
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(joined, `condition "req == nil" is always false here`) {
		t.Fatalf("expected fallthrough-backed helper facts, got:\n%s", joined)
	}
}

func TestDetectsRedundantBoolReturn(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func f(ok bool) bool {
	if ok {
		return true
	}
	return false
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(joined, `if returns boolean literals; replace with "return ok"`) {
		t.Fatalf("expected bool-return ceremony finding, got:\n%s", joined)
	}

	if !hasIssueKind(issues, "boolean_ceremony") {
		t.Fatalf("expected boolean_ceremony kind, got %#v", issues)
	}
}

func TestDetectsRedundantBoolAssignment(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func f(ok bool) bool {
	out := false
	if ok {
		out = true
	} else {
		out = false
	}
	return out
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(joined, `if assigns boolean literals; replace with "out = ok"`) {
		t.Fatalf("expected bool-assignment ceremony finding, got:\n%s", joined)
	}
}

func TestDetectsIdenticalIfElseBodies(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func f(ok bool) error {
	if ok {
		return nil
	} else {
		return nil
	}
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`if and else branches are identical; drop condition or hoist shared body`,
	) {
		t.Fatalf("expected identical-branch finding, got:\n%s", joined)
	}

	if !hasIssueKind(issues, "control_flow_merge") {
		t.Fatalf("expected control_flow_merge kind, got %#v", issues)
	}
}

func TestSkipsIdenticalIfElseBodiesWhenScopeWouldChange(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func f(ok bool) int {
	if ok {
		v := 1
		return v
	} else {
		v := 1
		return v
	}
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(
		joined,
		`if and else branches are identical; drop condition or hoist shared body`,
	) {
		t.Fatalf(
			"unexpected identical-branch finding when branch-local defs exist, got:\n%s",
			joined,
		)
	}
}

func TestDetectsIdenticalSwitchCaseBodies(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func f(ok bool) int {
	switch ok {
	case true:
		return 1
	case false:
		return 1
	}
	return 0
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`switch case "false" has identical body as previous case "true"; merge case lists`,
	) {
		t.Fatalf("expected identical switch branch finding, got:\n%s", joined)
	}
}

func TestSkipsNonAdjacentPredicateSwitchBodies(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func f(x int) int {
	switch {
	case x < 0:
		return -1
	case x == 0:
		return 0
	case x < 10:
		return -1
	}
	return 1
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `has identical body as previous case`) {
		t.Fatalf(
			"unexpected identical switch branch finding for non-adjacent predicate cases, got:\n%s",
			joined,
		)
	}
}

func TestDetectsExhaustiveBoolDefault(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func f(ok bool) {
	switch ok {
	case true:
		println("yes")
	case false:
		println("no")
	default:
		println("dead")
	}
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`default case is redundant; bool switch already covers true and false`,
	) {
		t.Fatalf("expected redundant default finding, got:\n%s", joined)
	}

	if !hasIssueKind(issues, "redundant_default") {
		t.Fatalf("expected redundant_default kind, got %#v", issues)
	}
}

func TestSkipsExhaustiveBoolDefaultWithPanic(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func f(ok bool) {
	switch ok {
	case true:
		return
	case false:
		return
	default:
		panic("impossible")
	}
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(
		joined,
		`default case is redundant; bool switch already covers true and false`,
	) {
		t.Fatalf("unexpected redundant default finding for panic default, got:\n%s", joined)
	}
}

func TestDetectsSingleUseTempAlias(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type Req struct { Name string }

func f(req Req) {
	name := req.Name
	if name == "" { println("bad") }
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`local "name" only renames cheap expression "req.Name" for one use; inline expression`,
	) {
		t.Fatalf("expected temp alias finding, got:\n%s", joined)
	}

	if !hasIssueKind(issues, "temp_alias") {
		t.Fatalf("expected temp_alias kind, got %#v", issues)
	}
}

func TestDetectsTrivialForwarderExperimental(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func execute(name string) bool { return name != "" }

func run(name string) bool {
	return execute(name)
}

func use(name string) bool {
	return run(name)
}
`)

	issues := lintInDirWithOptions(t, tmp, Options{MaxStates: 32, Experimental: true})
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`private helper "run" only forwards to "execute" at one callsite; inline or merge names`,
	) {
		t.Fatalf("expected trivial forwarder finding, got:\n%s", joined)
	}

	if !hasIssueKind(issues, "trivial_wrapper") {
		t.Fatalf("expected trivial_wrapper kind, got %#v", issues)
	}
}

func TestSkipsTrivialForwarderWithMultipleCallsites(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func execute(name string) bool { return name != "" }

func run(name string) bool {
	return execute(name)
}

func a(name string) bool { return run(name) }
func b(name string) bool { return run(name) }
`)

	issues := lintInDirWithOptions(t, tmp, Options{MaxStates: 32, Experimental: true})
	joined := joinMessages(issues)

	if strings.Contains(joined, `only forwards to "execute"`) {
		t.Fatalf("unexpected trivial forwarder finding with multiple callsites, got:\n%s", joined)
	}
}

func TestDetectsRestatementCommentExperimental(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

// Validate user
func validateUser(name string) bool {
	return name != ""
}
`)

	issues := lintInDirWithOptions(t, tmp, Options{MaxStates: 32, Experimental: true})
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`comment only restates private declaration "validateUser"; remove or explain intent`,
	) {
		t.Fatalf("expected restatement-comment finding, got:\n%s", joined)
	}

	if !hasIssueKind(issues, "comment_noise") {
		t.Fatalf("expected comment_noise kind, got %#v", issues)
	}
}

func TestSkipsRestatementCommentWhenIntentAdded(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

// Validate user before cache warmup.
func validateUser(name string) bool {
	return name != ""
}
`)

	issues := lintInDirWithOptions(t, tmp, Options{MaxStates: 32, Experimental: true})
	joined := joinMessages(issues)

	if strings.Contains(joined, `comment only restates private declaration "validateUser"`) {
		t.Fatalf(
			"unexpected restatement-comment finding when comment adds intent, got:\n%s",
			joined,
		)
	}
}

func TestSkipsTempAliasWhenNameAddsMeaning(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type Req struct { Name string }

func f(req Req) {
	customerName := req.Name
	if customerName == "" { println("bad") }
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `only renames cheap expression "req.Name"`) {
		t.Fatalf("unexpected temp alias finding for meaningful name, got:\n%s", joined)
	}
}

func TestSkipsTempAliasWhenDeclarationHasComment(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type Req struct { Name string }

func f(req Req) {
	name := req.Name // keep local for nearby label
	if name == "" { println("bad") }
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `only renames cheap expression "req.Name"`) {
		t.Fatalf("unexpected temp alias finding with declaration comment, got:\n%s", joined)
	}
}

func TestSkipsTempAliasWhenValueReadMultipleTimes(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type Req struct { Name string }

func f(req Req) {
	name := req.Name
	if name == "" { println("bad") }
	println(name)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `only renames cheap expression "req.Name"`) {
		t.Fatalf("unexpected temp alias finding with multiple reads, got:\n%s", joined)
	}
}

func TestFindingsExposeKinds(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func f(s string) {
	if s == "" { return }
	if s == "" { println("bad") }
}
`)

	issues := lintInDir(t, tmp)
	if len(issues) == 0 {
		t.Fatal("expected finding")
	}

	if issues[0].Kind != "redundant_condition" {
		t.Fatalf("issue kind = %q, want redundant_condition", issues[0].Kind)
	}
}

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

	pkgs, err := LoadPackages([]string{"./..."})
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

	pkgs, err := LoadPackages([]string{"./..."})
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
