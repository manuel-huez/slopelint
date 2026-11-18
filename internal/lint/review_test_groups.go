package lint

import (
	"fmt"
	"go/token"
	"path/filepath"
	"slices"
	"strings"

	"github.com/manuel-huez/slopelint/internal/lint/outline"
)

// Test groups are bounded units of structurally close tests from one file,
// judged together for steps they repeat. A whole test file is too long for
// the model to judge reliably, and most files hold no such tests, so a
// cheap syntactic pass picks the units.
const (
	testGroupMinFileTests = 3
	testGroupMaxTests     = 8
	testGroupMaxLines     = 400
	// testGroupMinSavedLines is the merge size the prompt asks for. A unit
	// must be able to save it even when all its tests but one go.
	testGroupMinSavedLines = 15
	// Two tests are close when they share at least testGroupMinShared callees,
	// or all callees of a test with fewer, and that share of the callees of
	// either.
	testGroupMinShared  = 2
	testGroupMinOverlap = 0.4
)

// groupedTest is one runnable test as test grouping sees it. Lines are file
// lines, doc comment included; calls are its distinct callee names, sorted.
type groupedTest struct {
	name      string
	startLine int
	endLine   int
	position  token.Position
	calls     []string
}

func (test groupedTest) lines() int {
	return test.endLine - test.startLine + 1
}

// testGroupUnits returns the comparison units of a file's tests, in file
// order. Close tests form connected groups; each group is cut in file order
// at the size caps. Units depend only on the calls and lengths of their own
// tests, so editing a test that is close to no other changes no unit.
func testGroupUnits(tests []groupedTest) [][]groupedTest {
	if len(tests) < testGroupMinFileTests {
		return nil
	}

	parent := make([]int, len(tests))
	for index := range parent {
		parent[index] = index
	}

	root := func(index int) int {
		for parent[index] != index {
			index = parent[index]
		}

		return index
	}

	for left := range tests {
		for right := left + 1; right < len(tests); right++ {
			if closeTests(tests[left], tests[right]) {
				parent[max(root(left), root(right))] = min(root(left), root(right))
			}
		}
	}

	members := make(map[int][]groupedTest)
	for index, test := range tests {
		members[root(index)] = append(members[root(index)], test)
	}

	var units [][]groupedTest
	for index := range tests {
		units = append(units, cutTestGroup(members[index])...)
	}

	return units
}

func cutTestGroup(group []groupedTest) [][]groupedTest {
	var (
		units [][]groupedTest
		unit  []groupedTest
		lines int
	)

	flush := func() {
		if len(unit) > 1 && lines-lines/len(unit) >= testGroupMinSavedLines {
			units = append(units, unit)
		}

		unit, lines = nil, 0
	}

	for _, test := range group {
		if len(unit) == testGroupMaxTests || lines+test.lines() > testGroupMaxLines {
			flush()
		}

		unit = append(unit, test)
		lines += test.lines()
	}

	flush()

	return units
}

func closeTests(left, right groupedTest) bool {
	if left.lines()+right.lines() > testGroupMaxLines {
		return false
	}

	shared := 0

	for _, call := range left.calls {
		if _, found := slices.BinarySearch(right.calls, call); found {
			shared++
		}
	}

	return shared > 0 && shared >= min(testGroupMinShared, len(left.calls), len(right.calls)) &&
		float64(shared) >= testGroupMinOverlap*float64(max(len(left.calls), len(right.calls)))
}

// testGroupDeclarations returns one request per unit of a file's tests.
// helpers holds the one-line signatures of file-local functions, keyed as
// groupedTest.calls names them.
func testGroupDeclarations(
	filename string,
	lines []string,
	tests []groupedTest,
	helpers map[string]string,
	policy codeReviewPolicy,
) ([]codeReviewDeclaration, error) {
	var declarations []codeReviewDeclaration

	for _, unit := range testGroupUnits(tests) {
		declaration, err := newTestGroupDeclaration(filename, lines, unit, helpers, policy)
		if err != nil {
			return nil, err
		}

		declarations = append(declarations, declaration)
	}

	return declarations, nil
}

// newTestGroupDeclaration builds the request for one unit: the signatures of
// file-local helpers its tests call, then each test, with gap lines for the
// code between. The source holds only the unit, so edits elsewhere in the
// file keep its key.
func newTestGroupDeclaration(
	filename string,
	lines []string,
	unit []groupedTest,
	helpers map[string]string,
	policy codeReviewPolicy,
) (codeReviewDeclaration, error) {
	language := outline.LanguageFor(filename)
	if language == "" {
		language = outline.Go
	}

	gap := "// ..."
	if language == outline.Python {
		gap = "# ..."
	}

	var (
		source []string
		names  []string
		called []string
	)

	for _, test := range unit {
		names = append(names, test.name)

		for _, call := range test.calls {
			if helpers[call] != "" && !slices.Contains(called, call) {
				called = append(called, call)
			}
		}
	}

	slices.Sort(called)

	for _, helper := range called {
		source = append(source, helpers[helper])
	}

	first := len(source) + 1
	if len(source) > 0 {
		first++
	}

	for _, test := range unit {
		if len(source) > 0 {
			source = append(source, gap)
		}

		source = append(source, lines[test.startLine-1:test.endLine]...)
	}

	request := codeReviewRequest{
		Location: fmt.Sprintf("%s:%d", filename, unit[0].startLine),
		Language: language,
		Test:     true,
		Source:   numberedCodeReviewSource(source),
	}
	if language != outline.Go {
		request.File = filepath.Base(filename)
	}

	declaration, err := newCodeReviewDeclaration(request, []codeReviewPositionedItem{{
		item: codeReviewItem{
			Kind:  codeReviewTestGroupItem,
			Lines: codeReviewLines(first, len(source)),
			Tests: names,
		},
		position: unit[0].position,
	}}, policy)
	declaration.tests = unit

	return declaration, err
}

// testGroupVerdict checks a group verdict against its unit and returns it
// with its tests in unit order. Asking for a count of saved lines makes the
// model weigh the merge; a count below testGroupMinSavedLines accepts the
// group. Cached records keep the count, since loading validates them again.
func testGroupVerdict(verdict codeReviewVerdict, item codeReviewItem) (codeReviewVerdict, error) {
	if verdict.SavedLines < testGroupMinSavedLines {
		verdict.Verdict = codeReviewDistinct
	}

	named := make(map[string]bool, len(verdict.Tests))
	for _, name := range verdict.Tests {
		named[name] = true
	}

	tests := slices.DeleteFunc(slices.Clone(item.Tests), func(name string) bool {
		return !named[name]
	})

	switch {
	case verdict.Verdict == codeReviewDistinct:
		verdict.Tests, verdict.SavedLines, verdict.Fix = nil, 0, ""
	case !slices.Contains(codeReviewTestGroupFixes, verdict.Fix):
		return verdict, fmt.Errorf("item %s: fix %q is invalid", verdict.ID, verdict.Fix)
	case len(tests) < 2 || len(tests) != len(verdict.Tests):
		return verdict, fmt.Errorf(
			"item %s: repeated tests %q must be at least two distinct tests of %q",
			verdict.ID,
			verdict.Tests,
			item.Tests,
		)
	default:
		verdict.Tests = tests
	}

	return verdict, nil
}

// combineTestGroupVotes keeps a repeated verdict only when both asks found
// it, for the tests both named. Across repeated single asks, about a third
// of the flagged groups change; most are borderline helper extractions.
func combineTestGroupVotes(first, second codeReviewRecord) codeReviewRecord {
	for index, verdict := range first.Items {
		other := second.Items[index]
		if verdict.Verdict != codeReviewRepeated || other.Verdict != codeReviewRepeated {
			first.Items[index] = codeReviewVerdict{ID: verdict.ID, Verdict: codeReviewDistinct}
			continue
		}

		verdict.Tests = slices.DeleteFunc(slices.Clone(verdict.Tests), func(name string) bool {
			return !slices.Contains(other.Tests, name)
		})
		verdict.SavedLines = min(verdict.SavedLines, other.SavedLines)

		if len(verdict.Tests) < 2 || verdict.SavedLines < testGroupMinSavedLines {
			verdict = codeReviewVerdict{ID: verdict.ID, Verdict: codeReviewDistinct}
		}

		first.Items[index] = verdict
	}

	return first
}

type testGroupFinding struct {
	issue Issue
	tests []groupedTest
}

func newTestGroupFinding(
	kind string,
	verdict codeReviewVerdict,
	unit []groupedTest,
) testGroupFinding {
	// verdict.Tests follow unit order.
	var tests []groupedTest

	for _, test := range unit {
		if len(tests) < len(verdict.Tests) && test.name == verdict.Tests[len(tests)] {
			tests = append(tests, test)
		}
	}

	return testGroupFinding{
		issue: Issue{
			Kind: kind,
			Message: fmt.Sprintf(
				"%s Tests: %s; fix: %s.",
				verdict.Advice,
				strings.Join(verdict.Tests, ", "),
				strings.ReplaceAll(verdict.Fix, "_", " "),
			),
			position: tests[0].position,
		},
		tests: tests,
	}
}

// uncoveredTestGroups drops the groups that another finding lies in: a
// semantic duplicate or repeated fixture in one of the group's tests
// already asks for the merge.
func uncoveredTestGroups(groups []testGroupFinding, others []Issue) []testGroupFinding {
	return slices.DeleteFunc(groups, func(group testGroupFinding) bool {
		return slices.ContainsFunc(others, func(other Issue) bool {
			position := issuePosition(other)

			return slices.ContainsFunc(group.tests, func(test groupedTest) bool {
				return filepath.Clean(position.Filename) ==
					filepath.Clean(test.position.Filename) &&
					position.Line >= test.startLine && position.Line <= test.endLine
			})
		})
	})
}
