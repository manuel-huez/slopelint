package lint

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// TestRuleGuidesMatchReadme keeps the guide table and the README rule tables
// naming the same rules, so every reported rule prints its fix.
func TestRuleGuidesMatchReadme(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}

	var documented []string

	name := regexp.MustCompile("`([a-z]+(?:_[a-z]+)+)`")

	for line := range strings.Lines(string(data)) {
		if cells := strings.Split(line, "|"); len(cells) > 2 && strings.HasPrefix(line, "| `") {
			for _, match := range name.FindAllStringSubmatch(cells[1], -1) {
				documented = append(documented, match[1])
			}
		}
	}

	slices.Sort(documented)

	guided := slices.Sorted(func(yield func(string) bool) {
		for kind, guide := range ruleGuides {
			if guide.finds == "" || guide.fix == "" || !yield(kind) {
				return
			}
		}
	})

	if !slices.Equal(guided, documented) {
		t.Fatalf("rule guides = %q\nREADME rules = %q", guided, documented)
	}
}
