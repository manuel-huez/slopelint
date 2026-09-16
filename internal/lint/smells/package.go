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
}

type Runner struct {
	pkg           *Package
	findings      []Finding
	reported      map[string]struct{}
	renderCache   map[ast.Node]string
	funcUseCounts map[string]int
	behaviorCalls map[string]behaviorSummary
}

// RunDefault runs smell checks enabled by default.
func RunDefault(pkg *Package) []Finding {
	r := newRunner(pkg)
	r.checkTrivialForwarders()
	r.checkRepeatedNormalizationCallsPackage()
	r.checkRedundantJSONMarshalText()
	r.checkRestatementComments()
	r.checkStaleComplexitySuppressions()
	r.checkPredicateReturnSignatures()
	r.checkUnusedPrivateParams()
	r.checkDeclarationGrouping()
	r.checkOversizedOwnerFiles()
	r.checkUnnamedLargeTableTests()
	r.checkRepeatedTestFixtures()
	r.checkConstValueTests()
	r.checkTestSupportFilenames()

	return r.findings
}

// RunPackage runs package-wide smell checks enabled by --package.
func RunPackage(pkg *Package) []Finding {
	r := newRunner(pkg)
	r.checkSingleUsePrivateHelpers()
	r.checkSingleImplInterfaces()
	r.checkOptionsOverkill()
	r.checkInternalResultWrappers()
	r.checkTestGlobalFuncStubs()
	r.checkTestFatalPanics()
	r.checkBoolModeParams()
	r.checkZeroValuePrivateArgs()
	r.checkOptionalResultTriples()
	r.checkProductionErrorPanics()
	r.checkSentinelErrorBreaks()

	return r.findings
}

// RunBehaviorPackage reports behavior clones inside one package.
func RunBehaviorPackage(pkg *Package) []Finding {
	r := newRunner(pkg)
	r.checkBehaviorClones(nil)

	return r.findings
}
