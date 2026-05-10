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
