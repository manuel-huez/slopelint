package outline

import (
	"slices"
	"strings"

	ts "github.com/odvcencio/gotreesitter"
)

var suppressionPrefixes = map[Language][]string{
	TypeScript: {
		"@ts-ignore",
		"@ts-expect-error",
		"@ts-nocheck",
		"eslint-disable",
		"oxlint-disable",
		"biome-ignore",
	},
	Python: {"noqa", "type: ignore", "pylint: disable", "pyright: ignore", "pragma: no cover"},
}

// directivePrefixes are tool instructions that are not prose: TypeScript
// triple-slash references, source maps, and lint re-enables; Python type
// comments and formatter switches. Suppressions win over them.
var directivePrefixes = map[Language][]string{
	TypeScript: {
		"/ <reference",
		"/ <amd",
		"# sourceMappingURL",
		"# sourceURL",
		"eslint-enable",
		"oxlint-enable",
	},
	Python: {"!", "-*-", "type:", "fmt:", "pylint: enable"},
}

// judgeComment reports whether review judges a comment and whether it is a
// suppression. A Python comment can trail its reason with a second #, as in
// `# reason  # noqa: E501`, so each #-segment is checked.
func judgeComment(text string, language Language) (bool, bool) {
	body, line := strings.CutPrefix(text, "//")
	segments := []string{body}

	switch {
	case language == Python:
		body = strings.TrimPrefix(text, "#")
		segments = strings.Split(body, "#")
	case !line:
		body = strings.TrimLeft(strings.TrimSuffix(strings.TrimPrefix(text, "/*"), "*/"), "*")
		segments = []string{body}
	}

	hasPrefix := func(prefixes []string, text string) bool {
		return slices.ContainsFunc(prefixes, func(prefix string) bool {
			return strings.HasPrefix(strings.TrimSpace(text), prefix)
		})
	}

	if slices.ContainsFunc(segments, func(segment string) bool {
		return hasPrefix(suppressionPrefixes[language], segment)
	}) {
		return true, true
	}

	return strings.TrimSpace(body) != "" && !hasPrefix(directivePrefixes[language], body), false
}

// commentGroup is a run of adjacent comments, grouped as go/parser groups
// Go comments: a comment that starts its own line on the next line after
// the group joins it, unless the group began after code on its line.
type commentGroup struct {
	comments []rawComment
}

func (w *walker) commentGroups() []commentGroup {
	slices.SortFunc(w.comments, func(a, b rawComment) int { return int(a.start) - int(b.start) })

	var groups []commentGroup

	for _, comment := range w.comments {
		if len(groups) > 0 && !comment.docstring {
			last := &groups[len(groups)-1]
			first, previous := last.comments[0], last.comments[len(last.comments)-1]
			gap := string(w.src[previous.end:comment.start])

			if !previous.docstring && first.ownLine && comment.ownLine &&
				strings.TrimSpace(gap) == "" && strings.Count(gap, "\n") <= 1 {
				last.comments = append(last.comments, comment)
				continue
			}
		}

		groups = append(groups, commentGroup{comments: []rawComment{comment}})
	}

	return groups
}

func (w *walker) finish(root *ts.Node) {
	groups := w.commentGroups()

	for _, group := range groups {
		if slices.ContainsFunc(
			group.comments,
			func(comment rawComment) bool { return comment.judged },
		) {
			w.file.Comments = append(w.file.Comments, Comment{
				Pos:         group.comments[0].pos,
				EndLine:     group.comments[len(group.comments)-1].endLine,
				Suppression: group.comments[0].suppression,
			})
		}
	}

	w.declarations(root, groups)

	// A suppression outside every declaration, such as a file-wide
	// `// @ts-nocheck`, is a declaration of its own so it is still judged.
	for _, comment := range w.file.Comments {
		if comment.Suppression && !slices.ContainsFunc(w.file.Decls, func(decl Decl) bool {
			return decl.StartLine <= comment.Pos.Line && comment.Pos.Line <= decl.EndLine
		}) {
			w.file.Decls = append(
				w.file.Decls,
				Decl{StartLine: comment.Pos.Line, EndLine: comment.EndLine},
			)
		}
	}

	slices.SortFunc(w.file.Decls, func(a, b Decl) int { return a.StartLine - b.StartLine })
}

// declarations records each top-level statement with the comment group
// that ends on the line above it. Adjacent statements form one declaration,
// as a Go import or var block does, so an import keeps the sys.path change
// above it. A module docstring documents the file, as a Go package comment
// does, so it starts none.
func (w *walker) declarations(root *ts.Node, groups []commentGroup) {
	docs := make(map[int]int)

	for _, group := range groups {
		first, last := group.comments[0], group.comments[len(group.comments)-1]
		if !first.docstring && first.ownLine {
			docs[last.endLine+1] = first.pos.Line
		}
	}

	for index := range root.NamedChildCount() {
		node := root.NamedChild(index)
		if node.Type(w.grammar) == commentNode || node == w.moduleDoc {
			continue
		}

		decl := Decl{StartLine: int(node.StartPoint().Row) + 1, EndLine: w.endLine(node)}
		if start, ok := docs[decl.StartLine]; ok {
			decl.StartLine = start
		}

		if last := len(
			w.file.Decls,
		) - 1; last >= 0 &&
			w.file.Decls[last].EndLine+1 >= decl.StartLine {
			w.file.Decls[last].EndLine = decl.EndLine
			continue
		}

		w.file.Decls = append(w.file.Decls, decl)
	}
}
