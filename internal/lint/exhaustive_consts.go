package lint

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"strings"
)

const minimumClosedConstSetSize = 2

type constSetCoverage struct {
	defaultClause *ast.CaseClause
	typeName      string
	named         *types.Named
	universe      map[string]string
	covered       map[string]struct{}
}

func (l *linter) checkExhaustiveConstSetDefault(stmt *ast.SwitchStmt) {
	coverage, ok := l.constSetSwitchCoverage(stmt)
	if !ok || !coverage.exhaustive() {
		return
	}

	if isImpossibleStatePanic(coverage.defaultClause.Body, l.pkg.TypesInfo) {
		return
	}

	if !constSetIncludesZeroValue(coverage.universe, coverage.named) {
		return
	}

	if l.defaultHandlesInvalidConstSetValue(coverage.defaultClause.Body) {
		return
	}

	l.report(
		coverage.defaultClause.Case,
		"redundant_default",
		fmt.Sprintf(
			"default case is redundant; %s switch covers all in-package constants",
			coverage.typeName,
		),
	)
}

func (l *linter) constSetSwitchCoverage(stmt *ast.SwitchStmt) (constSetCoverage, bool) {
	named, ok := l.closedConstSetSwitchType(stmt)
	if !ok {
		return constSetCoverage{}, false
	}

	universe, ok := l.constSetValues(named)
	if !ok {
		return constSetCoverage{}, false
	}

	coverage := constSetCoverage{
		typeName: named.Obj().Name(),
		named:    named,
		universe: universe,
		covered:  make(map[string]struct{}, len(universe)),
	}

	for _, raw := range stmt.Body.List {
		clause, ok := raw.(*ast.CaseClause)
		if !ok || clause == nil {
			continue
		}

		if len(clause.List) == 0 {
			coverage.defaultClause = clause
			continue
		}

		if !l.addConstSetCaseCoverage(clause.List, &coverage) {
			return constSetCoverage{}, false
		}
	}

	return coverage, true
}

func (l *linter) closedConstSetSwitchType(stmt *ast.SwitchStmt) (*types.Named, bool) {
	named, ok := l.pkg.TypesInfo.TypeOf(stmt.Tag).(*types.Named)
	if !ok || named == nil || named.Obj() == nil || named.Obj().Pkg() == nil {
		return nil, false
	}

	if named.Obj().Pkg().Path() != l.pkg.TypesPkg.Path() || ast.IsExported(named.Obj().Name()) {
		return nil, false
	}

	return named, true
}

func (l *linter) constSetValues(named *types.Named) (map[string]string, bool) {
	values := make(map[string]string)

	for _, file := range l.pkg.Files {
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}

			if !l.addConstSetValuesFromDecl(gen, named, values) {
				return nil, false
			}
		}
	}

	return values, len(values) >= minimumClosedConstSetSize
}

func (l *linter) addConstSetValuesFromDecl(
	decl *ast.GenDecl,
	named *types.Named,
	values map[string]string,
) bool {
	for _, spec := range decl.Specs {
		valueSpec, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}

		for _, name := range valueSpec.Names {
			obj, ok := l.pkg.TypesInfo.Defs[name].(*types.Const)
			if !ok || obj == nil || !types.Identical(obj.Type(), named) {
				continue
			}

			value, ok := scalarFromConstantValue(obj.Val())
			if !ok {
				return false
			}

			key := value.key()
			if prior, dup := values[key]; dup && prior != name.Name {
				return false
			}

			values[key] = name.Name
		}
	}

	return true
}

func (l *linter) addConstSetCaseCoverage(
	list []ast.Expr,
	coverage *constSetCoverage,
) bool {
	for _, expr := range list {
		value, ok := l.scalarOf(expr)
		if !ok {
			return false
		}

		key := value.key()
		if _, known := coverage.universe[key]; !known {
			return false
		}

		coverage.covered[key] = struct{}{}
	}

	return true
}

func (coverage constSetCoverage) exhaustive() bool {
	if coverage.defaultClause == nil || len(coverage.universe) == 0 {
		return false
	}

	for key := range coverage.universe {
		if _, ok := coverage.covered[key]; !ok {
			return false
		}
	}

	return true
}

func constSetIncludesZeroValue(universe map[string]string, named *types.Named) bool {
	value, ok := zeroValueScalarForNamed(named)
	if !ok {
		return false
	}

	_, ok = universe[value.key()]

	return ok
}

func zeroValueScalarForNamed(named *types.Named) (scalar, bool) {
	if named == nil {
		return scalar{}, false
	}

	basic, ok := named.Underlying().(*types.Basic)
	if !ok {
		return scalar{}, false
	}

	switch {
	case basic.Info()&types.IsInteger != 0:
		return scalar{kind: scalarInt, text: zeroIntText}, true
	case basic.Info()&types.IsString != 0:
		return scalar{kind: scalarString, text: ""}, true
	case basic.Info()&types.IsBoolean != 0:
		return scalar{kind: scalarBool, text: boolFalseText}, true
	default:
		return scalar{}, false
	}
}

func (l *linter) defaultHandlesInvalidConstSetValue(body []ast.Stmt) bool {
	text := strings.ToLower(l.renderStmtList(body))

	return strings.Contains(text, "invalid") ||
		strings.Contains(text, "unsupported") ||
		strings.Contains(text, "unknown")
}
