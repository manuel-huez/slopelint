package smells

import (
	"fmt"
	"go/ast"
	"strings"
	"unicode"
)

const restatementCommentWordCap = 6

var restatementNoiseWords = map[string]struct{}{
	"a":        {},
	"an":       {},
	"and":      {},
	"const":    {},
	"for":      {},
	"func":     {},
	"function": {},
	"helper":   {},
	"internal": {},
	"local":    {},
	"method":   {},
	"of":       {},
	"or":       {},
	"private":  {},
	"return":   {},
	"returns":  {},
	"set":      {},
	"sets":     {},
	"struct":   {},
	"that":     {},
	"the":      {},
	"this":     {},
	"to":       {},
	"type":     {},
	"var":      {},
	"value":    {},
}

func (l *Runner) checkRestatementComments() {
	for _, file := range l.pkg.Files {
		for _, decl := range file.Decls {
			switch decl := decl.(type) {
			case *ast.FuncDecl:
				l.checkFuncRestatementComment(decl)
			case *ast.GenDecl:
				l.checkGenDeclRestatementComment(decl)
			}
		}
	}
}

func (l *Runner) checkFuncRestatementComment(fn *ast.FuncDecl) {
	if fn == nil || fn.Name == nil || fn.Doc == nil {
		return
	}

	if ast.IsExported(fn.Name.Name) || !isRestatementCommentGroup(fn.Doc) {
		return
	}

	if !commentRestatesIdentifier(fn.Doc.Text(), fn.Name.Name) {
		return
	}

	l.reportRestatementComment(fn.Doc, fn.Name.Name)
}

func (l *Runner) checkGenDeclRestatementComment(decl *ast.GenDecl) {
	name, doc, ok := privateDeclNameAndDoc(decl)
	if !ok || !isRestatementCommentGroup(doc) {
		return
	}

	if !commentRestatesIdentifier(doc.Text(), name) {
		return
	}

	l.reportRestatementComment(doc, name)
}

func privateDeclNameAndDoc(decl *ast.GenDecl) (string, *ast.CommentGroup, bool) {
	if decl == nil || len(decl.Specs) != 1 {
		return "", nil, false
	}

	switch spec := decl.Specs[0].(type) {
	case *ast.TypeSpec:
		return privateTypeDeclNameAndDoc(decl, spec)
	case *ast.ValueSpec:
		return privateValueDeclNameAndDoc(decl, spec)
	default:
		return "", nil, false
	}
}

func privateTypeDeclNameAndDoc(
	decl *ast.GenDecl,
	spec *ast.TypeSpec,
) (string, *ast.CommentGroup, bool) {
	if spec == nil || spec.Name == nil || ast.IsExported(spec.Name.Name) {
		return "", nil, false
	}

	doc := docOrDeclDoc(spec.Doc, decl.Doc)

	return spec.Name.Name, doc, doc != nil
}

func privateValueDeclNameAndDoc(
	decl *ast.GenDecl,
	spec *ast.ValueSpec,
) (string, *ast.CommentGroup, bool) {
	if spec == nil || len(spec.Names) != 1 || spec.Names[0] == nil {
		return "", nil, false
	}

	name := spec.Names[0].Name
	if ast.IsExported(name) {
		return "", nil, false
	}

	doc := docOrDeclDoc(spec.Doc, decl.Doc)

	return name, doc, doc != nil
}

func docOrDeclDoc(doc *ast.CommentGroup, declDoc *ast.CommentGroup) *ast.CommentGroup {
	if doc != nil {
		return doc
	}

	return declDoc
}

func (l *Runner) reportRestatementComment(doc *ast.CommentGroup, name string) {
	l.report(
		doc.Pos(),
		"comment_noise",
		fmt.Sprintf(`comment only restates private declaration %q; remove or explain intent`, name),
	)
}

func isRestatementCommentGroup(doc *ast.CommentGroup) bool {
	if doc == nil || len(doc.List) != 1 {
		return false
	}

	text := strings.Join(strings.Fields(strings.TrimSpace(doc.Text())), " ")
	if text == "" {
		return false
	}

	trimmed := strings.TrimSpace(text)
	lower := strings.ToLower(trimmed)

	for _, prefix := range []string{"todo", "fixme", "warning", "note", "example", "nolint"} {
		if lower == prefix || strings.HasPrefix(lower, prefix+":") {
			return false
		}
	}

	return true
}

func commentRestatesIdentifier(text, name string) bool {
	commentWords := restatementCommentWords(text)
	if len(commentWords) == 0 || len(commentWords) > restatementCommentWordCap {
		return false
	}

	identifierWords := splitIdentifierWords(name)
	if len(identifierWords) == 0 || len(commentWords) < len(identifierWords) {
		return false
	}

	used := make([]bool, len(identifierWords))
	matches := 0

	for _, commentWord := range commentWords {
		found := false

		for idx, identWord := range identifierWords {
			if used[idx] {
				continue
			}

			if !restatementWordsMatch(commentWord, identWord) {
				continue
			}

			used[idx] = true
			matches++
			found = true

			break
		}

		if !found {
			return false
		}
	}

	return matches == len(identifierWords)
}

func restatementCommentWords(text string) []string {
	parts := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})

	if len(parts) == 0 {
		return nil
	}

	words := make([]string, 0, len(parts))
	for _, word := range parts {
		if word == "" {
			continue
		}

		if _, noise := restatementNoiseWords[word]; noise {
			continue
		}

		words = append(words, word)
	}

	return words
}

func restatementWordsMatch(commentWord, identWord string) bool {
	if commentWord == identWord {
		return true
	}

	for _, suffix := range []string{"s", "ed", "ing"} {
		if strings.TrimSuffix(commentWord, suffix) == identWord {
			return true
		}
	}

	return false
}
