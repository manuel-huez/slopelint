//go:build linux && amd64 && cgo

package lint

import "testing"

func TestParseCPUList(t *testing.T) {
	t.Parallel()

	got := parseCPUList("0-2,5,8-9")
	for _, cpu := range []int{0, 1, 2, 5, 8, 9} {
		if _, ok := got[cpu]; !ok {
			t.Fatalf("CPU %d missing from %#v", cpu, got)
		}
	}

	if len(got) != 6 {
		t.Fatalf("CPU count = %d, want 6", len(got))
	}

	if invalid := parseCPUList("3-1"); invalid != nil {
		t.Fatalf("invalid CPU list = %#v, want nil", invalid)
	}
}
