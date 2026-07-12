package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRepoDeadCodeKeepsReflectedMarshalAndUnmarshalMethods(t *testing.T) {
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

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	for _, unexpected := range []string{
		wantFieldPayloadName,
		`field "Wrapper.Value"`,
	} {
		if strings.Contains(joined, unexpected) {
			t.Fatalf(
				"YAML value container field reported dead for %q, got:\n%s",
				unexpected,
				joined,
			)
		}
	}

	if !strings.Contains(
		joined,
		`method "MarshalYAML" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf("expected unused YAML pointer marshal hook finding, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsYAMLMarshalAliasReturnFields(t *testing.T) {
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

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	for _, unexpected := range []string{
		wantFieldPayloadName,
		wantMethodMarshalYAML,
		`function "marshalPayload"`,
	} {
		if strings.Contains(joined, unexpected) {
			t.Fatalf(
				"YAML alias return dependency reported dead for %q, got:\n%s",
				unexpected,
				joined,
			)
		}
	}
}

func TestRepoDeadCodeKeepsYAMLMarshalAliasMapReturnFields(t *testing.T) {
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

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	for _, unexpected := range []string{
		wantFieldPayloadName,
		wantMethodMarshalYAML,
	} {
		if strings.Contains(joined, unexpected) {
			t.Fatalf(
				"YAML named result alias dependency reported dead for %q, got:\n%s",
				unexpected,
				joined,
			)
		}
	}
}

func TestRepoDeadCodeDoesNotKeepYAMLMarshalStaleNamedResultAliasFields(t *testing.T) {
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

func TestRepoDeadCodeKeepsYAMLMarshalExplicitNamedResultAliasFields(t *testing.T) {
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

	return out, nil
}

func Save() ([]byte, error) {
	return yaml.Marshal(Payload{})
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, wantFieldPayloadName) {
		t.Fatalf("YAML explicit named result alias field reported dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsYAMLMarshalLocalAliasReturnFields(t *testing.T) {
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

	var out any = raw(payload)

	return out, nil
}

func Save() ([]byte, error) {
	return yaml.Marshal(Payload{})
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, wantFieldPayloadName) {
		t.Fatalf("YAML local alias return field reported dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsYAMLMarshalAliasAcrossSwitch(t *testing.T) {
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

	switch {
	default:
		out = raw(payload)
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
		t.Fatalf("YAML switch alias return field reported dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeDoesNotKeepYAMLMarshalDefaultSwitchOverwriteFields(t *testing.T) {
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

func TestRepoDeadCodeKeepsYAMLMarshalAliasAcrossAppendedRange(t *testing.T) {
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
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, wantFieldPayloadName) {
		t.Fatalf("YAML range alias return field reported dead, got:\n%s", joined)
	}
}
