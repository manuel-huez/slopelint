package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestBehaviorClonePropagatesNestedCallEffects(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

var state int

func leftWrite(value int) { state = value }
func rightWrite(input int) { state = input }

func First(value int) {
	defer leftWrite(value)
	if value > 0 { go leftWrite(value) }
	leftWrite(value + 1)
}

func Second(input int) {
	defer rightWrite(input)
	if input > 0 { go rightWrite(input) }
	rightWrite(input + 1)
}
`)

	joined := joinMessages(lintInDir(t, tmp))
	if !strings.Contains(
		joined,
		`function "Second" has same supported behavior as "First" (effects: writes, calls, defers, concurrent)`,
	) {
		t.Fatalf("nested call effects missing from caller clone:\n%s", joined)
	}
}

func TestBehaviorClonePropagatesEquivalentCallsAcrossPackages(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "first", "first.go"), `package first

func adjust(value int) int { return value + 1 }

func Transform(value int) int {
	next := adjust(value)
	if next > 10 { return next * 2 }
	return next
}
`)
	writeFile(t, filepath.Join(tmp, "second", "second.go"), `package second

func increment(input int) int { return input + 1 }

func Transform(input int) int {
	next := increment(input)
	if next > 10 { return next * 2 }
	return next
}
`)

	joined := joinMessages(lintInDir(t, tmp))
	if !strings.Contains(
		joined,
		`function "Transform" has same supported behavior as "Transform" in package "example.com/sample/first"`,
	) {
		t.Fatalf("transitive cross-package clone missing:\n%s", joined)
	}
}

func TestBehaviorClonePropagatesEquivalentCallsIntoBlocks(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "first", "first.go"), `package first

func adjust(value int) int { return value + 1 }

func Transform(value int) int {
	if adjust(value) < 0 { return -1 }
	if adjust(value) == 0 { return 0 }
	if adjust(value) > 10 { return 10 }
	return value
}
`)
	writeFile(t, filepath.Join(tmp, "second", "second.go"), `package second

func increment(input int) int { return input + 1 }

func Transform(input int) int {
	if increment(input) < 0 { return -1 }
	if increment(input) == 0 { return 0 }
	if increment(input) > 10 { return 10 }
	return input + 1
}
`)

	joined := joinMessages(lintInDir(t, tmp))
	if !strings.Contains(
		joined,
		`behavior block in "Transform" duplicates block in "Transform" in package "example.com/sample/first"`,
	) {
		t.Fatalf("transitive cross-package block clone missing:\n%s", joined)
	}
}
