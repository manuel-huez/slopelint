package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRepoDeadCodeKeepsInterfaceAssertionMethods(t *testing.T) {
	tmp := newTestModule(t)
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
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_, _ = lib.Save(lib.State{Status: lib.StatusReady, Name: "x"})
	_, _ = lib.Load(nil)`)
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
	Status  Status `+"`json:\"status\"`"+`
	Name    string `+"`json:\"name\"`"+`
	Extra   string `+"`json:\"extra\"`"+`
	Ignored string `+"`json:\"-\"`"+`
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
		wantMethodMarshalJSON,
		wantMethodUnmarshalJSON,
		`function "parseStatus"`,
		`field "State.Status"`,
		`field "State.Name"`,
		`field "State.Extra"`,
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
		`exported field "State.Ignored" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf("expected ignored reflected field finding, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsMarshalOnlyFields(t *testing.T) {
	tmp := newTestModule(t)
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
		`field "ProofSummary.Verdict"`,
		`field "ProofSummary.BlockingFailures"`,
		`field "ProofSummary.LocalOnly"`,
	} {
		if strings.Contains(joined, want) {
			t.Fatalf("marshal-only field reported dead for %q, got:\n%s", want, joined)
		}
	}
}

func TestRepoDeadCodeDetectsExportedTypesVarsConstsFields(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	lib.Live()`)
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
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	lib.Live()`)
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
