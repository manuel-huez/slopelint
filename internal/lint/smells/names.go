package smells

import (
	"go/types"
	"strings"
	"unicode"
	"unicode/utf8"
)

func isBoolType(t types.Type) bool {
	basic, ok := types.Unalias(t).Underlying().(*types.Basic)

	return ok && basic.Info()&types.IsBoolean != 0
}

func isErrorType(t types.Type) bool {
	errorType := types.Universe.Lookup("error")

	return errorType != nil && types.Identical(types.Unalias(t), errorType.Type())
}

// IsPredicateName reports names such as IsReady: "Is" followed by an
// uppercase letter, digit, or underscore.
func IsPredicateName(name string) bool {
	rest, ok := strings.CutPrefix(name, "Is")
	if !ok || rest == "" {
		return false
	}

	r, _ := utf8.DecodeRuneInString(rest)

	return r == '_' || unicode.IsUpper(r) || unicode.IsDigit(r)
}
