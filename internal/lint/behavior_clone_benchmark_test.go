package lint

import (
	"os"
	"path/filepath"
	"testing"
)

func BenchmarkBehaviorCloneRepositoryPipeline(b *testing.B) {
	dir := b.TempDir()
	benchmarkWriteFile(b, filepath.Join(dir, "go.mod"), "module example.com/bench\n\ngo 1.26\n")
	benchmarkWriteFile(b, filepath.Join(dir, "first", "first.go"), `package first

func Transform(value int) int {
	next := value + 1
	if next > 10 { return next * 2 }
	return next
}
`)
	benchmarkWriteFile(b, filepath.Join(dir, "second", "second.go"), `package second

func Transform(input int) int {
	next := input + 1
	if next > 10 { return next * 2 }
	return next
}
`)

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		issues, err := LintRepository([]string{"./..."}, dir, Options{MaxStates: 32}, nil)
		if err != nil {
			b.Fatal(err)
		}

		if !hasIssueKind(issues, "behavior_clone") {
			b.Fatal("behavior clone missing")
		}
	}
}

func benchmarkWriteFile(b *testing.B, name, content string) {
	b.Helper()

	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		b.Fatalf("create benchmark dir: %v", err)
	}

	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		b.Fatalf("write benchmark file: %v", err)
	}
}
