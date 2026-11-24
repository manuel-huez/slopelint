package lint

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectsFieldLeakAndAllowsFieldHidingMethod(t *testing.T) {
	t.Parallel()

	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "queue.go"), `package sample

type ring struct {
	items []int
	head  int
	limit int
}

func NewRing(limit int) *ring {
	r := &ring{limit: limit}
	r.items = make([]int, 0, limit)
	return r
}

func (r *ring) full() bool {
	return len(r.items)-r.head >= r.limit
}

func (r *ring) rawItems() []int {
	return r.items
}

func (r *ring) setHead(head int) {
	r.head = head
}

func admit(r *ring, v int) {
	r.items = append(r.items, v)
	r.setHead(1)
}

func inspect(r *ring) int {
	return r.rawItems()[0]
}
`)

	// A data struct with no kept unexported fields (including exported-only DTOs) must not trigger field_leak.
	writeFile(t, filepath.Join(tmp, "dto.go"), `package sample

type dto struct {
	a int
	b int
}

func (d *dto) sum() int { return d.a + d.b }

func mutateDTO(d *dto) {
	d.a = 1
	d.b = 2
}

type ExportedDTO struct {
	Items []int
	Limit int
}

func (e *ExportedDTO) over() bool { return len(e.Items) > e.Limit }

func mutateExportedDTO(e *ExportedDTO) {
	e.Items = append(e.Items, 1)
}

// Exported fields on a hybrid struct with private state are public API by design.
type Hybrid struct {
	Header map[string]string
	secret int
}

func (h *Hybrid) next() int {
	h.secret++
	return h.secret
}

func UseHybrid(h *Hybrid) {
	h.Header["k"] = "v"
}

// Local initialization before first method use is construction, not a leak;
// mutating a field after a method call still leaks.
type builderTarget struct {
	items []int
	limit int
}

func (b *builderTarget) count() int { return len(b.items) + b.limit }

func BuildLocally() int {
	b := new(builderTarget)
	b.items = append(b.items, 1)
	return b.count()
}

// Reading exported sub-fields on a shared context pointer is allowed.
type SharedCtx struct {
	Tags map[string]string
}

type envHolder struct {
	ctx   *SharedCtx
	cache map[string]string
}

func (e *envHolder) lookup(k string) string { return e.cache[k] }

type consumer struct{ *envHolder }

func (c consumer) ReadTag(k string) string { return c.ctx.Tags[k] }

// A buffer struct whose only untouched unexported field is an anonymous embed
// used via promoted fields is not a guarded struct.
type embeddedCols struct {
	codes []int
}

type columnBatch struct {
	embeddedCols
	values []int
}

func (b *columnBatch) reset() {
	b.codes = b.codes[:0]
	b.values = b.values[:0]
}

func UseColumnBatch(b *columnBatch) int {
	b.reset()
	return b.codes[0] + b.values[0]
}
`)

	// An unexported method forwarding to a receiver field's method must not trigger trivial_wrapper.
	writeFile(t, filepath.Join(tmp, "encapsulated.go"), `package sample

type store struct {
	items []int
}

func (s *store) push(v int) {
	s.items = append(s.items, v)
}

type service struct {
	store store
	limit int
}

func (s *service) add(v int) {
	s.store.push(v)
}

func (s *service) over() bool {
	return len(s.store.items) > s.limit
}

func runService(s *service) {
	s.add(1)
	s.add(2)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	for _, want := range []string{
		`field "ring.items" is worked on outside ring in 2 places`,
		`field "ring.head" is worked on outside ring in 1 place (admit via setHead)`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected %q, got:\n%s", want, joined)
		}
	}

	for _, unwanted := range []string{
		"NewRing",
		`field "dto.`,
		`field "ExportedDTO.`,
		`field "Hybrid.Header"`,
		`field "builderTarget.items"`,
		`field "envHolder.ctx"`,
		`field "columnBatch.values"`,
		`private helper "add" only forwards`,
	} {
		if strings.Contains(joined, unwanted) {
			t.Fatalf("unexpected %q in findings:\n%s", unwanted, joined)
		}
	}
}

func TestDetectsTypeTangleOnlyAboveSevenTypes(t *testing.T) {
	t.Parallel()

	makeRing := func(count int) string {
		var b strings.Builder

		b.WriteString("package sample\n\n")

		for i := range count {
			next := (i + 1) % count
			fmt.Fprintf(&b, "type t%d struct { next *t%d }\n", i, next)
		}

		// Free functions taking *t0 first (such as entrypoints or helpers) must not create false cycles.
		for i := range 8 {
			fmt.Fprintf(&b, "type consumer%d struct { root *t0 }\n", i)
			fmt.Fprintf(
				&b,
				"func helper%d(x *t0) consumer%d { return consumer%d{root: x} }\n",
				i,
				i,
				i,
			)
		}

		return b.String()
	}

	small := newTestModule(t)
	writeFile(t, filepath.Join(small, "ring.go"), makeRing(7))

	if hasIssueKind(lintInDir(t, small), "type_tangle") {
		t.Fatalf("unexpected type_tangle for 7-type cycle")
	}

	large := newTestModule(t)
	writeFile(t, filepath.Join(large, "ring.go"), makeRing(8))

	joined := joinMessages(lintInDir(t, large))
	if !strings.Contains(joined, "8 types form a dependency cycle:") {
		t.Fatalf("expected type_tangle for 8-type cycle, got:\n%s", joined)
	}
}

func TestDetectsScriptModuleCycles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(
		t,
		filepath.Join(dir, "web", "a.ts"),
		"import { b } from './b.js'\nexport const a = () => b()\n",
	)
	writeFile(
		t,
		filepath.Join(dir, "web", "b.ts"),
		"import { a } from './a'\nexport const b = () => a()\n",
	)
	writeFile(
		t,
		filepath.Join(dir, "web", "ok.ts"),
		"import { a } from './a'\nexport const ok = () => a()\n",
	)
	// TypeScript import type back-edge must not trigger a cycle.
	writeFile(
		t,
		filepath.Join(dir, "web", "ta.ts"),
		"import type { TB } from './tb'\nexport type TA = { b?: TB }\nexport const ta = 1\n",
	)
	writeFile(
		t,
		filepath.Join(dir, "web", "tb.ts"),
		"import { ta } from './ta'\nexport type TB = { a: number }\nexport const tb = () => ta\n",
	)
	writeFile(t, filepath.Join(dir, "py", "x.py"), "from .y import y\n\ndef x():\n    return y()\n")
	writeFile(t, filepath.Join(dir, "py", "y.py"), "from .x import x\n\ndef y():\n    return x()\n")
	// TYPE_CHECKING-only back-edge must not trigger a cycle.
	writeFile(
		t,
		filepath.Join(dir, "py", "p.py"),
		"from typing import TYPE_CHECKING\nif TYPE_CHECKING:\n    from .q import q\n\ndef p():\n    return 1\n",
	)
	writeFile(t, filepath.Join(dir, "py", "q.py"), "from .p import p\n\ndef q():\n    return p()\n")
	initTestGitRepository(t, dir)

	issues, err := LintRepository(t.Context(), []string{allPackagesPattern}, dir, Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}

	joined := joinMessages(issues)
	for _, want := range []string{
		"2 modules form an import cycle: web/a.ts -> web/b.ts -> web/a.ts",
		"2 modules form an import cycle: py/x.py -> py/y.py -> py/x.py",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected %q, got:\n%s", want, joined)
		}
	}

	for _, unwanted := range []string{"py/p.py", "web/ta.ts"} {
		if strings.Contains(joined, unwanted) {
			t.Fatalf("unexpected module_cycle for type-only import %q:\n%s", unwanted, joined)
		}
	}
}

func TestDetectsWideDirectoryExcludingTests(t *testing.T) {
	t.Parallel()

	dir := newTestModule(t)
	for i := range 25 {
		writeFile(
			t,
			filepath.Join(dir, "pkg", fmt.Sprintf("f%02d.go", i)),
			fmt.Sprintf("package pkg\n\nfunc F%02d() int { return %d }\n", i, i),
		)
		writeFile(
			t,
			filepath.Join(dir, "pkg", fmt.Sprintf("f%02d_test.go", i)),
			fmt.Sprintf(
				"package pkg\n\nimport \"testing\"\n\nfunc TestF%02d(t *testing.T) { t.Parallel(); if F%02d() != %d { t.Fatal() } }\n",
				i,
				i,
				i,
			),
		)
	}

	issues, err := LintRepository(t.Context(), []string{allPackagesPattern}, dir, Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if hasIssueKind(issues, wideDirectoryRule) {
		t.Fatalf(
			"unexpected wide_directory at 25 prod files (+ 25 tests):\n%s",
			joinMessages(issues),
		)
	}

	writeFile(
		t,
		filepath.Join(dir, "pkg", "f25.go"),
		"package pkg\n\nfunc F25() int { return 25 }\n",
	)

	issues, err = LintRepository(t.Context(), []string{allPackagesPattern}, dir, Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}

	joined := joinMessages(issues)
	if !strings.Contains(joined, `directory "pkg" has 26 production entries (26 files; limit 25)`) {
		t.Fatalf("expected wide_directory at 26 prod files, got:\n%s", joined)
	}
}
