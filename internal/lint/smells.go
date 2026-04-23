package lint

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
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

func (l *linter) scanExperimentalPackage() {
	l.checkTrivialForwarders()
	l.checkRestatementComments()
}

func (l *linter) checkTrivialForwarders() {
	callCounts := l.packageCallCounts()

	for _, file := range l.pkg.Files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}

			l.checkTrivialForwarder(fn, callCounts)
		}
	}
}

func (l *linter) packageCallCounts() map[string]int {
	counts := make(map[string]int)

	for _, file := range l.pkg.Files {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}

			_, key, ok := l.calledFunc(call)
			if !ok {
				return true
			}

			counts[key]++

			return true
		})
	}

	return counts
}

func (l *linter) checkTrivialForwarder(fn *ast.FuncDecl, callCounts map[string]int) {
	if !isEligibleTrivialForwarderDecl(fn) {
		return
	}

	obj, ok := l.trivialForwarderObject(fn, callCounts)
	if !ok {
		return
	}

	call, ok := l.trivialForwarderBodyCall(fn, obj)
	if !ok {
		return
	}

	if !l.samePackageForwardTarget(obj, call) {
		return
	}

	l.report(
		fn.Name.Pos(),
		"trivial_wrapper",
		fmt.Sprintf(
			`private helper %q only forwards to %q at one callsite; inline or merge names`,
			fn.Name.Name,
			l.render(call.Fun),
		),
	)
}

func isEligibleTrivialForwarderDecl(fn *ast.FuncDecl) bool {
	switch {
	case fn == nil, fn.Name == nil, fn.Body == nil, fn.Doc != nil, fn.Recv != nil:
		return false
	case ast.IsExported(fn.Name.Name), hasTypeParams(fn.Type):
		return false
	case len(fn.Body.List) != 1:
		return false
	default:
		return true
	}
}

func hasTypeParams(fnType *ast.FuncType) bool {
	return fnType != nil && fnType.TypeParams != nil && len(fnType.TypeParams.List) != 0
}

func (l *linter) trivialForwarderObject(
	fn *ast.FuncDecl,
	callCounts map[string]int,
) (*types.Func, bool) {
	if l.hasAttachedComment(fn.Body.List[0]) {
		return nil, false
	}

	obj, ok := l.pkg.TypesInfo.ObjectOf(fn.Name).(*types.Func)
	if !ok || obj == nil {
		return nil, false
	}

	if callCounts[funcObjectKey(obj)] != 1 {
		return nil, false
	}

	return obj, true
}

func (l *linter) trivialForwarderBodyCall(
	fn *ast.FuncDecl,
	obj *types.Func,
) (*ast.CallExpr, bool) {
	sig, ok := obj.Type().(*types.Signature)
	if !ok || sig == nil {
		return nil, false
	}

	call, ok := l.trivialForwarderCall(fn.Body.List[0], sig.Results())
	if !ok {
		return nil, false
	}

	if !forwardedCallMatchesParams(
		l.pkg.TypesInfo,
		call,
		funcParamObjects(l.pkg.TypesInfo, fn.Type.Params),
		sig.Variadic(),
	) {
		return nil, false
	}

	return call, true
}

func (l *linter) samePackageForwardTarget(obj *types.Func, call *ast.CallExpr) bool {
	callee, _, ok := l.calledFunc(call)
	if !ok || obj == nil || callee == nil || callee == obj {
		return false
	}

	if obj.Pkg() == nil || callee.Pkg() == nil {
		return false
	}

	if obj.Pkg().Path() != callee.Pkg().Path() {
		return false
	}

	return true
}

func (l *linter) trivialForwarderCall(
	stmt ast.Stmt,
	results *types.Tuple,
) (*ast.CallExpr, bool) {
	resultCount := 0
	if results != nil {
		resultCount = results.Len()
	}

	switch stmt := stmt.(type) {
	case *ast.ReturnStmt:
		if resultCount == 0 || len(stmt.Results) != 1 {
			return nil, false
		}

		call, ok := l.unparen(stmt.Results[0]).(*ast.CallExpr)
		if !ok {
			return nil, false
		}

		return call, true
	case *ast.ExprStmt:
		if resultCount != 0 {
			return nil, false
		}

		call, ok := l.unparen(stmt.X).(*ast.CallExpr)
		if !ok {
			return nil, false
		}

		return call, true
	default:
		return nil, false
	}
}

func funcParamObjects(info *types.Info, fields *ast.FieldList) []*types.Var {
	if fields == nil {
		return nil
	}

	params := make([]*types.Var, 0)

	for _, field := range fields.List {
		if len(field.Names) == 0 {
			return nil
		}

		for _, name := range field.Names {
			obj, ok := info.ObjectOf(name).(*types.Var)
			if !ok || obj == nil {
				return nil
			}

			params = append(params, obj)
		}
	}

	return params
}

func forwardedCallMatchesParams(
	info *types.Info,
	call *ast.CallExpr,
	params []*types.Var,
	variadic bool,
) bool {
	if call == nil {
		return false
	}

	if variadic {
		if len(params) == 0 || len(call.Args) != len(params) || call.Ellipsis == token.NoPos {
			return false
		}
	} else if len(call.Args) != len(params) || call.Ellipsis != token.NoPos {
		return false
	}

	for idx, arg := range call.Args {
		if !identRefersToObject(info, arg, params[idx]) {
			return false
		}
	}

	return true
}

func identRefersToObject(info *types.Info, expr ast.Expr, obj types.Object) bool {
	ident, ok := expr.(*ast.Ident)
	if !ok || ident == nil {
		return false
	}

	return info.ObjectOf(ident) == obj
}

func (l *linter) checkRestatementComments() {
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

func (l *linter) checkFuncRestatementComment(fn *ast.FuncDecl) {
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

func (l *linter) checkGenDeclRestatementComment(decl *ast.GenDecl) {
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

func (l *linter) reportRestatementComment(doc *ast.CommentGroup, name string) {
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

	text := normalizeCommentText(doc.Text())
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
	switch {
	case commentWord == identWord:
		return true
	case strings.TrimSuffix(commentWord, "s") == identWord:
		return true
	case strings.TrimSuffix(commentWord, "ed") == identWord:
		return true
	case strings.TrimSuffix(commentWord, "ing") == identWord:
		return true
	default:
		return false
	}
}
