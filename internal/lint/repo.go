package lint

import (
	"fmt"
	"go/types"
	"maps"
	"sort"

	deadcodecheck "github.com/manuel-huez/slopelint/internal/lint/deadcode"
)

// LintRepository resolves patterns, replays valid cached analysis before type-checking,
// and runs semantic similarity when requested.
func LintRepository(
	patterns []string,
	dir string,
	opts Options,
	similarity *SimilarityOptions,
) ([]Issue, error) {
	if err := preflightSimilarityCI(dir, similarity); err != nil {
		return nil, err
	}

	cache, cacheErr := newRepoAnalysisCache(patterns, dir, opts, similarity)
	if cacheErr == nil {
		if entry, ok := cache.load(); ok {
			if issues, valid := replayRepoAnalysisCache(
				entry,
				opts.CacheHitHook,
				cache.sourceRoot,
			); valid {
				return issues, nil
			}
		}
	}

	targets, byImportPath, err := resolvePackageMetadata(patterns, dir)
	if err != nil {
		return nil, err
	}

	var issues []Issue

	if similarity != nil {
		// Similarity uses source syntax only. Validate CI stamps or finish local
		// work before retaining type graphs for structural linting.
		similarityPkgs, filesErr := similarityPackagesForTargets(targets)
		if filesErr != nil {
			return nil, filesErr
		}

		issues, err = CheckSimilarCode(similarityPkgs, *similarity)
		if err != nil {
			return nil, err
		}
	}

	packageIssues, err := lintPackageTargets(targets, byImportPath, opts)
	if err != nil {
		return nil, err
	}

	issues = append(issues, packageIssues...)
	sortIssues(issues)

	persistRepoAnalysisResult(patterns, dir, opts, similarity, issues)

	return issues, nil
}

func persistRepoAnalysisResult(
	patterns []string,
	dir string,
	opts Options,
	similarity *SimilarityOptions,
	issues []Issue,
) {
	// Similarity can update its committed stamp. Recompute the fast source key so
	// the stored result describes the state that the next command will observe.
	freshCache, err := newRepoAnalysisCache(patterns, dir, opts, similarity)
	if err != nil {
		return
	}

	_ = freshCache.store(issues)

	if similarity == nil || !similarity.CI {
		maybePruneCaches(opts.cacheDir)
	}
}

func preflightSimilarityCI(dir string, opts *SimilarityOptions) error {
	if opts == nil || !opts.CI {
		return nil
	}

	// Missing and obsolete attestations need no Git or Go work. Source freshness
	// still needs resolved package files and is checked by CheckSimilarCode.
	root, err := findGoModuleRoot(dir)
	if err != nil {
		return err
	}

	stamp, err := loadSimilarityStamp(root)
	if err != nil {
		return err
	}

	exists := stamp.Schema != 0
	if exists && stamp.policyMatches() {
		if stamp.RepositoryDigest != "" {
			digest, digestErr := similarityRepositoryDigest(root)
			if digestErr == nil && digest != stamp.RepositoryDigest {
				return fmt.Errorf(
					"%s is stale; run slopelint locally and commit the updated stamp",
					similarityStampName,
				)
			}
		}

		return nil
	}

	return verifySimilarityStamp(stamp, exists, stamp.SourceDigest)
}

func similarityPackagesForTargets(
	targets []*packageMeta,
) ([]*LoadedPackage, error) {
	pkgs := make([]*LoadedPackage, len(targets))
	for index, target := range targets {
		repoFiles, err := packageRepoFiles(target)
		if err != nil {
			return nil, err
		}

		pkgs[index] = &LoadedPackage{
			ImportPath: target.targetImportPath(),
			Name:       target.Name,
			Dir:        target.Dir,
			repoFiles:  repoFiles,
		}
	}

	return pkgs, nil
}

type repoPackageInput struct {
	importPath string
	name       string
	dir        string
	imports    []string
	files      []analysisCacheSourceFile
	sourceErr  error
	pkg        *LoadedPackage
	meta       *packageMeta
}

func lintPackageTargets(
	targets []*packageMeta,
	byImportPath map[string]*packageMeta,
	opts Options,
) ([]Issue, error) {
	inputs, err := repoPackageInputsForTargets(targets)
	if err != nil {
		return nil, err
	}

	loadContext := &packageLoadContext{byImportPath: byImportPath}

	typeDigests, err := prepareRepoPackageCache(
		inputs,
		targets,
		byImportPath,
		opts,
		loadContext,
	)
	if err != nil {
		return nil, err
	}

	return lintRepoPackageInputs(inputs, opts, loadContext, typeDigests)
}

func repoPackageInputsForTargets(
	targets []*packageMeta,
) ([]repoPackageInput, error) {
	inputs := make([]repoPackageInput, len(targets))
	for index, target := range targets {
		paths, err := packageRepoFiles(target)
		if err != nil {
			return nil, err
		}

		files, err := analysisCacheSourceFiles(paths, target.Dir)
		inputs[index] = repoPackageInput{
			importPath: target.targetImportPath(),
			name:       target.Name,
			dir:        target.Dir,
			imports:    append([]string(nil), target.Imports...),
			files:      files,
			sourceErr:  err,
			meta:       target,
		}
	}

	return inputs, nil
}

func prepareRepoPackageCache(
	inputs []repoPackageInput,
	targets []*packageMeta,
	byImportPath map[string]*packageMeta,
	opts Options,
	loadContext *packageLoadContext,
) (map[string]string, error) {
	if opts.CacheEnabled {
		typeDigests, missing := loadAnalysisCacheTypeDigests(
			targets,
			byImportPath,
			opts.cacheDir,
		)
		if len(missing) == 0 {
			return typeDigests, nil
		}

		if len(missing) <= analysisCacheTypeDigestRefreshLimit {
			refreshed, refreshErr := loadPackageTypeDigests(missing, loadContext)
			if refreshErr == nil {
				maps.Copy(typeDigests, refreshed)

				storeAnalysisCacheTypeDigests(
					targets,
					byImportPath,
					opts.cacheDir,
					typeDigests,
				)

				return typeDigests, nil
			}
		}
	}

	// Cold runs keep package parsing and export decoding parallel. The resulting
	// public API digests make later package-cache checks metadata-only.
	loaded := make([]*LoadedPackage, len(inputs))

	err := runPackageJobs(len(inputs), func(index int) error {
		pkg, loadErr := loadOne(inputs[index].meta, loadContext)
		loaded[index] = pkg

		return loadErr
	})
	if err != nil {
		return nil, err
	}

	for index := range inputs {
		inputs[index].pkg = loaded[index]
	}

	if !opts.CacheEnabled {
		return map[string]string{}, nil
	}

	typeDigests := analysisCacheTypeDigests(loaded)
	storeAnalysisCacheTypeDigests(
		targets,
		byImportPath,
		opts.cacheDir,
		typeDigests,
	)

	return typeDigests, nil
}

func lintRepoPackageInputs(
	inputs []repoPackageInput,
	opts Options,
	loadContext *packageLoadContext,
	typeDigests map[string]string,
) ([]Issue, error) {
	if len(inputs) == 0 {
		return nil, nil
	}

	inputs = append([]repoPackageInput(nil), inputs...)
	sort.Slice(inputs, func(i, j int) bool {
		return inputs[i].importPath < inputs[j].importPath
	})

	repoDeadCode := opts.ClosedWorld && hasMainPackageInputs(inputs)
	repoOpts := opts
	repoOpts.skipDeadCode = repoDeadCode

	summaries := make(map[string]callSummary)
	results := make(map[string]repoPackageLintResult, len(inputs))

	for _, level := range repoPackageDependencyLevels(inputs) {
		current, err := lintRepoPackageLevel(
			level,
			summaries,
			repoOpts,
			typeDigests,
			repoDeadCode,
			loadContext,
		)
		if err != nil {
			return nil, err
		}

		for _, result := range current {
			for _, export := range result.exports {
				summaries[export.FuncKey] = callSummaryFromFact(&export.Fact)
			}

			results[result.importPath] = result
		}
	}

	var (
		issues   []Issue
		deadPkgs = make([]*deadcodecheck.Package, 0, len(inputs))
	)

	for _, input := range inputs {
		result := results[input.importPath]
		issues = append(issues, result.issues...)

		if repoDeadCode {
			deadPkgs = append(deadPkgs, result.deadCode)
		}
	}

	if repoDeadCode {
		issues = append(issues, repoDeadCodeIssues(deadPkgs)...)
	}

	sortIssues(issues)

	return issues, nil
}

func lintRepoPackageLevel(
	inputs []repoPackageInput,
	summaries map[string]callSummary,
	opts Options,
	typeDigests map[string]string,
	repoDeadCode bool,
	loadContext *packageLoadContext,
) ([]repoPackageLintResult, error) {
	results := make([]repoPackageLintResult, len(inputs))

	err := runPackageJobs(len(inputs), func(index int) error {
		result, err := lintRepoPackage(
			inputs[index],
			summaries,
			opts,
			typeDigests,
			repoDeadCode,
			loadContext,
		)
		results[index] = result

		return err
	})

	return results, err
}

func lintRepoPackage(
	input repoPackageInput,
	summaries map[string]callSummary,
	opts Options,
	typeDigests map[string]string,
	repoDeadCode bool,
	loadContext *packageLoadContext,
) (repoPackageLintResult, error) {
	cache, _ := analysisCacheForSourceRoot(input.dir, opts, "packages", func() (string, error) {
		if input.sourceErr != nil {
			return "", input.sourceErr
		}

		return standaloneAnalysisCacheKey(
			input.importPath,
			input.imports,
			input.files,
			opts,
			typeDigests,
		)
	})
	if cached, ok := cachedRepoPackageLintResult(
		cache,
		input,
		summaries,
		opts,
		repoDeadCode,
		loadContext,
	); ok {
		return cached, nil
	}

	pkg, err := loadedRepoPackage(input, loadContext)
	if err != nil {
		return repoPackageLintResult{}, err
	}

	l := newLinter(pkg, opts)
	dependencies := make(map[string]analysisCacheImportedFact)
	l.externalSummary = func(obj *types.Func) (callSummary, bool) {
		if obj.Pkg() == nil || obj.Pkg().Path() == pkg.ImportPath {
			return callSummary{}, false
		}

		key := funcObjectKey(obj)
		summary, ok := summaries[key]

		dependency := analysisCacheImportedFact{FuncKey: key, Present: ok}
		if ok {
			dependency.Fact = *callSummaryFactFromSummary(summary)
		}

		dependencies[key] = dependency

		return summary, ok
	}

	l.run()
	sortIssues(l.issues)

	result := repoPackageLintResult{
		importPath: input.importPath,
		issues:     l.issues,
		exports:    cachedExportsForLinter(l),
	}
	if cache != nil {
		_ = cache.storeStandalone(
			l,
			l.issues,
			sortedAnalysisCacheDependencies(dependencies),
		)
	}

	if repoDeadCode {
		result.deadCode = l.deadCodePackage()
	}

	return result, nil
}

func loadedRepoPackage(
	input repoPackageInput,
	loadContext *packageLoadContext,
) (*LoadedPackage, error) {
	if input.pkg != nil {
		return input.pkg, nil
	}

	if input.meta == nil || loadContext == nil {
		return nil, fmt.Errorf("package %s has no load metadata", input.importPath)
	}

	return loadOne(input.meta, loadContext)
}

func cachedRepoPackageLintResult(
	cache *analysisCache,
	input repoPackageInput,
	summaries map[string]callSummary,
	opts Options,
	repoDeadCode bool,
	loadContext *packageLoadContext,
) (repoPackageLintResult, bool) {
	if cache == nil {
		return repoPackageLintResult{}, false
	}

	entry, ok := cache.load()
	if !ok {
		return repoPackageLintResult{}, false
	}

	issues, exports, ok := replayStandaloneAnalysisCache(
		entry,
		summaries,
		opts.CacheHitHook,
		input.importPath,
		input.files,
	)
	if !ok {
		return repoPackageLintResult{}, false
	}

	result := repoPackageLintResult{
		importPath: input.importPath,
		issues:     issues,
		exports:    exports,
	}
	if repoDeadCode {
		pkg, err := loadedRepoPackage(input, loadContext)
		if err != nil {
			return repoPackageLintResult{}, false
		}

		result.deadCode = newLinter(pkg, opts).deadCodePackage()
	}

	return result, true
}

func sortedAnalysisCacheDependencies(
	dependencies map[string]analysisCacheImportedFact,
) []analysisCacheImportedFact {
	keys := make([]string, 0, len(dependencies))
	for key := range dependencies {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	ordered := make([]analysisCacheImportedFact, 0, len(keys))
	for _, key := range keys {
		dependency := dependencies[key]
		dependency.Fact = *cloneCallSummaryFact(&dependency.Fact)
		ordered = append(ordered, dependency)
	}

	return ordered
}

type repoPackageLintResult struct {
	importPath string
	issues     []Issue
	exports    []analysisCacheExport
	deadCode   *deadcodecheck.Package
}

func hasMainPackageInputs(inputs []repoPackageInput) bool {
	for _, input := range inputs {
		if input.name == mainPkgName {
			return true
		}
	}

	return false
}

func repoPackageDependencyLevels(inputs []repoPackageInput) [][]repoPackageInput {
	byPath := make(map[string]repoPackageInput, len(inputs))
	for _, input := range inputs {
		byPath[input.importPath] = input
	}

	levels := make(map[string]int, len(inputs))
	visiting := make(map[string]struct{}, len(inputs))

	var assignLevel func(repoPackageInput) int

	assignLevel = func(input repoPackageInput) int {
		if level, ok := levels[input.importPath]; ok {
			return level
		}

		if _, cycle := visiting[input.importPath]; cycle {
			return 0
		}

		visiting[input.importPath] = struct{}{}
		level := 0

		imports := append([]string(nil), input.imports...)
		sort.Strings(imports)

		for _, imported := range imports {
			if dependency, ok := byPath[imported]; ok {
				level = max(level, assignLevel(dependency)+1)
			}
		}

		delete(visiting, input.importPath)
		levels[input.importPath] = level

		return level
	}

	maximumLevel := 0

	for _, input := range inputs {
		maximumLevel = max(maximumLevel, assignLevel(input))
	}

	ordered := make([][]repoPackageInput, maximumLevel+1)

	for _, input := range inputs {
		level := levels[input.importPath]
		ordered[level] = append(ordered[level], input)
	}

	for _, level := range ordered {
		sort.Slice(level, func(i, j int) bool {
			return level[i].importPath < level[j].importPath
		})
	}

	return ordered
}
