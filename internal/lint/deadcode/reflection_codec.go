package deadcode

import (
	"go/ast"
	"go/types"
	"maps"
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
	tag          string
	decodeFuncs  map[string]reflectedCodecFunc
	marshalFuncs map[string]reflectedCodecFunc
}

type reflectedCodecFunc struct {
	argIndex int
	hookTag  string
}

const (
	reflectedJSONTag      = "json"
	reflectedGoccyJSONTag = "json:goccy"
	reflectedXMLTag       = "xml"
	reflectedYAMLTag      = "yaml"
)

const (
	reflectedDedupeMinLen = 2
	reflectedLastArgIndex = -1
)

const (
	reflectedFirstArgIndex              = 0
	reflectedSecondArgIndex             = 1
	reflectedGoccyContextUnmarshalIndex = 2
)

const (
	reflectedContext     = "context"
	reflectedEncodingXML = "encoding/xml"
	reflectedGoccyJSON   = "github.com/goccy/go-json"
)

const (
	reflectedXMLHookArgs  = 2
	reflectedYAMLHookArgs = 1
)

const (
	reflectedDecodeFunc    = "Decode"
	reflectedEncodeFunc    = "Encode"
	reflectedMarshalFunc   = "Marshal"
	reflectedUnmarshalFunc = "Unmarshal"
)

const (
	reflectedMarshalJSONHook   = "MarshalJSON"
	reflectedMarshalTextHook   = "MarshalText"
	reflectedMarshalYAMLHook   = "MarshalYAML"
	reflectedUnmarshalJSONHook = "UnmarshalJSON"
	reflectedUnmarshalTextHook = "UnmarshalText"
	reflectedUnmarshalYAMLHook = "UnmarshalYAML"
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
		decodeHooks:        []string{reflectedUnmarshalJSONHook, reflectedUnmarshalTextHook},
		decodeAttrHooks:    []string{reflectedUnmarshalJSONHook, reflectedUnmarshalTextHook},
		decodeMapKeyHooks:  []string{reflectedUnmarshalTextHook},
		marshalHooks:       []string{reflectedMarshalJSONHook, reflectedMarshalTextHook},
		marshalAttrHooks:   []string{reflectedMarshalJSONHook, reflectedMarshalTextHook},
		marshalMapKeyHooks: []string{reflectedMarshalTextHook},
	},
	reflectedXMLTag: {
		tag:                reflectedXMLTag,
		decodeHooks:        []string{"UnmarshalXML", reflectedUnmarshalTextHook},
		decodeAttrHooks:    []string{"UnmarshalXMLAttr", reflectedUnmarshalTextHook},
		decodeMapKeyHooks:  []string{reflectedUnmarshalTextHook},
		marshalHooks:       []string{"MarshalXML", reflectedMarshalTextHook},
		marshalAttrHooks:   []string{"MarshalXMLAttr", reflectedMarshalTextHook},
		marshalMapKeyHooks: []string{reflectedMarshalTextHook},
	},
	reflectedYAMLTag: {
		tag:                 reflectedYAMLTag,
		decodeHooks:         []string{reflectedUnmarshalYAMLHook, reflectedUnmarshalTextHook},
		decodeAttrHooks:     []string{reflectedUnmarshalYAMLHook, reflectedUnmarshalTextHook},
		decodeMapKeyHooks:   []string{reflectedUnmarshalYAMLHook, reflectedUnmarshalTextHook},
		marshalHooks:        []string{reflectedMarshalYAMLHook, reflectedMarshalTextHook},
		marshalAttrHooks:    []string{reflectedMarshalYAMLHook, reflectedMarshalTextHook},
		marshalMapKeyHooks:  []string{reflectedMarshalYAMLHook, reflectedMarshalTextHook},
		mapKeyFallbackField: true,
	},
}

var (
	reflectedLastArgDecodeFunc = reflectedCodecFunc{argIndex: reflectedLastArgIndex}
	reflectedFirstArgCodecFunc = reflectedCodecFunc{argIndex: reflectedFirstArgIndex}
)

var (
	reflectedLastArgDecodeFuncs = map[string]reflectedCodecFunc{
		reflectedDecodeFunc:    reflectedLastArgDecodeFunc,
		reflectedUnmarshalFunc: reflectedLastArgDecodeFunc,
	}
	reflectedEncodeMarshalFuncs = map[string]reflectedCodecFunc{
		reflectedEncodeFunc:  reflectedFirstArgCodecFunc,
		reflectedMarshalFunc: reflectedFirstArgCodecFunc,
	}
)

var reflectedPackageCodecs = map[string]reflectedPackageCodec{
	"encoding/gob": {
		tag: "",
		decodeFuncs: map[string]reflectedCodecFunc{
			reflectedDecodeFunc: reflectedLastArgDecodeFunc,
		},
	},
	"encoding/json": {
		tag:          reflectedJSONTag,
		decodeFuncs:  reflectedLastArgDecodeFuncs,
		marshalFuncs: reflectedCodecFuncsWith(reflectedEncodeMarshalFuncs, "MarshalIndent"),
	},
	reflectedGoccyJSON: {
		tag: reflectedJSONTag,
		// goccy context hooks are only reachable through APIs that carry context.
		decodeFuncs: map[string]reflectedCodecFunc{
			reflectedDecodeFunc:    reflectedLastArgDecodeFunc,
			"DecodeContext":        reflectedContextCodecFunc(reflectedSecondArgIndex),
			"DecodeWithOption":     reflectedFirstArgCodecFunc,
			reflectedUnmarshalFunc: {argIndex: reflectedSecondArgIndex},
			"UnmarshalContext":     reflectedContextCodecFunc(reflectedGoccyContextUnmarshalIndex),
			"UnmarshalNoEscape":    {argIndex: reflectedSecondArgIndex},
			"UnmarshalWithOption":  {argIndex: reflectedSecondArgIndex},
		},
		marshalFuncs: map[string]reflectedCodecFunc{
			reflectedEncodeFunc:       reflectedFirstArgCodecFunc,
			"EncodeContext":           reflectedContextCodecFunc(reflectedSecondArgIndex),
			"EncodeWithOption":        reflectedFirstArgCodecFunc,
			reflectedMarshalFunc:      reflectedFirstArgCodecFunc,
			"MarshalContext":          reflectedContextCodecFunc(reflectedSecondArgIndex),
			"MarshalIndent":           reflectedFirstArgCodecFunc,
			"MarshalIndentWithOption": reflectedFirstArgCodecFunc,
			"MarshalNoEscape":         reflectedFirstArgCodecFunc,
			"MarshalWithOption":       reflectedFirstArgCodecFunc,
		},
	},
	reflectedEncodingXML: {
		tag: reflectedXMLTag,
		decodeFuncs: map[string]reflectedCodecFunc{
			reflectedDecodeFunc:    reflectedLastArgDecodeFunc,
			"DecodeElement":        reflectedFirstArgCodecFunc,
			reflectedUnmarshalFunc: reflectedLastArgDecodeFunc,
		},
		marshalFuncs: map[string]reflectedCodecFunc{
			reflectedEncodeFunc:  reflectedFirstArgCodecFunc,
			"EncodeElement":      reflectedFirstArgCodecFunc,
			reflectedMarshalFunc: reflectedFirstArgCodecFunc,
			"MarshalIndent":      reflectedFirstArgCodecFunc,
		},
	},
	"github.com/goccy/go-yaml": {
		tag:          reflectedYAMLTag,
		decodeFuncs:  reflectedLastArgDecodeFuncs,
		marshalFuncs: reflectedEncodeMarshalFuncs,
	},
	"gopkg.in/yaml.v2": {
		tag:          reflectedYAMLTag,
		decodeFuncs:  reflectedLastArgDecodeFuncs,
		marshalFuncs: reflectedEncodeMarshalFuncs,
	},
	"gopkg.in/yaml.v3": {
		tag:          reflectedYAMLTag,
		decodeFuncs:  reflectedLastArgDecodeFuncs,
		marshalFuncs: reflectedEncodeMarshalFuncs,
	},
	"sigs.k8s.io/yaml": {
		tag:         reflectedJSONTag,
		decodeFuncs: reflectedLastArgDecodeFuncs,
		marshalFuncs: map[string]reflectedCodecFunc{
			reflectedMarshalFunc: reflectedFirstArgCodecFunc,
		},
	},
}

func reflectedContextCodecFunc(argIndex int) reflectedCodecFunc {
	return reflectedCodecFunc{
		argIndex: argIndex,
		hookTag:  reflectedGoccyJSONTag,
	}
}

func reflectedCodecFuncsWith(
	funcs map[string]reflectedCodecFunc,
	names ...string,
) map[string]reflectedCodecFunc {
	out := make(map[string]reflectedCodecFunc, len(funcs)+len(names))
	maps.Copy(out, funcs)

	for _, name := range names {
		out[name] = reflectedFirstArgCodecFunc
	}

	return out
}

var reflectedHookSignatureValidators = map[string]reflectedHookSignatureValidator{
	reflectedMarshalJSONHook:   reflectedNoParamBytesErrorSignature,
	reflectedMarshalTextHook:   reflectedNoParamBytesErrorSignature,
	reflectedUnmarshalJSONHook: reflectedBytesParamErrorSignature,
	reflectedUnmarshalTextHook: reflectedBytesParamErrorSignature,
	reflectedMarshalYAMLHook: func(sig *types.Signature) bool {
		return tupleLen(sig.Params()) == 0 &&
			tupleLen(sig.Results()) == 2 &&
			typeIsAny(sig.Results().At(0).Type()) &&
			typeIsError(sig.Results().At(1).Type())
	},
	reflectedUnmarshalYAMLHook: func(sig *types.Signature) bool {
		return tupleLen(sig.Params()) == 1 &&
			reflectedYAMLUnmarshalParamType(sig.Params().At(0).Type()) &&
			reflectedErrorOnlyResultSignature(sig)
	},
	"MarshalXML": func(sig *types.Signature) bool {
		return tupleLen(sig.Params()) == reflectedXMLHookArgs &&
			reflectedPointerToNamedType(
				sig.Params().At(0).Type(),
				reflectedEncodingXML,
				"Encoder",
			) &&
			namedTypeMatches(sig.Params().At(1).Type(), reflectedEncodingXML, "StartElement") &&
			reflectedErrorOnlyResultSignature(sig)
	},
	"UnmarshalXML": func(sig *types.Signature) bool {
		return tupleLen(sig.Params()) == reflectedXMLHookArgs &&
			reflectedPointerToNamedType(
				sig.Params().At(0).Type(),
				reflectedEncodingXML,
				"Decoder",
			) &&
			namedTypeMatches(sig.Params().At(1).Type(), reflectedEncodingXML, "StartElement") &&
			reflectedErrorOnlyResultSignature(sig)
	},
	"MarshalXMLAttr": func(sig *types.Signature) bool {
		return tupleLen(sig.Params()) == 1 &&
			namedTypeMatches(sig.Params().At(0).Type(), reflectedEncodingXML, "Name") &&
			tupleLen(sig.Results()) == 2 &&
			namedTypeMatches(sig.Results().At(0).Type(), reflectedEncodingXML, "Attr") &&
			typeIsError(sig.Results().At(1).Type())
	},
	"UnmarshalXMLAttr": func(sig *types.Signature) bool {
		return tupleLen(sig.Params()) == 1 &&
			namedTypeMatches(sig.Params().At(0).Type(), reflectedEncodingXML, "Attr") &&
			reflectedErrorOnlyResultSignature(sig)
	},
}

type reflectedCodecUse struct {
	tag     string
	hookTag string
}

func reflectedCodecUseForTag(tag string) reflectedCodecUse {
	return reflectedCodecUse{
		tag:     tag,
		hookTag: tag,
	}
}

func (codec reflectedPackageCodec) use(fn reflectedCodecFunc) reflectedCodecUse {
	hookTag := fn.hookTag
	if hookTag == "" {
		hookTag = codec.tag
	}

	return reflectedCodecUse{
		tag:     codec.tag,
		hookTag: hookTag,
	}
}

func reflectedDecodeFuncCodec(fn *types.Func) (reflectedCodecUse, bool) {
	// Only known reflection decoders set fields by name; local Decode funcs use normal edges.
	return reflectedFuncCodec(fn, func(codec reflectedPackageCodec) map[string]reflectedCodecFunc {
		return codec.decodeFuncs
	})
}

func reflectedDecodeTargetArgIndex(fn *types.Func, call *ast.CallExpr) int {
	return reflectedTargetArgIndex(
		fn,
		call,
		func(codec reflectedPackageCodec) map[string]reflectedCodecFunc {
			return codec.decodeFuncs
		},
	)
}

func reflectedMarshalFuncCodec(fn *types.Func) (reflectedCodecUse, bool) {
	return reflectedFuncCodec(fn, func(codec reflectedPackageCodec) map[string]reflectedCodecFunc {
		return codec.marshalFuncs
	})
}

func reflectedMarshalTargetArgIndex(fn *types.Func, call *ast.CallExpr) int {
	return reflectedTargetArgIndex(
		fn,
		call,
		func(codec reflectedPackageCodec) map[string]reflectedCodecFunc {
			return codec.marshalFuncs
		},
	)
}

func reflectedFuncCodec(
	fn *types.Func,
	funcs func(reflectedPackageCodec) map[string]reflectedCodecFunc,
) (reflectedCodecUse, bool) {
	codec, ok := reflectedFuncPackageCodec(fn)
	if !ok {
		return reflectedCodecUse{}, false
	}

	codecFunc, ok := funcs(codec)[fn.Name()]

	return codec.use(codecFunc), ok
}

func reflectedTargetArgIndex(
	fn *types.Func,
	call *ast.CallExpr,
	funcs func(reflectedPackageCodec) map[string]reflectedCodecFunc,
) int {
	codec, ok := reflectedFuncPackageCodec(fn)
	if !ok {
		return reflectedLastCallArgIndex(call)
	}

	codecFunc, ok := funcs(codec)[fn.Name()]
	if !ok || codecFunc.argIndex == reflectedLastArgIndex {
		return reflectedLastCallArgIndex(call)
	}

	return codecFunc.argIndex
}

func reflectedLastCallArgIndex(call *ast.CallExpr) int {
	if call == nil {
		return reflectedLastArgIndex
	}

	return len(call.Args) - 1
}

func reflectedFuncPackageCodec(fn *types.Func) (reflectedPackageCodec, bool) {
	if fn == nil || fn.Pkg() == nil {
		return reflectedPackageCodec{}, false
	}

	codec, ok := reflectedPackageCodecs[fn.Pkg().Path()]

	return codec, ok
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
func reflectedHookMethodSignature(fn *types.Func, hookTag string, name string) bool {
	if fn == nil {
		return false
	}

	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig == nil {
		return false
	}

	return reflectedHookSignatureMatchesName(sig, hookTag, name)
}

func reflectedHookSignatureMatchesName(sig *types.Signature, hookTag string, name string) bool {
	if hookTag == reflectedGoccyJSONTag && reflectedGoccyJSONContextHookSignature(sig, name) {
		return true
	}

	validator := reflectedHookSignatureValidators[name]
	if validator == nil {
		return false
	}

	return validator(sig)
}

func reflectedGoccyJSONContextHookSignature(sig *types.Signature, name string) bool {
	switch name {
	case reflectedMarshalJSONHook:
		return tupleLen(sig.Params()) == 1 &&
			namedTypeMatches(sig.Params().At(0).Type(), reflectedContext, "Context") &&
			tupleLen(sig.Results()) == 2 &&
			reflectedExactByteSliceType(sig.Results().At(0).Type()) &&
			typeIsError(sig.Results().At(1).Type())
	case reflectedUnmarshalJSONHook:
		return tupleLen(sig.Params()) == 2 &&
			namedTypeMatches(sig.Params().At(0).Type(), reflectedContext, "Context") &&
			reflectedExactByteSliceType(sig.Params().At(1).Type()) &&
			tupleLen(sig.Results()) == 1 &&
			typeIsError(sig.Results().At(0).Type())
	default:
		return false
	}
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
	codec, ok := reflectedCodecsByTag[reflectedHookNameTag(tag)]
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
	return reflectedCodecsByTag[reflectedHookNameTag(tag)].mapKeyFallbackField
}

func reflectedHookNameTag(tag string) string {
	if tag == reflectedGoccyJSONTag {
		return reflectedJSONTag
	}

	return tag
}
