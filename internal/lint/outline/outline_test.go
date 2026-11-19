package outline

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func summary(file File) []string {
	var out []string

	for _, fn := range file.Funcs {
		name := fn.Name
		if fn.Literal {
			name = "<lit>"
		}

		out = append(
			out,
			fmt.Sprintf(
				"func %s:%d@%s:%d-%d",
				name,
				fn.Params,
				fn.Receiver,
				fn.StartLine,
				fn.EndLine,
			),
		)
	}

	for _, typ := range file.Types {
		out = append(out, "type "+typ.Name)
	}

	for _, comment := range file.Comments {
		mark := ""
		if comment.Suppression {
			mark = "!"
		}

		out = append(out, fmt.Sprintf("comment %d-%d%s", comment.Pos.Line, comment.EndLine, mark))
	}

	for _, decl := range file.Decls {
		out = append(out, fmt.Sprintf("decl %d-%d", decl.StartLine, decl.EndLine))
	}

	for _, test := range file.Tests {
		out = append(
			out,
			fmt.Sprintf("test %d-%d ctx %v", test.StartLine, test.EndLine, test.Context),
		)
	}

	for _, smell := range file.Smells {
		switch smell.Kind {
		case "test_focused":
			out = append(out, fmt.Sprintf("focused %d", smell.Pos.Line))
		case "test_sleep":
			out = append(out, fmt.Sprintf("sleep %d", smell.Pos.Line))
		}
	}

	for _, handler := range file.Handlers {
		out = append(out, fmt.Sprintf(
			"handler %d-%d in %d-%d",
			handler.StartLine,
			handler.EndLine,
			handler.Unit.StartLine,
			handler.Unit.EndLine,
		))
	}

	return out
}

func TestParse(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "app.ts",
			src: `// header

/** Doc. */
export function f(a: number, this: void, ...rest: number[]): void {
  const g = (x) => x // trailing
  run(function () {})
}
// @ts-expect-error upstream bug
class K {
  m(a, b) {}
  h = () => {}
}
export const e = Effect.fn('e')(function* (a) {})
export default () => {}
const o = { p: () => 1, q() {} }
`,
			want: []string{
				"func f:2@:4-7", "func g:1@:5-5", "func <lit>:0@:6-6",
				"func m:2@K:10-10", "func h:0@K:11-11", "func e:1@:13-13",
				"func default:0@:14-14", "func p:0@:15-15", "func q:0@:15-15",
				"type K",
				"comment 1-1", "comment 3-3", "comment 5-5", "comment 8-8!",
				// Statements without a blank line between join one declaration.
				"decl 3-15",
			},
		},
		{
			name: "view.tsx",
			src:  "/* eslint-disable */\n\nexport const V = () => <div>{/* note */}</div>\n",
			want: []string{"func V:0@:3-3", "comment 1-1!", "comment 3-3", "decl 1-1", "decl 3-3"},
		},
		{
			name: "mod.py",
			src: `"""Module doc."""
import os  # noqa: F401


def f(a, b=1, *args, c, **kw):
    """Doc."""
    def inner(x):
        return x
    return lambda y: y


class K:
    # reason
    @staticmethod
    def s(a, /, b, *, c):
        pass

    def m(self, z):  # type: int
        pass
`,
			want: []string{
				"func f:5@:5-9", "func inner:1@:7-8", "func <lit>:1@:9-9",
				"func s:3@K:15-16", "func m:1@K:18-19",
				"type K",
				"comment 1-1", "comment 2-2!", "comment 6-6", "comment 13-13",
				"decl 2-2", "decl 5-9", "decl 12-19",
			},
		},
		{
			// Handlers that raise again on every path are dropped; the rest are
			// judged in their outermost function with its doc comment.
			name: "errors.ts",
			src: `import a from 'a'
// load reads.
export const load = async () => {
  try { a() } catch (e) { if (x) { throw e } else { throw y } }
  try { a() } catch (e) { if (x) return; throw e }
  p.catch(() => undefined)
  return Effect.catch(() => Effect.succeed(1))
}
try { a() } catch { process.exit(1) }
try { a() } catch { process.exit(0) }
`,
			want: []string{
				"func load:0@:3-8", "func <lit>:0@:6-6", "func <lit>:0@:7-7",
				"comment 2-2", "decl 1-10",
				"handler 5-5 in 2-8", "handler 6-6 in 2-8", "handler 10-10 in 10-10",
			},
		},
		{
			name: "errors.py",
			src: `class Handlers:
    def m(self):
        try:
            a()
        except (ValueError, KeyError):
            raise
        except Exception:
            if x:
                raise
            log()
        with contextlib.suppress(OSError):
            b()
`,
			want: []string{
				"func m:0@Handlers:2-12", "type Handlers", "decl 1-12",
				"handler 7-10 in 2-12", "handler 11-12 in 2-12",
			},
		},
		{
			// Tests carry their suite headers and hooks; skipped tests and
			// tests nested in tests are not tests; zero delays only yield.
			name: "app.test.ts",
			src: `beforeAll(() => {})
describe.only('d', () => {
  beforeEach(() => {})
  it('a', async () => {
    await new Promise((r) => setTimeout(r, 50))
    await sleep(0)
    test('nested', () => {})
  })
  test.skip('b', () => {})
  it.layer(L)('e', (it) => {
    it.effect('c', () => Bun.sleepSync(1_000))
  })
})
`,
			want: []string{
				"func <lit>:0@:1-1", "func <lit>:0@:2-13", "func <lit>:0@:3-3",
				"func <lit>:0@:4-8", "func <lit>:1@:5-5", "func <lit>:0@:7-7",
				"func <lit>:0@:9-9", "func <lit>:1@:10-12", "func <lit>:0@:11-11",
				"decl 1-13",
				"test 4-8 ctx [{1 1} {2 2} {3 3}]", "test 11-11 ctx [{1 1} {2 2} {3 3} {10 10}]",
				"focused 2", "sleep 5", "sleep 11",
			},
		},
		{
			name: "fake.test.ts",
			src:  "vi.useFakeTimers()\ntest('a', async () => { await delay(10) })\n",
			want: []string{"func <lit>:0@:2-2", "decl 1-2", "test 2-2 ctx []"},
		},
		{
			// Patching time.sleep anywhere in the file fakes it in the whole
			// file, but not asyncio.sleep.
			name: "test_mod.py",
			src: `from time import sleep


def test_a():
    sleep(1)


@mock.patch("time.sleep")
class TestB(unittest.TestCase):
    def setUp(self):
        pass

    def test_c(self):
        time.sleep(0.5)
        asyncio.sleep(0)
        asyncio.sleep(0.1)

    def helper(self):
        pass


class Other:
    def test_d(self):
        pass
`,
			want: []string{
				"func test_a:0@:4-5", "func setUp:0@TestB:10-11", "func test_c:0@TestB:13-16",
				"func helper:0@TestB:18-19", "func test_d:0@Other:23-24",
				"type TestB", "type Other",
				"decl 1-1", "decl 4-5", "decl 8-19", "decl 22-24",
				"test 4-5 ctx []", "test 13-16 ctx [{9 9} {10 11}]",
				"sleep 16",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			file, err := Parse(test.name, []byte(test.src))
			if err != nil {
				t.Fatal(err)
			}

			if got := summary(file); !reflect.DeepEqual(got, test.want) {
				t.Fatalf(
					"got\n%s\nwant\n%s",
					strings.Join(got, "\n"),
					strings.Join(test.want, "\n"),
				)
			}
		})
	}
}

func TestParseSkipsGeneratedAndRejectsSyntaxErrors(t *testing.T) {
	t.Parallel()

	file, err := Parse("api.ts", []byte("// Code generated by tool. DO NOT EDIT.\nconst = ;\n"))
	if err != nil || !file.Generated || file.Lines != 2 {
		t.Fatalf("generated file = %+v, %v", file, err)
	}

	if _, err := Parse("bad.py", []byte("def f(:\n    pass\n")); err == nil ||
		!strings.Contains(err.Error(), "syntax error") {
		t.Fatalf("syntax error = %v", err)
	}
}

func TestFileClassification(t *testing.T) {
	t.Parallel()

	type class struct {
		language Language
		test     bool
	}

	for name, want := range map[string]class{
		"src/a.ts":              {TypeScript, false},
		"src/a.test.tsx":        {TypeScript, true},
		"src/__tests__/a.ts":    {TypeScript, true},
		"src/a.d.ts":            {},
		"src/routes.gen.ts":     {},
		"dist/app.min.ts":       {},
		"pkg/test_a.py":         {Python, true},
		"pkg/a_test.py":         {Python, true},
		"tests/helpers.py":      {Python, true},
		"pkg/conftest.py":       {Python, true},
		"pkg/a.py":              {Python, false},
		"cmd/main.go":           {},
		"cmd/main_test.go":      {test: true},
		"web/tests/fixtures.ts": {TypeScript, false},
	} {
		got := class{language: LanguageFor(name)}
		got.test = TestFile(name)

		if got != want {
			t.Errorf("%s = %+v, want %+v", name, got, want)
		}
	}
}

// TestParseBlocks checks what similarity compares: outermost named
// functions, methods, and test calls, without comments, docstrings, a
// leading export, or the first line's indentation; inner functions stay in
// their block and repeated symbols get a suffix.
func TestParseBlocks(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		src  string
		want []string
	}{
		"app.ts": {
			src: "// doc\nexport function f(a) {\n  // note\n  const g = () => a /* x */\n  return g()\n}\n" +
				"class K {\n  m() {\n    return 1\n  }\n}\nclass L {\n  m() {}\n}\nfunction f() {}\n",
			want: []string{
				"f@2 function f(a) {\n  const g = () => a\n  return g()\n}",
				"K.m@8 m() {\n  return 1\n}", "L.m@13 m() {}", "f#2@15 function f() {}",
			},
		},
		"a.test.ts": {
			src:  "describe('d', () => {\n  it('works', () => {\n    const h = () => 1\n  })\n})\n",
			want: []string{"it 'works'@2 it('works', () => {\n  const h = () => 1\n})"},
		},
		"mod.py": {
			src:  "class K:\n    def m(self):\n        \"\"\"Doc.\"\"\"\n        def inner():\n            pass  # why\n        return inner\n",
			want: []string{"K.m@2 def m(self):\n    def inner():\n        pass\n    return inner"},
		},
	} {
		file, err := Parse(name, []byte(test.src))
		if err != nil {
			t.Fatal(err)
		}

		var got []string
		for _, block := range file.Blocks {
			got = append(got, fmt.Sprintf("%s@%d %s", block.Symbol, block.Pos.Line, block.Content))
		}

		if !reflect.DeepEqual(got, test.want) {
			t.Errorf("%s blocks:\n%q\nwant\n%q", name, got, test.want)
		}
	}
}
