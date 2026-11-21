package lint

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/manuel-huez/slopelint/internal/lint/outline"
)

// TestScriptFilesLintAndReview runs the repository entrypoint on a module
// with TypeScript and Python files: tracked files outside hidden, dependency,
// and declaration paths get size findings and review, across a nested go.mod.
// writeScriptModule writes a Go main and fences web off as its own module,
// as a TypeScript project next to Go code is.
func writeScriptModule(t *testing.T, dir string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n\nfunc main() {}\n")
	writeFile(t, filepath.Join(dir, "web", "go.mod"), "module example.com/sample/web\n\ngo 1.22\n")
}

func TestScriptFilesLintAndReview(t *testing.T) {
	dir := newTestModule(t)
	longBody := "export function long(): number {\n  let total = 0\n" +
		strings.Repeat("  total += 1\n", 100) + "  return total\n}\n"

	writeScriptModule(t, dir)
	writeFile(
		t,
		filepath.Join(dir, "web", "app.ts"),
		longBody+"\nfunction sum(a: number, b: number): number {\n  // restates the sum\n  return a + b\n}\n"+
			"\nfunction parse(text: string): number {\n  try {\n    return JSON.parse(text)\n  } catch (lostErr) {\n    return 0\n  }\n}\n",
	)
	writeFile(t, filepath.Join(dir, "web", "__tests__", "app.ts"), longBody)
	writeFile(
		t,
		filepath.Join(dir, "web", "app.test.ts"),
		"describe('app', () => {\n  test.only('TestCountTrivial', async () => {\n    await sleep(10)\n  })\n})\n",
	)
	writeFile(t, filepath.Join(dir, "web", "types.d.ts"), longBody)
	// Valid TypeScript that tree-sitter cannot read: reported, never reviewed.
	writeFile(
		t,
		filepath.Join(dir, "web", "raw.ts"),
		"// restates the path\nexport const path = String.raw`C:\\x`\n",
	)
	writeFile(
		t,
		filepath.Join(dir, "web", "total.py"),
		"def total(values):\n    result = 0\n"+strings.Repeat(
			"    result += values[0] * 2 + 1\n",
			8,
		)+"    return result\n\n\ndef enabled(flag):\n    if flag is None:\n        return False\n    return True\n",
	)
	writeFile(t, filepath.Join(dir, "node_modules", "dep", "index.ts"), longBody)
	writeFile(t, filepath.Join(dir, ".stx", "model.py"), "import os  # noqa: F401 excuse\n")
	writeFile(t, filepath.Join(dir, "pkg", "mod.py"), "import os  # noqa: F401 excuse\n\n\n"+
		"def load(path):\n    try:\n        return open(path).read()\n    except OSError as lostErr:\n        return ''\n"+
		"    except ValueError:\n        raise\n")
	initTestGitRepository(t, dir)
	writeFile(t, filepath.Join(dir, "web", "untracked.ts"), longBody)

	model := new(reviewTestModel)

	issues, err := lintWithReviewForTest(t, dir, t.TempDir(), model)
	if err != nil {
		t.Fatal(err)
	}

	var got []string

	for _, issue := range issues {
		position := issuePosition(issue)
		if outline.LanguageFor(position.Filename) == "" {
			continue
		}

		if strings.Contains(issue.Message, "total.py") {
			t.Errorf("Python block paired with TypeScript: %s", issue.Message)
		}

		relative, _ := filepath.Rel(dir, position.Filename)
		got = append(got, filepath.ToSlash(relative)+":"+strconv.Itoa(position.Line)+" "+issue.Kind)
	}

	// Each caught error is judged in its function and each test with its
	// suite header, apart from comments; an except clause that re-raises is
	// not judged. The copied long function is a duplicate across directories;
	// the fake embedder scores every pair alike, but Python never pairs with
	// TypeScript.
	want := []string{
		"pkg/mod.py:1 comment_excuse",
		"pkg/mod.py:7 swallowed_error",
		"web/__tests__/app.ts:1 semantic_duplicate",
		"web/app.test.ts:2 test_trivial",
		"web/app.test.ts:2 test_focused",
		"web/app.test.ts:3 test_sleep",
		"web/app.ts:1 long_function",
		"web/app.ts:107 comment_noise",
		"web/app.ts:114 swallowed_error",
		"web/raw.ts:2 parse_error",
		"web/total.py:15 boolean_ceremony",
	}
	if !slices.Equal(got, want) {
		t.Fatalf(
			"script findings:\n%s\nwant:\n%s",
			strings.Join(got, "\n"),
			strings.Join(want, "\n"),
		)
	}

	var languages []outline.Language
	for _, request := range model.requests {
		languages = append(languages, request.Language)
	}

	slices.Sort(languages)

	if want := []outline.Language{
		outline.Python,
		outline.Python,
		outline.TypeScript,
		outline.TypeScript,
		outline.TypeScript,
	}; !slices.Equal(
		languages,
		want,
	) {
		t.Fatalf("review languages = %v, want %v", languages, want)
	}
}

// TestScriptOnlyRepositoryWithoutGoModule checks a repository with no
// go.mod: Go loading is skipped, scripts are linted and reviewed, and the
// stamp lives at the Git root once findings are fixed.
func TestScriptOnlyRepositoryWithoutGoModule(t *testing.T) {
	dir := t.TempDir()
	source := "def load(path):\n    try:\n        return open(path).read()\n" +
		"    except OSError as lostErr:\n        return ''\n"
	writeFile(t, filepath.Join(dir, "tools", "mod.py"), source)
	initTestGitRepository(t, dir)

	model := new(reviewTestModel)

	issues, err := lintWithReviewForTest(t, dir, t.TempDir(), model)
	if err != nil {
		t.Fatal(err)
	}

	if len(issues) != 1 || issues[0].Kind != "swallowed_error" {
		t.Fatalf("issues = %v, want one swallowed_error", issues)
	}

	writeFile(
		t,
		filepath.Join(dir, "tools", "mod.py"),
		strings.Replace(source, "return ''", "raise", 1),
	)

	if issues, err = lintWithReviewForTest(
		t,
		dir,
		t.TempDir(),
		model,
	); err != nil ||
		len(issues) != 0 {
		t.Fatalf("issues = %v, err = %v, want none", issues, err)
	}

	if _, err := os.Stat(filepath.Join(dir, similarityStampName)); err != nil {
		t.Fatalf("stamp at Git root: %v", err)
	}
}

// TestCodeReviewKeysFollowOwnFamily checks that each prompt family's version
// change selects new records only for that family's requests, so tuned
// verdicts of other languages and item kinds stay cached.
func TestCodeReviewKeysFollowOwnFamily(t *testing.T) {
	t.Parallel()

	comment := []codeReviewItem{{ID: "i1", Kind: codeReviewCommentItem, Lines: "1"}}
	group := []codeReviewItem{
		{ID: "i1", Kind: codeReviewTestGroupItem, Lines: "1-2", Tests: []string{"a", "b"}},
	}
	request := func(language outline.Language, kind string, items []codeReviewItem) codeReviewRequest {
		if kind != "" {
			items = []codeReviewItem{{ID: "i1", Kind: kind, Lines: "2"}}
		}

		return codeReviewRequest{Language: language, Source: "1| // note\n2| f()\n", Items: items}
	}

	requests := map[string]codeReviewRequest{
		"Go comment":            request(outline.Go, "", comment),
		"Go test":               request(outline.Go, codeReviewTestItem, nil),
		"Go test group":         request(outline.Go, "", group),
		"TypeScript comment":    request(outline.TypeScript, "", comment),
		"TypeScript error":      request(outline.TypeScript, codeReviewErrorItem, nil),
		"TypeScript test group": request(outline.TypeScript, "", group),
		"Python test":           request(outline.Python, codeReviewTestItem, nil),
		"Python test group":     request(outline.Python, "", group),
	}

	key := func(policy codeReviewPolicy, request codeReviewRequest) string {
		t.Helper()

		value, err := codeReviewKey(policy, request)
		if err != nil {
			t.Fatal(err)
		}

		return value
	}

	for _, test := range []struct {
		change  func(*codeReviewPolicy)
		changes []string
	}{
		{func(policy *codeReviewPolicy) { policy.Go++ }, []string{"Go comment", "Go test"}},
		{func(policy *codeReviewPolicy) { policy.TypeScript++ }, []string{"TypeScript comment", "TypeScript error"}},
		{func(policy *codeReviewPolicy) { policy.Python++ }, []string{"Python test"}},
		{func(policy *codeReviewPolicy) { policy.GoTestGroups++ }, []string{"Go test group"}},
		{
			func(policy *codeReviewPolicy) { policy.ScriptTestGroups++ },
			[]string{"TypeScript test group", "Python test group"},
		},
		{func(policy *codeReviewPolicy) { policy.Model += "-next" }, slices.Collect(maps.Keys(requests))},
	} {
		policy := codeReviewCurrentPolicy
		test.change(&policy)

		for name, each := range requests {
			moved := key(policy, each) != key(codeReviewCurrentPolicy, each)
			if moved != slices.Contains(test.changes, name) {
				t.Errorf("version change for %v: %s key changed=%v", test.changes, name, moved)
			}
		}
	}
}

// A folder fenced off with its own go.mod holds no Go package of the module,
// so linting only it must not need the Go code to compile.
func TestScriptOnlyPatternSkipsGoPackages(t *testing.T) {
	t.Parallel()

	dir := newTestModule(t)
	writeFile(
		t,
		filepath.Join(dir, "broken", "broken.go"),
		"package broken\n\nfunc broken() { undefined() }\n",
	)
	writeScriptModule(t, dir)
	writeFile(
		t,
		filepath.Join(dir, "web", "app.ts"),
		"export function long(): number {\n  let total = 0\n"+
			strings.Repeat("  total += 1\n", 41)+"  return total\n}\n",
	)
	initTestGitRepository(t, dir)

	issues, err := LintRepository(
		t.Context(),
		[]string{"./web/..."},
		dir,
		Options{MaxStates: 32},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	if !hasIssueKind(issues, "long_function") {
		t.Fatalf("script-only scan missed long_function:\n%s", joinMessages(issues))
	}
}
