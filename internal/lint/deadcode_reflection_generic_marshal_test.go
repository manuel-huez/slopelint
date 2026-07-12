package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRepoDeadCodeKeepsExternalGoccyJSONGenericMarshalWrapperFields(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), `module example.com/sample

go 1.22

require (
	example.com/jsoncodec v0.0.0
	github.com/goccy/go-json v0.0.0
)

replace example.com/jsoncodec => ./jsoncodec
replace github.com/goccy/go-json => ./gojson
`)
	writeFile(
		t,
		filepath.Join(tmp, "jsoncodec", "go.mod"),
		"module example.com/jsoncodec\n\ngo 1.22\n\nrequire github.com/goccy/go-json v0.0.0\n",
	)
	writeTestGoMod(t, filepath.Join(tmp, "gojson"), "github.com/goccy/go-json")
	writeFile(t, filepath.Join(tmp, "gojson", "json.go"), `package json

func Marshal(any) ([]byte, error) { return nil, nil }
`)
	writeFile(t, filepath.Join(tmp, "jsoncodec", "codec.go"), `package jsoncodec

import "github.com/goccy/go-json"

func Encode[T any](value T) ([]byte, error) {
	return json.Marshal(value)
}
`)
	writeTestMain(t, tmp, `	_, _ = lib.Save()`)
	writeJSONCodecPayloadSave(t, tmp)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, wantFieldPayloadName) {
		t.Fatalf(
			"external goccy JSON generic marshal wrapper field reported dead, got:\n%s",
			joined,
		)
	}
}

func TestRepoDeadCodeDoesNotKeepExternalGenericMarshalShadowedParamFields(t *testing.T) {
	tmp := newJSONCodecTestModule(t)
	writeFile(t, filepath.Join(tmp, "jsoncodec", "codec.go"), `package jsoncodec

import "encoding/json"

func Encode[T any](value T) ([]byte, error) {
	{
		value := struct {
			Other string `+"`json:\"other\"`"+`
		}{}

		return json.Marshal(value)
	}
}
`)
	writeTestMain(t, tmp, `	_, _ = lib.Save()`)
	writeJSONCodecPayloadSave(t, tmp)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		wantRemoveFieldPayloadName,
	) {
		t.Fatalf(
			"expected shadowed external generic marshal param to leave field dead, got:\n%s",
			joined,
		)
	}
}

func TestRepoDeadCodeKeepsExternalGenericMarshalMethodWithSameNameFields(t *testing.T) {
	tmp := newJSONCodecTestModule(t)
	writeFile(t, filepath.Join(tmp, "jsoncodec", "codec.go"), `package jsoncodec

import "encoding/json"

type Other[T any] struct{}

func (Other[T]) Encode(value T) ([]byte, error) {
	return nil, nil
}

type Codec[T any] struct{}

func (Codec[T]) Encode(value T) ([]byte, error) {
	return json.Marshal(value)
}
`)
	writeTestMain(t, tmp, `	_, _ = lib.Save()`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "example.com/jsoncodec"

type Payload struct {
	Name string `+"`json:\"name\"`"+`
}

func Save() ([]byte, error) {
	codec := jsoncodec.Codec[Payload]{}

	return codec.Encode(Payload{})
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, wantFieldPayloadName) {
		t.Fatalf("external generic marshal method field reported dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsDelegatedExternalGenericMarshalWrapperFields(t *testing.T) {
	tmp := newJSONCodecTestModule(t)
	writeFile(t, filepath.Join(tmp, "jsoncodec", "codec.go"), `package jsoncodec

import "encoding/json"

func Encode[T any](value T) ([]byte, error) {
	return json.Marshal(value)
}
`)
	writeTestMain(t, tmp, `	_, _ = lib.Save()`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "example.com/jsoncodec"

type Payload struct {
	Name string `+"`json:\"name\"`"+`
}

func EncodeVia[T any](value T) ([]byte, error) {
	return jsoncodec.Encode(value)
}

func Save() ([]byte, error) {
	return EncodeVia(Payload{})
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, wantFieldPayloadName) {
		t.Fatalf("delegated external generic marshal wrapper field reported dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsSigsYAMLGenericMarshalWrapperJSONFields(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), `module example.com/sample

go 1.22

require (
	example.com/yamlcodec v0.0.0
	sigs.k8s.io/yaml v0.0.0
)

replace example.com/yamlcodec => ./yamlcodec
replace sigs.k8s.io/yaml => ./yaml
`)
	writeFile(
		t,
		filepath.Join(tmp, "yamlcodec", "go.mod"),
		"module example.com/yamlcodec\n\ngo 1.22\n\nrequire sigs.k8s.io/yaml v0.0.0\n",
	)
	writeFile(t, filepath.Join(tmp, "yamlcodec", "codec.go"), `package yamlcodec

import "sigs.k8s.io/yaml"

func Encode[T any](value T) ([]byte, error) {
	return yaml.Marshal(value)
}
`)
	writeFile(t, filepath.Join(tmp, "yaml", "go.mod"), "module sigs.k8s.io/yaml\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "yaml", "yaml.go"), `package yaml

func Marshal(any) ([]byte, error) {
	return nil, nil
}
`)
	writeTestMain(t, tmp, `	_, _ = lib.Save()`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "example.com/yamlcodec"

type Payload struct {
	Name string `+"`json:\"name\"`"+`
}

func Save() ([]byte, error) {
	return yamlcodec.Encode(Payload{})
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, wantFieldPayloadName) {
		t.Fatalf("sigs YAML generic marshal wrapper JSON field reported dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeDoesNotKeepFakeYAMLGenericMarshalWrapperFields(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), `module example.com/sample

go 1.22

require (
	example.com/yamlcodec v0.0.0
	example.com/yamlfake v0.0.0
)

replace example.com/yamlcodec => ./yamlcodec
replace example.com/yamlfake => ./yamlfake
`)
	writeFile(
		t,
		filepath.Join(tmp, "yamlcodec", "go.mod"),
		"module example.com/yamlcodec\n\ngo 1.22\n\nrequire example.com/yamlfake v0.0.0\n",
	)
	writeFile(t, filepath.Join(tmp, "yamlcodec", "codec.go"), `package yamlcodec

import "example.com/yamlfake"

func Encode[T any](value T) ([]byte, error) {
	return yaml.Marshal(value)
}
`)
	writeFile(
		t,
		filepath.Join(tmp, "yamlfake", "go.mod"),
		"module example.com/yamlfake\n\ngo 1.22\n",
	)
	writeFile(t, filepath.Join(tmp, "yamlfake", "yaml.go"), `package yaml

func Marshal(any) ([]byte, error) {
	return nil, nil
}
`)
	writeTestMain(t, tmp, `	_, _ = lib.Save()`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "example.com/yamlcodec"

type Payload struct {
	Name string `+"`json:\"name\"`"+`
}

func Save() ([]byte, error) {
	return yamlcodec.Encode(Payload{})
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		wantRemoveFieldPayloadName,
	) {
		t.Fatalf("expected fake YAML generic marshal wrapper field to stay dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeDoesNotKeepFakeExternalGenericMarshalWrapperFields(t *testing.T) {
	tmp := newJSONCodecTestModule(t)
	writeFile(t, filepath.Join(tmp, "jsoncodec", "codec.go"), `package jsoncodec

func Encode[T any](value T) ([]byte, error) {
	return nil, nil
}
`)
	writeTestMain(t, tmp, `	_, _ = lib.Save()`)
	writeJSONCodecPayloadSave(t, tmp)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		wantRemoveFieldPayloadName,
	) {
		t.Fatalf(
			"expected fake external generic marshal wrapper field to stay dead, got:\n%s",
			joined,
		)
	}
}

func TestRepoDeadCodeDoesNotKeepExternalGenericEncodeWithoutCodecOutput(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), `module example.com/sample

go 1.22

require example.com/tools v0.0.0

replace example.com/tools => ./tools
`)
	writeFile(t, filepath.Join(tmp, "tools", "go.mod"), "module example.com/tools\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "tools", "tools.go"), `package tools

func Encode[T any](value T) ([]byte, error) {
	return nil, nil
}
`)
	writeTestMain(t, tmp, `	_, _ = lib.Save()`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "example.com/tools"

type Payload struct {
	Name string `+"`json:\"name\"`"+`
}

func Save() ([]byte, error) {
	return tools.Encode(Payload{})
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		wantRemoveFieldPayloadName,
	) {
		t.Fatalf("expected non-codec external encode to leave field dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeDoesNotKeepExternalGenericEncodeFromBareCodecPackage(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), `module example.com/sample

go 1.22

require example.com/codec v0.0.0

replace example.com/codec => ./codec
`)
	writeFile(t, filepath.Join(tmp, "codec", "go.mod"), "module example.com/codec\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "codec", "codec.go"), `package codec

func Encode[T any](value T) ([]byte, error) {
	return nil, nil
}
`)
	writeTestMain(t, tmp, `	_, _ = lib.Save()`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "example.com/codec"

type Payload struct {
	Name string `+"`json:\"name\"`"+`
}

func Save() ([]byte, error) {
	return codec.Encode(Payload{})
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		wantRemoveFieldPayloadName,
	) {
		t.Fatalf("expected bare codec external encode to leave field dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeDoesNotKeepExternalGenericEncodeFromJSONNamedNonCodec(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), `module example.com/sample

go 1.22

require example.com/jsonfake v0.0.0

replace example.com/jsonfake => ./jsonfake
`)
	writeFile(
		t,
		filepath.Join(tmp, "jsonfake", "go.mod"),
		"module example.com/jsonfake\n\ngo 1.22\n",
	)
	writeFile(t, filepath.Join(tmp, "jsonfake", "fake.go"), `package jsonfake

func Encode[T any](value T) ([]byte, error) {
	return nil, nil
}
`)
	writeTestMain(t, tmp, `	_, _ = lib.Save()`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "example.com/jsonfake"

type Payload struct {
	Name string `+"`json:\"name\"`"+`
}

func Save() ([]byte, error) {
	return jsonfake.Encode(Payload{})
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		wantRemoveFieldPayloadName,
	) {
		t.Fatalf("expected jsonfake external encode to leave field dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeDoesNotKeepExternalGenericEncodeJSONFromJSONNamedNonCodec(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), `module example.com/sample

go 1.22

require example.com/jsonfake v0.0.0

replace example.com/jsonfake => ./jsonfake
`)
	writeFile(
		t,
		filepath.Join(tmp, "jsonfake", "go.mod"),
		"module example.com/jsonfake\n\ngo 1.22\n",
	)
	writeFile(t, filepath.Join(tmp, "jsonfake", "fake.go"), `package jsonfake

func EncodeJSON[T any](value T) ([]byte, error) {
	return nil, nil
}
`)
	writeTestMain(t, tmp, `	_, _ = lib.Save()`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "example.com/jsonfake"

type Payload struct {
	Name string `+"`json:\"name\"`"+`
}

func Save() ([]byte, error) {
	return jsonfake.EncodeJSON(Payload{})
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		wantRemoveFieldPayloadName,
	) {
		t.Fatalf("expected jsonfake EncodeJSON to leave field dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeDoesNotKeepGenericMarshalFromDeadFuncLiteral(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_, _ = lib.Save()`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "encoding/json"

type Payload struct {
	Name string `+"`json:\"name\"`"+`
}

func Encode[T any](value T) ([]byte, error) {
	sink(func() {
		_, _ = json.Marshal(value)
	})

	return nil, nil
}

func sink(func()) {}

func Save() ([]byte, error) {
	return Encode(Payload{})
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		wantRemoveFieldPayloadName,
	) {
		t.Fatalf("expected dead func literal generic marshal to leave field dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeIgnoresYAMLMarshalNonRepresentationReturns(t *testing.T) {
	tmp := newYAMLTestModule(t)
	writeYAMLMarshalStub(t, tmp)
	writeTestMain(t, tmp, `	_, _ = lib.Save()`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "gopkg.in/yaml.v3"

type Payload struct {
	Name string `+"`yaml:\"name\"`"+`
}

type payloadError Payload

func (payloadError) Error() string {
	return "payload"
}

func (payload Payload) MarshalYAML() (any, error) {
	_ = func() any {
		type raw Payload

		return raw(payload)
	}

	return nil, payloadError(payload)
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
		t.Fatalf("expected YAML non-representation returns to leave field dead, got:\n%s", joined)
	}
}
