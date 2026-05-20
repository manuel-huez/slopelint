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

func DecodeJSON[T any]([]byte) (T, error) {
	var zero T

	return zero, nil
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

func Live(body []byte) error {
	payload, err := jsoncodec.DecodeJSON[Payload](body)
	if err != nil {
		return err
	}

	_ = payload

	return nil
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, `field "Payload.FromJSON"`) {
		t.Fatalf("external generic DecodeJSON result field reported dead, got:\n%s", joined)
	}

	if !strings.Contains(
		joined,
		`exported field "Payload.Ignored" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf("expected ignored external generic DecodeJSON field finding, got:\n%s", joined)
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

func TestRepoDeadCodeKeepsAllMarshalAndUnmarshalPrefixedMethods(t *testing.T) {
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

func (value Scalar) MarshalText() ([]byte, error) {
	return []byte(value), nil
}

func (value *Scalar) UnmarshalText([]byte) error {
	*value = "decoded"

	return nil
}

type Manifest struct {
	Value *Scalar `+"`yaml:\"value\"`"+`
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
	return yaml.Marshal(Manifest{Value: &value})
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
