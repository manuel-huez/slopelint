package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRepoDeadCodeKeepsFmtStringerMethods(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	lib.Live()`)
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

func TestRepoDeadCodeDoesNotRootAllStringersThroughAnyFmtArg(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_ = lib.Live("x")`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "fmt"

type digest string

func (digest) String() string {
	return "digest"
}

func Live(value any) string {
	_ = digest("")
	return fmt.Sprintf("%v", value)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`exported method "String" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf("expected unused String method finding, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsFmtStringerPassedThroughAnyParam(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_ = lib.Live(lib.Digest("x"))`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "fmt"

type Digest string

func (Digest) String() string {
	return "digest"
}

func Live(value any) string {
	return fmt.Sprintf("%v", value)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `method "String"`) {
		t.Fatalf("fmt Stringer method passed through any reported dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsFmtStringerPassedThroughNestedAnyParam(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_ = lib.Wrap(lib.Digest("x"))`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "fmt"

type Digest string

func (Digest) String() string {
	return "digest"
}

func Log(value any) string {
	return fmt.Sprint(value)
}

func Wrap(value any) string {
	return Log(value)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `method "String"`) {
		t.Fatalf("fmt nested any Stringer method reported dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsFmtStringerPassedThroughVariadicAnyParam(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_ = lib.Log(lib.Digest("x"))`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "fmt"

type Digest string

func (Digest) String() string {
	return "digest"
}

func Log(values ...any) string {
	return fmt.Sprint(values...)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `method "String"`) {
		t.Fatalf("fmt variadic any Stringer method reported dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsFmtStringerThroughAnyConversion(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_ = lib.Live()`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "fmt"

type Digest string

func (Digest) String() string {
	return "digest"
}

func Live() string {
	return fmt.Sprint(any(Digest("x")))
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `method "String"`) {
		t.Fatalf("fmt any conversion Stringer method reported dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsFmtStringerThroughAnyLocal(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_ = lib.Live()`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "fmt"

type Digest string

func (Digest) String() string {
	return "digest"
}

func Live() string {
	var value any = Digest("x")

	return fmt.Sprint(value)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `method "String"`) {
		t.Fatalf("fmt any local Stringer method reported dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsFmtStringerForwardedThroughAnySlice(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_ = lib.LogLiteral(lib.Digest("x"))
	_ = lib.LogAppend(lib.Digest("x"))`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "fmt"

type Digest string

func (Digest) String() string {
	return "digest"
}

func LogLiteral(value any) string {
	values := []any{value}

	return fmt.Sprint(values...)
}

func LogAppend(value any) string {
	values := []any{}
	values = append(values, value)

	return fmt.Sprint(values...)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `method "String"`) {
		t.Fatalf("fmt any slice forwarded Stringer method reported dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsFmtStringerThroughAnySliceLocal(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_ = lib.Live()`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "fmt"

type Digest string

func (Digest) String() string {
	return "digest"
}

func Live() string {
	values := []any{Digest("x")}

	return fmt.Sprint(values...)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `method "String"`) {
		t.Fatalf("fmt any slice local Stringer method reported dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsFmtStringerThroughKeyedAnySliceLocal(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_ = lib.Live()`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "fmt"

type Digest string

func (Digest) String() string {
	return "digest"
}

func Live() string {
	values := []any{0: Digest("x")}

	return fmt.Sprint(values...)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `method "String"`) {
		t.Fatalf("fmt keyed any slice local Stringer method reported dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsFmtStringerForwardedAcrossBranch(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_ = lib.Log(lib.Digest("x"), false)`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "fmt"

type Digest string

func (Digest) String() string {
	return "digest"
}

func Log(value any, drop bool) string {
	forwarded := value
	if drop {
		forwarded = nil
	}

	return fmt.Sprint(forwarded)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `method "String"`) {
		t.Fatalf("fmt branch forwarded Stringer method reported dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsFmtStringerLocalAnyPassedToWrapper(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_ = lib.Live()`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "fmt"

type Digest string

func (Digest) String() string {
	return "digest"
}

func Log(value any) string {
	return fmt.Sprint(value)
}

func Live() string {
	var value any = Digest("x")

	return Log(value)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `method "String"`) {
		t.Fatalf("fmt wrapper local any Stringer method reported dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsFmtStringerAnySlicePassedToWrapper(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_ = lib.Live()`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "fmt"

type Digest string

func (Digest) String() string {
	return "digest"
}

func Log(values ...any) string {
	return fmt.Sprint(values...)
}

func Live() string {
	values := []any{Digest("x")}

	return Log(values...)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `method "String"`) {
		t.Fatalf("fmt wrapper any slice Stringer method reported dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsFmtStringerKeyedAnySlicePassedToWrapper(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_ = lib.Live()`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "fmt"

type Digest string

func (Digest) String() string {
	return "digest"
}

func Log(values ...any) string {
	return fmt.Sprint(values...)
}

func Live() string {
	values := []any{0: Digest("x")}

	return Log(values...)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `method "String"`) {
		t.Fatalf("fmt keyed wrapper any slice Stringer method reported dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsFmtStringerNonVariadicAnySlicePassedToWrapper(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_ = lib.Live()`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "fmt"

type Digest string

func (Digest) String() string {
	return "digest"
}

func Log(values []any) string {
	return fmt.Sprint(values...)
}

func Live() string {
	return Log([]any{Digest("x")})
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `method "String"`) {
		t.Fatalf(
			"fmt non-variadic any slice wrapper Stringer method reported dead, got:\n%s",
			joined,
		)
	}
}

func TestRepoDeadCodeKeepsFmtStringerThroughSwitch(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_ = lib.Live(1)`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "fmt"

type Digest string

func (Digest) String() string {
	return "digest"
}

func Live(mode int) string {
	var value any
	switch mode {
	case 1:
		value = Digest("x")
	default:
		value = nil
	}

	return fmt.Sprint(value)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `method "String"`) {
		t.Fatalf("fmt switch Stringer method reported dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeDoesNotKeepFmtStringerFromDefaultSwitchOverwrite(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_ = lib.Live()`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "fmt"

type Digest string

func (Digest) String() string {
	return "digest"
}

func Live() string {
	var value any = Digest("x")
	switch {
	default:
		value = nil
	}

	return fmt.Sprint(value)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`method "String" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf("expected default switch overwrite to leave Stringer dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeDoesNotKeepTupleSiblingFmtStringer(t *testing.T) {
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

func pair() (liveDigest, deadDigest) {
	return "live", "dead"
}

func Live() string {
	var live any
	var dead any
	live, dead = pair()
	_ = dead

	return fmt.Sprint(live)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`method "String" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf("expected tuple sibling String method to stay dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsFmtStringerThroughTupleForwarder(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_ = lib.Live(lib.Digest("x"))`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "fmt"

type Digest string

func (Digest) String() string {
	return "digest"
}

func unpack(value any) (any, bool) {
	return value, true
}

func Live(value any) string {
	next, _ := unpack(value)

	return fmt.Sprint(next)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `method "String"`) {
		t.Fatalf("fmt tuple-forwarded Stringer method reported dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsFmtStringerThroughRange(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_ = lib.Live()`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "fmt"

type Digest string

func (Digest) String() string {
	return "digest"
}

func Live() string {
	var out string
	for _, value := range []any{Digest("x")} {
		out = fmt.Sprint(value)
	}

	return out
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `method "String"`) {
		t.Fatalf("fmt range Stringer method reported dead, got:\n%s", joined)
	}
}
