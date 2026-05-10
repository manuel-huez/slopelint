package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectsDuplicateValidationLadder(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type req struct {
	name string
	kind string
	size int
}

func create(value req) error {
	if value.name == "" {
		return errBad
	}
	if value.kind == "" {
		return errBad
	}
	if value.size == 0 {
		return errBad
	}
	return nil
}

func update(value req) error {
	if value.name == "" {
		return errBad
	}
	if value.kind == "" {
		return errBad
	}
	if value.size == 0 {
		return errBad
	}
	return nil
}

var errBad error
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`validation ladder in "update" duplicates "create"; extract shared validation`,
	) {
		t.Fatalf("expected duplicate validation finding, got:\n%s", joined)
	}

	if !hasIssueKind(issues, "duplicate_validation") {
		t.Fatalf("expected duplicate_validation kind, got %#v", issues)
	}
}

func TestDetectsSingleUsePrivateHelper(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func setupHelper(value *int) {
	*value = 1
	println(*value)
}

func run() {
	var value int
	setupHelper(&value)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`private helper "setupHelper" has one production callsite and a tiny body; inline or give it a stronger role`,
	) {
		t.Fatalf("expected single-use helper finding, got:\n%s", joined)
	}

	if !strings.Contains(
		joined,
		`private helper "setupHelper" has generic name and one tiny callsite; rename or inline`,
	) {
		t.Fatalf("expected paired generic-name finding, got:\n%s", joined)
	}
}

func TestSkipsSingleUsePrivateHelperWithComplexBody(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

import "sort"

func sortValues(values []int) {
	sort.SliceStable(values, func(i, j int) bool {
		return values[i] < values[j]
	})
}

func run(values []int) {
	sortValues(values)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `has one production callsite and a tiny body`) {
		t.Fatalf("unexpected single-use helper finding for complex body, got:\n%s", joined)
	}
}

func TestDetectsSingleImplInterface(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type writer interface {
	Write(string)
}

type fileWriter struct{}

func (fileWriter) Write(value string) {}

func use(value writer) {
	value.Write("x")
}

func run() {
	use(fileWriter{})
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`private interface "writer" has one in-package implementation "fileWriter"; use concrete type unless substitution is needed`,
	) {
		t.Fatalf("expected single-impl interface finding, got:\n%s", joined)
	}
}

func TestDetectsOptionsOverkill(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type config struct { name string }
type option func(*config)

func withName(name string) option {
	return func(cfg *config) { cfg.name = name }
}

func newThing(opts ...option) int {
	var cfg config
	for _, opt := range opts {
		opt(&cfg)
	}
	return len(cfg.name)
}

func run() int {
	return newThing(withName("x"))
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`private API "newThing" uses functional options for 1 production callsites; pass config directly`,
	) {
		t.Fatalf("expected options-overkill finding, got:\n%s", joined)
	}
}

func TestSkipsOptionsOverkillWhenPrefixIsNotConstructorWord(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type config struct { name string }
type option func(*config)

func withName(name string) option {
	return func(cfg *config) { cfg.name = name }
}

func newsThing(opts ...option) int {
	var cfg config
	for _, opt := range opts {
		opt(&cfg)
	}
	return len(cfg.name)
}

func run() int {
	return newsThing(withName("x"))
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `private API "newsThing" uses functional options`) {
		t.Fatalf(
			"non-constructor prefix should not trigger options-overkill finding, got:\n%s",
			joined,
		)
	}
}

func TestDetectsInternalResultWrapper(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type parseResult struct {
	value string
	ok    bool
}

func parse() parseResult {
	return parseResult{value: "x", ok: true}
}

func run() bool {
	return parse().ok
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`private result wrapper "parseResult" only carries value plus status; return ordinary Go results`,
	) {
		t.Fatalf("expected result-wrapper finding, got:\n%s", joined)
	}
}
