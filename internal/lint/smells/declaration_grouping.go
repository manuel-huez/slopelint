package smells

import (
	"fmt"
	"go/ast"
	"go/token"
	"sort"
	"strings"
)

const ownerFileLineLimit = 1000

func (l *Runner) checkOversizedOwnerFiles() {
	for _, file := range l.pkg.Files {
		if file == nil || ast.IsGenerated(file) {
			continue
		}

		tokenFile := l.pkg.FSet.File(file.Pos())
		if tokenFile == nil || tokenFile.LineCount() <= ownerFileLineLimit {
			continue
		}

		l.report(
			file.Package,
			"oversized_owner_file",
			fmt.Sprintf(
				"source file has %d lines; split by responsibility so whole-file review stays tractable",
				tokenFile.LineCount(),
			),
		)
	}
}

const (
	ungroupedDeclItemLimit        = 10
	constNamePrefixMinimumWords   = 2
	mixedConstPrefixItemMinimum   = 6
	mixedConstPrefixMinimumGroups = 3
	mixedConstPrefixMinimumSize   = 2
)

type declGroupingRule struct {
	token token.Token
	kind  string
	label string
}

var declGroupingRules = map[token.Token]declGroupingRule{
	token.CONST: {token: token.CONST, kind: "const_grouping", label: "const"},
	token.TYPE:  {token: token.TYPE, kind: "type_grouping", label: "type"},
	token.VAR:   {token: token.VAR, kind: "var_grouping", label: "var"},
}

type declGroupRun struct {
	pos    token.Pos
	count  int
	rule   declGroupingRule
	prefix map[string]int
}

func (l *Runner) checkDeclarationGrouping() {
	for _, file := range l.pkg.Files {
		l.checkDeclRuns(file.Decls)
	}
}

func (l *Runner) checkDeclRuns(decls []ast.Decl) {
	var run declGroupRun

	var previous ast.Decl

	for _, decl := range decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok {
			l.reportDeclGroupRun(run)
			run = declGroupRun{}
			previous = nil

			continue
		}

		rule, supported := declGroupingRules[gen.Tok]
		if !supported {
			l.reportDeclGroupRun(run)
			run = declGroupRun{}
			previous = nil

			continue
		}

		if gen.Lparen.IsValid() {
			l.reportDeclGroupRun(run)
			l.checkGroupedDeclItems(gen, rule)

			run = declGroupRun{}
			previous = nil

			continue
		}

		if previous == nil || gen.Tok != run.rule.token || l.declNodeStartsGroup(previous, gen) {
			l.reportDeclGroupRun(run)
			run = newDeclGroupRun(gen.Pos(), rule)
		}

		run.addNames(declSpecNames(gen.Specs))
		previous = gen
	}

	l.reportDeclGroupRun(run)
}

func (l *Runner) checkGroupedDeclItems(decl *ast.GenDecl, rule declGroupingRule) {
	run := declGroupRun{}

	var previous ast.Spec

	for _, spec := range decl.Specs {
		if previous == nil || l.declNodeStartsGroup(previous, spec) {
			l.reportDeclGroupRun(run)
			run = newDeclGroupRun(spec.Pos(), rule)
		}

		run.addNames(declSpecNames([]ast.Spec{spec}))
		previous = spec
	}

	l.reportDeclGroupRun(run)
}

func newDeclGroupRun(pos token.Pos, rule declGroupingRule) declGroupRun {
	return declGroupRun{
		pos:    pos,
		rule:   rule,
		prefix: make(map[string]int),
	}
}

func (run *declGroupRun) addNames(names []string) {
	for _, name := range names {
		run.count++

		if run.rule.token != token.CONST {
			continue
		}

		if prefix := constNamePrefix(name); prefix != "" {
			run.prefix[prefix]++
		}
	}
}

func (l *Runner) declNodeStartsGroup(previous ast.Node, current ast.Node) bool {
	if previous == nil || current == nil || l.pkg.FSet == nil {
		return false
	}

	previousEndLine := l.pkg.FSet.Position(previous.End()).Line
	currentStartLine := l.pkg.FSet.Position(current.Pos()).Line

	// Line gaps cover blank lines and standalone comments between decl items.
	return currentStartLine > previousEndLine+1
}

func declSpecNames(specs []ast.Spec) []string {
	var names []string

	for _, spec := range specs {
		switch spec := spec.(type) {
		case *ast.TypeSpec:
			if spec.Name != nil {
				names = append(names, spec.Name.Name)
			}
		case *ast.ValueSpec:
			for _, name := range spec.Names {
				if name != nil {
					names = append(names, name.Name)
				}
			}
		}
	}

	return names
}

func (l *Runner) reportDeclGroupRun(run declGroupRun) {
	if run.pos == token.NoPos {
		return
	}

	if run.count > ungroupedDeclItemLimit {
		l.report(
			run.pos,
			run.rule.kind,
			fmt.Sprintf(
				"%s chunk has %d consecutive items; split related declarations with blank lines, group comments, or smaller %s blocks",
				run.rule.label,
				run.count,
				run.rule.label,
			),
		)
	}

	l.reportMixedConstPrefixes(run)
}

func (l *Runner) reportMixedConstPrefixes(run declGroupRun) {
	if run.rule.token != token.CONST || run.count < mixedConstPrefixItemMinimum {
		return
	}

	prefixes := mixedConstPrefixes(run.prefix)
	if len(prefixes) < mixedConstPrefixMinimumGroups {
		return
	}

	l.report(
		run.pos,
		"mixed_const_prefixes",
		fmt.Sprintf(
			"const chunk mixes %s prefixes without grouping; split unrelated const families with blank lines or group comments",
			strings.Join(prefixes, ", "),
		),
	)
}

func mixedConstPrefixes(counts map[string]int) []string {
	prefixes := make([]string, 0, len(counts))

	for prefix, count := range counts {
		if count >= mixedConstPrefixMinimumSize {
			prefixes = append(prefixes, prefix)
		}
	}

	sort.Strings(prefixes)

	return prefixes
}

func constNamePrefix(name string) string {
	words := splitIdentifierWords(name)
	if len(words) < constNamePrefixMinimumWords {
		return ""
	}

	return words[0]
}
