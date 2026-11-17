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
		longBody+"\nfunction sum(a: number, b: number): number {\n  // restates the sum\n  return a + b\n}\n",
	)
	writeFile(t, filepath.Join(dir, "web", "__tests__", "app.ts"), longBody)
	writeFile(t, filepath.Join(dir, "web", "types.d.ts"), longBody)
	writeFile(t, filepath.Join(dir, "node_modules", "dep", "index.ts"), longBody)
	writeFile(t, filepath.Join(dir, ".stx", "model.py"), "import os  # noqa: F401 excuse\n")
	writeFile(t, filepath.Join(dir, "pkg", "mod.py"), "import os  # noqa: F401 excuse\n")
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

		relative, _ := filepath.Rel(dir, position.Filename)
		got = append(got, filepath.ToSlash(relative)+":"+strconv.Itoa(position.Line)+" "+issue.Kind)
	}

	want := []string{
		"pkg/mod.py:1 comment_excuse",
		"web/app.ts:1 long_function",
		"web/app.ts:107 comment_noise",
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
		outline.TypeScript,
	}; !slices.Equal(
		languages,
		want,
	) {
		t.Fatalf("review languages = %v, want %v", languages, want)
	}
}

// TestCodeReviewKeysKeepPerLanguagePolicy pins Go keys to their format from
// before script languages, so tuned Go verdicts stay cached, and checks that
// each script language's key follows only its own prompt version.
func TestCodeReviewKeysKeepPerLanguagePolicy(t *testing.T) {
	request := codeReviewRequest{
		Language: outline.Go,
		Source:   "1| // note\n2| func f() {}\n",
		Items:    []codeReviewItem{{ID: "i1", Kind: codeReviewCommentItem, Lines: "1"}},
	}

	type legacyPolicy struct {
		Schema int    `json:"schema"`
		Model  string `json:"model"`
		Effort string `json:"effort"`
	}

	legacy, err := json.Marshal(struct {
		Policy legacyPolicy     `json:"policy"`
		Test   bool             `json:"test"`
		Source string           `json:"source"`
		Items  []codeReviewItem `json:"items"`
	}{
		legacyPolicy{
			codeReviewPolicyVersion,
			codeReviewCurrentPolicy.Model,
			codeReviewCurrentPolicy.Effort,
		},
		false,
		request.Source,
		request.Items,
	})
	if err != nil {
		t.Fatal(err)
	}

	key := func(policy codeReviewPolicy, request codeReviewRequest) string {
		t.Helper()

		value, err := codeReviewKey(policy, request)
		if err != nil {
			t.Fatal(err)
		}

		return value
	}

	sum := sha256.Sum256(legacy)
	if got := key(codeReviewCurrentPolicy, request); got != hex.EncodeToString(sum[:]) {
		t.Fatal("Go review key changed format")
	}

	script := request
	script.Language, script.File = outline.TypeScript, "app.ts"
	current := key(codeReviewCurrentPolicy, script)

	for _, change := range []func(*codeReviewPolicy){
		func(policy *codeReviewPolicy) { policy.Schema++ },
		func(policy *codeReviewPolicy) { policy.PythonSchema++ },
	} {
		policy := codeReviewCurrentPolicy
		change(&policy)

		if key(policy, script) != current {
			t.Fatal("TypeScript key followed another language's version")
		}
	}

	bumped := codeReviewCurrentPolicy
	bumped.TypeScriptSchema++

	if key(bumped, script) == current ||
		key(bumped, request) != key(codeReviewCurrentPolicy, request) {
		t.Fatal("TypeScript version change must select new TypeScript records only")
	}
}
