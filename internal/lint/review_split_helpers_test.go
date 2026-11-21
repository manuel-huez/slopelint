package lint

import (
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCodeReviewSendsSplitHelpersWithCaller(t *testing.T) {
	tmp, _ := newReviewTestModule(t)
	model := new(reviewTestModel)

	if _, err := lintWithReviewForTest(t, tmp, t.TempDir(), model); err != nil {
		t.Fatal(err)
	}

	var got []string

	for _, request := range model.requests {
		for _, item := range request.Items {
			if item.Kind == codeReviewSplitHelperItem {
				caller, _, _ := strings.Cut(request.Source, "\n")
				got = append(got, caller+" / "+item.Facts)
			}
		}
	}

	want := []string{
		"1| func Totals(values []int) (int, int) { / only Totals calls it, once; the call passes 1 of Totals's variables, and it returns 2 values",
		"1| func addressed(path string) { / only addressed calls it, once; the call passes 1 of addressed's variables, and it returns 0 values",
	}
	if slices.Sort(got); !slices.Equal(got, want) {
		t.Fatalf(
			"split helper items:\n%s\nwant:\n%s",
			strings.Join(got, "\n"),
			strings.Join(want, "\n"),
		)
	}
}

func TestCodeReviewNeedsTwoFragmentVotes(t *testing.T) {
	tmp, _ := newReviewTestModule(t)

	var votes atomic.Int32

	model := &reviewTestModel{verdict: func(item codeReviewItem, text string) codeReviewVerdict {
		if item.Kind == codeReviewSplitHelperItem && strings.Contains(text, "Part") &&
			votes.Add(1) > 1 {
			return codeReviewVerdict{Verdict: codeReviewOwnJob}
		}

		return reviewTestVerdict(item, text)
	}}

	issues, err := lintWithReviewForTest(t, tmp, t.TempDir(), model)
	if err != nil {
		t.Fatal(err)
	}

	if votes.Load() != 2 {
		t.Fatalf("fragment votes = %d, want 2", votes.Load())
	}

	if hasIssueKind(issues, codeReviewFindingKinds[codeReviewFragment]) {
		t.Fatalf("one fragment vote became a finding:\n%s", joinMessages(issues))
	}
}

func TestCodeReviewJudgesTypeScriptSplitHelpers(t *testing.T) {
	dir := newTestModule(t)
	writeScriptModule(t, dir)
	writeFile(
		t,
		filepath.Join(dir, "web", "totals.ts"),
		`function totalsPart(values: number[], sum: number) {
  for (const value of values) sum += value
  return sum
}

export function totals(values: number[]) {
  const start = 0
  return totalsPart(values, start)
}
`,
	)
	initTestGitRepository(t, dir)

	model := new(reviewTestModel)

	issues, err := lintWithReviewForTest(t, dir, t.TempDir(), model)
	if err != nil {
		t.Fatal(err)
	}

	var facts []string

	for _, request := range model.requests {
		for _, item := range request.Items {
			if item.Kind == codeReviewSplitHelperItem {
				facts = append(facts, string(request.Language)+": "+item.Facts)
			}
		}
	}

	want := []string{"typescript: only totals calls it, once; the call passes 2 of totals's names"}
	if !slices.Equal(facts, want) ||
		!hasIssueKind(issues, codeReviewFindingKinds[codeReviewFragment]) {
		t.Fatalf(
			"split helper items = %q, want %q, and a fragment finding in:\n%s",
			facts,
			want,
			joinMessages(issues),
		)
	}
}
