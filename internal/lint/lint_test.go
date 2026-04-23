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

	var issues []Issue
	for _, pkg := range pkgs {
		issues = append(issues, LintPackage(pkg, Options{MaxStates: 32})...)
	}

	return issues
}

func writeFile(t *testing.T, path string, contents string) {
	t.Helper()

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
