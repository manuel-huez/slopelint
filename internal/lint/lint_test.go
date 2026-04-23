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
