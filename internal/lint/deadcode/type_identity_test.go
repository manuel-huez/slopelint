package deadcode

import (
	"go/token"
	"go/types"
	"testing"
)

func TestDeadCodeTypeStringUsesPackagePath(t *testing.T) {
	t.Parallel()

	first := namedStringType("example.com/first/model", "model", "Digest")
	second := namedStringType("example.com/second/model", "model", "Digest")

	if deadCodeTypeString(first) == deadCodeTypeString(second) {
		t.Fatalf("package-distinct types share key %q", deadCodeTypeString(first))
	}
}

func TestGenericDecodeEncodedInputTypeRequiresIOPackagePath(t *testing.T) {
	t.Parallel()

	fakeReader := types.NewNamed(
		types.NewTypeName(
			token.NoPos,
			types.NewPackage("example.com/fake/io", "io"),
			"Reader",
			nil,
		),
		types.NewInterfaceType(nil, nil).Complete(),
		nil,
	)

	if genericDecodeEncodedInputType(fakeReader) {
		t.Fatal("type named io.Reader outside package io treated as encoded input")
	}
}

func namedStringType(path, pkgName, typeName string) *types.Named {
	pkg := types.NewPackage(path, pkgName)

	return types.NewNamed(
		types.NewTypeName(token.NoPos, pkg, typeName, nil),
		types.Typ[types.String],
		nil,
	)
}
