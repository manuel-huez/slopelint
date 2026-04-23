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
	facts map[string]fact
}

func newState() state {
	return state{facts: make(map[string]fact)}
}

func (s state) clone() state {
	out := state{facts: make(map[string]fact, len(s.facts))}
	for k, v := range s.facts {
		out.facts[k] = v.clone()
	}

	return out
}

func (s state) hash() string {
	if len(s.facts) == 0 {
		return "{}"
	}

	keys := make([]string, 0, len(s.facts))
	for k := range s.facts {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	var b strings.Builder

	for _, k := range keys {
		f := s.facts[k]
		b.WriteString(k)
		b.WriteByte(':')

		if f.exact != nil {
			b.WriteString("=")
			b.WriteString(f.exact.value.key())
		}

		if len(f.not) != 0 {
			notKeys := make([]string, 0, len(f.not))
			for nk := range f.not {
				notKeys = append(notKeys, nk)
			}

			sort.Strings(notKeys)
			b.WriteByte('!')

			for _, nk := range notKeys {
				b.WriteString(nk)
				b.WriteByte(',')
			}
		}

		b.WriteByte(';')
	}

	return b.String()
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
		return scalar{}, false
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
