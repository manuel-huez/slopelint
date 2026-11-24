package outline

import (
	"fmt"
	"strings"

	ts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars/python"
	"github.com/odvcencio/gotreesitter/grammars/tsx"
	"github.com/odvcencio/gotreesitter/grammars/typescript"
)

// SyntaxError marks a file the parser cannot read. The tree-sitter parser
// also rejects some valid TypeScript, so callers report the file and lint
// the rest instead of failing the run.
type SyntaxError struct {
	Pos Pos
}

func (err *SyntaxError) Error() string {
	return fmt.Sprintf("%d:%d: syntax error", err.Pos.Line, err.Pos.Column)
}

// Parse outlines one TypeScript or Python file. Generated files get only
// their line count.
func Parse(name string, src []byte) (File, error) {
	language := LanguageFor(name)
	file := File{
		Lines: lineCount(src),
		Start: Pos{Line: 1, Column: 1},
		Test:  TestFile(name),
	}

	if generated(src) {
		file.Generated = true
		return file, nil
	}

	var grammar *ts.Language

	switch {
	case language == Python:
		grammar = python.Language()
	case strings.HasSuffix(name, ".tsx"):
		grammar = tsx.Language()
	case language == TypeScript:
		grammar = typescript.Language()
	default:
		return File{}, fmt.Errorf("%s is not a TypeScript or Python file", name)
	}

	tree, err := ts.NewParser(grammar).ParseStrict(src)
	if err != nil {
		return File{}, err
	}
	defer tree.Release()

	root := tree.RootNode()
	if root == nil {
		return file, nil
	}

	if bad := firstSyntaxError(root); bad != nil {
		return File{}, &SyntaxError{Pos: syntax{}.pos(bad)}
	}

	w := walker{
		syntax:   syntax{grammar: grammar, src: src},
		name:     name,
		language: language,
		file:     &file,
	}
	w.walk(root, "")
	w.markCallbacks(root)
	w.finish(root)
	w.finishTests()
	w.finishBlocks()
	w.finishSmells(root)

	if !file.Test {
		file.Imports = collectImports(w.syntax, language, root)
	}

	return file, nil
}

func (w *walker) markCallbacks(root *ts.Node) {
	refs := make(map[string]int)
	calls := make(map[string]int)

	var visit func(*ts.Node)

	visit = func(node *ts.Node) {
		switch node.Type(w.grammar) {
		case identifierNode:
			refs[w.text(node)]++
		case tsCallNode, pyCallNode:
			if callee := w.field(
				node,
				"function",
			); callee != nil &&
				callee.Type(w.grammar) == identifierNode {
				calls[w.text(callee)]++
			}
		}

		for index := range node.NamedChildCount() {
			visit(node.NamedChild(index))
		}
	}

	visit(root)

	for index, fn := range w.file.Funcs {
		if fn.Name != "" && !fn.Literal && fn.Receiver == "" {
			w.file.Funcs[index].Callback = calls[fn.Name] == 0 && refs[fn.Name] > 1
		}
	}
}

func firstSyntaxError(node *ts.Node) *ts.Node {
	if node.IsError() || node.IsMissing() {
		return node
	}

	if !node.HasError() {
		return nil
	}

	for index := range node.ChildCount() {
		if bad := firstSyntaxError(node.Child(index)); bad != nil {
			return bad
		}
	}

	return node
}

// Statement node types both grammars share.
const (
	statementNode = "expression_statement"
	ifNode        = "if_statement"
	elseNode      = "else_clause"
	returnNode    = "return_statement"
	withNode      = "with_statement"
	forNode       = "for_statement"
)

// Other node types both grammars share.
const (
	commentNode    = "comment"
	identifierNode = "identifier"
	stringNode     = "string"
	trueNode       = "true"
	falseNode      = "false"
)

// TypeScript declaration node types.
const (
	tsFieldNode      = "public_field_definition"
	tsLexicalNode    = "lexical_declaration"
	tsVarNode        = "variable_declaration"
	tsDeclaratorNode = "variable_declarator"
	tsClassNode      = "class_declaration"
	tsExportNode     = "export_statement"
	tsMethodNode     = "method_definition"
)

// TypeScript function node types.
const (
	tsFuncDeclNode  = "function_declaration"
	tsGeneratorNode = "generator_function_declaration"
	tsArrowNode     = "arrow_function"
	tsFuncExprNode  = "function_expression"
	tsGeneratorExpr = "generator_function"
)

// TypeScript statement node types.
const (
	tsBlockNode  = "statement_block"
	tsSwitchNode = "switch_statement"
	tsForInNode  = "for_in_statement"
	tsCatchNode  = "catch_clause"
)

// TypeScript expression node types.
const (
	tsCallNode   = "call_expression"
	tsAwaitNode  = "await_expression"
	tsMemberNode = "member_expression"
	tsAssignNode = "assignment_expression"
	tsBinaryNode = "binary_expression"
	tsUnaryNode  = "unary_expression"
	tsParenNode  = "parenthesized_expression"
	tsYieldNode  = "yield_expression"
)

const (
	tsThisNode    = "this"
	tsNumberNode  = "number"
	tsCaseNode    = "switch_case"
	tsDefaultNode = "switch_default"
)

const (
	pyModuleNode    = "module"
	pyBlockNode     = "block"
	pyDecoratedNode = "decorated_definition"
	pyFunctionNode  = "function_definition"
	pyClassNode     = "class_definition"
	pyLambdaNode    = "lambda"
	pyElifNode      = "elif_clause"
	pyExceptNode    = "except_clause"
	pyImportNode    = "import_from_statement"
	pyAssignNode    = "assignment"
)

// Python expression node types.
const (
	pyCallNode      = "call"
	pyAttributeNode = "attribute"
	pyNotNode       = "not_operator"
	pyBoolNode      = "boolean_operator"
	pyCompareNode   = "comparison_operator"
)

type rawComment struct {
	start, end  uint32
	pos         Pos
	endLine     int
	ownLine     bool
	judged      bool
	suppression bool
	docstring   bool
}

type syntax struct {
	grammar *ts.Language
	src     []byte
}

type walker struct {
	syntax

	name      string
	language  Language
	file      *File
	comments  []rawComment
	moduleDoc *ts.Node
	// sleeps wait for finishTests to drop those that fakeTimers, a patched
	// sleep, or the module sleepImport names covers.
	sleeps      []sleep
	fakeTimers  bool
	patched     []string
	sleepImport string
	blocks      []pendingBlock
	blockSpans  map[[2]uint32]bool
}

// walk records functions, types, and comments. receiver is the class whose
// body directly holds node; deeper nodes get none.
func (w *walker) walk(node *ts.Node, receiver string) {
	// Anonymous keyword tokens share names such as "class" and "lambda" with
	// the nodes they start.
	if !node.IsNamed() {
		return
	}

	kind := node.Type(w.grammar)

	if kind == commentNode {
		w.comment(node)
		return
	}

	if w.language == Python {
		w.walkPython(node, kind, receiver)
	} else {
		w.walkTypeScript(node, kind, receiver)
	}
}

func (w *walker) walkChildren(node *ts.Node, receiver string) {
	for index := range node.ChildCount() {
		w.walk(node.Child(index), receiver)
	}
}

func (w *walker) walkTypeScript(node *ts.Node, kind, receiver string) {
	switch kind {
	case tsClassNode, "abstract_class_declaration", "class":
		name := w.field(node, nameField)
		if name != nil {
			w.file.Types = append(w.file.Types, Type{
				Name:   w.text(name),
				Pos:    w.pos(name),
				Embeds: w.baseClasses(node),
			})
		}

		for index := range node.ChildCount() {
			child := node.Child(index)
			if child.Type(w.grammar) == "class_body" && name != nil {
				w.walkChildren(child, w.text(name))
			} else {
				w.walk(child, "")
			}
		}

		return
	case tsFuncDeclNode, tsGeneratorNode, tsMethodNode:
		name := w.field(node, nameField)
		w.function(node, w.text(name), name, receiver)
	case tsArrowNode, tsFuncExprNode, tsGeneratorExpr:
		name, anchor, owner := w.boundName(node)
		w.function(node, name, anchor, owner)
	case tsCallNode, tsAwaitNode, tsCatchNode:
		w.tsEvent(node, kind)
	}

	w.walkChildren(node, "")
}

var bindingFields = map[string][2]string{
	tsDeclaratorNode: {valueField, nameField},
	"pair":           {valueField, "key"},
	tsAssignNode:     {"right", "left"},
	tsFieldNode:      {valueField, nameField},
}

const (
	valueField = "value"
	nameField  = "name"
)

// boundName returns the name a function value is bound to and the node
// that anchors it: a variable, an object or class property, an assignment
// target, or a default export. A class field's function is a method of that
// class. The sole argument of a configured wrapper call, as in
// `const f = Effect.fn("f")(function* () {})`, takes the wrapper's binding.
// An unbound value returns a nil anchor.
func (w *walker) boundName(node *ts.Node) (string, *ts.Node, string) {
	parent := node.Parent()
	if parent == nil {
		return "", nil, ""
	}

	kind := parent.Type(w.grammar)

	switch kind {
	case tsExportNode:
		return "default", node, ""
	case "arguments":
		call := parent.Parent()
		if parent.NamedChildCount() == 1 && call.Type(w.grammar) == tsCallNode &&
			w.field(call, "function").Type(w.grammar) == tsCallNode {
			return w.boundName(call)
		}
	}

	fields, ok := bindingFields[kind]
	if !ok || w.field(parent, fields[0]) != node {
		return "", nil, ""
	}

	name := w.field(parent, fields[1])
	if property := w.field(name, "property"); property != nil {
		name = property
	}

	// A class field sits in the class body, whose parent names the class.
	owner := ""
	if kind == tsFieldNode {
		owner = w.text(w.field(parent.Parent().Parent(), nameField))
	}

	return w.text(name), name, owner
}

func (w *walker) tsEvent(node *ts.Node, kind string) {
	switch {
	case !w.file.Test && kind != tsAwaitNode:
		w.handler(node, kind)
	case w.file.Test && kind == tsCallNode:
		w.tsTestCall(node)
		w.tsClockCall(node)
	case w.file.Test && kind == tsAwaitNode:
		w.tsClockAwait(node)
	}
}

func (w *walker) pythonEvent(node *ts.Node, kind string) {
	switch {
	case w.file.Test && (kind == pyCallNode || kind == pyImportNode):
		w.pythonClock(node)
	case !w.file.Test && kind != pyCallNode && kind != pyImportNode:
		w.handler(node, kind)
	}
}

// baseClasses names the classes a class extends by plain name: the
// TypeScript extends clause, or Python's base list. Qualified bases live in
// another file, so they never count toward this one.
func (w *walker) baseClasses(class *ts.Node) []string {
	var bases []string

	add := func(node *ts.Node) {
		if node != nil && node.Type(w.grammar) == identifierNode {
			bases = append(bases, w.text(node))
		}
	}

	if supers := w.field(class, "superclasses"); supers != nil {
		for index := range supers.NamedChildCount() {
			add(supers.NamedChild(index))
		}
	}

	for index := range class.NamedChildCount() {
		heritage := class.NamedChild(index)
		if heritage.Type(w.grammar) != "class_heritage" {
			continue
		}

		for clause := range heritage.NamedChildCount() {
			if extends := heritage.NamedChild(clause); extends.Type(w.grammar) == "extends_clause" {
				add(extends.NamedChild(0))
			}
		}
	}

	return bases
}

func (w *walker) walkPython(node *ts.Node, kind, receiver string) {
	switch kind {
	case pyClassNode:
		name := w.field(node, nameField)
		w.file.Types = append(w.file.Types, Type{
			Name:   w.text(name),
			Pos:    w.pos(name),
			Embeds: w.baseClasses(node),
		})
		body := w.field(node, "body")
		w.docstring(body)

		for index := range node.ChildCount() {
			child := node.Child(index)
			if child == body {
				w.walkChildren(child, w.text(name))
			} else {
				w.walk(child, "")
			}
		}

		return
	case pyDecoratedNode:
		w.walkChildren(node, receiver)
		return
	case pyFunctionNode:
		w.docstring(w.field(node, "body"))
		name := w.field(node, nameField)
		w.function(node, w.text(name), name, receiver)

		if w.file.Test {
			w.pythonTestFunction(node, w.text(name))
		}
	case pyCallNode, pyImportNode, pyExceptNode, "except_group_clause", withNode:
		w.pythonEvent(node, kind)
	case pyLambdaNode:
		w.function(node, "", nil, "")
	case pyModuleNode:
		w.moduleDoc = w.docstring(node)
	}

	w.walkChildren(node, "")
}

// function records a function named at anchor; a nil anchor marks a
// literal.
func (w *walker) function(node *ts.Node, name string, anchor *ts.Node, receiver string) {
	fn := Func{
		Name:       name,
		Pos:        w.pos(node),
		StartLine:  int(node.StartPoint().Row) + 1,
		Statements: w.statements(node),
		Largest:    w.largestStatements(node),
		Movable:    w.movableClosures(node, anchor),
		Params:     w.params(node, receiver),
		Body:       true,
		Literal:    anchor == nil,
		Receiver:   receiver,
	}

	if anchor != nil {
		fn.Pos = w.pos(anchor)

		symbol := name
		if receiver != "" {
			symbol = receiver + "." + name
		}

		w.block(symbol, anchor, node)
	}

	w.file.Funcs = append(w.file.Funcs, fn)
}

// params counts declared parameters. TypeScript `this` annotations and the
// self or cls of Python methods are receivers, not parameters.
func (w *walker) params(node *ts.Node, receiver string) int {
	list := w.field(node, "parameters")
	if list == nil {
		if w.field(node, "parameter") != nil {
			return 1
		}

		return 0
	}

	count := 0

	// Python parameters after `*` or `*args` are keyword-only: each call
	// names them, as it would the fields of an options struct.
	for index := range list.NamedChildCount() {
		param := list.NamedChild(index)
		if param.Type(w.grammar) == "keyword_separator" {
			break
		}

		if !w.receiverOrSeparator(param, count == 0 && receiver != "") {
			count++
		}

		if param.Type(w.grammar) == "list_splat_pattern" {
			break
		}
	}

	return count
}

// receiverOrSeparator reports a parameter-list entry that declares no
// parameter: a separator, a TypeScript `this` annotation, or the self or cls
// that opens a Python method.
func (w *walker) receiverOrSeparator(param *ts.Node, first bool) bool {
	switch param.Type(w.grammar) {
	case commentNode, "positional_separator", "keyword_separator":
		return true
	case "required_parameter", "optional_parameter":
		pattern := w.field(param, "pattern")
		return pattern != nil && pattern.Type(w.grammar) == tsThisNode
	case identifierNode:
		return first && w.language == Python && (w.text(param) == "self" || w.text(param) == "cls")
	default:
		return false
	}
}

func (w *walker) docstring(body *ts.Node) *ts.Node {
	if body == nil {
		return nil
	}

	for index := range body.NamedChildCount() {
		child := body.NamedChild(index)

		switch child.Type(w.grammar) {
		case commentNode:
			continue
		case statementNode:
			if child.NamedChildCount() != 1 || child.NamedChild(0).Type(w.grammar) != stringNode {
				return nil
			}
		case stringNode:
		default:
			return nil
		}

		w.comments = append(w.comments, rawComment{
			start:     child.StartByte(),
			end:       child.EndByte(),
			pos:       w.pos(child),
			endLine:   w.endLine(child),
			judged:    true,
			docstring: true,
		})

		return child
	}

	return nil
}

func (w *walker) comment(node *ts.Node) {
	start := node.StartByte()
	lineStart := strings.LastIndexByte(string(w.src[:start]), '\n') + 1
	text := w.text(node)
	judged, suppression := judgeComment(text, w.language)

	w.comments = append(w.comments, rawComment{
		start:       start,
		end:         node.EndByte(),
		pos:         w.pos(node),
		endLine:     w.endLine(node),
		ownLine:     strings.TrimSpace(string(w.src[lineStart:start])) == "",
		judged:      judged,
		suppression: suppression,
	})
}

func (s syntax) field(node *ts.Node, name string) *ts.Node {
	if node == nil {
		return nil
	}

	return node.ChildByFieldName(name, s.grammar)
}

func (s syntax) text(node *ts.Node) string {
	if node == nil {
		return ""
	}

	return node.Text(s.src)
}

func (s syntax) pos(node *ts.Node) Pos {
	point := node.StartPoint()

	return Pos{
		Offset: int(node.StartByte()),
		Line:   int(point.Row) + 1,
		Column: int(point.Column) + 1,
	}
}

// endLine is the line of the node's last byte; tree-sitter ends a node that
// includes its newline at column 0 of the next line.
func (s syntax) endLine(node *ts.Node) int {
	end := node.EndPoint()
	if end.Column == 0 && end.Row > node.StartPoint().Row {
		return int(end.Row)
	}

	return int(end.Row) + 1
}
