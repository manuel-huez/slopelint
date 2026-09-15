package smells

import (
	"fmt"
	"go/token"
	"testing"
)

func BenchmarkExactBehaviorMatches(b *testing.B) {
	const candidateCount = 10_000

	candidates := make([]behaviorCandidate, 0, candidateCount)
	for idx := range candidateCount {
		key := fmt.Sprintf("function|unique-%d", idx)
		if idx%100 == 99 {
			key = fmt.Sprintf("function|clone-%d", idx/100)
		}

		if idx%100 == 98 {
			key = fmt.Sprintf("function|clone-%d", idx/100)
		}

		pos := token.Pos(idx*10 + 1)
		candidates = append(candidates, newBehaviorCandidate(
			nil,
			key,
			fmt.Sprintf("fn%d", idx),
			pos,
			pos+5,
			behaviorCandidateFunction,
			10,
			0,
		))
	}

	b.ReportAllocs()

	for b.Loop() {
		matches := exactBehaviorMatches(candidates)
		if len(matches) != candidateCount/100 {
			b.Fatalf("got %d matches, want %d", len(matches), candidateCount/100)
		}
	}
}

func BenchmarkExactBehaviorMatchesDenseCloneClass(b *testing.B) {
	const candidateCount = 10_000

	candidates := make([]behaviorCandidate, 0, candidateCount)
	for idx := range candidateCount {
		pos := token.Pos(idx*10 + 1)
		candidates = append(candidates, newBehaviorCandidate(
			nil,
			"function|same-behavior",
			fmt.Sprintf("fn%d", idx),
			pos,
			pos+5,
			behaviorCandidateFunction,
			10,
			0,
		))
	}

	b.ReportAllocs()

	for b.Loop() {
		matches := exactBehaviorMatches(candidates)
		if len(matches) != candidateCount-1 {
			b.Fatalf("got %d matches, want %d", len(matches), candidateCount-1)
		}
	}
}

func FuzzExactBehaviorMatches(f *testing.F) {
	f.Add([]byte{1, 2, 1, 3, 2})
	f.Add([]byte{7, 7, 7, 7})

	f.Fuzz(func(t *testing.T, keys []byte) {
		if len(keys) > 128 {
			keys = keys[:128]
		}

		candidates := make([]behaviorCandidate, 0, len(keys))
		seen := make(map[byte]struct{})
		expected := 0

		for idx, key := range keys {
			if _, exists := seen[key]; exists {
				expected++
			} else {
				seen[key] = struct{}{}
			}

			pos := token.Pos(idx*10 + 1)
			candidates = append(candidates, newBehaviorCandidate(
				nil,
				fmt.Sprintf("function|%d", key),
				fmt.Sprintf("fn%d", idx),
				pos,
				pos+5,
				behaviorCandidateFunction,
				10,
				0,
			))
		}

		if matches := exactBehaviorMatches(candidates); len(matches) != expected {
			t.Fatalf("got %d matches, want %d", len(matches), expected)
		}
	})
}
