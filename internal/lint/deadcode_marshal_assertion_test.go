package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRepoDeadCodeKeepsValueMarshalHooksFromInterfaceAssertions(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), `module example.com/sample

go 1.22

require (
	gopkg.in/yaml.v2 v2.0.0
	gopkg.in/yaml.v3 v3.0.0
)

replace gopkg.in/yaml.v2 => ./yamlv2
replace gopkg.in/yaml.v3 => ./yaml
`)
	writeFile(t, filepath.Join(tmp, "yamlv2", "go.mod"), "module gopkg.in/yaml.v2\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "yamlv2", "yaml.go"), `package yaml

type Marshaler interface {
	MarshalYAML() (interface{}, error)
}
`)
	writeFile(t, filepath.Join(tmp, "yaml", "go.mod"), "module gopkg.in/yaml.v3\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "yaml", "yaml.go"), `package yaml

type Marshaler interface {
	MarshalYAML() (interface{}, error)
}
`)
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/contract"

func main() {
	contract.Live()
}
`)
	writeFile(t, filepath.Join(tmp, "contract", "contract.go"), `package contract

import (
	"encoding"
	"encoding/json"
	"time"

	yamlv2 "gopkg.in/yaml.v2"
	yamlv3 "gopkg.in/yaml.v3"
)

type LocalDate time.Time

var _ encoding.TextMarshaler = LocalDate(time.Time{})

func (date LocalDate) MarshalText() ([]byte, error) {
	return []byte(time.Time(date).Format("2006-01-02")), nil
}

type Digest []byte

var _ encoding.BinaryMarshaler = Digest(nil)

func (digest Digest) MarshalBinary() ([]byte, error) {
	return []byte(digest), nil
}

type Status string

var _ json.Marshaler = Status("")

func (status Status) MarshalJSON() ([]byte, error) {
	return json.Marshal(string(status))
}

type Label string

var _ yamlv2.Marshaler = Label("")

func (label Label) MarshalYAML() (interface{}, error) {
	return string(label), nil
}

type Name string

var _ yamlv3.Marshaler = Name("")

func (name Name) MarshalYAML() (interface{}, error) {
	return string(name), nil
}

func Live() {}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	for _, unexpected := range []string{
		`method "MarshalText"`,
		`method "MarshalBinary"`,
		`method "MarshalJSON"`,
		`method "MarshalYAML"`,
	} {
		if strings.Contains(joined, unexpected) {
			t.Fatalf(
				"interface assertion marshal hook reported dead for %q, got:\n%s",
				unexpected,
				joined,
			)
		}
	}
}

func TestRepoDeadCodeKeepsValueTextMarshalHookThroughJSONEncoders(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), `module example.com/sample

go 1.22

require github.com/goccy/go-json v0.0.0

replace github.com/goccy/go-json => ./gojson
`)
	writeFile(
		t,
		filepath.Join(tmp, "gojson", "go.mod"),
		"module github.com/goccy/go-json\n\ngo 1.22\n",
	)
	writeFile(t, filepath.Join(tmp, "gojson", "json.go"), `package json

func Marshal(any) ([]byte, error) { return nil, nil }
`)
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/contract"

func main() {
	_, _ = contract.SaveStdlib()
	_, _ = contract.SaveGoccy()
}
`)
	writeFile(t, filepath.Join(tmp, "contract", "contract.go"), `package contract

import (
	"encoding/json"
	"time"

	goccyjson "github.com/goccy/go-json"
)

type LocalDate time.Time

func (date LocalDate) MarshalText() ([]byte, error) {
	return []byte(time.Time(date).Format("2006-01-02")), nil
}

type Event struct {
	Day LocalDate `+"`json:\"day\"`"+`
}

func SaveStdlib() ([]byte, error) {
	return json.Marshal(Event{})
}

func SaveGoccy() ([]byte, error) {
	return goccyjson.Marshal(Event{})
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	for _, unexpected := range []string{
		`field "Event.Day"`,
		`method "MarshalText"`,
	} {
		if strings.Contains(joined, unexpected) {
			t.Fatalf(
				"JSON encoder text hook dependency reported dead for %q, got:\n%s",
				unexpected,
				joined,
			)
		}
	}
}
