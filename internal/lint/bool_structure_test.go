package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectsRedundantBoolReturn(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func f(ok bool) bool {
	if ok {
		return true
	}
	return false
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(joined, `if returns boolean literals; replace with "return ok"`) {
		t.Fatalf("expected bool-return ceremony finding, got:\n%s", joined)
	}

	if !hasIssueKind(issues, "boolean_ceremony") {
		t.Fatalf("expected boolean_ceremony kind, got %#v", issues)
	}
}

func TestDetectsRedundantBoolAssignment(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func f(ok bool) bool {
	out := false
	if ok {
		out = true
	} else {
		out = false
	}
	return out
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(joined, `if assigns boolean literals; replace with "out = ok"`) {
		t.Fatalf("expected bool-assignment ceremony finding, got:\n%s", joined)
	}
}

func TestDetectsIdenticalIfElseBodies(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func f(ok bool) error {
	if ok {
		return nil
	} else {
		return nil
	}
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`if and else branches are identical; preserve condition evaluation and hoist shared body`,
	) {
		t.Fatalf("expected identical-branch finding, got:\n%s", joined)
	}

	if !hasIssueKind(issues, "control_flow_merge") {
		t.Fatalf("expected control_flow_merge kind, got %#v", issues)
	}
}

func TestSkipsIdenticalIfElseBodiesWhenScopeWouldChange(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func f(ok bool) int {
	if ok {
		v := 1
		return v
	} else {
		v := 1
		return v
	}
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(
		joined,
		`if and else branches are identical; preserve condition evaluation and hoist shared body`,
	) {
		t.Fatalf(
			"unexpected identical-branch finding when branch-local defs exist, got:\n%s",
			joined,
		)
	}
}

func TestDetectsRedundantReturnGuardBeforeFallback(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func f(err error) error {
	if err != nil {
		return nil
	}

	return nil
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`1 guard return(s) duplicate following return "return nil"; drop redundant branch checks`,
	) {
		t.Fatalf("expected redundant return guard finding, got:\n%s", joined)
	}
}

func TestDetectsRedundantReturnGuardSetBeforeFallback(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func f(first bool, second bool) int {
	if first {
		return 0
	}
	if second {
		return 0
	}

	return 0
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`2 guard return(s) duplicate following return "return 0"; drop redundant branch checks`,
	) {
		t.Fatalf("expected redundant return guard set finding, got:\n%s", joined)
	}
}

func TestSkipsRedundantReturnGuardWhenConditionMayPanic(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type req struct { name string }

func f(req *req) string {
	if req.name == "" {
		return ""
	}

	return ""
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `drop redundant branch checks`) {
		t.Fatalf(
			"unexpected redundant return guard finding for selector condition, got:\n%s",
			joined,
		)
	}
}

func TestSkipsRedundantReturnGuardWhenEqualityMayPanic(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

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
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `drop redundant branch checks`) {
		t.Fatalf(
			"unexpected redundant return guard finding for equality that may panic, got:\n%s",
			joined,
		)
	}
}

func TestDetectsNestedFinalIfPyramid(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func f(ok bool, ready bool) {
	if ok {
		if ready {
			println("run")
		}
	}
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`nested if pyramid has 2 levels at function end; invert conditions into guard clauses`,
	) {
		t.Fatalf("expected nested if pyramid finding, got:\n%s", joined)
	}
}

func TestSkipsNestedIfPyramidWhenWorkFollows(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func f(ok bool, ready bool) {
	if ok {
		if ready {
			println("run")
		}
	}

	println("done")
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `nested if pyramid`) {
		t.Fatalf("unexpected nested if pyramid finding with trailing work, got:\n%s", joined)
	}
}

func TestDetectsIdenticalSwitchCaseBodies(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func f(ok bool) int {
	switch ok {
	case true:
		return 1
	case false:
		return 1
	}
	return 0
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`switch case "false" has identical body as previous case "true"; merge case lists`,
	) {
		t.Fatalf("expected identical switch branch finding, got:\n%s", joined)
	}
}

func TestSkipsNonAdjacentPredicateSwitchBodies(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

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
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `has identical body as previous case`) {
		t.Fatalf(
			"unexpected identical switch branch finding for non-adjacent predicate cases, got:\n%s",
			joined,
		)
	}
}

func TestDetectsExhaustiveBoolDefault(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

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
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`default case is redundant; bool switch already covers true and false`,
	) {
		t.Fatalf("expected redundant default finding, got:\n%s", joined)
	}

	if !hasIssueKind(issues, "redundant_default") {
		t.Fatalf("expected redundant_default kind, got %#v", issues)
	}
}

func TestSkipsExhaustiveBoolDefaultWithPanic(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

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
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(
		joined,
		`default case is redundant; bool switch already covers true and false`,
	) {
		t.Fatalf("unexpected redundant default finding for panic default, got:\n%s", joined)
	}
}

func TestSkipsExhaustiveConstSetDefault(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

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
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `default case is redundant`) {
		t.Fatalf("unexpected redundant const-set default finding, got:\n%s", joined)
	}
}
