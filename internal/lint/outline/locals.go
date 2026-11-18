package outline

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	ts "github.com/odvcencio/gotreesitter"
)

type localRules struct{ *checker }

// The limits match the Go abstraction_overkill and trivial_wrapper rules.
const (
	helperMaxStmts      = 3
	helperMaxParams     = 2
	forwarderShortLines = 3
	forwarderAssignForm = 2
)

// Words splits an identifier into lower-case words at underscores and
// case changes, so `parseHTTPRequest` and `parse_http_request` agree.
func Words(name string) []string {
	var words []string

	start := -1
	runes := []rune(name)

	flush := func(end int) {
		if start < 0 || start >= end {
			return
		}

		words = append(words, strings.ToLower(string(runes[start:end])))
		start = -1
	}

	for idx, r := range runes {
		if r == '_' {
			flush(idx)
			continue
		}

		if start < 0 {
			start = idx
			continue
		}

		prev := runes[idx-1]

		nextLower := idx+1 < len(runes) && unicode.IsLower(runes[idx+1])
		if unicode.IsLower(prev) && unicode.IsUpper(r) ||
			unicode.IsUpper(prev) && unicode.IsUpper(r) && nextLower {
			flush(idx)
			start = idx
		}
	}

	flush(len(runes))

	return words
}

// GenericName reports a helper name built from words that name no role.
func GenericName(name string) bool {
	for _, word := range Words(name) {
		switch word {
		case "helper", "manager", "processor", "util", "utils", "base", "impl", "wrapper":
			return true
		}
	}

	return false
}

// occurrences counts name as a whole identifier in text, including in
// strings and comments.
func occurrences(text, name string) int {
	count := 0

	for offset := 0; ; {
		index := strings.Index(text[offset:], name)
		if index < 0 {
			return count
		}

		start, end := offset+index, offset+index+len(name)
		if (start == 0 || !identifierByte(text[start-1])) &&
			(end == len(text) || !identifierByte(text[end])) {
			count++
		}

		offset = end
	}
}

func identifierByte(b byte) bool {
	return b == '_' || b == '$' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' ||
		b >= 'A' && b <= 'Z' || b >= 0x80
}

func (s syntax) enclosingFunction(node *ts.Node) *ts.Node {
	for parent := node.Parent(); parent != nil; parent = parent.Parent() {
		if kind := parent.Type(s.grammar); tsFunctionKinds[kind] || kind == pyFunctionNode {
			return parent
		}
	}

	return nil
}

// identifiers calls visit for each identifier under node that can name a
// value: not a Python attribute or keyword-argument name.
func (s syntax) identifiers(node *ts.Node, visit func(*ts.Node)) {
	switch node.Type(s.grammar) {
	case identifierNode, "shorthand_property_identifier":
		parent := node.Parent()
		if parent == nil || s.field(parent, "attribute") != node &&
			(parent.Type(s.grammar) != "keyword_argument" || s.field(parent, nameField) != node) {
			visit(node)
		}

		return
	}

	for index := range node.NamedChildCount() {
		s.identifiers(node.NamedChild(index), visit)
	}
}

func (s syntax) mentions(node *ts.Node, name string) []*ts.Node {
	var out []*ts.Node

	s.identifiers(node, func(each *ts.Node) {
		if s.text(each) == name {
			out = append(out, each)
		}
	})

	return out
}

// aliasSource matches a read of a name or a chain of plain property reads,
// and returns the last name.
func (s syntax) aliasSource(node *ts.Node) (string, bool) {
	switch node.Type(s.grammar) {
	case identifierNode:
		return s.text(node), true
	case tsMemberNode, pyAttributeNode:
		property := s.field(node, "property")
		if property == nil {
			property = s.field(node, "attribute")
		}

		object := s.field(node, "object")
		if _, ok := s.aliasSource(object); property == nil ||
			!ok && object.Type(s.grammar) != tsThisNode {
			return "", false
		}

		return s.text(property), true
	default:
		return "", false
	}
}

// localAlias matches `const name = source` or Python `name = source` and
// returns the name node and the source.
func (c localRules) localAlias(stmt *ts.Node) (*ts.Node, *ts.Node, bool) {
	kind := stmt.Type(c.grammar)
	if c.language == Python {
		left, right, ok := c.assignment(stmt)
		return left, right, ok && left.Type(c.grammar) == identifierNode
	}

	if kind != tsLexicalNode && kind != tsVarNode || stmt.NamedChildCount() != 1 {
		return nil, nil, false
	}

	decl := stmt.NamedChild(0)
	name, value := c.field(decl, nameField), c.field(decl, valueField)

	return name, value, decl.Type(c.grammar) == tsDeclaratorNode && c.field(decl, "type") == nil &&
		name != nil && name.Type(c.grammar) == identifierNode && value != nil
}

// tempAlias reports a local that only renames a property or variable for
// its one use in the next statement, as the Go temp_alias rule does. The
// name must appear exactly twice in its function, which rules out later
// uses and shadowing. The next statement must read the name exactly once
// on every run, and must not await, yield, assign, or call through the
// source's root, so inlining reads the source once, at the same point
// relative to every write that syntax can see.
func (c localRules) tempAlias(list *ts.Node, stmts []*ts.Node, index int) {
	if index+1 >= len(stmts) {
		return
	}

	name, source, ok := c.localAlias(stmts[index])
	if !ok || c.attachedComment(stmts[index]) {
		return
	}

	scope := c.enclosingFunction(list)

	leaf, ok := c.aliasSource(source)
	if !ok || scope == nil {
		return
	}

	nameWords, leafWords := AliasWords(c.text(name)), AliasWords(leaf)
	if len(nameWords) == 0 || !slices.Equal(nameWords, leafWords) ||
		len(c.mentions(scope, c.text(name))) != 2 {
		return
	}

	if !c.inlinable(stmts[index+1], c.text(name), source) {
		return
	}

	c.report(name, "temp_alias", fmt.Sprintf(
		`local %q only renames cheap expression %q for one use; inline expression`,
		c.text(name), c.oneLine(source),
	))
}

func (c localRules) inlinable(next *ts.Node, name string, source *ts.Node) bool {
	uses := c.mentions(next, name)

	return len(uses) == 1 && c.evaluatedOnce(next, uses[0]) &&
		(!strings.Contains(c.text(source), "?.") || !c.chainedOn(uses[0])) &&
		!c.mayWrite(next, source)
}

// AliasWords returns Words without the filler words that temp aliases add,
// such as `tmp` or `value`.
func AliasWords(name string) []string {
	return slices.DeleteFunc(Words(name), func(word string) bool {
		switch word {
		case "alias", "copy", "temp", "tmp", "val", "value":
			return true
		default:
			return false
		}
	})
}

var (
	// deferringKinds run their body later or repeatedly, so an inlined read
	// would move or repeat.
	deferringKinds = map[string]bool{
		pyLambdaNode: true, tsClassNode: true, pyClassNode: true,
		"for_statement": true, "for_in_statement": true, "while_statement": true,
		"do_statement": true, "list_comprehension": true, "set_comprehension": true,
		"dictionary_comprehension": true, "generator_expression": true,
	}
	// conditionalKinds run on some paths only, or destructure.
	conditionalKinds = map[string]bool{
		tsCaseNode: true, tsDefaultNode: true, tsCatchNode: true, pyExceptNode: true,
		"case_clause": true, pyElifNode: true, elseNode: true, "pair_pattern": true,
		"object_pattern": true, "array_pattern": true,
	}
	// writeKinds can write a value or suspend.
	writeKinds = map[string]bool{
		tsAssignNode: true, "augmented_assignment_expression": true, "update_expression": true,
		pyAssignNode: true, "augmented_assignment": true, tsAwaitNode: true, "await": true,
		tsYieldNode: true, "yield": true, "named_expression": true, "delete_statement": true,
	}
)

// mayWrite reports whether next can write through the root of source:
// any assignment, await, or yield, or a call with the root as receiver or
// argument, as the Go rule's mutation check counts them.
func (c localRules) mayWrite(next, source *ts.Node) bool {
	root := source
	for root.Type(c.grammar) == tsMemberNode || root.Type(c.grammar) == pyAttributeNode {
		root = c.field(root, "object")
	}

	return c.contains(next, func(*ts.Node) bool { return false }, func(node *ts.Node) bool {
		kind := node.Type(c.grammar)

		switch {
		case writeKinds[kind] || kind == tsUnaryNode && c.text(c.field(node, "operator")) == "delete":
			return true
		case kind == tsCallNode || kind == pyCallNode || kind == "new_expression":
			return c.callReaches(node, c.text(root))
		default:
			return false
		}
	})
}

func (c localRules) callReaches(call *ts.Node, root string) bool {
	callee := c.field(call, "function")
	if callee == nil {
		callee = c.field(call, "constructor")
	}

	if callee.Type(c.grammar) != identifierNode && c.rootedAt(callee, root) {
		return true
	}

	args := c.field(call, "arguments")
	if args == nil {
		return false
	}

	for index := range args.NamedChildCount() {
		if c.rootedAt(args.NamedChild(index), root) {
			return true
		}
	}

	return false
}

// rootedAt reports an access chain that starts at root; a chain whose root
// syntax does not show counts.
func (c localRules) rootedAt(node *ts.Node, root string) bool {
	for {
		switch node.Type(c.grammar) {
		case tsMemberNode, pyAttributeNode, "subscript_expression", "subscript":
			node = c.field(node, "object")
			if node == nil {
				return true
			}
		default:
			return c.text(node) == root
		}
	}
}

// evaluatedOnce reports whether next evaluates use exactly once, every
// time it runs. A getter, Python property, or tracking proxy such as a
// query result can observe a read that inlining would move into a branch
// or loop. A loop evaluates its iterable once, so only its other parts
// count.
func (c localRules) evaluatedOnce(next, use *ts.Node) bool {
	for child, parent := use, use.Parent(); parent != nil && child != next; child, parent = parent, parent.Parent() {
		if c.deferred(parent, child) ||
			strings.Contains(string(c.src[parent.StartByte():child.StartByte()]), "?.") {
			return false
		}
	}

	return true
}

// deferred reports whether parent may skip, repeat, or postpone child.
func (c localRules) deferred(parent, child *ts.Node) bool {
	kind := parent.Type(c.grammar)

	switch {
	case tsFunctionKinds[kind] || kind == pyFunctionNode || conditionalKinds[kind]:
		return true
	case deferringKinds[kind]:
		return c.field(parent, "right") != child
	case strings.HasPrefix(kind, "jsx_"):
		return c.field(parent, nameField) == child
	case kind == ifNode || kind == "ternary_expression":
		return c.field(parent, "condition") != child
	case kind == "conditional_expression":
		return parent.NamedChild(1) != child
	case kind == tsBinaryNode || kind == pyBoolNode:
		switch c.text(c.field(parent, "operator")) {
		case "&&", "||", "??", "and", "or":
			return c.field(parent, "left") != child
		}
	}

	return false
}

// chainedOn reports a use that a non-optional property access or call
// continues: `a?.b` inlined into `x.c` would short-circuit where `x.c` threw.
func (c localRules) chainedOn(use *ts.Node) bool {
	parent := use.Parent()
	switch parent.Type(c.grammar) {
	case tsMemberNode, "subscript_expression", tsCallNode:
	default:
		return false
	}

	return !strings.HasPrefix(string(c.src[use.EndByte():parent.EndByte()]), "?.")
}

// localFunc is a function only its own file can call: a TypeScript
// top-level function that is not exported.
type localFunc struct {
	name   *ts.Node
	node   *ts.Node
	params *ts.Node
	// body is a statement block, or the expression of an arrow function.
	body *ts.Node
}

// localFunc matches a function declaration or a const bound to a function
// value. Async and generic functions are skipped, as are type predicates
// and assertions: they narrow types at their call, which inlining loses.
func (c localRules) localFunc(stmt *ts.Node) (localFunc, bool) {
	name, fn := c.boundFunction(stmt)
	if fn == nil {
		return localFunc{}, false
	}

	switch fn.Type(c.grammar) {
	case tsFuncDeclNode, tsArrowNode, tsFuncExprNode:
	default:
		return localFunc{}, false
	}

	returns := c.text(c.field(fn, "return_type"))
	if name == nil || name.Type(c.grammar) != identifierNode ||
		c.field(fn, "type_parameters") != nil || c.text(fn.Child(0)) == "async" ||
		strings.Contains(returns, " is ") || strings.Contains(returns, "asserts ") {
		return localFunc{}, false
	}

	params := c.field(fn, "parameters")
	if params == nil {
		params = c.field(fn, "parameter")
	}

	return localFunc{name: name, node: fn, params: params, body: c.field(fn, "body")}, true
}

// boundFunction returns the name and value of a function declaration or
// an untyped `const` binding.
func (c localRules) boundFunction(stmt *ts.Node) (*ts.Node, *ts.Node) {
	if stmt.Type(c.grammar) != tsLexicalNode {
		return c.field(stmt, nameField), stmt
	}

	if stmt.NamedChildCount() != 1 || c.text(stmt.Child(0)) != "const" {
		return nil, nil
	}

	decl := stmt.NamedChild(0)
	if c.field(decl, "type") != nil {
		return nil, nil
	}

	return c.field(decl, nameField), c.field(decl, valueField)
}

// functions runs the Go trivial_wrapper, abstraction_overkill, and
// generic_naming rules on file-local functions of a production file. Every
// whole-word mention of the name in the file must be a direct call, apart
// from the declaration: a name used as a value, exported, shadowed, or
// mentioned in a string or comment is skipped. A call from the function's
// own body is no use.
func (c localRules) functions(root *ts.Node) {
	var fns []localFunc

	for index := range root.NamedChildCount() {
		if fn, ok := c.localFunc(root.NamedChild(index)); ok {
			fns = append(fns, fn)
		}
	}

	uses := c.funcUses(root, fns)

	for _, fn := range fns {
		use := uses[c.text(fn.name)]
		if use.fn == fn && !use.other && use.calls > 0 &&
			occurrences(string(c.src), c.text(fn.name)) == use.calls+1 {
			c.localFunction(fn, use.calls)
		}
	}
}

type funcUse struct {
	fn    localFunc
	calls int
	other bool
}

// funcUses counts the direct calls to each function from outside its body
// in one walk; any other mention, or a second function of the name, marks
// other.
func (c localRules) funcUses(root *ts.Node, fns []localFunc) map[string]*funcUse {
	uses := make(map[string]*funcUse, len(fns))

	for _, fn := range fns {
		if prior, ok := uses[c.text(fn.name)]; ok {
			prior.other = true
		} else {
			uses[c.text(fn.name)] = &funcUse{fn: fn}
		}
	}

	c.identifiers(root, func(node *ts.Node) {
		use, ok := uses[c.text(node)]
		if !ok || node == use.fn.name {
			return
		}

		parent := node.Parent()
		outside := node.StartByte() < use.fn.node.StartByte() ||
			node.StartByte() >= use.fn.node.EndByte()

		if outside && parent.Type(c.grammar) == tsCallNode && c.field(parent, "function") == node {
			use.calls++
		} else {
			use.other = true
		}
	})

	return uses
}

func (c localRules) localFunction(fn localFunc, calls int) {
	stmts := c.bodyStatements(fn.body)
	name := c.text(fn.name)

	if len(stmts) == 0 || c.commentIn(fn.body) {
		return
	}

	if callee, ok := c.forwardedCall(fn, stmts); ok {
		reason := " at one production use"

		if calls > 1 {
			if c.endLine(stmts[len(stmts)-1])-c.pos(stmts[0]).Line+1 > forwarderShortLines {
				return
			}

			reason = " in a body of 3 lines or less"
		}

		c.report(fn.name, "trivial_wrapper", fmt.Sprintf(
			`private helper %q only forwards to %q%s; inline or merge names`, name, callee, reason,
		))
		c.genericName(fn.name, "only forwards")

		return
	}

	if calls != 1 || len(stmts) > helperMaxStmts || c.paramCount(fn.params) > helperMaxParams ||
		slices.ContainsFunc(stmts, func(stmt *ts.Node) bool { return !c.tinyStatement(stmt) }) {
		return
	}

	c.report(fn.name, "abstraction_overkill", fmt.Sprintf(
		`private helper %q has one production use and a tiny body; inline or give it a stronger role`,
		name,
	))
	c.genericName(fn.name, "one tiny use")
}

func (c localRules) genericName(name *ts.Node, reason string) {
	if GenericName(c.text(name)) {
		c.report(name, "generic_naming", fmt.Sprintf(
			`private helper %q has generic name and %s; rename or inline`, c.text(name), reason,
		))
	}
}

// bodyStatements counts an arrow function's expression body as one return.
func (c localRules) bodyStatements(body *ts.Node) []*ts.Node {
	if body.Type(c.grammar) == tsBlockNode {
		return c.statements(body)
	}

	return []*ts.Node{body}
}

func (c localRules) commentIn(body *ts.Node) bool {
	first := c.commentFrom(c.pos(body).Line)

	return first < len(c.comments) && c.comments[first].pos.Line <= c.endLine(body)
}

func (c localRules) paramCount(params *ts.Node) int {
	switch {
	case params == nil:
		return 0
	case params.Type(c.grammar) == identifierNode:
		return 1
	default:
		return len(c.statements(params))
	}
}

// plainParams returns parameter names when every parameter is a bare name,
// with at most a type: no default, rest, destructuring, or modifier.
func (c localRules) plainParams(params *ts.Node) ([]string, bool) {
	if params == nil {
		return nil, true
	}

	if params.Type(c.grammar) == identifierNode {
		return []string{c.text(params)}, true
	}

	var names []string

	for _, param := range c.statements(params) {
		name := c.field(param, "pattern")
		extra := param.NamedChildCount() - 1

		if param.Type(c.grammar) != "required_parameter" || name == nil ||
			name.Type(c.grammar) != identifierNode || extra > 1 ||
			extra == 1 && c.field(param, "type") == nil {
			return nil, false
		}

		names = append(names, c.text(name))
	}

	return names, true
}

// forwardedCall matches a body that only passes the parameters, in order,
// to one named function, as the Go trivial_wrapper rule does: `return
// f(a, b)`, `f(a, b)`, or `const r = f(a, b); return r`. It returns the
// callee text.
func (c localRules) forwardedCall(fn localFunc, stmts []*ts.Node) (string, bool) {
	params, ok := c.plainParams(fn.params)
	call := c.bodyCall(stmts)

	if !ok || call == nil || call.Type(c.grammar) != tsCallNode ||
		c.field(call, "type_arguments") != nil {
		return "", false
	}

	callee := c.field(call, "function")
	if _, ok := c.aliasSource(callee); !ok || c.text(callee) == c.text(fn.name) ||
		slices.ContainsFunc(params, func(param string) bool {
			return occurrences(c.text(callee), param) > 0
		}) {
		return "", false
	}

	var args []string

	for _, arg := range c.statements(c.field(call, "arguments")) {
		if arg.Type(c.grammar) != identifierNode {
			return "", false
		}

		args = append(args, c.text(arg))
	}

	return c.oneLine(callee), slices.Equal(args, params)
}

func (c localRules) bodyCall(stmts []*ts.Node) *ts.Node {
	switch len(stmts) {
	case 1:
		stmt := stmts[0]
		if stmt.Type(c.grammar) != returnNode && stmt.Type(c.grammar) != statementNode {
			return stmt
		}

		if stmt.NamedChildCount() == 1 {
			return stmt.NamedChild(0)
		}
	case forwarderAssignForm:
		name, value, ok := c.localAlias(stmts[0])
		if key, isReturn := c.returnKey(stmts[1]); ok && isReturn && key == c.text(name) {
			return value
		}
	}

	return nil
}

// complexKinds are control flow, or make an inlined body behave apart from
// the original: `this` and yield depend on the function around them.
// Conditional expressions count as control flow: Go spells them with an
// if, which the Go rule's tiny body excludes.
var complexKinds = map[string]bool{
	ifNode: true, "for_statement": true, "for_in_statement": true, "while_statement": true,
	"do_statement": true, "switch_statement": true, "try_statement": true, withNode: true,
	tsClassNode: true, "class": true, tsYieldNode: true, tsThisNode: true,
	"ternary_expression": true, "optional_chain": true,
}

// tinyStatement matches an expression, declaration, or return with no
// nested function or control flow, as the Go rule's tiny body does.
func (c localRules) tinyStatement(stmt *ts.Node) bool {
	switch stmt.Type(c.grammar) {
	case statementNode, returnNode, tsLexicalNode, tsVarNode:
	default:
		if parent := stmt.Parent(); parent.Type(c.grammar) != tsArrowNode {
			return false
		}
	}

	return !c.contains(stmt, func(*ts.Node) bool { return false }, func(node *ts.Node) bool {
		kind := node.Type(c.grammar)

		return tsFunctionKinds[kind] || complexKinds[kind] ||
			kind == tsBinaryNode && c.text(c.field(node, "operator")) == "??"
	})
}
