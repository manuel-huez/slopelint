package smells

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/token"
	"go/types"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/manuel-huez/slopelint/internal/lint/outline"
)

// findingSink collects findings in report order and drops exact repeats.
type findingSink struct {
	fset     *token.FileSet
	findings []Finding
	reported map[string]struct{}
}

func newFindingSink(fset *token.FileSet) *findingSink {
	return &findingSink{
		fset:     fset,
		findings: []Finding{},
		reported: map[string]struct{}{},
	}
}

func (s *findingSink) report(pos token.Pos, kind, msg string) {
	position := s.fset.Position(pos)
	key := strings.Join([]string{
		position.Filename,
		strconv.Itoa(position.Line),
		strconv.Itoa(position.Column),
		kind,
		msg,
	}, "\x00")

	if _, exists := s.reported[key]; exists {
		return
	}

	s.reported[key] = struct{}{}
	s.findings = append(s.findings, Finding{Pos: pos, Kind: kind, Message: msg})
}

// pkgQuery answers syntax and position queries over one package. Its caches
// live for one Run call, so every rule family in that run shares them.
type pkgQuery struct {
	pkg           *Package
	renderCache   map[ast.Node]string
	funcUseCounts map[string]int
}

type ruleEnv struct {
	*pkgQuery
	*findingSink
}

func newRuleEnv(pkg *Package) ruleEnv {
	return ruleEnv{
		pkgQuery:    &pkgQuery{pkg: pkg, renderCache: map[ast.Node]string{}},
		findingSink: newFindingSink(pkg.FSet),
	}
}

// forEachHandwrittenProductionFunc skips generated files, which
// ProductionFuncs keeps for dead-code roots and behavior checks.
func (q *pkgQuery) forEachHandwrittenProductionFunc(fn func(*ast.FuncDecl)) {
	for _, file := range q.pkg.ProductionFiles {
		if ast.IsGenerated(file) {
			continue
		}

		for _, decl := range file.Decls {
			if funcDecl, ok := decl.(*ast.FuncDecl); ok {
				fn(funcDecl)
			}
		}
	}
}

func (q *pkgQuery) render(node ast.Node) string {
	if cached := q.renderCache[node]; cached != "" {
		return cached
	}

	var buf bytes.Buffer
	if err := format.Node(&buf, q.pkg.FSet, node); err != nil {
		return "<expr>"
	}

	rendered := buf.String()
	q.renderCache[node] = rendered

	return rendered
}

func (q *pkgQuery) fileIsTest(file *ast.File) bool {
	return strings.HasSuffix(q.fileName(file), "_test.go")
}

func (q *pkgQuery) fileName(file *ast.File) string {
	if tokenFile := q.pkg.FSet.File(file.Pos()); tokenFile != nil {
		return tokenFile.Name()
	}

	return ""
}

func (q *pkgQuery) hasAttachedComment(node ast.Node, ignored *ast.CommentGroup) bool {
	fset := q.pkg.FSet

	nodeFile := fset.File(node.Pos())
	if nodeFile == nil {
		return false
	}

	start := fset.Position(node.Pos()).Line
	end := fset.Position(node.End()).Line

	for _, file := range q.pkg.Files {
		for _, group := range file.Comments {
			if group == ignored || fset.File(group.Pos()) != nodeFile {
				continue
			}

			if commentTouchesLines(
				fset.Position(group.Pos()).Line,
				fset.Position(group.End()).Line,
				start,
				end,
			) {
				return true
			}
		}
	}

	return false
}

func (q *pkgQuery) positionText(pos token.Pos) string {
	position := q.pkg.FSet.Position(pos)
	if !position.IsValid() {
		return unknownPos
	}

	return fmt.Sprintf("%s:%d", position.Filename, position.Line)
}

func (q *pkgQuery) productionPackageFuncUseCounts() map[string]int {
	if q.funcUseCounts != nil {
		return q.funcUseCounts
	}

	counts := make(map[string]int)

	for _, file := range q.pkg.ProductionFiles {
		ast.PreorderStack(file, nil, func(n ast.Node, stack []ast.Node) bool {
			expr, ok := n.(ast.Expr)
			if !ok || funcUseExprIsDeclarationOrSelectorName(expr, stack) {
				return true
			}

			if obj := funcObject(q.pkg.TypesInfo, expr); obj != nil {
				counts[FuncObjectKey(obj)]++
			}

			return true
		})
	}

	q.funcUseCounts = counts

	return counts
}

func funcUseExprIsDeclarationOrSelectorName(expr ast.Expr, stack []ast.Node) bool {
	if len(stack) == 0 {
		return false
	}

	parent := stack[len(stack)-1]

	ident, ok := expr.(*ast.Ident)
	if !ok {
		return false
	}

	switch parent := parent.(type) {
	case *ast.FuncDecl:
		return parent.Name == ident
	case *ast.SelectorExpr:
		return parent.Sel == ident
	case *ast.Field:
		return slices.Contains(parent.Names, ident)
	default:
		return false
	}
}

func calledFunc(info *types.Info, call *ast.CallExpr) (*types.Func, string, bool) {
	obj := funcObject(info, ast.Unparen(call.Fun))
	if obj == nil {
		return nil, "", false
	}

	return obj, FuncObjectKey(obj), true
}

func isBuiltinCall(info *types.Info, call *ast.CallExpr, name string) bool {
	ident, ok := ast.Unparen(call.Fun).(*ast.Ident)
	if !ok || ident.Name != name {
		return false
	}

	_, ok = info.ObjectOf(ident).(*types.Builtin)

	return ok
}

func isPackageFuncCall(info *types.Info, call *ast.CallExpr, pkgPath, name string) bool {
	fn, _, ok := calledFunc(info, call)

	return ok && fn.Pkg() != nil && fn.Pkg().Path() == pkgPath && fn.Name() == name
}

func funcObject(info *types.Info, expr ast.Expr) *types.Func {
	switch expr := expr.(type) {
	case *ast.Ident:
		obj, _ := info.ObjectOf(expr).(*types.Func)
		return obj
	case *ast.SelectorExpr:
		if selection := info.Selections[expr]; selection != nil {
			obj, _ := selection.Obj().(*types.Func)
			return obj
		}

		obj, _ := info.ObjectOf(expr.Sel).(*types.Func)

		return obj
	default:
		return nil
	}
}

// FuncObjectKey identifies a function across packages and type-checker
// instances; generic instances map to their origin.
func FuncObjectKey(obj *types.Func) string {
	obj = obj.Origin()

	pkgPath := ""
	if obj.Pkg() != nil {
		pkgPath = obj.Pkg().Path()
	}

	return pkgPath + "|" + obj.FullName()
}

func funcDeclObject(info *types.Info, fn *ast.FuncDecl) (*types.Func, bool) {
	obj, ok := info.ObjectOf(fn.Name).(*types.Func)

	return obj, ok && obj != nil
}

// privateFuncObject returns the object of a private, non-generic function or
// method with a body; init is excluded because nothing can call it.
func privateFuncObject(info *types.Info, fn *ast.FuncDecl) (*types.Func, bool) {
	if fn.Body == nil ||
		ast.IsExported(fn.Name.Name) ||
		hasTypeParams(fn.Type) ||
		fn.Name.Name == initFuncName {
		return nil, false
	}

	return funcDeclObject(info, fn)
}

func identObject(info *types.Info, expr ast.Expr) types.Object {
	ident, ok := ast.Unparen(expr).(*ast.Ident)
	if !ok {
		return nil
	}

	return info.ObjectOf(ident)
}

func commentTouchesLines(commentStart, commentEnd, nodeStart, nodeEnd int) bool {
	return commentStart >= nodeStart && commentStart <= nodeEnd ||
		commentStart == nodeEnd ||
		commentEnd == nodeStart-1
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

// signatureFixed reports functions whose signature callers do not choose:
// ones passed as a value, such as a callback, and methods that implement an
// interface of the package. Their parameters and wrappers must stay as they
// are.
func signatureFixed(pkg *Package) func(*types.Func) bool {
	values := funcValues(pkg)

	var interfaces []*types.Interface

	for _, name := range pkg.TypesPkg.Scope().Names() {
		if iface, ok := pkg.TypesPkg.Scope().Lookup(name).Type().Underlying().(*types.Interface); ok {
			interfaces = append(interfaces, iface)
		}
	}

	return func(fn *types.Func) bool {
		fn = fn.Origin()
		if values[fn] {
			return true
		}

		recv := fn.Signature().Recv()
		if recv == nil {
			return false
		}

		return slices.ContainsFunc(interfaces, func(iface *types.Interface) bool {
			method, _, _ := types.LookupFieldOrMethod(iface, false, fn.Pkg(), fn.Name())

			return method != nil &&
				(types.Implements(recv.Type(), iface) ||
					types.Implements(types.NewPointer(recv.Type()), iface))
		})
	}
}

// funcValues collects the functions the production files use other than by
// calling them, alone or as x.Name.
func funcValues(pkg *Package) map[*types.Func]bool {
	values := make(map[*types.Func]bool)

	for _, file := range pkg.ProductionFiles {
		ast.PreorderStack(file, nil, func(node ast.Node, stack []ast.Node) bool {
			ident, ok := node.(*ast.Ident)
			if !ok || len(stack) == 0 {
				return true
			}

			fn, ok := pkg.TypesInfo.Uses[ident].(*types.Func)
			if !ok {
				return true
			}

			var callee ast.Expr = ident

			parent := stack[len(stack)-1]
			if selector, ok := parent.(*ast.SelectorExpr); ok && selector.Sel == ident &&
				len(stack) > 1 {
				callee, parent = selector, stack[len(stack)-2]
			}

			if call, ok := parent.(*ast.CallExpr); !ok || ast.Unparen(call.Fun) != callee {
				values[fn.Origin()] = true
			}

			return true
		})
	}

	return values
}

const initFuncName = "init"

func reportGenericNameForHelper(sink *findingSink, fn *ast.FuncDecl, reason string) {
	if !outline.GenericName(fn.Name.Name) {
		return
	}

	sink.report(
		fn.Name.Pos(),
		"generic_naming",
		fmt.Sprintf(
			`private helper %q has generic name and %s`,
			fn.Name.Name,
			reason,
		),
	)
}

func funcParamCount(fields *ast.FieldList) int {
	if fields == nil {
		return 0
	}

	count := 0

	for _, field := range fields.List {
		if len(field.Names) == 0 {
			count++
			continue
		}

		count += len(field.Names)
	}

	return count
}

func isBoolType(t types.Type) bool {
	basic, ok := types.Unalias(t).Underlying().(*types.Basic)

	return ok && basic.Info()&types.IsBoolean != 0
}

func isErrorType(t types.Type) bool {
	errorType := types.Universe.Lookup("error")

	return errorType != nil && types.Identical(types.Unalias(t), errorType.Type())
}

// IsPredicateName reports names such as IsReady: "Is" followed by an
// uppercase letter, digit, or underscore.
func IsPredicateName(name string) bool {
	rest, ok := strings.CutPrefix(name, "Is")
	if !ok || rest == "" {
		return false
	}

	r, _ := utf8.DecodeRuneInString(rest)

	return r == '_' || unicode.IsUpper(r) || unicode.IsDigit(r)
}
