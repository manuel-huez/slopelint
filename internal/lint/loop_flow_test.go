package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRangeLoopInvalidatesFacts(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		source   string
		unwanted string
	}{
		{
			name: "does not keep initial nil fact across iterations",
			source: `package sample

func f(values []int) {
	var best *int
	for _, value := range values {
		if best == nil {
			copy := value
			best = &copy
			continue
		}
	}
}
`,
			unwanted: `condition "best == nil" is always true here`,
		},
		{
			name: "local helper write invalidates after-loop fact",
			source: `package sample

import (
	"errors"
	"sync"
)

var errBoom = errors.New("boom")

func f(values []int) error {
	var (
		firstErr error
		once     sync.Once
	)

	fail := func(err error) {
		if err == nil { return }
		once.Do(func() {
			firstErr = err
		})
	}

	for _, value := range values {
		if value > 0 {
			fail(errBoom)
		}
	}

	if firstErr != nil {
		return firstErr
	}

	return nil
}
`,
			unwanted: `condition "firstErr != nil" is always false here`,
		},
		{
			name: "does not discard body effects at exit",
			source: `package sample

func f(values []string) {
	candidate := ""
	for _, value := range values {
		if value == "" {
			continue
		}
		candidate = value
	}

	if candidate != "" { println("maybe") }
}
`,
			unwanted: `condition "candidate != \"\"" is always false here`,
		},
		{
			name: "drops facts of escaped address",
			source: `package sample

import "encoding/json"

func Decode(raw map[string][]byte) *string {
	var date *string

	for key, target := range map[string]any{"date": &date} {
		_ = json.Unmarshal(raw[key], target)
	}

	if date == nil {
		return nil
	}

	return date
}
`,
			unwanted: `condition "date == nil" is always true here`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tmp := newTestModule(t)
			writeFile(t, filepath.Join(tmp, "sample.go"), tc.source)

			joined := joinMessages(lintInDir(t, tmp))
			if strings.Contains(joined, tc.unwanted) {
				t.Fatalf("unexpected finding %q, got:\n%s", tc.unwanted, joined)
			}
		})
	}
}

func TestRunAnalysisRangeLoopDoesNotKeepInitialFacts(t *testing.T) {
	t.Parallel()

	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func f(values []string) {
	candidate := ""
	for _, value := range values {
		if candidate == "" {
			candidate = value
			continue
		}
	}
}
`)

	pkg := loadOnePackageForTest(t, tmp)
	pass, _ := newAnalysisTestPass(pkg, nil)

	issues, err := RunAnalysis(pass, Options{MaxStates: 32})
	if err != nil {
		t.Fatalf("run analysis: %v", err)
	}

	joined := joinMessages(issues)
	if strings.Contains(joined, `condition "candidate == \"\"" is always true here`) {
		t.Fatalf("run analysis should not keep initial loop fact, got:\n%s", joined)
	}
}

// TestForConditionUsesFactsAtEveryIteration checks that a for condition is
// judged after the body's writes: a value the body sets is not known on the
// next check, while one it never writes keeps its fact.
func TestForConditionUsesFactsAtEveryIteration(t *testing.T) {
	t.Parallel()

	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func First(names []string) string {
	var name string
	for i := 0; i < len(names) && name == ""; i++ {
		name = names[i]
	}
	return name
}

func Count(names []string) int {
	ready := true
	total := 0
	for i := 0; i < len(names) && ready; i++ {
		total++
	}
	return total
}
`)

	joined := joinMessages(lintInDir(t, tmp))
	if strings.Contains(joined, `"name == \"\""`) {
		t.Fatalf("condition the body changes reported as decided:\n%s", joined)
	}

	if !strings.Contains(joined, `right side of && is always true after the left side: "ready"`) {
		t.Fatalf("condition the body never changes not reported:\n%s", joined)
	}
}
