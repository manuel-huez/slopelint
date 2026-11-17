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

func isIsPredicateName(name string) bool {
	if strings.TrimPrefix(name, "Is") == name || len(name) == len("Is") {
		return false
	}

	r, _ := utf8.DecodeRuneInString(name[len("Is"):])

	return r == '_' || unicode.IsUpper(r) || unicode.IsDigit(r)
}
