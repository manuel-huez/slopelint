package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRepoDeadCodeKeepsReflectedMarshalAndUnmarshalMethods(t *testing.T) {
	t.Parallel()

	tmp := newYAMLTestModule(t)
	writeFile(t, filepath.Join(tmp, "yaml", "yaml.go"), `package yaml

type Node struct{}

func Marshal(any) ([]byte, error) { return nil, nil }
func Unmarshal([]byte, any) error { return nil }
func (*Node) Decode(any) error { return nil }
`)
	writeTestMain(t, tmp, `	value := lib.Scalar("x")
	_, _ = lib.Save(value)
	_, _ = lib.Load(nil)`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "gopkg.in/yaml.v3"

type Scalar string

func (value Scalar) MarshalYAML() (any, error) {
	return string(value), nil
}

type TextScalar string

func (value TextScalar) MarshalText() ([]byte, error) {
	return []byte(value), nil
}

func (value *TextScalar) UnmarshalText([]byte) error {
	*value = "decoded"

	return nil
}

type Manifest struct {
	Value *Scalar `+"`yaml:\"value\"`"+`
	Text  *TextScalar `+"`yaml:\"text\"`"+`
}

func (manifest *Manifest) UnmarshalYAML(node *yaml.Node) error {
	type rawManifest Manifest

	var raw rawManifest
	if err := node.Decode(&raw); err != nil {
		return err
	}

	*manifest = Manifest(raw)

	return nil
}

func Save(value Scalar) ([]byte, error) {
	text := TextScalar("text")

	return yaml.Marshal(Manifest{Value: &value, Text: &text})
}

func Load(body []byte) (Manifest, error) {
	var manifest Manifest
	err := yaml.Unmarshal(body, &manifest)

	return manifest, err
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	for _, unexpected := range []string{
		wantMethodMarshalYAML,
		wantMethodMarshalText,
		wantMethodUnmarshalYAML,
		wantMethodUnmarshalText,
	} {
		if strings.Contains(joined, unexpected) {
			t.Fatalf(
				"YAML-reflected declaration reported dead for %q, got:\n%s",
				unexpected,
				joined,
			)
		}
	}
}

func TestRepoDeadCodeUsesYAMLMarshalMethodSetForValueContainers(t *testing.T) {
	t.Parallel()

	tmp := newYAMLTestModule(t)
	writeYAMLMarshalStub(t, tmp)
	writeTestMain(t, tmp, `	_, _ = lib.Save()`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "gopkg.in/yaml.v3"

type Payload struct {
	Name string `+"`yaml:\"name\"`"+`
}

func (*Payload) MarshalYAML() (any, error) {
	return nil, nil
}

type Wrapper struct {
	Value Payload `+"`yaml:\"value\"`"+`
}

func Save() ([]byte, error) {
	return yaml.Marshal([]Wrapper{{}})
}
`)

	requireDeadCode(t, tmp, []string{
		wantFieldPayloadName,
		`field "Wrapper.Value"`,
	}, []string{
		`method "MarshalYAML" is unreachable from repo entrypoints`,
	})
}

func TestRepoDeadCodeKeepsYAMLMarshalAliasReturnFields(t *testing.T) {
	t.Parallel()

	tmp := newYAMLTestModule(t)
	writeYAMLMarshalStub(t, tmp)
	writeTestMain(t, tmp, `	_, _ = lib.Save()`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "gopkg.in/yaml.v3"

type Payload struct {
	Name string `+"`yaml:\"name\"`"+`
}

func (payload Payload) MarshalYAML() (any, error) {
	type raw Payload

	return marshalPayload(payload)
}

func marshalPayload(payload Payload) (any, error) {
	type raw Payload

	return []raw{raw(payload)}, nil
}

func Save() ([]byte, error) {
	return yaml.Marshal(Payload{})
}
`)

	requireDeadCode(t, tmp, []string{
		wantFieldPayloadName,
		wantMethodMarshalYAML,
		`function "marshalPayload"`,
	}, nil)
}

func TestRepoDeadCodeKeepsYAMLMarshalAliasMapReturnFields(t *testing.T) {
	t.Parallel()

	tmp := newYAMLTestModule(t)
	writeYAMLMarshalStub(t, tmp)
	writeTestMain(t, tmp, `	_, _ = lib.Save()`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "gopkg.in/yaml.v3"

type Payload struct {
	Name string `+"`yaml:\"name\"`"+`
}

func (payload Payload) MarshalYAML() (any, error) {
	type raw Payload

	return map[string]any{"payload": raw(payload)}, nil
}

func Save() ([]byte, error) {
	return yaml.Marshal(Payload{})
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, wantFieldPayloadName) {
		t.Fatalf("YAML map alias return field reported dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsCrossPackageYAMLMarshalAliasReturnFields(t *testing.T) {
	t.Parallel()

	tmp := newYAMLTestModule(t)
	writeYAMLMarshalStub(t, tmp)
	writeTestMain(t, tmp, `	_, _ = lib.Save()`)
	writeFile(t, filepath.Join(tmp, "model", "model.go"), `package model

type Payload struct {
	Name string `+"`yaml:\"name\"`"+`
}

func (payload Payload) MarshalYAML() (any, error) {
	type raw Payload

	return raw(payload), nil
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import (
	"example.com/sample/model"

	"gopkg.in/yaml.v3"
)

func Save() ([]byte, error) {
	return yaml.Marshal(model.Payload{})
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	for _, unexpected := range []string{
		wantFieldPayloadName,
		wantMethodMarshalYAML,
	} {
		if strings.Contains(joined, unexpected) {
			t.Fatalf(
				"cross-package YAML alias return dependency reported dead for %q, got:\n%s",
				unexpected,
				joined,
			)
		}
	}
}

func TestRepoDeadCodeKeepsYAMLMarshalNamedResultAliasFields(t *testing.T) {
	t.Parallel()

	tmp := newYAMLTestModule(t)
	writeYAMLMarshalStub(t, tmp)
	writeTestMain(t, tmp, `	_, _ = lib.Save()`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "gopkg.in/yaml.v3"

type Payload struct {
	Name string `+"`yaml:\"name\"`"+`
}

func (payload Payload) MarshalYAML() (out any, err error) {
	type raw Payload

	out = raw(payload)

	return
}

func Save() ([]byte, error) {
	return yaml.Marshal(Payload{})
}
`)

	requireDeadCode(t, tmp, []string{
		wantFieldPayloadName,
		wantMethodMarshalYAML,
	}, nil)
}

func TestRepoDeadCodeDoesNotKeepYAMLMarshalStaleNamedResultAliasFields(t *testing.T) {
	t.Parallel()

	tmp := newYAMLTestModule(t)
	writeYAMLMarshalStub(t, tmp)
	writeTestMain(t, tmp, `	_, _ = lib.Save()`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "gopkg.in/yaml.v3"

type Payload struct {
	Name string `+"`yaml:\"name\"`"+`
}

func (payload Payload) MarshalYAML() (out any, err error) {
	type raw Payload

	out = raw(payload)
	out = nil

	return
}

func Save() ([]byte, error) {
	return yaml.Marshal(Payload{})
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		wantRemoveFieldPayloadName,
	) {
		t.Fatalf(
			"expected stale YAML named result assignment to leave field dead, got:\n%s",
			joined,
		)
	}
}

func TestRepoDeadCodeKeepsYAMLMarshalNamedResultAliasAcrossBranch(t *testing.T) {
	t.Parallel()

	tmp := newYAMLTestModule(t)
	writeYAMLMarshalStub(t, tmp)
	writeTestMain(t, tmp, `	_, _ = lib.Save()`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "gopkg.in/yaml.v3"

type Payload struct {
	Name string `+"`yaml:\"name\"`"+`
}

func (payload Payload) MarshalYAML() (out any, err error) {
	type raw Payload

	out = raw(payload)
	if err != nil {
		out = nil
		return
	}

	return
}

func Save() ([]byte, error) {
	return yaml.Marshal(Payload{})
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, wantFieldPayloadName) {
		t.Fatalf("YAML branch named result alias field reported dead, got:\n%s", joined)
	}
}

type repoDeadCodeKeepsYAMLMarshalAliasFieldsCase struct {
	name, source, failure string
}

func runRepoDeadCodeKeepsYAMLMarshalAliasFields(
	t *testing.T,
	cases []repoDeadCodeKeepsYAMLMarshalAliasFieldsCase,
) {
	t.Helper()

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			tmp := newYAMLTestModule(t)
			writeYAMLMarshalStub(t, tmp)
			writeTestMain(t, tmp, `	_, _ = lib.Save()`)
			writeFile(t, filepath.Join(tmp, "lib", "lib.go"), test.source)

			issues := lintInDir(t, tmp)
			joined := joinMessages(issues)

			if strings.Contains(joined, wantFieldPayloadName) {
				t.Fatalf(test.failure, joined)
			}
		})
	}
}

func TestRepoDeadCodeKeepsYAMLMarshalLocalAliasFields(t *testing.T) {
	t.Parallel()

	runRepoDeadCodeKeepsYAMLMarshalAliasFields(t, []repoDeadCodeKeepsYAMLMarshalAliasFieldsCase{
		{
			name: "RepoDeadCodeKeepsYAMLMarshalExplicitNamedResultAliasFields",
			source: `package lib

import "gopkg.in/yaml.v3"

type Payload struct {
	Name string ` + "`yaml:\"name\"`" + `
}

func (payload Payload) MarshalYAML() (out any, err error) {
	type raw Payload

	out = raw(payload)

	return out, nil
}

func Save() ([]byte, error) {
	return yaml.Marshal(Payload{})
}
`,
			failure: "YAML explicit named result alias field reported dead, got:\n%s",
		},
		{
			name: "RepoDeadCodeKeepsYAMLMarshalLocalAliasReturnFields",
			source: `package lib

import "gopkg.in/yaml.v3"

type Payload struct {
	Name string ` + "`yaml:\"name\"`" + `
}

func (payload Payload) MarshalYAML() (any, error) {
	type raw Payload

	var out any = raw(payload)

	return out, nil
}

func Save() ([]byte, error) {
	return yaml.Marshal(Payload{})
}
`,
			failure: "YAML local alias return field reported dead, got:\n%s",
		},
		{
			name: "RepoDeadCodeKeepsYAMLMarshalAliasAcrossSwitch",
			source: `package lib

import "gopkg.in/yaml.v3"

type Payload struct {
	Name string ` + "`yaml:\"name\"`" + `
}

func (payload Payload) MarshalYAML() (out any, err error) {
	type raw Payload

	switch {
	default:
		out = raw(payload)
	}

	return
}

func Save() ([]byte, error) {
	return yaml.Marshal(Payload{})
}
`,
			failure: "YAML switch alias return field reported dead, got:\n%s",
		},
	})
}

func TestRepoDeadCodeKeepsYAMLMarshalAliasAcrossAppendedRange(t *testing.T) {
	t.Parallel()

	runRepoDeadCodeKeepsYAMLMarshalAliasFields(t, []repoDeadCodeKeepsYAMLMarshalAliasFieldsCase{
		{
			name: "RepoDeadCodeKeepsYAMLMarshalAliasAcrossAppendedRange",
			source: `package lib

import "gopkg.in/yaml.v3"

type Payload struct {
	Name string ` + "`yaml:\"name\"`" + `
}

func (payload Payload) MarshalYAML() (out any, err error) {
	type raw Payload

	values := []any{}
	values = append(values, raw(payload))

	for _, value := range values {
		out = value
	}

	return out, nil
}

func Save() ([]byte, error) {
	return yaml.Marshal(Payload{})
}
`,
			failure: "YAML range alias return field reported dead, got:\n%s",
		},
	})
}

func TestRepoDeadCodeDoesNotKeepYAMLMarshalDefaultSwitchOverwriteFields(t *testing.T) {
	t.Parallel()

	tmp := newYAMLTestModule(t)
	writeYAMLMarshalStub(t, tmp)
	writeTestMain(t, tmp, `	_, _ = lib.Save()`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "gopkg.in/yaml.v3"

type Payload struct {
	Name string `+"`yaml:\"name\"`"+`
}

func (payload Payload) MarshalYAML() (out any, err error) {
	type raw Payload

	out = raw(payload)
	switch {
	default:
		out = nil
	}

	return
}

func Save() ([]byte, error) {
	return yaml.Marshal(Payload{})
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		wantRemoveFieldPayloadName,
	) {
		t.Fatalf("expected YAML default switch overwrite to leave field dead, got:\n%s", joined)
	}
}

// encoding/json/v2 takes the value before its options and calls both the v1
// and the streaming hooks of field types.
func TestRepoDeadCodeKeepsJSONv2Hooks(t *testing.T) {
	t.Setenv("GOEXPERIMENT", "jsonv2")

	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_, _ = lib.Load(nil)`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
)

type Date string

func (date *Date) UnmarshalJSON([]byte) error {
	*date = "decoded"

	return nil
}

type Ticks struct{}

func (*Ticks) UnmarshalJSONFrom(*jsontext.Decoder) error { return nil }

type Payload struct {
	Day   Date
	Ticks Ticks
}

func Load(data []byte) (Payload, error) {
	var payload Payload
	err := json.Unmarshal(data, &payload, json.MatchCaseInsensitiveNames(true))

	return payload, err
}
`)

	joined := joinMessages(lintInDir(t, tmp))
	for _, method := range []string{`"UnmarshalJSON"`, `"UnmarshalJSONFrom"`} {
		if strings.Contains(joined, method) {
			t.Fatalf("json/v2 hook %s reported dead:\n%s", method, joined)
		}
	}
}

func TestRepoDeadCodeKeepsGoJSONExperimentHooks(t *testing.T) {
	t.Parallel()

	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "go.mod"), `module example.com/sample

go 1.22

require github.com/go-json-experiment/json v0.0.0

replace github.com/go-json-experiment/json => ./jsonexp
`)
	writeTestGoMod(t, filepath.Join(tmp, "jsonexp"), "github.com/go-json-experiment/json")
	writeFile(t, filepath.Join(tmp, "jsonexp", "json.go"), `package json

import "io"

type Options interface{}

func UnmarshalRead(io.Reader, any, ...Options) error { return nil }
`)
	writeFile(
		t,
		filepath.Join(tmp, "jsonexp", "jsontext", "jsontext.go"),
		"package jsontext\n\ntype Decoder struct{}\n",
	)
	writeTestMain(t, tmp, `	_ = lib.Read(nil)`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import (
	"io"

	jsonv2 "github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
)

type ticks struct{}

func (*ticks) UnmarshalJSONFrom(*jsontext.Decoder) error { return nil }

func Read(reader io.Reader) error {
	var value ticks

	return jsonv2.UnmarshalRead(reader, &value)
}
`)

	if joined := joinMessages(lintInDir(t, tmp)); strings.Contains(joined, `"UnmarshalJSONFrom"`) {
		t.Fatalf("go-json-experiment hook reported dead:\n%s", joined)
	}
}
