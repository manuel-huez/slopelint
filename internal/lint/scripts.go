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

	return issues, nil
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
			Kind: "parse_error",
			Message: "parser cannot read this file, so other rules skip it; " +
				"if the code is valid, it is a tree-sitter parser bug",
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

// addScriptTests sends each test with its suite header.
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
