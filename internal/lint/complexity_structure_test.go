package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectsBehaviorPreservingComplexityCluster(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func f(dst []int, src []int, items []string, ok bool) []int {
	if len(src) > 0 {
		dst = append(dst, src...)
	}

	if len(items) > 0 {
		for _, item := range items {
			println(item)
		}
	}

	if ok {
		println("same")
	} else {
		println("same")
	}

	return dst
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`function has 3 behavior-preserving simplification points; remove redundant branches/guards before adding more control flow`,
	) {
		t.Fatalf("expected complexity simplification cluster finding, got:\n%s", joined)
	}

	if !hasIssueKind(issues, "complexity_simplification") {
		t.Fatalf("expected complexity_simplification kind, got %#v", issues)
	}
}

func TestDetectsScatteredInputGuards(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type req struct {
	name string
	kind string
	size int
}

func f(value req) error {
	if value.name == "" {
		return errBad
	}

	println("prepare")

	if value.kind == "" {
		return errBad
	}

	println("build")

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
		`2 of 3 input guard returns are interleaved with other work; keep validation guards together or split function`,
	) {
		t.Fatalf("expected scattered input guard finding, got:\n%s", joined)
	}

	if !hasIssueKind(issues, "guard_complexity") {
		t.Fatalf("expected guard_complexity kind, got %#v", issues)
	}
}

func TestSkipsTopInputGuards(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type req struct {
	name string
	kind string
	size int
}

func f(value req) error {
	if value.name == "" {
		return errBad
	}
	if value.kind == "" {
		return errBad
	}
	if value.size == 0 {
		return errBad
	}

	println("work")

	return nil
}

var errBad error
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `input guard returns are interleaved`) {
		t.Fatalf("unexpected scattered guard finding for top guards, got:\n%s", joined)
	}
}

func TestInputGuardPreparation(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup string
		want  bool
	}{
		{name: "local predicate", setup: `if !valid(value.name) { return errBad }`},
		{name: "imported predicate", setup: `if value.id.IsZero() { return errBad }`},
		{name: "imported predicate then work", setup: `if value.id.IsZero() { return errBad }; println(value.name)`, want: true},
		{name: "success return", setup: `if value.size < 0 { return nil }`},
		{name: "local conditional setup", setup: `name := value.name; if value.kind != "" { name = value.kind }; _ = name`},
		{name: "imported preparation", setup: `day, ok := keyDay(value.id); if !ok { return errBad }; _ = day`},
		{name: "initializer validation", setup: `if err := validate(value.name); err != nil { return err }`},
		{name: "initializer context", setup: `if err := value.ctx.Err(); err != nil { return err }`},
		{name: "range validation", setup: `if err := validateNames(value.names); err != nil { return err }`},
		{name: "validation result", setup: `name, err := identity(value.name); if err != nil { return err }; _ = name`},
		{name: "cleanup", setup: `defer value.body.Close()`},
		{name: "conditional cleanup", setup: `if value.body != nil { defer func() { _ = value.body.Close() }() }`},
		{name: "work", setup: `println(value.name)`, want: true},
		{name: "eager defer argument", setup: `defer cleanup(read(value.name))`, want: true},
		{name: "cleanup with work", setup: `if value.body != nil { println(value.name); defer value.body.Close() }`, want: true},
		{name: "network result", setup: `response, err := http.Get(value.name); if err != nil { return err }; defer response.Body.Close()`, want: true},
		{name: "predicate with work argument", setup: `if !valid(read(value.name)) { return errBad }`, want: true},
		{name: "helper with work", setup: `name := read(value.name); _ = name`, want: true},
		{name: "predicate with mutation", setup: `if change(value.name) { return errBad }`, want: true},
		{name: "recursive predicate", setup: `if recurse(value.name) { return errBad }`},
		{name: "recursive work", setup: `if recurseWork(value.name) { return errBad }`, want: true},
		{name: "initializer work", setup: `if err := validateRead(value.name); err != nil { return err }`, want: true},
		{name: "range work", setup: `if err := readNames(value.names); err != nil { return err }`, want: true},
		{name: "range global mutation", setup: `if rangeChange(value.names) { return errBad }`, want: true},
		{name: "pointer mutation", setup: `if mutate(value) { return errBad }`, want: true},
		{name: "direct pointer mutation", setup: `value.size = value.size + 1`, want: true},
		{name: "direct global mutation", setup: `calls = value.size`, want: true},
		{name: "called closure", setup: `if closure(value.name) { return errBad }`, want: true},
		{name: "basic format", setup: `name := format(value.name); _ = name`},
		{name: "format with method", setup: `name := describe(value.name); _ = name`, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			tmp := newTestModule(t)
			writeFile(t, filepath.Join(tmp, "scalar", "value.go"), `package scalar
type ID int
func (id ID) IsZero() bool { return id == 0 }
func (id ID) Day() int { return int(id) }
`)
			writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"example.com/sample/scalar"
)

type req struct { name, kind string; size int; body io.ReadCloser; id scalar.ID; ctx context.Context; names []string }
var errBad error
var calls int
var _ = http.Get
func valid(value string) bool { return value != "" }
func identity(value string) (string, error) { return value, nil }
func read(value string) string { println(value); return value }
func cleanup(string) {}
func change(value string) bool { calls++; return value != "" }
func recurse(value string) bool { return recurse(value) }
func recurseWork(value string) bool { result := recurseOther(value); calls++; return result }
func recurseOther(value string) bool { return recurseWork(value) }
func keyDay(id scalar.ID) (int, bool) { if id.IsZero() { return 0, false }; return id.Day(), true }
func validate(value string) error { if value == "" { return errBad }; return nil }
func validateNames(values []string) error { for _, value := range values { if value == "" { return errBad } }; return nil }
func validateRead(value string) error { println(value); return nil }
func readNames(values []string) error { for _, value := range values { println(value) }; return nil }
func rangeChange(values []string) bool { for calls = range values {}; return calls > 0 }
func mutate(value *req) bool { value.size++; return value.size > 1 }
func closure(value string) bool { return func() bool { calls++; return value != "" }() }
func format(value string) string { return fmt.Sprintf("%s", value) }
type loud string
func (value loud) String() string { calls++; return string(value) }
func describe(value string) string { return fmt.Sprintf("%s", loud(value)) }

func f(value *req) error {
`+test.setup+`
	if value.name == "" { return errBad }
	if value.kind == "" { return errBad }
	if value.size == 0 { return errBad }
	return nil
}
`)

			issues := lintInDir(t, tmp)
			if got := hasIssueKind(issues, "guard_complexity"); got != test.want {
				t.Fatalf("guard finding = %v, want %v:\n%s", got, test.want, joinMessages(issues))
			}
		})
	}
}

func TestInputGuardsDoNotTrackUnknownCallResults(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample
import "io"
func write(writer io.Writer, payload []byte) error {
	first, firstErr := writer.Write(payload)
	println(first)
	if firstErr != nil { return firstErr }
	if first != len(payload) { return io.ErrShortWrite }
	second, secondErr := writer.Write(payload)
	if secondErr != nil { return secondErr }
	if second != len(payload) { return io.ErrShortWrite }
	return nil
}
`)

	issues := lintInDir(t, tmp)
	if hasIssueKind(issues, "guard_complexity") {
		t.Fatalf("output checks reported as input guards:\n%s", joinMessages(issues))
	}
}

func TestSkipsInterleavedErrorGuards(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func f(first func() error, second func() error, third func() error) error {
	err := first()
	if err != nil {
		return err
	}

	err = second()
	if err != nil {
		return err
	}

	err = third()
	if err != nil {
		return err
	}

	return nil
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `input guard returns are interleaved`) {
		t.Fatalf("unexpected scattered guard finding for error guards, got:\n%s", joined)
	}
}

func TestSkipsInterleavedNilSuccessReturns(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type req struct {
	name string
	kind string
	size int
}

func f(value req) (req, error) {
	if value.name == "" {
		return value, nil
	}

	println("prepare")

	if value.kind == "" {
		return value, nil
	}

	println("build")

	if value.size == 0 {
		return value, nil
	}

	return req{}, errBad
}

var errBad error
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `input guard returns are interleaved`) {
		t.Fatalf("unexpected scattered guard finding for nil success returns, got:\n%s", joined)
	}
}

func TestSkipsPackageVarStateChecksAsInputGuards(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

var featureEnabled bool
var stateReady bool
var allowFallback bool

func f() error {
	if featureEnabled {
		return errBad
	}

	println("prepare")

	if stateReady {
		return errBad
	}

	println("build")

	if allowFallback {
		return errBad
	}

	return nil
}

var errBad error
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `input guard returns are interleaved`) {
		t.Fatalf("unexpected scattered guard finding for package vars, got:\n%s", joined)
	}
}

func TestSkipsConstantOnlyChecksAsInputGuards(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

const featureEnabled = true

func f() error {
	always := true
	if always {
		return errBad
	}

	println("prepare")

	feature := featureEnabled
	if feature {
		return errBad
	}

	println("build")

	if 1 == 1 {
		return errBad
	}

	return nil
}

var errBad error
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `input guard returns are interleaved`) {
		t.Fatalf("unexpected scattered guard finding for constant checks, got:\n%s", joined)
	}
}

func TestSkipsNewConstructorErrorGuards(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type store struct{}
type manifestResult struct{}
type refResult struct{}
type resolvedResult struct{}

func New(root string) (store, error) {
	return store{}, nil
}

func (store) Load(id string) (manifestResult, error) {
	return manifestResult{}, nil
}

func ParseRef(value string) (refResult, error) {
	return refResult{}, nil
}

func ResolveArtifact(manifestResult, refResult) (resolvedResult, error) {
	return resolvedResult{}, nil
}

func f(root string, universeID string, modelRef string) (manifestResult, refResult, resolvedResult, error) {
	store, err := New(root)
	if err != nil {
		return manifestResult{}, refResult{}, resolvedResult{}, err
	}

	manifest, err := store.Load(universeID)
	if err != nil {
		return manifestResult{}, refResult{}, resolvedResult{}, err
	}

	ref, err := ParseRef(modelRef)
	if err != nil {
		return manifestResult{}, refResult{}, resolvedResult{}, err
	}

	resolved, err := ResolveArtifact(manifest, ref)
	if err != nil {
		return manifestResult{}, refResult{}, resolvedResult{}, err
	}

	return manifest, ref, resolved, nil
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `input guard returns are interleaved`) {
		t.Fatalf(
			"unexpected scattered guard finding for constructor error guards, got:\n%s",
			joined,
		)
	}
}

func TestSkipsComparatorDecisionLadder(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type row struct {
	name string
	kind string
	size int
}

func less(left row, right row) bool {
	if left.name != right.name {
		return left.name < right.name
	}

	if left.kind != right.kind {
		return left.kind < right.kind
	}

	if left.size != right.size {
		return left.size < right.size
	}

	return false
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `input guard returns are interleaved`) {
		t.Fatalf("unexpected scattered guard finding for comparator, got:\n%s", joined)
	}
}

func TestSkipsMethodPredicateInputGuards(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type id string

func (value id) IsZero() bool {
	return value == ""
}

func f(first id, second id, third id) error {
	if first.IsZero() {
		return errBad
	}
	if second.IsZero() {
		return errBad
	}
	if third.IsZero() {
		return errBad
	}

	println("work")

	return nil
}

var errBad error
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `input guard returns are interleaved`) {
		t.Fatalf("unexpected scattered guard finding for method predicates, got:\n%s", joined)
	}
}

func TestSkipsValidationPrepBetweenInputGuards(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

import "strings"

type id string

func (value id) IsZero() bool {
	return value == ""
}

func keyUnixDay(value int) (int, bool) {
	return value, value != 0
}

func f(instrumentID id, tradeDate int, dteBucket string, moneynessBucket string, contractType string) error {
	if instrumentID.IsZero() {
		return errBad
	}

	tradeDay, ok := keyUnixDay(tradeDate)
	if !ok {
		return errBad
	}

	dteBucket = strings.TrimSpace(strings.ToLower(dteBucket))
	if dteBucket == "" {
		return errBad
	}

	moneynessBucket = strings.TrimSpace(strings.ToLower(moneynessBucket))
	if moneynessBucket == "" {
		return errBad
	}

	contractType = strings.TrimSpace(strings.ToLower(contractType))
	if contractType != "call" && contractType != "put" {
		return errBad
	}

	println(tradeDay)

	return nil
}

var errBad error
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `input guard returns are interleaved`) {
		t.Fatalf("unexpected scattered guard finding for validation prep, got:\n%s", joined)
	}
}

func TestSkipsExportedValidationPrepBetweenInputGuards(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

import "strings"

func ParseName(value string) (string, bool) {
	value = strings.TrimSpace(value)

	return value, value != ""
}

func NormalizeKind(value string) string {
	return strings.TrimSpace(strings.ToLower(value))
}

func KeySize(value int) (int, bool) {
	return value, value != 0
}

func f(name string, kind string, size int) error {
	name, ok := ParseName(name)
	if !ok {
		return errBad
	}

	kind = NormalizeKind(kind)
	if kind == "" {
		return errBad
	}

	sizeKey, ok := KeySize(size)
	if !ok {
		return errBad
	}

	println(name, kind, sizeKey)

	return nil
}

var errBad error
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `input guard returns are interleaved`) {
		t.Fatalf(
			"unexpected scattered guard finding for exported validation prep, got:\n%s",
			joined,
		)
	}
}
