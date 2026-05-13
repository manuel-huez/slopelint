package smells

import (
	"fmt"
	"go/ast"
	"go/token"
)

const ungroupedConstItemLimit = 10

func (l *Runner) checkLargeUngroupedConstChunks() {
	for _, file := range l.pkg.Files {
		l.checkConstDeclRuns(file.Decls)
	}
}

func (l *Runner) checkConstDeclRuns(decls []ast.Decl) {
	runItems := 0
	runStart := token.NoPos

	var previous ast.Decl

	for _, decl := range decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			l.reportLargeUngroupedConstChunk(runStart, runItems)

			runItems = 0
			previous = nil

			continue
		}

		if gen.Lparen.IsValid() {
			l.reportLargeUngroupedConstChunk(runStart, runItems)
			l.checkConstDeclGrouping(gen)

			runItems = 0
			previous = nil

			continue
		}

		if previous == nil || l.constNodeStartsGroup(previous, gen) {
			l.reportLargeUngroupedConstChunk(runStart, runItems)

			runItems = 0
			runStart = gen.Pos()
		}

		runItems += constDeclItemCount(gen)
		previous = gen
	}

	l.reportLargeUngroupedConstChunk(runStart, runItems)
}

func (l *Runner) checkConstDeclGrouping(decl *ast.GenDecl) {
	runItems := 0
	runStart := token.NoPos

	var previous ast.Spec

	for _, spec := range decl.Specs {
		valueSpec, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}

		if previous == nil || l.constNodeStartsGroup(previous, valueSpec) {
			l.reportLargeUngroupedConstChunk(runStart, runItems)

			runItems = 0
			runStart = valueSpec.Pos()
		}

		runItems += len(valueSpec.Names)
		previous = valueSpec
	}

	l.reportLargeUngroupedConstChunk(runStart, runItems)
}

func (l *Runner) constNodeStartsGroup(previous ast.Node, current ast.Node) bool {
	if previous == nil || current == nil || l.pkg.FSet == nil {
		return false
	}

	previousEndLine := l.pkg.FSet.Position(previous.End()).Line
	currentStartLine := l.pkg.FSet.Position(current.Pos()).Line

	// Line gaps cover blank lines and standalone comments between const items.
	return currentStartLine > previousEndLine+1
}

func constDeclItemCount(decl *ast.GenDecl) int {
	count := 0

	for _, spec := range decl.Specs {
		valueSpec, ok := spec.(*ast.ValueSpec)
		if ok {
			count += len(valueSpec.Names)
		}
	}

	return count
}

func (l *Runner) reportLargeUngroupedConstChunk(pos token.Pos, items int) {
	if pos == token.NoPos || items <= ungroupedConstItemLimit {
		return
	}

	l.report(
		pos,
		"const_grouping",
		fmt.Sprintf(
			"const chunk has %d consecutive items; split related constants with blank lines, group comments, or smaller const blocks",
			items,
		),
	)
}
