package smells

import (
	"go/ast"
	"go/types"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

func (l *Runner) checkTestSupportFilenames() {
	for _, file := range l.pkg.TestSupportFiles {
		if ast.IsGenerated(file) {
			continue
		}

		name := strings.TrimSuffix(filepath.Base(l.fileName(file)), ".go")
		// A delimited marker also permits Go's trailing OS and architecture suffixes.
		if strings.Contains("_"+name+"_", "_test_support_") {
			continue
		}

		l.report(
			file.Package,
			"test_support_filename",
			"shared test-only source must use a test_support filename marker (for example helpers_test_support.go); keep .go so other packages' tests can import it",
		)
	}
}

func splitIdentifierWords(name string) []string {
	var words []string

	start := -1
	runes := []rune(name)

	flush := func(end int) {
		if start < 0 || start >= end {
			return
		}

		words = append(words, strings.ToLower(string(runes[start:end])))
		start = -1
	}

	for idx, r := range runes {
		if r == '_' {
			flush(idx)
			continue
		}

		if start < 0 {
			start = idx
			continue
		}

		prev := runes[idx-1]

		nextLower := idx+1 < len(runes) && unicode.IsLower(runes[idx+1])
		if unicode.IsLower(prev) && unicode.IsUpper(r) ||
			unicode.IsUpper(prev) && unicode.IsUpper(r) && nextLower {
			flush(idx)
			start = idx
		}
	}

	flush(len(runes))

	return words
}

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
