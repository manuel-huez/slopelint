package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRepoDeadCodeKeepsHooksThroughLocalGoccyJSONWrapper(t *testing.T) {
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

type Decoder struct{}

func Marshal(any) ([]byte, error) { return nil, nil }
func Unmarshal([]byte, any) error { return nil }
func NewDecoder(any) *Decoder { return nil }
func (*Decoder) Decode(any) error { return nil }
`)
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_ = lib.Load(nil)
	_ = lib.LoadMany(nil)
	_, _ = lib.Save()
}
`)
	writeFile(t, filepath.Join(tmp, "jsonio", "jsonio.go"), `package jsonio

import (
	"io"

	goccyjson "github.com/goccy/go-json"
)

type Decoder = goccyjson.Decoder

func Marshal(value any) ([]byte, error) {
	return goccyjson.Marshal(value)
}

func Unmarshal(data []byte, value any) error {
	return goccyjson.Unmarshal(data, value)
}

func NewDecoder(reader io.Reader) *Decoder {
	return goccyjson.NewDecoder(reader)
}

func ForEachArray[T any](reader io.Reader, emit func(T) error) error {
	visit := func(decoder *Decoder) error {
		var item T
		if err := decoder.Decode(&item); err != nil {
			return err
		}

		return emit(item)
	}

	err := readArray(visit)
	visit = func(*Decoder) error {
		return nil
	}

	return err
}

func readArray(visit func(*Decoder) error) error {
	return visit(NewDecoder(nil))
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import (
	"io"

	"example.com/sample/jsonio"
)

type Kind uint8

func (kind Kind) MarshalJSON() ([]byte, error) {
	return jsonio.Marshal(kindText(kind))
}

func (kind *Kind) UnmarshalJSON(body []byte) error {
	return parseKind(body)
}

type Scope map[string][]string

func (scope Scope) MarshalJSON() ([]byte, error) {
	return jsonio.Marshal(map[string]string{})
}

func (scope *Scope) UnmarshalJSON(body []byte) error {
	return parseScope(body)
}

type Payload struct {
	Kind    Kind   `+"`json:\"kind\"`"+`
	Scope   Scope  `+"`json:\"scope\"`"+`
	Ignored string `+"`json:\"-\"`"+`
}

type StreamTime struct{}

func (time *StreamTime) UnmarshalJSON(body []byte) error {
	return parseStreamTime(body)
}

type StreamItem struct {
	At StreamTime `+"`json:\"at\"`"+`
}

func Save() ([]byte, error) {
	return jsonio.Marshal(Payload{})
}

func Load(body []byte) error {
	var payload Payload

	return jsonio.Unmarshal(body, &payload)
}

func LoadMany(reader io.Reader) error {
	return jsonio.ForEachArray(reader, func(item StreamItem) error {
		return nil
	})
}

func kindText(Kind) string {
	return "request"
}

func parseKind([]byte) error {
	return nil
}

func parseScope([]byte) error {
	return nil
}

func parseStreamTime([]byte) error {
	return nil
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	for _, unexpected := range []string{
		`field "Payload.Kind"`,
		`field "Payload.Scope"`,
		`method "MarshalJSON"`,
		`method "UnmarshalJSON"`,
		`function "kindText"`,
		`function "parseKind"`,
		`function "parseScope"`,
		`function "parseStreamTime"`,
		`field "StreamItem.At"`,
	} {
		if strings.Contains(joined, unexpected) {
			t.Fatalf(
				"local goccy JSON wrapper dependency reported dead for %q, got:\n%s",
				unexpected,
				joined,
			)
		}
	}

	if !strings.Contains(
		joined,
		`exported field "Payload.Ignored" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf("expected ignored JSON field finding, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsGoccyJSONOptionAndContextCodecUses(t *testing.T) {
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

import "context"

type DecodeOptionFunc func()
type EncodeOptionFunc func()

func MarshalContext(context.Context, any, ...EncodeOptionFunc) ([]byte, error) { return nil, nil }
func MarshalIndentWithOption(any, string, string, ...EncodeOptionFunc) ([]byte, error) {
	return nil, nil
}
func MarshalNoEscape(any) ([]byte, error) { return nil, nil }
func MarshalWithOption(any, ...EncodeOptionFunc) ([]byte, error) { return nil, nil }
func UnmarshalContext(context.Context, []byte, any, ...DecodeOptionFunc) error { return nil }
func UnmarshalNoEscape([]byte, any, ...DecodeOptionFunc) error { return nil }
func UnmarshalWithOption([]byte, any, ...DecodeOptionFunc) error { return nil }
`)
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import (
	"context"

	"example.com/sample/lib"
)

func main() {
	ctx := context.Background()

	_ = lib.LoadContext(ctx, nil)
	_ = lib.LoadNoEscape(nil)
	_ = lib.LoadWithOption(nil)
	_, _ = lib.SaveContext(ctx)
	_, _ = lib.SaveIndentWithOption()
	_, _ = lib.SaveNoEscape()
	_, _ = lib.SaveWithOption()
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import (
	"context"

	goccyjson "github.com/goccy/go-json"
)

type Custom string

func (custom Custom) MarshalJSON(ctx context.Context) ([]byte, error) {
	return goccyjson.MarshalWithOption(customText(custom))
}

func (custom *Custom) UnmarshalJSON(ctx context.Context, body []byte) error {
	return parseCustom(body)
}

type Payload struct {
	Name    string `+"`json:\"name\"`"+`
	Custom  Custom `+"`json:\"custom\"`"+`
	Ignored string `+"`json:\"-\"`"+`
}

func SaveContext(ctx context.Context) ([]byte, error) {
	return goccyjson.MarshalContext(ctx, Payload{})
}

func SaveIndentWithOption() ([]byte, error) {
	return goccyjson.MarshalIndentWithOption(Payload{}, "", "  ")
}

func SaveNoEscape() ([]byte, error) {
	return goccyjson.MarshalNoEscape(Payload{})
}

func SaveWithOption() ([]byte, error) {
	return goccyjson.MarshalWithOption(Payload{})
}

func LoadContext(ctx context.Context, body []byte) error {
	var payload Payload

	return goccyjson.UnmarshalContext(ctx, body, &payload)
}

func LoadNoEscape(body []byte) error {
	var payload Payload

	return goccyjson.UnmarshalNoEscape(body, &payload)
}

func LoadWithOption(body []byte) error {
	var payload Payload

	return goccyjson.UnmarshalWithOption(body, &payload)
}

func customText(Custom) string {
	return "ok"
}

func parseCustom([]byte) error {
	return nil
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	for _, unexpected := range []string{
		`field "Payload.Name"`,
		`field "Payload.Custom"`,
		`method "MarshalJSON"`,
		`method "UnmarshalJSON"`,
		`function "customText"`,
		`function "parseCustom"`,
	} {
		if strings.Contains(joined, unexpected) {
			t.Fatalf(
				"goccy JSON option/context dependency reported dead for %q, got:\n%s",
				unexpected,
				joined,
			)
		}
	}

	if !strings.Contains(
		joined,
		`exported field "Payload.Ignored" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf("expected ignored JSON field finding, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsGoccyJSONStreamOptionAndContextCodecUses(t *testing.T) {
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

import "context"

type DecodeOptionFunc func()
type EncodeOptionFunc func()
type Decoder struct{}
type Encoder struct{}

func NewDecoder(any) *Decoder { return nil }
func (*Decoder) DecodeContext(context.Context, any) error { return nil }
func (*Decoder) DecodeWithOption(any, ...DecodeOptionFunc) error { return nil }
func NewEncoder(any) *Encoder { return nil }
func (*Encoder) EncodeContext(context.Context, any, ...EncodeOptionFunc) error { return nil }
func (*Encoder) EncodeWithOption(any, ...EncodeOptionFunc) error { return nil }
`)
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import (
	"context"

	"example.com/sample/lib"
)

func main() {
	ctx := context.Background()

	_ = lib.LoadDecodeContext(ctx)
	_ = lib.LoadDecodeWithOption()
	_ = lib.SaveEncodeContext(ctx)
	_ = lib.SaveEncodeWithOption()
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import (
	"context"

	goccyjson "github.com/goccy/go-json"
)

type Plain string

func (plain Plain) MarshalJSON() ([]byte, error) {
	return []byte(plainText(plain)), nil
}

func (plain *Plain) UnmarshalJSON(body []byte) error {
	return parsePlain(body)
}

type Contextual string

func (contextual Contextual) MarshalJSON(ctx context.Context) ([]byte, error) {
	return []byte(contextText(ctx, contextual)), nil
}

func (contextual *Contextual) UnmarshalJSON(ctx context.Context, body []byte) error {
	return parseContext(ctx, body)
}

type PlainPayload struct {
	Value Plain `+"`json:\"value\"`"+`
}

type ContextPayload struct {
	Value Contextual `+"`json:\"value\"`"+`
}

func SaveEncodeWithOption() error {
	return goccyjson.NewEncoder(nil).EncodeWithOption(PlainPayload{})
}

func LoadDecodeWithOption() error {
	var payload PlainPayload

	return goccyjson.NewDecoder(nil).DecodeWithOption(&payload)
}

func SaveEncodeContext(ctx context.Context) error {
	return goccyjson.NewEncoder(nil).EncodeContext(ctx, ContextPayload{})
}

func LoadDecodeContext(ctx context.Context) error {
	var payload ContextPayload

	return goccyjson.NewDecoder(nil).DecodeContext(ctx, &payload)
}

func plainText(Plain) string {
	return "plain"
}

func parsePlain([]byte) error {
	return nil
}

func contextText(context.Context, Contextual) string {
	return "context"
}

func parseContext(context.Context, []byte) error {
	return nil
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	for _, unexpected := range []string{
		`field "PlainPayload.Value"`,
		`field "ContextPayload.Value"`,
		`method "MarshalJSON"`,
		`method "UnmarshalJSON"`,
		`function "plainText"`,
		`function "parsePlain"`,
		`function "contextText"`,
		`function "parseContext"`,
	} {
		if strings.Contains(joined, unexpected) {
			t.Fatalf(
				"goccy JSON stream dependency reported dead for %q, got:\n%s",
				unexpected,
				joined,
			)
		}
	}
}

func TestRepoDeadCodeDoesNotKeepGoccyJSONContextHooksThroughPlainCodecUses(t *testing.T) {
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

type DecodeOptionFunc func()
type EncodeOptionFunc func()
type Decoder struct{}
type Encoder struct{}

func Marshal(any) ([]byte, error) { return nil, nil }
func MarshalIndentWithOption(any, string, string, ...EncodeOptionFunc) ([]byte, error) {
	return nil, nil
}
func MarshalNoEscape(any) ([]byte, error) { return nil, nil }
func MarshalWithOption(any, ...EncodeOptionFunc) ([]byte, error) { return nil, nil }
func NewEncoder(any) *Encoder { return nil }
func (*Encoder) Encode(any) error { return nil }
func (*Encoder) EncodeWithOption(any, ...EncodeOptionFunc) error { return nil }
func Unmarshal([]byte, any) error { return nil }
func UnmarshalNoEscape([]byte, any, ...DecodeOptionFunc) error { return nil }
func UnmarshalWithOption([]byte, any, ...DecodeOptionFunc) error { return nil }
func NewDecoder(any) *Decoder { return nil }
func (*Decoder) Decode(any) error { return nil }
func (*Decoder) DecodeWithOption(any, ...DecodeOptionFunc) error { return nil }
`)
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_ = lib.LoadDecode()
	_ = lib.LoadDecodeWithOption()
	_ = lib.LoadNoEscape(nil)
	_ = lib.LoadPlain(nil)
	_ = lib.LoadWithOption(nil)
	_ = lib.SaveEncode()
	_ = lib.SaveEncodeWithOption()
	_, _ = lib.SaveIndentWithOption()
	_, _ = lib.SaveNoEscape()
	_, _ = lib.SavePlain()
	_, _ = lib.SaveWithOption()
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import (
	"context"

	goccyjson "github.com/goccy/go-json"
)

type Custom string

func (custom Custom) MarshalJSON(ctx context.Context) ([]byte, error) {
	return []byte(customText(ctx, custom)), nil
}

func (custom *Custom) UnmarshalJSON(ctx context.Context, body []byte) error {
	return parseCustom(ctx, body)
}

type Payload struct {
	Name   string `+"`json:\"name\"`"+`
	Custom Custom `+"`json:\"custom\"`"+`
}

func SavePlain() ([]byte, error) {
	return goccyjson.Marshal(Payload{})
}

func SaveIndentWithOption() ([]byte, error) {
	return goccyjson.MarshalIndentWithOption(Payload{}, "", "  ")
}

func SaveNoEscape() ([]byte, error) {
	return goccyjson.MarshalNoEscape(Payload{})
}

func SaveWithOption() ([]byte, error) {
	return goccyjson.MarshalWithOption(Payload{})
}

func SaveEncode() error {
	return goccyjson.NewEncoder(nil).Encode(Payload{})
}

func SaveEncodeWithOption() error {
	return goccyjson.NewEncoder(nil).EncodeWithOption(Payload{})
}

func LoadPlain(body []byte) error {
	var payload Payload

	return goccyjson.Unmarshal(body, &payload)
}

func LoadNoEscape(body []byte) error {
	var payload Payload

	return goccyjson.UnmarshalNoEscape(body, &payload)
}

func LoadWithOption(body []byte) error {
	var payload Payload

	return goccyjson.UnmarshalWithOption(body, &payload)
}

func LoadDecode() error {
	var payload Payload

	return goccyjson.NewDecoder(nil).Decode(&payload)
}

func LoadDecodeWithOption() error {
	var payload Payload

	return goccyjson.NewDecoder(nil).DecodeWithOption(&payload)
}

func customText(context.Context, Custom) string {
	return "custom"
}

func parseCustom(context.Context, []byte) error {
	return nil
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	for _, unexpected := range []string{
		`field "Payload.Name"`,
		`field "Payload.Custom"`,
	} {
		if strings.Contains(joined, unexpected) {
			t.Fatalf(
				"plain goccy JSON codec field reported dead for %q, got:\n%s",
				unexpected,
				joined,
			)
		}
	}

	for _, expected := range []string{
		`method "MarshalJSON" is unreachable from repo entrypoints; remove it`,
		`method "UnmarshalJSON" is unreachable from repo entrypoints; remove it`,
		`function "customText" is never used by production code; remove it`,
		`function "parseCustom" is never used by production code; remove it`,
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf(
				"expected plain goccy JSON context hook finding %q, got:\n%s",
				expected,
				joined,
			)
		}
	}
}

func TestRepoDeadCodeDoesNotKeepStdlibJSONContextHooks(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_ = lib.Load(nil)
	_, _ = lib.Save()
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import (
	"context"
	"encoding/json"
)

type Custom string

func (custom Custom) MarshalJSON(ctx context.Context) ([]byte, error) {
	return json.Marshal(customText(custom))
}

func (custom *Custom) UnmarshalJSON(ctx context.Context, body []byte) error {
	return parseCustom(body)
}

type Payload struct {
	Custom Custom `+"`json:\"custom\"`"+`
}

func Save() ([]byte, error) {
	return json.Marshal(Payload{})
}

func Load(body []byte) error {
	var payload Payload

	return json.Unmarshal(body, &payload)
}

func customText(Custom) string {
	return "ok"
}

func parseCustom([]byte) error {
	return nil
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	for _, expected := range []string{
		`method "MarshalJSON" is unreachable from repo entrypoints; remove it`,
		`method "UnmarshalJSON" is unreachable from repo entrypoints; remove it`,
		`function "customText" is never used by production code; remove it`,
		`function "parseCustom" is never used by production code; remove it`,
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("expected stdlib JSON context hook finding %q, got:\n%s", expected, joined)
		}
	}
}

func TestRepoDeadCodeDoesNotKeepLocalWrapperShadowedParamFields(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_, _ = lib.Save()
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "encoding/json"

type Payload struct {
	Name string `+"`json:\"name\"`"+`
}

func Encode(value any) ([]byte, error) {
	{
		value := struct {
			Other string `+"`json:\"other\"`"+`
		}{}

		return json.Marshal(value)
	}
}

func Save() ([]byte, error) {
	return Encode(Payload{})
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`field "Payload.Name" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf("expected shadowed wrapper param to leave field dead, got:\n%s", joined)
	}
}
