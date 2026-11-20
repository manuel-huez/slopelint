package lint

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"maps"
	"slices"
	"strings"
	"unicode"

	"github.com/manuel-huez/slopelint/internal/lint/outline"
	smellcheck "github.com/manuel-huez/slopelint/internal/lint/smells"
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

	splitHelpers := slices.DeleteFunc(slices.Clone(candidates), func(candidate Issue) bool {
		return candidate.Kind != smellcheck.SplitHelperCandidate
	})
	source := codeReviewSource{
		fset:  fset,
		file:  file,
		lines: strings.Split(string(content), "\n"),
		test:  strings.HasSuffix(filename, "_test.go"),
		candidates: slices.DeleteFunc(candidates, func(candidate Issue) bool {
			return candidate.Kind == smellcheck.SplitHelperCandidate
		}),
		ifs:    make(map[int]*ast.IfStmt),
		policy: policy,
	}

	ast.Inspect(file, func(node ast.Node) bool {
		if stmt, ok := node.(*ast.IfStmt); ok {
			source.ifs[fset.PositionFor(stmt.Pos(), false).Offset] = stmt
		}

		return true
	})

	for _, candidate := range source.candidates {
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

	if !source.test {
		helpers, err := source.splitHelperDeclarations(splitHelpers)

		return append(declarations, helpers...), err
	}

	tests, helpers := source.groupedTests()
	testGroups, err := testGroupDeclarations(filename, source.lines, tests, helpers, policy)

	return append(declarations, testGroups...), err
}

func (source codeReviewSource) groupedTests() ([]groupedTest, map[string]string) {
	var tests []groupedTest

	helpers := make(map[string]string)
	packages := make(map[string]bool)

	for _, spec := range source.file.Imports {
		packages[goImportName(spec)] = true
	}

	for _, decl := range source.file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}

		if !runnableGoTest(fn) {
			if fn.Recv == nil {
				helpers[fn.Name.Name] = "func " + fn.Name.Name +
					strings.TrimPrefix(types.ExprString(fn.Type), "func")
			}

			continue
		}

		start := fn.Pos()
		if fn.Doc != nil {
			start = fn.Doc.Pos()
		}

		tests = append(tests, groupedTest{
			name:      fn.Name.Name,
			startLine: source.position(start).Line,
			endLine:   source.position(fn.End()).Line,
			position:  source.position(fn.Pos()),
			calls:     goTestCalls(fn, packages),
		})
	}

	return tests, helpers
}

// goTestCalls returns the distinct callees of a test that can be steps it
// shares with others: functions as name or package.name, and methods as
// .name, since tests name their receivers freely. Calls on *testing.T
// values, local function values, builtins, and the formatting, comparison,
// and assertion packages every test uses tell tests apart by nothing.
func goTestCalls(fn *ast.FuncDecl, packages map[string]bool) []string {
	scope := goTestScope{
		packages:   packages,
		locals:     make(map[string]bool),
		testValues: make(map[string]bool),
	}
	ast.Inspect(fn, scope.declare)

	calls := make(map[string]bool)

	ast.Inspect(fn.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}

		// A generic call names its function before the type arguments.
		fun := ast.Unparen(call.Fun)
		if index, ok := fun.(*ast.IndexExpr); ok {
			fun = index.X
		} else if index, ok := fun.(*ast.IndexListExpr); ok {
			fun = index.X
		}

		if name := scope.callee(fun); name != "" {
			calls[name] = true
		}

		return true
	})

	return slices.Sorted(maps.Keys(calls))
}

// goTestScope holds the names a test binds: locals shadow functions, and
// testValues are its *testing.T values.
type goTestScope struct {
	packages   map[string]bool
	locals     map[string]bool
	testValues map[string]bool
}

func (scope goTestScope) declare(node ast.Node) bool {
	var names []*ast.Ident

	switch node := node.(type) {
	case *ast.FuncType:
		for _, field := range node.Params.List {
			for _, name := range field.Names {
				scope.testValues[name.Name] = types.ExprString(field.Type) == "*testing.T"
			}

			names = append(names, field.Names...)
		}
	case *ast.AssignStmt:
		if node.Tok == token.DEFINE {
			for _, lhs := range node.Lhs {
				if ident, ok := lhs.(*ast.Ident); ok {
					names = append(names, ident)
				}
			}
		}
	case *ast.ValueSpec:
		names = node.Names
	}

	for _, name := range names {
		scope.locals[name.Name] = true
	}

	return true
}

func (scope goTestScope) callee(fun ast.Expr) string {
	switch fun := fun.(type) {
	case *ast.Ident:
		if types.Universe.Lookup(fun.Name) != nil || scope.locals[fun.Name] {
			return ""
		}

		return fun.Name
	case *ast.SelectorExpr:
		ident, _ := fun.X.(*ast.Ident)
		if ident == nil || !scope.packages[ident.Name] && !scope.testValues[ident.Name] {
			return "." + fun.Sel.Name
		}

		if scope.testValues[ident.Name] || goNoiseCallPackages[ident.Name] {
			return ""
		}

		return ident.Name + "." + fun.Sel.Name
	default:
		return ""
	}
}

// goImportName approximates the name an import binds without loading it:
// the alias, or the last path element that is not a major version.
func goImportName(spec *ast.ImportSpec) string {
	if spec.Name != nil {
		return spec.Name.Name
	}

	parts := strings.Split(strings.Trim(spec.Path.Value, `"`), "/")
	name := parts[len(parts)-1]

	if len(parts) > 1 && len(name) > 1 && name[0] == 'v' &&
		strings.Trim(name[1:], "0123456789") == "" {
		name = parts[len(parts)-2]
	}

	return name
}

var goNoiseCallPackages = map[string]bool{
	"fmt": true, "errors": true, "strings": true, "bytes": true, "reflect": true,
	"slices": true, "maps": true, "sort": true, "strconv": true, "math": true,
	"cmp": true, "assert": true, "require": true,
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
	doc := declDoc(decl)
	startLine, endLine := source.lineSpan(decl)

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
		items = append(items, source.serialItems(doc, startLine)...)
	}

	if len(items) == 0 {
		return codeReviewDeclaration{}, nil
	}

	return newCodeReviewDeclaration(codeReviewRequest{
		Location: fmt.Sprintf("%s:%d", source.position(decl.Pos()).Filename, startLine),
		Language: outline.Go,
		Test:     source.test,
		Source:   numberedCodeReviewSource(source.lines[startLine-1 : endLine]),
	}, items, source.policy)
}

// splitHelperDeclarations builds one request per caller: the caller, then
// each helper split from it, so review judges the pieces together.
func (source codeReviewSource) splitHelperDeclarations(
	candidates []Issue,
) ([]codeReviewDeclaration, error) {
	funcs := make(map[int]*ast.FuncDecl)

	for _, decl := range source.file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok {
			funcs[source.position(fn.Name.Pos()).Offset] = fn
		}
	}

	byCaller := make(map[*ast.FuncDecl][]Issue)

	var callers []*ast.FuncDecl

	for _, candidate := range candidates {
		helper := funcs[candidate.position.Offset]

		caller := source.caller(helper)
		if caller == nil {
			return nil, fmt.Errorf(
				"%s changed during the scan; run slopelint again",
				candidate.position.Filename,
			)
		}

		if byCaller[caller] == nil {
			callers = append(callers, caller)
		}

		byCaller[caller] = append(byCaller[caller], candidate)
	}

	declarations := make([]codeReviewDeclaration, 0, len(callers))

	for _, caller := range callers {
		declaration, err := source.splitHelperDeclaration(caller, byCaller[caller], funcs)
		if err != nil {
			return nil, err
		}

		declarations = append(declarations, declaration)
	}

	return declarations, nil
}

func (source codeReviewSource) splitHelperDeclaration(
	caller *ast.FuncDecl,
	candidates []Issue,
	funcs map[int]*ast.FuncDecl,
) (codeReviewDeclaration, error) {
	startLine, endLine := source.lineSpan(caller)
	lines := slices.Clone(source.lines[startLine-1 : endLine])
	items := make([]codeReviewPositionedItem, 0, len(candidates))

	for _, candidate := range candidates {
		helperStart, helperEnd := source.lineSpan(funcs[candidate.position.Offset])

		lines = append(lines, "// ...")
		first := len(lines) + 1
		lines = append(lines, source.lines[helperStart-1:helperEnd]...)
		items = append(items, codeReviewPositionedItem{
			item: codeReviewItem{
				Kind:  codeReviewSplitHelperItem,
				Lines: codeReviewLines(first, len(lines)),
				Facts: candidate.Message,
			},
			position: candidate.position,
		})
	}

	return newCodeReviewDeclaration(codeReviewRequest{
		Location: fmt.Sprintf("%s:%d", source.position(caller.Pos()).Filename, startLine),
		Language: outline.Go,
		Source:   numberedCodeReviewSource(lines),
	}, items, source.policy)
}

// caller returns the other function in the file that calls helper. The
// linter found exactly one use, so a name match finds it.
func (source codeReviewSource) caller(helper *ast.FuncDecl) *ast.FuncDecl {
	if helper == nil {
		return nil
	}

	for _, decl := range source.file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn == helper || fn.Body == nil {
			continue
		}

		found := false

		ast.Inspect(fn.Body, func(node ast.Node) bool {
			if call, ok := node.(*ast.CallExpr); ok && calledName(call.Fun) == helper.Name.Name {
				found = true
			}

			return !found
		})

		if found {
			return fn
		}
	}

	return nil
}

func calledName(fun ast.Expr) string {
	switch fun := ast.Unparen(fun).(type) {
	case *ast.Ident:
		return fun.Name
	case *ast.SelectorExpr:
		return fun.Sel.Name
	case *ast.IndexExpr:
		return calledName(fun.X)
	default:
		return ""
	}
}

func (source codeReviewSource) lineSpan(decl ast.Decl) (int, int) {
	start := decl.Pos()
	if doc := declDoc(decl); doc != nil {
		start = doc.Pos()
	}

	return source.position(start).Line, source.position(decl.End()).Line
}

func (source codeReviewSource) commentItems(startLine, endLine int) []codeReviewPositionedItem {
	var groups []codeReviewCommentGroup

	for _, group := range source.file.Comments {
		if !codeReviewHasItem(group) {
			continue
		}

		// A serial marker at either end of a group is judged as its test's
		// serial item, so the comment item leaves it out.
		comments := group.List
		for serialMarkerComment(comments[0]) {
			comments = comments[1:]
		}

		for serialMarkerComment(comments[len(comments)-1]) {
			comments = comments[:len(comments)-1]
		}

		groups = append(groups, codeReviewCommentGroup{
			position:    source.position(comments[0].Pos()),
			lastLine:    source.position(comments[len(comments)-1].End()).Line,
			suppression: codeReviewSuppression(comments[0].Text),
		})
	}

	return codeReviewCommentItems(groups, startLine, endLine)
}

// serialItems returns the first //slopelint:serial marker with a reason in a
// test's doc comment; the static rule reports a marker without one.
func (source codeReviewSource) serialItems(
	doc *ast.CommentGroup,
	startLine int,
) []codeReviewPositionedItem {
	if doc == nil {
		return nil
	}

	for _, comment := range doc.List {
		if reason, ok := smellcheck.SerialMarker(comment.Text); ok && reason != "" {
			position := source.position(comment.Pos())
			line := position.Line - startLine + 1

			return []codeReviewPositionedItem{
				{
					item: codeReviewItem{
						Kind:  codeReviewSerialItem,
						Lines: codeReviewLines(line, line),
					},
					position: position,
				},
			}
		}
	}

	return nil
}

func serialMarkerComment(comment *ast.Comment) bool {
	_, ok := smellcheck.SerialMarker(comment.Text)

	return ok
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
