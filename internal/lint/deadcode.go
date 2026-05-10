package lint

import deadcodecheck "example.com/slopelint/internal/lint/deadcode"

func (l *linter) checkDeadPrivateDecls() {
	for _, finding := range deadcodecheck.Private(l.deadCodePackage()) {
		l.report(finding.Pos, finding.Kind, finding.Message)
	}
}

func repoDeadCodeIssues(pkgs []*deadcodecheck.Package) []Issue {
	findings := deadcodecheck.Repo(pkgs)
	issues := make([]Issue, 0, len(findings))

	for _, finding := range findings {
		issues = append(issues, Issue{
			Pos:     finding.Pos,
			Kind:    finding.Kind,
			Message: finding.Message,
			fset:    finding.FSet,
		})
	}

	return issues
}

func (l *linter) deadCodePackage() *deadcodecheck.Package {
	return &deadcodecheck.Package{
		ImportPath:      l.pkg.ImportPath,
		Name:            l.pkg.Name,
		FSet:            l.pkg.FSet,
		TypesPkg:        l.pkg.TypesPkg,
		TypesInfo:       l.pkg.TypesInfo,
		ProductionDecls: l.index.productionDecls,
		ProductionFuncs: l.index.productionFuncs,
	}
}
