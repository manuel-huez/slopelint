package smells

import (
	"cmp"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"slices"
)

const fieldLeakExampleLimit = 3

type fieldLeakCheck struct {
	ruleEnv

	tangleOf map[*types.TypeName]int
}

type guardedStruct struct {
	owner   *types.TypeName
	fields  []*guardedField
	working bool
}

type guardedField struct {
	owner       *guardedStruct
	field       *types.Var
	seenOutside bool
	leaks       []fieldLeakSite
}

type fieldLeakSite struct {
	pos     token.Pos
	caller  string
	through string
}

type fieldAccessor struct {
	owner  *types.TypeName
	field  *guardedField
	name   string
	setter bool
}

func (l fieldLeakCheck) check() {
	structs, fieldByVar := l.collectGuardedStructs()
	if len(structs) == 0 {
		return
	}

	accessors := l.collectFieldAccessors(fieldByVar)
	l.scanFieldUses(fieldByVar, accessors)
	l.reportFieldLeaks(structs)
}

func (l fieldLeakCheck) collectGuardedStructs() ([]*guardedStruct, map[*types.Var]*guardedField) {
	var structs []*guardedStruct

	fieldByVar := make(map[*types.Var]*guardedField)

	for _, file := range l.pkg.ProductionFiles {
		if ast.IsGenerated(file) {
			continue
		}

		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}

			for _, raw := range gen.Specs {
				spec, ok := raw.(*ast.TypeSpec)
				if !ok {
					continue
				}

				entry := l.buildGuardedStruct(spec, fieldByVar)
				if entry != nil {
					structs = append(structs, entry)
				}
			}
		}
	}

	return structs, fieldByVar
}

func (l fieldLeakCheck) buildGuardedStruct(
	spec *ast.TypeSpec,
	fieldByVar map[*types.Var]*guardedField,
) *guardedStruct {
	obj, ok := l.pkg.TypesInfo.Defs[spec.Name].(*types.TypeName)
	if !ok || obj.IsAlias() {
		return nil
	}

	st, ok := obj.Type().Underlying().(*types.Struct)
	if !ok {
		return nil
	}

	entry := &guardedStruct{owner: obj}

	for field := range st.Fields() {
		if field.Name() == "_" {
			continue
		}

		tracked := &guardedField{owner: entry, field: field.Origin()}
		entry.fields = append(entry.fields, tracked)
		fieldByVar[tracked.field] = tracked
	}

	if len(entry.fields) == 0 {
		return nil
	}

	return entry
}

func (l fieldLeakCheck) collectFieldAccessors(
	fieldByVar map[*types.Var]*guardedField,
) map[*types.Func]fieldAccessor {
	accessors := make(map[*types.Func]fieldAccessor)

	l.forEachHandwrittenProductionFunc(func(fn *ast.FuncDecl) {
		if fn.Recv == nil || fn.Body == nil || len(fn.Body.List) != 1 {
			return
		}

		obj, ok := funcDeclObject(l.pkg.TypesInfo, fn)
		if !ok {
			return
		}

		owner := methodOwnerTypeName(l.pkg.TypesPkg, obj.Signature())
		receiver := funcReceiverObject(l.pkg.TypesInfo, fn.Recv)

		if owner == nil || receiver == nil {
			return
		}

		if acc, ok := l.matchGetterAccessor(fn, obj, owner, receiver, fieldByVar); ok {
			accessors[obj.Origin()] = acc
			return
		}

		if acc, ok := l.matchSetterAccessor(fn, obj, owner, receiver, fieldByVar); ok {
			accessors[obj.Origin()] = acc
		}
	})

	return accessors
}

func (l fieldLeakCheck) matchGetterAccessor(
	fn *ast.FuncDecl,
	obj *types.Func,
	owner *types.TypeName,
	receiver *types.Var,
	fieldByVar map[*types.Var]*guardedField,
) (fieldAccessor, bool) {
	sig := obj.Signature()
	if sig.Params().Len() != 0 || sig.Results().Len() != 1 {
		return fieldAccessor{}, false
	}

	ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return fieldAccessor{}, false
	}

	expr := ast.Unparen(ret.Results[0])
	if unary, ok := expr.(*ast.UnaryExpr); ok && unary.Op == token.AND {
		expr = ast.Unparen(unary.X)
	}

	tracked := l.directReceiverField(expr, receiver, owner, fieldByVar)
	if tracked == nil {
		return fieldAccessor{}, false
	}

	return fieldAccessor{owner: owner, field: tracked, name: fn.Name.Name}, true
}

func (l fieldLeakCheck) matchSetterAccessor(
	fn *ast.FuncDecl,
	obj *types.Func,
	owner *types.TypeName,
	receiver *types.Var,
	fieldByVar map[*types.Var]*guardedField,
) (fieldAccessor, bool) {
	sig := obj.Signature()
	if sig.Params().Len() != 1 || sig.Results().Len() != 0 {
		return fieldAccessor{}, false
	}

	assign, ok := fn.Body.List[0].(*ast.AssignStmt)
	if !ok || assign.Tok != token.ASSIGN || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
		return fieldAccessor{}, false
	}

	tracked := l.directReceiverField(assign.Lhs[0], receiver, owner, fieldByVar)
	if tracked == nil || !identRefersToObject(l.pkg.TypesInfo, assign.Rhs[0], sig.Params().At(0)) {
		return fieldAccessor{}, false
	}

	return fieldAccessor{owner: owner, field: tracked, name: fn.Name.Name, setter: true}, true
}

func (l fieldLeakCheck) directReceiverField(
	expr ast.Expr,
	receiver *types.Var,
	owner *types.TypeName,
	fieldByVar map[*types.Var]*guardedField,
) *guardedField {
	sel, ok := ast.Unparen(expr).(*ast.SelectorExpr)
	if !ok || !identRefersToObject(l.pkg.TypesInfo, sel.X, receiver) {
		return nil
	}

	selection := l.pkg.TypesInfo.Selections[sel]
	if selection == nil || selection.Kind() != types.FieldVal {
		return nil
	}

	fieldVar, ok := selection.Obj().(*types.Var)
	if !ok {
		return nil
	}

	tracked := fieldByVar[fieldVar.Origin()]
	if tracked == nil || tracked.owner.owner != owner {
		return nil
	}

	return tracked
}

func (l fieldLeakCheck) scanFieldUses(
	fieldByVar map[*types.Var]*guardedField,
	accessors map[*types.Func]fieldAccessor,
) {
	for _, file := range l.pkg.ProductionFiles {
		if ast.IsGenerated(file) {
			continue
		}

		for _, decl := range file.Decls {
			l.scanDeclFieldUses(decl, fieldByVar, accessors)
		}
	}
}

type fieldCallerContext struct {
	owner       *types.TypeName
	constructed *types.TypeName
	freshLocals map[*types.Var]token.Pos
	name        string
	isAccessor  bool
}

func (l fieldLeakCheck) callerContext(
	decl ast.Decl,
	accessors map[*types.Func]fieldAccessor,
) fieldCallerContext {
	ctx := fieldCallerContext{name: "package init"}

	fn, ok := decl.(*ast.FuncDecl)
	if !ok {
		return ctx
	}

	ctx.name = fn.Name.Name
	if fn.Body != nil {
		ctx.freshLocals = collectFreshLocalStructs(fn.Body, l.pkg.TypesInfo, l.pkg.TypesPkg)
	}

	obj, ok := funcDeclObject(l.pkg.TypesInfo, fn)
	if !ok {
		return ctx
	}

	ctx.owner = methodOwnerTypeName(l.pkg.TypesPkg, obj.Signature())
	ctx.constructed = constructorTypeName(l.pkg.TypesPkg, obj.Signature())
	_, ctx.isAccessor = accessors[obj.Origin()]

	if ctx.owner != nil {
		ctx.name = ctx.owner.Name() + "." + fn.Name.Name
	}

	return ctx
}

// collectFreshLocalStructs maps each local variable initialized from a fresh
// package struct allocation (T{}, &T{}, new(T), or zero-value var v T) to the
// earliest source position where it is called, returned, stored, or captured.
// Field accesses before that position are struct initialization, not leaks.
func collectFreshLocalStructs(
	body *ast.BlockStmt,
	info *types.Info,
	pkg *types.Package,
) map[*types.Var]token.Pos {
	fresh := make(map[*types.Var]token.Pos)

	ast.Inspect(body, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.AssignStmt:
			recordFreshAssigns(node, info, pkg, fresh)
		case *ast.ValueSpec:
			recordFreshValueSpecs(node, info, pkg, fresh)
		}

		return true
	})

	if len(fresh) == 0 {
		return nil
	}

	recordFreshFirstEscapes(body, info, fresh)

	return fresh
}

func recordFreshAssigns(
	stmt *ast.AssignStmt,
	info *types.Info,
	pkg *types.Package,
	fresh map[*types.Var]token.Pos,
) {
	if stmt.Tok != token.DEFINE || len(stmt.Lhs) != len(stmt.Rhs) {
		return
	}

	for idx, lhs := range stmt.Lhs {
		ident, ok := lhs.(*ast.Ident)
		if !ok {
			continue
		}

		v, ok := info.Defs[ident].(*types.Var)
		if ok && isFreshStructExpr(stmt.Rhs[idx], info, pkg) {
			fresh[v] = token.NoPos
		}
	}
}

func recordFreshValueSpecs(
	spec *ast.ValueSpec,
	info *types.Info,
	pkg *types.Package,
	fresh map[*types.Var]token.Pos,
) {
	for idx, name := range spec.Names {
		v, ok := info.Defs[name].(*types.Var)
		if !ok {
			continue
		}

		if len(spec.Values) == 0 {
			if namedPackageType(pkg, v.Type()) != nil {
				fresh[v] = token.NoPos
			}

			continue
		}

		if idx < len(spec.Values) && isFreshStructExpr(spec.Values[idx], info, pkg) {
			fresh[v] = token.NoPos
		}
	}
}

func isFreshStructExpr(expr ast.Expr, info *types.Info, pkg *types.Package) bool {
	expr = ast.Unparen(expr)
	if unary, ok := expr.(*ast.UnaryExpr); ok && unary.Op == token.AND {
		expr = ast.Unparen(unary.X)
	}

	switch expr := expr.(type) {
	case *ast.CompositeLit:
		return namedPackageType(pkg, info.TypeOf(expr)) != nil
	case *ast.CallExpr:
		return isBuiltinCall(info, expr, "new") && namedPackageType(pkg, info.TypeOf(expr)) != nil
	default:
		return false
	}
}

func recordFreshFirstEscapes(
	body *ast.BlockStmt,
	info *types.Info,
	fresh map[*types.Var]token.Pos,
) {
	ast.PreorderStack(body, nil, func(node ast.Node, stack []ast.Node) bool {
		ident, ok := node.(*ast.Ident)
		if !ok {
			return true
		}

		v, ok := info.Uses[ident].(*types.Var)
		if !ok {
			return true
		}

		prev, tracked := fresh[v]
		if !tracked || isDirectFieldSelectionBase(ident, stack, info) {
			return true
		}

		if prev == token.NoPos || ident.Pos() < prev {
			fresh[v] = ident.Pos()
		}

		return true
	})
}

func isDirectFieldSelectionBase(ident *ast.Ident, stack []ast.Node, info *types.Info) bool {
	if len(stack) == 0 {
		return false
	}

	sel, ok := stack[len(stack)-1].(*ast.SelectorExpr)
	if !ok || sel.X != ident {
		return false
	}

	selection := info.Selections[sel]

	return selection != nil && selection.Kind() == types.FieldVal
}

func (l fieldLeakCheck) isLocalStructInit(sel *ast.SelectorExpr, ctx fieldCallerContext) bool {
	if len(ctx.freshLocals) == 0 {
		return false
	}

	ident, ok := ast.Unparen(sel.X).(*ast.Ident)
	if !ok {
		return false
	}

	v, ok := l.pkg.TypesInfo.Uses[ident].(*types.Var)
	if !ok {
		return false
	}

	firstEscape, tracked := ctx.freshLocals[v]
	if !tracked {
		return false
	}

	return firstEscape == token.NoPos || sel.Pos() < firstEscape
}

func (l fieldLeakCheck) scanDeclFieldUses(
	decl ast.Decl,
	fieldByVar map[*types.Var]*guardedField,
	accessors map[*types.Func]fieldAccessor,
) {
	ctx := l.callerContext(decl, accessors)

	ast.PreorderStack(decl, nil, func(node ast.Node, stack []ast.Node) bool {
		switch node := node.(type) {
		case *ast.SelectorExpr:
			l.recordSelectorFieldUse(node, stack, ctx, fieldByVar)
		case *ast.CallExpr:
			l.recordAccessorFieldUse(node, stack, ctx, accessors)
		}

		return true
	})
}

func (l fieldLeakCheck) recordSelectorFieldUse(
	sel *ast.SelectorExpr,
	stack []ast.Node,
	ctx fieldCallerContext,
	fieldByVar map[*types.Var]*guardedField,
) {
	selection := l.pkg.TypesInfo.Selections[sel]
	if selection == nil || selection.Kind() != types.FieldVal {
		return
	}

	fieldVar, ok := selection.Obj().(*types.Var)
	if !ok {
		return
	}

	tracked := fieldByVar[fieldVar.Origin()]
	if tracked == nil {
		return
	}

	owner := tracked.owner.owner
	if l.isFieldInsider(ctx, owner) || l.isLocalStructInit(sel, ctx) {
		if ctx.owner == owner && !ctx.isAccessor {
			tracked.owner.working = true
		}

		return
	}

	tracked.seenOutside = true
	if !tracked.field.Exported() && exprWorksOnField(sel, stack, l.pkg.TypesInfo) {
		tracked.leaks = append(tracked.leaks, fieldLeakSite{pos: sel.Sel.Pos(), caller: ctx.name})
	}
}

func (l fieldLeakCheck) recordAccessorFieldUse(
	call *ast.CallExpr,
	stack []ast.Node,
	ctx fieldCallerContext,
	accessors map[*types.Func]fieldAccessor,
) {
	callee, _, ok := calledFunc(l.pkg.TypesInfo, call)
	if !ok {
		return
	}

	acc, ok := accessors[callee.Origin()]
	if !ok || l.isFieldInsider(ctx, acc.owner) {
		return
	}

	acc.field.seenOutside = true
	if !acc.field.field.Exported() &&
		(acc.setter || exprWorksOnField(call, stack, l.pkg.TypesInfo)) {
		acc.field.leaks = append(acc.field.leaks, fieldLeakSite{
			pos:     call.Pos(),
			caller:  ctx.name,
			through: acc.name,
		})
	}
}

func (l fieldLeakCheck) isFieldInsider(ctx fieldCallerContext, owner *types.TypeName) bool {
	if ctx.owner == owner || ctx.constructed == owner {
		return true
	}

	if ctx.owner == nil {
		return false
	}

	tangle := l.tangleOf[owner]

	return tangle != 0 && l.tangleOf[ctx.owner] == tangle
}

// exprWorksOnField reports whether an outer expression mutates, borrows,
// indexes, ranges over, calls a method on, or reaches into unexported parts of
// expr rather than just reading a value or an exported read-only sub-field.
func exprWorksOnField(expr ast.Expr, stack []ast.Node, info *types.Info) bool {
	child, parentIdx := unwrapParentParens(expr, stack)
	if parentIdx < 0 {
		return false
	}

	parent := stack[parentIdx]
	if parentMutatesChild(parent, child, info) || parentIndexesChild(parent, child) {
		return true
	}

	if sel, ok := parent.(*ast.SelectorExpr); ok && sel.X == child {
		return selectorWorksOnField(sel, stack[:parentIdx], info)
	}

	star, ok := parent.(*ast.StarExpr)

	return ok && star.X == child && exprWorksOnField(star, stack[:parentIdx], info)
}

// selectorWorksOnField flags method calls and unexported sub-field accesses on
// a struct field, while allowing pure reads of exported sub-fields (such as
// shared context pointers like l.pkg.TypesInfo or q.info.Uses[id]).
func selectorWorksOnField(sel *ast.SelectorExpr, stack []ast.Node, info *types.Info) bool {
	selection := info.Selections[sel]
	if selection == nil {
		return false
	}

	if selection.Kind() != types.FieldVal {
		return true
	}

	subField, ok := selection.Obj().(*types.Var)
	if !ok || !subField.Exported() {
		return true
	}

	return exprIsMutated(sel, stack, info)
}

func exprIsMutated(expr ast.Expr, stack []ast.Node, info *types.Info) bool {
	child, parentIdx := unwrapParentParens(expr, stack)
	if parentIdx < 0 {
		return false
	}

	parent := stack[parentIdx]
	if parentMutatesChild(parent, child, info) {
		return true
	}

	switch parent := parent.(type) {
	case *ast.IndexExpr:
		return parent.X == child && exprIsMutated(parent, stack[:parentIdx], info)
	case *ast.StarExpr:
		return parent.X == child && exprIsMutated(parent, stack[:parentIdx], info)
	case *ast.SelectorExpr:
		return parent.X == child && exprIsMutated(parent, stack[:parentIdx], info)
	default:
		return false
	}
}

func parentMutatesChild(parent ast.Node, child ast.Expr, info *types.Info) bool {
	switch parent := parent.(type) {
	case *ast.AssignStmt:
		return slices.Contains(parent.Lhs, child)
	case *ast.IncDecStmt:
		return parent.X == child
	case *ast.UnaryExpr:
		return parent.X == child && (parent.Op == token.AND || parent.Op == token.ARROW)
	case *ast.SendStmt:
		return parent.Chan == child
	case *ast.CallExpr:
		return mutatingBuiltinArg(info, parent, child)
	default:
		return false
	}
}

func parentIndexesChild(parent ast.Node, child ast.Expr) bool {
	switch parent := parent.(type) {
	case *ast.IndexExpr:
		return parent.X == child
	case *ast.IndexListExpr:
		return parent.X == child
	case *ast.SliceExpr:
		return parent.X == child
	case *ast.RangeStmt:
		return parent.X == child
	default:
		return false
	}
}

func unwrapParentParens(expr ast.Expr, stack []ast.Node) (ast.Expr, int) {
	child := expr
	idx := len(stack) - 1

	for idx >= 0 {
		paren, ok := stack[idx].(*ast.ParenExpr)
		if !ok {
			break
		}

		child = paren
		idx--
	}

	return child, idx
}

func mutatingBuiltinArg(info *types.Info, call *ast.CallExpr, arg ast.Expr) bool {
	ident, ok := ast.Unparen(call.Fun).(*ast.Ident)
	if !ok {
		return false
	}

	if _, ok := info.ObjectOf(ident).(*types.Builtin); !ok || len(call.Args) == 0 {
		return false
	}

	switch ident.Name {
	case "append", "delete", "clear", "close", "copy":
		return call.Args[0] == arg
	default:
		return false
	}
}

func (l fieldLeakCheck) reportFieldLeaks(structs []*guardedStruct) {
	for _, entry := range structs {
		if !entry.working || !structKeepsPrivateField(entry) {
			continue
		}

		for _, field := range entry.fields {
			if len(field.leaks) == 0 {
				continue
			}

			l.reportFieldLeak(entry.owner.Name(), field)
		}
	}
}

// structKeepsPrivateField reports whether entry keeps at least one unexported
// non-embedded field completely internal. Exported fields are public data, and
// anonymous embedded structs promote their fields rather than hiding state.
func structKeepsPrivateField(entry *guardedStruct) bool {
	return slices.ContainsFunc(entry.fields, func(field *guardedField) bool {
		return !field.field.Exported() && !field.field.Anonymous() && !field.seenOutside
	})
}

func (l fieldLeakCheck) reportFieldLeak(typeName string, field *guardedField) {
	slices.SortFunc(field.leaks, func(a, b fieldLeakSite) int {
		return cmp.Compare(a.pos, b.pos)
	})

	var (
		unique  []fieldLeakSite
		callers []string
		seenPos = make(map[string]struct{})
		seenFn  = make(map[string]struct{})
	)

	for _, site := range field.leaks {
		key := l.positionText(site.pos)
		if _, ok := seenPos[key]; ok {
			continue
		}

		seenPos[key] = struct{}{}

		unique = append(unique, site)

		label := site.caller
		if site.through != "" {
			label += " via " + site.through
		}

		if _, ok := seenFn[label]; !ok {
			seenFn[label] = struct{}{}
			callers = append(callers, label)
		}
	}

	placeWord := "places"
	if len(unique) == 1 {
		placeWord = "place"
	}

	l.report(
		unique[0].pos,
		"field_leak",
		fmt.Sprintf(
			"field %q is worked on outside %s in %d %s (%s)",
			typeName+"."+field.field.Name(),
			typeName,
			len(unique),
			placeWord,
			formatListedNames(callers, fieldLeakExampleLimit),
		),
	)
}
