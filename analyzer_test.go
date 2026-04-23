package slopelint

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnalyzerCrossPackageFacts(t *testing.T) {
	vettool := filepath.Join(t.TempDir(), "slopelint")
	build := exec.Command("go", "build", "-o", vettool, "./cmd/slopelint")

	build.Dir = "."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build vettool: %v\n%s", err, out)
	}

	tmp := t.TempDir()
	writeAnalyzerFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeAnalyzerFile(t, filepath.Join(tmp, "guard", "guard.go"), `package guard

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
	writeAnalyzerFile(t, filepath.Join(tmp, "use", "use.go"), `package use

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

	vet := exec.Command("go", "vet", "-vettool="+vettool, "./...")
	vet.Dir = tmp

	out, err := vet.CombinedOutput()
	if err != nil && len(out) == 0 {
		t.Fatalf("go vet failed without diagnostics: %v", err)
	}

	joined := string(out)
	for _, want := range []string{
		`condition \"req == nil\" is always false here`,
		`condition \"req.Name == \\\"\\\"\" is always false here`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing diagnostic %q in:\n%s", want, joined)
		}
	}
}

func writeAnalyzerFile(t *testing.T, path string, contents string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}

	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
