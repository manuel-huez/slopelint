package defenselint

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestVetToolReportsFindings(t *testing.T) {
	t.Parallel()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	tool := filepath.Join(t.TempDir(), "defenselint")
	build := exec.Command("go", "build", "-o", tool, "./cmd/defenselint")
	build.Dir = wd
	build.Env = os.Environ()

	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build vettool: %v\n%s", err, out)
	}

	sample := t.TempDir()
	writeFile(t, filepath.Join(sample, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(sample, "sample.go"), `package sample

func f(s string) {
	if s == "" { return }
	if s == "" { println("bad") }
}
`)

	vet := exec.Command("go", "vet", "-vettool="+tool, "./...")
	vet.Dir = sample
	vet.Env = os.Environ()

	var out bytes.Buffer

	vet.Stdout = &out
	vet.Stderr = &out

	err = vet.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("go vet failed without exit error: %v\n%s", err, out.String())
		}

		if exitErr.ExitCode() != 1 {
			t.Fatalf("go vet exit code = %d, want 0 or 1\n%s", exitErr.ExitCode(), out.String())
		}
	}

	if !strings.Contains(out.String(), "always false here") {
		t.Fatalf("go vet output missing diagnostic:\n%s", out.String())
	}
}

func writeFile(t *testing.T, path string, contents string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
