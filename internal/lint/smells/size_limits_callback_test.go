package smells

import (
	"strings"
	"testing"

	"github.com/manuel-huez/slopelint/internal/lint/outline"
)

func TestLongParamListSkipsCallbacks(t *testing.T) {
	t.Parallel()

	file, err := outline.Parse(
		"attention.py",
		[]byte(`def registered(module, query, key, value, mask, dropout, scaling):
    return query


def called(module, query, key, value, mask, dropout, scaling):
    return query


register("local", registered)
called(1, 2, 3, 4, 5, 6, 7)
`),
	)
	if err != nil {
		t.Fatal(err)
	}

	var got []string

	for _, finding := range CheckSizes([]outline.File{file}) {
		if finding.Kind == "long_param_list" {
			got = append(got, finding.Message)
		}
	}

	if len(got) != 1 || !strings.HasPrefix(got[0], `function "called"`) {
		t.Fatalf("long_param_list findings = %q, want only called", got)
	}
}
