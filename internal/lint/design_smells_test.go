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

func TestDetectsLargeUngroupedConstChunk(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type state string

const (
	stateUnknown state = ""
	statePending state = "pending"
	stateRunning state = "running"
	stateStopped state = "stopped"
	stateFailed state = "failed"
	stateSkipped state = "skipped"
	stateQueued state = "queued"
	stateBlocked state = "blocked"
	stateReady state = "ready"
	stateDone state = "done"
	stateArchived state = "archived"
	statePaused state = "paused"
)

func use(state) {}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`const chunk has 12 consecutive items; split related declarations with blank lines, group comments, or smaller const blocks`,
	) {
		t.Fatalf("expected const-grouping finding, got:\n%s", joined)
	}

	if !hasIssueKind(issues, "const_grouping") {
		t.Fatalf("expected const_grouping kind, got %#v", issues)
	}
}

func TestSkipsLargeConstBlockWithGroupBreaks(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type state string

const (
	stateUnknown state = ""
	statePending state = "pending"
	stateRunning state = "running"
	stateStopped state = "stopped"
	stateFailed state = "failed"
	stateSkipped state = "skipped"

	// terminal states
	stateDone state = "done"
	stateArchived state = "archived"
	stateExpired state = "expired"
	stateCancelled state = "cancelled"
	stateFailedHard state = "failed_hard"
)

func use(state) {}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `const chunk has`) {
		t.Fatalf("unexpected const-grouping finding, got:\n%s", joined)
	}
}

func TestDetectsLargeUngroupedConstChunkInTestFile(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func use(state) {}

type state string
`)
	writeFile(t, filepath.Join(tmp, "sample_test.go"), `package sample

const (
	stateUnknown state = ""
	statePending state = "pending"
	stateRunning state = "running"
	stateStopped state = "stopped"
	stateFailed state = "failed"
	stateSkipped state = "skipped"
	stateQueued state = "queued"
	stateBlocked state = "blocked"
	stateReady state = "ready"
	stateDone state = "done"
	stateArchived state = "archived"
	statePaused state = "paused"
)
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`const chunk has 12 consecutive items; split related declarations with blank lines, group comments, or smaller const blocks`,
	) {
		t.Fatalf("expected const-grouping finding in test file, got:\n%s", joined)
	}
}

func TestDetectsAdjacentUngroupedConstDecls(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type state string

const stateUnknown state = ""
const statePending state = "pending"
const stateRunning state = "running"
const stateStopped state = "stopped"
const stateFailed state = "failed"
const stateSkipped state = "skipped"
const stateQueued state = "queued"
const stateBlocked state = "blocked"
const stateReady state = "ready"
const stateDone state = "done"
const stateArchived state = "archived"
const statePaused state = "paused"

func use(state) {}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`const chunk has 12 consecutive items; split related declarations with blank lines, group comments, or smaller const blocks`,
	) {
		t.Fatalf("expected const-grouping finding, got:\n%s", joined)
	}

	if !hasIssueKind(issues, "const_grouping") {
		t.Fatalf("expected const_grouping kind, got %#v", issues)
	}
}

func TestDetectsLargeUngroupedVarChunk(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

var (
	cacheA = 1
	cacheB = 2
	cacheC = 3
	cacheD = 4
	cacheE = 5
	cacheF = 6
	cacheG = 7
	cacheH = 8
	cacheI = 9
	cacheJ = 10
	cacheK = 11
	cacheL = 12
)
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`var chunk has 12 consecutive items; split related declarations with blank lines, group comments, or smaller var blocks`,
	) {
		t.Fatalf("expected var-grouping finding, got:\n%s", joined)
	}

	if !hasIssueKind(issues, "var_grouping") {
		t.Fatalf("expected var_grouping kind, got %#v", issues)
	}
}

func TestDetectsAdjacentUngroupedTypeDecls(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type reqA struct{}
type reqB struct{}
type reqC struct{}
type reqD struct{}
type reqE struct{}
type reqF struct{}
type reqG struct{}
type reqH struct{}
type reqI struct{}
type reqJ struct{}
type reqK struct{}
type reqL struct{}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`type chunk has 12 consecutive items; split related declarations with blank lines, group comments, or smaller type blocks`,
	) {
		t.Fatalf("expected type-grouping finding, got:\n%s", joined)
	}

	if !hasIssueKind(issues, "type_grouping") {
		t.Fatalf("expected type_grouping kind, got %#v", issues)
	}
}

func TestDetectsMixedConstPrefixes(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

const (
	statusReady = "ready"
	statusDone = "done"
	phaseInit = "init"
	phaseRun = "run"
	modeFast = "fast"
	modeSlow = "slow"
)
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`const chunk mixes mode, phase, status prefixes without grouping; split unrelated const families with blank lines or group comments`,
	) {
		t.Fatalf("expected mixed-const-prefix finding, got:\n%s", joined)
	}

	if !hasIssueKind(issues, "mixed_const_prefixes") {
		t.Fatalf("expected mixed_const_prefixes kind, got %#v", issues)
	}
}

func TestSkipsMixedConstPrefixesWithGroupBreaks(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

const (
	statusReady = "ready"
	statusDone = "done"

	phaseInit = "init"
	phaseRun = "run"

	modeFast = "fast"
	modeSlow = "slow"
)
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `const chunk mixes`) {
		t.Fatalf("unexpected mixed-const-prefix finding, got:\n%s", joined)
	}
}

func TestDetectsUnnamedLargeTableTest(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample_test.go"), `package sample

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		input string
		want string
	}{
		{input: "a", want: "a"},
		{input: "b", want: "b"},
		{input: "c", want: "c"},
		{input: "d", want: "d"},
		{input: "e", want: "e"},
		{input: "f", want: "f"},
		{input: "g", want: "g"},
		{input: "h", want: "h"},
		{input: "i", want: "i"},
		{input: "j", want: "j"},
		{input: "k", want: "k"},
	}
	_ = cases
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`table test has 11 cases without name/desc field; add case names so failures identify scenarios`,
	) {
		t.Fatalf("expected table-test-grouping finding, got:\n%s", joined)
	}

	if !hasIssueKind(issues, "table_test_grouping") {
		t.Fatalf("expected table_test_grouping kind, got %#v", issues)
	}
}

func TestSkipsNamedLargeTableTest(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample_test.go"), `package sample

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		name string
		input string
		want string
	}{
		{name: "a", input: "a", want: "a"},
		{name: "b", input: "b", want: "b"},
		{name: "c", input: "c", want: "c"},
		{name: "d", input: "d", want: "d"},
		{name: "e", input: "e", want: "e"},
		{name: "f", input: "f", want: "f"},
		{name: "g", input: "g", want: "g"},
		{name: "h", input: "h", want: "h"},
		{name: "i", input: "i", want: "i"},
		{name: "j", input: "j", want: "j"},
		{name: "k", input: "k", want: "k"},
	}
	_ = cases
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `table test has`) {
		t.Fatalf("unexpected table-test-grouping finding, got:\n%s", joined)
	}
}

func TestSkipsAdjacentConstDeclsWithGroupBreaks(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type state string

const stateUnknown state = ""
const statePending state = "pending"
const stateRunning state = "running"
const stateStopped state = "stopped"
const stateFailed state = "failed"
const stateSkipped state = "skipped"

// terminal states
const stateDone state = "done"
const stateArchived state = "archived"
const stateExpired state = "expired"
const stateCancelled state = "cancelled"
const stateFailedHard state = "failed_hard"

func use(state) {}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `const chunk has`) {
		t.Fatalf("unexpected const-grouping finding, got:\n%s", joined)
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
