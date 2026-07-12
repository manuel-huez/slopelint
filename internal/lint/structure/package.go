package structure

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

type Runner struct {
	pkg         *Package
	findings    []Finding
	reported    map[string]struct{}
	renderCache map[ast.Node]string
}

type blockContext struct {
	functionBody       bool
	functionHasResults bool
	functionResults    []types.Type
	functionInputs     map[types.Object]struct{}
}

// New creates a reusable structural checker.
func New(pkg *Package) *Runner {
	if pkg == nil {
		pkg = &Package{}
	}

	return &Runner{
		pkg:         pkg,
		findings:    make([]Finding, 0),
		reported:    make(map[string]struct{}),
		renderCache: make(map[ast.Node]string),
	}
}

// ScanFunctionBody checks one function body and returns findings added by this scan.
func (l *Runner) ScanFunctionBody(
	fnType *ast.FuncType,
	recv *ast.FieldList,
	body *ast.BlockStmt,
) []Finding {
	start := len(l.findings)

	if body != nil {
		ctx := blockContext{
			functionBody: true,
			functionHasResults: fnType != nil && fnType.Results != nil &&
				len(fnType.Results.List) != 0,
			functionResults: l.funcResultTypes(fnType),
			functionInputs:  l.funcInputObjects(fnType, recv),
		}

		l.checkFunctionComplexity(body, ctx)
		l.scanStructuralBlock(body.List, ctx)
	}

	out := make([]Finding, len(l.findings)-start)
	copy(out, l.findings[start:])

	return out
}

func (l *Runner) funcInputObjects(
	fnType *ast.FuncType,
	recv *ast.FieldList,
) map[types.Object]struct{} {
	out := make(map[types.Object]struct{})

	l.addFieldListObjects(out, recv)

	if fnType != nil {
		l.addFieldListObjects(out, fnType.Params)
	}

	return out
}

func (l *Runner) addFieldListObjects(
	out map[types.Object]struct{},
	fields *ast.FieldList,
) {
	if fields == nil {
		return
	}

	for _, field := range fields.List {
		for _, name := range field.Names {
			if name == nil || name.Name == "_" {
				continue
			}

			if obj := l.pkg.TypesInfo.ObjectOf(name); obj != nil {
				out[obj] = struct{}{}
			}
		}
	}
}

func (l *Runner) funcResultTypes(fnType *ast.FuncType) []types.Type {
	if fnType == nil || fnType.Results == nil {
		return nil
	}

	out := make([]types.Type, 0, len(fnType.Results.List))
	for _, field := range fnType.Results.List {
		typ := l.pkg.TypesInfo.TypeOf(field.Type)
		if typ == nil {
			continue
		}

		count := 1
		if len(field.Names) > 0 {
			count = len(field.Names)
		}

		for range count {
			out = append(out, typ)
		}
	}

	return out
}

func (l *Runner) report(pos token.Pos, kind, msg string) {
	position := l.pkg.FSet.Position(pos)

	if _, exists := l.reported[structureReportKey(position, kind, msg)]; exists {
		return
	}

	l.reported[structureReportKey(position, kind, msg)] = struct{}{}
	l.findings = append(l.findings, Finding{Pos: pos, Kind: kind, Message: msg})
}

func structureReportKey(position token.Position, kind, msg string) string {
	var b strings.Builder
	b.WriteString(position.Filename)
	b.WriteByte('#')
	b.WriteString(strconv.Itoa(position.Line))
	b.WriteByte(':')
	b.WriteString(strconv.Itoa(position.Column))
	b.WriteByte(':')
	b.WriteString(kind)
	b.WriteByte(':')
	b.WriteString(msg)

	return b.String()
}

func (l *Runner) render(node ast.Node) string {
	if node == nil {
		return ""
	}

	if text := l.renderCache[node]; text != "" {
		return text
	}

	var buf bytes.Buffer
	if format.Node(&buf, l.pkg.FSet, node) != nil {
		return unknownExpr
	}

	text := buf.String()
	l.renderCache[node] = text

	return text
}

func (l *Runner) unparen(expr ast.Expr) ast.Expr {
	for {
		paren, ok := expr.(*ast.ParenExpr)
		if !ok {
			return expr
		}

		expr = paren.X
	}
}

func (l *Runner) renderCaseClauseHeader(clause *ast.CaseClause) string {
	if clause == nil || len(clause.List) == 0 {
		return "default"
	}

	if len(clause.List) == 1 {
		return l.render(clause.List[0])
	}

	return fmt.Sprintf("%d values", len(clause.List))
}
