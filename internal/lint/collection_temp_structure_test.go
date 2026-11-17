package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectsRedundantTempAndLenGuards(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, source, want, failure, kind, failure2 string
	}{
		{
			name: "DetectsSingleUseTempAlias",
			source: `package sample

type Req struct { Name string }

func f(req Req) {
	name := req.Name
	if name == "" { println("bad") }
}
`,
			want:     `local "name" only renames cheap expression "req.Name" for one use; inline expression`,
			failure:  "expected temp alias finding, got:\n%s",
			kind:     "temp_alias",
			failure2: "expected temp_alias kind, got %#v",
		},
		{
			name: "DetectsRedundantAppendLenGuard",
			source: `package sample

func f(dst []int, src []int) []int {
	if len(src) > 0 {
		dst = append(dst, src...)
	}

	return dst
}
`,
			want:     `len guard before append(src...) is redundant; append no-ops for empty variadic slices`,
			failure:  "expected redundant append guard finding, got:\n%s",
			kind:     "append_ceremony",
			failure2: "expected append_ceremony kind, got %#v",
		},
		{
			name: "DetectsRedundantRangeLenGuard",
			source: `package sample

func f(items []string) {
	if len(items) > 0 {
		for _, item := range items {
			println(item)
		}
	}
}
`,
			want:     `len guard before range over items is redundant; range has no iterations when guarded value is empty`,
			failure:  "expected redundant range len guard finding, got:\n%s",
			kind:     "range_ceremony",
			failure2: "expected range_ceremony kind, got %#v",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			tmp := newTestModule(t)
			writeFile(t, filepath.Join(tmp, "sample.go"), test.source)

			issues := lintInDir(t, tmp)
			joined := joinMessages(issues)

			if !strings.Contains(
				joined,
				test.want,
			) {
				t.Fatalf(test.failure, joined)
			}

			if !hasIssueKind(issues, test.kind) {
				t.Fatalf(test.failure2, issues)
			}
		})
	}
}

func TestSkipsGuardsThatDoMore(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, source, want, failure string
	}{
		{
			name: "SkipsAppendLenGuardWhenBodyDoesMore",
			source: `package sample

func f(dst []int, src []int) []int {
	if len(src) > 0 {
		println(len(src))
		dst = append(dst, src...)
	}

	return dst
}
`,
			want:    `len guard before append`,
			failure: "unexpected redundant append guard finding, got:\n%s",
		},
		{
			name: "SkipsRangeLenGuardForChannel",
			source: `package sample

func f(items chan int) {
	if len(items) > 0 {
		for item := range items {
			println(item)
		}
	}
}
`,
			want:    `guard before range`,
			failure: "unexpected redundant range guard finding for channel, got:\n%s",
		},
		{
			name: "SkipsEmptyReturnGuardBeforeRangeWhenFunctionReturnsValue",
			source: `package sample

func f(items []string) int {
	if len(items) == 0 {
		return 0
	}

	for _, item := range items {
		println(item)
	}

	return 1
}
`,
			want:    `empty range already ends function`,
			failure: "unexpected empty range return guard finding with result func, got:\n%s",
		},
		{
			name: "InvalidatesCopiedIsPredicateAfterFieldWrite",
			source: `package sample

type coverage struct {
	start int
}

func (c coverage) IsZero() bool {
	return c.start == 0
}

func f(coverage coverage) {
	if coverage.IsZero() {
		return
	}

	next := coverage
	next.start = 0
	if next.IsZero() {
		println("changed")
	}
}
`,
			want:    `next.IsZero()`,
			failure: "unexpected stale predicate finding after field write, got:\n%s",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			tmp := newTestModule(t)
			writeFile(t, filepath.Join(tmp, "sample.go"), test.source)

			issues := lintInDir(t, tmp)
			joined := joinMessages(issues)

			if strings.Contains(joined, test.want) {
				t.Fatalf(test.failure, joined)
			}
		})
	}
}

type detectsRedundantGuardsAndSubexpressionsCase struct {
	name, source, want, failure string
}

func runDetectsRedundantGuardsAndSubexpressions(
	t *testing.T,
	cases []detectsRedundantGuardsAndSubexpressionsCase,
) {
	t.Helper()

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			tmp := newTestModule(t)
			writeFile(t, filepath.Join(tmp, "sample.go"), test.source)

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

func TestDetectsRedundantRangeGuards(t *testing.T) {
	t.Parallel()

	runDetectsRedundantGuardsAndSubexpressions(t, []detectsRedundantGuardsAndSubexpressionsCase{
		{
			name: "DetectsRedundantRangeNilLenGuard",
			source: `package sample

func f(items []string) {
	if items != nil && len(items) != 0 {
		for _, item := range items {
			println(item)
		}
	}
}
`,
			want:    `nil/len guard before range over items is redundant; range has no iterations when guarded value is empty`,
			failure: "expected redundant range nil/len guard finding, got:\n%s",
		},
		{
			name: "DetectsRedundantRangeNilGuard",
			source: `package sample

func f(items map[string]int) {
	if items != nil {
		for key, value := range items {
			println(key, value)
		}
	}
}
`,
			want:    `nil guard before range over items is redundant; range has no iterations when guarded value is empty`,
			failure: "expected redundant range nil guard finding, got:\n%s",
		},
		{
			name: "DetectsEmptyReturnGuardBeforeFinalRange",
			source: `package sample

func f(items []string) {
	if len(items) == 0 {
		return
	}

	for _, item := range items {
		println(item)
	}
}
`,
			want:    `len return guard before final range over items is redundant; empty range already ends function`,
			failure: "expected empty range return guard finding, got:\n%s",
		},
		{
			name: "DetectsNilLenReturnGuardBeforeFinalRange",
			source: `package sample

func f(items []string) {
	if items == nil || len(items) == 0 {
		return
	}

	for _, item := range items {
		println(item)
	}
}
`,
			want:    `nil/len return guard before final range over items is redundant; empty range already ends function`,
			failure: "expected nil/len range return guard finding, got:\n%s",
		},
	})
}

func TestDetectsRepeatedIsPredicateSubexpression(t *testing.T) {
	t.Parallel()

	runDetectsRedundantGuardsAndSubexpressions(t, []detectsRedundantGuardsAndSubexpressionsCase{
		{
			name: "DetectsRepeatedIsPredicateSubexpression",
			source: `package sample

type coverage struct {
	zero bool
}

func (c coverage) IsZero() bool {
	return c.zero
}

func f(coverage coverage, requested coverage) {
	if coverage.IsZero() {
		return
	}

	if coverage.IsZero() || requested.IsZero() {
		println("bad")
	}
}
`,
			want:    `left side of || is always false: "coverage.IsZero()"`,
			failure: "expected repeated Is predicate finding, got:\n%s",
		},
	})
}

func TestDetectsDuplicateAdjacentRangeLoops(t *testing.T) {
	t.Parallel()

	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func use(value string) { println(value) }

func f(values []string) {
	for _, symbol := range values {
		use(symbol)
	}

	for _, name := range values {
		use(name)
	}
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(joined, `behavior block in "f" duplicates block in "f" at sample.go:`) ||
		!strings.Contains(joined, `(effects: calls); extract shared behavior`) {
		t.Fatalf("expected behavior clone finding, got:\n%s", joined)
	}

	if !hasIssueKind(issues, "behavior_clone") {
		t.Fatalf("expected behavior_clone kind, got %#v", issues)
	}
}

func TestSkipsMeaningfulTempAliases(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, source, failure string
	}{
		{
			name: "SkipsTempAliasWhenNameAddsMeaning",
			source: `package sample

type Req struct { Name string }

func f(req Req) {
	customerName := req.Name
	if customerName == "" { println("bad") }
}
`,
			failure: "unexpected temp alias finding for meaningful name, got:\n%s",
		},
		{
			name: "SkipsTempAliasWhenDeclarationHasComment",
			source: `package sample

type Req struct { Name string }

func f(req Req) {
	name := req.Name // keep local for nearby label
	if name == "" { println("bad") }
}
`,
			failure: "unexpected temp alias finding with declaration comment, got:\n%s",
		},
		{
			name: "SkipsTempAliasWhenValueReadMultipleTimes",
			source: `package sample

type Req struct { Name string }

func f(req Req) {
	name := req.Name
	if name == "" { println("bad") }
	println(name)
}
`,
			failure: "unexpected temp alias finding with multiple reads, got:\n%s",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			tmp := newTestModule(t)
			writeFile(t, filepath.Join(tmp, "sample.go"), test.source)

			issues := lintInDir(t, tmp)
			joined := joinMessages(issues)

			if strings.Contains(joined, `only renames cheap expression "req.Name"`) {
				t.Fatalf(test.failure, joined)
			}
		})
	}
}

func TestSkipsTempAliasAcrossSourceMutation(t *testing.T) {
	t.Parallel()

	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type Req struct{ Name string }

func f(req *Req) {
	name := req.Name
	{
		req.Name = "changed"
		println(name)
	}
}
`)

	joined := joinMessages(lintInDir(t, tmp))
	if strings.Contains(joined, `only renames cheap expression "req.Name"`) {
		t.Fatalf("unexpected behavior-changing temp alias finding, got:\n%s", joined)
	}
}

func TestSkipsAdjacentRangeLoopsOverDifferentSources(t *testing.T) {
	t.Parallel()

	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func f(first, second []int) {
	for _, value := range first {
		println(value)
	}
	for _, value := range second {
		println(value)
	}
}
`)

	joined := joinMessages(lintInDir(t, tmp))
	if strings.Contains(joined, `behavior block in "f" duplicates block in "f"`) {
		t.Fatalf("unexpected duplicate-loop finding for different sources, got:\n%s", joined)
	}
}
