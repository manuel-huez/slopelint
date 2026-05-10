package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectsSingleUseTempAlias(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type Req struct { Name string }

func f(req Req) {
	name := req.Name
	if name == "" { println("bad") }
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`local "name" only renames cheap expression "req.Name" for one use; inline expression`,
	) {
		t.Fatalf("expected temp alias finding, got:\n%s", joined)
	}

	if !hasIssueKind(issues, "temp_alias") {
		t.Fatalf("expected temp_alias kind, got %#v", issues)
	}
}

func TestDetectsRedundantAppendLenGuard(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func f(dst []int, src []int) []int {
	if len(src) > 0 {
		dst = append(dst, src...)
	}

	return dst
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`len guard before append(src...) is redundant; append no-ops for empty variadic slices`,
	) {
		t.Fatalf("expected redundant append guard finding, got:\n%s", joined)
	}

	if !hasIssueKind(issues, "append_ceremony") {
		t.Fatalf("expected append_ceremony kind, got %#v", issues)
	}
}

func TestSkipsAppendLenGuardWhenBodyDoesMore(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func f(dst []int, src []int) []int {
	if len(src) > 0 {
		println(len(src))
		dst = append(dst, src...)
	}

	return dst
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `len guard before append`) {
		t.Fatalf("unexpected redundant append guard finding, got:\n%s", joined)
	}
}

func TestDetectsRepeatedIsPredicateSubexpression(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

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
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`left side of || is always false: "coverage.IsZero()"`,
	) {
		t.Fatalf("expected repeated Is predicate finding, got:\n%s", joined)
	}
}

func TestInvalidatesCopiedIsPredicateAfterFieldWrite(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

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
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `next.IsZero()`) {
		t.Fatalf("unexpected stale predicate finding after field write, got:\n%s", joined)
	}
}

func TestDetectsDuplicateAdjacentRangeLoops(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func use(value string) {}

func f(first []string, second []string, third []string) {
	for _, symbol := range first {
		use(symbol)
	}

	for _, name := range second {
		use(name)
	}

	for _, value := range third {
		use(value)
	}
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`adjacent range loop repeats previous loop body; merge ranges or collapse shared input list`,
	) {
		t.Fatalf("expected duplicate range loop finding, got:\n%s", joined)
	}

	if !hasIssueKind(issues, "loop_ceremony") {
		t.Fatalf("expected loop_ceremony kind, got %#v", issues)
	}
}

func TestSkipsTempAliasWhenNameAddsMeaning(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type Req struct { Name string }

func f(req Req) {
	customerName := req.Name
	if customerName == "" { println("bad") }
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `only renames cheap expression "req.Name"`) {
		t.Fatalf("unexpected temp alias finding for meaningful name, got:\n%s", joined)
	}
}

func TestSkipsTempAliasWhenDeclarationHasComment(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type Req struct { Name string }

func f(req Req) {
	name := req.Name // keep local for nearby label
	if name == "" { println("bad") }
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `only renames cheap expression "req.Name"`) {
		t.Fatalf("unexpected temp alias finding with declaration comment, got:\n%s", joined)
	}
}

func TestSkipsTempAliasWhenValueReadMultipleTimes(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type Req struct { Name string }

func f(req Req) {
	name := req.Name
	if name == "" { println("bad") }
	println(name)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `only renames cheap expression "req.Name"`) {
		t.Fatalf("unexpected temp alias finding with multiple reads, got:\n%s", joined)
	}
}
