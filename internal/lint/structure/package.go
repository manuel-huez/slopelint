package structure

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
)

const (
	boolTrueText  = "true"
	boolFalseText = "false"
	nilText       = "nil"
	panicText     = "panic"
	unknownExpr   = "<expr>"
)

// Finding is one structural diagnostic emitted by this package.
type Finding struct {
	Pos     token.Pos
	Kind    string
	Message string
}

// Package carries parsed package data shared by structural checks.
type Package struct {
	Files     []*ast.File
	FSet      *token.FileSet
	TypesPkg  *types.Package
	TypesInfo *types.Info
}

// Runner scans function bodies of one package. Its render cache, report
// dedupe and function body index live across ScanFunctionBody calls.
type Runner struct {
	q       *pkgQuery
	sink    *findingSink
	effects *validationEffects
}

// blockContext describes a statement list; only a function's top-level list
// sets functionBody.
type blockContext struct {
	functionBody       bool
	functionHasResults bool
}

// New creates a reusable structural checker.
func New(pkg *Package) *Runner {
	return &Runner{
		q:       &pkgQuery{pkg: pkg, renderCache: make(map[ast.Node]string)},
		sink:    &findingSink{fset: pkg.FSet, reported: make(map[string]struct{})},
		effects: &validationEffects{pkg: pkg, visiting: make(map[*types.Func]bool)},
	}
}

// ScanFunctionBody checks one function body and returns findings added by this scan.
func (l *Runner) ScanFunctionBody(
	fnType *ast.FuncType,
	recv *ast.FieldList,
	body *ast.BlockStmt,
) []Finding {
	start := len(l.sink.findings)
	info := l.q.pkg.TypesInfo

	inputs := make(map[types.Object]struct{})
	addFieldListObjects(info, inputs, recv)
	addFieldListObjects(info, inputs, fnType.Params)

	ctx := blockContext{
		functionBody:       true,
		functionHasResults: fnType.Results != nil && len(fnType.Results.List) != 0,
	}

	if finding, ok := l.simplificationScore(body, ctx); ok {
		l.sink.report(finding)
	}

	guards := inputGuardScan{
		q:       l.q,
		effects: l.effects,
		temps:   inputs,
		results: funcResultTypes(info, fnType),
	}
	if finding, ok := guards.scattered(body); ok {
		l.sink.report(finding)
	}

	stmtWalker{
		atIndex: func(stmts []ast.Stmt, idx int, ctx blockContext) {
			for _, finding := range l.blockFindingsAt(stmts, idx, ctx) {
				l.sink.report(finding.Finding)
			}
		},
		atStmt: func(stmt ast.Stmt) {
			for _, finding := range l.stmtFindings(stmt) {
				l.sink.report(finding)
			}
		},
	}.walkList(body.List, ctx)

	out := make([]Finding, len(l.sink.findings)-start)
	copy(out, l.sink.findings[start:])

	return out
}

func addFieldListObjects(info *types.Info, out map[types.Object]struct{}, fields *ast.FieldList) {
	if fields == nil {
		return
	}

	for _, field := range fields.List {
		for _, name := range field.Names {
			if name.Name == "_" {
				continue
			}

			out[info.ObjectOf(name)] = struct{}{}
		}
	}
}

func funcResultTypes(info *types.Info, fnType *ast.FuncType) []types.Type {
	if fnType.Results == nil {
		return nil
	}

	out := make([]types.Type, 0, len(fnType.Results.List))
	for _, field := range fnType.Results.List {
		typ := info.TypeOf(field.Type)

		count := max(len(field.Names), 1)
		for range count {
			out = append(out, typ)
		}
	}

	return out
}

// findingSink collects findings in report order and drops exact repeats.
type findingSink struct {
	fset     *token.FileSet
	findings []Finding
	reported map[string]struct{}
}

func (s *findingSink) report(finding Finding) {
	position := s.fset.Position(finding.Pos)
	key := strings.Join([]string{
		position.Filename,
		strconv.Itoa(position.Line),
		strconv.Itoa(position.Column),
		finding.Kind,
		finding.Message,
	}, "\x00")

	if _, exists := s.reported[key]; exists {
		return
	}

	s.reported[key] = struct{}{}
	s.findings = append(s.findings, finding)
}

type pkgQuery struct {
	pkg         *Package
	renderCache map[ast.Node]string
}

func (q *pkgQuery) render(node ast.Node) string {
	if text := q.renderCache[node]; text != "" {
		return text
	}

	var buf bytes.Buffer
	if format.Node(&buf, q.pkg.FSet, node) != nil {
		return unknownExpr
	}

	text := buf.String()
	q.renderCache[node] = text

	return text
}

func (q *pkgQuery) renderStmtList(stmts []ast.Stmt) string {
	if len(stmts) == 0 {
		return "{}"
	}

	parts := make([]string, 0, len(stmts))
	for _, stmt := range stmts {
		parts = append(parts, q.render(stmt))
	}

	return strings.Join(parts, "\n")
}

func (q *pkgQuery) renderCaseClauseHeader(clause *ast.CaseClause) string {
	switch len(clause.List) {
	case 0:
		return "default"
	case 1:
		return q.render(clause.List[0])
	default:
		return fmt.Sprintf("%d values", len(clause.List))
	}
}

func (q *pkgQuery) commentTexts(node ast.Node) []string {
	fset := q.pkg.FSet
	file := fset.File(node.Pos())
	out := make([]string, 0)

	for _, astFile := range q.pkg.Files {
		for _, group := range astFile.Comments {
			if fset.File(group.Pos()) != file || group.Pos() < node.Pos() ||
				group.End() > node.End() {
				continue
			}

			text := strings.Join(strings.Fields(group.Text()), " ")
			if text != "" {
				out = append(out, text)
			}
		}
	}

	return out
}

func (q *pkgQuery) commentsMatch(first, second ast.Node) bool {
	return slices.Equal(q.commentTexts(first), q.commentTexts(second))
}

// hasAttachedComment reports comments inside node, trailing its last line, or
// on the line just above it.
func (q *pkgQuery) hasAttachedComment(node ast.Node) bool {
	fset := q.pkg.FSet
	file := fset.File(node.Pos())
	start := fset.Position(node.Pos()).Line
	end := fset.Position(node.End()).Line

	for _, astFile := range q.pkg.Files {
		for _, group := range astFile.Comments {
			if fset.File(group.Pos()) != file {
				continue
			}

			groupStart := fset.Position(group.Pos()).Line
			if groupStart >= start && groupStart <= end ||
				fset.Position(group.End()).Line == start-1 {
				return true
			}
		}
	}

	return false
}

// plainIfStmt matches an if with no init, no else and no attached comment,
// which checks may rewrite without losing code or notes.
func (q *pkgQuery) plainIfStmt(stmt ast.Stmt) (*ast.IfStmt, bool) {
	ifStmt, ok := stmt.(*ast.IfStmt)
	if !ok || ifStmt.Init != nil || ifStmt.Else != nil || q.hasAttachedComment(ifStmt) {
		return nil, false
	}

	return ifStmt, true
}
