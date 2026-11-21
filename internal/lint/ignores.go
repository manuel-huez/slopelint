package lint

import (
	"fmt"
	"go/scanner"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	ignoreMarker      = "slopelint:ignore"
	staleIgnoreKind   = "stale_ignore"
	invalidIgnoreKind = "invalid_ignore"
	ownerFileRule     = "oversized_owner_file"
)

// IgnoreHint tells agents when they may add an ignore. It is printed once
// after findings, not in each message.
const IgnoreHint = "slopelint: fix each finding in the code. First check what each part is for, who owns and calls it, and delete, merge or simplify what does not need to exist. Only when no clean change improves the code, show the user your plan and why it fails, and ask. Add `//slopelint:ignore <rule> -- <reason>` (`#` in Python) on the line above the reported line only after the user approves. Code review findings cannot be ignored."

// ignoreDirective is one `slopelint:ignore <rule> -- <reason>` comment. It
// covers the first code line after it, or its own line when it trails code;
// a file-level rule is covered anywhere in the file.
type ignoreDirective struct {
	file   string
	line   int
	target int
	rule   string
	used   bool
}

// ignoreSet holds the directives of the scanned files. complete is true when
// the scan covers every file a finding could come from, so an unused
// directive is stale rather than out of scope.
type ignoreSet struct {
	byTarget map[ignoreKey]*ignoreDirective
	byFile   map[ignoreKey]*ignoreDirective
	all      []*ignoreDirective
	invalid  []Issue
	complete bool
}

type ignoreKey struct {
	file string
	line int
	rule string
}

// loadIgnores reads the directives of files. Unreadable files are skipped:
// lint reports their load errors.
func loadIgnores(files []string) *ignoreSet {
	set := &ignoreSet{
		byTarget: make(map[ignoreKey]*ignoreDirective),
		byFile:   make(map[ignoreKey]*ignoreDirective),
	}

	seen := make(map[string]bool, len(files))

	for _, file := range files {
		file = filepath.Clean(file)
		if seen[file] {
			continue
		}

		seen[file] = true

		if data, err := os.ReadFile(file); err == nil {
			set.parse(file, data)
		}
	}

	return set
}

// sourceComment is a comment that opens with the ignore marker. trailing
// means code precedes it on its line.
type sourceComment struct {
	line     int
	text     string
	trailing bool
}

// parse adds the directives of one file. Each covers the next line holding
// code, so doc comments and decorators may sit between it and the code.
func (set *ignoreSet) parse(file string, data []byte) {
	var (
		comments  []sourceComment
		codeLines []int
	)

	if filepath.Ext(file) == ".go" {
		comments, codeLines = goIgnoreComments(data)
	} else {
		comments, codeLines = scriptIgnoreComments(file, data)
	}

	for _, comment := range comments {
		dir := set.directive(file, comment.line, comment.text)
		if dir == nil {
			continue
		}

		// With no code after it, a directive covers its own line, so it is
		// reported stale instead of being dropped.
		dir.target = comment.line
		if next, _ := slices.BinarySearch(codeLines, comment.line+1); !comment.trailing &&
			next < len(codeLines) {
			dir.target = codeLines[next]
		}

		set.add(dir)
	}
}

// goIgnoreComments tokenizes Go source, so markers inside string literals,
// such as test fixtures, are not directives.
func goIgnoreComments(data []byte) ([]sourceComment, []int) {
	var (
		comments  []sourceComment
		codeLines []int
		scan      scanner.Scanner
	)

	fset := token.NewFileSet()
	file := fset.AddFile("", -1, len(data))
	scan.Init(file, data, nil, scanner.ScanComments)

	for {
		pos, tok, lit := scan.Scan()
		if tok == token.EOF {
			return comments, codeLines
		}

		line := file.Line(pos)

		switch {
		case tok == token.COMMENT:
			text := strings.TrimSpace(strings.TrimPrefix(lit, "//"))
			if strings.HasPrefix(text, ignoreMarker) {
				comments = append(comments, sourceComment{
					line:     line,
					text:     text,
					trailing: slices.Contains(codeLines, line),
				})
			}
		case tok == token.SEMICOLON && lit == "\n":
		case len(codeLines) == 0 || codeLines[len(codeLines)-1] != line:
			codeLines = append(codeLines, line)
		}
	}
}

// scriptIgnoreComments scans TypeScript and Python by line. Decorators and
// comment lines do not count as code.
func scriptIgnoreComments(file string, data []byte) ([]sourceComment, []int) {
	marker := "//"
	if filepath.Ext(file) == ".py" {
		marker = "#"
	}

	var (
		comments  []sourceComment
		codeLines []int
	)

	for index, raw := range strings.Split(string(data), "\n") {
		text := strings.TrimSpace(raw)

		if at := strings.Index(text, ignoreMarker); at >= 0 {
			code, opens := strings.CutSuffix(strings.TrimRight(text[:at], " \t"), marker)
			if opens {
				comments = append(comments, sourceComment{
					line:     index + 1,
					text:     text[at:],
					trailing: code != "",
				})
			}
		}

		if text != "" && !strings.HasPrefix(text, marker) && !strings.HasPrefix(text, "@") &&
			(marker != "//" || !strings.HasPrefix(text, "/*") && !strings.HasPrefix(text, "*")) {
			codeLines = append(codeLines, index+1)
		}
	}

	return comments, codeLines
}

// directive parses one marker comment; a malformed one becomes an
// invalid_ignore issue.
func (set *ignoreSet) directive(file string, line int, comment string) *ignoreDirective {
	rest := strings.TrimSpace(strings.TrimPrefix(comment, ignoreMarker))
	rule, reason, _ := strings.Cut(rest, "--")
	rule = strings.TrimSpace(rule)

	report := func(message string) *ignoreDirective {
		set.invalid = append(set.invalid, Issue{
			Kind:     invalidIgnoreKind,
			Message:  message,
			position: ignorePosition(file, line),
		})

		return nil
	}

	switch {
	case rule == "" || strings.ContainsAny(rule, " \t"):
		return report("slopelint:ignore needs one rule name, then `-- <reason>`")
	case strings.TrimSpace(reason) == "":
		return report(
			fmt.Sprintf(
				"slopelint:ignore %s needs `-- <reason>`: why no clean change improves the code, as the user approved",
				rule,
			),
		)
	case !ignorableRule(rule):
		return report(rule + " cannot be ignored; change the code")
	}

	return &ignoreDirective{file: file, line: line, rule: rule}
}

func (set *ignoreSet) add(dir *ignoreDirective) {
	set.all = append(set.all, dir)
	set.byTarget[ignoreKey{file: dir.file, line: dir.target, rule: dir.rule}] = dir

	if dir.rule == ownerFileRule {
		set.byFile[ignoreKey{file: dir.file, rule: dir.rule}] = dir
	}
}

// ignorableRule rejects code review findings, which must be fixed, and the
// rules that police ignores themselves.
func ignorableRule(rule string) bool {
	switch rule {
	case staleIgnoreKind, invalidIgnoreKind, "parse_error":
		return false
	}

	return !slices.Contains(slices.Collect(maps.Values(codeReviewFindingKinds)), rule)
}

// filter drops the issues a directive covers and marks those directives used.
func (set *ignoreSet) filter(issues []Issue) []Issue {
	if set == nil || len(set.all) == 0 {
		return issues
	}

	kept := issues[:0:0]

	for _, issue := range issues {
		position := issueFilePosition(issue)
		file := filepath.Clean(position.Filename)

		dir := set.byTarget[ignoreKey{file: file, line: position.Line, rule: issue.Kind}]
		if dir == nil {
			dir = set.byFile[ignoreKey{file: file, rule: issue.Kind}]
		}

		if dir == nil {
			kept = append(kept, issue)
			continue
		}

		dir.used = true
	}

	return kept
}

// stale returns, on a complete scan, the directives for rules that covers
// accepts that no finding used. Duplicate findings are checked apart from the
// rest, so each side asks for its own rules.
func (set *ignoreSet) stale(covers func(rule string) bool) []Issue {
	if set == nil || !set.complete {
		return nil
	}

	var issues []Issue

	for _, dir := range set.all {
		if !covers(dir.rule) || dir.used {
			continue
		}

		where := fmt.Sprintf("at line %d", dir.target)
		if dir.rule == ownerFileRule {
			where = "in this file"
		}

		issues = append(issues, Issue{
			Kind: staleIgnoreKind,
			Message: fmt.Sprintf(
				"nothing reports %s %s anymore; delete this slopelint:ignore",
				dir.rule,
				where,
			),
			position: ignorePosition(dir.file, dir.line),
		})
	}

	return issues
}

func ignorePosition(file string, line int) token.Position {
	return token.Position{Filename: file, Line: line, Column: 1}
}
