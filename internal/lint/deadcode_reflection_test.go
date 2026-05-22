package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRepoDeadCodeKeepsFieldsSetByJSONDecoder(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_ = lib.Load(nil)
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import (
	"encoding/json"
	"io"
)

	type Payload struct {
		Meta    Meta   `+"`json:\"meta\"`"+`
		Rows    []Row  `+"`json:\"rows\"`"+`
		Dash    string `+"`json:\"-,\"`"+`
		Ignored string `+"`json:\"-\"`"+`
	}

type Meta struct {
	Name  string `+"`json:\"name\"`"+`
	Total int    `+"`json:\"total\"`"+`
}

type Row struct {
	Value string `+"`json:\"value\"`"+`
}

func Load(reader io.Reader) error {
	var payload Payload

	return json.NewDecoder(reader).Decode(&payload)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	for _, unexpected := range []string{
		`field "Payload.Meta"`,
		`field "Payload.Rows"`,
		`field "Meta.Name"`,
		`field "Meta.Total"`,
		`field "Row.Value"`,
		`field "Payload.Dash"`,
	} {
		if strings.Contains(joined, unexpected) {
			t.Fatalf(
				"JSON-decoded field reported dead for %q, got:\n%s",
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

func TestRepoDeadCodeUsesXMLIgnoreTagsForXMLDecoder(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_ = lib.Load(nil)
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "encoding/xml"

type Payload struct {
	Keep string `+"`xml:\"keep\" json:\"-\"`"+`
	Skip string `+"`xml:\"-\"`"+`
}

func Load(body []byte) error {
	var payload Payload

	return xml.Unmarshal(body, &payload)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `field "Payload.Keep"`) {
		t.Fatalf("XML-decoded field with json ignore tag reported dead, got:\n%s", joined)
	}

	if !strings.Contains(
		joined,
		`exported field "Payload.Skip" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf("expected XML-ignored field finding, got:\n%s", joined)
	}
}

func TestRepoDeadCodeUsesYAMLIgnoreTagsForYAMLDecoder(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), `module example.com/sample

go 1.22

require gopkg.in/yaml.v3 v3.0.0

replace gopkg.in/yaml.v3 => ./yaml
`)
	writeFile(t, filepath.Join(tmp, "yaml", "go.mod"), "module gopkg.in/yaml.v3\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "yaml", "yaml.go"), `package yaml

func Unmarshal([]byte, any) error { return nil }
`)
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_ = lib.Load(nil)
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "gopkg.in/yaml.v3"

type Payload struct {
	Keep string `+"`yaml:\"keep\" json:\"-\"`"+`
	Skip string `+"`yaml:\"-\"`"+`
}

func Load(body []byte) error {
	var payload Payload

	return yaml.Unmarshal(body, &payload)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `field "Payload.Keep"`) {
		t.Fatalf("YAML-decoded field with json ignore tag reported dead, got:\n%s", joined)
	}

	if !strings.Contains(
		joined,
		`exported field "Payload.Skip" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf("expected YAML-ignored field finding, got:\n%s", joined)
	}
}

func TestRepoDeadCodeReportsFieldsPassedToLocalDecode(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	lib.Run()
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

type Payload struct {
	Live string
	Dead string
}

func Decode(*Payload) error {
	return nil
}

func Run() {
	var payload Payload
	_ = Decode(&payload)
	_ = payload.Live
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `field "Payload.Live"`) {
		t.Fatalf("read local-decode field reported dead, got:\n%s", joined)
	}

	if !strings.Contains(
		joined,
		`exported field "Payload.Dead" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf("expected field passed only to local Decode finding, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsFieldsSetByGenericJSONDecoder(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_ = lib.Live(nil)
}
`)
	writeFile(t, filepath.Join(tmp, "codec", "codec.go"), `package codec

import "encoding/json"

func ForEachJSON[T any](body []byte, emit func(T) error) error {
	var item T
	if err := json.Unmarshal(body, &item); err != nil {
		return err
	}

	return emit(item)
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "example.com/sample/codec"

type Result struct {
	ModelID         string         `+"`json:\"model_id\"`"+`
	HeadlineMetrics map[string]any `+"`json:\"headline_metrics\"`"+`
	Ignored         string         `+"`json:\"-\"`"+`
}

func Live(body []byte) error {
	return codec.ForEachJSON(body, func(result Result) error {
		_ = result.ModelID

		return nil
	})
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `field "Result.HeadlineMetrics"`) {
		t.Fatalf("generic JSON-decoded field reported dead, got:\n%s", joined)
	}

	if !strings.Contains(
		joined,
		`exported field "Result.Ignored" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf("expected ignored generic JSON field finding, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsFieldsSetByGenericReceiverJSONDecoder(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_ = lib.Live(nil)
}
`)
	writeFile(t, filepath.Join(tmp, "codec", "codec.go"), `package codec

import "encoding/json"

type Decoder[T any] struct{}

func (Decoder[T]) Decode(body []byte) (T, error) {
	var item T
	err := json.Unmarshal(body, &item)

	return item, err
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "example.com/sample/codec"

type Payload struct {
	FromJSON string `+"`json:\"from_json\"`"+`
	Ignored  string `+"`json:\"-\"`"+`
}

func Live(body []byte) error {
	_, err := codec.Decoder[Payload]{}.Decode(body)

	return err
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `field "Payload.FromJSON"`) {
		t.Fatalf("generic receiver JSON-decoded field reported dead, got:\n%s", joined)
	}

	if !strings.Contains(
		joined,
		`exported field "Payload.Ignored" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf("expected ignored generic receiver JSON field finding, got:\n%s", joined)
	}
}

func TestRepoDeadCodeInspectsGenericDecoderCalledFromRootPackage(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/codec"

type Payload struct {
	FromJSON string `+"`json:\"from_json\"`"+`
	Ignored  string `+"`json:\"-\"`"+`
}

func main() {
	_ = codec.Each[Payload](nil, func(Payload) error {
		return nil
	})
}
`)
	writeFile(t, filepath.Join(tmp, "codec", "codec.go"), `package codec

import "encoding/json"

func Each[T any](body []byte, emit func(T) error) error {
	var item T
	if err := json.Unmarshal(body, &item); err != nil {
		return err
	}

	return emit(item)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `field "Payload.FromJSON"`) {
		t.Fatalf("root generic JSON-decoded field reported dead, got:\n%s", joined)
	}

	if !strings.Contains(
		joined,
		`exported field "Payload.Ignored" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf("expected ignored root generic JSON field finding, got:\n%s", joined)
	}
}

func TestRepoDeadCodeReportsFieldsPassedAsNonPointerGenericDecodeTarget(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_ = lib.Live(nil)
}
`)
	writeFile(t, filepath.Join(tmp, "codec", "codec.go"), `package codec

import "encoding/json"

func DecodeJSON[T any](body []byte, target T) error {
	return json.Unmarshal(body, target)
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "example.com/sample/codec"

type Payload struct {
	Live string
	Dead string
}

func Live(body []byte) error {
	var payload Payload
	_ = payload.Live

	return codec.DecodeJSON(body, payload)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `field "Payload.Live"`) {
		t.Fatalf("read field reported dead, got:\n%s", joined)
	}

	if !strings.Contains(
		joined,
		`exported field "Payload.Dead" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf("expected non-pointer generic decode target finding, got:\n%s", joined)
	}
}

func TestRepoDeadCodeReportsFieldsPassedToExternalGenericJSONNonDecoder(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), `module example.com/sample

go 1.22

require example.com/jsonutil v0.0.0

replace example.com/jsonutil => ./jsonutil
`)
	writeFile(
		t,
		filepath.Join(tmp, "jsonutil", "go.mod"),
		"module example.com/jsonutil\n\ngo 1.22\n",
	)
	writeFile(t, filepath.Join(tmp, "jsonutil", "jsonutil.go"), `package jsonutil

func JSONIdentity[T any](value T) T {
	return value
}
`)
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	lib.Live()
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "example.com/jsonutil"

type Payload struct {
	Live string
	Dead string
}

func Live() {
	var payload Payload
	payload = jsonutil.JSONIdentity(payload)
	_ = payload.Live
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `field "Payload.Live"`) {
		t.Fatalf("read external generic non-decoder field reported dead, got:\n%s", joined)
	}

	if !strings.Contains(
		joined,
		`exported field "Payload.Dead" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf("expected external generic non-decoder field finding, got:\n%s", joined)
	}
}

func TestRepoDeadCodeReportsFieldsReturnedByExternalGenericDecodeID(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), `module example.com/sample

go 1.22

require example.com/codec v0.0.0

replace example.com/codec => ./codec
`)
	writeFile(t, filepath.Join(tmp, "codec", "go.mod"), "module example.com/codec\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "codec", "codec.go"), `package codec

func DecodeID[T any](id string) T {
	var zero T

	return zero
}
`)
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	lib.Live()
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "example.com/codec"

type Payload struct {
	Live string
	Dead string
}

func Live() {
	payload := codec.DecodeID[Payload]("id")
	_ = payload.Live
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `field "Payload.Live"`) {
		t.Fatalf("read external generic DecodeID field reported dead, got:\n%s", joined)
	}

	if !strings.Contains(
		joined,
		`exported field "Payload.Dead" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf("expected external generic DecodeID field finding, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsFieldsReturnedByExternalGenericDecodeJSON(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), `module example.com/sample

go 1.22

require example.com/jsoncodec v0.0.0

replace example.com/jsoncodec => ./jsoncodec
`)
	writeFile(
		t,
		filepath.Join(tmp, "jsoncodec", "go.mod"),
		"module example.com/jsoncodec\n\ngo 1.22\n",
	)
	writeFile(t, filepath.Join(tmp, "jsoncodec", "jsoncodec.go"), `package jsoncodec

import "encoding/json"

func DecodeJSON[T any](body []byte) (T, error) {
	var zero T
	err := json.Unmarshal(body, &zero)

	return zero, err
}

func DecodeIntoJSON[T any](body []byte, out *T) error {
	return json.Unmarshal(body, any(out))
}
`)
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_ = lib.Live(nil)
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "example.com/jsoncodec"

type Payload struct {
	FromJSON string `+"`json:\"from_json\"`"+`
	Ignored  string `+"`json:\"-\"`"+`
}

type IntoPayload struct {
	FromJSON string `+"`json:\"from_json\"`"+`
}

func Live(body []byte) error {
	payload, err := jsoncodec.DecodeJSON[Payload](body)
	if err != nil {
		return err
	}

	var into IntoPayload
	if err := jsoncodec.DecodeIntoJSON(body, &into); err != nil {
		return err
	}

	_, _ = payload, into

	return nil
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `field "Payload.FromJSON"`) {
		t.Fatalf("external generic DecodeJSON result field reported dead, got:\n%s", joined)
	}

	if strings.Contains(joined, `field "IntoPayload.FromJSON"`) {
		t.Fatalf("external generic DecodeIntoJSON field reported dead, got:\n%s", joined)
	}

	if !strings.Contains(
		joined,
		`exported field "Payload.Ignored" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf("expected ignored external generic DecodeJSON field finding, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsExternalGoccyJSONGenericDecodeContextHooks(t *testing.T) {
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
	writeFile(
		t,
		filepath.Join(tmp, "gojson", "go.mod"),
		"module github.com/goccy/go-json\n\ngo 1.22\n",
	)
	writeFile(t, filepath.Join(tmp, "gojson", "json.go"), `package json

import "context"

type DecodeOptionFunc func()

func UnmarshalContext(context.Context, []byte, any, ...DecodeOptionFunc) error { return nil }
`)
	writeFile(t, filepath.Join(tmp, "jsoncodec", "jsoncodec.go"), `package jsoncodec

import (
	"context"

	goccyjson "github.com/goccy/go-json"
)

func DecodeContext[T any](ctx context.Context, body []byte) (*T, error) {
	value := new(T)
	err := goccyjson.UnmarshalContext(ctx, body, any(value))

	return value, err
}
`)
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import (
	"context"

	"example.com/sample/lib"
)

func main() {
	_ = lib.Load(context.Background(), nil)
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import (
	"context"

	"example.com/jsoncodec"
)

type Custom string

func (custom *Custom) UnmarshalJSON(ctx context.Context, body []byte) error {
	return parseCustom(ctx, body)
}

type Payload struct {
	Custom Custom `+"`json:\"custom\"`"+`
	Ignored string `+"`json:\"-\"`"+`
}

func Load(ctx context.Context, body []byte) error {
	payload, err := jsoncodec.DecodeContext[Payload](ctx, body)
	if err != nil {
		return err
	}

	_ = payload

	return nil
}

func parseCustom(context.Context, []byte) error {
	return nil
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	for _, unexpected := range []string{
		`field "Payload.Custom"`,
		`method "UnmarshalJSON"`,
		`function "parseCustom"`,
	} {
		if strings.Contains(joined, unexpected) {
			t.Fatalf(
				"external goccy JSON generic context decode dependency reported dead for %q, got:\n%s",
				unexpected,
				joined,
			)
		}
	}

	if !strings.Contains(
		joined,
		`exported field "Payload.Ignored" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf(
			"expected ignored external goccy JSON generic decode field finding, got:\n%s",
			joined,
		)
	}
}

func TestRepoDeadCodeKeepsFieldsSetByDelegatedGenericJSONDecoder(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_ = lib.Live(nil)
}
`)
	writeFile(t, filepath.Join(tmp, "codec", "codec.go"), `package codec

import "encoding/json"

func decodeJSON[T any](body []byte) (T, error) {
	var item T
	err := json.Unmarshal(body, &item)

	return item, err
}

func ForEachJSON[T any](body []byte, emit func(T) error) error {
	item, err := decodeJSON[T](body)
	if err != nil {
		return err
	}

	return emit(item)
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "example.com/sample/codec"

type Result struct {
	ModelID         string         `+"`json:\"model_id\"`"+`
	HeadlineMetrics map[string]any `+"`json:\"headline_metrics\"`"+`
	Ignored         string         `+"`json:\"-\"`"+`
}

func Live(body []byte) error {
	return codec.ForEachJSON(body, func(result Result) error {
		_ = result.ModelID

		return nil
	})
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `field "Result.HeadlineMetrics"`) {
		t.Fatalf("delegated generic JSON-decoded field reported dead, got:\n%s", joined)
	}

	if !strings.Contains(
		joined,
		`exported field "Result.Ignored" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf("expected ignored delegated generic JSON field finding, got:\n%s", joined)
	}
}

func TestRepoDeadCodeStopsAtCustomJSONUnmarshalHook(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_ = lib.Load(nil)
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "encoding/json"

type Payload struct {
	Inner Inner `+"`json:\"inner\"`"+`
}

type Inner struct {
	Used   string `+"`json:\"used\"`"+`
	Hidden string `+"`json:\"hidden\"`"+`
}

func (inner *Inner) UnmarshalJSON([]byte) error {
	inner.Used = "decoded"

	return nil
}

func Load(body []byte) error {
	var payload Payload

	return json.Unmarshal(body, &payload)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	for _, unexpected := range []string{
		`field "Payload.Inner"`,
		`field "Inner.Used"`,
		`method "UnmarshalJSON"`,
	} {
		if strings.Contains(joined, unexpected) {
			t.Fatalf(
				"custom JSON unmarshal dependency reported dead for %q, got:\n%s",
				unexpected,
				joined,
			)
		}
	}

	if !strings.Contains(
		joined,
		`exported field "Inner.Hidden" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf("expected custom JSON unmarshal hidden field finding, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsFieldsDecodedThroughCustomJSONUnmarshalAlias(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_ = lib.Load(nil)
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "encoding/json"

type Payload struct {
	Name    string `+"`json:\"name\"`"+`
	Ignored string `+"`json:\"-\"`"+`
}

func (payload *Payload) UnmarshalJSON(body []byte) error {
	type raw Payload

	var decoded raw
	if err := json.Unmarshal(body, &decoded); err != nil {
		return err
	}

	*payload = Payload(decoded)

	return nil
}

func Load(body []byte) error {
	var payload Payload

	return json.Unmarshal(body, &payload)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	for _, unexpected := range []string{
		`field "Payload.Name"`,
		`method "UnmarshalJSON"`,
	} {
		if strings.Contains(joined, unexpected) {
			t.Fatalf(
				"custom JSON alias dependency reported dead for %q, got:\n%s",
				unexpected,
				joined,
			)
		}
	}

	if !strings.Contains(
		joined,
		`exported field "Payload.Ignored" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf("expected ignored custom JSON alias field finding, got:\n%s", joined)
	}
}

func TestRepoDeadCodeReportsOriginalFieldsWhenWireAliasIsDecoded(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_ = lib.Load(nil)
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "encoding/json"

type Payload struct {
	Live string
	Dead string
}

type Wire Payload

func NewPayload() Payload {
	return Payload{Live: "ok"}
}

func Load(body []byte) error {
	var wire Wire
	if err := json.Unmarshal(body, &wire); err != nil {
		return err
	}

	_ = wire.Live
	_ = NewPayload()

	return nil
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `field "Payload.Live"`) {
		t.Fatalf("used original field reported dead, got:\n%s", joined)
	}

	if !strings.Contains(
		joined,
		`exported field "Payload.Dead" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf("expected original field finding despite decoded wire alias, got:\n%s", joined)
	}
}

func TestRepoDeadCodeReportsGenericJSONTypeArgsWhenBodyDoesNotDecode(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_ = lib.Live()
}
`)
	writeFile(t, filepath.Join(tmp, "codec", "codec.go"), `package codec

func ForEachJSON[T any](emit func(T) error) error {
	var item T

	return emit(item)
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "example.com/sample/codec"

type Result struct {
	ModelID         string
	HeadlineMetrics map[string]any
}

func Live() error {
	return codec.ForEachJSON(func(result Result) error {
		_ = result.ModelID

		return nil
	})
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `field "Result.ModelID"`) {
		t.Fatalf("read generic JSON field reported dead, got:\n%s", joined)
	}

	if !strings.Contains(
		joined,
		`exported field "Result.HeadlineMetrics" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf("expected non-decoded generic JSON field finding, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsReflectedMarshalAndUnmarshalMethods(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), `module example.com/sample

go 1.22

require gopkg.in/yaml.v3 v3.0.0

replace gopkg.in/yaml.v3 => ./yaml
`)
	writeFile(t, filepath.Join(tmp, "yaml", "go.mod"), "module gopkg.in/yaml.v3\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "yaml", "yaml.go"), `package yaml

type Node struct{}

func Marshal(any) ([]byte, error) { return nil, nil }
func Unmarshal([]byte, any) error { return nil }
func (*Node) Decode(any) error { return nil }
`)
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	value := lib.Scalar("x")
	_, _ = lib.Save(value)
	_, _ = lib.Load(nil)
}
`)
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
		`method "MarshalYAML"`,
		`method "MarshalText"`,
		`method "UnmarshalYAML"`,
		`method "UnmarshalText"`,
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
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), `module example.com/sample

go 1.22

require gopkg.in/yaml.v3 v3.0.0

replace gopkg.in/yaml.v3 => ./yaml
`)
	writeFile(t, filepath.Join(tmp, "yaml", "go.mod"), "module gopkg.in/yaml.v3\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "yaml", "yaml.go"), `package yaml

func Marshal(any) ([]byte, error) { return nil, nil }
`)
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_, _ = lib.Save()
}
`)
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
		`field "Payload.Name"`,
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
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), `module example.com/sample

go 1.22

require gopkg.in/yaml.v3 v3.0.0

replace gopkg.in/yaml.v3 => ./yaml
`)
	writeFile(t, filepath.Join(tmp, "yaml", "go.mod"), "module gopkg.in/yaml.v3\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "yaml", "yaml.go"), `package yaml

func Marshal(any) ([]byte, error) { return nil, nil }
`)
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_, _ = lib.Save()
}
`)
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
		`field "Payload.Name"`,
		`method "MarshalYAML"`,
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
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), `module example.com/sample

go 1.22

require gopkg.in/yaml.v3 v3.0.0

replace gopkg.in/yaml.v3 => ./yaml
`)
	writeFile(t, filepath.Join(tmp, "yaml", "go.mod"), "module gopkg.in/yaml.v3\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "yaml", "yaml.go"), `package yaml

func Marshal(any) ([]byte, error) { return nil, nil }
`)
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_, _ = lib.Save()
}
`)
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

	if strings.Contains(joined, `field "Payload.Name"`) {
		t.Fatalf("YAML map alias return field reported dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsCrossPackageYAMLMarshalAliasReturnFields(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), `module example.com/sample

go 1.22

require gopkg.in/yaml.v3 v3.0.0

replace gopkg.in/yaml.v3 => ./yaml
`)
	writeFile(t, filepath.Join(tmp, "yaml", "go.mod"), "module gopkg.in/yaml.v3\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "yaml", "yaml.go"), `package yaml

func Marshal(any) ([]byte, error) { return nil, nil }
`)
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_, _ = lib.Save()
}
`)
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
		`field "Payload.Name"`,
		`method "MarshalYAML"`,
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
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), `module example.com/sample

go 1.22

require gopkg.in/yaml.v3 v3.0.0

replace gopkg.in/yaml.v3 => ./yaml
`)
	writeFile(t, filepath.Join(tmp, "yaml", "go.mod"), "module gopkg.in/yaml.v3\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "yaml", "yaml.go"), `package yaml

func Marshal(any) ([]byte, error) { return nil, nil }
`)
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_, _ = lib.Save()
}
`)
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
		`field "Payload.Name"`,
		`method "MarshalYAML"`,
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
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), `module example.com/sample

go 1.22

require gopkg.in/yaml.v3 v3.0.0

replace gopkg.in/yaml.v3 => ./yaml
`)
	writeFile(t, filepath.Join(tmp, "yaml", "go.mod"), "module gopkg.in/yaml.v3\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "yaml", "yaml.go"), `package yaml

func Marshal(any) ([]byte, error) { return nil, nil }
`)
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_, _ = lib.Save()
}
`)
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
		`field "Payload.Name" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf(
			"expected stale YAML named result assignment to leave field dead, got:\n%s",
			joined,
		)
	}
}

func TestRepoDeadCodeKeepsYAMLMarshalNamedResultAliasAcrossBranch(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), `module example.com/sample

go 1.22

require gopkg.in/yaml.v3 v3.0.0

replace gopkg.in/yaml.v3 => ./yaml
`)
	writeFile(t, filepath.Join(tmp, "yaml", "go.mod"), "module gopkg.in/yaml.v3\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "yaml", "yaml.go"), `package yaml

func Marshal(any) ([]byte, error) { return nil, nil }
`)
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_, _ = lib.Save()
}
`)
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

	if strings.Contains(joined, `field "Payload.Name"`) {
		t.Fatalf("YAML branch named result alias field reported dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsYAMLMarshalExplicitNamedResultAliasFields(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), `module example.com/sample

go 1.22

require gopkg.in/yaml.v3 v3.0.0

replace gopkg.in/yaml.v3 => ./yaml
`)
	writeFile(t, filepath.Join(tmp, "yaml", "go.mod"), "module gopkg.in/yaml.v3\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "yaml", "yaml.go"), `package yaml

func Marshal(any) ([]byte, error) { return nil, nil }
`)
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_, _ = lib.Save()
}
`)
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

	if strings.Contains(joined, `field "Payload.Name"`) {
		t.Fatalf("YAML explicit named result alias field reported dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsYAMLMarshalLocalAliasReturnFields(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), `module example.com/sample

go 1.22

require gopkg.in/yaml.v3 v3.0.0

replace gopkg.in/yaml.v3 => ./yaml
`)
	writeFile(t, filepath.Join(tmp, "yaml", "go.mod"), "module gopkg.in/yaml.v3\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "yaml", "yaml.go"), `package yaml

func Marshal(any) ([]byte, error) { return nil, nil }
`)
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_, _ = lib.Save()
}
`)
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

	if strings.Contains(joined, `field "Payload.Name"`) {
		t.Fatalf("YAML local alias return field reported dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsYAMLMarshalAliasAcrossSwitch(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), `module example.com/sample

go 1.22

require gopkg.in/yaml.v3 v3.0.0

replace gopkg.in/yaml.v3 => ./yaml
`)
	writeFile(t, filepath.Join(tmp, "yaml", "go.mod"), "module gopkg.in/yaml.v3\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "yaml", "yaml.go"), `package yaml

func Marshal(any) ([]byte, error) { return nil, nil }
`)
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_, _ = lib.Save()
}
`)
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

	if strings.Contains(joined, `field "Payload.Name"`) {
		t.Fatalf("YAML switch alias return field reported dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeDoesNotKeepYAMLMarshalDefaultSwitchOverwriteFields(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), `module example.com/sample

go 1.22

require gopkg.in/yaml.v3 v3.0.0

replace gopkg.in/yaml.v3 => ./yaml
`)
	writeFile(t, filepath.Join(tmp, "yaml", "go.mod"), "module gopkg.in/yaml.v3\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "yaml", "yaml.go"), `package yaml

func Marshal(any) ([]byte, error) { return nil, nil }
`)
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_, _ = lib.Save()
}
`)
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
		`field "Payload.Name" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf("expected YAML default switch overwrite to leave field dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsYAMLMarshalAliasAcrossAppendedRange(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), `module example.com/sample

go 1.22

require gopkg.in/yaml.v3 v3.0.0

replace gopkg.in/yaml.v3 => ./yaml
`)
	writeFile(t, filepath.Join(tmp, "yaml", "go.mod"), "module gopkg.in/yaml.v3\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "yaml", "yaml.go"), `package yaml

func Marshal(any) ([]byte, error) { return nil, nil }
`)
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_, _ = lib.Save()
}
`)
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

	if strings.Contains(joined, `field "Payload.Name"`) {
		t.Fatalf("YAML range alias return field reported dead, got:\n%s", joined)
	}
}

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
	writeFile(
		t,
		filepath.Join(tmp, "gojson", "go.mod"),
		"module github.com/goccy/go-json\n\ngo 1.22\n",
	)
	writeFile(t, filepath.Join(tmp, "gojson", "json.go"), `package json

func Marshal(any) ([]byte, error) { return nil, nil }
`)
	writeFile(t, filepath.Join(tmp, "jsoncodec", "codec.go"), `package jsoncodec

import "github.com/goccy/go-json"

func Encode[T any](value T) ([]byte, error) {
	return json.Marshal(value)
}
`)
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_, _ = lib.Save()
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "example.com/jsoncodec"

type Payload struct {
	Name string `+"`json:\"name\"`"+`
}

func Save() ([]byte, error) {
	return jsoncodec.Encode(Payload{})
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `field "Payload.Name"`) {
		t.Fatalf(
			"external goccy JSON generic marshal wrapper field reported dead, got:\n%s",
			joined,
		)
	}
}

func TestRepoDeadCodeDoesNotKeepExternalGenericMarshalShadowedParamFields(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), `module example.com/sample

go 1.22

require example.com/jsoncodec v0.0.0

replace example.com/jsoncodec => ./jsoncodec
`)
	writeFile(
		t,
		filepath.Join(tmp, "jsoncodec", "go.mod"),
		"module example.com/jsoncodec\n\ngo 1.22\n",
	)
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
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_, _ = lib.Save()
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "example.com/jsoncodec"

type Payload struct {
	Name string `+"`json:\"name\"`"+`
}

func Save() ([]byte, error) {
	return jsoncodec.Encode(Payload{})
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`field "Payload.Name" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf(
			"expected shadowed external generic marshal param to leave field dead, got:\n%s",
			joined,
		)
	}
}

func TestRepoDeadCodeKeepsExternalGenericMarshalMethodWithSameNameFields(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), `module example.com/sample

go 1.22

require example.com/jsoncodec v0.0.0

replace example.com/jsoncodec => ./jsoncodec
`)
	writeFile(
		t,
		filepath.Join(tmp, "jsoncodec", "go.mod"),
		"module example.com/jsoncodec\n\ngo 1.22\n",
	)
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
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_, _ = lib.Save()
}
`)
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

	if strings.Contains(joined, `field "Payload.Name"`) {
		t.Fatalf("external generic marshal method field reported dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsDelegatedExternalGenericMarshalWrapperFields(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), `module example.com/sample

go 1.22

require example.com/jsoncodec v0.0.0

replace example.com/jsoncodec => ./jsoncodec
`)
	writeFile(
		t,
		filepath.Join(tmp, "jsoncodec", "go.mod"),
		"module example.com/jsoncodec\n\ngo 1.22\n",
	)
	writeFile(t, filepath.Join(tmp, "jsoncodec", "codec.go"), `package jsoncodec

import "encoding/json"

func Encode[T any](value T) ([]byte, error) {
	return json.Marshal(value)
}
`)
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_, _ = lib.Save()
}
`)
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

	if strings.Contains(joined, `field "Payload.Name"`) {
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
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_, _ = lib.Save()
}
`)
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

	if strings.Contains(joined, `field "Payload.Name"`) {
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
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_, _ = lib.Save()
}
`)
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
		`field "Payload.Name" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf("expected fake YAML generic marshal wrapper field to stay dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeDoesNotKeepFakeExternalGenericMarshalWrapperFields(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), `module example.com/sample

go 1.22

require example.com/jsoncodec v0.0.0

replace example.com/jsoncodec => ./jsoncodec
`)
	writeFile(
		t,
		filepath.Join(tmp, "jsoncodec", "go.mod"),
		"module example.com/jsoncodec\n\ngo 1.22\n",
	)
	writeFile(t, filepath.Join(tmp, "jsoncodec", "codec.go"), `package jsoncodec

func Encode[T any](value T) ([]byte, error) {
	return nil, nil
}
`)
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_, _ = lib.Save()
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "example.com/jsoncodec"

type Payload struct {
	Name string `+"`json:\"name\"`"+`
}

func Save() ([]byte, error) {
	return jsoncodec.Encode(Payload{})
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(
		joined,
		`field "Payload.Name" is unreachable from repo entrypoints; remove it`,
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
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_, _ = lib.Save()
}
`)
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
		`field "Payload.Name" is unreachable from repo entrypoints; remove it`,
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
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_, _ = lib.Save()
}
`)
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
		`field "Payload.Name" is unreachable from repo entrypoints; remove it`,
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
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_, _ = lib.Save()
}
`)
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
		`field "Payload.Name" is unreachable from repo entrypoints; remove it`,
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
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_, _ = lib.Save()
}
`)
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
		`field "Payload.Name" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf("expected jsonfake EncodeJSON to leave field dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeDoesNotKeepGenericMarshalFromDeadFuncLiteral(t *testing.T) {
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
		`field "Payload.Name" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf("expected dead func literal generic marshal to leave field dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeIgnoresYAMLMarshalNonRepresentationReturns(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), `module example.com/sample

go 1.22

require gopkg.in/yaml.v3 v3.0.0

replace gopkg.in/yaml.v3 => ./yaml
`)
	writeFile(t, filepath.Join(tmp, "yaml", "go.mod"), "module gopkg.in/yaml.v3\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "yaml", "yaml.go"), `package yaml

func Marshal(any) ([]byte, error) { return nil, nil }
`)
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_, _ = lib.Save()
}
`)
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
		`field "Payload.Name" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf("expected YAML non-representation returns to leave field dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsYAMLMapKeyHooks(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), `module example.com/sample

go 1.22

require gopkg.in/yaml.v3 v3.0.0

replace gopkg.in/yaml.v3 => ./yaml
`)
	writeFile(t, filepath.Join(tmp, "yaml", "go.mod"), "module gopkg.in/yaml.v3\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "yaml", "yaml.go"), `package yaml

type Node struct{}

func Marshal(any) ([]byte, error) { return nil, nil }
func Unmarshal([]byte, any) error { return nil }
`)
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_, _ = lib.Save()
	_ = lib.Load(nil)
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "gopkg.in/yaml.v3"

type Key struct {
	ID string
}

func (Key) MarshalYAML() (any, error) {
	return "key", nil
}

func (*Key) UnmarshalYAML(*yaml.Node) error {
	return nil
}

func Save() ([]byte, error) {
	return yaml.Marshal(map[Key]string{})
}

func Load(body []byte) error {
	var payload map[Key]string

	return yaml.Unmarshal(body, &payload)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	for _, unexpected := range []string{
		`method "MarshalYAML"`,
		`method "UnmarshalYAML"`,
	} {
		if strings.Contains(joined, unexpected) {
			t.Fatalf("YAML map key hook reported dead for %q, got:\n%s", unexpected, joined)
		}
	}

	if !strings.Contains(
		joined,
		`exported field "Key.ID" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf("expected unused YAML map key field finding, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsYAMLMapKeyFieldsWithoutHooks(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), `module example.com/sample

go 1.22

require gopkg.in/yaml.v3 v3.0.0

replace gopkg.in/yaml.v3 => ./yaml
`)
	writeFile(t, filepath.Join(tmp, "yaml", "go.mod"), "module gopkg.in/yaml.v3\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "yaml", "yaml.go"), `package yaml

func Marshal(any) ([]byte, error) { return nil, nil }
func Unmarshal([]byte, any) error { return nil }
`)
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_, _ = lib.Save()
	_ = lib.Load(nil)
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "gopkg.in/yaml.v3"

type Key struct {
	ID string `+"`yaml:\"id\"`"+`
}

func Save() ([]byte, error) {
	return yaml.Marshal(map[Key]string{})
}

func Load(body []byte) error {
	var payload map[Key]string

	return yaml.Unmarshal(body, &payload)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `field "Key.ID"`) {
		t.Fatalf("YAML map key field reported dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeReportsUnusedMarshalHooks(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	lib.Live()
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "fmt"

type OptionalFloat struct {
	Value float64
	Valid bool
}

func (value *OptionalFloat) UnmarshalJSON([]byte) error {
	return parseOptionalFloat()
}

func parseOptionalFloat() error {
	return fmt.Errorf("invalid")
}

func Live() {}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	for _, want := range []string{
		`method "UnmarshalJSON"`,
		`function "parseOptionalFloat"`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected unused marshal hook finding %q, got:\n%s", want, joined)
		}
	}
}

func TestRepoDeadCodeFallsThroughPointerMarshalHookForValueMarshal(t *testing.T) {
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

func (*Payload) MarshalJSON() ([]byte, error) {
	return nil, nil
}

func Save() ([]byte, error) {
	return json.Marshal(Payload{})
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `field "Payload.Name"`) {
		t.Fatalf("value-marshal field suppressed by pointer hook, got:\n%s", joined)
	}

	if !strings.Contains(
		joined,
		`method "MarshalJSON" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf("expected pointer-only marshal hook finding, got:\n%s", joined)
	}
}

func TestRepoDeadCodeIgnoresInvalidReflectedHookSignatures(t *testing.T) {
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

func (Payload) MarshalJSON() string {
	return ""
}

func Save() ([]byte, error) {
	return json.Marshal(Payload{})
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `field "Payload.Name"`) {
		t.Fatalf("field suppressed by invalid marshal hook signature, got:\n%s", joined)
	}

	if !strings.Contains(
		joined,
		`method "MarshalJSON" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf("expected invalid marshal hook finding, got:\n%s", joined)
	}
}

func TestRepoDeadCodeIgnoresInvalidYAMLAndXMLHookSignatures(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), `module example.com/sample

go 1.22

require gopkg.in/yaml.v3 v3.0.0

replace gopkg.in/yaml.v3 => ./yaml
`)
	writeFile(t, filepath.Join(tmp, "yaml", "go.mod"), "module gopkg.in/yaml.v3\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "yaml", "yaml.go"), `package yaml

func Marshal(any) ([]byte, error) { return nil, nil }
func Unmarshal([]byte, any) error { return nil }
`)
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_ = lib.Load(nil)
	_, _ = lib.Save()
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import (
	"encoding/xml"

	"gopkg.in/yaml.v3"
)

type Payload struct {
	Name string `+"`yaml:\"name\" xml:\"name\"`"+`
}

func (Payload) MarshalYAML() (string, error) {
	return "", nil
}

func (*Payload) UnmarshalYAML(int) error {
	return nil
}

func (Payload) MarshalXML(int, int) error {
	return nil
}

func Save() ([]byte, error) {
	return yaml.Marshal(Payload{})
}

func Load(body []byte) error {
	var payload Payload
	if err := yaml.Unmarshal(body, &payload); err != nil {
		return err
	}

	return xml.Unmarshal(body, &payload)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `field "Payload.Name"`) {
		t.Fatalf("field suppressed by invalid YAML/XML hook signature, got:\n%s", joined)
	}

	for _, want := range []string{
		`method "MarshalYAML"`,
		`method "UnmarshalYAML"`,
		`method "MarshalXML"`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected invalid hook finding %q, got:\n%s", want, joined)
		}
	}
}

func TestRepoDeadCodeMapsMarshalAliasFieldsToOriginalType(t *testing.T) {
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
	Name    string `+"`json:\"name\"`"+`
	Ignored string `+"`json:\"-\"`"+`
}

func (payload Payload) MarshalJSON() ([]byte, error) {
	type raw Payload

	return json.Marshal(raw(payload))
}

func Save() ([]byte, error) {
	return json.Marshal(Payload{})
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `field "Payload.Name"`) {
		t.Fatalf("marshal alias field reported dead, got:\n%s", joined)
	}

	if strings.Contains(joined, `method "MarshalJSON"`) {
		t.Fatalf("marshal alias hook reported dead, got:\n%s", joined)
	}

	if !strings.Contains(
		joined,
		`exported field "Payload.Ignored" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf("expected ignored marshal alias field finding, got:\n%s", joined)
	}
}

func TestRepoDeadCodeMapsDelegatedMarshalAliasFieldsToOriginalType(t *testing.T) {
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

func (payload Payload) MarshalJSON() ([]byte, error) {
	return marshalPayload(payload)
}

func marshalPayload(payload Payload) ([]byte, error) {
	type raw Payload

	return json.Marshal(raw(payload))
}

func Save() ([]byte, error) {
	return json.Marshal(Payload{})
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	for _, unexpected := range []string{
		`field "Payload.Name"`,
		`method "MarshalJSON"`,
		`function "marshalPayload"`,
	} {
		if strings.Contains(joined, unexpected) {
			t.Fatalf(
				"delegated marshal alias dependency reported dead for %q, got:\n%s",
				unexpected,
				joined,
			)
		}
	}
}

func TestRepoDeadCodeMapsPackageAliasFieldsToOriginalType(t *testing.T) {
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

import "encoding/json"

type Payload struct {
	Name string `+"`json:\"name\"`"+`
}

type rawPayload Payload

func (payload Payload) MarshalJSON() ([]byte, error) {
	return json.Marshal(rawPayload(payload))
}

func (payload *Payload) UnmarshalJSON(body []byte) error {
	var raw rawPayload
	if err := json.Unmarshal(body, &raw); err != nil {
		return err
	}

	*payload = Payload(raw)

	return nil
}

func Save() ([]byte, error) {
	return json.Marshal(Payload{})
}

func Load(body []byte) error {
	var payload Payload

	return json.Unmarshal(body, &payload)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	for _, unexpected := range []string{
		`field "Payload.Name"`,
		`method "MarshalJSON"`,
		`method "UnmarshalJSON"`,
	} {
		if strings.Contains(joined, unexpected) {
			t.Fatalf(
				"package alias dependency reported dead for %q, got:\n%s",
				unexpected,
				joined,
			)
		}
	}
}

func TestRepoDeadCodeMapsTextHookAliasFieldsToOriginalType(t *testing.T) {
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

import "encoding/json"

type Payload struct {
	Name string `+"`json:\"name\"`"+`
}

func (payload Payload) MarshalText() ([]byte, error) {
	type raw Payload

	return json.Marshal(raw(payload))
}

func (payload *Payload) UnmarshalText(text []byte) error {
	type raw Payload

	var value raw
	if err := json.Unmarshal(text, &value); err != nil {
		return err
	}

	*payload = Payload(value)

	return nil
}

func Save() ([]byte, error) {
	return json.Marshal(Payload{})
}

func Load(body []byte) error {
	var payload Payload

	return json.Unmarshal(body, &payload)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	for _, unexpected := range []string{
		`field "Payload.Name"`,
		`method "MarshalText"`,
		`method "UnmarshalText"`,
	} {
		if strings.Contains(joined, unexpected) {
			t.Fatalf(
				"text hook alias dependency reported dead for %q, got:\n%s",
				unexpected,
				joined,
			)
		}
	}
}

func TestRepoDeadCodeKeepsFieldsPassedThroughGenericJSONMarshal(t *testing.T) {
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

func Encode[T any](value T) ([]byte, error) {
	return json.Marshal(value)
}

func Save() ([]byte, error) {
	return Encode(Payload{})
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `field "Payload.Name"`) {
		t.Fatalf("generic marshal field reported dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsFieldsPassedThroughGenericInterfaceConversions(t *testing.T) {
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

import "encoding/json"

type Payload struct {
	Name string `+"`json:\"name\"`"+`
}

func Encode[T any](value T) ([]byte, error) {
	return json.Marshal(any(value))
}

func Decode[T any](body []byte, out *T) error {
	return json.Unmarshal(body, any(out))
}

func Save() ([]byte, error) {
	return Encode(Payload{})
}

func Load(body []byte) error {
	var payload Payload

	return Decode(body, &payload)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `field "Payload.Name"`) {
		t.Fatalf("generic interface conversion field reported dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsPointerHooksPassedThroughGenericJSONMarshal(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_, _ = lib.SavePointer()
	_, _ = lib.SaveNamedSlice()
	_, _ = lib.SaveSlice()
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "encoding/json"

type Payload struct{}

func (*Payload) MarshalJSON() ([]byte, error) {
	return nil, nil
}

func EncodePointer[T any](value T) ([]byte, error) {
	return json.Marshal(&value)
}

func EncodeSlice[T any](value T) ([]byte, error) {
	return json.Marshal([]T{value})
}

func EncodeNamedSlice[T any](value T) ([]byte, error) {
	type Slice []T

	return json.Marshal(Slice{value})
}

func SavePointer() ([]byte, error) {
	return EncodePointer(Payload{})
}

func SaveNamedSlice() ([]byte, error) {
	return EncodeNamedSlice(Payload{})
}

func SaveSlice() ([]byte, error) {
	return EncodeSlice(Payload{})
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `method "MarshalJSON"`) {
		t.Fatalf("generic pointer/container marshal hook reported dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeDoesNotKeepGenericJSONMapKeyFields(t *testing.T) {
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

type Key struct {
	ID string
}

func (Key) MarshalText() ([]byte, error) {
	return []byte("key"), nil
}

func Encode[K comparable, V any](value map[K]V) ([]byte, error) {
	return json.Marshal(value)
}

func Save() ([]byte, error) {
	return Encode[Key, string](nil)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `method "MarshalText"`) {
		t.Fatalf("generic map key text hook reported dead, got:\n%s", joined)
	}

	if !strings.Contains(
		joined,
		`exported field "Key.ID" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf("expected unused generic map key field finding, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsGenericJSONMapKeyUnmarshalTextHook(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_ = lib.Load(nil)
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "encoding/json"

type Key struct {
	ID string
}

func (*Key) UnmarshalText([]byte) error {
	return nil
}

func Decode[K comparable, V any](body []byte, out *map[K]V) error {
	return json.Unmarshal(body, out)
}

func DecodeVia[K comparable, V any](body []byte, out *map[K]V) error {
	return Decode(body, out)
}

func Load(body []byte) error {
	var payload map[Key]string

	return DecodeVia(body, &payload)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `method "UnmarshalText"`) {
		t.Fatalf("generic map key unmarshal text hook reported dead, got:\n%s", joined)
	}

	if !strings.Contains(
		joined,
		`exported field "Key.ID" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf("expected unused generic map key field finding, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsJSONMapKeyTextHooks(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_, _ = lib.Save()
	_, _ = lib.Load(nil)
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "encoding/json"

type Key string

func (key Key) MarshalText() ([]byte, error) {
	return []byte(key), nil
}

func (key *Key) UnmarshalText(text []byte) error {
	*key = Key(text)

	return nil
}

type Value struct {
	Name string `+"`json:\"name\"`"+`
}

func Save() ([]byte, error) {
	return json.Marshal(map[Key]Value{Key("id"): {Name: "x"}})
}

func Load(body []byte) (map[Key]Value, error) {
	var payload map[Key]Value
	err := json.Unmarshal(body, &payload)

	return payload, err
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	for _, unexpected := range []string{
		`method "MarshalText"`,
		`method "UnmarshalText"`,
		`field "Value.Name"`,
	} {
		if strings.Contains(joined, unexpected) {
			t.Fatalf(
				"JSON map-key text hook dependency reported dead for %q, got:\n%s",
				unexpected,
				joined,
			)
		}
	}
}

func TestRepoDeadCodeKeepsXMLAttrHooks(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_, _ = lib.Save(lib.Item{})
	_, _ = lib.Load(nil)
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "encoding/xml"

type Attr string

func (attr Attr) MarshalXMLAttr(name xml.Name) (xml.Attr, error) {
	return xml.Attr{Name: name, Value: string(attr)}, nil
}

func (attr *Attr) UnmarshalXMLAttr(value xml.Attr) error {
	*attr = Attr(value.Value)

	return nil
}

type Item struct {
	ID Attr `+"`xml:\"id,attr\"`"+`
}

func Save(item Item) ([]byte, error) {
	return xml.Marshal(item)
}

func Load(body []byte) (Item, error) {
	var item Item
	err := xml.Unmarshal(body, &item)

	return item, err
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	for _, unexpected := range []string{
		`method "MarshalXMLAttr"`,
		`method "UnmarshalXMLAttr"`,
		`field "Item.ID"`,
	} {
		if strings.Contains(joined, unexpected) {
			t.Fatalf("XML attr hook dependency reported dead for %q, got:\n%s", unexpected, joined)
		}
	}
}

func TestRepoDeadCodeKeepsXMLElementAPIHooksAndFields(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_ = lib.Save(lib.Payload{})
	_, _ = lib.Load(nil)
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import (
	"bytes"
	"encoding/xml"
)

type XMLValue string

func (value XMLValue) MarshalXML(encoder *xml.Encoder, start xml.StartElement) error {
	return encoder.EncodeElement(string(value), start)
}

func (value *XMLValue) UnmarshalXML(decoder *xml.Decoder, start xml.StartElement) error {
	var text string
	if err := decoder.DecodeElement(&text, &start); err != nil {
		return err
	}

	*value = XMLValue(text)

	return nil
}

type Payload struct {
	Value XMLValue `+"`xml:\"value\"`"+`
	Name  string   `+"`xml:\"name\"`"+`
}

func Save(payload Payload) error {
	var buffer bytes.Buffer
	encoder := xml.NewEncoder(&buffer)

	return encoder.EncodeElement(
		payload,
		xml.StartElement{Name: xml.Name{Local: "payload"}},
	)
}

func Load(body []byte) (Payload, error) {
	decoder := xml.NewDecoder(bytes.NewReader(body))
	var payload Payload
	var start xml.StartElement
	err := decoder.DecodeElement(&payload, &start)

	return payload, err
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	for _, unexpected := range []string{
		`method "MarshalXML"`,
		`method "UnmarshalXML"`,
		`field "Payload.Value"`,
		`field "Payload.Name"`,
	} {
		if strings.Contains(joined, unexpected) {
			t.Fatalf(
				"XML element API dependency reported dead for %q, got:\n%s",
				unexpected,
				joined,
			)
		}
	}
}

func TestRepoDeadCodeKeepsGenericXMLElementDecodeFields(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_, _ = lib.Load(nil)
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import (
	"bytes"
	"encoding/xml"
)

type XMLValue string

func (value *XMLValue) UnmarshalXML(decoder *xml.Decoder, start xml.StartElement) error {
	var text string
	if err := decoder.DecodeElement(&text, &start); err != nil {
		return err
	}

	*value = XMLValue(text)

	return nil
}

type Payload struct {
	Value XMLValue `+"`xml:\"value\"`"+`
	Name  string   `+"`xml:\"name\"`"+`
}

func decodeElement[T any](
	decoder *xml.Decoder,
	start xml.StartElement,
) (T, error) {
	var value T
	err := decoder.DecodeElement(&value, &start)

	return value, err
}

func Load(body []byte) (Payload, error) {
	decoder := xml.NewDecoder(bytes.NewReader(body))
	var start xml.StartElement

	return decodeElement[Payload](decoder, start)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	for _, unexpected := range []string{
		`method "UnmarshalXML"`,
		`field "Payload.Value"`,
		`field "Payload.Name"`,
	} {
		if strings.Contains(joined, unexpected) {
			t.Fatalf(
				"generic XML element dependency reported dead for %q, got:\n%s",
				unexpected,
				joined,
			)
		}
	}
}
