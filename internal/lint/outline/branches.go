package outline

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	ts "github.com/odvcencio/gotreesitter"
)

// Smell is one structural finding in a TypeScript or Python file. Each
// rule follows the Go rule of the same kind, narrowed to what syntax alone
// proves: without types, a condition or alias counts as safe only when no
// evaluation it drops or moves can run code.
type Smell struct {
	Pos     Pos
	Kind    string
	Message string
}

const (
	kindControlFlowMerge = "control_flow_merge"
	// pyramidMinDepth matches the Go control_flow_pyramid rule.
	pyramidMinDepth = 2
)

// checker holds the comments and findings that branchRules and localRules
// share for one file.
type checker struct {
	syntax

	language Language
	comments []rawComment
	smells   []Smell
	helpers  []SplitHelper
}

type branchRules struct{ *checker }

func (w *walker) finishSmells(root *ts.Node) {
	c := &checker{syntax: w.syntax, language: w.language, comments: w.comments}
	c.walk(root)

	// Python has no file-local functions: other modules import, and tests
	// patch, even underscore names.
	if !w.file.Test && w.language == TypeScript {
		localRules{c}.functions(root)
	}

	w.file.Smells = append(w.file.Smells, c.smells...)
	w.file.SplitHelpers = c.helpers
}

func (c *checker) walk(node *ts.Node) {
	if !node.IsNamed() {
		return
	}

	switch node.Type(c.grammar) {
	case "program", pyModuleNode, tsBlockNode, pyBlockNode, tsCaseNode, tsDefaultNode:
		stmts := c.statements(node)
		for index := range stmts {
			branchRules{c}.list(stmts, index)
			localRules{c}.tempAlias(node, stmts, index)
		}
	case ifNode:
		branchRules{c}.ifChain(node)
	case tsSwitchNode:
		branchRules{c}.switchCases(node)
	}

	for index := range node.NamedChildCount() {
		c.walk(node.NamedChild(index))
	}
}

func (s syntax) statements(list *ts.Node) []*ts.Node {
	var out []*ts.Node

	value := s.field(list, valueField)

	for index := range list.NamedChildCount() {
		child := list.NamedChild(index)
		if child.Type(s.grammar) != commentNode && child != value {
			out = append(out, child)
		}
	}

	return out
}

func (s syntax) branchStatements(branch *ts.Node) []*ts.Node {
	switch branch.Type(s.grammar) {
	case tsBlockNode, pyBlockNode:
		return s.statements(branch)
	default:
		return []*ts.Node{branch}
	}
}

// key renders nodes as their tokens joined by spaces, comments included,
// so layout differences compare equal and comment differences do not. A
// node with text outside its children, such as string content, is one
// token.
func (s syntax) key(nodes []*ts.Node) string {
	var out strings.Builder

	var add func(*ts.Node)

	add = func(node *ts.Node) {
		if node.ChildCount() == 0 || s.hiddenText(node) {
			if out.Len() > 0 {
				out.WriteByte(' ')
			}

			out.WriteString(s.text(node))

			return
		}

		for index := range node.ChildCount() {
			add(node.Child(index))
		}
	}

	for _, node := range nodes {
		add(node)
	}

	return out.String()
}

func (s syntax) hiddenText(node *ts.Node) bool {
	offset := node.StartByte()

	for index := range node.ChildCount() {
		child := node.Child(index)
		if strings.TrimSpace(string(s.src[offset:child.StartByte()])) != "" {
			return true
		}

		offset = child.EndByte()
	}

	return strings.TrimSpace(string(s.src[offset:node.EndByte()])) != ""
}

// commentsIn relies on comments sorted by start, as commentGroups leaves
// them, and searches so each rule's check stays logarithmic in the file.
func (c *checker) commentsIn(start, end uint32) string {
	var out strings.Builder

	first, _ := slices.BinarySearchFunc(
		c.comments,
		start,
		func(comment rawComment, start uint32) int {
			return cmp.Compare(comment.start, start)
		},
	)

	for _, comment := range c.comments[first:] {
		if comment.end > end {
			break
		}

		out.Write(c.src[comment.start:comment.end])
		out.WriteByte('\n')
	}

	return out.String()
}

func (s syntax) unparen(cond *ts.Node) *ts.Node {
	if cond.Type(s.grammar) == tsParenNode && cond.NamedChildCount() == 1 {
		return cond.NamedChild(0)
	}

	return cond
}

func (s syntax) oneLine(node *ts.Node) string {
	return strings.Join(strings.Fields(s.text(node)), " ")
}

func (c *checker) attachedComment(node *ts.Node) bool {
	start := c.pos(node).Line

	first := c.commentFrom(start)
	if first > 0 && c.comments[first-1].endLine == start-1 {
		return true
	}

	return first < len(c.comments) && c.comments[first].pos.Line <= c.endLine(node)
}

func (c *checker) commentFrom(line int) int {
	first, _ := slices.BinarySearchFunc(c.comments, line, func(comment rawComment, line int) int {
		return cmp.Compare(comment.pos.Line, line)
	})

	return first
}

func (c *checker) report(node *ts.Node, kind, message string) {
	c.smells = append(c.smells, Smell{Pos: c.pos(node), Kind: kind, Message: message})
}

// branch is one arm of an if chain; cond is nil for the final else.
type branch struct {
	keyword *ts.Node
	cond    *ts.Node
	body    *ts.Node
}

// arms flattens `if/else if/else` and `if/elif/else`. It returns nil for an
// if nested in an else, which its outer chain covers.
func (c branchRules) arms(stmt *ts.Node) []branch {
	if parent := stmt.Parent(); parent != nil && parent.Type(c.grammar) == elseNode {
		return nil
	}

	out := []branch{c.conditionalArm(stmt)}

	for current := stmt; current != nil; {
		var next *ts.Node

		for index := range current.NamedChildCount() {
			child := current.NamedChild(index)

			switch child.Type(c.grammar) {
			case pyElifNode:
				out = append(out, c.conditionalArm(child))
			case elseNode:
				body := c.field(child, "body")
				if body == nil {
					body = child.NamedChild(child.NamedChildCount() - 1)
				}

				if body.Type(c.grammar) == ifNode {
					next = body
					out = append(out, c.conditionalArm(body))
				} else {
					out = append(out, branch{keyword: child, body: body})
				}
			}
		}

		current = next
	}

	return out
}

func (c branchRules) conditionalArm(node *ts.Node) branch {
	return branch{
		keyword: node,
		cond:    c.field(node, "condition"),
		body:    c.field(node, "consequence"),
	}
}

// armComments runs from the end of the previous arm, so a comment above an
// else-if belongs to it: tree-sitter can attach it to the clause, not the
// block.
func (c branchRules) armComments(arms []branch, index int) string {
	start := arms[index].keyword.StartByte()
	if index > 0 {
		start = arms[index-1].body.EndByte()
	}

	return c.commentsIn(start, arms[index].body.EndByte())
}

// ifChain reports adjacent arms of one if chain with identical bodies.
// A plain if/else is the Go control_flow_merge case. Adjacent else-if arms
// are the script form of Go's adjacent switch cases: `if (a) X else if (b) X`
// runs X exactly when `a || b` does, evaluating the same conditions.
func (c branchRules) ifChain(stmt *ts.Node) {
	arms := c.arms(stmt)

	for index := 1; index < len(arms); index++ {
		prior, arm := arms[index-1], arms[index]
		priorStmts, stmts := c.branchStatements(prior.body), c.branchStatements(arm.body)

		same := len(stmts) > 0 && len(stmts) == len(priorStmts) && c.key(priorStmts) == c.key(stmts)
		if !same || c.armComments(arms, index-1) != c.armComments(arms, index) {
			continue
		}

		switch {
		case arm.cond != nil:
			c.report(arm.keyword, kindControlFlowMerge, fmt.Sprintf(
				"branch %q has identical body as previous branch %q; join the conditions",
				c.oneLine(c.unparen(arm.cond)), c.oneLine(c.unparen(prior.cond)),
			))
		case len(arms) == 2 && !c.declaresNames(stmts):
			c.report(
				stmt,
				kindControlFlowMerge,
				"if and else branches are identical; preserve condition evaluation and hoist shared body",
			)
		}
	}
}

// declaresNames reports a TypeScript block-scoped declaration, whose scope
// changes when the body is hoisted out of the branch.
func (c branchRules) declaresNames(stmts []*ts.Node) bool {
	for _, stmt := range stmts {
		switch stmt.Type(c.grammar) {
		case tsLexicalNode, tsVarNode, tsFuncDeclNode, tsClassNode, tsGeneratorNode:
			return true
		}
	}

	return false
}

// switchCases reports a case whose body equals the previous case's. A
// body that does not end in a jump falls through, so it never merges.
func (c branchRules) switchCases(stmt *ts.Node) {
	body := c.field(stmt, "body")
	if body == nil {
		return
	}

	var prior *ts.Node

	for index := range body.NamedChildCount() {
		clause := body.NamedChild(index)

		stmts := c.statements(clause)
		if len(stmts) == 0 || !c.jump(stmts[len(stmts)-1]) {
			prior = nil
			continue
		}

		if prior != nil && c.key(c.statements(prior)) == c.key(stmts) &&
			c.commentsIn(prior.StartByte(), prior.EndByte()) ==
				c.commentsIn(clause.StartByte(), clause.EndByte()) {
			c.report(clause, kindControlFlowMerge, fmt.Sprintf(
				"switch case %q has identical body as previous case %q; merge case lists",
				c.caseHeader(clause), c.caseHeader(prior),
			))
		}

		prior = clause
	}
}

func (c branchRules) caseHeader(clause *ts.Node) string {
	if value := c.field(clause, valueField); value != nil {
		return c.oneLine(value)
	}

	return "default"
}

func (s syntax) jump(stmt *ts.Node) bool {
	switch stmt.Type(s.grammar) {
	case "break_statement", "continue_statement", returnNode, "throw_statement":
		return true
	default:
		return false
	}
}

func (c branchRules) list(stmts []*ts.Node, index int) {
	c.returnGuardRun(stmts, index)
	c.boolChoice(stmts, index)

	if index == len(stmts)-1 {
		c.pyramid(stmts[index].Parent(), stmts[index])
	}
}

func (c branchRules) bareIf(stmt *ts.Node) (*ts.Node, []*ts.Node, bool) {
	if stmt.Type(c.grammar) != ifNode || c.attachedComment(stmt) {
		return nil, nil, false
	}

	for index := range stmt.NamedChildCount() {
		switch stmt.NamedChild(index).Type(c.grammar) {
		case elseNode, pyElifNode:
			return nil, nil, false
		}
	}

	return c.field(stmt, "condition"), c.branchStatements(c.field(stmt, "consequence")), true
}

// returnKey renders a return statement's value; ok is false for other
// statements.
func (c *checker) returnKey(stmt *ts.Node) (string, bool) {
	if stmt.Type(c.grammar) != returnNode {
		return "", false
	}

	var values []*ts.Node

	for index := range stmt.NamedChildCount() {
		if child := stmt.NamedChild(index); child.Type(c.grammar) != commentNode {
			values = append(values, child)
		}
	}

	return c.key(values), true
}

func (c branchRules) returnGuard(stmts []*ts.Node, index int) (string, bool) {
	if index < 0 || index >= len(stmts) {
		return "", false
	}

	cond, body, ok := c.bareIf(stmts[index])
	if !ok || len(body) != 1 || !c.safeCondition(cond) {
		return "", false
	}

	return c.returnKey(body[0])
}

// returnGuardRun reports guards that all return what the return right
// after them returns, as the Go rule does; it reports a run once, at its
// first guard.
func (c branchRules) returnGuardRun(stmts []*ts.Node, index int) {
	first, ok := c.returnGuard(stmts, index)
	if !ok {
		return
	}

	if prior, ok := c.returnGuard(stmts, index-1); ok && prior == first {
		return
	}

	for scan := index + 1; scan < len(stmts); scan++ {
		if key, ok := c.returnKey(stmts[scan]); ok {
			if key == first {
				c.report(stmts[index], kindControlFlowMerge, fmt.Sprintf(
					`%d guard return(s) duplicate following return %q; drop redundant branch checks`,
					scan-index,
					strings.TrimSuffix(c.oneLine(stmts[scan]), ";"),
				))
			}

			return
		}

		if key, ok := c.returnGuard(stmts, scan); !ok || key != first {
			return
		}
	}
}

// safeCondition reports a condition whose evaluation runs no user code on
// plain values, so a guard on it can be dropped: names, literals, negation,
// `&&`/`||`/and/or, and comparisons. TypeScript allows strict equality,
// loose equality against null or undefined, and ordering against a number
// literal. Python allows `is`, and other comparisons against a literal;
// `in` is excluded. Ordering and Python equality could call a user method
// on an object, but against a literal the other side is a primitive in
// practice; truthiness that raises, as for numpy arrays, would already break
// the guard. Member reads, calls, and subscripts are excluded: a getter or
// property can run code.
func (c branchRules) safeCondition(node *ts.Node) bool {
	switch node.Type(c.grammar) {
	case tsParenNode:
		return node.NamedChildCount() == 1 && c.safeCondition(node.NamedChild(0))
	case tsUnaryNode:
		return c.text(c.field(node, "operator")) == "!" &&
			c.safeCondition(c.field(node, "argument"))
	case pyNotNode:
		return c.safeCondition(c.field(node, "argument"))
	case pyBoolNode:
		return c.safeCondition(c.field(node, "left")) && c.safeCondition(c.field(node, "right"))
	case tsBinaryNode:
		return c.safeBinary(node)
	case pyCompareNode:
		return c.safeComparison(node)
	default:
		return c.safeOperand(node)
	}
}

func (c branchRules) safeBinary(node *ts.Node) bool {
	left, right := c.field(node, "left"), c.field(node, "right")

	operator := c.text(c.field(node, "operator"))
	if operator == "&&" || operator == "||" {
		return c.safeCondition(left) && c.safeCondition(right)
	}

	if !c.safeOperand(left) || !c.safeOperand(right) {
		return false
	}

	switch operator {
	case "===", "!==":
		return true
	case "==", "!=":
		return c.nullish(left) || c.nullish(right)
	case "<", "<=", ">", ">=":
		return left.Type(c.grammar) == tsNumberNode || right.Type(c.grammar) == tsNumberNode
	default:
		return false
	}
}

func (c branchRules) nullish(node *ts.Node) bool {
	return node.Type(c.grammar) == "null" || c.text(node) == "undefined"
}

// safeComparison allows a Python comparison of names and literals that is
// an identity test or has a literal operand. `in` calls __contains__.
func (c branchRules) safeComparison(node *ts.Node) bool {
	literal, identity := false, true

	for index := range node.ChildCount() {
		child := node.Child(index)
		operator := c.text(child)

		switch {
		case child.IsNamed():
			if !c.safeOperand(child) {
				return false
			}

			literal = literal || child.Type(c.grammar) != identifierNode
		case strings.Contains(operator, "in"):
			return false
		case !strings.HasPrefix(operator, "is") && operator != "not":
			identity = false
		}
	}

	return identity || literal
}

func (c branchRules) safeOperand(node *ts.Node) bool {
	switch node.Type(c.grammar) {
	case identifierNode, tsThisNode, trueNode, falseNode, "null", "undefined", "none",
		tsNumberNode, "integer", "float":
		return true
	case stringNode:
		// A Python f-string runs code; TypeScript templates are another type.
		return !c.contains(node, func(*ts.Node) bool { return false }, func(child *ts.Node) bool {
			return child.Type(c.grammar) == "interpolation"
		})
	default:
		return false
	}
}

func (c branchRules) boolChoice(stmts []*ts.Node, index int) {
	if stmts[index].Type(c.grammar) != ifNode {
		return
	}

	arms := c.arms(stmts[index])

	other, otherComments, ok := c.otherBoolArm(arms, stmts, index)
	if !ok {
		return
	}

	thenTarget, thenValue, ok := c.boolAction(c.branchStatements(arms[0].body))
	if !ok {
		return
	}

	otherTarget, otherValue, ok := c.boolAction(other)
	if !ok || thenTarget != otherTarget || thenValue == otherValue ||
		c.armComments(arms, 0) != otherComments {
		return
	}

	prefix, verb := "return ", "returns"
	if thenTarget != "" {
		prefix, verb = thenTarget+" = ", "assigns"
	}

	replacement := c.boolText(arms[0].cond)
	if !thenValue {
		replacement = c.negatedText(arms[0].cond)
	}

	c.report(stmts[index], "boolean_ceremony", fmt.Sprintf(
		`if %s boolean literals; replace with %q`, verb, prefix+replacement,
	))
}

// otherBoolArm returns what runs when the if's condition is false, with its
// comments: the else arm, or the return after an if without one. Unlike
// the Go rule, a comment above that return blocks the finding: it explains
// that return, and the rewrite would drop it.
func (c branchRules) otherBoolArm(
	arms []branch,
	stmts []*ts.Node,
	index int,
) ([]*ts.Node, string, bool) {
	switch {
	case len(arms) == 2 && arms[1].cond == nil:
		return c.branchStatements(arms[1].body), c.armComments(arms, 1), true
	case len(arms) == 1 && index+1 < len(stmts) && stmts[index+1].Type(c.grammar) == returnNode &&
		!c.attachedComment(stmts[index+1]):
		next := stmts[index+1]

		return []*ts.Node{next}, c.commentsIn(next.StartByte(), next.EndByte()), true
	default:
		return nil, "", false
	}
}

func (c branchRules) boolText(cond *ts.Node) string {
	cond = c.unparen(cond)
	text := c.oneLine(cond)

	switch {
	case c.alwaysBoolean(cond):
		return text
	case c.language == Python:
		return "bool(" + text + ")"
	default:
		return "Boolean(" + text + ")"
	}
}

func (c branchRules) negatedText(cond *ts.Node) string {
	cond = c.unparen(cond)
	if negated := c.field(cond, "argument"); negated != nil &&
		(cond.Type(c.grammar) == pyNotNode || c.text(c.field(cond, "operator")) == "!") {
		return c.boolText(negated)
	}

	if c.language == Python {
		return "not (" + c.oneLine(cond) + ")"
	}

	return "!(" + c.oneLine(cond) + ")"
}

func (c branchRules) alwaysBoolean(node *ts.Node) bool {
	switch node.Type(c.grammar) {
	case tsParenNode:
		return c.alwaysBoolean(node.NamedChild(0))
	case tsUnaryNode:
		return c.text(c.field(node, "operator")) == "!"
	case pyNotNode, trueNode, falseNode:
		return true
	case pyBoolNode:
		return c.alwaysBoolean(c.field(node, "left")) && c.alwaysBoolean(c.field(node, "right"))
	case pyCompareNode:
		return c.membershipTest(node)
	case tsBinaryNode:
		switch c.text(c.field(node, "operator")) {
		case "&&", "||":
			return c.alwaysBoolean(c.field(node, "left")) && c.alwaysBoolean(c.field(node, "right"))
		case "===", "!==", "==", "!=", "<", "<=", ">", ">=", "instanceof", "in":
			return true
		}
	}

	return false
}

// membershipTest matches Python `is` and `in` tests, which yield a bool;
// other comparisons can yield other values, as numpy's do.
func (c branchRules) membershipTest(node *ts.Node) bool {
	for index := range node.ChildCount() {
		if child := node.Child(index); !child.IsNamed() {
			switch c.text(child) {
			case "is", "in", "not", "is not", "not in":
			default:
				return false
			}
		}
	}

	return true
}

// boolAction matches one statement that returns or assigns a boolean
// literal; target is empty for a return.
func (c branchRules) boolAction(stmts []*ts.Node) (string, bool, bool) {
	if len(stmts) != 1 {
		return "", false, false
	}

	var (
		target string
		value  *ts.Node
	)

	if stmts[0].Type(c.grammar) == returnNode {
		if stmts[0].NamedChildCount() != 1 {
			return "", false, false
		}

		value = stmts[0].NamedChild(0)
	} else {
		left, right, ok := c.assignment(stmts[0])
		if !ok || c.callIn(left) || left.Type(c.grammar) == "pattern_list" {
			return "", false, false
		}

		target, value = c.oneLine(left), right
	}

	switch value.Type(c.grammar) {
	case trueNode:
		return target, true, true
	case falseNode:
		return target, false, true
	default:
		return "", false, false
	}
}

func (s syntax) assignment(stmt *ts.Node) (*ts.Node, *ts.Node, bool) {
	assign := stmt
	if stmt.Type(s.grammar) == statementNode && stmt.NamedChildCount() == 1 {
		assign = stmt.NamedChild(0)
	}

	kind := assign.Type(s.grammar)
	left, right := s.field(assign, "left"), s.field(assign, "right")

	return left, right, (kind == tsAssignNode || kind == pyAssignNode) && left != nil &&
		right != nil && s.field(assign, "type") == nil
}

func (s syntax) callIn(node *ts.Node) bool {
	return s.contains(node, func(*ts.Node) bool { return false }, func(node *ts.Node) bool {
		kind := node.Type(s.grammar)
		return kind == tsCallNode || kind == pyCallNode
	})
}

// pyramid reports nested ifs that end a function without a result, as the
// Go control_flow_pyramid rule does.
func (c branchRules) pyramid(list, last *ts.Node) {
	fn := list.Parent()
	if fn == nil || !c.mayLackResults(fn) || c.returnsValue(list) {
		return
	}

	depth := 0

	for current := last; ; {
		cond, body, ok := c.bareIf(current)
		if cond == nil || !ok || len(body) == 0 {
			return
		}

		depth++

		if len(body) != 1 || body[0].Type(c.grammar) != ifNode {
			break
		}

		current = body[0]
	}

	if depth >= pyramidMinDepth {
		c.report(last, "control_flow_pyramid", fmt.Sprintf(
			`nested if pyramid has %d levels at function end; invert conditions into guard clauses`,
			depth,
		))
	}
}

// mayLackResults leaves out generators and getters, which always produce
// values.
func (c branchRules) mayLackResults(fn *ts.Node) bool {
	switch fn.Type(c.grammar) {
	case pyFunctionNode, tsFuncDeclNode, tsArrowNode, tsFuncExprNode:
		return true
	case tsMethodNode:
		return c.text(fn.Child(0)) != "get"
	default:
		return false
	}
}

// returnsValue reports a return with a value or a yield in body, outside
// nested functions and classes.
func (c branchRules) returnsValue(body *ts.Node) bool {
	return c.contains(body, func(node *ts.Node) bool {
		kind := node.Type(c.grammar)

		return node != body && (tsFunctionKinds[kind] || kind == pyFunctionNode ||
			kind == pyLambdaNode || kind == tsClassNode || kind == pyClassNode)
	}, func(node *ts.Node) bool {
		switch node.Type(c.grammar) {
		case returnNode:
			return node.NamedChildCount() > 0 &&
				node.NamedChild(0).Type(c.grammar) != commentNode
		case "yield", tsYieldNode:
			return true
		default:
			return false
		}
	})
}
