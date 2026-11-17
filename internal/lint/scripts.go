package lint

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/token"
	"os"
	"path/filepath"
	"sort"
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

	root, err := findGoModuleRoot(absoluteDir)
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

// lintScriptFiles runs the size checks on each script file. Results are
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

	file, err := outline.Parse(name, content)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", name, err)
	}

	var issues []Issue

	for _, finding := range smellcheck.CheckSizes([]outline.File{file}) {
		issues = append(issues, Issue{
			Kind:    finding.Kind,
			Message: finding.Message,
			position: token.Position{
				Filename: name,
				Offset:   finding.Pos.Offset,
				Line:     finding.Pos.Line,
				Column:   finding.Pos.Column,
			},
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

func collectScriptCodeReviewDeclarations(
	filename string,
	content []byte,
	policy codeReviewPolicy,
) ([]codeReviewDeclaration, error) {
	file, err := outline.Parse(filename, content)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", filename, err)
	}

	if file.Generated {
		return nil, nil
	}

	groups := make([]codeReviewCommentGroup, len(file.Comments))
	for index, comment := range file.Comments {
		groups[index] = codeReviewCommentGroup{
			position: token.Position{
				Filename: filename,
				Offset:   comment.Pos.Offset,
				Line:     comment.Pos.Line,
				Column:   comment.Pos.Column,
			},
			lastLine:    comment.EndLine,
			suppression: comment.Suppression,
		}
	}

	lines := strings.Split(string(content), "\n")

	var declarations []codeReviewDeclaration

	for _, decl := range file.Decls {
		items := codeReviewCommentItems(groups, decl.StartLine, decl.EndLine)
		if len(items) == 0 {
			continue
		}

		declaration, err := newCodeReviewDeclaration(codeReviewRequest{
			Location: fmt.Sprintf("%s:%d", filename, decl.StartLine),
			Language: outline.LanguageFor(filename),
			File:     filepath.Base(filename),
			Test:     file.Test,
			Source:   numberedCodeReviewSource(lines[decl.StartLine-1 : decl.EndLine]),
		}, items, policy)
		if err != nil {
			return nil, err
		}

		declarations = append(declarations, declaration)
	}

	return declarations, nil
}
