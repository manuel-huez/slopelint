package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRepoDeadCodeKeepsYAMLMapKeyHooks(t *testing.T) {
	tmp := newYAMLTestModule(t)
	writeFile(t, filepath.Join(tmp, "yaml", "yaml.go"), `package yaml

type Node struct{}

func Marshal(any) ([]byte, error) { return nil, nil }
func Unmarshal([]byte, any) error { return nil }
`)
	writeTestMain(t, tmp, `	_, _ = lib.Save()
	_ = lib.Load(nil)`)
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
		wantMethodMarshalYAML,
		wantMethodUnmarshalYAML,
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
	tmp := newYAMLTestModule(t)
	writeFile(t, filepath.Join(tmp, "yaml", "yaml.go"), `package yaml

func Marshal(any) ([]byte, error) { return nil, nil }
func Unmarshal([]byte, any) error { return nil }
`)
	writeTestMain(t, tmp, `	_, _ = lib.Save()
	_ = lib.Load(nil)`)
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
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	lib.Live()`)
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

func Live() {
	_ = OptionalFloat{}
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(joined, wantMethodUnmarshalJSON) {
		t.Fatalf(
			"expected unused marshal hook finding %q, got:\n%s",
			wantMethodUnmarshalJSON,
			joined,
		)
	}

	if strings.Contains(joined, `function "parseOptionalFloat"`) {
		t.Fatalf("unexpected unused marshal hook cascade, got:\n%s", joined)
	}
}

func TestRepoDeadCodeFallsThroughPointerMarshalHookForValueMarshal(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_, _ = lib.Save()`)
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

	if strings.Contains(joined, wantFieldPayloadName) {
		t.Fatalf("value-marshal field suppressed by pointer hook, got:\n%s", joined)
	}

	if !strings.Contains(
		joined,
		wantRemoveMethodMarshalJSON,
	) {
		t.Fatalf("expected pointer-only marshal hook finding, got:\n%s", joined)
	}
}

func TestRepoDeadCodeIgnoresInvalidReflectedHookSignatures(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_, _ = lib.Save()`)
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

	if strings.Contains(joined, wantFieldPayloadName) {
		t.Fatalf("field suppressed by invalid marshal hook signature, got:\n%s", joined)
	}

	if !strings.Contains(
		joined,
		wantRemoveMethodMarshalJSON,
	) {
		t.Fatalf("expected invalid marshal hook finding, got:\n%s", joined)
	}
}

func TestRepoDeadCodeIgnoresInvalidYAMLAndXMLHookSignatures(t *testing.T) {
	tmp := newYAMLTestModule(t)
	writeFile(t, filepath.Join(tmp, "yaml", "yaml.go"), `package yaml

func Marshal(any) ([]byte, error) { return nil, nil }
func Unmarshal([]byte, any) error { return nil }
`)
	writeTestMain(t, tmp, `	_ = lib.Load(nil)
	_, _ = lib.Save()`)
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

	if strings.Contains(joined, wantFieldPayloadName) {
		t.Fatalf("field suppressed by invalid YAML/XML hook signature, got:\n%s", joined)
	}

	for _, want := range []string{
		wantMethodMarshalYAML,
		wantMethodUnmarshalYAML,
		`method "MarshalXML"`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected invalid hook finding %q, got:\n%s", want, joined)
		}
	}
}

func TestRepoDeadCodeMapsMarshalAliasFieldsToOriginalType(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_, _ = lib.Save()`)
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

	if strings.Contains(joined, wantFieldPayloadName) {
		t.Fatalf("marshal alias field reported dead, got:\n%s", joined)
	}

	if strings.Contains(joined, wantMethodMarshalJSON) {
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
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_, _ = lib.Save()`)
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
		wantFieldPayloadName,
		wantMethodMarshalJSON,
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
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_ = lib.Load(nil)
	_, _ = lib.Save()`)
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
		wantFieldPayloadName,
		wantMethodMarshalJSON,
		wantMethodUnmarshalJSON,
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
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_ = lib.Load(nil)
	_, _ = lib.Save()`)
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
		wantFieldPayloadName,
		wantMethodMarshalText,
		wantMethodUnmarshalText,
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
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_, _ = lib.Save()`)
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

	if strings.Contains(joined, wantFieldPayloadName) {
		t.Fatalf("generic marshal field reported dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsFieldsPassedThroughGenericInterfaceConversions(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_ = lib.Load(nil)
	_, _ = lib.Save()`)
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

	if strings.Contains(joined, wantFieldPayloadName) {
		t.Fatalf("generic interface conversion field reported dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsPointerHooksPassedThroughGenericJSONMarshal(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_, _ = lib.SavePointer()
	_, _ = lib.SaveNamedSlice()
	_, _ = lib.SaveSlice()`)
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

	if strings.Contains(joined, wantMethodMarshalJSON) {
		t.Fatalf("generic pointer/container marshal hook reported dead, got:\n%s", joined)
	}
}

func TestRepoDeadCodeDoesNotKeepGenericJSONMapKeyFields(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_, _ = lib.Save()`)
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

	if strings.Contains(joined, wantMethodMarshalText) {
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
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_ = lib.Load(nil)`)
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

	if strings.Contains(joined, wantMethodUnmarshalText) {
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
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_, _ = lib.Save()
	_, _ = lib.Load(nil)`)
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
		wantMethodMarshalText,
		wantMethodUnmarshalText,
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
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_, _ = lib.Save(lib.Item{})
	_, _ = lib.Load(nil)`)
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
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_ = lib.Save(lib.Payload{})
	_, _ = lib.Load(nil)`)
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
		wantFieldPayloadName,
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
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_, _ = lib.Load(nil)`)
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
		wantFieldPayloadName,
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
