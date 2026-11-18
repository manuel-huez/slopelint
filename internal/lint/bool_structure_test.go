package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectsBooleanCeremonyAndRedundantBranches(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, source, want, failure, kind string
	}{
		{
			name: "DetectsRedundantBoolReturn",
			source: `package sample

func f(ok bool) bool {
	if ok {
		return true
	}
	return false
}
`,
			want:    `if returns boolean literals; replace with "return ok"`,
			failure: "expected bool-return ceremony finding, got:\n%s",
			kind:    "boolean_ceremony",
		},
		{
			name: "DetectsRedundantBoolAssignment",
			source: `package sample

func f(ok bool) bool {
	out := false
	if ok {
		out = true
	} else {
		out = false
	}
	return out
}
`,
			want:    `if assigns boolean literals; replace with "out = ok"`,
			failure: "expected bool-assignment ceremony finding, got:\n%s",
		},
		{
			name: "DetectsIdenticalIfElseBodies",
			source: `package sample

func f(ok bool) error {
	if ok {
		return nil
	} else {
		return nil
	}
}
`,
			want:    `if and else branches are identical; preserve condition evaluation and hoist shared body`,
			failure: "expected identical-branch finding, got:\n%s",
			kind:    "control_flow_merge",
		},
		{
			name: "DetectsRedundantReturnGuardBeforeFallback",
			source: `package sample

func f(err error) error {
	if err != nil {
		return nil
	}

	return nil
}
`,
			want:    `1 guard return(s) duplicate following return "return nil"; drop redundant branch checks`,
			failure: "expected redundant return guard finding, got:\n%s",
		},
		{
			name: "DetectsRedundantReturnGuardSetBeforeFallback",
			source: `package sample

func f(first bool, second bool) int {
	if first {
		return 0
	}
	if second {
		return 0
	}

	return 0
}
`,
			want:    `2 guard return(s) duplicate following return "return 0"; drop redundant branch checks`,
			failure: "expected redundant return guard set finding, got:\n%s",
		},
		{
			name: "DetectsNestedFinalIfPyramid",
			source: `package sample

func f(ok bool, ready bool) {
	if ok {
		if ready {
			println("run")
		}
	}
}
`,
			want:    `nested if pyramid has 2 levels at function end; invert conditions into guard clauses`,
			failure: "expected nested if pyramid finding, got:\n%s",
		},
		{
			name: "DetectsIdenticalSwitchCaseBodies",
			source: `package sample

func f(ok bool) int {
	switch ok {
	case true:
		return 1
	case false:
		return 1
	}
	return 0
}
`,
			want:    `switch case "false" has identical body as previous case "true"; merge case lists`,
			failure: "expected identical switch branch finding, got:\n%s",
		},
		{
			name: "DetectsExhaustiveBoolDefault",
			source: `package sample

func f(ok bool) {
	switch ok {
	case true:
		println("yes")
	case false:
		println("no")
	default:
		println("dead")
	}
}
`,
			want:    `default case is redundant; bool switch already covers true and false`,
			failure: "expected redundant default finding, got:\n%s",
			kind:    "redundant_default",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			tmp := newTestModule(t)
			writeFile(t, filepath.Join(tmp, "sample.go"), test.source)

			issues := lintInDir(t, tmp)
			joined := joinMessages(issues)

			if !strings.Contains(joined, test.want) {
				t.Fatalf(test.failure, joined)
			}

			if test.kind != "" && !hasIssueKind(issues, test.kind) {
				t.Fatalf("expected %s kind, got %#v", test.kind, issues)
			}
		})
	}
}

func TestSkipsBranchMergesWhenBehaviorDiffers(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, source, forbidden, failure string
	}{
		{
			name: "SkipsIdenticalIfElseBodiesWhenScopeWouldChange",
			source: `package sample

func f(ok bool) int {
	if ok {
		v := 1
		return v
	} else {
		v := 1
		return v
	}
}
`,
			forbidden: `if and else branches are identical; preserve condition evaluation and hoist shared body`,
			failure:   "unexpected identical-branch finding when branch-local defs exist, got:\n%s",
		},
		{
			name: "SkipsRedundantReturnGuardWhenConditionMayPanic",
			source: `package sample

type req struct { name string }

func f(req *req) string {
	if req.name == "" {
		return ""
	}

	return ""
}
`,
			forbidden: `drop redundant branch checks`,
			failure:   "unexpected redundant return guard finding for selector condition, got:\n%s",
		},
		{
			name: "SkipsRedundantReturnGuardWhenEqualityMayPanic",
			source: `package sample

type box struct { value any }

func F(left any, right any) int {
	if left == right {
		return 0
	}

	return 0
}

func G(left box, right box) int {
	if left == right {
		return 0
	}

	return 0
}
`,
			forbidden: `drop redundant branch checks`,
			failure:   "unexpected redundant return guard finding for equality that may panic, got:\n%s",
		},
		{
			name: "SkipsNestedIfPyramidWhenWorkFollows",
			source: `package sample

func f(ok bool, ready bool) {
	if ok {
		if ready {
			println("run")
		}
	}

	println("done")
}
`,
			forbidden: `nested if pyramid`,
			failure:   "unexpected nested if pyramid finding with trailing work, got:\n%s",
		},
		{
			name: "SkipsNonAdjacentPredicateSwitchBodies",
			source: `package sample

func f(x int) int {
	switch {
	case x < 0:
		return -1
	case x == 0:
		return 0
	case x < 10:
		return -1
	}
	return 1
}
`,
			forbidden: `has identical body as previous case`,
			failure:   "unexpected identical switch branch finding for non-adjacent predicate cases, got:\n%s",
		},
		{
			name: "SkipsExhaustiveBoolDefaultWithPanic",
			source: `package sample

func f(ok bool) {
	switch ok {
	case true:
		return
	case false:
		return
	default:
		panic("impossible")
	}
}
`,
			forbidden: `default case is redundant; bool switch already covers true and false`,
			failure:   "unexpected redundant default finding for panic default, got:\n%s",
		},
		{
			name: "SkipsExhaustiveConstSetDefault",
			source: `package sample

type mode int

const (
	modeRead mode = iota
	modeWrite
)

func f(value mode) int {
	switch value {
	case modeRead:
		return 1
	case modeWrite:
		return 2
	default:
		return 0
	}
}
`,
			forbidden: `default case is redundant`,
			failure:   "unexpected redundant const-set default finding, got:\n%s",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			tmp := newTestModule(t)
			writeFile(t, filepath.Join(tmp, "sample.go"), test.source)

			if joined := joinMessages(lintInDir(t, tmp)); strings.Contains(joined, test.forbidden) {
				t.Fatalf(test.failure, joined)
			}
		})
	}
}
