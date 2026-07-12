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
	fset    *token.FileSet
}

func sortIssues(issues []Issue) {
	sort.Slice(issues, func(i, j int) bool {
		pi := issuePosition(issues[i])
		pj := issuePosition(issues[j])

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

func issuePosition(issue Issue) token.Position {
	if issue.fset == nil {
		return token.Position{}
	}

	return issue.fset.Position(issue.Pos)
}

// FormatIssuePosition returns a stable source position for an issue.
func FormatIssuePosition(issue Issue) string {
	position := issuePosition(issue)
	if !position.IsValid() {
		return unknownPos
	}

	return position.String()
}
