package lint

import (
	"sort"

	deadcodecheck "example.com/slopelint/internal/lint/deadcode"
)

// LintPackages runs repo-aware analysis across loaded packages.
func LintPackages(pkgs []*LoadedPackage, opts Options) []Issue {
	if len(pkgs) == 0 {
		return nil
	}

	explicitFacts, inferredFacts := inferRepoSummaries(pkgs, opts)
	repoDeadCode := hasMainPackage(pkgs)
	repoOpts := opts
	repoOpts.skipDeadCode = repoDeadCode

	sort.Slice(pkgs, func(i, j int) bool {
		return pkgs[i].ImportPath < pkgs[j].ImportPath
	})

	var (
		deadPkgs = make([]*deadcodecheck.Package, 0, len(pkgs))
		issues   []Issue
	)

	for _, pkg := range pkgs {
		l := newLinter(pkg, repoOpts)
		l.explicitFacts = explicitFacts
		l.inferredFacts = inferredFacts
		l.collectLocalFuncLits()
		l.analyzeFiles()
		sortIssues(pkg.FSet, l.issues)
		issues = append(issues, l.issues...)

		if repoDeadCode {
			deadPkgs = append(deadPkgs, l.deadCodePackage())
		}
	}

	if repoDeadCode {
		issues = append(issues, repoDeadCodeIssues(deadPkgs)...)
	}

	return issues
}

func hasMainPackage(pkgs []*LoadedPackage) bool {
	for _, pkg := range pkgs {
		if pkg != nil && pkg.Name == mainPkgName {
			return true
		}
	}

	return false
}

func inferRepoSummaries(
	pkgs []*LoadedPackage,
	opts Options,
) (map[string][]guardContract, map[string]callSummary) {
	explicitFacts := make(map[string][]guardContract)
	summaries := make(map[string]callSummary)
	funcs := make([]repoSummarizableFunc, 0)

	sort.Slice(pkgs, func(i, j int) bool {
		return pkgs[i].ImportPath < pkgs[j].ImportPath
	})

	for _, pkg := range pkgs {
		l := newLinter(pkg, opts)
		l.explicitFacts = explicitFacts
		l.collectLocalFuncLits()

		l.collectContracts()

		for _, fn := range l.collectSummarizableFuncs() {
			funcs = append(funcs, repoSummarizableFunc{
				l:  l,
				fn: fn,
			})
		}
	}

	maxPasses := len(funcs) + 1
	for range maxPasses {
		changed := false

		for _, item := range funcs {
			item.l.inferredFacts = summaries

			summary := item.l.summarizeFunc(item.fn)

			prev := summaries[item.fn.key]
			if callSummaryEqual(prev, summary) {
				continue
			}

			summaries[item.fn.key] = summary
			changed = true
		}

		if !changed {
			break
		}
	}

	return explicitFacts, summaries
}

type repoSummarizableFunc struct {
	l  *linter
	fn summarizableFunc
}
