package structure

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"slices"
	"strings"
	"unicode"
)

func (l *Runner) checkSingleUseTempAlias(stmts []ast.Stmt, idx int) {
	decl, ok := l.singleUseTempAlias(stmts, idx)
	if !ok {
		return
	}

	l.report(
		decl.name.Pos(),
		"temp_alias",
		fmt.Sprintf(
			`local %q only renames cheap expression %q for one use; inline expression`,
			decl.name.Name,
			l.render(decl.rhs),
		),
	)
}

func (l *Runner) singleUseTempAlias(stmts []ast.Stmt, idx int) (tempAliasDecl, bool) {
	if idx+1 >= len(stmts) {
		return tempAliasDecl{}, false
	}

	decl, ok := l.tempAliasDeclFromStmt(stmts[idx])
	if !ok || l.hasAttachedComment(stmts[idx]) {
		return tempAliasDecl{}, false
	}

	if !tempAliasNameLooksDisposable(decl.name.Name, decl.rhs) {
		return tempAliasDecl{}, false
	}

	nextUse := l.objectUseSummary(stmts[idx+1], decl.obj)
	if nextUse.unsafe || nextUse.reads != 1 {
		return tempAliasDecl{}, false
	}

	for _, later := range stmts[idx+2:] {
		if nodeUsesObject(later, decl.obj, l.pkg.TypesInfo) {
			return tempAliasDecl{}, false
		}
	}

	return decl, true
}

func (l *Runner) tempAliasDeclFromStmt(stmt ast.Stmt) (tempAliasDecl, bool) {
	assign, ok := stmt.(*ast.AssignStmt)
	if ok {
		return l.tempAliasDeclFromAssign(assign)
	}

	decl, ok := stmt.(*ast.DeclStmt)
	if ok {
		return l.tempAliasDeclFromDecl(decl)
	}

	return tempAliasDecl{}, false
}

func (l *Runner) tempAliasDeclFromAssign(stmt *ast.AssignStmt) (tempAliasDecl, bool) {
	if stmt.Tok != token.DEFINE || len(stmt.Lhs) != 1 || len(stmt.Rhs) != 1 {
		return tempAliasDecl{}, false
	}

	name, ok := stmt.Lhs[0].(*ast.Ident)
	if !ok {
		return tempAliasDecl{}, false
	}

	return l.tempAliasDecl(name, stmt.Rhs[0])
}

func (l *Runner) tempAliasDeclFromDecl(stmt *ast.DeclStmt) (tempAliasDecl, bool) {
	decl, ok := stmt.Decl.(*ast.GenDecl)
	if !ok || decl.Tok != token.VAR || len(decl.Specs) != 1 {
		return tempAliasDecl{}, false
	}

	spec, ok := decl.Specs[0].(*ast.ValueSpec)
	if !ok || spec.Type != nil || len(spec.Names) != 1 || len(spec.Values) != 1 {
		return tempAliasDecl{}, false
	}

	return l.tempAliasDecl(spec.Names[0], spec.Values[0])
}

func (l *Runner) tempAliasDecl(name *ast.Ident, rhs ast.Expr) (tempAliasDecl, bool) {
	if name == nil || name.Name == "_" {
		return tempAliasDecl{}, false
	}

	obj, ok := l.pkg.TypesInfo.ObjectOf(name).(*types.Var)
	if !ok || obj == nil || obj.IsField() {
		return tempAliasDecl{}, false
	}

	rhs = l.unparen(rhs)
	if !l.isCheapTempAliasExpr(rhs) {
		return tempAliasDecl{}, false
	}

	return tempAliasDecl{name: name, obj: obj, rhs: rhs}, true
}

func (l *Runner) isCheapTempAliasExpr(expr ast.Expr) bool {
	expr = l.unparen(expr)

	switch expr := expr.(type) {
	case *ast.Ident:
		if expr.Name == "_" {
			return false
		}

		obj := l.pkg.TypesInfo.ObjectOf(expr)
		if obj == nil {
			return false
		}

		if _, ok := obj.(*types.PkgName); ok {
			return false
		}

		_, isFn := types.Unalias(l.pkg.TypesInfo.TypeOf(expr)).Underlying().(*types.Signature)

		return !isFn
	case *ast.SelectorExpr:
		sel := l.pkg.TypesInfo.Selections[expr]
		if sel == nil || sel.Kind() != types.FieldVal {
			return false
		}

		_, isFn := types.Unalias(l.pkg.TypesInfo.TypeOf(expr)).Underlying().(*types.Signature)

		return !isFn && l.isCheapTempAliasExpr(expr.X)
	default:
		return false
	}
}

func (l *Runner) commentTextsInRange(start, end token.Pos) []string {
	if start == token.NoPos || end == token.NoPos {
		return nil
	}

	file := l.pkg.FSet.File(start)
	if file == nil {
		return nil
	}

	out := make([]string, 0)

	for _, astFile := range l.pkg.Files {
		for _, group := range astFile.Comments {
			if l.pkg.FSet.File(group.Pos()) != file {
				continue
			}

			if group.Pos() < start || group.End() > end {
				continue
			}

			text := strings.Join(strings.Fields(strings.TrimSpace(group.Text())), " ")
			if text == "" {
				continue
			}

			out = append(out, text)
		}
	}

	return out
}

func (l *Runner) hasAttachedComment(node ast.Node) bool {
	if node == nil {
		return false
	}

	file := l.pkg.FSet.File(node.Pos())
	if file == nil {
		return false
	}

	start := l.pkg.FSet.Position(node.Pos())
	end := l.pkg.FSet.Position(node.End())

	for _, astFile := range l.pkg.Files {
		for _, group := range astFile.Comments {
			if l.pkg.FSet.File(group.Pos()) != file {
				continue
			}

			groupStart := l.pkg.FSet.Position(group.Pos())
			groupEnd := l.pkg.FSet.Position(group.End())

			if groupStart.Line >= start.Line && groupStart.Line <= end.Line {
				return true
			}

			if groupStart.Line == end.Line {
				return true
			}

			if groupEnd.Line == start.Line-1 {
				return true
			}
		}
	}

	return false
}

func tempAliasNameLooksDisposable(name string, rhs ast.Expr) bool {
	leaf, ok := tempAliasLeafName(rhs)
	if !ok {
		return false
	}

	nameWords := stripAliasNoise(splitIdentifierWords(name))
	leafWords := stripAliasNoise(splitIdentifierWords(leaf))

	return len(nameWords) != 0 && sameWords(nameWords, leafWords)
}

func tempAliasLeafName(expr ast.Expr) (string, bool) {
	switch expr := expr.(type) {
	case *ast.Ident:
		return expr.Name, true
	case *ast.SelectorExpr:
		return expr.Sel.Name, true
	default:
		return "", false
	}
}

func splitIdentifierWords(name string) []string {
	if name == "" {
		return nil
	}

	runes := []rune(name)
	start := 0
	words := make([]string, 0, identifierWordCap)

	for idx := 1; idx < len(runes); idx++ {
		nextStart, split := nextIdentifierWordStart(runes, idx)
		if split {
			appendIdentifierWord(runes, start, idx, &words)
			start = nextStart
		}
	}

	appendIdentifierWord(runes, start, len(runes), &words)

	return words
}

func nextIdentifierWordStart(runes []rune, idx int) (int, bool) {
	prev := runes[idx-1]
	curr := runes[idx]

	switch {
	case curr == '_':
		return idx + 1, true
	case prev == '_',
		unicode.IsLower(prev) && unicode.IsUpper(curr),
		unicode.IsUpper(prev) && unicode.IsUpper(curr) &&
			idx+1 < len(runes) && unicode.IsLower(runes[idx+1]):
		return idx, true
	default:
		return 0, false
	}
}

func appendIdentifierWord(runes []rune, start, end int, words *[]string) {
	if end <= start {
		return
	}

	word := strings.ToLower(string(runes[start:end]))
	if word == "" {
		return
	}

	*words = append(*words, word)
}

func stripAliasNoise(words []string) []string {
	if len(words) == 0 {
		return nil
	}

	out := make([]string, 0, len(words))
	for _, word := range words {
		switch word {
		case "alias", "copy", "temp", "tmp", "val", "value":
			continue
		default:
			out = append(out, word)
		}
	}

	return out
}

func sameWords(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}

	for idx := range left {
		if left[idx] != right[idx] {
			return false
		}
	}

	return true
}

func normalizeRenderedIdentifier(text string, name string, replacement string) string {
	if name == "" {
		return text
	}

	var out strings.Builder

	runes := []rune(text)

	for idx := 0; idx < len(runes); {
		ch := runes[idx]
		if !isIdentifierStart(ch) {
			out.WriteRune(ch)

			idx++

			continue
		}

		start := idx

		idx++

		for idx < len(runes) && (isIdentifierStart(runes[idx]) || unicode.IsDigit(runes[idx])) {
			idx++
		}

		token := string(runes[start:idx])
		if token == name {
			out.WriteString(replacement)
			continue
		}

		out.WriteString(token)
	}

	return out.String()
}

func isIdentifierStart(ch rune) bool {
	return ch == '_' || unicode.IsLetter(ch)
}

func (l *Runner) objectUseSummary(node ast.Node, target types.Object) objectUseSummary {
	var out objectUseSummary

	inspectWithAncestors(node, func(n ast.Node, ancestors []ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			if nodeUsesObject(n.Body, target, l.pkg.TypesInfo) {
				out.unsafe = true
			}

			return false
		case *ast.Ident:
			if l.pkg.TypesInfo.ObjectOf(n) != target {
				return !out.unsafe
			}

			if objectUseIsUnsafe(n, ancestors) {
				out.unsafe = true
				return false
			}

			out.reads++
		}

		return !out.unsafe
	})

	return out
}

func objectUseIsUnsafe(ident *ast.Ident, ancestors []ast.Node) bool {
	for _, ancestor := range slices.Backward(ancestors) {
		switch node := ancestor.(type) {
		case *ast.UnaryExpr:
			if node.Op == token.AND && nodeContains(node.X, ident) {
				return true
			}
		case *ast.IncDecStmt:
			return true
		case *ast.AssignStmt:
			if nodeContainedInExprList(ident, node.Lhs) {
				return true
			}
		case *ast.RangeStmt:
			if nodeContains(node.Key, ident) || nodeContains(node.Value, ident) {
				return true
			}
		}
	}

	return false
}

func inspectWithAncestors(node ast.Node, fn func(ast.Node, []ast.Node) bool) {
	ancestors := make([]ast.Node, 0, ancestorStackCap)

	ast.Inspect(node, func(n ast.Node) bool {
		if n == nil {
			ancestors = ancestors[:len(ancestors)-1]
			return false
		}

		keep := fn(n, ancestors)
		if keep {
			ancestors = append(ancestors, n)
		}

		return keep
	})
}

func nodeUsesObject(node ast.Node, target types.Object, info *types.Info) bool {
	found := false

	ast.Inspect(node, func(n ast.Node) bool {
		if found {
			return false
		}

		ident, ok := n.(*ast.Ident)
		if !ok {
			return true
		}

		found = info.ObjectOf(ident) == target

		return !found
	})

	return found
}

func nodeContainedInExprList(target ast.Node, exprs []ast.Expr) bool {
	for _, expr := range exprs {
		if nodeContains(expr, target) {
			return true
		}
	}

	return false
}

func nodeContains(root, target ast.Node) bool {
	if root == nil || target == nil {
		return false
	}

	found := false

	ast.Inspect(root, func(n ast.Node) bool {
		if found || n == nil {
			return !found
		}

		if n == target {
			found = true
			return false
		}

		return true
	})

	return found
}
