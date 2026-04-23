package lint

import (
	"go/token"
	"sort"
)

// Issue is one linter finding.
type Issue struct {
	Pos     token.Position
	Message string
}

func sortIssues(issues []Issue) {
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].Pos.Filename != issues[j].Pos.Filename {
			return issues[i].Pos.Filename < issues[j].Pos.Filename
		}
		if issues[i].Pos.Line != issues[j].Pos.Line {
			return issues[i].Pos.Line < issues[j].Pos.Line
		}
		if issues[i].Pos.Column != issues[j].Pos.Column {
			return issues[i].Pos.Column < issues[j].Pos.Column
		}
		return issues[i].Message < issues[j].Message
	})
}
