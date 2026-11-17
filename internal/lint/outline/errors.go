package outline

import (
	"strings"
	"unicode"

	ts "github.com/odvcencio/gotreesitter"
)

// Handler is one place where production code catches an error and may drop
// it: a TypeScript catch clause or promise .catch callback, or a Python
// except clause or contextlib.suppress block. Handlers that raise again on every
// path are left out; review judges the rest.
type Handler struct {
	Pos       Pos
	StartLine int
	EndLine   int
	// Unit is the outermost function around the handler, with the comment
	// group directly above it, or the top-level statement when no function
	// holds it. Review shows the model this range.
	Unit Decl
}

// handler records a caught-error site of a production file. It runs during
// the walk, before comment groups exist, so finish adds unit doc lines.
func (w *walker) handler(node *ts.Node, kind string) {
	anchor, body := node, (*ts.Node)(nil)

	switch kind {
	case "catch_clause":
		body = w.field(node, "body")
	case "except_clause", "except_group_clause":
		body = w.lastNamedChild(node, pyBlockNode)
	case tsCallNode:
		var ok bool
		if anchor, body, ok = w.promiseCatch(node); !ok {
			return
		}
	case "with_statement":
		if !w.suppressBlock(node) {
			return
		}
	}

	if body != nil && w.alwaysRaises(body) {
		return
	}

	w.file.Handlers = append(w.file.Handlers, Handler{
		Pos:       w.pos(anchor),
		StartLine: w.pos(anchor).Line,
		EndLine:   w.endLine(node),
		Unit:      w.unit(node),
	})
}

// promiseCatch matches p.catch(fn) and returns the catch name and the
// callback body. A capitalized receiver, as in Effect.catch, is a library
// namespace, not a promise.
func (s syntax) promiseCatch(call *ts.Node) (*ts.Node, *ts.Node, bool) {
	callee := s.field(call, "function")
	callback := s.soleArgument(call)
	receiver := s.text(s.field(callee, "object"))

	if s.text(s.field(callee, "property")) != "catch" ||
		receiver != "" && unicode.IsUpper([]rune(receiver)[0]) &&
			!strings.ContainsAny(receiver, ".()") ||
		callback == nil || !tsFunctionKinds[callback.Type(s.grammar)] {
		return nil, nil, false
	}

	return s.field(callee, "property"), s.field(callback, "body"), true
}

var tsFunctionKinds = map[string]bool{
	"function_declaration":           true,
	"generator_function_declaration": true,
	"method_definition":              true,
	"arrow_function":                 true,
	"function_expression":            true,
	"generator_function":             true,
}

// unitWrappers are statements that only bind or export the function they
// hold, so its unit grows to include them.
var unitWrappers = map[string]bool{
	"variable_declarator":  true,
	"lexical_declaration":  true,
	"variable_declaration": true,
	"export_statement":     true,
	tsFieldNode:            true,
	pyDecoratedNode:        true,
}

// unit returns the lines of the outermost function around node, or of its
// top-level statement.
func (s syntax) unit(node *ts.Node) Decl {
	var outer, top *ts.Node

	for current := node; current != nil && current.Parent() != nil; current = current.Parent() {
		kind := current.Type(s.grammar)
		if tsFunctionKinds[kind] || kind == pyFunctionNode {
			outer = current
		}

		top = current
	}

	if outer == nil {
		outer = top
	}

	for parent := outer.Parent(); parent != nil && unitWrappers[parent.Type(s.grammar)]; parent = parent.Parent() {
		outer = parent
	}

	return Decl{StartLine: s.pos(outer).Line, EndLine: s.endLine(outer)}
}

func (s syntax) soleArgument(call *ts.Node) *ts.Node {
	args := s.field(call, "arguments")
	if args == nil || args.NamedChildCount() != 1 {
		return nil
	}

	return args.NamedChild(0)
}

func (s syntax) lastNamedChild(node *ts.Node, kind string) *ts.Node {
	for index := node.NamedChildCount() - 1; index >= 0; index-- {
		if child := node.NamedChild(index); child.Type(s.grammar) == kind {
			return child
		}
	}

	return nil
}

func (s syntax) suppressBlock(with *ts.Node) bool {
	return s.contains(with, func(node *ts.Node) bool {
		return node.Type(s.grammar) == pyBlockNode
	}, func(node *ts.Node) bool {
		return node.Type(s.grammar) == pyCallNode && lastName(s.chain(node)) == "suppress"
	})
}

// alwaysRaises reports a block that raises or exits on every path: some
// statement raises before any statement could leave the block another way.
// It is syntactic and conservative; anything unclear counts as not raising.
func (s syntax) alwaysRaises(block *ts.Node) bool {
	for index := range block.NamedChildCount() {
		statement := block.NamedChild(index)
		if s.raises(statement) {
			return true
		}

		if s.jumpsOutsideNestedFunctions(statement) {
			return false
		}
	}

	return false
}

func (s syntax) raises(statement *ts.Node) bool {
	switch statement.Type(s.grammar) {
	case "throw_statement", "raise_statement":
		return true
	case "statement_block", pyBlockNode:
		return s.alwaysRaises(statement)
	case statementNode:
		return statement.NamedChildCount() == 1 && s.raises(statement.NamedChild(0))
	case pyCallNode, tsCallNode:
		return s.exitCall(statement)
	case "if_statement":
		return s.ifRaises(statement)
	default:
		return false
	}
}

// ifRaises needs an else branch: without one the false path falls through.
func (s syntax) ifRaises(statement *ts.Node) bool {
	if !s.raises(s.field(statement, "consequence")) {
		return false
	}

	hasElse := false

	for index := range statement.NamedChildCount() {
		branch := statement.NamedChild(index)

		switch branch.Type(s.grammar) {
		case "else_clause":
			hasElse = true

			body := s.field(branch, "body")
			if body == nil {
				body = branch.NamedChild(branch.NamedChildCount() - 1)
			}

			if !s.raises(body) {
				return false
			}
		case "elif_clause":
			if !s.raises(s.field(branch, "consequence")) {
				return false
			}
		}
	}

	return hasElse
}

// exitCall matches process.exit, sys.exit, and os._exit with a status that is
// not a literal zero: the exit code tells the caller about the failure.
func (s syntax) exitCall(call *ts.Node) bool {
	switch s.text(s.field(call, "function")) {
	case "process.exit", "sys.exit", "os._exit":
	default:
		return false
	}

	status := s.soleArgument(call)

	return status != nil && s.text(status) != "0"
}

func (s syntax) jumpsOutsideNestedFunctions(statement *ts.Node) bool {
	return s.contains(statement, func(node *ts.Node) bool {
		kind := node.Type(s.grammar)
		return tsFunctionKinds[kind] || kind == pyFunctionNode || kind == "lambda"
	}, func(node *ts.Node) bool {
		switch node.Type(s.grammar) {
		case "return_statement", "break_statement", "continue_statement":
			return true
		default:
			return false
		}
	})
}

func (s syntax) contains(node *ts.Node, skip, match func(*ts.Node) bool) bool {
	if node == nil || !node.IsNamed() || skip(node) {
		return false
	}

	if match(node) {
		return true
	}

	for index := range node.NamedChildCount() {
		if s.contains(node.NamedChild(index), skip, match) {
			return true
		}
	}

	return false
}
