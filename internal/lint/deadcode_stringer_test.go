package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

const mainCallsLive = `	_ = lib.Live()`

func TestRepoDeadCodeKeepsFmtStringerMethods(t *testing.T) {
	t.Parallel()

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
		`private method "unusedPrivate" is never used by production code`,
	) {
		t.Fatalf("expected unused private method finding, got:\n%s", joined)
	}
}

type repoDeadCodeDoesNotKeepUnusedFmtStringersCase struct {
	name, mainBody, source, want, failure string
}

func runRepoDeadCodeDoesNotKeepUnusedFmtStringers(
	t *testing.T,
	cases []repoDeadCodeDoesNotKeepUnusedFmtStringersCase,
) {
	t.Helper()

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			tmp := newTestModule(t)
			writeTestMain(t, tmp, test.mainBody)
			writeFile(t, filepath.Join(tmp, "lib", "lib.go"), test.source)

			issues := lintInDir(t, tmp)
			joined := joinMessages(issues)

			if !strings.Contains(
				joined,
				test.want,
			) {
				t.Fatalf(test.failure, joined)
			}
		})
	}
}

func TestRepoDeadCodeDoesNotKeepUnusedFmtStringers(t *testing.T) {
	t.Parallel()

	runRepoDeadCodeDoesNotKeepUnusedFmtStringers(t, []repoDeadCodeDoesNotKeepUnusedFmtStringersCase{
		{
			name:     "RepoDeadCodeDoesNotRootAllStringersThroughAnyFmtArg",
			mainBody: `	_ = lib.Live("x")`,
			source: `package lib

import "fmt"

type digest string

func (digest) String() string {
	return "digest"
}

func Live(value any) string {
	_ = digest("")
	return fmt.Sprintf("%v", value)
}
`,
			want:    `exported method "String" is unreachable from repo entrypoints`,
			failure: "expected unused String method finding, got:\n%s",
		},
		{
			name:     "RepoDeadCodeDoesNotKeepFmtStringerFromDefaultSwitchOverwrite",
			mainBody: `	_ = lib.Live()`,
			source: `package lib

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
`,
			want:    `method "String" is unreachable from repo entrypoints`,
			failure: "expected default switch overwrite to leave Stringer dead, got:\n%s",
		},
		{
			name:     "RepoDeadCodeDoesNotKeepTupleSiblingFmtStringer",
			mainBody: `	_ = lib.Live()`,
			source: `package lib

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
`,
			want:    `method "String" is unreachable from repo entrypoints`,
			failure: "expected tuple sibling String method to stay dead, got:\n%s",
		},
	})
}

type repoDeadCodeKeepsFmtStringerThroughAnyParamsCase struct {
	name, mainBody, source, failure string
}

func runRepoDeadCodeKeepsFmtStringerThroughAnyParams(
	t *testing.T,
	cases []repoDeadCodeKeepsFmtStringerThroughAnyParamsCase,
) {
	t.Helper()

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			tmp := newTestModule(t)
			writeTestMain(t, tmp, test.mainBody)
			writeFile(t, filepath.Join(tmp, "lib", "lib.go"), test.source)

			issues := lintInDir(t, tmp)
			joined := joinMessages(issues)

			if strings.Contains(joined, `method "String"`) {
				t.Fatalf(test.failure, joined)
			}
		})
	}
}

func TestRepoDeadCodeKeepsFmtStringerThroughAnyParams(t *testing.T) {
	t.Parallel()

	runRepoDeadCodeKeepsFmtStringerThroughAnyParams(
		t,
		[]repoDeadCodeKeepsFmtStringerThroughAnyParamsCase{
			{
				name:     "RepoDeadCodeKeepsFmtStringerPassedThroughAnyParam",
				mainBody: `	_ = lib.Live(lib.Digest("x"))`,
				source: `package lib

import "fmt"

type Digest string

func (Digest) String() string {
	return "digest"
}

func Live(value any) string {
	return fmt.Sprintf("%v", value)
}
`,
				failure: "fmt Stringer method passed through any reported dead, got:\n%s",
			},
			{
				name:     "RepoDeadCodeKeepsFmtStringerPassedThroughNestedAnyParam",
				mainBody: `	_ = lib.Wrap(lib.Digest("x"))`,
				source: `package lib

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
`,
				failure: "fmt nested any Stringer method reported dead, got:\n%s",
			},
			{
				name:     "RepoDeadCodeKeepsFmtStringerPassedThroughVariadicAnyParam",
				mainBody: `	_ = lib.Log(lib.Digest("x"))`,
				source: `package lib

import "fmt"

type Digest string

func (Digest) String() string {
	return "digest"
}

func Log(values ...any) string {
	return fmt.Sprint(values...)
}
`,
				failure: "fmt variadic any Stringer method reported dead, got:\n%s",
			},
			{
				name:     "RepoDeadCodeKeepsFmtStringerThroughAnyConversion",
				mainBody: mainCallsLive,
				source: `package lib

import "fmt"

type Digest string

func (Digest) String() string {
	return "digest"
}

func Live() string {
	return fmt.Sprint(any(Digest("x")))
}
`,
				failure: "fmt any conversion Stringer method reported dead, got:\n%s",
			},
		},
	)
}

func TestRepoDeadCodeKeepsFmtStringerThroughAnyLocals(t *testing.T) {
	t.Parallel()

	runRepoDeadCodeKeepsFmtStringerThroughAnyParams(
		t,
		[]repoDeadCodeKeepsFmtStringerThroughAnyParamsCase{
			{
				name:     "RepoDeadCodeKeepsFmtStringerThroughAnyLocal",
				mainBody: mainCallsLive,
				source: `package lib

import "fmt"

type Digest string

func (Digest) String() string {
	return "digest"
}

func Live() string {
	var value any = Digest("x")

	return fmt.Sprint(value)
}
`,
				failure: "fmt any local Stringer method reported dead, got:\n%s",
			},
			{
				name: "RepoDeadCodeKeepsFmtStringerForwardedThroughAnySlice",
				mainBody: `	_ = lib.LogLiteral(lib.Digest("x"))
	_ = lib.LogAppend(lib.Digest("x"))`,
				source: `package lib

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
`,
				failure: "fmt any slice forwarded Stringer method reported dead, got:\n%s",
			},
		},
	)
}

type repoDeadCodeKeepsFmtStringerThroughLocalsAndWrappersCase struct {
	name, mainBody, source, failure string
}

func runRepoDeadCodeKeepsFmtStringerThroughLocalsAndWrappers(
	t *testing.T,
	cases []repoDeadCodeKeepsFmtStringerThroughLocalsAndWrappersCase,
) {
	t.Helper()

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			tmp := newTestModule(t)
			writeTestMain(t, tmp, test.mainBody)
			writeFile(t, filepath.Join(tmp, "lib", "lib.go"), test.source)

			issues := lintInDir(t, tmp)
			joined := joinMessages(issues)

			if strings.Contains(joined, `method "String"`) {
				t.Fatalf(test.failure, joined)
			}
		})
	}
}

func TestRepoDeadCodeKeepsFmtStringerThroughSliceLocals(t *testing.T) {
	t.Parallel()

	runRepoDeadCodeKeepsFmtStringerThroughLocalsAndWrappers(
		t,
		[]repoDeadCodeKeepsFmtStringerThroughLocalsAndWrappersCase{
			{
				name:     "RepoDeadCodeKeepsFmtStringerThroughAnySliceLocal",
				mainBody: mainCallsLive,
				source: `package lib

import "fmt"

type Digest string

func (Digest) String() string {
	return "digest"
}

func Live() string {
	values := []any{Digest("x")}

	return fmt.Sprint(values...)
}
`,
				failure: "fmt any slice local Stringer method reported dead, got:\n%s",
			},
			{
				name:     "RepoDeadCodeKeepsFmtStringerThroughKeyedAnySliceLocal",
				mainBody: mainCallsLive,
				source: `package lib

import "fmt"

type Digest string

func (Digest) String() string {
	return "digest"
}

func Live() string {
	values := []any{0: Digest("x")}

	return fmt.Sprint(values...)
}
`,
				failure: "fmt keyed any slice local Stringer method reported dead, got:\n%s",
			},
			{
				name:     "RepoDeadCodeKeepsFmtStringerForwardedAcrossBranch",
				mainBody: `	_ = lib.Log(lib.Digest("x"), false)`,
				source: `package lib

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
`,
				failure: "fmt branch forwarded Stringer method reported dead, got:\n%s",
			},
		},
	)
}

func TestRepoDeadCodeKeepsFmtStringerPassedToWrappers(t *testing.T) {
	t.Parallel()

	runRepoDeadCodeKeepsFmtStringerThroughLocalsAndWrappers(
		t,
		[]repoDeadCodeKeepsFmtStringerThroughLocalsAndWrappersCase{
			{
				name:     "RepoDeadCodeKeepsFmtStringerLocalAnyPassedToWrapper",
				mainBody: mainCallsLive,
				source: `package lib

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
`,
				failure: "fmt wrapper local any Stringer method reported dead, got:\n%s",
			},
			{
				name:     "RepoDeadCodeKeepsFmtStringerAnySlicePassedToWrapper",
				mainBody: mainCallsLive,
				source: `package lib

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
`,
				failure: "fmt wrapper any slice Stringer method reported dead, got:\n%s",
			},
			{
				name:     "RepoDeadCodeKeepsFmtStringerKeyedAnySlicePassedToWrapper",
				mainBody: mainCallsLive,
				source: `package lib

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
`,
				failure: "fmt keyed wrapper any slice Stringer method reported dead, got:\n%s",
			},
		},
	)
}

func TestRepoDeadCodeKeepsFmtStringerThroughWrapperSlicesAndSwitch(t *testing.T) {
	t.Parallel()

	runRepoDeadCodeKeepsFmtStringerThroughLocalsAndWrappers(
		t,
		[]repoDeadCodeKeepsFmtStringerThroughLocalsAndWrappersCase{
			{
				name:     "RepoDeadCodeKeepsFmtStringerNonVariadicAnySlicePassedToWrapper",
				mainBody: mainCallsLive,
				source: `package lib

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
`,
				failure: "fmt non-variadic any slice wrapper Stringer method reported dead, got:\n%s",
			},
			{
				name:     "RepoDeadCodeKeepsFmtStringerThroughSwitch",
				mainBody: `	_ = lib.Live(1)`,
				source: `package lib

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
`,
				failure: "fmt switch Stringer method reported dead, got:\n%s",
			},
		},
	)
}

func TestRepoDeadCodeKeepsFmtStringerThroughTupleForwarder(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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
