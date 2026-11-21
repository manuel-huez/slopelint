package outline

import (
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
