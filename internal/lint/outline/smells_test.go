package outline

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestParseSmells(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "merge.ts",
			src: `function f(a, b) {
  if (a) { g(); } else { g(); }
  if (a) { const x = 1; g(x); } else { const x = 1; g(x); }
  if (a) { return 1; } else if (b) return 1; else { h(); }
  if (a) g(); else if (b) h(); else h();
  if (a) { g(); } else { g(); /* why */ }
}
function s(v) {
  switch (v) {
    case 1: g(); break;
    case 2: g(); break;
    case 3: g();
    case 4: g();
    case 5:
    default: return 0;
  }
}
`,
			want: []string{
				"2 control_flow_merge if and else branches are identical; preserve condition evaluation and hoist shared body",
				`4 control_flow_merge branch "b" has identical body as previous branch "a"; join the conditions`,
				`11 control_flow_merge switch case "2" has identical body as previous case "1"; merge case lists`,
			},
		},
		{
			name: "merge.py",
			src: `def f(a, b):
    if a:
        g()
    else:
        g()
    if a:
        return 1
    elif b:
        return 1
    elif a and b:
        return 2
    # c needs no work either
    elif c:
        return 2
    if a:
        g()
    else:
        # why
        g()
    if a:
        g("[%s] / %s\n")
    else:
        g("%s / [%s]\n")
`,
			want: []string{
				"2 control_flow_merge if and else branches are identical; preserve condition evaluation and hoist shared body",
				`8 control_flow_merge branch "b" has identical body as previous branch "a"; join the conditions`,
			},
		},
		{
			name: "guards.ts",
			src: `function f(a, b, c) {
  if (a === 1) return null;
  if (!b || c == null) { return null; }
  return null;
}
function g(a) {
  if (a.length > 0) return null;
  return null;
}
function h(a) {
  if (a) return;
  return;
}
function k(a) {
  if (a == 2) return 1;
  if (a) return 2;
  return 2;
}
function t(a) {
  if (a === 1) return "x";
  return "x";
}
function s(a) {
  if (a === "x") return 0;
  if (a === ` + "`${a}`" + `) return 0;
  return 0;
}
function m(a) {
  if (a) return;
  // reason
  if (a < 3) return;
  return;
}
`,
			want: []string{
				`2 control_flow_merge 2 guard return(s) duplicate following return "return null"; drop redundant branch checks`,
				`11 control_flow_merge 1 guard return(s) duplicate following return "return"; drop redundant branch checks`,
				`16 control_flow_merge 1 guard return(s) duplicate following return "return 2"; drop redundant branch checks`,
				`20 control_flow_merge 1 guard return(s) duplicate following return "return \"x\""; drop redundant branch checks`,
			},
		},
		{
			name: "guards.py",
			src: `def f(a, b):
    if a is None or not b:
        return None
    if a == 1:
        return None
    return None


def h(a):
    if a == f"{a}":
        return None
    if a == " is ":
        return None
    return None


def g(a, b):
    if a in b:
        return None
    if a.x:
        return None
    if a > b:
        return None
    return None
`,
			want: []string{
				`2 control_flow_merge 2 guard return(s) duplicate following return "return None"; drop redundant branch checks`,
				`12 control_flow_merge 1 guard return(s) duplicate following return "return None"; drop redundant branch checks`,
			},
		},
		{
			name: "bools.ts",
			src: `function f(a, b) {
  if (a > b) { return true; } else { return false; }
}
function g(a) {
  if (a) return false;
  return true;
}
function h(a) {
  if (a.ok) return true;
  return false;
}
function k(a) {
  if (a) { x.y = true; } else { x.y = false; }
  if (a) { return true; } else { return true; }
  if (a) { f().y = true; } else { f().y = false; }
}
`,
			want: []string{
				`2 boolean_ceremony if returns boolean literals; replace with "return a > b"`,
				`5 boolean_ceremony if returns boolean literals; replace with "return !(a)"`,
				`9 boolean_ceremony if returns boolean literals; replace with "return Boolean(a.ok)"`,
				`13 boolean_ceremony if assigns boolean literals; replace with "x.y = Boolean(a)"`,
				"14 control_flow_merge if and else branches are identical; preserve condition evaluation and hoist shared body",
			},
		},
		{
			name: "bools.py",
			src: `def f(a, b):
    if a is None:
        return True
    else:
        return False


def g(a):
    if a == 1:
        return True
    return False


def h(a):
    if not a:
        return False
    return True


def k(a):
    if a:
        # why
        return True
    return False
`,
			want: []string{
				`2 boolean_ceremony if returns boolean literals; replace with "return a is None"`,
				`9 boolean_ceremony if returns boolean literals; replace with "return bool(a == 1)"`,
				`15 boolean_ceremony if returns boolean literals; replace with "return bool(a)"`,
			},
		},
		{
			name: "pyramid.ts",
			src: `function f(a, b) {
  g();
  if (a) {
    if (b) {
      h();
    }
  }
}
function r(a, b) {
  if (a) {
    if (b) {
      return 1;
    }
  }
  return 2;
}
const c = (a, b) => {
  if (a) { if (b) { h(); } else { g(); } }
};
`,
			want: []string{
				"3 control_flow_pyramid nested if pyramid has 2 levels at function end; invert conditions into guard clauses",
			},
		},
		{
			name: "pyramid.py",
			src: `def f(a, b):
    if a:
        if b:
            h()


def g(a, b):
    if a:
        if b:
            yield 1
`,
			want: []string{
				"2 control_flow_pyramid nested if pyramid has 2 levels at function end; invert conditions into guard clauses",
			},
		},
		{
			name: "alias.tsx",
			src: `function f(user) {
  const name = user.name;
  return format(name);
}
function g(user) {
  const name = user.name;
  save(user, name);
}
function h(user) {
  const name = user.name;
  return () => format(name);
}
function k(user) {
  const items = user.items;
  for (const item of items) { use(item); }
}
function m(user) {
  const label = user.name;
  return format(label);
}
function n(query, done) {
  const isPending = query.isPending;
  if (done) { return isPending; }
}
function o(query, done) {
  const isPending = query.isPending;
  return done && isPending;
}
function p(props) {
  const Icon = props.icon;
  return <Icon />;
}
function q(a) {
  const items = a?.items;
  return items.length;
}
function r(a) {
  const items = a?.items;
  return items?.length;
}
`,
			want: []string{
				`2 temp_alias local "name" only renames cheap expression "user.name" for one use; inline expression`,
				`14 temp_alias local "items" only renames cheap expression "user.items" for one use; inline expression`,
				`38 temp_alias local "items" only renames cheap expression "a?.items" for one use; inline expression`,
			},
		},
		{
			name: "alias.py",
			src: `def f(self):
    items = self.items
    return len(items)


def g(self):
    items = self.items
    return [x for x in items if x in items]
`,
			want: []string{
				`2 temp_alias local "items" only renames cheap expression "self.items" for one use; inline expression`,
			},
		},
		{
			name: "helpers.ts",
			src: `function wrapperFor(a, b) {
  return target.run(a, b);
}
function tiny(a) {
  return a + 1;
}
const twice = (a) => tiny(a) * 2;
function asValue(a) { return tiny(a); }
function mentioned(a) { return a * 3; }
function isName(a): a is string { return names.has(a); }
function pick(a) { return a ? 1 : 2; }
function fallback(a) { return a ?? 2; }
function chained(a) { return a?.b; }
export function useAll(x) {
  [1].map(asValue);
  return wrapperFor(x, 1) + twice(x) + mentioned(x) + "mentioned" + isName(x) + pick(x) + fallback(x) + chained(x);
}
`,
			want: []string{
				`1 trivial_wrapper private helper "wrapperFor" only forwards to "target.run" at one production use; inline or merge names`,
				`1 generic_naming private helper "wrapperFor" has generic name and only forwards; rename or inline`,
				`7 abstraction_overkill private helper "twice" has one production use and a tiny body; inline or give it a stronger role`,
			},
		},
		{
			name: "helpers.py",
			src: `def _forward(a):
    return run(a)


def _tiny(a):
    return a + 1


def use(x):
    return _forward(x) + _tiny(x)
`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			file, err := Parse(test.name, []byte(test.src))
			if err != nil {
				t.Fatal(err)
			}

			var got []string
			for _, smell := range file.Smells {
				got = append(
					got,
					fmt.Sprintf("%d %s %s", smell.Pos.Line, smell.Kind, smell.Message),
				)
			}

			slices.Sort(got)
			slices.Sort(test.want)

			if !slices.Equal(got, test.want) {
				t.Errorf("got:\n%s\nwant:\n%s", lines(got), lines(test.want))
			}
		})
	}
}

func lines(items []string) string {
	return "  " + strings.Join(items, "\n  ") + "\n"
}
