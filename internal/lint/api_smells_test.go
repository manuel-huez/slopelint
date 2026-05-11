package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectsTestGlobalFuncStub(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func run() {}

var runHook = run

func call() {
	runHook()
}
`)
	writeFile(t, filepath.Join(tmp, "sample_test.go"), `package sample

func testCall() {
	previous := runHook
	runHook = func() {}
	call()
	runHook = previous
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`package-level function variable "runHook" is reassigned in tests`,
	) {
		t.Fatalf("expected test-global-func-stub finding, got:\n%s", joined)
	}

	if !hasIssueKind(issues, "test_global_func_stub") {
		t.Fatalf("expected test_global_func_stub kind, got %#v", issues)
	}
}

func TestDetectsTestGlobalFuncStubWithTestingImport(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func run() {}

var runHook = run

func call() {
	runHook()
}
`)
	writeFile(t, filepath.Join(tmp, "sample_test.go"), `package sample

import "testing"

func TestCall(t *testing.T) {
	previous := runHook
	runHook = func() {}
	call()
	runHook = previous
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`package-level function variable "runHook" is reassigned in tests`,
	) {
		t.Fatalf("expected test-global-func-stub finding, got:\n%s", joined)
	}
}

func TestDetectsBoolModeParamCalledWithLiteral(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func emit(final bool) {
	if final {
		println("done")
		return
	}

	println("tick")
}

func run() {
	emit(false)
	emit(true)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`private function "emit" has bool mode parameter "final" called with boolean literals; split named operations or use a typed mode`,
	) {
		t.Fatalf("expected bool-mode-param finding, got:\n%s", joined)
	}

	if !hasIssueKind(issues, "bool_mode_param") {
		t.Fatalf("expected bool_mode_param kind, got %#v", issues)
	}
}

func TestSkipsBoolModeParamWithoutLiteralCall(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func emit(final bool) {
	if final {
		println("done")
		return
	}

	println("tick")
}

func run(final bool) {
	emit(final)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `has bool mode parameter`) {
		t.Fatalf("unexpected bool-mode-param finding without literal call, got:\n%s", joined)
	}
}

func TestDetectsZeroValuePrivateArg(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type Coverage struct {
	Start string
}

func (coverage Coverage) IsZero() bool {
	return coverage.Start == ""
}

func (coverage Coverage) ContainsDate(value string) bool {
	return coverage.Start <= value
}

func encode(rows []string) []string {
	return encodeInCoverage(rows, Coverage{})
}

func encodeInCoverage(rows []string, coverage Coverage) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if !coverageContainsDate(coverage, row) {
			continue
		}

		out = append(out, row)
	}

	return out
}

func coverageContainsDate(coverage Coverage, date string) bool {
	return coverage.IsZero() || coverage.ContainsDate(date)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`private function "encodeInCoverage" parameter "coverage" is always called with zero value "Coverage{}"; remove the parameter or pass real variation`,
	) {
		t.Fatalf("expected zero-value-arg finding, got:\n%s", joined)
	}

	if !hasIssueKind(issues, "api_overkill") {
		t.Fatalf("expected api_overkill kind, got %#v", issues)
	}
}

func TestSkipsZeroValuePrivateArgWithRealCallerValue(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type Coverage struct {
	Start string
}

func encode(rows []string) []string {
	return encodeInCoverage(rows, Coverage{})
}

func encodeWindow(rows []string, coverage Coverage) []string {
	return encodeInCoverage(rows, coverage)
}

func encodeInCoverage(rows []string, coverage Coverage) []string {
	if coverage.Start == "" {
		return rows
	}

	return rows[:0]
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `is always called with zero value`) {
		t.Fatalf("unexpected zero-value-arg finding with real caller value, got:\n%s", joined)
	}
}

func TestDetectsOptionalResultTriple(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func decode(value string) (string, bool, error) {
	if value == "" {
		return "", false, nil
	}

	return value, true, nil
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`private function "decode" returns (value, ok, error); make absence part of the error/zero-value contract or return a named state type`,
	) {
		t.Fatalf("expected optional-result-triple finding, got:\n%s", joined)
	}

	if !hasIssueKind(issues, "optional_result_triple") {
		t.Fatalf("expected optional_result_triple kind, got %#v", issues)
	}
}

func TestDetectsProductionErrorPanic(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

import "errors"

func mustValue() string {
	err := errors.New("bad")
	if err != nil {
		panic(err)
	}

	return "ok"
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`production function "mustValue" panics with error "err"; return error or prove the invariant before this call`,
	) {
		t.Fatalf("expected production-error-panic finding, got:\n%s", joined)
	}

	if !hasIssueKind(issues, "prod_must_panic") {
		t.Fatalf("expected prod_must_panic kind, got %#v", issues)
	}
}

func TestSkipsProductionErrorPanicInTests(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample_test.go"), `package sample

var testErr error

func testMustValue() {
	err := testErr
	if err != nil {
		panic(err)
	}
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `panics with error`) {
		t.Fatalf("unexpected production-error-panic finding in test file, got:\n%s", joined)
	}
}

func TestSkipsProductionErrorPanicWhenPanicIsShadowed(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

import "errors"

func run() {
	err := errors.New("bad")
	panic := func(error) {}
	panic(err)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `panics with error`) {
		t.Fatalf("unexpected production-error-panic finding for shadowed panic, got:\n%s", joined)
	}
}

func TestDetectsSentinelErrorBreak(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

import (
	"errors"
	"io"
)

func each(visit func(int) error) error {
	return visit(1)
}

func first() error {
	err := each(func(value int) error {
		if value > 0 {
			return io.EOF
		}

		return nil
	})
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}

	return nil
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`callback returns sentinel error "io.EOF" for control flow and caller suppresses it; use an explicit stop signal instead`,
	) {
		t.Fatalf("expected sentinel-error-break finding, got:\n%s", joined)
	}

	if !hasIssueKind(issues, "sentinel_error_break") {
		t.Fatalf("expected sentinel_error_break kind, got %#v", issues)
	}
}

func TestSkipsSentinelErrorCheckThatReturnsSentinel(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

import (
	"errors"
	"io"
)

func each(visit func(int) error) error {
	return visit(1)
}

func first() error {
	err := each(func(value int) error {
		if value > 0 {
			return io.EOF
		}

		return nil
	})
	if errors.Is(err, io.EOF) {
		return err
	}

	return err
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `callback returns sentinel error`) {
		t.Fatalf(
			"unexpected sentinel-error-break finding when sentinel is returned, got:\n%s",
			joined,
		)
	}
}

func TestSkipsUnrelatedSentinelErrorSuppression(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

import (
	"errors"
	"io"
)

func each(visit func(int) error) error {
	return visit(1)
}

func first(err error) error {
	callbackErr := each(func(value int) error {
		if value > 0 {
			return io.EOF
		}

		return nil
	})
	if callbackErr != nil {
		return callbackErr
	}

	if errors.Is(err, io.EOF) {
		return nil
	}

	return err
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `callback returns sentinel error`) {
		t.Fatalf("unexpected sentinel-error-break finding for unrelated err, got:\n%s", joined)
	}
}
