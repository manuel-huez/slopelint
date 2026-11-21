package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

const wantRunForwardsToExecute = `private helper "run" only forwards to "execute" at one production use`

const wantForwardsToExecute = `only forwards to "execute"`

func TestDetectsForwarderAndPredicateSmells(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, source, want, failure, kind, failure2 string
	}{
		{
			name: "DetectsIsPredicateWithNonBoolSignature",
			source: `package sample

func IsReady() int {
	return 1
}
`,
			want:     `predicate-like function "IsReady" should return bool or (bool, error)`,
			failure:  "expected predicate signature finding, got:\n%s",
			kind:     "predicate_signature",
			failure2: "expected predicate_signature kind, got %#v",
		},
		{
			name: "DetectsTrivialForwarder",
			source: `package sample

func execute(name string) bool { return name != "" }

// run keeps call-site naming visible.
func run(name string) bool {
	return execute(name)
}

func use(name string) bool {
	return run(name)
}
`,
			want:     wantRunForwardsToExecute,
			failure:  "expected trivial forwarder finding, got:\n%s",
			kind:     "trivial_wrapper",
			failure2: "expected trivial_wrapper kind, got %#v",
		},
		{
			name: "DetectsRestatementComment",
			source: `package sample

// Validate user
func validateUser(name string) bool {
	return name != ""
}
`,
			want:     `comment only restates private declaration "validateUser"`,
			failure:  "expected restatement-comment finding, got:\n%s",
			kind:     "comment_noise",
			failure2: "expected comment_noise kind, got %#v",
		},
	} {
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

type detectsTrivialForwarderShapesCase struct {
	name, source, want, failure string
}

func runDetectsTrivialForwarderShapes(t *testing.T, cases []detectsTrivialForwarderShapesCase) {
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
		})
	}
}

func TestDetectsTrivialForwarderShapes(t *testing.T) {
	t.Parallel()

	runDetectsTrivialForwarderShapes(t, []detectsTrivialForwarderShapesCase{
		{
			name: "DetectsTrivialForwarderAssignReturn",
			source: `package sample

func execute(name string) bool { return name != "" }

func run(name string) bool {
	ok := execute(name)
	return ok
}

func use(name string) bool {
	return run(name)
}
`,
			want:    wantRunForwardsToExecute,
			failure: "expected trivial assign-return forwarder finding, got:\n%s",
		},
		{
			name: "DetectsTrivialForwarderVarReturn",
			source: `package sample

func execute(name string) (string, bool) { return name, name != "" }

func run(name string) (string, bool) {
	var value, ok = execute(name)
	return value, ok
}

func use(name string) bool {
	value, ok := run(name)
	return ok && value != ""
}
`,
			want:    wantRunForwardsToExecute,
			failure: "expected trivial var-return forwarder finding, got:\n%s",
		},
		{
			name: "DetectsTrivialForwarderToImportedFunction",
			source: `package sample

import "strconv"

func parseCount(value string) (int, error) {
	return strconv.Atoi(value)
}

func use(value string) error {
	_, err := parseCount(value)
	return err
}
`,
			want:    `private helper "parseCount" only forwards to "strconv.Atoi" at one production use`,
			failure: "expected imported trivial forwarder finding, got:\n%s",
		},
		{
			name: "DetectsTrivialForwarderWithParamMethodAdapter",
			source: `package sample

type CacheScope map[string]string

func (s CacheScope) StringMap() map[string]string { return s }

func fxScopeCurrencies(scope map[string]string) (string, string, error) {
	return scope["from"], scope["to"], nil
}

func fxCacheScopeCurrencies(scope CacheScope) (string, string, error) {
	return fxScopeCurrencies(scope.StringMap())
}

func use(scope CacheScope) error {
	_, _, err := fxCacheScopeCurrencies(scope)
	return err
}
`,
			want:    `private helper "fxCacheScopeCurrencies" only forwards to "fxScopeCurrencies" at one production use`,
			failure: "expected trivial adapter forwarder finding, got:\n%s",
		},
	})
}

func TestDetectsTrivialForwarderWithParamFieldAndConversionAdapters(t *testing.T) {
	t.Parallel()

	runDetectsTrivialForwarderShapes(t, []detectsTrivialForwarderShapesCase{
		{
			name: "DetectsTrivialForwarderWithParamFieldAndConversionAdapters",
			source: `package sample

type CurrencyCode string

type request struct {
	ID string
}

func convertScope(id string, code string) (string, error) {
	return id + code, nil
}

func convertRequest(req request, code CurrencyCode) (string, error) {
	return convertScope(req.ID, string(code))
}

func use(req request, code CurrencyCode) error {
	_, err := convertRequest(req, code)
	return err
}
`,
			want:    `private helper "convertRequest" only forwards to "convertScope" at one production use`,
			failure: "expected trivial field/conversion adapter forwarder finding, got:\n%s",
		},
	})
}

func TestDetectsTrivialForwarderDespiteTestCallsite(t *testing.T) {
	t.Parallel()

	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func execute(name string) bool { return name != "" }

func run(name string) bool {
	return execute(name)
}

func use(name string) bool {
	return run(name)
}
`)
	writeFile(t, filepath.Join(tmp, "sample_test.go"), `package sample

import "testing"

func TestRun(t *testing.T) {
	if !run("ok") {
		t.Fatal("run failed")
	}
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		wantRunForwardsToExecute,
	) {
		t.Fatalf("expected trivial forwarder finding despite test callsite, got:\n%s", joined)
	}
}

type skipsNonTrivialForwardersCase struct {
	name, source, want, failure string
}

func runSkipsNonTrivialForwarders(t *testing.T, cases []skipsNonTrivialForwardersCase) {
	t.Helper()

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			tmp := newTestModule(t)
			writeFile(t, filepath.Join(tmp, "sample.go"), test.source)

			issues := lintInDir(t, tmp)
			joined := joinMessages(issues)

			if strings.Contains(joined, test.want) {
				t.Fatalf(
					test.failure,
					joined,
				)
			}
		})
	}
}

func TestSkipsChangedOrUnusedForwarders(t *testing.T) {
	t.Parallel()

	runSkipsNonTrivialForwarders(t, []skipsNonTrivialForwardersCase{
		{
			name: "SkipsTrivialForwarderWithReorderedAdapterArgs",
			source: `package sample

type CurrencyCode string

type request struct {
	ID string
}

func convertScope(id string, code string) (string, error) {
	return id + code, nil
}

func convertRequest(req request, code CurrencyCode) (string, error) {
	return convertScope(string(code), req.ID)
}

func use(req request, code CurrencyCode) error {
	_, err := convertRequest(req, code)
	return err
}
`,
			want:    `only forwards to "convertScope"`,
			failure: "unexpected trivial adapter forwarder finding with reordered args, got:\n%s",
		},
		{
			name: "SkipsTrivialForwarderWhenReturnOrderChanges",
			source: `package sample

func execute(name string) (string, bool) { return name, name != "" }

func run(name string) (bool, string) {
	value, ok := execute(name)
	return ok, value
}

func use(name string) bool {
	ok, _ := run(name)
	return ok
}
`,
			want:    wantForwardsToExecute,
			failure: "unexpected trivial forwarder finding when return order changes, got:\n%s",
		},
		{
			name: "SkipsTrivialForwarderWhenVarAddsExplicitType",
			source: `package sample

type runner interface { Run() bool }

type worker struct{}

func (worker) Run() bool { return true }

func execute(name string) worker { return worker{} }

func run(name string) runner {
	var value runner = execute(name)
	return value
}

func use(name string) bool {
	return run(name).Run()
}
`,
			want:    wantForwardsToExecute,
			failure: "unexpected trivial forwarder finding when var adds explicit type, got:\n%s",
		},
		{
			name: "SkipsUnusedPrivateTrivialForwarder",
			source: `package sample

func execute(name string) bool { return name != "" }

func run(name string) bool {
	return execute(name)
}
`,
			want:    `private helper "run" only forwards to "execute"`,
			failure: "unexpected trivial forwarder finding for unused helper, got:\n%s",
		},
	})
}

func TestSkipsLongTrivialForwarderWithMultipleCallsites(t *testing.T) {
	t.Parallel()

	runSkipsNonTrivialForwarders(t, []skipsNonTrivialForwardersCase{
		{
			name: "SkipsLongTrivialForwarderWithMultipleCallsites",
			source: `package sample

func execute(first string, second string, third string) bool {
	return first != "" || second != "" || third != ""
}

func run(first string, second string, third string) bool {
	return execute(
		first,
		second,
		third,
	)
}

func a(name string) bool { return run(name, "", "") }
func b(name string) bool { return run("", name, "") }
`,
			want:    wantForwardsToExecute,
			failure: "unexpected long trivial forwarder finding with multiple callsites, got:\n%s",
		},
	})
}

func TestDetectsTrivialForwarderMethods(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, source, want, failure string
	}{
		{
			name: "DetectsShortTrivialForwarderWithMultipleCallsites",
			source: `package sample

func execute(name string) bool { return name != "" }

func run(name string) bool {
	return execute(name)
}

func a(name string) bool { return run(name) }
func b(name string) bool { return run(name) }
`,
			want:    `private helper "run" only forwards to "execute" in a body of 3 lines or less`,
			failure: "expected short trivial forwarder finding with multiple callsites, got:\n%s",
		},
		{
			name: "DetectsPrivateTrivialForwarderMethod",
			source: `package sample

type runner struct {
	name string
}

func execute(name string) bool { return name != "" }

func (r runner) run() bool {
	return execute(r.name)
}

func use(r runner) bool { return r.run() }
`,
			want:    wantRunForwardsToExecute,
			failure: "expected private method trivial forwarder finding, got:\n%s",
		},
		{
			name: "DetectsExportedTrivialForwarderMethodWithoutPrivateReceiverUse",
			source: `package sample

type Runner struct{}

func execute(name string) bool { return name != "" }

func (r Runner) Run(name string) bool {
	return execute(name)
}

func a(r Runner) bool { return r.Run("a") }
func b(r Runner) bool { return r.Run("b") }
`,
			want:    `exported method "Run" only forwards to "execute" in a body of 3 lines or less`,
			failure: "expected exported method trivial forwarder finding, got:\n%s",
		},
		{
			name: "DetectsDocumentedExportedTrivialForwarderMethod",
			source: `package sample

type Runner struct{}

func execute(name string) bool { return name != "" }

// Run reports whether name can run.
func (r Runner) Run(name string) bool {
	return execute(name)
}

func a(r Runner) bool { return r.Run("a") }
func b(r Runner) bool { return r.Run("b") }
`,
			want:    `exported method "Run" only forwards to "execute" in a body of 3 lines or less`,
			failure: "expected documented exported method trivial forwarder finding, got:\n%s",
		},
	} {
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
				t.Fatalf(
					test.failure,
					joined,
				)
			}
		})
	}
}

func TestSkipsExportedTrivialForwarderMethodWithPrivateReceiverField(t *testing.T) {
	t.Parallel()

	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type Runner struct {
	name string
}

func execute(name string) bool { return name != "" }

func (r Runner) Run() bool {
	return execute(r.name)
}

func a(r Runner) bool { return r.Run() }
func b(r Runner) bool { return r.Run() }
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, wantForwardsToExecute) {
		t.Fatalf("unexpected trivial forwarder finding for exported method, got:\n%s", joined)
	}
}

func TestSkipsForwarderMethodsWithReasons(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, source, want, failure string
	}{
		{
			name: "SkipsExportedTrivialForwarderMethodWithPrivateReceiverMethod",
			source: `package sample

type Runner struct {
	name string
}

func execute(name string) bool { return name != "" }

func (r Runner) value() string {
	return r.name
}

func (r Runner) Run() bool {
	return execute(r.value())
}

func a(r Runner) bool { return r.Run() }
func b(r Runner) bool { return r.Run() }
`,
			want:    wantForwardsToExecute,
			failure: "unexpected trivial forwarder finding for exported method using private method, got:\n%s",
		},
		{
			name: "SkipsExportedCodecHookForwarderMethod",
			source: `package sample

import "encoding/json"

type Payload string

func (p Payload) MarshalJSON() ([]byte, error) {
	return json.Marshal(string(p))
}

func encode(p Payload) ([]byte, error) {
	return json.Marshal(p)
}
`,
			want:    `exported method "MarshalJSON" only forwards to "json.Marshal"`,
			failure: "unexpected trivial forwarder finding for codec hook, got:\n%s",
		},
		{
			name: "SkipsRestatementCommentWhenIntentAdded",
			source: `package sample

// Validate user before cache warmup.
func validateUser(name string) bool {
	return name != ""
}
`,
			want:    `comment only restates private declaration "validateUser"`,
			failure: "unexpected restatement-comment finding when comment adds intent, got:\n%s",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			tmp := newTestModule(t)
			writeFile(t, filepath.Join(tmp, "sample.go"), test.source)

			issues := lintInDir(t, tmp)
			joined := joinMessages(issues)

			if strings.Contains(joined, test.want) {
				t.Fatalf(
					test.failure,
					joined,
				)
			}
		})
	}
}
