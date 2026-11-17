// Package outline describes source files in a language-neutral shape: their
// functions, types, comment groups, and top-level declarations. Go fills it
// from go/ast; TypeScript and Python fill it from tree-sitter syntax trees.
package outline

import (
	"path"
	"slices"
	"strings"
)

// Language names a source language.
type Language string

// Supported languages. TSX files use the TypeScript language.
const (
	Go         Language = "go"
	TypeScript Language = "typescript"
	Python     Language = "python"
)

// Pos is a source position. Line and Column are 1-based; Column counts bytes.
type Pos struct {
	Offset int
	Line   int
	Column int
}

// File is the outline of one source file.
type File struct {
	Lines int
	// Start anchors file-level findings.
	Start Pos
	// Test marks test and test-support code.
	Test      bool
	Generated bool
	Funcs     []Func
	Types     []Type
	Comments  []Comment
	Decls     []Decl
}

// Func is one function, method, or function literal.
type Func struct {
	// Name is empty for literals.
	Name string
	// Pos is the name, or the start of a literal.
	Pos       Pos
	StartLine int
	EndLine   int
	Params    int
	Results   int
	Body      bool
	// Literal marks an anonymous function value: a Go func literal, an inline
	// TypeScript callback, or a Python lambda.
	Literal bool
	// Receiver names the type or class that owns a method.
	Receiver string
}

// Type is one type or class that can own methods.
type Type struct {
	Name string
	Pos  Pos
}

// Comment is one group of adjacent comments, or one Python docstring.
type Comment struct {
	Pos     Pos
	EndLine int
	// Suppression marks a group whose first comment silences a check.
	Suppression bool
}

// Decl is the line range of one top-level statement, including the comment
// group directly above it.
type Decl struct {
	StartLine int
	EndLine   int
}

// LanguageFor returns the language of a TypeScript or Python file that
// slopelint lints, or "" for other files, including declaration files and
// generated or minified bundles named by convention.
func LanguageFor(name string) Language {
	base := path.Base(strings.ReplaceAll(name, "\\", "/"))

	switch {
	case strings.HasSuffix(base, ".d.ts") || strings.HasSuffix(base, ".gen.ts") ||
		strings.Contains(base, ".min."):
		return ""
	case strings.HasSuffix(base, ".ts") || strings.HasSuffix(base, ".tsx"):
		return TypeScript
	case strings.HasSuffix(base, ".py"):
		return Python
	default:
		return ""
	}
}

// generatedMarkerLines bounds the header that may carry a generated marker.
const generatedMarkerLines = 5

func generated(src []byte) bool {
	header := string(src)
	for range generatedMarkerLines {
		line, rest, _ := strings.Cut(header, "\n")
		if strings.Contains(line, "@generated") || strings.Contains(line, "DO NOT EDIT") ||
			strings.Contains(line, "Code generated") {
			return true
		}

		header = rest
	}

	return false
}

// testFile mirrors each ecosystem's test discovery: Jest and Vitest names
// and __tests__ directories, pytest names, tests directories, and conftest.py.
func testFile(name string, language Language) bool {
	name = strings.ReplaceAll(name, "\\", "/")
	dirs := strings.Split(path.Dir(name), "/")
	base := path.Base(name)

	if language == TypeScript {
		stem := strings.TrimSuffix(strings.TrimSuffix(base, ".tsx"), ".ts")

		return strings.HasSuffix(stem, ".test") || strings.HasSuffix(stem, ".spec") ||
			slices.Contains(dirs, "__tests__")
	}

	return strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py") ||
		base == "conftest.py" || slices.Contains(dirs, "tests")
}

func lineCount(src []byte) int {
	lines := strings.Count(string(src), "\n")
	if len(src) > 0 && src[len(src)-1] != '\n' {
		lines++
	}

	return lines
}
