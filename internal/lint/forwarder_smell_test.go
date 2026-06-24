package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectsIsPredicateWithNonBoolSignature(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func IsReady() int {
	return 1
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`predicate-like function "IsReady" should return bool or (bool, error)`,
	) {
		t.Fatalf("expected predicate signature finding, got:\n%s", joined)
	}

	if !hasIssueKind(issues, "predicate_signature") {
		t.Fatalf("expected predicate_signature kind, got %#v", issues)
	}
}

func TestDetectsTrivialForwarder(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func execute(name string) bool { return name != "" }

// run keeps call-site naming visible.
func run(name string) bool {
	return execute(name)
}

func use(name string) bool {
	return run(name)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`private helper "run" only forwards to "execute" at one production use; inline or merge names`,
	) {
		t.Fatalf("expected trivial forwarder finding, got:\n%s", joined)
	}

	if !hasIssueKind(issues, "trivial_wrapper") {
		t.Fatalf("expected trivial_wrapper kind, got %#v", issues)
	}
}

func TestDetectsTrivialForwarderAssignReturn(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func execute(name string) bool { return name != "" }

func run(name string) bool {
	ok := execute(name)
	return ok
}

func use(name string) bool {
	return run(name)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`private helper "run" only forwards to "execute" at one production use; inline or merge names`,
	) {
		t.Fatalf("expected trivial assign-return forwarder finding, got:\n%s", joined)
	}
}

func TestDetectsTrivialForwarderVarReturn(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func execute(name string) (string, bool) { return name, name != "" }

func run(name string) (string, bool) {
	var value, ok = execute(name)
	return value, ok
}

func use(name string) bool {
	value, ok := run(name)
	return ok && value != ""
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`private helper "run" only forwards to "execute" at one production use; inline or merge names`,
	) {
		t.Fatalf("expected trivial var-return forwarder finding, got:\n%s", joined)
	}
}

func TestDetectsTrivialForwarderToImportedFunction(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

import "strconv"

func parseCount(value string) (int, error) {
	return strconv.Atoi(value)
}

func use(value string) error {
	_, err := parseCount(value)
	return err
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`private helper "parseCount" only forwards to "strconv.Atoi" at one production use; inline or merge names`,
	) {
		t.Fatalf("expected imported trivial forwarder finding, got:\n%s", joined)
	}
}

func TestDetectsTrivialForwarderDespiteTestCallsite(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
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
		`private helper "run" only forwards to "execute" at one production use; inline or merge names`,
	) {
		t.Fatalf("expected trivial forwarder finding despite test callsite, got:\n%s", joined)
	}
}

func TestDetectsTrivialForwarderWithParamMethodAdapter(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

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
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`private helper "fxCacheScopeCurrencies" only forwards to "fxScopeCurrencies" at one production use; inline or merge names`,
	) {
		t.Fatalf("expected trivial adapter forwarder finding, got:\n%s", joined)
	}
}

func TestDetectsTrivialForwarderWithParamFieldAndConversionAdapters(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

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
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`private helper "convertRequest" only forwards to "convertScope" at one production use; inline or merge names`,
	) {
		t.Fatalf("expected trivial field/conversion adapter forwarder finding, got:\n%s", joined)
	}
}

func TestSkipsTrivialForwarderWithReorderedAdapterArgs(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

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
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `only forwards to "convertScope"`) {
		t.Fatalf(
			"unexpected trivial adapter forwarder finding with reordered args, got:\n%s",
			joined,
		)
	}
}

func TestSkipsTrivialForwarderWhenReturnOrderChanges(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func execute(name string) (string, bool) { return name, name != "" }

func run(name string) (bool, string) {
	value, ok := execute(name)
	return ok, value
}

func use(name string) bool {
	ok, _ := run(name)
	return ok
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `only forwards to "execute"`) {
		t.Fatalf("unexpected trivial forwarder finding when return order changes, got:\n%s", joined)
	}
}

func TestSkipsTrivialForwarderWhenVarAddsExplicitType(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

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
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `only forwards to "execute"`) {
		t.Fatalf(
			"unexpected trivial forwarder finding when var adds explicit type, got:\n%s",
			joined,
		)
	}
}

func TestDetectsShortTrivialForwarderWithMultipleCallsites(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func execute(name string) bool { return name != "" }

func run(name string) bool {
	return execute(name)
}

func a(name string) bool { return run(name) }
func b(name string) bool { return run(name) }
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`private helper "run" only forwards to "execute" in a body of 3 lines or less; inline or merge names`,
	) {
		t.Fatalf(
			"expected short trivial forwarder finding with multiple callsites, got:\n%s",
			joined,
		)
	}
}

func TestSkipsUnusedPrivateTrivialForwarder(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func execute(name string) bool { return name != "" }

func run(name string) bool {
	return execute(name)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `private helper "run" only forwards to "execute"`) {
		t.Fatalf("unexpected trivial forwarder finding for unused helper, got:\n%s", joined)
	}
}

func TestSkipsLongTrivialForwarderWithMultipleCallsites(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

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
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `only forwards to "execute"`) {
		t.Fatalf(
			"unexpected long trivial forwarder finding with multiple callsites, got:\n%s",
			joined,
		)
	}
}

func TestDetectsPrivateTrivialForwarderMethod(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type runner struct {
	name string
}

func execute(name string) bool { return name != "" }

func (r runner) run() bool {
	return execute(r.name)
}

func use(r runner) bool { return r.run() }
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`private helper "run" only forwards to "execute" at one production use; inline or merge names`,
	) {
		t.Fatalf("expected private method trivial forwarder finding, got:\n%s", joined)
	}
}

func TestDetectsExportedTrivialForwarderMethodWithoutPrivateReceiverUse(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type Runner struct{}

func execute(name string) bool { return name != "" }

func (r Runner) Run(name string) bool {
	return execute(name)
}

func a(r Runner) bool { return r.Run("a") }
func b(r Runner) bool { return r.Run("b") }
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`exported method "Run" only forwards to "execute" in a body of 3 lines or less; inline or merge names`,
	) {
		t.Fatalf("expected exported method trivial forwarder finding, got:\n%s", joined)
	}
}

func TestDetectsDocumentedExportedTrivialForwarderMethod(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type Runner struct{}

func execute(name string) bool { return name != "" }

// Run reports whether name can run.
func (r Runner) Run(name string) bool {
	return execute(name)
}

func a(r Runner) bool { return r.Run("a") }
func b(r Runner) bool { return r.Run("b") }
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`exported method "Run" only forwards to "execute" in a body of 3 lines or less; inline or merge names`,
	) {
		t.Fatalf("expected documented exported method trivial forwarder finding, got:\n%s", joined)
	}
}

func TestSkipsExportedTrivialForwarderMethodWithPrivateReceiverField(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
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

	if strings.Contains(joined, `only forwards to "execute"`) {
		t.Fatalf("unexpected trivial forwarder finding for exported method, got:\n%s", joined)
	}
}

func TestSkipsExportedTrivialForwarderMethodWithPrivateReceiverMethod(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

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
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `only forwards to "execute"`) {
		t.Fatalf(
			"unexpected trivial forwarder finding for exported method using private method, got:\n%s",
			joined,
		)
	}
}

func TestSkipsExportedCodecHookForwarderMethod(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

import "encoding/json"

type Payload string

func (p Payload) MarshalJSON() ([]byte, error) {
	return json.Marshal(string(p))
}

func encode(p Payload) ([]byte, error) {
	return json.Marshal(p)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `exported method "MarshalJSON" only forwards to "json.Marshal"`) {
		t.Fatalf("unexpected trivial forwarder finding for codec hook, got:\n%s", joined)
	}
}

func TestDetectsRestatementComment(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

// Validate user
func validateUser(name string) bool {
	return name != ""
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`comment only restates private declaration "validateUser"; remove or explain intent`,
	) {
		t.Fatalf("expected restatement-comment finding, got:\n%s", joined)
	}

	if !hasIssueKind(issues, "comment_noise") {
		t.Fatalf("expected comment_noise kind, got %#v", issues)
	}
}

func TestSkipsRestatementCommentWhenIntentAdded(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

// Validate user before cache warmup.
func validateUser(name string) bool {
	return name != ""
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `comment only restates private declaration "validateUser"`) {
		t.Fatalf(
			"unexpected restatement-comment finding when comment adds intent, got:\n%s",
			joined,
		)
	}
}
