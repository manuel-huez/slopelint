package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectsUnreachableSwitchCases(t *testing.T) {
	tmp := newTestModule(t)
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

func TestSelectDoesNotBlindWholeFunction(t *testing.T) {
	tmp := newTestModule(t)
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
	tmp := newTestModule(t)
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
	tmp := newTestModule(t)
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
	tmp := newTestModule(t)
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
	tmp := newTestModule(t)
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
	tmp := newTestModule(t)
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
