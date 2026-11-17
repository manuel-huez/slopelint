package outline

import (
	"fmt"
	"strings"

	ts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars/python"
	"github.com/odvcencio/gotreesitter/grammars/tsx"
	"github.com/odvcencio/gotreesitter/grammars/typescript"
)

// Parse outlines one TypeScript or Python file. Generated files get only
// their line count. A syntax error fails the file, as a Go parse error fails
// its package.
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
		point := bad.StartPoint()
		return File{}, fmt.Errorf("%d:%d: syntax error", point.Row+1, point.Column+1)
	}

	w := walker{
		syntax:   syntax{grammar: grammar, src: src},
		name:     name,
		language: language,
		file:     &file,
	}
	w.walk(root, "")
	w.finish(root)
	w.finishTests()
	w.finishBlocks()

	return file, nil
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

const (
	commentNode     = "comment"
	tsCallNode      = "call_expression"
	tsFieldNode     = "public_field_definition"
	pyDecoratedNode = "decorated_definition"
	pyFunctionNode  = "function_definition"
	pyBlockNode     = "block"
	pyCallNode      = "call"
	tsAwaitNode     = "await_expression"
	statementNode   = "expression_statement"
	pyImportNode    = "import_from_statement"
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
	case "class_declaration", "abstract_class_declaration", "class":
		name := w.field(node, nameField)
		if name != nil {
			w.file.Types = append(w.file.Types, Type{Name: w.text(name), Pos: w.pos(name)})
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
	case "function_declaration", "generator_function_declaration", "method_definition":
		name := w.field(node, nameField)
		w.function(node, w.text(name), name, receiver)
	case "arrow_function", "function_expression", "generator_function":
		name, anchor, owner := w.boundName(node)
		w.function(node, name, anchor, owner)
	case tsCallNode, tsAwaitNode, "catch_clause":
		w.tsEvent(node, kind)
	}

	w.walkChildren(node, "")
}

var bindingFields = map[string][2]string{
	"variable_declarator":   {valueField, nameField},
	"pair":                  {valueField, "key"},
	"assignment_expression": {"right", "left"},
	tsFieldNode:             {valueField, nameField},
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
	case "export_statement":
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

func (w *walker) walkPython(node *ts.Node, kind, receiver string) {
	switch kind {
	case "class_definition":
		name := w.field(node, nameField)
		w.file.Types = append(w.file.Types, Type{Name: w.text(name), Pos: w.pos(name)})
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
	case pyCallNode, pyImportNode, "except_clause", "except_group_clause", "with_statement":
		w.pythonEvent(node, kind)
	case "lambda":
		w.function(node, "", nil, "")
	case "module":
		w.moduleDoc = w.docstring(node)
	}

	w.walkChildren(node, "")
}

// function records a function named at anchor; a nil anchor marks a
// literal.
func (w *walker) function(node *ts.Node, name string, anchor *ts.Node, receiver string) {
	fn := Func{
		Name:      name,
		Pos:       w.pos(node),
		StartLine: int(node.StartPoint().Row) + 1,
		EndLine:   w.endLine(node),
		Params:    w.params(node, receiver),
		Body:      true,
		Literal:   anchor == nil,
		Receiver:  receiver,
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

	for index := range list.NamedChildCount() {
		if !w.receiverOrSeparator(list.NamedChild(index), count == 0 && receiver != "") {
			count++
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
		return pattern != nil && pattern.Type(w.grammar) == "this"
	case "identifier":
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
			if child.NamedChildCount() != 1 || child.NamedChild(0).Type(w.grammar) != "string" {
				return nil
			}
		case "string":
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
