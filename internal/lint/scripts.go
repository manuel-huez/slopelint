package lint

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/manuel-huez/slopelint/internal/lint/outline"
	smellcheck "github.com/manuel-huez/slopelint/internal/lint/smells"
)

var scriptPathspecs = []string{"*.ts", "*.tsx", "*.py"}

// moduleScriptFiles returns the tracked TypeScript and Python files of the Go
// module at root that slopelint lints, as absolute paths. Untracked files and
// modules outside Git have none.
func moduleScriptFiles(root string) ([]string, error) {
	location, err := repositoryCacheLocationForDir(root)
	if err != nil || !location.git {
		return nil, err
	}

	index, err := repoAnalysisGitIndex(root, scriptPathspecs)
	if err != nil {
		return nil, fmt.Errorf("list TypeScript and Python files: %w", err)
	}

	var files []string

	for name := range index {
		if outline.LanguageFor(name) == "" {
			continue
		}

		excluded, err := repoAnalysisExcludedWorktreePath(root, name)
		if err != nil {
			return nil, err
		}

		path := filepath.Join(root, filepath.FromSlash(name))
		if _, statErr := os.Lstat(path); excluded || statErr != nil {
			continue
		}

		files = append(files, path)
	}

	sort.Strings(files)

	return files, nil
}

// patternScriptFiles returns the module root of dir and its script files
// whose directory a relative or absolute directory pattern covers: `./x`
// covers one directory, `./x/...` its tree. Import-path patterns select none.
func patternScriptFiles(patterns []string, dir string) (string, []string, error) {
	if len(patterns) == 0 {
		patterns = []string{allPackagesPattern}
	}

	absoluteDir, err := filepath.Abs(dir)
	if err != nil {
		return "", nil, err
	}

	root, err := findSourceRoot(absoluteDir)
	if err != nil {
		return "", nil, err
	}

	files, err := moduleScriptFiles(root)
	if err != nil {
		return "", nil, err
	}

	var selected []string

	for _, file := range files {
		fileDir := filepath.Dir(file)

		for _, pattern := range patterns {
			if scriptPatternCovers(pattern, absoluteDir, fileDir) {
				selected = append(selected, file)
				break
			}
		}
	}

	return root, selected, nil
}

func scriptPatternCovers(pattern, dir, fileDir string) bool {
	if !filepath.IsAbs(pattern) && pattern != "." && pattern != ".." &&
		!strings.HasPrefix(pattern, "./") && !strings.HasPrefix(pattern, "../") {
		return false
	}

	base, tree := strings.CutSuffix(pattern, "/...")
	if !filepath.IsAbs(base) {
		base = filepath.Join(dir, base)
	}

	relative, err := filepath.Rel(filepath.Clean(base), fileDir)
	if err != nil {
		return false
	}

	return relative == "." || tree && filepath.IsLocal(relative)
}

// lintScriptFiles runs the size and test checks on each script file. Results are
// cached per file content and path, since paths decide test status.
func lintScriptFiles(files []string, root string, opts Options) ([]Issue, error) {
	results := make([][]Issue, len(files))

	err := runPackageJobs(len(files), func(index int) error {
		issues, err := lintScriptFile(files[index], root, opts)
		results[index] = issues

		return err
	})
	if err != nil {
		return nil, err
	}

	var issues []Issue
	for _, fileIssues := range results {
		issues = append(issues, fileIssues...)
	}

	cycleIssues, err := checkScriptModuleCycles(files, root)
	if err != nil {
		return nil, err
	}

	return append(issues, cycleIssues...), nil
}

func lintScriptFile(name, root string, opts Options) ([]Issue, error) {
	content, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}

	relative, err := filepath.Rel(root, name)
	if err != nil {
		return nil, err
	}

	digest := sha256.Sum256(content)
	source := analysisCacheSourceFile{
		Name:         name,
		RelativePath: filepath.ToSlash(relative),
		SHA256:       hex.EncodeToString(digest[:]),
		Size:         len(content),
	}

	cache, _ := analysisCacheForSourceRoot(root, opts, "scripts", func() (string, error) {
		buildID, err := analysisCacheBuildID(opts)
		if err != nil {
			return "", err
		}

		return analysisCacheFingerprintKey(struct {
			Schema  int    `json:"schema"`
			BuildID string `json:"build_id"`
			Path    string `json:"path"`
			SHA256  string `json:"sha256"`
		}{analysisCacheSchema, buildID, source.RelativePath, source.SHA256})
	})
	if entry, ok := cache.load(); ok {
		if issues, valid := replayStandaloneAnalysisIssues(
			entry,
			[]analysisCacheSourceFile{source},
		); valid {
			return issues, nil
		}
	}

	var issues []Issue

	file, err := outline.Parse(name, content)
	if syntaxErr, ok := errors.AsType[*outline.SyntaxError](err); ok {
		issues = append(issues, Issue{
			Kind:     parseErrorKind,
			Message:  "parser cannot read this file",
			position: scriptPosition(name, syntaxErr.Pos),
		})
	} else if err != nil {
		return nil, fmt.Errorf("parse %s: %w", name, err)
	}

	findings := smellcheck.CheckSizes([]outline.File{file})
	for _, smell := range file.Smells {
		findings = append(findings, smellcheck.OutlineFinding{
			Pos:     smell.Pos,
			Kind:    smell.Kind,
			Message: smell.Message,
		})
	}

	for _, finding := range findings {
		issues = append(issues, Issue{
			Kind:     finding.Kind,
			Message:  finding.Message,
			position: scriptPosition(name, finding.Pos),
		})
	}

	if cache != nil {
		cached, err := buildAnalysisCacheIssues(
			issues,
			root,
			func(issue Issue) (token.Position, error) { return issue.position, nil },
		)
		if err == nil {
			_ = writeAnalysisCacheEntry(cache.path, analysisCacheEntry{Issues: cached})
		}
	}

	return issues, nil
}

// parseReadableScript returns an empty outline for a file the parser cannot
// read; lintScriptFile reports it, and the other passes skip it.
func parseReadableScript(name string, content []byte) (outline.File, error) {
	file, err := outline.Parse(name, content)
	if _, ok := errors.AsType[*outline.SyntaxError](err); ok {
		return outline.File{}, nil
	} else if err != nil {
		return outline.File{}, fmt.Errorf("parse %s: %w", name, err)
	}

	return file, nil
}

func collectScriptCodeReviewDeclarations(
	filename string,
	content []byte,
	policy codeReviewPolicy,
) ([]codeReviewDeclaration, error) {
	file, err := parseReadableScript(filename, content)
	if err != nil {
		return nil, err
	}

	if file.Generated {
		return nil, nil
	}

	groups := make([]codeReviewCommentGroup, len(file.Comments))
	for index, comment := range file.Comments {
		groups[index] = codeReviewCommentGroup{
			position:    scriptPosition(filename, comment.Pos),
			lastLine:    comment.EndLine,
			suppression: comment.Suppression,
		}
	}

	lines := strings.Split(string(content), "\n")

	var declarations []codeReviewDeclaration

	add := func(start int, source []string, items []codeReviewPositionedItem) error {
		if len(items) == 0 {
			return nil
		}

		declaration, err := newCodeReviewDeclaration(codeReviewRequest{
			Location: fmt.Sprintf("%s:%d", filename, start),
			Language: outline.LanguageFor(filename),
			File:     filepath.Base(filename),
			Test:     file.Test,
			Source:   numberedCodeReviewSource(source),
		}, items, policy)
		declarations = append(declarations, declaration)

		return err
	}

	for _, decl := range file.Decls {
		items := codeReviewCommentItems(groups, decl.StartLine, decl.EndLine)
		if err := add(decl.StartLine, lines[decl.StartLine-1:decl.EndLine], items); err != nil {
			return nil, err
		}
	}

	// Caught errors and tests get their own requests, so comment requests
	// keep the keys they had before those were judged.
	units, handled := scriptErrorUnits(filename, file.Handlers)
	for _, unit := range units {
		if err := add(
			unit.StartLine,
			lines[unit.StartLine-1:unit.EndLine],
			handled[unit],
		); err != nil {
			return nil, err
		}
	}

	gap := "// ..."
	if outline.LanguageFor(filename) == outline.Python {
		gap = "# ..."
	}

	if err := addScriptTests(file.Tests, lines, add, gap, filename); err != nil {
		return nil, err
	}

	if err := addSplitHelpers(file.SplitHelpers, lines, add, gap, filename); err != nil {
		return nil, err
	}

	tests, helpers := scriptGroupedTests(filename, file, lines)
	testGroups, err := testGroupDeclarations(filename, lines, tests, helpers, policy)

	return append(declarations, testGroups...), err
}

func addScriptTests(
	tests []outline.Test,
	lines []string,
	add func(int, []string, []codeReviewPositionedItem) error,
	gap string,
	filename string,
) error {
	for _, test := range tests {
		source, first := scriptTestSource(lines, test, gap)
		item := codeReviewPositionedItem{
			item: codeReviewItem{
				Kind:  codeReviewTestItem,
				Lines: codeReviewLines(first, first+test.EndLine-test.StartLine),
			},
			position: scriptPosition(filename, test.Pos),
		}

		if err := add(test.StartLine, source, []codeReviewPositionedItem{item}); err != nil {
			return err
		}
	}

	return nil
}

// addSplitHelpers sends each caller with the single-use helpers it calls,
// so review judges the pieces together.
func addSplitHelpers(
	helpers []outline.SplitHelper,
	lines []string,
	add func(int, []string, []codeReviewPositionedItem) error,
	gap string,
	filename string,
) error {
	byCaller := make(map[outline.Decl][]outline.SplitHelper)

	var callers []outline.Decl

	for _, helper := range helpers {
		if byCaller[helper.Caller] == nil {
			callers = append(callers, helper.Caller)
		}

		byCaller[helper.Caller] = append(byCaller[helper.Caller], helper)
	}

	for _, caller := range callers {
		source := slices.Clone(lines[caller.StartLine-1 : caller.EndLine])
		items := make([]codeReviewPositionedItem, 0, len(byCaller[caller]))

		for _, helper := range byCaller[caller] {
			source = append(source, gap)
			first := len(source) + 1
			source = append(source, lines[helper.Helper.StartLine-1:helper.Helper.EndLine]...)
			items = append(items, codeReviewPositionedItem{
				item: codeReviewItem{
					Kind:  codeReviewSplitHelperItem,
					Lines: codeReviewLines(first, len(source)),
					Facts: fmt.Sprintf(
						"only %s calls it, once; the call passes %d of %s's names",
						helper.CallerName,
						helper.Passed,
						helper.CallerName,
					),
				},
				position: scriptPosition(filename, helper.Pos),
			})
		}

		if err := add(caller.StartLine, source, items); err != nil {
			return err
		}
	}

	return nil
}

// scriptGroupedTests returns the file's tests and the first lines of its
// named functions, keyed as Test.Calls names their calls. Repeated test
// titles get #2, #3, ... so each test has its own name.
func scriptGroupedTests(
	filename string,
	file outline.File,
	lines []string,
) ([]groupedTest, map[string]string) {
	helpers := make(map[string]string)

	for _, fn := range file.Funcs {
		if fn.Name == "" || fn.Literal {
			continue
		}

		key := fn.Name
		if fn.Receiver != "" {
			key = "." + fn.Name
		}

		helpers[key] = strings.TrimSpace(lines[fn.StartLine-1])
	}

	tests := make([]groupedTest, len(file.Tests))
	seen := make(map[string]int)

	for index, test := range file.Tests {
		name := test.Name

		seen[name]++
		if count := seen[name]; count > 1 {
			name += " #" + strconv.Itoa(count)
		}

		tests[index] = groupedTest{
			name:      name,
			startLine: test.StartLine,
			endLine:   test.EndLine,
			position:  scriptPosition(filename, test.Pos),
			calls:     test.Calls,
		}
	}

	return tests, helpers
}

// scriptErrorUnits groups caught errors by enclosing function: each unit
// is one request.
func scriptErrorUnits(
	filename string,
	handlers []outline.Handler,
) ([]outline.Decl, map[outline.Decl][]codeReviewPositionedItem) {
	var (
		units   []outline.Decl
		handled = make(map[outline.Decl][]codeReviewPositionedItem)
	)

	for _, handler := range handlers {
		unit := handler.Unit
		if handled[unit] == nil {
			units = append(units, unit)
		}

		handled[unit] = append(handled[unit], codeReviewPositionedItem{
			item: codeReviewItem{
				Kind: codeReviewErrorItem,
				Lines: codeReviewLines(
					handler.StartLine-unit.StartLine+1,
					handler.EndLine-unit.StartLine+1,
				),
			},
			position: scriptPosition(filename, handler.Pos),
		})
	}

	return units, handled
}

// scriptTestSource returns the test with its context lines in file order,
// a gap line between ranges that are not adjacent, and the line number of
// the test's first line in the result.
func scriptTestSource(lines []string, test outline.Test, gap string) ([]string, int) {
	span := outline.Decl{StartLine: test.StartLine, EndLine: test.EndLine}
	spans := append(slices.Clone(test.Context), span)
	slices.SortFunc(spans, outline.Decl.Compare)

	var (
		source []string
		first  int
		last   int
	)

	for _, each := range spans {
		if last > 0 && each.StartLine > last+1 {
			source = append(source, gap)
		}

		if each == span {
			first = len(source) + 1
		}

		source = append(source, lines[each.StartLine-1:each.EndLine]...)
		last = each.EndLine
	}

	return source, first
}

func scriptPosition(filename string, pos outline.Pos) token.Position {
	return token.Position{
		Filename: filename,
		Offset:   pos.Offset,
		Line:     pos.Line,
		Column:   pos.Column,
	}
}

const (
	moduleCycleRule  = "module_cycle"
	minCycleFilePair = 2
)

func checkScriptModuleCycles(files []string, root string) ([]Issue, error) {
	var prod []string

	for _, file := range files {
		if !outline.TestFile(file) {
			prod = append(prod, filepath.Clean(file))
		}
	}

	if len(prod) < minCycleFilePair {
		return nil, nil
	}

	slices.Sort(prod)

	indexByPath := make(map[string]int, len(prod))
	for index, path := range prod {
		indexByPath[path] = index
	}

	edges, edgePos, err := buildScriptImportGraph(prod, root, indexByPath)
	if err != nil {
		return nil, err
	}

	components := smellcheck.StronglyConnectedComponents(edges)
	if len(components) == 0 {
		return nil, nil
	}

	issues := make([]Issue, 0, len(components))
	for _, comp := range components {
		issue, err := moduleCycleIssue(comp, prod, root, edges, edgePos)
		if err != nil {
			return nil, err
		}

		issues = append(issues, issue)
	}

	return issues, nil
}

func buildScriptImportGraph(
	prod []string,
	root string,
	indexByPath map[string]int,
) ([][]int, map[[2]int]outline.Pos, error) {
	edges := make([][]int, len(prod))
	edgePos := make(map[[2]int]outline.Pos)

	for index, path := range prod {
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, err
		}

		parsed, err := parseReadableScript(path, content)
		if err != nil {
			return nil, nil, err
		}

		if parsed.Generated || len(parsed.Imports) == 0 {
			continue
		}

		seen := make(map[int]bool)

		for _, imp := range parsed.Imports {
			for _, target := range resolveScriptImportTargets(
				path,
				root,
				outline.LanguageFor(path),
				imp,
				indexByPath,
			) {
				if target == index || seen[target] {
					continue
				}

				seen[target] = true
				edges[index] = append(edges[index], target)
				edgePos[[2]int{index, target}] = imp.Pos
			}
		}

		slices.Sort(edges[index])
	}

	return edges, edgePos, nil
}

func resolveScriptImportTargets(
	file string,
	root string,
	lang outline.Language,
	imp outline.Import,
	indexByPath map[string]int,
) []int {
	if lang == outline.TypeScript {
		if target, ok := resolveTypeScriptImport(file, imp.Path, indexByPath); ok {
			return []int{target}
		}

		return nil
	}

	return resolvePythonImport(file, root, imp, indexByPath)
}

func resolveTypeScriptImport(
	file string,
	spec string,
	indexByPath map[string]int,
) (int, bool) {
	if !strings.HasPrefix(spec, "./") && !strings.HasPrefix(spec, "../") {
		return 0, false
	}

	base := filepath.Clean(filepath.Join(filepath.Dir(file), filepath.FromSlash(spec)))
	candidates := []string{base}

	for _, jsExt := range []string{".js", ".jsx", ".mjs", ".cjs"} {
		if stem, ok := strings.CutSuffix(base, jsExt); ok {
			candidates = append(candidates, stem+".ts", stem+".tsx")
		}
	}

	candidates = append(
		candidates,
		base+".ts",
		base+".tsx",
		filepath.Join(base, "index.ts"),
		filepath.Join(base, "index.tsx"),
	)

	for _, candidate := range candidates {
		if index, ok := indexByPath[candidate]; ok {
			return index, true
		}
	}

	return 0, false
}

func resolvePythonImport(
	file string,
	root string,
	imp outline.Import,
	indexByPath map[string]int,
) []int {
	bases := pythonImportBaseDirs(file, root, imp.Relative)

	if imp.Path != "" {
		if target, ok := resolvePythonModuleInDirs(bases, imp.Path, indexByPath); ok {
			return []int{target}
		}
	}

	var targets []int

	for _, name := range imp.Names {
		sub := name
		if imp.Path != "" {
			sub = imp.Path + "." + name
		}

		if target, ok := resolvePythonModuleInDirs(bases, sub, indexByPath); ok {
			targets = append(targets, target)
		}
	}

	return targets
}

func pythonImportBaseDirs(file, root string, relative int) []string {
	if relative <= 0 {
		return []string{root, filepath.Join(root, "src")}
	}

	baseDir := filepath.Dir(file)
	for range relative - 1 {
		baseDir = filepath.Dir(baseDir)
	}

	return []string{baseDir}
}

func resolvePythonModuleInDirs(
	bases []string,
	modulePath string,
	indexByPath map[string]int,
) (int, bool) {
	rel := filepath.FromSlash(strings.ReplaceAll(modulePath, ".", "/"))

	for _, dir := range bases {
		joined := filepath.Clean(filepath.Join(dir, rel))
		for _, candidate := range []string{
			joined + ".py",
			filepath.Join(joined, "__init__.py"),
		} {
			if index, ok := indexByPath[candidate]; ok {
				return index, true
			}
		}
	}

	return 0, false
}

func moduleCycleIssue(
	comp []int,
	prod []string,
	root string,
	edges [][]int,
	edgePos map[[2]int]outline.Pos,
) (Issue, error) {
	inComp := make(map[int]bool, len(comp))
	for _, node := range comp {
		inComp[node] = true
	}

	cycle := shortestCyclePath(comp[0], edges, inComp)
	anchorNode := cycle[0]
	nextNode := cycle[1]
	pos := edgePos[[2]int{anchorNode, nextNode}]

	relNames := make([]string, len(cycle))
	for i, node := range cycle {
		rel, err := relativeSlashPath(root, prod[node])
		if err != nil {
			return Issue{}, err
		}

		relNames[i] = rel
	}

	return Issue{
		Kind: moduleCycleRule,
		Message: fmt.Sprintf(
			"%d modules form an import cycle: %s",
			len(comp),
			strings.Join(relNames, " -> "),
		),
		position: scriptPosition(prod[anchorNode], pos),
	}, nil
}

// shortestCyclePath finds a shortest directed cycle starting and ending at
// start using only nodes in allowed.
func shortestCyclePath(start int, edges [][]int, allowed map[int]bool) []int {
	prev := map[int]int{}
	queue := make([]int, 0, len(allowed))

	for _, next := range edges[start] {
		if !allowed[next] {
			continue
		}

		if next == start {
			return []int{start, start}
		}

		if _, seen := prev[next]; !seen {
			prev[next] = start
			queue = append(queue, next)
		}
	}

	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]

		for _, next := range edges[curr] {
			if !allowed[next] {
				continue
			}

			if next == start {
				path := []int{start}
				for node := curr; node != start; node = prev[node] {
					path = append(path, node)
				}

				path = append(path, start)
				slices.Reverse(path)

				return path
			}

			if _, seen := prev[next]; !seen {
				prev[next] = curr
				queue = append(queue, next)
			}
		}
	}

	return []int{start, start}
}

func relativeSlashPath(root, path string) (string, error) {
	if root == "" {
		return filepath.ToSlash(path), nil
	}

	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", fmt.Errorf("relative path for %q from %q: %w", path, root, err)
	}

	return filepath.ToSlash(rel), nil
}
