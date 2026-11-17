package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectsTestGlobalFuncStub(t *testing.T) {
	t.Parallel()

	tmp := newTestModule(t)
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
	t.Parallel()

	tmp := newTestModule(t)
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

type detectsAPISmellsCase struct {
	name, source, want, failure, kind, failure2 string
}

func runDetectsAPISmells(t *testing.T, cases []detectsAPISmellsCase) {
	t.Helper()

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			tmp := newTestModule(t)
			writeFile(t, filepath.Join(tmp, "sample.go"), test.source)

			issues := lintInDir(t, tmp)
			joined := joinMessages(issues)

			if !strings.Contains(
				joined,
				test.want,
			) {
				t.Fatalf(test.failure, joined)
			}

			if !hasIssueKind(issues, test.kind) {
				t.Fatalf(test.failure2, issues)
			}
		})
	}
}

func TestDetectsArgumentAndResultSmells(t *testing.T) {
	t.Parallel()

	runDetectsAPISmells(t, []detectsAPISmellsCase{
		{
			name: "DetectsBoolModeParamCalledWithLiteral",
			source: `package sample

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
`,
			want:     `private function "emit" has bool mode parameter "final" called with boolean literals; split named operations or use a typed mode`,
			failure:  "expected bool-mode-param finding, got:\n%s",
			kind:     "bool_mode_param",
			failure2: "expected bool_mode_param kind, got %#v",
		},
		{
			name: "DetectsZeroValuePrivateArg",
			source: `package sample

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
`,
			want:     `private function "encodeInCoverage" parameter "coverage" is always called with zero value "Coverage{}"; remove the parameter or pass real variation`,
			failure:  "expected zero-value-arg finding, got:\n%s",
			kind:     "api_overkill",
			failure2: "expected api_overkill kind, got %#v",
		},
		{
			name: "DetectsOptionalResultTriple",
			source: `package sample

func decode(value string) (string, bool, error) {
	if value == "" {
		return "", false, nil
	}

	return value, true, nil
}
`,
			want:     `private function "decode" returns (value, ok, error); make absence part of the error/zero-value contract or return a named state type`,
			failure:  "expected optional-result-triple finding, got:\n%s",
			kind:     "optional_result_triple",
			failure2: "expected optional_result_triple kind, got %#v",
		},
	})
}

func TestDetectsProductionErrorPanic(t *testing.T) {
	t.Parallel()

	runDetectsAPISmells(t, []detectsAPISmellsCase{
		{
			name: "DetectsProductionErrorPanic",
			source: `package sample

import "errors"

func mustValue() string {
	err := errors.New("bad")
	if err != nil {
		panic(err)
	}

	return "ok"
}
`,
			want:     `production function "mustValue" panics with error "err"; return error or prove the invariant before this call`,
			failure:  "expected production-error-panic finding, got:\n%s",
			kind:     "prod_must_panic",
			failure2: "expected prod_must_panic kind, got %#v",
		},
	})
}

type skipsAPISmellsCase struct {
	name, path, source, want, failure string
}

func runSkipsAPISmells(t *testing.T, cases []skipsAPISmellsCase) {
	t.Helper()

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			tmp := newTestModule(t)
			writeFile(t, filepath.Join(tmp, test.path), test.source)

			issues := lintInDir(t, tmp)
			joined := joinMessages(issues)

			if strings.Contains(joined, test.want) {
				t.Fatalf(test.failure, joined)
			}
		})
	}
}

func TestSkipsAPISmells(t *testing.T) {
	t.Parallel()

	runSkipsAPISmells(t, []skipsAPISmellsCase{
		{
			name: "SkipsBoolModeParamWithoutLiteralCall",
			path: similarityTestFilename,
			source: `package sample

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
`,
			want:    `has bool mode parameter`,
			failure: "unexpected bool-mode-param finding without literal call, got:\n%s",
		},
		{
			name: "SkipsZeroValuePrivateArgWithRealCallerValue",
			path: similarityTestFilename,
			source: `package sample

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
`,
			want:    `is always called with zero value`,
			failure: "unexpected zero-value-arg finding with real caller value, got:\n%s",
		},
		{
			name: "SkipsProductionErrorPanicInTests",
			path: "sample_test.go",
			source: `package sample

var testErr error

func testMustValue() {
	err := testErr
	if err != nil {
		panic(err)
	}
}
`,
			want:    `panics with error`,
			failure: "unexpected production-error-panic finding in test file, got:\n%s",
		},
		{
			name: "SkipsProductionErrorPanicWhenPanicIsShadowed",
			path: similarityTestFilename,
			source: `package sample

import "errors"

func run() {
	err := errors.New("bad")
	panic := func(error) {}
	panic(err)
}
`,
			want:    `panics with error`,
			failure: "unexpected production-error-panic finding for shadowed panic, got:\n%s",
		},
	})
}

func TestDetectsSentinelErrorBreak(t *testing.T) {
	t.Parallel()

	tmp := newTestModule(t)
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
	t.Parallel()

	tmp := newTestModule(t)
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
	t.Parallel()

	tmp := newTestModule(t)
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

func TestSkipsSentinelErrorSuppressionAfterErrorReassignment(t *testing.T) {
	t.Parallel()

	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

import "errors"

var errStop = errors.New("stop")

func walk(fn func() error) error { return fn() }
func unrelated() error { return nil }

func f() error {
	err := walk(func() error { return errStop })
	err = unrelated()
	if errors.Is(err, errStop) {
		return nil
	}
	return err
}

`)

	joined := joinMessages(lintInDir(t, tmp))
	if strings.Contains(joined, `callback returns sentinel error`) {
		t.Fatalf("unexpected sentinel finding across reassignment, got:\n%s", joined)
	}
}

func TestDetectsUnusedPrivateParameter(t *testing.T) {
	t.Parallel()

	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func parse(value string, unused int) string {
	return value
}
`)

	joined := joinMessages(lintInDir(t, tmp))
	if !strings.Contains(joined, `private function "parse" does not use parameter "unused"`) {
		t.Fatalf("expected unused-private-param finding, got:\n%s", joined)
	}
}

func TestDetectsStaleComplexitySuppression(t *testing.T) {
	t.Parallel()

	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

//nolint:cyclop,gocognit // stale suppression
func parse(value string) string {
	return value
}
`)

	joined := joinMessages(lintInDir(t, tmp))
	if !strings.Contains(
		joined,
		`function "parse" suppresses complexity checks but has no decision points`,
	) {
		t.Fatalf("expected stale-complexity-suppression finding, got:\n%s", joined)
	}
}

func TestSkipsComplexitySuppressionWithDecision(t *testing.T) {
	t.Parallel()

	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

//nolint:cyclop // decision-heavy shape represented minimally for detector coverage
func parse(value string) string {
	if value == "" {
		return "missing"
	}
	return value
}
`)

	joined := joinMessages(lintInDir(t, tmp))
	if strings.Contains(joined, "stale_complexity_suppression") {
		t.Fatalf("unexpected stale-complexity-suppression finding, got:\n%s", joined)
	}
}
