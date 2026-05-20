package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRepoDeadCodeKeepsKnownOptionalByteReaderMethod(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), `module example.com/sample

go 1.22

require github.com/ulikunitz/xz v0.0.0

replace github.com/ulikunitz/xz => ./xz
`)
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_ = lib.Live()
}
`)
	writeFile(t, filepath.Join(tmp, "xz", "go.mod"), "module github.com/ulikunitz/xz\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "xz", "lzma", "reader.go"), `package lzma

import "io"

type ReaderConfig struct{}

func (ReaderConfig) NewReader(io.Reader) error {
	return nil
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import (
	"io"

	"github.com/ulikunitz/xz/lzma"
)

type headerReader struct{}

func (*headerReader) Read([]byte) (int, error) { return 0, io.EOF }
func (*headerReader) ReadByte() (byte, error) { return 0, io.EOF }
func (*headerReader) unusedPrivate() {}

func Live() error {
	return lzma.ReaderConfig{}.NewReader(&headerReader{})
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	for _, unexpected := range []string{
		`method "Read"`,
		`method "ReadByte"`,
	} {
		if strings.Contains(joined, unexpected) {
			t.Fatalf(
				"optional byte reader method reported dead for %q, got:\n%s",
				unexpected,
				joined,
			)
		}
	}

	if !strings.Contains(
		joined,
		`private method "unusedPrivate" is never used by production code; remove it`,
	) {
		t.Fatalf("expected unused private method finding, got:\n%s", joined)
	}
}
