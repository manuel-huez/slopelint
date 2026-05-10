package deadcode

import (
	"fmt"
)

func (decl deadCodeDecl) issue() Finding {
	message := fmt.Sprintf(
		`private %s %q is never used by production code; remove it`,
		decl.kind,
		decl.name,
	)

	if decl.exported {
		message = fmt.Sprintf(
			`exported %s %q is unreachable from repo entrypoints; remove it`,
			decl.kind,
			decl.name,
		)
	}

	return Finding{
		Pos:     decl.pos,
		Kind:    "dead_code",
		Message: message,
		FSet:    decl.pkg.FSet,
	}
}

func compareDeadCodeDecl(left, right deadCodeDecl) bool {
	leftPos := left.pkg.FSet.Position(left.pos)
	rightPos := right.pkg.FSet.Position(right.pos)

	if leftPos.Filename != rightPos.Filename {
		return leftPos.Filename < rightPos.Filename
	}

	if leftPos.Line != rightPos.Line {
		return leftPos.Line < rightPos.Line
	}

	if leftPos.Column != rightPos.Column {
		return leftPos.Column < rightPos.Column
	}

	return left.name < right.name
}
