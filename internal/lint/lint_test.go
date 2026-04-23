package lint

import (
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

	return LintPackages(pkgs, Options{MaxStates: 32})
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
