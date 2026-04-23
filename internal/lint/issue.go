package lint

import (
	"go/token"
	"sort"
)

// Issue is one linter finding.
type Issue struct {
	Pos     token.Pos
	Kind    string
	Message string
}

func sortIssues(fset *token.FileSet, issues []Issue) {
	sort.Slice(issues, func(i, j int) bool {
		pi := fset.Position(issues[i].Pos)
		pj := fset.Position(issues[j].Pos)

		if pi.Filename != pj.Filename {
			return pi.Filename < pj.Filename
		}

		if pi.Line != pj.Line {
			return pi.Line < pj.Line
		}

		if pi.Column != pj.Column {
			return pi.Column < pj.Column
		}

		return issues[i].Message < issues[j].Message
	})
}
