package lint

import (
	"go/token"
	"path/filepath"
	"testing"
)

func TestSortIssuesAcrossFileSetsUsesNumericLines(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	contents := []byte("1\n2\n3\n4\n5\n6\n7\n8\n9\n10")
	file := fset.AddFile("sample.go", -1, len(contents))
	file.SetLinesForContent(contents)

	issues := []Issue{
		{Pos: file.LineStart(10), Message: "line ten", fset: fset},
		{Pos: file.LineStart(2), Message: "line two", fset: fset},
	}

	sortIssues(issues)

	if issues[0].Message != "line two" {
		t.Fatalf("first issue = %q, want line two", issues[0].Message)
	}
}

func TestFindingsExposeKinds(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func F(s string) {
	if s == "" { return }
	if s == "" { println("bad") }
}
`)

	issues := lintInDir(t, tmp)
	if len(issues) == 0 {
		t.Fatal("expected finding")
	}

	if issues[0].Kind != "redundant_condition" {
		t.Fatalf("issue kind = %q, want redundant_condition", issues[0].Kind)
	}
}
