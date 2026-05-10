package deadcode

import (
	"go/types"
)

func namedDeadCodeType(typ types.Type) *types.Named {
	typ = dereferenceDeadCodeType(typ)
	if typ == nil {
		return nil
	}

	named, _ := types.Unalias(typ).(*types.Named)

	return named
}

func deadCodeStructType(typ types.Type) *types.Struct {
	typ = dereferenceDeadCodeType(typ)
	if typ == nil {
		return nil
	}

	if named, ok := types.Unalias(typ).(*types.Named); ok {
		typ = named.Underlying()
	}

	structType, _ := types.Unalias(typ).Underlying().(*types.Struct)

	return structType
}

func dereferenceDeadCodeType(typ types.Type) types.Type {
	if typ == nil {
		return nil
	}

	typ = types.Unalias(typ)
	if ptr, ok := typ.(*types.Pointer); ok {
		return types.Unalias(ptr.Elem())
	}

	return typ
}
