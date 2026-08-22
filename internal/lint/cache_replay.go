package lint

import (
	"go/ast"
	"go/token"
	"go/types"
	"sort"

	"golang.org/x/tools/go/analysis"
)

func cachedExportsForLinter(l *linter) []analysisCacheExport {
	funcs := l.collectSummarizableFuncs()
	if len(funcs) == 0 {
		return nil
	}

	out := make([]analysisCacheExport, 0, len(funcs))

	for _, fn := range funcs {
		summary := l.summaryWithExplicit(fn.key, l.inferredFacts[fn.key])
		out = append(out, analysisCacheExport{
			FuncKey: fn.key,
			Fact:    *callSummaryFactFromSummary(summary),
		})
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].FuncKey < out[j].FuncKey
	})

	return out
}

func replayAnalysisCache(
	pass *analysis.Pass,
	pkg *LoadedPackage,
	entry *analysisCacheEntry,
	cacheHitHook func(string),
) ([]Issue, bool) {
	if entry == nil {
		return nil, false
	}

	funcs := packageFuncObjects(pkg)
	for _, export := range entry.Exports {
		obj := funcs[export.FuncKey]
		if obj == nil {
			return nil, false
		}

		fact := cloneCallSummaryFact(&export.Fact)
		pass.ExportObjectFact(obj.Origin(), fact)
	}

	files := packageTokenFiles(pass.Files, pass.Fset)
	issues := make([]Issue, 0, len(entry.Issues))

	for _, cached := range entry.Issues {
		file := files[cached.Filename]
		if file == nil || cached.Offset < 0 || cached.Offset > file.Size() {
			return nil, false
		}

		issues = append(issues, Issue{
			Pos:     file.Pos(cached.Offset),
			Kind:    cached.Kind,
			Message: cached.Message,
			fset:    pass.Fset,
		})
	}

	if cacheHitHook != nil {
		cacheHitHook(pkg.ImportPath)
	}

	return issues, true
}

func replayRepoAnalysisCache(
	entry *analysisCacheEntry,
	cacheHitHook func(string),
) ([]Issue, bool) {
	if entry == nil {
		return nil, false
	}

	issues := make([]Issue, 0, len(entry.Issues))

	for _, cached := range entry.Issues {
		if cached.Filename == "" || cached.Offset < 0 || cached.Line <= 0 || cached.Column <= 0 {
			return nil, false
		}

		issues = append(issues, Issue{
			Kind:    cached.Kind,
			Message: cached.Message,
			position: token.Position{
				Filename: cached.Filename,
				Offset:   cached.Offset,
				Line:     cached.Line,
				Column:   cached.Column,
			},
		})
	}

	if cacheHitHook != nil {
		cacheHitHook("repo")
	}

	return issues, true
}

func packageFuncObjects(pkg *LoadedPackage) map[string]*types.Func {
	l := newLinter(pkg, Options{})

	out := make(map[string]*types.Func)

	for _, fn := range l.collectSummarizableFuncs() {
		obj, ok := pkg.TypesInfo.ObjectOf(fn.decl.Name).(*types.Func)
		if !ok || obj == nil {
			continue
		}

		out[fn.key] = obj
	}

	return out
}

func packageTokenFiles(files []*ast.File, fset *token.FileSet) map[string]*token.File {
	out := make(map[string]*token.File, len(files))

	for _, file := range files {
		tokenFile := fset.File(file.Package)
		if tokenFile == nil {
			continue
		}

		out[tokenFile.Name()] = tokenFile
	}

	return out
}

func cloneCallSummaryFact(fact *callSummaryFact) *callSummaryFact {
	if fact == nil {
		return &callSummaryFact{}
	}

	return callSummaryFactFromSummary(callSummaryFromFact(fact))
}
