package lint

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"slices"
	"strings"
	"unicode"

	"github.com/manuel-huez/slopelint/internal/lint/outline"
)

func collectGoCodeReviewDeclarations(
	filename string,
	content []byte,
	candidates []Issue,
	policy codeReviewPolicy,
) ([]codeReviewDeclaration, error) {
	fset := token.NewFileSet()

	file, err := parser.ParseFile(fset, filename, content, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", filename, err)
	}

	if ast.IsGenerated(file) {
		return nil, nil
	}

	source := codeReviewSource{
		fset:       fset,
		file:       file,
		lines:      strings.Split(string(content), "\n"),
		test:       strings.HasSuffix(filename, "_test.go"),
		candidates: candidates,
		ifs:        make(map[int]*ast.IfStmt),
		policy:     policy,
	}

	ast.Inspect(file, func(node ast.Node) bool {
		if stmt, ok := node.(*ast.IfStmt); ok {
			source.ifs[fset.PositionFor(stmt.Pos(), false).Offset] = stmt
		}

		return true
	})

	for _, candidate := range candidates {
		if source.ifs[candidate.position.Offset] == nil {
			return nil, fmt.Errorf("%s changed during the scan; run slopelint again", filename)
		}
	}

	var declarations []codeReviewDeclaration

	for _, decl := range file.Decls {
		declaration, err := source.declaration(decl)
		if err != nil {
			return nil, err
		}

		if len(declaration.request.Items) > 0 {
			declarations = append(declarations, declaration)
		}
	}

	return declarations, nil
}

type codeReviewSource struct {
	fset       *token.FileSet
	file       *ast.File
	lines      []string
	test       bool
	candidates []Issue
	ifs        map[int]*ast.IfStmt
	policy     codeReviewPolicy
}

// position ignores //line directives: review reads the file's own lines.
func (source codeReviewSource) position(pos token.Pos) token.Position {
	return source.fset.PositionFor(pos, false)
}

func (source codeReviewSource) declaration(decl ast.Decl) (codeReviewDeclaration, error) {
	start, doc := decl.Pos(), declDoc(decl)
	if doc != nil {
		start = doc.Pos()
	}

	startLine := source.position(start).Line
	endLine := source.position(decl.End()).Line

	var items []codeReviewPositionedItem

	// A cgo preamble is C source, not a Go comment.
	if !importsC(decl) {
		items = source.commentItems(startLine, endLine)
	}

	items = append(items, source.droppedErrorItems(decl, startLine)...)

	if source.test && runnableGoTest(decl) {
		position := source.position(decl.Pos())
		items = append(items, codeReviewPositionedItem{
			item: codeReviewItem{
				Kind:  codeReviewTestItem,
				Lines: codeReviewLines(position.Line-startLine+1, endLine-startLine+1),
			},
			position: position,
		})
	}

	if len(items) == 0 {
		return codeReviewDeclaration{}, nil
	}

	return newCodeReviewDeclaration(codeReviewRequest{
		Location: fmt.Sprintf("%s:%d", source.position(start).Filename, startLine),
		Language: outline.Go,
		Test:     source.test,
		Source:   numberedCodeReviewSource(source.lines[startLine-1 : endLine]),
	}, items, source.policy)
}

func (source codeReviewSource) commentItems(startLine, endLine int) []codeReviewPositionedItem {
	var groups []codeReviewCommentGroup

	for _, group := range source.file.Comments {
		if codeReviewHasItem(group) {
			groups = append(groups, codeReviewCommentGroup{
				position:    source.position(group.Pos()),
				lastLine:    source.position(group.End()).Line,
				suppression: codeReviewSuppression(group.List[0].Text),
			})
		}
	}

	return codeReviewCommentItems(groups, startLine, endLine)
}

func (source codeReviewSource) droppedErrorItems(
	decl ast.Decl,
	startLine int,
) []codeReviewPositionedItem {
	startOffset := source.position(decl.Pos()).Offset
	endOffset := source.position(decl.End()).Offset

	var items []codeReviewPositionedItem

	for _, candidate := range source.candidates {
		offset := candidate.position.Offset
		if offset < startOffset || offset > endOffset {
			continue
		}

		ifStmt := source.ifs[offset]
		items = append(items, codeReviewPositionedItem{
			item: codeReviewItem{
				Kind: codeReviewErrorItem,
				Lines: codeReviewLines(
					source.position(ifStmt.Pos()).Line-startLine+1,
					source.position(ifStmt.End()).Line-startLine+1,
				),
				Error: candidate.Message,
			},
			position: candidate.position,
		})
	}

	return items
}

// runnableGoTest matches func TestXxx(t *testing.T) as go test names tests.
// It is syntactic, so a renamed testing import is not matched.
func runnableGoTest(decl ast.Decl) bool {
	fn, ok := decl.(*ast.FuncDecl)
	if !ok || fn.Recv != nil {
		return false
	}

	rest, ok := strings.CutPrefix(fn.Name.Name, "Test")
	if !ok || rest != "" && unicode.IsLower([]rune(rest)[0]) {
		return false
	}

	params := fn.Type.Params.List

	return len(params) == 1 && len(params[0].Names) <= 1 &&
		types.ExprString(params[0].Type) == "*testing.T"
}

func declDoc(decl ast.Decl) *ast.CommentGroup {
	switch decl := decl.(type) {
	case *ast.FuncDecl:
		return decl.Doc
	case *ast.GenDecl:
		return decl.Doc
	default:
		return nil
	}
}

func importsC(decl ast.Decl) bool {
	gen, ok := decl.(*ast.GenDecl)
	if !ok || gen.Tok != token.IMPORT {
		return false
	}

	for _, spec := range gen.Specs {
		if spec, ok := spec.(*ast.ImportSpec); ok && spec.Path.Value == `"C"` {
			return true
		}
	}

	return false
}

// codeReviewSuppressions are directives that silence a check. They are always
// judged, with or without a reason, so a bare suppression is no way around
// review.
var codeReviewSuppressions = []string{
	"nolint",
	"exhaustive:ignore",
	"slopelint:ignore",
	"lint:ignore",
	"lint:file-ignore",
}

func codeReviewHasItem(group *ast.CommentGroup) bool {
	return slices.ContainsFunc(group.List, func(comment *ast.Comment) bool {
		return codeReviewJudged(comment.Text)
	})
}

func codeReviewJudged(text string) bool {
	if body, ok := strings.CutPrefix(text, "/*"); ok {
		return strings.TrimSpace(strings.TrimSuffix(body, "*/")) != ""
	}

	if codeReviewSuppression(text) {
		return true
	}

	body := strings.TrimPrefix(text, "//")

	return strings.TrimSpace(body) != "" && !strings.HasPrefix(body, " +build") &&
		!commentDirective(body)
}

func codeReviewSuppression(text string) bool {
	body := strings.TrimPrefix(text, "//")

	return slices.ContainsFunc(codeReviewSuppressions, func(prefix string) bool {
		return strings.HasPrefix(body, prefix)
	})
}

// commentDirective matches Go's directive form: `//name:arg` with no space,
// plus the `//line` and `//export`-style forms.
func commentDirective(body string) bool {
	for _, prefix := range []string{"line ", "export ", "extern "} {
		if strings.HasPrefix(body, prefix) {
			return true
		}
	}

	name, _, ok := strings.Cut(body, ":")
	if !ok || name == "" {
		return false
	}

	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}

	return true
}
