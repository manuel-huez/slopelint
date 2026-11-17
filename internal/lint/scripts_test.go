package lint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
func TestScriptFilesLintAndReview(t *testing.T) {
	dir := newTestModule(t)
	longBody := "export function long(): number {\n  let total = 0\n" +
		strings.Repeat("  total += 1\n", 100) + "  return total\n}\n"

	writeFile(t, filepath.Join(dir, "main.go"), "package main\n\nfunc main() {}\n")
	writeFile(t, filepath.Join(dir, "web", "go.mod"), "module example.com/sample/web\n\ngo 1.22\n")
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

// TestCodeReviewKeysKeepPerLanguagePolicy pins Go keys to their format from
// before script languages, and script comment keys to theirs from before
// script errors and tests, so tuned verdicts stay cached. Each key follows
// only the versions of its own language and item families.
func TestCodeReviewKeysKeepPerLanguagePolicy(t *testing.T) {
	t.Parallel()

	request := codeReviewRequest{
		Language: outline.Go,
		Source:   "1| // note\n2| func f() {}\n",
		Items:    []codeReviewItem{{ID: "i1", Kind: codeReviewCommentItem, Lines: "1"}},
	}
	script := request
	script.Language, script.File = outline.TypeScript, "app.ts"

	type legacyPolicy struct {
		Schema           int    `json:"schema"`
		TypeScriptSchema int    `json:"typescript_schema,omitempty"`
		Model            string `json:"model"`
		Effort           string `json:"effort"`
	}

	legacyKey := func(policy legacyPolicy, request codeReviewRequest) string {
		t.Helper()

		policy.Model, policy.Effort = codeReviewCurrentPolicy.Model, codeReviewCurrentPolicy.Effort

		data, err := json.Marshal(struct {
			Policy legacyPolicy     `json:"policy"`
			File   string           `json:"file,omitempty"`
			Test   bool             `json:"test"`
			Source string           `json:"source"`
			Items  []codeReviewItem `json:"items"`
		}{policy, request.File, false, request.Source, request.Items})
		if err != nil {
			t.Fatal(err)
		}

		sum := sha256.Sum256(data)

		return hex.EncodeToString(sum[:])
	}

	key := func(policy codeReviewPolicy, request codeReviewRequest) string {
		t.Helper()

		value, err := codeReviewKey(policy, request)
		if err != nil {
			t.Fatal(err)
		}

		return value
	}

	if key(
		codeReviewCurrentPolicy,
		request,
	) != legacyKey(
		legacyPolicy{Schema: codeReviewPolicyVersion},
		request,
	) {
		t.Fatal("Go review key changed format")
	}

	if key(codeReviewCurrentPolicy, script) != legacyKey(
		legacyPolicy{TypeScriptSchema: codeReviewTypeScriptPolicyVersion},
		script,
	) {
		t.Fatal("TypeScript comment review key changed format")
	}

	scriptError := script
	scriptError.Items = []codeReviewItem{{ID: "i1", Kind: codeReviewErrorItem, Lines: "2"}}
	scriptTest := script
	scriptTest.Items = []codeReviewItem{{ID: "i1", Kind: codeReviewTestItem, Lines: "2"}}
	goTest := scriptTest
	goTest.Language, goTest.File = outline.Go, ""

	const goTestRequest = "Go test"

	requests := map[string]codeReviewRequest{
		"Go comment":         request,
		goTestRequest:        goTest,
		"TypeScript comment": script,
		"TypeScript error":   scriptError,
		"TypeScript test":    scriptTest,
	}

	// Each version change selects new records only for its own requests; Go
	// test requests also carry the Go comment and error version.
	for _, test := range []struct {
		change  func(*codeReviewPolicy)
		changes []string
	}{
		{func(policy *codeReviewPolicy) { policy.Schema++ }, []string{"Go comment", goTestRequest}},
		{func(policy *codeReviewPolicy) { policy.TestSchema++ }, []string{goTestRequest}},
		{func(policy *codeReviewPolicy) { policy.PythonSchema++ }, nil},
		{func(policy *codeReviewPolicy) { policy.TypeScriptSchema++ }, []string{"TypeScript comment"}},
		{func(policy *codeReviewPolicy) { policy.ScriptErrorSchema++ }, []string{"TypeScript error"}},
		{func(policy *codeReviewPolicy) { policy.ScriptTestSchema++ }, []string{"TypeScript test"}},
	} {
		policy := codeReviewCurrentPolicy
		test.change(&policy)

		for name, each := range requests {
			if moved := key(
				policy,
				each,
			) != key(
				codeReviewCurrentPolicy,
				each,
			); moved != slices.Contains(
				test.changes,
				name,
			) {
				t.Errorf("version change for %v: %s key changed=%v", test.changes, name, moved)
			}
		}
	}
}
