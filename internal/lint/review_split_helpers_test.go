package lint

import (
	"slices"
	"strings"
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
