package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

const loopExternalCallKind = "loop_external_call"

func TestDetectsMembershipScanInsideLoop(t *testing.T) {
	t.Parallel()

	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

import "slices"

func f(items []string, allowed []string) {
	for _, item := range items {
		if slices.Contains(allowed, item) {
			println(item)
		}
	}
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`slices.Contains scans invariant collection allowed inside loop; build a set before the loop`,
	) {
		t.Fatalf("expected membership scan finding, got:\n%s", joined)
	}

	if !hasIssueKind(issues, "loop_membership_scan") {
		t.Fatalf("expected loop_membership_scan kind, got %#v", issues)
	}
}

func TestDetectsExplicitGenericMembershipScanInsideLoop(t *testing.T) {
	t.Parallel()

	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

import "slices"

func f(items []string, allowed []string) {
	for _, item := range items {
		if slices.Contains[[]string, string](allowed, item) {
			println(item)
		}
	}
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`slices.Contains scans invariant collection allowed inside loop; build a set before the loop`,
	) {
		t.Fatalf("expected explicit generic membership scan finding, got:\n%s", joined)
	}
}

func TestSkipsMutatedMembershipScans(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, source, failure string
	}{
		{
			name: "SkipsLoopSpecificMembershipScan",
			source: `package sample

import "slices"

type group struct {
	values []string
}

func f(groups []group) {
	for _, group := range groups {
		if slices.Contains(group.values, "admin") {
			println("hit")
		}
	}
}
`,
			failure: "unexpected membership scan finding, got:\n%s",
		},
		{
			name: "SkipsMembershipScanAfterCollectionReassignedInsideLoop",
			source: `package sample

import "slices"

func allowedFor(item string) []string {
	return []string{item}
}

func f(items []string, allowed []string) {
	for _, item := range items {
		allowed = allowedFor(item)
		if slices.Contains(allowed, "admin") {
			println(item)
		}
	}
}
`,
			failure: "unexpected membership scan finding for reassigned collection, got:\n%s",
		},
		{
			name: "SkipsMembershipScanAfterCollectionIndexMutationInsideLoop",
			source: `package sample

import "slices"

func f(items []string, allowed []string) {
	for i, item := range items {
		allowed[i] = item
		if slices.Contains(allowed, item) {
			println(item)
		}
	}
}
`,
			failure: "unexpected membership scan finding after indexed mutation, got:\n%s",
		},
		{
			name: "SkipsMembershipScanAfterHelperMayMutateCollectionInsideLoop",
			source: `package sample

import "slices"

func fill(values []string, item string) {}

func f(items []string, allowed []string) {
	for _, item := range items {
		fill(allowed, item)
		if slices.Contains(allowed, item) {
			println(item)
		}
	}
}
`,
			failure: "unexpected membership scan finding after helper mutation, got:\n%s",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			tmp := newTestModule(t)
			writeFile(t, filepath.Join(tmp, "sample.go"), test.source)

			issues := lintInDir(t, tmp)
			joined := joinMessages(issues)

			if strings.Contains(joined, `scans invariant collection`) {
				t.Fatalf(test.failure, joined)
			}
		})
	}
}

func TestDetectsSortInsideLoop(t *testing.T) {
	t.Parallel()

	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

import "slices"

func f(items []string, keys []int) {
	for _, item := range items {
		slices.Sort(keys)
		println(item, keys[0])
	}
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`slices.Sort orders keys inside loop; sort once outside or maintain order incrementally`,
	) {
		t.Fatalf("expected sort-in-loop finding, got:\n%s", joined)
	}

	if !hasIssueKind(issues, "loop_sort") {
		t.Fatalf("expected loop_sort kind, got %#v", issues)
	}
}

func TestSkipsSortInsideLoopWhenComparatorVarUsesLoopValue(t *testing.T) {
	t.Parallel()

	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

import "sort"

type group struct {
	id int
}

type user struct {
	groupID int
}

func f(groups []group, users []user) {
	for _, group := range groups {
		var less = func(i int, j int) bool {
			return users[i].groupID == group.id
		}

		sort.Slice(users, less)
	}
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `sort.Slice orders users inside loop`) {
		t.Fatalf("unexpected sort finding for comparator var using loop value, got:\n%s", joined)
	}
}

type detectsLoopPerformanceSmellsCase struct {
	name, source, want, failure, kind, failure2 string
}

func runDetectsLoopPerformanceSmells(t *testing.T, cases []detectsLoopPerformanceSmellsCase) {
	t.Helper()

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			tmp := newTestModule(t)
			writeFile(t, filepath.Join(tmp, "sample.go"), test.source)

			issues := lintInDir(t, tmp)
			joined := joinMessages(issues)

			if !strings.Contains(
				joined,
				test.want,
			) {
				t.Fatalf(test.failure, joined)
			}

			if !hasIssueKind(issues, test.kind) {
				t.Fatalf(test.failure2, issues)
			}
		})
	}
}

func TestDetectsLoopInvariantAndNestedWork(t *testing.T) {
	t.Parallel()

	runDetectsLoopPerformanceSmells(t, []detectsLoopPerformanceSmellsCase{
		{
			name: "DetectsLoopInvariantRegexp",
			source: `package sample

import "regexp"

func f(items []string) {
	for _, item := range items {
		re := regexp.MustCompile("^[a-z]+$")
		if re.MatchString(item) {
			println(item)
		}
	}
}
`,
			want:     `regexp.MustCompile is recomputed inside loop without loop inputs; compute it before the loop`,
			failure:  "expected loop-invariant regexp finding, got:\n%s",
			kind:     "loop_invariant_work",
			failure2: "expected loop_invariant_work kind, got %#v",
		},
		{
			name: "DetectsNestedLookupLoop",
			source: `package sample

type order struct {
	customerID int
}

type customer struct {
	id int
	name string
}

func f(orders []order, customers []customer) {
	for _, order := range orders {
		for _, customer := range customers {
			if customer.id == order.customerID {
				println(customer.name)
				break
			}
		}
	}
}
`,
			want:     `nested range scans customers for each orders item; build a lookup map before the outer loop`,
			failure:  "expected nested lookup finding, got:\n%s",
			kind:     "nested_lookup_loop",
			failure2: "expected nested_lookup_loop kind, got %#v",
		},
		{
			name: "DetectsPairwiseComparisonLoop",
			source: `package sample

type span struct {
	start int
	end int
}

func f(spans []span) {
	for _, left := range spans {
		for _, right := range spans {
			if left.start < right.end && right.start < left.end {
				println("overlap")
			}
		}
	}
}
`,
			want:     `nested range compares pairs from spans; consider sort/two-pointer, sweep line, or bucketing for growing inputs`,
			failure:  "expected pairwise comparison finding, got:\n%s",
			kind:     "pairwise_comparison_loop",
			failure2: "expected pairwise_comparison_loop kind, got %#v",
		},
	})
}

func TestDetectsNetworkCallsInLoops(t *testing.T) {
	t.Parallel()

	runDetectsLoopPerformanceSmells(t, []detectsLoopPerformanceSmellsCase{
		{
			name: "DetectsNetworkCallInsideLoop",
			source: `package sample

import "net/http"

func f(urls []string) {
	for _, url := range urls {
		http.Get(url)
	}
}
`,
			want:     `network call http.Get inside loop can become N+1 work; batch fetch or move request outside loop`,
			failure:  "expected network call loop finding, got:\n%s",
			kind:     loopExternalCallKind,
			failure2: "expected loop_external_call kind, got %#v",
		},
		{
			name: "DetectsNetworkCallWithLoopDerivedTemp",
			source: `package sample

import "net/http"

func f(ids []string, baseURL string) {
	for _, id := range ids {
		url := baseURL + "/" + id
		http.Get(url)
	}
}
`,
			want:     `network call http.Get inside loop can become N+1 work; batch fetch or move request outside loop`,
			failure:  "expected network call loop finding through temp, got:\n%s",
			kind:     loopExternalCallKind,
			failure2: "expected loop_external_call kind, got %#v",
		},
	})
}

func TestSkipsNestedLookupLoopWithSideEffectStmt(t *testing.T) {
	t.Parallel()

	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type order struct {
	customerID int
}

type customer struct {
	id int
	name string
}

func f(orders []order, customers []customer) {
	hits := 0
	for _, order := range orders {
		for _, customer := range customers {
			hits++
			if customer.id == order.customerID {
				println(customer.name)
				break
			}
		}
	}
	println(hits)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `nested range scans customers`) {
		t.Fatalf("unexpected nested lookup finding with side effect stmt, got:\n%s", joined)
	}
}

func TestSkipsPairwiseComparisonLoopForShadowedRangeSource(t *testing.T) {
	t.Parallel()

	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type span struct {
	start int
	end int
}

func f(spans []span) {
	for _, left := range spans {
		spans := []span{{start: left.start, end: left.end}}
		for _, right := range spans {
			if left.start < right.end && right.start < left.end {
				println("overlap")
			}
		}
	}
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `nested range compares pairs from spans`) {
		t.Fatalf("unexpected pairwise comparison finding for shadowed source, got:\n%s", joined)
	}
}

func TestNetworkLoopCallReceivers(t *testing.T) {
	t.Parallel()

	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

import "net/http"

type wrappedClient struct { *http.Client }
type clientAlias = http.Client

func f(requests []*http.Request, client *http.Client, wrapped wrappedClient, alias *clientAlias) {
	for _, request := range requests {
		_ = request.Header.Get("Content-Type")
		_ = http.Header.Get(request.Header, "Content-Type")
		client.Get(request.URL.String())
		client.Do(request)
		wrapped.Head(request.URL.String())
		alias.Post(request.URL.String(), "text/plain", request.Body)
		(*http.Client).Do(client, request)
	}
}
`)

	var calls []string

	for _, issue := range lintInDir(t, tmp) {
		if issue.Kind == loopExternalCallKind {
			calls = append(calls, issue.Message)
		}
	}

	if len(calls) != 5 || strings.Contains(strings.Join(calls, "\n"), "Header.Get") {
		t.Fatalf("want five client calls and no header reads, got %v", calls)
	}
}
