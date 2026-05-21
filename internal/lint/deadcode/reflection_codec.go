package deadcode

import (
	"go/ast"
	"go/types"
	"reflect"
	"slices"
	"strings"
)

type reflectedHookContext uint8
type reflectedHookSignatureValidator func(*types.Signature) bool

type reflectedCodec struct {
	tag                 string
	decodeHooks         []string
	decodeAttrHooks     []string
	decodeMapKeyHooks   []string
	marshalHooks        []string
	marshalAttrHooks    []string
	marshalMapKeyHooks  []string
	mapKeyFallbackField bool
}

type reflectedPackageCodec struct {
	tag     string
	decode  bool
	marshal bool
}

const (
	reflectedJSONTag      = "json"
	reflectedXMLTag       = "xml"
	reflectedYAMLTag      = "yaml"
	reflectedDedupeMinLen = 2
	reflectedEncodingXML  = "encoding/xml"
	reflectedXMLHookArgs  = 2
	reflectedYAMLHookArgs = 1
)

const (
	reflectedValueHook reflectedHookContext = iota
	reflectedAttrHook
	reflectedMapKeyHook
)

var reflectedCodecsByTag = map[string]reflectedCodec{
	"": {
		tag: "",
	},
	reflectedJSONTag: {
		tag:                reflectedJSONTag,
		decodeHooks:        []string{"UnmarshalJSON", "UnmarshalText"},
		decodeAttrHooks:    []string{"UnmarshalJSON", "UnmarshalText"},
		decodeMapKeyHooks:  []string{"UnmarshalText"},
		marshalHooks:       []string{"MarshalJSON", "MarshalText"},
		marshalAttrHooks:   []string{"MarshalJSON", "MarshalText"},
		marshalMapKeyHooks: []string{"MarshalText"},
	},
	reflectedXMLTag: {
		tag:                reflectedXMLTag,
		decodeHooks:        []string{"UnmarshalXML", "UnmarshalText"},
		decodeAttrHooks:    []string{"UnmarshalXMLAttr", "UnmarshalText"},
		decodeMapKeyHooks:  []string{"UnmarshalText"},
		marshalHooks:       []string{"MarshalXML", "MarshalText"},
		marshalAttrHooks:   []string{"MarshalXMLAttr", "MarshalText"},
		marshalMapKeyHooks: []string{"MarshalText"},
	},
	reflectedYAMLTag: {
		tag:                 reflectedYAMLTag,
		decodeHooks:         []string{"UnmarshalYAML", "UnmarshalText"},
		decodeAttrHooks:     []string{"UnmarshalYAML", "UnmarshalText"},
		decodeMapKeyHooks:   []string{"UnmarshalYAML", "UnmarshalText"},
		marshalHooks:        []string{"MarshalYAML", "MarshalText"},
		marshalAttrHooks:    []string{"MarshalYAML", "MarshalText"},
		marshalMapKeyHooks:  []string{"MarshalYAML", "MarshalText"},
		mapKeyFallbackField: true,
	},
}

var reflectedPackageCodecs = map[string]reflectedPackageCodec{
	"encoding/gob": {
		tag:    "",
		decode: true,
	},
	"encoding/json": {
		tag:     reflectedJSONTag,
		decode:  true,
		marshal: true,
	},
	reflectedEncodingXML: {
		tag:     reflectedXMLTag,
		decode:  true,
		marshal: true,
	},
	"github.com/goccy/go-yaml": {
		tag:     reflectedYAMLTag,
		decode:  true,
		marshal: true,
	},
	"gopkg.in/yaml.v2": {
		tag:     reflectedYAMLTag,
		decode:  true,
		marshal: true,
	},
	"gopkg.in/yaml.v3": {
		tag:     reflectedYAMLTag,
		decode:  true,
		marshal: true,
	},
	"sigs.k8s.io/yaml": {
		tag:     reflectedJSONTag,
		decode:  true,
		marshal: true,
	},
}

var reflectedDecodeFuncNames = map[string]struct{}{
	"Decode":        {},
	"DecodeElement": {},
	"Unmarshal":     {},
}

var reflectedMarshalFuncNames = map[string]struct{}{
	"Encode":        {},
	"EncodeElement": {},
	"Marshal":       {},
	"MarshalIndent": {},
}

var reflectedHookSignatureValidators = map[string]reflectedHookSignatureValidator{
	"MarshalJSON":      reflectedNoParamBytesErrorSignature,
	"MarshalText":      reflectedNoParamBytesErrorSignature,
	"UnmarshalJSON":    reflectedBytesParamErrorSignature,
	"UnmarshalText":    reflectedBytesParamErrorSignature,
	"MarshalYAML":      reflectedNoParamValueErrorSignature,
	"UnmarshalYAML":    reflectedYAMLUnmarshalSignature,
	"MarshalXML":       reflectedMarshalXMLSignature,
	"UnmarshalXML":     reflectedUnmarshalXMLSignature,
	"MarshalXMLAttr":   reflectedMarshalXMLAttrSignature,
	"UnmarshalXMLAttr": reflectedUnmarshalXMLAttrSignature,
}

func reflectedDecodeFuncTag(fn *types.Func) (string, bool) {
	// Only known reflection decoders set fields by name; local Decode funcs use normal edges.
	return reflectedFuncTag(fn, reflectedDecodeFuncNames, func(codec reflectedPackageCodec) bool {
		return codec.decode
	})
}

func reflectedDecodeTargetArgIndex(fn *types.Func, call *ast.CallExpr) int {
	if fn != nil &&
		fn.Pkg() != nil &&
		fn.Pkg().Path() == reflectedEncodingXML &&
		fn.Name() == "DecodeElement" {
		return 0
	}

	return len(call.Args) - 1
}
func reflectedMarshalFuncTag(fn *types.Func) (string, bool) {
	return reflectedFuncTag(fn, reflectedMarshalFuncNames, func(codec reflectedPackageCodec) bool {
		return codec.marshal
	})
}

func reflectedFuncTag(
	fn *types.Func,
	names map[string]struct{},
	enabled func(reflectedPackageCodec) bool,
) (string, bool) {
	if fn == nil || fn.Pkg() == nil {
		return "", false
	}

	codec, ok := reflectedPackageCodecs[fn.Pkg().Path()]
	if !ok || !enabled(codec) {
		return "", false
	}

	_, ok = names[fn.Name()]

	return codec.tag, ok
}

type reflectedFieldTag struct {
	ignored bool
	attr    bool
}

func reflectedStructFieldTag(structTag string, tag string) reflectedFieldTag {
	if tag == "" {
		return reflectedFieldTag{}
	}

	value := reflect.StructTag(structTag).Get(tag)
	if value == "-" {
		return reflectedFieldTag{ignored: true}
	}

	return reflectedFieldTag{
		attr: tag == reflectedXMLTag && reflectedTagHasOption(value, "attr"),
	}
}

func reflectedTagHasOption(value string, option string) bool {
	_, options, ok := strings.Cut(value, ",")
	if !ok {
		return false
	}

	return slices.Contains(strings.Split(options, ","), option)
}
func reflectedHookMethodSignature(fn *types.Func, name string) bool {
	if fn == nil {
		return false
	}

	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig == nil {
		return false
	}

	return reflectedHookSignatureMatchesName(sig, name)
}

func reflectedHookSignatureMatchesName(sig *types.Signature, name string) bool {
	validator := reflectedHookSignatureValidators[name]
	if validator == nil {
		return false
	}

	return validator(sig)
}

func reflectedNoParamBytesErrorSignature(sig *types.Signature) bool {
	return tupleLen(sig.Params()) == 0 &&
		tupleLen(sig.Results()) == 2 &&
		reflectedExactByteSliceType(sig.Results().At(0).Type()) &&
		typeIsError(sig.Results().At(1).Type())
}

func reflectedBytesParamErrorSignature(sig *types.Signature) bool {
	return tupleLen(sig.Params()) == 1 &&
		reflectedExactByteSliceType(sig.Params().At(0).Type()) &&
		tupleLen(sig.Results()) == 1 &&
		typeIsError(sig.Results().At(0).Type())
}

func reflectedNoParamValueErrorSignature(sig *types.Signature) bool {
	return tupleLen(sig.Params()) == 0 &&
		tupleLen(sig.Results()) == 2 &&
		typeIsAny(sig.Results().At(0).Type()) &&
		typeIsError(sig.Results().At(1).Type())
}

func reflectedYAMLUnmarshalSignature(sig *types.Signature) bool {
	return tupleLen(sig.Params()) == 1 &&
		reflectedYAMLUnmarshalParamType(sig.Params().At(0).Type()) &&
		reflectedErrorOnlyResultSignature(sig)
}

func reflectedYAMLUnmarshalParamType(typ types.Type) bool {
	if reflectedPointerToNamedType(typ, "gopkg.in/yaml.v3", "Node") ||
		reflectedPointerToNamedType(typ, "github.com/goccy/go-yaml", "Node") {
		return true
	}

	sig, ok := types.Unalias(typ).Underlying().(*types.Signature)

	return ok &&
		tupleLen(sig.Params()) == reflectedYAMLHookArgs &&
		typeIsAny(sig.Params().At(0).Type()) &&
		reflectedErrorOnlyResultSignature(sig)
}

func reflectedMarshalXMLSignature(sig *types.Signature) bool {
	return tupleLen(sig.Params()) == reflectedXMLHookArgs &&
		reflectedPointerToNamedType(sig.Params().At(0).Type(), reflectedEncodingXML, "Encoder") &&
		namedTypeMatches(sig.Params().At(1).Type(), reflectedEncodingXML, "StartElement") &&
		reflectedErrorOnlyResultSignature(sig)
}

func reflectedUnmarshalXMLSignature(sig *types.Signature) bool {
	return tupleLen(sig.Params()) == reflectedXMLHookArgs &&
		reflectedPointerToNamedType(sig.Params().At(0).Type(), reflectedEncodingXML, "Decoder") &&
		namedTypeMatches(sig.Params().At(1).Type(), reflectedEncodingXML, "StartElement") &&
		reflectedErrorOnlyResultSignature(sig)
}

func reflectedMarshalXMLAttrSignature(sig *types.Signature) bool {
	return tupleLen(sig.Params()) == 1 &&
		namedTypeMatches(sig.Params().At(0).Type(), reflectedEncodingXML, "Name") &&
		tupleLen(sig.Results()) == 2 &&
		namedTypeMatches(sig.Results().At(0).Type(), reflectedEncodingXML, "Attr") &&
		typeIsError(sig.Results().At(1).Type())
}

func reflectedUnmarshalXMLAttrSignature(sig *types.Signature) bool {
	return tupleLen(sig.Params()) == 1 &&
		namedTypeMatches(sig.Params().At(0).Type(), reflectedEncodingXML, "Attr") &&
		reflectedErrorOnlyResultSignature(sig)
}

func reflectedErrorOnlyResultSignature(sig *types.Signature) bool {
	return tupleLen(sig.Results()) == 1 && typeIsError(sig.Results().At(0).Type())
}

func reflectedExactByteSliceType(typ types.Type) bool {
	return types.Identical(typ, types.NewSlice(types.Typ[types.Byte]))
}

func reflectedPointerToNamedType(typ types.Type, pkgPath string, name string) bool {
	ptr, ok := types.Unalias(typ).(*types.Pointer)
	if !ok {
		return false
	}

	return namedTypeMatches(ptr.Elem(), pkgPath, name)
}

func reflectedHookNames(
	tag string,
	mode reflectedStructFieldUseMode,
	context reflectedHookContext,
) []string {
	codec, ok := reflectedCodecsByTag[tag]
	if !ok {
		return nil
	}

	switch mode {
	case reflectedDecodeStructFields:
		switch context {
		case reflectedValueHook:
			return codec.decodeHooks
		case reflectedAttrHook:
			return codec.decodeAttrHooks
		case reflectedMapKeyHook:
			return codec.decodeMapKeyHooks
		}
	case reflectedMarshalStructFields:
		switch context {
		case reflectedValueHook:
			return codec.marshalHooks
		case reflectedAttrHook:
			return codec.marshalAttrHooks
		case reflectedMapKeyHook:
			return codec.marshalMapKeyHooks
		}
	}

	return nil
}

func reflectedMapKeyFallbackField(tag string) bool {
	return reflectedCodecsByTag[tag].mapKeyFallbackField
}
