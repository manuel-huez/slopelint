package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectsDeadPrivateDecls(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
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

	for _, want := range []string{
		`private const "unusedLimit" is never used by production code; remove it`,
		`private type "unusedState" is never used by production code; remove it`,
		`private var "unusedName" is never used by production code; remove it`,
		`private function "unusedHelper" is never used by production code; remove it`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected dead-code finding %q, got:\n%s", want, joined)
		}
	}

	if !hasIssueKind(issues, "dead_code") {
		t.Fatalf("expected dead_code kind, got %#v", issues)
	}
}

func TestDeadPrivateDeclsFollowProductionRoots(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
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
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
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

func TestDeadPrivateDeclsSkipSideEffectVarInitializers(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
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
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
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
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	lib.Live()
}
`)
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

	for _, want := range []string{
		`exported function "UnusedExported" is unreachable from repo entrypoints; remove it`,
		`private function "deadPrivate" is never used by production code; remove it`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected repo dead-code finding %q, got:\n%s", want, joined)
		}
	}

	if strings.Contains(joined, `private function "usedPrivate" is never used`) {
		t.Fatalf("live private function reported dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeDetectsUnusedMethods(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	lib.Live()
}
`)
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
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	lib.Facts()
}
`)
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
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_ = lib.Response()
}
`)
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
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	lib.Live()
}
`)
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
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
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
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
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
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
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
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	lib.Live()
}
`)
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
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	lib.Live()
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

type matcher struct{}

func (matcher) Is(target error) bool {
	return false
}

func Live() {}
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

func TestRepoDeadCodeKeepsFmtStringerMethods(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	lib.Live()
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "fmt"

type phase uint8

const phaseFetch phase = 1

func (p phase) String() string {
	return "fetch"
}

func (p phase) unusedPrivate() {}

func Live() string {
	return fmt.Sprintf("phase=%*s/%.*s", 8, phaseFetch, 3, phaseFetch)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `method "String"`) {
		t.Fatalf("fmt Stringer method reported dead, got:\n%s", joined)
	}

	if !strings.Contains(
		joined,
		`private method "unusedPrivate" is never used by production code; remove it`,
	) {
		t.Fatalf("expected unused private method finding, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsInterfaceAssertionMethods(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import (
	"example.com/sample/engine"
	"example.com/sample/ingester"
)

func main() {
	engine.Run(ingester.New())
}
`)
	writeFile(t, filepath.Join(tmp, "contract", "contract.go"), `package contract

type Ingester interface {
	NewProcessSink()
}
`)
	writeFile(t, filepath.Join(tmp, "engine", "engine.go"), `package engine

import "example.com/sample/contract"

type progressSnapshotProvider interface {
	ProgressSnapshot() Snapshot
}

type dedupClusterReleaser interface {
	ReleaseDedupCluster(string)
}

type snapshotFlusher interface {
	FlushSnapshot()
}

type Snapshot struct {
	Retained int
}

func Run(ingester contract.Ingester) {
	ingester.NewProcessSink()

	if provider, ok := ingester.(progressSnapshotProvider); ok {
		_ = provider.ProgressSnapshot()
	}

	if releaser, ok := ingester.(dedupClusterReleaser); ok {
		releaser.ReleaseDedupCluster("cluster")
	}

	switch flusher := ingester.(type) {
	case snapshotFlusher:
		flusher.FlushSnapshot()
	}
}
`)
	writeFile(t, filepath.Join(tmp, "ingester", "ingester.go"), `package ingester

import "example.com/sample/engine"

type rowAccumulator struct {
	seen map[string]struct{}
}

func (a *rowAccumulator) retainedCount() int {
	return len(a.seen)
}

type DuckDB struct {
	rows rowAccumulator
}

func New() *DuckDB {
	return &DuckDB{}
}

func (db *DuckDB) NewProcessSink() {}

func (db *DuckDB) ProgressSnapshot() engine.Snapshot {
	return engine.Snapshot{Retained: db.retainedCountsLocked()}
}

func (db *DuckDB) retainedCountsLocked() int {
	return db.rows.retainedCount()
}

func (db *DuckDB) ReleaseDedupCluster(clusterKey string) {
	releaseUniqueKeyCluster(db.rows.seen, clusterKey)
}

func (db *DuckDB) FlushSnapshot() {
	flushRows(db.rows.seen)
}

func releaseUniqueKeyCluster(seen map[string]struct{}, clusterKey string) {
	delete(seen, clusterKey)
}

func flushRows(seen map[string]struct{}) {
	delete(seen, "snapshot")
}

func (db *DuckDB) unusedPrivate() {}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	for _, unexpected := range []string{
		`method "ProgressSnapshot"`,
		`method "ReleaseDedupCluster"`,
		`method "retainedCountsLocked"`,
		`method "retainedCount"`,
		`function "releaseUniqueKeyCluster"`,
		`method "FlushSnapshot"`,
		`function "flushRows"`,
	} {
		if strings.Contains(joined, unexpected) {
			t.Fatalf(
				"interface assertion dependency reported dead for %q, got:\n%s",
				unexpected,
				joined,
			)
		}
	}

	if !strings.Contains(
		joined,
		`private method "unusedPrivate" is never used by production code; remove it`,
	) {
		t.Fatalf("expected unused private method finding, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsMarshalPrefixedMethods(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_, _ = lib.Save(lib.State{Status: lib.StatusReady, Name: "x"})
	_, _ = lib.Load(nil)
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import (
	"encoding/json"
	"fmt"
)

type Status int

const StatusReady Status = 1

func (status Status) MarshalJSON() ([]byte, error) {
	return json.Marshal(statusName(status))
}

func (status *Status) UnmarshalJSON(body []byte) error {
	var value string
	if err := json.Unmarshal(body, &value); err != nil {
		return err
	}

	parsed, err := parseStatus(value)
	if err != nil {
		return err
	}

	*status = parsed

	return nil
}

func statusName(status Status) string {
	if status == StatusReady {
		return "ready"
	}

	return "unknown"
}

func parseStatus(value string) (Status, error) {
	if value == "ready" {
		return StatusReady, nil
	}

	return 0, fmt.Errorf("invalid status %q", value)
}

type State struct {
	Status Status `+"`json:\"status\"`"+`
	Name   string `+"`json:\"name\"`"+`
	Extra  string `+"`json:\"extra\"`"+`
}

func Save(state State) ([]byte, error) {
	return json.MarshalIndent(state, "", "  ")
}

func Load(body []byte) (State, error) {
	var state State
	err := json.Unmarshal(body, &state)

	return state, err
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	for _, unexpected := range []string{
		`method "MarshalJSON"`,
		`method "UnmarshalJSON"`,
		`function "parseStatus"`,
		`field "State.Status"`,
		`field "State.Name"`,
	} {
		if strings.Contains(joined, unexpected) {
			t.Fatalf(
				"marshal-prefixed declaration reported dead for %q, got:\n%s",
				unexpected,
				joined,
			)
		}
	}

	if !strings.Contains(
		joined,
		`exported field "State.Extra" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf("expected unused reflected field finding, got:\n%s", joined)
	}
}

func TestRepoDeadCodeReportsTaggedFieldsWithoutInRepoUse(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/report"

func main() {
	_, _ = report.Render()
}
`)
	writeFile(t, filepath.Join(tmp, "report", "report.go"), `package report

import "encoding/json"

type ProofSummary struct {
	Verdict          string   `+"`json:\"verdict\"`"+`
	BlockingFailures []string `+"`yaml:\"blocking_failures\"`"+`
	LocalOnly        string
}

func Render() ([]byte, error) {
	return json.Marshal(ProofSummary{})
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	for _, want := range []string{
		`exported field "ProofSummary.Verdict" is unreachable from repo entrypoints; remove it`,
		`exported field "ProofSummary.BlockingFailures" is unreachable from repo entrypoints; remove it`,
		`exported field "ProofSummary.LocalOnly" is unreachable from repo entrypoints; remove it`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected tagged field finding %q, got:\n%s", want, joined)
		}
	}
}

func TestRepoDeadCodeKeepsAllMarshalAndUnmarshalPrefixedMethods(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), `module example.com/sample

go 1.22

require gopkg.in/yaml.v3 v3.0.0

replace gopkg.in/yaml.v3 => ./yaml
`)
	writeFile(t, filepath.Join(tmp, "yaml", "go.mod"), "module gopkg.in/yaml.v3\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "yaml", "yaml.go"), `package yaml

type Node struct{}

func Marshal(any) ([]byte, error) { return nil, nil }
func Unmarshal([]byte, any) error { return nil }
func (*Node) Decode(any) error { return nil }
`)
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	value := lib.Scalar("x")
	_, _ = lib.Save(value)
	_, _ = lib.Load(nil)
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "gopkg.in/yaml.v3"

type Scalar string

func (value Scalar) MarshalYAML() (any, error) {
	return string(value), nil
}

func (value Scalar) MarshalText() ([]byte, error) {
	return []byte(value), nil
}

func (value *Scalar) UnmarshalText([]byte) error {
	*value = "decoded"

	return nil
}

type Manifest struct {
	Value *Scalar `+"`yaml:\"value\"`"+`
}

func (manifest *Manifest) UnmarshalYAML(node *yaml.Node) error {
	type rawManifest Manifest

	var raw rawManifest
	if err := node.Decode(&raw); err != nil {
		return err
	}

	*manifest = Manifest(raw)

	return nil
}

func Save(value Scalar) ([]byte, error) {
	return yaml.Marshal(Manifest{Value: &value})
}

func Load(body []byte) (Manifest, error) {
	var manifest Manifest
	err := yaml.Unmarshal(body, &manifest)

	return manifest, err
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	for _, unexpected := range []string{
		`method "MarshalYAML"`,
		`method "MarshalText"`,
		`method "UnmarshalYAML"`,
		`method "UnmarshalText"`,
	} {
		if strings.Contains(joined, unexpected) {
			t.Fatalf(
				"YAML-reflected declaration reported dead for %q, got:\n%s",
				unexpected,
				joined,
			)
		}
	}
}

func TestRepoDeadCodeDetectsExportedTypesVarsConstsFields(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	lib.Live()
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

const LiveConst = 1
const DeadConst = 2

var LiveVar = LiveConst
var DeadVar = 3

type LiveType struct {
	UsedField      string
	UnusedExported string
	unusedPrivate  string
}

type DeadType int

func Live() {
	value := LiveType{UsedField: "x"}
	println(value.UsedField, LiveVar)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	for _, want := range []string{
		`exported const "DeadConst" is unreachable from repo entrypoints; remove it`,
		`exported var "DeadVar" is unreachable from repo entrypoints; remove it`,
		`exported type "DeadType" is unreachable from repo entrypoints; remove it`,
		`exported field "LiveType.UnusedExported" is unreachable from repo entrypoints; remove it`,
		`private field "LiveType.unusedPrivate" is never used by production code; remove it`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected repo dead-code finding %q, got:\n%s", want, joined)
		}
	}

	for _, unexpected := range []string{
		`"LiveConst"`,
		`"LiveVar"`,
		`"LiveType"`,
		`"LiveType.UsedField"`,
	} {
		if strings.Contains(joined, unexpected) {
			t.Fatalf("live declaration reported dead for %q, got:\n%s", unexpected, joined)
		}
	}
}

func TestRepoDeadCodeKeepsUnkeyedCompositeFields(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	lib.Live()
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

type Pair struct {
	First  int
	Second int
}

func Live() {
	_ = Pair{1, 2}
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `"Pair.First"`) || strings.Contains(joined, `"Pair.Second"`) {
		t.Fatalf("unkeyed composite fields reported dead, got:\n%s", joined)
	}
}
