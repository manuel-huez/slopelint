package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestBehaviorCloneReportsIndependentClosureBodies(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func First() int {
	transform := func(value int) int {
		next := value + 1
		if next > 10 { return next * 2 }
		return next
	}
	return transform(1)
}

func Second() int {
	apply := func(input int) int {
		next := input + 1
		if next > 10 { return next * 2 }
		return next
	}
	return apply(2) + 7
}
`)

	joined := joinMessages(lintInDir(t, tmp))
	if !strings.Contains(
		joined,
		`function "Second closure" has same supported behavior as "First closure"`,
	) {
		t.Fatalf("independent closure clone missing:\n%s", joined)
	}

	if strings.Contains(joined, `function "Second" has same supported behavior as "First"`) {
		t.Fatalf("different parent functions matched:\n%s", joined)
	}
}
