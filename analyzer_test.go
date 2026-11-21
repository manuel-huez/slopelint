package slopelint

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/analysis/singlechecker"
)

const vettoolEnv = "SLOPELINT_TEST_VETTOOL"

// TestMain lets go vet run this test binary as the vettool, so the test needs
// no separate build of cmd/slopelint.
func TestMain(m *testing.M) {
	if os.Getenv(vettoolEnv) == "1" {
		singlechecker.Main(Analyzer)
		return
	}

	os.Exit(m.Run())
}

func TestAnalyzerCrossPackageFacts(t *testing.T) {
	t.Setenv("SLOPELINT_CACHE", "off")
	t.Setenv(vettoolEnv, "1")

	vettool, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	tmp := t.TempDir()
	writeAnalyzerFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeAnalyzerFile(t, filepath.Join(tmp, "guard", "guard.go"), `package guard

type Req struct { Name string }

type failure struct{}

func (failure) Error() string { return "bad" }

func Valid(req *Req) bool {
	if req == nil { return false }
	if req.Name == "" { return false }
	return true
}

func Check(req *Req) error {
	if req == nil { return failure{} }
	if req.Name == "" { return failure{} }
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
		`condition "req == nil" is always false here`,
		`condition "req.Name == \"\"" is always false here`,
		"[redundant_condition] Fix: Delete the redundant code",
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
