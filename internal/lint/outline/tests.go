package outline

import (
	"path"
	"slices"
	"strconv"
	"strings"

	ts "github.com/odvcencio/gotreesitter"
)

// Test is one test case: a TypeScript test or it call with a callback, or
// a Python test function or method as pytest and unittest collect them.
type Test struct {
	Pos       Pos
	StartLine int
	EndLine   int
	// Context is the line ranges review shows before the test: enclosing
	// suite and class header lines and their setup hooks.
	Context []Decl
}

// hookLineLimit bounds a setup hook shown whole as test context; a longer
// hook shows only its first line.
const hookLineLimit = 40

const (
	pythonTestPrefix = "test"
	pythonTime       = "time"
	pythonAsyncio    = "asyncio"
	pythonSleep      = "sleep"
)

// The callee names of bun:test, Vitest, Jest, and node:test tests, suites,
// and hooks.
var (
	tsTestRoots  = map[string]bool{"test": true, "it": true, "fit": true}
	tsFocusRoots = map[string]bool{"fit": true, "fdescribe": true}
	tsSuiteRoots = map[string]bool{"describe": true, "suite": true}
	tsHookNames  = map[string]bool{
		"beforeEach": true, "beforeAll": true, "afterEach": true, "afterAll": true,
		"before": true, "after": true,
	}
	// tsSkipModifiers do not run the test body.
	tsSkipModifiers = map[string]bool{"skip": true, "todo": true, "skipIf": true, "fixme": true}
)

// callChain returns the names along a callee: [test, each] for
// test.each(rows)(...), [expect, not, toBe] for expect(x).not.toBe(...).
func (s syntax) callChain(callee *ts.Node) []string {
	if callee == nil {
		return nil
	}

	switch callee.Type(s.grammar) {
	case "identifier", "this":
		return []string{s.text(callee)}
	case "member_expression":
		return append(s.callChain(s.field(callee, "object")), s.text(s.field(callee, "property")))
	case "attribute":
		return append(s.callChain(s.field(callee, "object")), s.text(s.field(callee, "attribute")))
	case tsCallNode, pyCallNode:
		return s.callChain(s.field(callee, "function"))
	case tsAwaitNode, "parenthesized_expression", "non_null_expression":
		return s.callChain(callee.NamedChild(0))
	default:
		return nil
	}
}

func (s syntax) chain(call *ts.Node) []string {
	return s.callChain(s.field(call, "function"))
}

func lastName(chain []string) string {
	if len(chain) == 0 {
		return ""
	}

	return chain[len(chain)-1]
}

// tsTestCall records a TypeScript test or focused call in a test file.
// Tests nested in another test's callback are part of that test.
func (w *walker) tsTestCall(call *ts.Node) {
	chain := w.chain(call)
	if len(chain) == 0 || w.lastFunctionArgument(call) == nil {
		return
	}

	root, modifiers := chain[0], chain[1:]
	if tsFocusRoots[root] ||
		slices.Contains(modifiers, "only") && (tsTestRoots[root] || tsSuiteRoots[root]) {
		w.file.Focused = append(w.file.Focused, w.pos(call))
	}

	if !tsTestRoots[root] || w.suite(chain) ||
		slices.ContainsFunc(modifiers, func(name string) bool { return tsSkipModifiers[name] }) ||
		w.enclosingTest(call) {
		return
	}

	w.file.Tests = append(w.file.Tests, Test{
		Pos:       w.pos(call),
		StartLine: w.pos(call).Line,
		EndLine:   w.endLine(call),
		Context:   w.tsTestContext(call),
	})
	w.block(root+" "+w.title(call), call, call)
}

func (s syntax) title(call *ts.Node) string {
	args := s.field(call, "arguments")
	if args == nil || args.NamedChildCount() == 0 {
		return ""
	}

	return s.text(args.NamedChild(0))
}

func (s syntax) lastFunctionArgument(call *ts.Node) *ts.Node {
	args := s.field(call, "arguments")
	if args == nil {
		return nil
	}

	for index := args.NamedChildCount() - 1; index >= 0; index-- {
		if arg := args.NamedChild(index); tsFunctionKinds[arg.Type(s.grammar)] {
			return arg
		}
	}

	return nil
}

// suite matches describe and suite calls, and @effect/vitest's it.layer,
// which groups the tests of its callback.
func (s syntax) suite(chain []string) bool {
	return len(chain) > 0 && (tsSuiteRoots[chain[0]] ||
		tsTestRoots[chain[0]] && slices.Contains(chain, "layer"))
}

func (s syntax) enclosingTest(node *ts.Node) bool {
	for parent := node.Parent(); parent != nil; parent = parent.Parent() {
		if parent.Type(s.grammar) != tsCallNode {
			continue
		}

		if chain := s.chain(parent); len(chain) > 0 && tsTestRoots[chain[0]] &&
			!s.suite(chain) && s.lastFunctionArgument(parent) != nil {
			return true
		}
	}

	return false
}

func (w *walker) tsTestContext(call *ts.Node) []Decl {
	var context []Decl

	for parent := call.Parent(); parent != nil; parent = parent.Parent() {
		switch parent.Type(w.grammar) {
		case tsCallNode:
			if w.suite(w.chain(parent)) {
				line := w.pos(parent).Line
				context = append(context, Decl{StartLine: line, EndLine: line})
			}
		case "statement_block", "program":
			for index := range parent.NamedChildCount() {
				statement := parent.NamedChild(index)
				if statement.Type(w.grammar) != statementNode ||
					statement.NamedChildCount() != 1 {
					continue
				}

				hook := statement.NamedChild(0)
				if chain := w.chain(hook); hook.Type(w.grammar) == tsCallNode &&
					len(chain) == 1 && tsHookNames[chain[0]] {
					context = append(context, w.hookLines(hook))
				}
			}
		}
	}

	slices.SortFunc(context, Decl.Compare)

	return context
}

func (s syntax) hookLines(hook *ts.Node) Decl {
	lines := Decl{StartLine: s.pos(hook).Line, EndLine: s.endLine(hook)}
	if lines.EndLine-lines.StartLine+1 > hookLineLimit {
		lines.EndLine = lines.StartLine
	}

	return lines
}

var pythonSetups = map[string]bool{
	"setUp": true, "asyncSetUp": true, "setUpClass": true,
	"setup_method": true, "setup_class": true, "setup": true,
}

// pythonTestFunction records a function pytest or unittest collects: a
// module-level test* function, or a test* method of a Test* class or a
// TestCase subclass, in a test*.py or *_test.py file.
func (w *walker) pythonTestFunction(fn *ts.Node, name string) {
	base := path.Base(strings.ReplaceAll(w.name, "\\", "/"))
	if !strings.HasPrefix(base, pythonTestPrefix) && !strings.HasSuffix(base, "_test.py") ||
		!strings.HasPrefix(name, pythonTestPrefix) {
		return
	}

	definition := fn
	if parent := fn.Parent(); parent.Type(w.grammar) == pyDecoratedNode {
		definition = parent
	}

	var context []Decl

	switch owner := definition.Parent(); owner.Type(w.grammar) {
	case "module":
	case pyBlockNode:
		class := owner.Parent()
		if class.Type(w.grammar) != "class_definition" || !w.testClass(class) {
			return
		}

		context = w.classContext(class, owner)
	default:
		return
	}

	w.file.Tests = append(w.file.Tests, Test{
		Pos:       w.pos(fn),
		StartLine: w.pos(definition).Line,
		EndLine:   w.endLine(definition),
		Context:   context,
	})
}

func (s syntax) classContext(class, body *ts.Node) []Decl {
	line := s.pos(class).Line
	context := []Decl{{StartLine: line, EndLine: line}}

	for index := range body.NamedChildCount() {
		method := body.NamedChild(index)
		if method.Type(s.grammar) == pyDecoratedNode {
			method = s.lastNamedChild(method, pyFunctionNode)
		}

		if method != nil && method.Type(s.grammar) == pyFunctionNode &&
			pythonSetups[s.text(s.field(method, nameField))] {
			context = append(context, s.hookLines(method))
		}
	}

	return context
}

func (s syntax) testClass(class *ts.Node) bool {
	return strings.HasPrefix(s.text(s.field(class, nameField)), "Test") ||
		strings.Contains(s.text(s.field(class, "superclasses")), "TestCase")
}

// tsSleeps are callee names that wait for real time when awaited: sleep and
// delay helpers, setTimeout from node:timers/promises, and Playwright's
// waitForTimeout.
var tsSleeps = map[string]bool{
	pythonSleep:      true,
	"delay":          true,
	"setTimeout":     true,
	"waitForTimeout": true,
}

// tsClockCall records whether a TypeScript test file installs fake timers,
// and its Bun.sleepSync calls. A zero or missing delay only yields.
func (w *walker) tsClockCall(call *ts.Node) {
	chain := w.chain(call)
	if slices.Contains(chain, "useFakeTimers") {
		w.fakeTimers = true
	}

	if slices.Equal(chain, []string{"Bun", "sleepSync"}) && w.realDelay(w.argument(call, 0)) {
		w.sleeps = append(w.sleeps, sleep{pos: w.pos(call)})
	}
}

func (w *walker) tsClockAwait(node *ts.Node) {
	switch waited := node.NamedChild(0); waited.Type(w.grammar) {
	case "new_expression":
		if w.text(w.field(waited, "constructor")) == "Promise" &&
			w.contains(waited, func(*ts.Node) bool { return false }, func(inner *ts.Node) bool {
				return inner.Type(w.grammar) == tsCallNode &&
					lastName(w.chain(inner)) == "setTimeout" && w.realDelay(w.argument(inner, 1))
			}) {
			w.sleeps = append(w.sleeps, sleep{pos: w.pos(node)})
		}
	case tsCallNode:
		first := w.argument(waited, 0)
		if tsSleeps[lastName(w.chain(waited))] && w.realDelay(first) &&
			!tsFunctionKinds[first.Type(w.grammar)] {
			w.sleeps = append(w.sleeps, sleep{pos: w.pos(node)})
		}
	}
}

// sleep is a real wait in a test file. module is time or asyncio for a
// Python sleep, which can be patched one module at a time; empty for a bare
// sleep() call until its import is known.
type sleep struct {
	pos    Pos
	module string
}

// pythonClock records time.sleep and asyncio.sleep calls in a Python test
// file, sleep calls imported from those modules, and patches of them, as by
// mock.patch("time.sleep") or monkeypatch.setattr(time, "sleep", ...).
// freezegun and time-machine do not count: they fake the clock, not sleep,
// which still waits. node is a call or a from-import.
func (w *walker) pythonClock(node *ts.Node) {
	if node.Type(w.grammar) == pyImportNode {
		module := w.text(w.field(node, "module_name"))
		if (module == pythonTime || module == pythonAsyncio) &&
			strings.Contains(w.text(node), pythonSleep) {
			w.sleepImport = module
		}

		return
	}

	chain := w.chain(node)
	module := ""

	switch {
	case slices.Equal(chain, []string{pythonTime, pythonSleep}),
		slices.Equal(chain, []string{pythonAsyncio, pythonSleep}):
		module = chain[0]
	case slices.Equal(chain, []string{pythonSleep}):
	case slices.Contains([]string{"patch", "setattr", "object"}, lastName(chain)):
		w.patchedSleep(w.text(w.field(node, "arguments")))

		return
	default:
		return
	}

	if w.realDelay(w.argument(node, 0)) {
		w.sleeps = append(w.sleeps, sleep{pos: w.pos(node), module: module})
	}
}

func (w *walker) patchedSleep(args string) {
	switch {
	case !strings.Contains(args, pythonSleep):
	case strings.Contains(args, pythonAsyncio):
		w.patched = append(w.patched, pythonAsyncio)
	default:
		w.patched = append(w.patched, pythonTime)
	}
}

// finishTests keeps the sleeps no fake clock covers, once every import and
// patch of the file is known. As Go files that use testing/synctest are,
// TypeScript files with fake timers are exempt as a whole.
func (w *walker) finishTests() {
	if w.fakeTimers {
		return
	}

	for _, each := range w.sleeps {
		if w.language == Python {
			if each.module == "" {
				each.module = w.sleepImport
			}

			if each.module == "" || slices.Contains(w.patched, each.module) {
				continue
			}
		}

		w.file.Sleeps = append(w.file.Sleeps, each.pos)
	}
}

func (s syntax) realDelay(arg *ts.Node) bool {
	if arg == nil {
		return false
	}

	value, err := strconv.ParseFloat(strings.ReplaceAll(s.text(arg), "_", ""), 64)

	return err != nil || value != 0
}

func (s syntax) argument(call *ts.Node, index int) *ts.Node {
	args := s.field(call, "arguments")
	if args == nil || index >= args.NamedChildCount() {
		return nil
	}

	return args.NamedChild(index)
}
