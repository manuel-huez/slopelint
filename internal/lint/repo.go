package lint

import (
	"errors"
	"sort"

	deadcodecheck "github.com/manuel-huez/slopelint/internal/lint/deadcode"
	smellcheck "github.com/manuel-huez/slopelint/internal/lint/smells"
)

// LintPackages runs repo-aware analysis across loaded packages.
func LintPackages(pkgs []*LoadedPackage, opts Options) []Issue {
	if len(pkgs) == 0 {
		return nil
	}

	pkgs = append([]*LoadedPackage(nil), pkgs...)

	cache, err := newRepoAnalysisCache(pkgs, opts)
	if err == nil {
		if entry, ok := cache.load(); ok {
			if issues, ok := replayRepoAnalysisCache(pkgs, entry, opts.CacheHitHook); ok {
				return issues
			}
		}
	} else if !errors.Is(err, errAnalysisCacheDisabled) {
		cache = nil
	}

	explicitFacts, inferredFacts := inferRepoSummaries(pkgs, opts)
	repoDeadCode := opts.ClosedWorld && hasMainPackage(pkgs)
	repoOpts := opts
	repoOpts.skipDeadCode = repoDeadCode
	repoOpts.skipBehaviorClones = true

	sortLoadedPackages(pkgs)

	var (
		deadPkgs   = make([]*deadcodecheck.Package, 0, len(pkgs))
		smellPkgs  = make([]*smellcheck.Package, 0, len(pkgs))
		pkgLinters = make(map[*smellcheck.Package]*linter, len(pkgs))
		linters    = make([]*linter, 0, len(pkgs))
		issues     []Issue
	)

	for _, pkg := range pkgs {
		l := newLinter(pkg, repoOpts)
		l.explicitFacts = explicitFacts
		l.inferredFacts = inferredFacts
		l.checkContractComments()
		l.collectLocalFuncLits()
		l.analyzeFiles()
		linters = append(linters, l)

		smellPkg := l.smellsPackage()
		smellPkgs = append(smellPkgs, smellPkg)
		pkgLinters[smellPkg] = l

		if repoDeadCode {
			deadPkgs = append(deadPkgs, l.deadCodePackage())
		}
	}

	addRepoBehaviorFindings(smellPkgs, pkgLinters)

	for _, l := range linters {
		sortIssues(l.issues)
		issues = append(issues, l.issues...)
	}

	if repoDeadCode {
		issues = append(issues, repoDeadCodeIssues(deadPkgs)...)
	}

	sortIssues(issues)

	if cache != nil {
		// Cache persistence is best-effort; analysis results remain valid without it.
		_ = cache.store(issues)
	}

	return issues
}

func addRepoBehaviorFindings(
	pkgs []*smellcheck.Package,
	linters map[*smellcheck.Package]*linter,
) {
	for pkg, findings := range smellcheck.RunBehaviorRepo(pkgs) {
		linters[pkg].addSmellFindings(findings)
	}
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

	sortLoadedPackages(pkgs)

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

func sortLoadedPackages(pkgs []*LoadedPackage) {
	sort.Slice(pkgs, func(i, j int) bool {
		return pkgs[i].ImportPath < pkgs[j].ImportPath
	})
}

type repoSummarizableFunc struct {
	l  *linter
	fn summarizableFunc
}
