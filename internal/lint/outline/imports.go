package outline

import (
	"strings"

	ts "github.com/odvcencio/gotreesitter"
)

const (
	tsImportNode         = "import_statement"
	pyPlainImportNode    = "import_statement"
	pyRelativeImportNode = "relative_import"
	pyImportPrefixNode   = "import_prefix"
	pyDottedNameNode     = "dotted_name"
	pyAliasedImportNode  = "aliased_import"
	quotedPairLen        = 2
)

type importCollector struct {
	s       syntax
	lang    Language
	imports []Import
}

func collectImports(s syntax, lang Language, root *ts.Node) []Import {
	collector := importCollector{s: s, lang: lang}
	collector.walk(root)

	return collector.imports
}

func (c *importCollector) walk(node *ts.Node) {
	if !node.IsNamed() {
		return
	}

	kind := node.Type(c.s.grammar)

	if c.lang == Python {
		if kind == pyPlainImportNode || kind == pyImportNode {
			c.pythonImport(node, kind)
			return
		}
	} else if kind == tsImportNode || kind == tsExportNode {
		c.tsImport(node)
	}

	for index := range node.ChildCount() {
		c.walk(node.Child(index))
	}
}

func (c *importCollector) tsImport(node *ts.Node) {
	if c.isTSTypeOnlyImport(node) {
		return
	}

	source := c.s.field(node, "source")
	if source == nil {
		return
	}

	spec := unquoteImportPath(c.s.text(source))
	if spec == "" {
		return
	}

	c.imports = append(c.imports, Import{
		Pos:  c.s.pos(node),
		Path: spec,
	})
}

func (c *importCollector) isTSTypeOnlyImport(node *ts.Node) bool {
	return c.hasTypeKeyword(node) || c.allSpecifiersTypeOnly(node)
}

func (c *importCollector) allSpecifiersTypeOnly(node *ts.Node) bool {
	var (
		specifiers int
		typed      int
	)

	for index := range node.NamedChildCount() {
		child := node.NamedChild(index)
		switch child.Type(c.s.grammar) {
		case "import_clause", "export_clause":
			if !c.countTypeSpecifiers(child, &specifiers, &typed) {
				return false
			}
		}
	}

	return specifiers > 0 && specifiers == typed
}

func (c *importCollector) countTypeSpecifiers(node *ts.Node, total, typed *int) bool {
	for index := range node.NamedChildCount() {
		child := node.NamedChild(index)
		switch child.Type(c.s.grammar) {
		case "named_imports":
			if !c.countTypeSpecifiers(child, total, typed) {
				return false
			}
		case "import_specifier", "export_specifier":
			*total++

			if c.hasTypeKeyword(child) {
				*typed++
			}
		default:
			return false
		}
	}

	return true
}

func (c *importCollector) hasTypeKeyword(node *ts.Node) bool {
	for index := range node.ChildCount() {
		child := node.Child(index)
		if !child.IsNamed() && c.s.text(child) == "type" {
			return true
		}
	}

	return false
}

func (c *importCollector) pythonImport(node *ts.Node, kind string) {
	if c.inTypeCheckingGuard(node) {
		return
	}

	if kind == pyPlainImportNode {
		c.pythonPlainImport(node)
		return
	}

	c.pythonFromImport(node)
}

func (c *importCollector) pythonPlainImport(node *ts.Node) {
	pos := c.s.pos(node)

	for index := range node.NamedChildCount() {
		child := node.NamedChild(index)

		var target string

		switch child.Type(c.s.grammar) {
		case pyDottedNameNode:
			target = c.s.text(child)
		case pyAliasedImportNode:
			target = c.s.text(c.s.field(child, nameField))
		}

		if target != "" {
			c.imports = append(c.imports, Import{Pos: pos, Path: target})
		}
	}
}

func (c *importCollector) pythonFromImport(node *ts.Node) {
	moduleNode := c.s.field(node, "module_name")
	if moduleNode == nil {
		return
	}

	imp, ok := c.pythonFromModule(node, moduleNode)
	if !ok {
		return
	}

	for index := range node.NamedChildCount() {
		child := node.NamedChild(index)
		if child == moduleNode {
			continue
		}

		if name := c.pythonImportedName(child); name != "" {
			imp.Names = append(imp.Names, name)
		}
	}

	c.imports = append(c.imports, imp)
}

func (c *importCollector) pythonFromModule(node, moduleNode *ts.Node) (Import, bool) {
	imp := Import{Pos: c.s.pos(node)}

	switch moduleNode.Type(c.s.grammar) {
	case pyRelativeImportNode:
		for index := range moduleNode.NamedChildCount() {
			part := moduleNode.NamedChild(index)
			switch part.Type(c.s.grammar) {
			case pyImportPrefixNode:
				imp.Relative = strings.Count(c.s.text(part), ".")
			case pyDottedNameNode:
				imp.Path = c.s.text(part)
			}
		}

		return imp, true
	case pyDottedNameNode:
		imp.Path = c.s.text(moduleNode)
		return imp, true
	default:
		return Import{}, false
	}
}

func (c *importCollector) pythonImportedName(node *ts.Node) string {
	switch node.Type(c.s.grammar) {
	case pyDottedNameNode, identifierNode:
		return c.s.text(node)
	case pyAliasedImportNode:
		return c.s.text(c.s.field(node, nameField))
	default:
		return ""
	}
}

func (c *importCollector) inTypeCheckingGuard(node *ts.Node) bool {
	for parent := node.Parent(); parent != nil; parent = parent.Parent() {
		if parent.Type(c.s.grammar) != ifNode {
			continue
		}

		cond := c.s.text(c.s.field(parent, "condition"))
		if cond == "TYPE_CHECKING" || strings.HasSuffix(cond, ".TYPE_CHECKING") {
			return true
		}
	}

	return false
}

func unquoteImportPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) < quotedPairLen {
		return ""
	}

	first, last := raw[0], raw[len(raw)-1]
	if (first == '\'' || first == '"' || first == '`') && first == last {
		return raw[1 : len(raw)-1]
	}

	return ""
}
