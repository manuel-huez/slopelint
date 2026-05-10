package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectsRedundantBoolReturn(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
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
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
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
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
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
		`if and else branches are identical; drop condition or hoist shared body`,
	) {
		t.Fatalf("expected identical-branch finding, got:\n%s", joined)
	}

	if !hasIssueKind(issues, "control_flow_merge") {
		t.Fatalf("expected control_flow_merge kind, got %#v", issues)
	}
}

func TestSkipsIdenticalIfElseBodiesWhenScopeWouldChange(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
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
		`if and else branches are identical; drop condition or hoist shared body`,
	) {
		t.Fatalf(
			"unexpected identical-branch finding when branch-local defs exist, got:\n%s",
			joined,
		)
	}
}

func TestDetectsIdenticalSwitchCaseBodies(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
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
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
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
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
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
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
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

func TestDetectsExhaustiveConstSetDefault(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
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

	if !strings.Contains(
		joined,
		`default case is redundant; mode switch covers all in-package constants`,
	) {
		t.Fatalf("expected redundant const-set default finding, got:\n%s", joined)
	}
}

func TestSkipsExhaustiveConstSetDefaultWhenZeroValueInvalid(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

import "fmt"

type mode int

const (
	modeRead mode = iota + 1
	modeWrite
)

func f(value mode) (int, error) {
	switch value {
	case modeRead:
		return 1, nil
	case modeWrite:
		return 2, nil
	default:
		return 0, fmt.Errorf("invalid mode %d", value)
	}
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `switch covers all in-package constants`) {
		t.Fatalf("unexpected redundant const-set default finding, got:\n%s", joined)
	}
}
