package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectsDeadPrivateDecls(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

const unusedLimit = 3

type unusedState struct {
	Name string
}

var unusedName = "x"

func unusedHelper() {
	println(unusedLimit, unusedName)
	_ = unusedState{}
}

func Live() {}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	want := `private function "unusedHelper" is never used by production code; remove it`
	if !strings.Contains(joined, want) {
		t.Fatalf("expected dead-code subgraph root finding %q, got:\n%s", want, joined)
	}

	for _, cascade := range []string{"unusedLimit", "unusedState", "unusedName"} {
		if strings.Contains(joined, cascade) {
			t.Fatalf("unexpected dead-code subgraph cascade %q, got:\n%s", cascade, joined)
		}
	}

	if !hasIssueKind(issues, "dead_code") {
		t.Fatalf("expected dead_code kind, got %#v", issues)
	}
}

func TestDeadPrivateDeclsFollowProductionRoots(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

const liveLimit = 3

type liveState struct {
	Name string
}

var liveName = "x"

func liveHelper() liveState {
	println(liveLimit, liveName)
	return liveState{}
}

func Live() {
	liveHelper()
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `is never used by production code`) {
		t.Fatalf("unexpected dead-code finding for rooted declarations, got:\n%s", joined)
	}
}

func TestDeadPrivateDeclsIgnoreTestOnlyUses(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func helperOnlyUsedByTest() {}
`)
	writeFile(t, filepath.Join(tmp, "sample_test.go"), `package sample

import "testing"

func TestHelper(t *testing.T) {
	helperOnlyUsedByTest()
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`private function "helperOnlyUsedByTest" is never used by production code; remove it`,
	) {
		t.Fatalf("expected test-only use to remain dead production code, got:\n%s", joined)
	}
}

func TestRepoDeadCodeReportsInternalTestOnlySubgraphRoot(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	lib.Live()`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

type testOnlyState struct {
	value int
}

func testOnlyEntry() int {
	return testOnlyLeaf(testOnlyState{value: 1})
}

func testOnlyLeaf(state testOnlyState) int {
	return state.value
}

func Live() {}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib_test.go"), `package lib

import "testing"

func TestTestOnlyEntry(t *testing.T) {
	if got := testOnlyEntry(); got != 1 {
		t.Fatalf("testOnlyEntry() = %d, want 1", got)
	}
}
`)

	joined := joinMessages(lintInDir(t, tmp))
	if !strings.Contains(
		joined,
		`private function "testOnlyEntry" is never used by production code; remove it`,
	) {
		t.Fatalf("expected internal-test-only subgraph root finding, got:\n%s", joined)
	}

	for _, cascade := range []string{
		`private type "testOnlyState"`,
		`private field "testOnlyState.value"`,
		`private function "testOnlyLeaf"`,
	} {
		if strings.Contains(joined, cascade) {
			t.Fatalf("unexpected internal-test-only subgraph cascade %q, got:\n%s", cascade, joined)
		}
	}
}

func TestRepoDeadCodeReportsExternalTestOnlySubgraphRoot(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	lib.Live()`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

func TestOnlyEntry() int {
	return testOnlyLeaf()
}

func testOnlyLeaf() int {
	return 1
}

func Live() {}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib_test.go"), `package lib_test

import (
	"testing"

	"example.com/sample/lib"
)

func TestTestOnlyEntry(t *testing.T) {
	if got := lib.TestOnlyEntry(); got != 1 {
		t.Fatalf("TestOnlyEntry() = %d, want 1", got)
	}
}
`)

	joined := joinMessages(lintInDir(t, tmp))
	if !strings.Contains(
		joined,
		`exported function "TestOnlyEntry" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf("expected external-test-only subgraph root finding, got:\n%s", joined)
	}

	if strings.Contains(joined, `private function "testOnlyLeaf"`) {
		t.Fatalf("unexpected external-test-only subgraph cascade, got:\n%s", joined)
	}
}

func TestRepoDeadCodeReportsActiveBuildTaggedTestOnlySubgraphRoot(t *testing.T) {
	t.Setenv("GOFLAGS", "-tags=deadcode_fixture")

	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	lib.Live()`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

func Live() {}
`)
	writeFile(t, filepath.Join(tmp, "lib", "tagged.go"), `//go:build deadcode_fixture

package lib

func taggedTestOnlyEntry() int {
	return taggedTestOnlyLeaf()
}

func taggedTestOnlyLeaf() int {
	return 1
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "tagged_test.go"), `//go:build deadcode_fixture

package lib

import "testing"

func TestTaggedTestOnlyEntry(t *testing.T) {
	if got := taggedTestOnlyEntry(); got != 1 {
		t.Fatalf("taggedTestOnlyEntry() = %d, want 1", got)
	}
}
`)

	joined := joinMessages(lintInDir(t, tmp))
	if !strings.Contains(
		joined,
		`private function "taggedTestOnlyEntry" is never used by production code; remove it`,
	) {
		t.Fatalf("expected active tagged test-only subgraph root finding, got:\n%s", joined)
	}

	if strings.Contains(joined, `private function "taggedTestOnlyLeaf"`) {
		t.Fatalf("unexpected active tagged test-only subgraph cascade, got:\n%s", joined)
	}
}

func TestDeadPrivateDeclsSkipSideEffectVarInitializers(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

var unusedValue = build()

func build() string {
	return "x"
}

func Live() {}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `private var "unusedValue" is never used`) {
		t.Fatalf("unexpected dead-code finding for side-effect var initializer, got:\n%s", joined)
	}

	if strings.Contains(joined, `private function "build" is never used`) {
		t.Fatalf("initializer call should root build, got:\n%s", joined)
	}
}

func TestDeadPrivateDeclsIgnoreLocalShadows(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

var unusedValue = "pkg"

func Live() {
	unusedValue := "local"
	println(unusedValue)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`private var "unusedValue" is never used by production code; remove it`,
	) {
		t.Fatalf("expected package var shadowed by local var to stay dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeFollowsMainEntrypoint(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	lib.Live()`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

func Live() {
	usedPrivate()
}

func UnusedExported() {
	deadPrivate()
}

func usedPrivate() {}

func deadPrivate() {}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	want := `exported function "UnusedExported" is unreachable from repo entrypoints; remove it`
	if !strings.Contains(joined, want) {
		t.Fatalf("expected repo dead-code subgraph root finding %q, got:\n%s", want, joined)
	}

	if strings.Contains(joined, `private function "deadPrivate"`) {
		t.Fatalf("unexpected repo dead-code subgraph cascade, got:\n%s", joined)
	}

	if strings.Contains(joined, `private function "usedPrivate" is never used`) {
		t.Fatalf("live private function reported dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeDetectsUnusedMethods(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	lib.Live()`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

type Service struct{}

func Live() {
	Service{}.used()
}

func (Service) used() {}

func (Service) UnusedExported() {}

func (Service) unusedPrivate() {}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	for _, want := range []string{
		`exported method "UnusedExported" is unreachable from repo entrypoints; remove it`,
		`private method "unusedPrivate" is never used by production code; remove it`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected method dead-code finding %q, got:\n%s", want, joined)
		}
	}

	if strings.Contains(joined, `private method "used" is never used`) {
		t.Fatalf("live method reported dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsInterfaceMethods(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	lib.Facts()`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

type Fact interface {
	AFact()
}

type callSummaryFact struct{}

func (*callSummaryFact) AFact() {}

func (*callSummaryFact) unusedPrivate() {}

func Facts() []Fact {
	return []Fact{new(callSummaryFact)}
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `method "AFact"`) {
		t.Fatalf("interface method reported dead, got:\n%s", joined)
	}

	if !strings.Contains(
		joined,
		`private method "unusedPrivate" is never used by production code; remove it`,
	) {
		t.Fatalf("expected unused private method finding, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsStructFieldInterfaceMethods(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_ = lib.Response()`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import (
	"io"
	"net/http"
)

type body struct{}

func (*body) Read([]byte) (int, error) { return 0, io.EOF }
func (*body) Close() error { return nil }
func (*body) unusedPrivate() {}

func Response() *http.Response {
	return &http.Response{Body: &body{}}
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	for _, unexpected := range []string{
		`method "Read"`,
		`method "Close"`,
	} {
		if strings.Contains(joined, unexpected) {
			t.Fatalf("interface method reported dead for %q, got:\n%s", unexpected, joined)
		}
	}

	if !strings.Contains(
		joined,
		`private method "unusedPrivate" is never used by production code; remove it`,
	) {
		t.Fatalf("expected unused private method finding, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsGenericConstraintMethods(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	lib.Live()`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

type rowMarker interface {
	markRow()
}

type row struct{}

func (row) markRow() {}
func (row) unusedPrivate() {}

func emit[T rowMarker](value T) {}

func Live() {
	emit(row{})
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `method "markRow"`) {
		t.Fatalf("generic constraint method reported dead, got:\n%s", joined)
	}

	if !strings.Contains(
		joined,
		`private method "unusedPrivate" is never used by production code; remove it`,
	) {
		t.Fatalf("expected unused private method finding, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsCrossPackageGenericConstraintMethods(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/source"

func main() {
	source.Live()
}
`)
	writeFile(t, filepath.Join(tmp, "contract", "contract.go"), `package contract

type Row interface {
	processRow()
}

type DailyBar struct{}

func (DailyBar) processRow() {}
func (DailyBar) unusedPrivate() {}
`)
	writeFile(t, filepath.Join(tmp, "source", "source.go"), `package source

import "example.com/sample/contract"

func emit[T contract.Row](value T) {}

func Live() {
	emit(contract.DailyBar{})
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `method "processRow"`) {
		t.Fatalf("cross-package generic constraint method reported dead, got:\n%s", joined)
	}

	if !strings.Contains(
		joined,
		`private method "unusedPrivate" is never used by production code; remove it`,
	) {
		t.Fatalf("expected unused private method finding, got:\n%s", joined)
	}
}

func TestDeadCodeKeepsPrivateMarkerMethodsWithoutLocalUse(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "contract", "contract.go"), `package contract

type ProcessRow interface {
	processRow()
}

type DailyBar struct{}

func (DailyBar) processRow() {}
func (DailyBar) unusedPrivate() {}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `method "processRow"`) {
		t.Fatalf("marker method reported dead, got:\n%s", joined)
	}

	if !strings.Contains(
		joined,
		`private method "unusedPrivate" is never used by production code; remove it`,
	) {
		t.Fatalf("expected unused private method finding, got:\n%s", joined)
	}
}

func TestDeadCodeKeepsPrivateMarkerMethodsOnSealedInterfaces(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "contract", "contract.go"), `package contract

type NamedRow interface {
	Name() string
}

type ProcessRow interface {
	NamedRow
	processRow()
	Shard() string
}

type DailyBar struct{}

func (DailyBar) Name() string { return "daily" }
func (DailyBar) Shard() string { return "default" }
func (DailyBar) processRow() {}
func (DailyBar) unusedPrivate() {}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `method "processRow"`) {
		t.Fatalf("sealed interface marker method reported dead, got:\n%s", joined)
	}

	if !strings.Contains(
		joined,
		`private method "unusedPrivate" is never used by production code; remove it`,
	) {
		t.Fatalf("expected unused private method finding, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsErrorProtocolMethods(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	lib.Live()`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "errors"

var errSentinel = errors.New("sentinel")

type wrappedError struct {
	err error
}

func (err wrappedError) Error() string {
	return err.err.Error()
}

func (err wrappedError) Unwrap() error {
	return err.err
}

func (err wrappedError) Is(target error) bool {
	return target == errSentinel
}

func (err wrappedError) As(target any) bool {
	return false
}

func (err wrappedError) unusedPrivate() {}

func Live() {
	err := wrappedError{err: errSentinel}
	_ = errors.Is(err, errSentinel)

	var target wrappedError
	_ = errors.As(err, &target)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	for _, unexpected := range []string{
		`method "Unwrap"`,
		`method "Is"`,
		`method "As"`,
	} {
		if strings.Contains(joined, unexpected) {
			t.Fatalf("error protocol method reported dead for %q, got:\n%s", unexpected, joined)
		}
	}

	if !strings.Contains(
		joined,
		`private method "unusedPrivate" is never used by production code; remove it`,
	) {
		t.Fatalf("expected unused private method finding, got:\n%s", joined)
	}
}

func TestRepoDeadCodeReportsErrorProtocolNamesOnNonErrors(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	lib.Live()`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

type matcher struct{}

func (matcher) Is(target error) bool {
	return false
}

func Live() {
	_ = matcher{}
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`exported method "Is" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf("expected non-error Is method finding, got:\n%s", joined)
	}
}
