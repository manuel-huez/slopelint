package outline

import (
	"strings"

	ts "github.com/odvcencio/gotreesitter"
)

// statements counts the statements under node: every node directly in a
// block, and *_statement nodes outside one, such as an unbraced if body or
// a switch case. The Python grammar puts bare expressions such as
// assignments in blocks, so node types alone would miss them.
func (w *walker) statements(node *ts.Node) int {
	count := 0
	inBlock := node.Type(w.grammar) == pyBlockNode || node.Type(w.grammar) == tsBlockNode

	for index := range node.NamedChildCount() {
		child := node.NamedChild(index)
		if kind := child.Type(w.grammar); kind != commentNode &&
			(inBlock || strings.HasSuffix(kind, "_statement")) {
			count += w.statementSize(child)
		} else {
			count += w.statements(child)
		}
	}

	return count
}

// statementSize counts node and the statements it holds. A switch or match
// counts one per case plus its longest case: a table of short cases reads
// case by case.
func (w *walker) statementSize(node *ts.Node) int {
	kind := node.Type(w.grammar)
	if kind != tsSwitchNode && kind != "match_statement" {
		return 1 + w.statements(node)
	}

	body := w.field(node, "body")
	if body == nil {
		return 1 + w.statements(node)
	}

	cases, longest := 0, 0

	for index := range body.NamedChildCount() {
		if clause := body.NamedChild(index); clause.Type(w.grammar) != commentNode {
			cases++
			longest = max(longest, w.caseStatements(clause))
		}
	}

	return 1 + cases + longest
}

func (w *walker) caseStatements(clause *ts.Node) int {
	if body := w.field(clause, "consequence"); body != nil {
		return w.statements(body)
	}

	value := w.field(clause, "value")
	count := 0

	for index := range clause.NamedChildCount() {
		if child := clause.NamedChild(
			index,
		); child != value &&
			child.Type(w.grammar) != commentNode {
			count += w.statementSize(child)
		}
	}

	return count
}

func (w *walker) largestStatements(node *ts.Node) []StatementBlock {
	body := w.field(node, "body")
	if body == nil {
		return nil
	}

	var blocks []StatementBlock

	for index := range body.NamedChildCount() {
		if child := body.NamedChild(index); child.Type(w.grammar) != commentNode {
			blocks = append(blocks, StatementBlock{
				Line:       int(child.StartPoint().Row) + 1,
				Statements: w.statementSize(child),
			})
		}
	}

	return LargestStatements(blocks)
}

// movableClosures returns the closures in a named function that use no name
// the function declares, so they can move out of it.
func (w *walker) movableClosures(node, anchor *ts.Node) []StatementBlock {
	body := w.field(node, "body")
	if anchor == nil || body == nil {
		return nil
	}

	declared := make(map[string]bool)
	w.declaredNames(node, declared)

	var closures []StatementBlock

	var visit func(*ts.Node)

	visit = func(current *ts.Node) {
		for index := range current.NamedChildCount() {
			child := current.NamedChild(index)
			if !w.closureNode(child) {
				visit(child)
				continue
			}

			if w.usesAny(child, w.field(child, nameField), declared) {
				visit(child)
				continue
			}

			closures = append(closures, StatementBlock{
				Line:       int(child.StartPoint().Row) + 1,
				Statements: w.statements(child),
			})
		}
	}

	visit(body)

	return LargestStatements(closures)
}

func (w *walker) closureNode(node *ts.Node) bool {
	switch node.Type(w.grammar) {
	case tsArrowNode, tsFuncExprNode, tsGeneratorExpr, pyLambdaNode, pyFunctionNode:
		return true
	default:
		return false
	}
}

// declaredNames collects the parameter and local names of a function,
// without those of the closures inside it.
func (w *walker) declaredNames(node *ts.Node, names map[string]bool) {
	if params := w.field(node, "parameters"); params != nil {
		w.identifiers(params, names)
	}

	var visit func(*ts.Node)

	visit = func(current *ts.Node) {
		for index := range current.NamedChildCount() {
			child := current.NamedChild(index)

			switch child.Type(w.grammar) {
			case tsDeclaratorNode, pyFunctionNode, tsFuncDeclNode, pyClassNode:
				if name := w.field(child, nameField); name != nil {
					w.identifiers(name, names)
				}
			case pyAssignNode, "augmented_assignment", forNode, tsForInNode:
				if left := w.field(child, "left"); left != nil {
					w.identifiers(left, names)
				}
			}

			if !w.closureNode(child) {
				visit(child)
			}
		}
	}

	visit(w.field(node, "body"))
}

func (w *walker) identifiers(node *ts.Node, names map[string]bool) {
	if node.Type(w.grammar) == identifierNode {
		names[w.text(node)] = true
	}

	for index := range node.NamedChildCount() {
		w.identifiers(node.NamedChild(index), names)
	}
}

// usesAny reports whether node uses one of names, outside skip, the name of
// a nested def.
func (w *walker) usesAny(node, skip *ts.Node, names map[string]bool) bool {
	if node == skip {
		return false
	}

	if node.Type(w.grammar) == identifierNode && names[w.text(node)] {
		return true
	}

	for index := range node.NamedChildCount() {
		if w.usesAny(node.NamedChild(index), skip, names) {
			return true
		}
	}

	return false
}
