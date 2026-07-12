package smells

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/token"
	"go/types"
	"strconv"
	"strings"
)

func newRunner(pkg *Package) *Runner {
	if pkg == nil {
		pkg = &Package{}
	}

	return &Runner{
		pkg:         pkg,
		findings:    []Finding{},
		reported:    map[string]struct{}{},
		renderCache: map[ast.Node]string{},
	}
}

func (l *Runner) report(pos token.Pos, kind, msg string) {
	position := l.pkg.FSet.Position(pos)
	key := strings.Join([]string{
		position.Filename,
		strconv.Itoa(position.Line),
		strconv.Itoa(position.Column),
		kind,
		msg,
	}, "\x00")

	if _, exists := l.reported[key]; exists {
		return
	}

	l.reported[key] = struct{}{}
	l.findings = append(l.findings, Finding{Pos: pos, Kind: kind, Message: msg})
}

func (l *Runner) forEachProductionFunc(fn func(*ast.FuncDecl)) {
	for idx := range l.pkg.ProductionFuncs {
		fn(l.pkg.ProductionFuncs[idx])
	}
}

func (l *Runner) forEachProductionTypeSpec(fn func(*ast.TypeSpec)) {
	for idx := range l.pkg.ProductionTypes {
		fn(l.pkg.ProductionTypes[idx])
	}
}

func (l *Runner) forEachProductionDecl(fn func(ast.Decl)) {
	for idx := range l.pkg.ProductionDecls {
		fn(l.pkg.ProductionDecls[idx])
	}
}

func (l *Runner) forEachPackageFunc(fn func(*ast.FuncDecl)) {
	for _, file := range l.pkg.Files {
		for _, decl := range file.Decls {
			if funcDecl, ok := decl.(*ast.FuncDecl); ok {
				fn(funcDecl)
			}
		}
	}
}

func (l *Runner) render(node ast.Node) string {
	if node == nil {
		return ""
	}

	if cached := l.renderCache[node]; cached != "" {
		return cached
	}

	var buf bytes.Buffer
	if err := format.Node(&buf, l.pkg.FSet, node); err != nil {
		return "<expr>"
	}

	rendered := buf.String()
	l.renderCache[node] = rendered

	return rendered
}

func (l *Runner) unparen(expr ast.Expr) ast.Expr {
	for paren, ok := expr.(*ast.ParenExpr); ok; paren, ok = expr.(*ast.ParenExpr) {
		expr = paren.X
	}

	return expr
}

func (l *Runner) calledFunc(call *ast.CallExpr) (*types.Func, string, bool) {
	if call == nil {
		return nil, "", false
	}

	obj := l.funcObject(l.unparen(call.Fun))
	if obj == nil {
		return nil, "", false
	}

	return obj, funcObjectKey(obj), true
}

func (l *Runner) isBuiltinCall(call *ast.CallExpr, name string) bool {
	if call == nil {
		return false
	}

	ident, ok := l.unparen(call.Fun).(*ast.Ident)
	if !ok || ident.Name != name {
		return false
	}

	builtin, ok := l.pkg.TypesInfo.ObjectOf(ident).(*types.Builtin)

	return ok && builtin != nil && builtin.Name() == name
}

func (l *Runner) funcObject(expr ast.Expr) *types.Func {
	switch expr := expr.(type) {
	case *ast.Ident:
		obj, _ := l.pkg.TypesInfo.ObjectOf(expr).(*types.Func)
		return obj
	case *ast.SelectorExpr:
		if selection := l.pkg.TypesInfo.Selections[expr]; selection != nil {
			obj, _ := selection.Obj().(*types.Func)
			return obj
		}

		obj, _ := l.pkg.TypesInfo.ObjectOf(expr.Sel).(*types.Func)

		return obj
	default:
		return nil
	}
}

func funcObjectKey(obj *types.Func) string {
	origin := obj.Origin()

	var b strings.Builder
	if pkg := origin.Pkg(); pkg != nil {
		b.WriteString(pkg.Path())
	}

	b.WriteByte('|')
	b.WriteString(origin.FullName())

	return b.String()
}

func (l *Runner) fileIsTest(file *ast.File) bool {
	return l.fileName(file) != "" && strings.HasSuffix(l.fileName(file), "_test.go")
}

func (l *Runner) fileName(file *ast.File) string {
	if file == nil || l.pkg.FSet == nil {
		return ""
	}

	if tokenFile := l.pkg.FSet.File(file.Pos()); tokenFile != nil {
		return tokenFile.Name()
	}

	return ""
}

func (l *Runner) hasAttachedComment(node ast.Node, ignored *ast.CommentGroup) bool {
	if node == nil || l.pkg.FSet == nil {
		return false
	}

	nodeFile := l.pkg.FSet.File(node.Pos())
	if nodeFile == nil {
		return false
	}

	start := l.pkg.FSet.Position(node.Pos()).Line
	end := l.pkg.FSet.Position(node.End()).Line

	for _, file := range l.pkg.Files {
		for _, group := range file.Comments {
			if group == ignored {
				continue
			}

			if l.pkg.FSet.File(group.Pos()) != nodeFile {
				continue
			}

			if commentTouchesLines(
				l.pkg.FSet.Position(group.Pos()).Line,
				l.pkg.FSet.Position(group.End()).Line,
				start,
				end,
			) {
				return true
			}
		}
	}

	return false
}

func commentTouchesLines(commentStart, commentEnd, nodeStart, nodeEnd int) bool {
	return commentStart >= nodeStart && commentStart <= nodeEnd ||
		commentStart == nodeEnd ||
		commentEnd == nodeStart-1
}

func (l *Runner) positionText(pos token.Pos) string {
	position := l.pkg.FSet.Position(pos)
	if !position.IsValid() {
		return unknownPos
	}

	return fmt.Sprintf("%s:%d", position.Filename, position.Line)
}

func inspectNodesBetween(
	root ast.Node,
	after token.Pos,
	before token.Pos,
	visit func(ast.Node) bool,
) {
	ast.Inspect(root, func(node ast.Node) bool {
		if node == nil {
			return false
		}

		if node.End() <= after || node.Pos() >= before {
			return false
		}

		if node.Pos() <= after {
			return true
		}

		return visit(node)
	})
}
