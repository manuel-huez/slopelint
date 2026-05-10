package lint

import (
	"bytes"
	"fmt"
	"go/constant"
	"go/format"
	"go/token"
	"go/types"
	"sort"
	"strings"
	"unicode"
)

type triState uint8

const (
	triUnknown triState = iota
	triFalse
	triTrue
)

type scalarKind uint8

const (
	scalarInvalid scalarKind = iota
	scalarNil
	scalarBool
	scalarString
	scalarInt
)

type scalar struct {
	kind scalarKind
	text string
}

func (s scalar) key() string {
	return fmt.Sprintf("%d:%s", s.kind, s.text)
}

func (s scalar) String() string {
	switch s.kind {
	case scalarInvalid:
		return "<unknown>"
	case scalarNil:
		return "nil"
	case scalarBool:
		return s.text
	case scalarString:
		return fmt.Sprintf("%q", s.text)
	case scalarInt:
		return s.text
	default:
		return "<unknown>"
	}
}

type evidence struct {
	pos  token.Pos
	text string
}

type exactFact struct {
	value scalar
	why   evidence
}

type fact struct {
	exact *exactFact
	not   map[string]evidence
}

func (f fact) clone() fact {
	out := fact{}

	if f.exact != nil {
		copyExact := *f.exact
		out.exact = &copyExact
	}

	if len(f.not) != 0 {
		out.not = make(map[string]evidence, len(f.not))
		for k, v := range f.not {
			out.not[k] = v
		}
	}

	return out
}

func (f fact) empty() bool {
	return f.exact == nil && len(f.not) == 0
}

type state struct {
	facts    map[string]fact
	aliases  map[string]map[string]struct{}
	bindings map[string]resultBinding
}

func newState() state {
	return state{
		facts:    make(map[string]fact),
		aliases:  make(map[string]map[string]struct{}),
		bindings: make(map[string]resultBinding),
	}
}

func (s state) clone() state {
	out := state{
		facts:    make(map[string]fact, len(s.facts)),
		aliases:  make(map[string]map[string]struct{}, len(s.aliases)),
		bindings: make(map[string]resultBinding, len(s.bindings)),
	}
	for k, v := range s.facts {
		out.facts[k] = v.clone()
	}

	for key, peers := range s.aliases {
		outPeers := make(map[string]struct{}, len(peers))
		for peer := range peers {
			outPeers[peer] = struct{}{}
		}

		out.aliases[key] = outPeers
	}

	for key, binding := range s.bindings {
		out.bindings[key] = binding.clone()
	}

	return out
}

func (s state) hash() string {
	var b strings.Builder

	if len(s.facts) == 0 {
		b.WriteString("{}")
	} else {
		appendFactsHash(&b, s.facts)
	}

	if len(s.aliases) != 0 {
		b.WriteByte('|')

		for _, edge := range sortedAliasEdges(s.aliases) {
			b.WriteString(edge)
			b.WriteByte(';')
		}
	}

	if len(s.bindings) != 0 {
		b.WriteByte('|')

		for _, key := range sortedBindingKeys(s.bindings) {
			b.WriteString(key)
			b.WriteByte(':')
			b.WriteString(bindingHash(s.bindings[key]))
			b.WriteByte(';')
		}
	}

	return b.String()
}

func appendFactsHash(b *strings.Builder, facts map[string]fact) {
	for _, key := range sortedFactKeys(facts) {
		appendFactHash(b, key, facts[key])
	}
}

func sortedFactKeys(facts map[string]fact) []string {
	keys := make([]string, 0, len(facts))
	for key := range facts {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	return keys
}

func appendFactHash(b *strings.Builder, key string, f fact) {
	b.WriteString(key)
	b.WriteByte(':')

	if f.exact != nil {
		b.WriteString("=")
		b.WriteString(f.exact.value.key())
	}

	if len(f.not) != 0 {
		b.WriteByte('!')

		for _, notKey := range sortedEvidenceKeys(f.not) {
			b.WriteString(notKey)
			b.WriteByte(',')
		}
	}

	b.WriteByte(';')
}

func sortedEvidenceKeys(values map[string]evidence) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	return keys
}

func sortedAliasEdges(aliases map[string]map[string]struct{}) []string {
	edges := make([]string, 0)
	seen := make(map[string]struct{})

	for key, peers := range aliases {
		for peer := range peers {
			edge := aliasEdgeName(key, peer)
			if _, ok := seen[edge]; ok {
				continue
			}

			seen[edge] = struct{}{}
			edges = append(edges, edge)
		}
	}

	sort.Strings(edges)

	return edges
}

func aliasEdgeName(left, right string) string {
	if left > right {
		left, right = right, left
	}

	return left + "=" + right
}

type symbol struct {
	key  string
	root string
	name string
	typ  types.Type
}

func symbolForObject(obj types.Object) symbol {
	pkgPath := ""
	if obj.Pkg() != nil {
		pkgPath = obj.Pkg().Path()
	}

	root := fmt.Sprintf("%s:%s:%d", pkgPath, obj.Name(), obj.Pos())

	return symbol{
		key:  root,
		root: root,
		name: obj.Name(),
		typ:  obj.Type(),
	}
}

func (s symbol) child(field string, typ types.Type) symbol {
	return symbol{
		key:  s.key + "|" + field,
		root: s.root,
		name: s.name + "." + field,
		typ:  typ,
	}
}

func isSameOrChild(key, prefix string) bool {
	return key == prefix || strings.HasPrefix(key, prefix+"|")
}

func renderNode(fset *token.FileSet, node any) string {
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, node); err != nil {
		return "<expr>"
	}

	return buf.String()
}

func scalarFromConstantValue(v constant.Value) (scalar, bool) {
	if v == nil {
		return scalar{}, false
	}

	switch v.Kind() {
	case constant.Unknown:
		return scalar{}, false
	case constant.Bool:
		if constant.BoolVal(v) {
			return scalar{kind: scalarBool, text: "true"}, true
		}

		return scalar{kind: scalarBool, text: "false"}, true
	case constant.String:
		return scalar{kind: scalarString, text: constant.StringVal(v)}, true
	case constant.Int:
		return scalar{kind: scalarInt, text: v.ExactString()}, true
	case constant.Float, constant.Complex:
		return scalar{kind: scalarInvalid}, false
	default:
		return scalar{}, false
	}
}

func zeroScalarOfType(t types.Type) (scalar, bool) {
	t = types.Unalias(t)
	switch tt := t.(type) {
	case *types.Basic:
		info := tt.Info()
		switch {
		case info&types.IsBoolean != 0:
			return scalar{kind: scalarBool, text: "false"}, true
		case info&types.IsString != 0:
			return scalar{kind: scalarString, text: ""}, true
		case info&types.IsInteger != 0:
			return scalar{kind: scalarInt, text: "0"}, true
		default:
			return scalar{}, false
		}
	case *types.Pointer, *types.Slice, *types.Map, *types.Chan, *types.Signature:
		return scalar{kind: scalarNil, text: "nil"}, true
	case *types.Interface:
		return scalar{kind: scalarNil, text: "nil"}, true
	case *types.Named:
		return zeroScalarOfType(tt.Underlying())
	default:
		return scalar{}, false
	}
}

func isBoolType(t types.Type) bool {
	t = types.Unalias(t)
	if named, ok := t.(*types.Named); ok {
		t = named.Underlying()
	}

	basic, ok := t.(*types.Basic)

	return ok && basic.Info()&types.IsBoolean != 0
}

func isPointerLike(t types.Type) bool {
	t = types.Unalias(t)
	switch tt := t.(type) {
	case *types.Named:
		return isPointerLike(tt.Underlying())
	case *types.Pointer, *types.Map, *types.Slice, *types.Interface, *types.Signature, *types.Chan:
		return true
	default:
		return false
	}
}

func isIsPredicateName(name string) bool {
	if !strings.HasPrefix(name, "Is") || len(name) <= len("Is") {
		return false
	}

	for _, ch := range name[len("Is"):] {
		return ch == '_' || unicode.IsUpper(ch) || unicode.IsDigit(ch)
	}

	return false
}
