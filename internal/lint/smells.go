package lint

import (
	deadcodecheck "github.com/manuel-huez/slopelint/internal/lint/deadcode"
	smellcheck "github.com/manuel-huez/slopelint/internal/lint/smells"
)

// scanPackageChecks runs the package-wide smell and dead-code checks after the
// per-function walk, so their findings follow path findings in report order.
func (l *linter) scanPackageChecks() {
	pkg := l.smellsPackage()
	l.addSmellFindings(smellcheck.RunDefault(pkg))

	if !l.skipBehaviorClones {
		l.addSmellFindings(smellcheck.RunBehaviorPackage(pkg))
	}

	l.addSmellFindings(smellcheck.RunPackage(pkg))

	if !l.skipDeadCode {
		for _, finding := range deadcodecheck.Private(l.deadCodePackage()) {
			l.sink.report(finding.Pos, finding.Kind, finding.Message)
		}
	}
}

func (l *linter) smellsPackage() *smellcheck.Package {
	return &smellcheck.Package{
		Files:            l.index.files,
		ProductionFiles:  l.index.productionFiles,
		TestFiles:        l.index.testFiles,
		TestSupportFiles: l.index.testSupportFiles,
		ProductionDecls:  l.index.productionDecls,
		ProductionFuncs:  l.index.productionFuncs,
		ProductionTypes:  l.index.productionTypes,
		FSet:             l.pkg.FSet,
		TypesPkg:         l.pkg.TypesPkg,
		TypesInfo:        l.pkg.TypesInfo,
	}
}

func (l *linter) addSmellFindings(findings []smellcheck.Finding) {
	for _, finding := range findings {
		l.sink.report(finding.Pos, finding.Kind, finding.Message)
	}
}
