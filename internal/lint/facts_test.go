package lint

import (
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

func TestRuntimeTargetConstantsDoNotBecomeRedundantConditions(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

import "runtime"

func f() {
	if runtime.GOOS != "windows" { println("ok") }
	if runtime.GOARCH == "amd64" { println("ok") }
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `condition "runtime.GOOS != \"windows\"" is always true here`) ||
		strings.Contains(joined, `condition "runtime.GOARCH == \"amd64\"" is always true here`) ||
		strings.Contains(joined, `condition "runtime.GOARCH == \"amd64\"" is always false here`) {
		t.Fatalf(
			"runtime target constants should not be folded into redundant conditions, got:\n%s",
			joined,
		)
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

func TestLocalFuncCallbackArgInvalidatesOuterFact(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func f(writeRows func(func([]int) error) error) error {
	wroteRows := false
	write := func(rows []int) error {
		if len(rows) == 0 { return nil }
		wroteRows = true

		return nil
	}

	if err := writeRows(write); err != nil {
		return err
	}

	if !wroteRows { println("empty") }

	return nil
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `condition "!wroteRows" is always true here`) {
		t.Fatalf("callback arg closure write should invalidate outer bool fact, got:\n%s", joined)
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
