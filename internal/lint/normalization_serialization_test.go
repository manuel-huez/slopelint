package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectsRedundantJSONMarshalText(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

import "encoding/json"

type Symbol string

func (value Symbol) String() string {
	return string(value)
}

func (value Symbol) MarshalText() ([]byte, error) {
	return []byte(value.String()), nil
}

func (value Symbol) MarshalJSON() ([]byte, error) {
	return json.Marshal(value.String())
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`Symbol.MarshalJSON only marshals String while MarshalText exists; remove MarshalJSON and let encoding/json use MarshalText`,
	) {
		t.Fatalf("expected redundant JSON/Text marshal finding, got:\n%s", joined)
	}

	if !hasIssueKind(issues, "serialization_ceremony") {
		t.Fatalf("expected serialization_ceremony kind, got %#v", issues)
	}
}

func TestSkipsJSONMarshalWhenNoMarshalText(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

import "encoding/json"

type Status string

func (value Status) String() string {
	return string(value)
}

func (value Status) MarshalJSON() ([]byte, error) {
	return json.Marshal(value.String())
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `only marshals String while MarshalText exists`) {
		t.Fatalf("unexpected redundant JSON/Text marshal finding, got:\n%s", joined)
	}
}

func TestDetectsRedundantTrimSpaceGuardReturn(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

import "strings"

func defaultName(name string) string {
	if strings.TrimSpace(name) != "" {
		return strings.TrimSpace(name)
	}

	return "fallback"
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`"strings.TrimSpace(name)" is computed multiple times in this function; bind normalized value once`,
	) {
		t.Fatalf("expected redundant trim-space finding, got:\n%s", joined)
	}

	if !hasIssueKind(issues, "normalization_ceremony") {
		t.Fatalf("expected normalization_ceremony kind, got %#v", issues)
	}
}

func TestSkipsTrimSpaceGuardReturnWhenReturnDiffers(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

import "strings"

func defaultName(name string) string {
	if strings.TrimSpace(name) != "" {
		return name
	}

	return "fallback"
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `computed multiple times`) {
		t.Fatalf("unexpected redundant trim-space finding, got:\n%s", joined)
	}
}

func TestDetectsRepeatedNestedNormalizationCall(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

import "strings"

func complete(value string, candidates []string) []string {
	out := make([]string, 0)
	for _, candidate := range candidates {
		if strings.HasPrefix(candidate, strings.ToLower(strings.TrimSpace(value))) {
			out = append(out, strings.ToLower(strings.TrimSpace(value)))
		}
	}
	return out
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`"strings.ToLower(strings.TrimSpace(value))" is computed multiple times in this function; bind normalized value once`,
	) {
		t.Fatalf("expected repeated nested normalization finding, got:\n%s", joined)
	}
}

func TestDetectsRepeatedNormalizationInFuncLiteral(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

import "strings"

func build(name string) func() string {
	return func() string {
		if strings.TrimSpace(name) != "" {
			return strings.TrimSpace(name)
		}

		return "fallback"
	}
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`"strings.TrimSpace(name)" is computed multiple times in this function; bind normalized value once`,
	) {
		t.Fatalf("expected func literal normalization finding, got:\n%s", joined)
	}
}
