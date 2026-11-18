package lint

import (
	"context"
	"fmt"
	"hash/fnv"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const testGroupTestAdvice = "Table the input and wanted total around Count and its check."

// testGroupTestSource has three copies of one check and one test of other
// calls; fixture and similar mark the copies that another check reports.
func testGroupTestSource(fixture, similar string) string {
	var source strings.Builder

	source.WriteString(
		"package sample\n\nimport \"testing\"\n\nfunc writeFile(*testing.T, string, string) {}\n",
	)

	for _, name := range []string{"One", "Two", "Three"} {
		source.WriteString(`
func TestCount` + name + `(t *testing.T) {
	t.Parallel()

	writeFile(t, "` + name + `", "` + fixture + `")
	values := []int{1, 2, 3}
	if len(values) == 0 {
		t.Fatal("no values")
	}

	total := Count(values)
	if total != 3 {
		t.Fatalf("Count() = %d, want 3", total)
	}

	again := Count(values[:1])
	if again != 1 {
		t.Fatalf("Count() = %d, want 1", again)
	}
	_ = "` + similar + `"
}
`)
	}

	source.WriteString(`
func TestOther(t *testing.T) {
	t.Parallel()

	if Other() != 0 {
		t.Fatal("Other() != 0")
	}
}
`)

	return source.String()
}

func testGroupTestModel() *reviewTestModel {
	return &reviewTestModel{verdict: func(item codeReviewItem, text string) codeReviewVerdict {
		if item.Kind != codeReviewTestGroupItem {
			return keepAllForTest(item, text)
		}

		return codeReviewVerdict{
			Verdict:    codeReviewRepeated,
			Advice:     testGroupTestAdvice,
			Tests:      item.Tests,
			SavedLines: 30,
			Fix:        "table",
		}
	}}
}

// testGroupDescriber describes each block apart, so descriptions match no
// other block.
type testGroupDescriber struct{}

func (testGroupDescriber) describe(
	_ context.Context,
	requests []similarityDescriptionRequest,
	accept func([]similarityDescription) error,
) error {
	out := make([]similarityDescription, len(requests))
	for index, request := range requests {
		out[index] = similarityDescriptionForTest(request)
		if !request.Detail {
			out[index].ContractSignature += " " + request.ID
		}
	}

	return accept(out)
}

// validatingTestReviewer validates records before they are cached, as the
// Codex reviewer does.
type validatingTestReviewer struct {
	model *reviewTestModel
}

func (reviewer validatingTestReviewer) review(
	ctx context.Context,
	requests []codeReviewRequest,
	accept func([]codeReviewRecord) error,
) error {
	byID := make(map[string]codeReviewRequest, len(requests))
	for _, request := range requests {
		byID[request.ID] = request
	}

	return reviewer.model.review(ctx, requests, func(records []codeReviewRecord) error {
		for index, record := range records {
			validated, err := record.validated(byID[record.ID])
			if err != nil {
				return err
			}

			validated.ID = record.ID
			records[index] = validated
		}

		return accept(records)
	})
}

// lintTestGroupsForTest gives blocks that hold the similar marker one
// vector and other source a vector by hash, so only marked copies are
// semantic duplicates.
func lintTestGroupsForTest(
	t *testing.T,
	dir, cacheDir string,
	model *reviewTestModel,
) []string {
	t.Helper()

	t.Setenv(codexEnv, "auto")

	options := reviewTestOptions(cacheDir, model)
	options.reviewer = validatingTestReviewer{model}
	options.describer = testGroupDescriber{}
	options.embedder = &similarityTestEmbedder{vector: func(content string) []float32 {
		vector := make([]float32, 64)
		if strings.Contains(content, `"similar"`) {
			vector[0] = 1
			return vector
		}

		hash := fnv.New32a()
		_, _ = hash.Write([]byte(content))
		vector[1+hash.Sum32()%63] = 1

		return vector
	}}

	issues, err := LintRepository(
		t.Context(),
		[]string{allPackagesPattern},
		dir,
		Options{CacheEnabled: true, cacheDir: cacheDir},
		options,
	)
	if err != nil {
		t.Fatal(err)
	}

	var found []string

	for _, issue := range issues {
		if issue.Kind == codeReviewFindingKinds[codeReviewRepeated] ||
			issue.Kind == similarityIssueKind {
			found = append(found, issue.Kind+" "+FormatIssuePosition(issue)+" "+issue.Message)
		}
	}

	return found
}

func newTestGroupModule(t *testing.T, fixture, similar string) (string, string) {
	t.Helper()

	dir := newTestModule(t)
	writeFile(t, filepath.Join(dir, "sample.go"), `package sample

func Count(values []int) int { return len(values) }

func Other() int { return 0 }
`)

	testPath := filepath.Join(dir, "sample_test.go")
	writeFile(t, testPath, testGroupTestSource(fixture, similar))

	return dir, testPath
}

func TestCodeReviewReportsRepeatedTestsOnceAndKeepsUnitKeys(t *testing.T) {
	dir, testPath := newTestGroupModule(t, "a", "copy")
	cacheDir := t.TempDir()
	model := testGroupTestModel()

	want := []string{
		"test_repeated_steps " + testPath + ":7:1 " + testGroupTestAdvice +
			" Tests: TestCountOne, TestCountTwo, TestCountThree; fix: table.",
	}
	if got := lintTestGroupsForTest(t, dir, cacheDir, model); !slices.Equal(got, want) {
		t.Fatalf("findings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	groups := 0

	for _, request := range model.requests {
		if request.Items[0].Kind == codeReviewTestGroupItem {
			groups++

			if !strings.Contains(request.Source, "func writeFile(*testing.T, string, string)") ||
				strings.Contains(request.Source, "TestOther") {
				t.Fatalf("test group source:\n%s", request.Source)
			}
		}
	}

	if groups != 2 {
		t.Fatalf("test group requests = %d, want 2 votes", groups)
	}

	// A test outside the unit changes only its own requests, and the cached
	// verdict still reports.
	asked := model.requestCount()
	source := strings.Replace(testGroupTestSource("a", "copy"), "Other() != 0", "Other() > 0", 1)
	writeFile(t, testPath, source)

	if got := lintTestGroupsForTest(t, dir, cacheDir, model); !slices.Equal(got, want) {
		t.Fatalf(
			"cached findings:\n%s\nwant:\n%s",
			strings.Join(got, "\n"),
			strings.Join(want, "\n"),
		)
	}

	for _, request := range model.requests[asked:] {
		if request.Items[0].Kind == codeReviewTestGroupItem {
			t.Fatalf("edit outside the unit asked its test group again:\n%s", request.Source)
		}
	}
}

// A group is reported only when both asks find it, for the tests both name.
func TestCodeReviewKeepsTestGroupsBothVotesFind(t *testing.T) {
	for _, test := range []struct {
		name   string
		second func(codeReviewVerdict) codeReviewVerdict
		want   string
	}{
		{
			name: "second vote distinct",
			second: func(codeReviewVerdict) codeReviewVerdict {
				return codeReviewVerdict{Verdict: codeReviewDistinct}
			},
		},
		{
			name: "second vote names fewer tests",
			second: func(verdict codeReviewVerdict) codeReviewVerdict {
				verdict.Tests, verdict.SavedLines = verdict.Tests[1:], testGroupMinSavedLines
				return verdict
			},
			want: ":28:1 " + testGroupTestAdvice + " Tests: TestCountTwo, TestCountThree; fix: table.",
		},
		{
			name: "votes share one test",
			second: func(verdict codeReviewVerdict) codeReviewVerdict {
				verdict.Tests = []string{"TestCountOne", "TestCountTwo"}
				verdict.SavedLines = testGroupMinSavedLines - 1
				return verdict
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir, testPath := newTestGroupModule(t, "a", "copy")
			model := testGroupTestModel()
			first, asks := model.verdict, 0
			model.verdict = func(item codeReviewItem, text string) codeReviewVerdict {
				verdict := first(item, text)
				if item.Kind == codeReviewTestGroupItem {
					if asks++; asks == 2 {
						verdict = test.second(verdict)
					}
				}

				return verdict
			}

			got := lintTestGroupsForTest(t, dir, t.TempDir(), model)

			var want []string
			if test.want != "" {
				want = []string{"test_repeated_steps " + testPath + test.want}
			}

			if !slices.Equal(got, want) {
				t.Fatalf("findings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		})
	}
}

// A repeated fixture or a semantic duplicate in a grouped test already asks
// for the merge, so the group is not reported again.
func TestCodeReviewDropsTestGroupsOtherChecksCover(t *testing.T) {
	for _, test := range []struct {
		name, fixture, similar string
		want                   string
	}{
		{name: "repeated fixture", fixture: "shared fixture contents", similar: "copy"},
		{name: "semantic duplicate", fixture: "a", similar: "similar", want: similarityIssueKind},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir, _ := newTestGroupModule(t, test.fixture, test.similar)

			got := lintTestGroupsForTest(t, dir, t.TempDir(), testGroupTestModel())
			for _, finding := range got {
				if !strings.HasPrefix(finding, test.want) || test.want == "" {
					t.Fatalf("findings:\n%s\nwant only %q", strings.Join(got, "\n"), test.want)
				}
			}

			if test.want != "" && len(got) == 0 {
				t.Fatalf("no %s finding", test.want)
			}
		})
	}
}

// Model output names tests and a fix that findings depend on, so a group
// verdict must name tests of its unit and a known fix.
func TestCodeReviewValidatesTestGroupVerdicts(t *testing.T) {
	t.Parallel()

	names := []string{"TestA", "TestB", "TestC"}
	request := codeReviewRequest{Items: []codeReviewItem{{
		ID:    "i1",
		Kind:  codeReviewTestGroupItem,
		Lines: "1-9",
		Tests: names,
	}}}

	for _, test := range []struct {
		name    string
		verdict codeReviewVerdict
		want    string
	}{
		{name: "unknown test", verdict: codeReviewVerdict{Tests: []string{names[0], "TestX"}}, want: "must be at least two"},
		{name: "one test", verdict: codeReviewVerdict{Tests: names[:1]}, want: "must be at least two"},
		{name: "no fix", verdict: codeReviewVerdict{Fix: "none"}, want: `fix "none" is invalid`},
		{name: "short merge", verdict: codeReviewVerdict{SavedLines: 14}, want: codeReviewDistinct},
		{name: "valid", verdict: codeReviewVerdict{}, want: names[0] + " " + names[2]},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			verdict := codeReviewVerdict{
				ID:         "i1",
				Verdict:    codeReviewRepeated,
				Advice:     testGroupTestAdvice,
				Tests:      []string{names[2], names[0]},
				SavedLines: 20,
				Fix:        "table",
			}
			if test.verdict.Tests != nil {
				verdict.Tests = test.verdict.Tests
			}

			if test.verdict.Fix != "" {
				verdict.Fix = test.verdict.Fix
			}

			if test.verdict.SavedLines != 0 {
				verdict.SavedLines = test.verdict.SavedLines
			}

			record, err := codeReviewRecord{Items: []codeReviewVerdict{verdict}}.validated(request)

			got := fmt.Sprint(err)
			if err == nil {
				got = record.Items[0].Verdict + " " + strings.Join(record.Items[0].Tests, " ")
			}

			if !strings.Contains(got, test.want) {
				t.Fatalf("validated = %q, want %q", got, test.want)
			}
		})
	}
}

// TypeScript and Python tests form units by the same rule, named by title
// and by class and method.
func TestCodeReviewReportsRepeatedScriptTests(t *testing.T) {
	dir, _ := newTestGroupModule(t, "a", "copy")

	var typescript, python strings.Builder

	for _, name := range []string{"alpha", "beta", "gamma"} {
		typescript.WriteString("test('counts " + name + "', () => {\n" +
			"  const values = load('" + name + "')\n  const total = count(values)\n" +
			"  expect(total).toBe(3)\n  const again = count(values.slice(1))\n" +
			"  expect(again).toBe(2)\n  report(total, again)\n})\n\n")
		python.WriteString("    def test_" + name + "(self):\n" +
			"        values = load('" + name + "')\n        total = count(values)\n" +
			"        self.assertEqual(total, 3)\n        again = count(values[1:])\n" +
			"        self.assertEqual(again, 2)\n        report(total, again)\n        report(again, total)\n\n")
	}

	writeFile(t, filepath.Join(dir, "web", "count.test.ts"), typescript.String())
	writeFile(t, filepath.Join(dir, "tests", "test_count.py"), "class TestCount:\n"+python.String())
	initTestGitRepository(t, dir)

	var got []string

	for _, finding := range lintTestGroupsForTest(t, dir, t.TempDir(), testGroupTestModel()) {
		_, message, _ := strings.Cut(finding, " Tests: ")
		if strings.Contains(finding, "sample_test.go") {
			continue
		}

		got = append(got, filepath.Base(strings.Fields(finding)[1])+" "+message)
	}

	want := []string{
		"test_count.py:2:5 TestCount.test_alpha, TestCount.test_beta, TestCount.test_gamma; fix: table.",
		"count.test.ts:1:1 counts alpha, counts beta, counts gamma; fix: table.",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("findings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
