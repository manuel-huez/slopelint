package outline

import (
	"fmt"
	"reflect"
	"testing"
)

func TestFunctionSizeCountsSwitchesAndFindsMovableClosures(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		src        string
		statements int
		movable    []StatementBlock
	}{
		{
			name: "table.ts",
			src: `export function label(kind: number): string {
  switch (kind) {
    case 1:
      return 'one'
    case 2:
      return 'two'
    default:
      return 'many'
  }
}
`,
			// One switch, three cases, the longest case one statement.
			statements: 5,
		},
		{
			name: "gaze.ts",
			src: `export function track(target: Element) {
  let release = 0
  const pick = (node: Element | null) => {
    if (!node) return null
    return node.closest('a')
  }
  const look = () => {
    release = 1
  }
  look()
  return pick(target)
}
`,
			statements: 9,
			movable:    []StatementBlock{{Line: 3, Statements: 3}},
		},
		{
			name: "gaze.py",
			src: `def track(target):
    release = 0

    def pick(node):
        if node is None:
            return None
        return node.parent

    def look():
        return release

    return pick(target), look()
`,
			statements: 8,
			movable:    []StatementBlock{{Line: 4, Statements: 3}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			file, err := Parse(test.name, []byte(test.src))
			if err != nil {
				t.Fatal(err)
			}

			fn := file.Funcs[0]
			if fn.Statements != test.statements || !reflect.DeepEqual(fn.Movable, test.movable) {
				t.Fatalf("%s: statements = %d, movable = %v; want %d, %v",
					fn.Name, fn.Statements, fn.Movable, test.statements, test.movable)
			}
		})
	}
}

func TestParseRecordsSplitHelpers(t *testing.T) {
	t.Parallel()

	file, err := Parse("app.ts", []byte(`const tiny = (a) => a + 1;
function step(a, b) { return tiny(a) + b; }
function shared(a) { return a; }
export function run(x, y) {
  const z = shared(x)
  return step(x, z) + shared(y)
}
`))
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, helper := range file.SplitHelpers {
		got = append(
			got,
			fmt.Sprintf("%d %s passes %d", helper.Pos.Line, helper.CallerName, helper.Passed),
		)
	}

	// tiny is called by step and step by run; shared has two calls.
	want := []string{"1 step passes 1", "2 run passes 2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("split helpers = %q, want %q", got, want)
	}
}
