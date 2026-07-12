package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRepoDeadCodeKeepsFmtStringerThroughForwardedPackageAny(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_ = lib.Live()`)
	writeFile(t, filepath.Join(tmp, "model", "model.go"), `package model

type Digest string

func (Digest) String() string {
	return "digest"
}

var Value any = Wrapped
var Wrapped any = Digest("x")
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import (
	"fmt"

	"example.com/sample/model"
)

func Live() string {
	return fmt.Sprint(model.Value)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `method "String"`) {
		t.Fatalf("fmt package any Stringer method reported dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsFmtStringerThroughPackageAnyInit(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_ = lib.Live()`)
	writeFile(t, filepath.Join(tmp, "model", "model.go"), `package model

type Digest string

func (Digest) String() string {
	return "digest"
}

var Value any

func init() {
	Value = Digest("x")
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import (
	"fmt"

	"example.com/sample/model"
)

func Live() string {
	return fmt.Sprint(model.Value)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `method "String"`) {
		t.Fatalf("fmt package init any Stringer method reported dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsFmtStringerFromLivePackageMutator(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	lib.Set()
	_ = lib.Live()`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "fmt"

type Digest string

func (Digest) String() string {
	return "digest"
}

var Value any

func Set() {
	Value = Digest("x")
}

func Live() string {
	return fmt.Sprint(Value)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `method "String"`) {
		t.Fatalf("fmt live package mutator Stringer method reported dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeDoesNotKeepFmtStringerFromDeadPackageMutator(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_ = lib.Live()`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "fmt"

type liveDigest string

func (liveDigest) String() string {
	return "live"
}

type deadDigest string

func (deadDigest) String() string {
	return "dead"
}

var Value any = liveDigest("x")

func deadSet() {
	Value = deadDigest("x")
}

func Live() string {
	_ = deadDigest("")
	return fmt.Sprint(Value)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`method "String" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf("expected dead package mutator String method to stay dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeDoesNotKeepFmtStringerFromDeadAnyCaller(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_ = lib.Live()`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "fmt"

type liveDigest string

func (liveDigest) String() string {
	return "live"
}

type deadDigest string

func (deadDigest) String() string {
	return "dead"
}

func Log(value any) string {
	return fmt.Sprint(value)
}

func Live() string {
	return Log(liveDigest("x"))
}

func dead() string {
	return Log(deadDigest("x"))
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`private function "dead" is never used by production code; remove it`,
	) {
		t.Fatalf("expected dead fmt caller subgraph root finding, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsFmtStringersWithSamePackageQualifiedName(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_ = lib.Live()`)
	writeFile(t, filepath.Join(tmp, "a", "model", "model.go"), `package model

type Digest string

func (Digest) String() string {
	return "a"
}

func New() Digest {
	return Digest("a")
}
`)
	writeFile(t, filepath.Join(tmp, "b", "model", "model.go"), `package model

type Digest string

func (Digest) String() string {
	return "b"
}

func New() Digest {
	return Digest("b")
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import (
	"fmt"

	firstmodel "example.com/sample/a/model"
	secondmodel "example.com/sample/b/model"
)

func Live() string {
	values := []any{firstmodel.New(), secondmodel.New()}

	return fmt.Sprint(values...)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `method "String"`) {
		t.Fatalf(
			"fmt Stringer methods with same package-qualified names reported dead, got:\n%s",
			joined,
		)
	}
}

func TestRepoDeadCodeDistinguishesFmtStringerStructFieldOwners(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_ = lib.Live()`)

	lib := `package lib

import "fmt"

type A string

func (A) String() string {
	return "a"
}

type B string

func (B) String() string {
	return "b"
}

type Box struct {
	Value any
}

func Live() string {
	var first Box
	var second Box

	first.Value = A("x")
	second.Value = B("x")

	return fmt.Sprint(first.Value)
}
`
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), lib)

	issues := lintInDir(t, tmp)
	line := issueLineForMessage(t, issues, filepath.Join("lib", "lib.go"), `method "String"`)

	expected := sourceLine(t, lib, "func (B) String() string")
	if line != expected {
		t.Fatalf(
			"expected dead String method on B line %d, got line %d:\n%s",
			expected,
			line,
			joinMessages(issues),
		)
	}
}

func TestRepoDeadCodeKeepsFmtStringerThroughLocalParallelAssignment(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_ = lib.Live()`)

	lib := `package lib

import "fmt"

type A string

func (A) String() string {
	return "a"
}

type B string

func (B) String() string {
	return "b"
}

func Live() string {
	var left any = A("x")
	var right any = B("x")

	left, right = right, left

	return fmt.Sprint(right)
}
`
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), lib)

	issues := lintInDir(t, tmp)
	line := issueLineForMessage(t, issues, filepath.Join("lib", "lib.go"), `method "String"`)

	expected := sourceLine(t, lib, "func (B) String() string")
	if line != expected {
		t.Fatalf(
			"expected dead String method on B line %d, got line %d:\n%s",
			expected,
			line,
			joinMessages(issues),
		)
	}
}

func TestRepoDeadCodeKeepsFmtStringerThroughForwarderParallelAssignment(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_ = lib.Live()`)

	lib := `package lib

import "fmt"

type A string

func (A) String() string {
	return "a"
}

type B string

func (B) String() string {
	return "b"
}

func Log(left any, right any) string {
	left, right = right, left

	return fmt.Sprint(right)
}

func Live() string {
	return Log(A("x"), B("x"))
}
`
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), lib)

	issues := lintInDir(t, tmp)
	line := issueLineForMessage(t, issues, filepath.Join("lib", "lib.go"), `method "String"`)

	expected := sourceLine(t, lib, "func (B) String() string")
	if line != expected {
		t.Fatalf(
			"expected dead String method on B line %d, got line %d:\n%s",
			expected,
			line,
			joinMessages(issues),
		)
	}
}

func TestRepoDeadCodeKeepsFmtStringerFromCrossPackageMutator(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	lib.Set()
	_ = lib.Live()`)
	writeFile(t, filepath.Join(tmp, "model", "model.go"), `package model

type Digest string

func (Digest) String() string {
	return "digest"
}

var Value any
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import (
	"fmt"

	"example.com/sample/model"
)

func Set() {
	model.Value = model.Digest("x")
}

func Live() string {
	return fmt.Sprint(model.Value)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `method "String"`) {
		t.Fatalf("cross-package package var mutator String method reported dead, got:\n%s", joined)
	}
}

func sourceLine(t *testing.T, source string, needle string) int {
	t.Helper()

	index := strings.Index(source, needle)
	if index < 0 {
		t.Fatalf("source missing %q", needle)
	}

	return strings.Count(source[:index], "\n") + 1
}

func issueLineForMessage(
	t *testing.T,
	issues []Issue,
	pathSuffix string,
	message string,
) int {
	t.Helper()

	lines := make([]int, 0, 1)

	for _, issue := range issues {
		if !strings.Contains(issue.Message, message) {
			continue
		}

		position := issue.fset.Position(issue.Pos)
		if strings.HasSuffix(position.Filename, pathSuffix) {
			lines = append(lines, position.Line)
		}
	}

	if len(lines) != 1 {
		t.Fatalf(
			"expected one %q issue in %s, got lines %v:\n%s",
			message,
			pathSuffix,
			lines,
			joinMessages(issues),
		)
	}

	return lines[0]
}
