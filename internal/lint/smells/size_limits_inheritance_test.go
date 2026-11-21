package smells

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/manuel-huez/slopelint/internal/lint/outline"
)

// TestGodReceiverCountsSameFileBaseClasses checks that a class split into a
// base only it extends still counts as one class, while a base two classes
// extend is shared and counts for neither.
func TestGodReceiverCountsSameFileBaseClasses(t *testing.T) {
	t.Parallel()

	methods := func(format string, count int) string {
		var out strings.Builder
		for index := range count {
			fmt.Fprintf(&out, format, index)
		}

		return out.String()
	}

	for _, test := range []struct{ name, source string }{
		{"store.ts", "class Base {\n" + methods("  base%d() {}\n", 20) + "}\n" +
			"class Store extends Base {\n" + methods("  own%d() {}\n", 21) + "}\n" +
			"class Mixin {\n" + methods("  mix%d() {}\n", 20) + "}\n" +
			"class Left extends Mixin {\n" + methods("  left%d() {}\n", 21) + "}\n" +
			"class Right extends Mixin {\n" + methods("  right%d() {}\n", 21) + "}\n"},
		{"store.py", "class Base:\n" + methods("    def base%d(self): pass\n", 20) +
			"class Store(Base, mod.Other):\n" + methods("    def own%d(self): pass\n", 21) +
			"class Mixin:\n" + methods("    def mix%d(self): pass\n", 20) +
			"class Left(Mixin):\n" + methods("    def left%d(self): pass\n", 21) +
			"class Right(Mixin):\n" + methods("    def right%d(self): pass\n", 21)},
	} {
		file, err := outline.Parse(test.name, []byte(test.source))
		if err != nil {
			t.Fatal(err)
		}

		var got []string

		for _, finding := range CheckSizes([]outline.File{file}) {
			if finding.Kind == "god_receiver" {
				got = append(got, finding.Message)
			}
		}

		want := []string{
			`type "Store" has 41 methods: 21 of its own in 1 file, plus those from Base (20)`,
		}
		if !slices.Equal(got, want) {
			t.Fatalf("%s: god_receiver = %q, want %q", test.name, got, want)
		}
	}
}
