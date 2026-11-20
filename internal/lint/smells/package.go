package smells

import (
	"go/ast"
	"go/token"
	"go/types"
)

const (
	boolTrueText      = "true"
	boolFalseText     = "false"
	nilText           = "nil"
	panicText         = "panic"
	stringsImportPath = "strings"
	testingImportPath = "testing"
	unknownPos        = "unknown position"
)

// Finding is one smell diagnostic emitted by this package.
type Finding struct {
	Pos     token.Pos
	Kind    string
	Message string
}

// Package carries parsed package data shared by all smell checks.
type Package struct {
	Files            []*ast.File
	ProductionFiles  []*ast.File
	TestFiles        []*ast.File
	TestSupportFiles []*ast.File
	ProductionDecls  []ast.Decl
	ProductionFuncs  []*ast.FuncDecl
	ProductionTypes  []*ast.TypeSpec
	FSet             *token.FileSet
	TypesPkg         *types.Package
	TypesInfo        *types.Info
	// ImportedParallelParams returns ParallelParams of a function in another
	// package; nil when no summaries are available.
	ImportedParallelParams func(*types.Func) []int
}

// RunDefault runs smell checks enabled by default.
func RunDefault(pkg *Package) []Finding {
	env := newRuleEnv(pkg)
	forwarderCheck{env}.check()
	normalizationCheck{env}.check()
	serializationCheck{env}.check()
	commentCheck{env}.checkRestatements()
	commentCheck{env}.checkStaleComplexitySuppressions()
	apiCheck{env}.checkPredicateSignatures()
	apiCheck{env}.checkUnusedPrivateParams()
	declGroupingCheck{env}.check()
	sizeLimitCheck{env}.check()
	errorCheck{env}.checkSwallowed()
	tableTestCheck{env}.checkUnnamedLargeTables()
	tableTestCheck{env}.checkRepeatedFixtures()
	constValueTestCheck{env}.check()
	testSupportCheck{env}.checkFilenames()
	testSupportCheck{env}.checkSleeps()
	testParallelCheck{env}.check()

	return env.findings
}

// RunPackage runs package-wide smell checks enabled by --package.
func RunPackage(pkg *Package) []Finding {
	env := newRuleEnv(pkg)
	singleUseHelperCheck{env}.check()
	singleImplCheck{env}.check()
	optionsOverkillCheck{env}.check()
	resultWrapperCheck{env}.check()
	testSupportCheck{env}.checkGlobalFuncStubs()
	testSupportCheck{env}.checkFatalPanics()
	apiCheck{env}.checkBoolModeParams()
	zeroValueArgCheck{env}.check()
	apiCheck{env}.checkOptionalResultTriples()
	errorCheck{env}.checkPanics()
	errorCheck{env}.checkSentinelBreaks()

	return env.findings
}

// RunBehaviorPackage reports behavior clones inside one package.
func RunBehaviorPackage(pkg *Package) []Finding {
	analysis := buildBehaviorSSAAnalysis([]*Package{pkg})
	b := behaviorPackage{pkg: pkg, calls: analysis.objectSummary}

	return reportBehaviorClones(
		b.candidates(behaviorComparableSignatures(analysis.candidates[pkg]), analysis),
	)[pkg]
}
