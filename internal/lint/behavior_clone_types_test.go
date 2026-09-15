package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestBehaviorCloneCanonicalizesGenericParametersAndConstraints(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func First[T ~int](input T) T {
	next := input + 1
	if next > 10 { return next * 2 }
	return next
}

func Second[Value ~int](value Value) Value {
	next := value + 1
	if next > 10 { return next * 2 }
	return next
}

func Different[T ~int64](value T) T {
	next := value + 1
	if next > 10 { return next * 2 }
	return next
}
`)

	joined := joinMessages(lintInDir(t, tmp))
	if !strings.Contains(joined, `function "Second" has same supported behavior as "First"`) {
		t.Fatalf("generic parameter rename missed clone:\n%s", joined)
	}

	if strings.Contains(joined, `function "Different" has same supported behavior`) {
		t.Fatalf("different generic constraint matched:\n%s", joined)
	}
}

func TestBehaviorCloneHandlesRecursiveGenericInterfaces(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func First[T interface { Equal(T) bool }](value T) bool {
	matched := value.Equal(value)
	if matched { return !value.Equal(value) }
	return value.Equal(value)
}

func Second[U interface { Equal(U) bool }](input U) bool {
	matched := input.Equal(input)
	if matched { return !input.Equal(input) }
	return input.Equal(input)
}
`)

	joined := joinMessages(lintInDir(t, tmp))
	if !strings.Contains(joined, `function "Second" has same supported behavior as "First"`) {
		t.Fatalf("recursive generic interface clone missing:\n%s", joined)
	}
}
