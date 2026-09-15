package smells

import (
	"fmt"
	"go/types"
	"maps"
	"sort"
	"strconv"
	"strings"
)

type behaviorTypeEncoder struct {
	params   map[*types.TypeParam]string
	visiting map[types.Type]bool
}

func newBehaviorTypeEncoder(signature *types.Signature) *behaviorTypeEncoder {
	encoder := &behaviorTypeEncoder{
		params:   make(map[*types.TypeParam]string),
		visiting: make(map[types.Type]bool),
	}
	encoder.bindTypeParams("r", signature.RecvTypeParams())
	encoder.bindTypeParams("t", signature.TypeParams())

	return encoder
}

func (e *behaviorTypeEncoder) bindTypeParams(prefix string, params *types.TypeParamList) {
	if params == nil {
		return
	}

	for idx := range params.Len() {
		e.params[params.At(idx)] = prefix + strconv.Itoa(idx)
	}
}

func (e *behaviorTypeEncoder) signatureKey(signature *types.Signature) string {
	var out strings.Builder
	out.WriteString("recvparams(")
	e.writeTypeParamConstraints(&out, signature.RecvTypeParams())
	out.WriteString(")typeparams(")
	e.writeTypeParamConstraints(&out, signature.TypeParams())
	out.WriteString(")recv(")

	if signature.Recv() != nil {
		out.WriteString(e.typeKey(signature.Recv().Type()))
	}

	out.WriteString(")params(")
	e.writeTupleTypes(&out, signature.Params())
	out.WriteString(")results(")
	e.writeTupleTypes(&out, signature.Results())
	fmt.Fprintf(&out, ")variadic=%t", signature.Variadic())

	return out.String()
}

func (e *behaviorTypeEncoder) writeTypeParamConstraints(
	out *strings.Builder,
	params *types.TypeParamList,
) {
	if params == nil {
		return
	}

	for idx := range params.Len() {
		if idx > 0 {
			out.WriteByte(',')
		}

		param := params.At(idx)
		out.WriteString(e.params[param])
		out.WriteByte(':')
		out.WriteString(e.typeKey(param.Constraint()))
	}
}

func (e *behaviorTypeEncoder) writeTupleTypes(out *strings.Builder, tuple *types.Tuple) {
	if tuple == nil {
		return
	}

	for idx := range tuple.Len() {
		if idx > 0 {
			out.WriteByte(',')
		}

		out.WriteString(e.typeKey(tuple.At(idx).Type()))
	}
}

//nolint:cyclop // Go's closed type set needs one canonical case per type form.
func (e *behaviorTypeEncoder) typeKey(typ types.Type) string {
	if typ == nil {
		return "<none>"
	}

	typ = types.Unalias(typ)
	if e.visiting[typ] {
		return "<cycle>"
	}

	switch typ := typ.(type) {
	case *types.Basic:
		return typ.Name()
	case *types.Array:
		return "[" + strconv.FormatInt(typ.Len(), 10) + "]" + e.typeKey(typ.Elem())
	case *types.Slice:
		return "[]" + e.typeKey(typ.Elem())
	case *types.Pointer:
		return "*" + e.typeKey(typ.Elem())
	case *types.Map:
		return "map[" + e.typeKey(typ.Key()) + "]" + e.typeKey(typ.Elem())
	case *types.Chan:
		return "chan(" + strconv.Itoa(int(typ.Dir())) + "," + e.typeKey(typ.Elem()) + ")"
	case *types.Named:
		return e.namedTypeKey(typ)
	case *types.TypeParam:
		if name, ok := e.params[typ]; ok {
			return name
		}

		return "typeparam(" + e.typeKey(typ.Constraint()) + ")"
	case *types.Tuple:
		var out strings.Builder
		out.WriteString("tuple(")
		e.writeTupleTypes(&out, typ)
		out.WriteByte(')')

		return out.String()
	case *types.Signature:
		return e.nestedSignatureKey(typ)
	case *types.Struct:
		return e.structTypeKey(typ)
	case *types.Interface:
		return e.interfaceTypeKey(typ)
	case *types.Union:
		return e.unionTypeKey(typ)
	default:
		return types.TypeString(typ, behaviorPackagePath)
	}
}

func (e *behaviorTypeEncoder) namedTypeKey(typ *types.Named) string {
	obj := typ.Obj()
	pkgPath := "builtin"

	if obj.Pkg() != nil {
		pkgPath = obj.Pkg().Path()
	}

	var out strings.Builder
	out.WriteString(pkgPath)
	out.WriteByte('.')
	out.WriteString(obj.Name())

	if args := typ.TypeArgs(); args != nil && args.Len() > 0 {
		out.WriteByte('[')

		for idx := range args.Len() {
			if idx > 0 {
				out.WriteByte(',')
			}

			out.WriteString(e.typeKey(args.At(idx)))
		}

		out.WriteByte(']')
	}

	return out.String()
}

func (e *behaviorTypeEncoder) nestedSignatureKey(signature *types.Signature) string {
	nested := &behaviorTypeEncoder{
		params:   make(map[*types.TypeParam]string, len(e.params)),
		visiting: e.visiting,
	}
	maps.Copy(nested.params, e.params)

	nested.bindTypeParams("nr", signature.RecvTypeParams())
	nested.bindTypeParams("nt", signature.TypeParams())

	return "func(" + nested.signatureKey(signature) + ")"
}

func (e *behaviorTypeEncoder) structTypeKey(typ *types.Struct) string {
	e.visiting[typ] = true
	defer delete(e.visiting, typ)

	var out strings.Builder
	out.WriteString("struct{")

	for idx := range typ.NumFields() {
		if idx > 0 {
			out.WriteByte(';')
		}

		field := typ.Field(idx)
		out.WriteString(behaviorObjectPackage(field))
		out.WriteByte('.')
		out.WriteString(field.Name())
		fmt.Fprintf(&out, ":embedded=%t:", field.Embedded())
		out.WriteString(e.typeKey(field.Type()))
		out.WriteByte(':')
		out.WriteString(strconv.Quote(typ.Tag(idx)))
	}

	out.WriteByte('}')

	return out.String()
}

func (e *behaviorTypeEncoder) interfaceTypeKey(typ *types.Interface) string {
	typ = typ.Complete()
	e.visiting[typ] = true

	defer delete(e.visiting, typ)

	parts := make([]string, 0, typ.NumExplicitMethods()+typ.NumEmbeddeds()+1)
	for method := range typ.ExplicitMethods() {
		parts = append(parts, "method:"+behaviorObjectPackage(method)+"."+
			method.Name()+":"+e.typeKey(method.Type()))
	}

	for embedded := range typ.EmbeddedTypes() {
		parts = append(parts, "embed:"+e.typeKey(embedded))
	}

	if typ.IsComparable() {
		parts = append(parts, "comparable")
	}

	sort.Strings(parts)

	return "interface{" + strings.Join(parts, ";") + "}"
}

func (e *behaviorTypeEncoder) unionTypeKey(typ *types.Union) string {
	parts := make([]string, 0, typ.Len())
	for term := range typ.Terms() {
		prefix := ""

		if term.Tilde() {
			prefix = "~"
		}

		parts = append(parts, prefix+e.typeKey(term.Type()))
	}

	sort.Strings(parts)

	return strings.Join(parts, "|")
}

func behaviorPackagePath(pkg *types.Package) string {
	if pkg == nil {
		return ""
	}

	return pkg.Path()
}

func behaviorObjectPackage(obj types.Object) string {
	if obj == nil || obj.Pkg() == nil {
		return "builtin"
	}

	return obj.Pkg().Path()
}

func (e *behaviorTypeEncoder) objectKey(obj types.Object) string {
	if obj == nil {
		return "<nil>"
	}

	return behaviorObjectPackage(obj) + "." + obj.Name() + ":" + e.typeKey(obj.Type())
}
