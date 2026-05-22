package lint

import smellcheck "github.com/manuel-huez/slopelint/internal/lint/smells"

func (l *linter) scanDefaultSmells() {
	l.addSmellFindings(smellcheck.RunDefault(l.smellsPackage()))
}

func (l *linter) scanPackageSmells() {
	l.addSmellFindings(smellcheck.RunPackage(l.smellsPackage()))

	if !l.skipDeadCode {
		l.checkDeadPrivateDecls()
	}
}

func (l *linter) smellsPackage() *smellcheck.Package {
	return &smellcheck.Package{
		Files:           l.index.files,
		ProductionFiles: l.index.productionFiles,
		TestFiles:       l.index.testFiles,
		ProductionDecls: l.index.productionDecls,
		ProductionFuncs: l.index.productionFuncs,
		ProductionTypes: l.index.productionTypes,
		FSet:            l.pkg.FSet,
		TypesPkg:        l.pkg.TypesPkg,
		TypesInfo:       l.pkg.TypesInfo,
	}
}

func (l *linter) addSmellFindings(findings []smellcheck.Finding) {
	for _, finding := range findings {
		l.report(finding.Pos, finding.Kind, finding.Message)
	}
}
