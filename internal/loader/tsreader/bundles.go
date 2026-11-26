package tsreader

import (
	"encoding/json"
	"fmt"
	"slices"

	ir "github.com/parable-work/superschematic/ir"
)

// bundleMarker is @superschematic/db's Bundle<Parts, Fields>, the type a
// bundle's alias is (D103, step 5).
const bundleMarker = "Bundle"

// bundleUsage is how a bundle is declared, for its errors.
const bundleUsage = `a bundle is a type alias generic in the table and its settings, whose type is Bundle<Parts, Fields>: type Reservation<T, S extends { ... }> = Bundle<[Holds<T, { ... }>, ...], { status: Workflow<{ ... }> }>, Parts the behaviors the table implements, each its carrier with T and a config, and Fields the behaviors it anchors on fields, by field; a config reads a setting as S["name"]`

// bundleUse is a bundle a class implements (D103, step 5): the bundle and
// the settings the class gives it, as the IR records them, whether the
// registry declares it, and its parts, each a use of the class with the
// settings substituted into its config, marked with the bundle.
type bundleUse struct {
	ref        ir.BundleRef
	registered bool
	parts      []ir.BehaviorRef
	node       *astNode
}

// isBundleMarker reports whether node, a type alias's type, is
// @superschematic/db's Bundle<Parts, Fields>, resolved by its origin.
func (w *walker) isBundleMarker(node *astNode) bool {
	if node == nil || node.Kind != kindTypeReference {
		return false
	}
	id, ok := w.identityOf(node.AsTypeReferenceNode().TypeName)
	return ok && id.is(behaviorFieldPackage, bundleMarker)
}

// bundleAliasOf reports the bundle an implements-clause entry names: a type
// alias whose type is Bundle<Parts, Fields>, the core's from
// @superschematic/db or one the project declares.
func (w *walker) bundleAliasOf(t *astNode) (symbolIdentity, bool) {
	id, ok := w.identityOf(t.AsExpressionWithTypeArguments().Expression)
	if !ok || id.decl == nil || id.decl.Kind != kindTypeAliasDeclaration {
		return symbolIdentity{}, false
	}
	return id, w.isBundleMarker(id.decl.AsTypeAliasDeclaration().Type)
}

// bundleImplementsOf reads an implements-clause entry that names a bundle,
// `implements Reservation<Booking, { resource: "desk"; ... }>`, into its
// parts (D103, step 5). Its type arguments are the class itself and the
// settings, which the configReader reads through the checker into
// canonical JSON. Each part's config, written in the bundle's alias, is
// read through the checker with the bundle's settings parameter bound to
// the class's settings: a setting the config reads as S["name"] takes the
// class's value, and an object's member whose setting the class leaves out
// is left out. Each part is held to its behavior's config schema. A bundle
// @superschematic/db declares under a name the registry knows is
// recorded as the IR's bundle (ir.TypeDef.Bundles), which the loader holds
// to the registered declaration; one a project declares is TypeScript's
// alone, and only its parts are recorded. class is the class declaration
// whose clause holds t.
func (w *walker) bundleImplementsOf(class, t *astNode, id symbolIdentity) (bundleUse, *SchemaError) {
	name := className(class)
	alias := id.decl.AsTypeAliasDeclaration()
	var params []*astNode
	if alias.TypeParameters != nil {
		params = alias.TypeParameters.Nodes
	}
	if len(params) == 0 || len(params) > 2 {
		return bundleUse{}, errorAtNode(id.decl, "bundle %s: %s", id.name, bundleUsage)
	}
	var args []*astNode
	if list := t.AsExpressionWithTypeArguments().TypeArguments; list != nil {
		args = list.Nodes
	}
	usage := fmt.Sprintf("implements %s<%s, { ... }>", id.name, name)
	if len(params) == 1 {
		usage = fmt.Sprintf("implements %s<%s>", id.name, name)
	}
	if len(args) != len(params) {
		return bundleUse{}, errorAtNode(t, "type %s: bundle %s takes %d type arguments, the table and its settings: %s (D103)", name, id.name, len(params), usage)
	}
	selfOK := false
	if args[0].Kind == kindTypeReference {
		if ref, ok := w.identityOf(args[0].AsTypeReferenceNode().TypeName); ok && ref.decl == class {
			selfOK = true
		}
	}
	if !selfOK {
		return bundleUse{}, errorAtNode(args[0], "type %s: bundle %s's first type argument is the table itself, %s: %s (D103)", name, id.name, name, usage)
	}
	binding := &bundleBinding{name: id.name, table: params[0]}
	var settings json.RawMessage
	if len(params) == 2 {
		binding.settings = params[1]
		binding.argNode = args[1]
		binding.arg = w.checker.GetTypeFromTypeNode(args[1])
		reader := configReader{w: w, where: fmt.Sprintf("type %s: bundle %s settings", name, id.name), root: args[1]}
		value, serr := reader.value(binding.arg, args[1], "", 0)
		if serr != nil {
			return bundleUse{}, serr
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return bundleUse{}, errorAtNode(args[1], "type %s: bundle %s settings: %v", name, id.name, err)
		}
		settings = raw
	}
	refs := []ir.BundleRef{{Name: id.name, Settings: settings}}
	if err := ir.CanonicalizeBundles(refs); err != nil {
		return bundleUse{}, errorAtNode(t, "type %s: %v", name, err)
	}
	parts, serr := w.bundleParts(name, id.name, alias.Type, binding)
	if serr != nil {
		return bundleUse{}, serr
	}
	_, registered := w.reg.Bundle(id.name)
	registered = registered && id.pkg == behaviorFieldPackage
	return bundleUse{ref: refs[0], registered: registered, parts: parts, node: t}, nil
}

// bundleParts reads a bundle alias's Bundle<Parts, Fields> into its uses,
// in order: Parts, a tuple of the behaviors the table implements, each its
// carrier with the table and a config; then Fields, an object type of the
// behaviors anchored on fields, by field, each its field type with a
// config.
func (w *walker) bundleParts(class, bundle string, marker *astNode, binding *bundleBinding) ([]ir.BehaviorRef, *SchemaError) {
	ref := marker.AsTypeReferenceNode()
	var markerArgs []*astNode
	if ref.TypeArguments != nil {
		markerArgs = ref.TypeArguments.Nodes
	}
	if len(markerArgs) == 0 || len(markerArgs) > 2 {
		return nil, errorAtNode(marker, "bundle %s: %s", bundle, bundleUsage)
	}
	tuple := unwrapConfigTypeNode(markerArgs[0])
	if tuple == nil || tuple.Kind != kindTupleType {
		return nil, errorAtNode(markerArgs[0], "bundle %s: Bundle's Parts is a tuple of the behaviors the table implements, written in place; %s", bundle, bundleUsage)
	}
	var parts []ir.BehaviorRef
	var implemented []string
	for _, element := range tuple.Elements() {
		element = unwrapConfigTypeNode(element)
		behavior, carrierID, ok := w.behaviorCarrier(element)
		if !ok {
			return nil, errorAtNode(element, "bundle %s: a part is a behavior's carrier from %s with the table and its config, as Holds<T, { ... }>", bundle, behaviorFieldPackage)
		}
		if behavior.Value != "" {
			return nil, errorAtNode(element, "bundle %s: %s names a value field, so it is anchored on a field: write it in Bundle's Fields, as { status: %s<{ ... }> }", bundle, behavior.Name, carrierID.name)
		}
		if slices.Contains(implemented, behavior.Name) {
			return nil, errorAtNode(element, "bundle %s implements %s twice", bundle, behavior.Name)
		}
		implemented = append(implemented, behavior.Name)
		var typeArgs []*astNode
		if list := element.AsTypeReferenceNode().TypeArguments; list != nil {
			typeArgs = list.Nodes
		}
		if len(typeArgs) != 2 || binding.paramOf(w, typeArgs[0]) != binding.table {
			return nil, errorAtNode(element, "bundle %s: part %s takes two type arguments, the bundle's table and its config: %s<%s, { ... }>", bundle, behavior.Name, carrierID.name, binding.table.Name().Text())
		}
		part, serr := w.bundlePart(class, bundle, ir.BehaviorRef{Name: behavior.Name, Implements: true, Bundle: bundle}, typeArgs[1], binding)
		if serr != nil {
			return nil, serr
		}
		parts = append(parts, part)
	}
	if len(markerArgs) == 1 {
		return parts, nil
	}
	literal := unwrapConfigTypeNode(markerArgs[1])
	if literal == nil || literal.Kind != kindTypeLiteral {
		return nil, errorAtNode(markerArgs[1], "bundle %s: Bundle's Fields is an object type written in place, a field type for each field the bundle anchors a behavior on; %s", bundle, bundleUsage)
	}
	for _, m := range literal.AsTypeLiteralNode().Members.Nodes {
		if m.Kind != kindPropertySignature {
			return nil, errorAtNode(m, "bundle %s: Bundle's Fields holds only properties, each a field and its behavior's field type", bundle)
		}
		sig := m.AsPropertySignatureDeclaration()
		field := m.Name().Text()
		if sig.PostfixToken != nil && sig.PostfixToken.Kind == kindQuestionToken {
			return nil, errorAtNode(m, "bundle %s: field %s takes no ?: the behavior keeps it", bundle, field)
		}
		behavior, carrierID, ok := w.behaviorCarrier(unwrapConfigTypeNode(sig.Type))
		if !ok || behavior.Value == "" {
			return nil, errorAtNode(m, "bundle %s: field %s's type is a behavior's field type from %s, as Workflow<{ ... }>", bundle, field, behaviorFieldPackage)
		}
		if valueFromCreate(behavior) {
			return nil, errorAtNode(m, "bundle %s: field %s is a %s, whose value a create gives: the table declares it (D147)", bundle, field, behavior.Name)
		}
		var typeArgs []*astNode
		if list := unwrapConfigTypeNode(sig.Type).AsTypeReferenceNode().TypeArguments; list != nil {
			typeArgs = list.Nodes
		}
		if len(typeArgs) > 1 {
			return nil, errorAtNode(sig.Type, "bundle %s: field %s: %s<C> takes one type argument, its config", bundle, field, carrierID.name)
		}
		base := ir.BehaviorRef{Name: behavior.Name, Field: field, Bundle: bundle}
		if len(typeArgs) == 0 {
			parts = append(parts, base)
			continue
		}
		part, serr := w.bundlePart(class, bundle, base, typeArgs[0], binding)
		if serr != nil {
			return nil, serr
		}
		parts = append(parts, part)
	}
	return parts, nil
}

// bundlePart reads one part's config, node, with the bundle's settings
// bound, into base's config, canonical, its Workflow transitions written
// out and held to its behavior's config schema.
func (w *walker) bundlePart(class, bundle string, base ir.BehaviorRef, node *astNode, binding *bundleBinding) (ir.BehaviorRef, *SchemaError) {
	what := base.Name
	if base.FieldAnchored() {
		what = base.Name + " on field " + base.Field
	}
	where := fmt.Sprintf("type %s: bundle %s part %s config", class, bundle, what)
	reader := configReader{w: w, where: where, root: node, bundle: binding}
	value, serr := reader.value(w.checker.GetTypeFromTypeNode(node), node, "", 0)
	if serr != nil {
		return ir.BehaviorRef{}, serr
	}
	if _, absent := value.(absentSetting); absent {
		return ir.BehaviorRef{}, errorAtNode(node, "%s: the config is a setting the table leaves out", where)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return ir.BehaviorRef{}, errorAtNode(node, "%s: %v", where, err)
	}
	base.Config = raw
	refs := []ir.BehaviorRef{base}
	if err := ir.CanonicalizeBehaviors(refs); err != nil {
		return ir.BehaviorRef{}, errorAtNode(node, "type %s: bundle %s: %v", class, bundle, err)
	}
	if err := refs[0].ExpandTransitions(); err != nil {
		return ir.BehaviorRef{}, errorAtNode(node, "%s: %v", where, err)
	}
	behavior, _ := w.reg.Behavior(base.Name)
	if err := behavior.ValidateConfig(refs[0].Config); err != nil {
		return ir.BehaviorRef{}, errorAtNode(node, "type %s: bundle %s part %s: %s", class, bundle, what, err)
	}
	return refs[0], nil
}

// bundleBinding binds a bundle alias's type parameters while its parts'
// configs are read (D103, step 5): its table, which a config never reads,
// and its settings, which a config reads as S["name"] and the class's
// settings answer.
type bundleBinding struct {
	// name is the bundle's, for the errors.
	name string
	// table is the alias's first type parameter's declaration, T.
	table *astNode
	// settings is its second's, S; nil when the bundle takes no settings.
	settings *astNode
	// arg and argNode are the settings the class gives: their type and
	// the type argument as written.
	arg     *checkerType
	argNode *astNode
}

// paramOf is the declaration of the bundle's type parameter node refers
// to, T or S, or nil when it refers to neither.
func (b *bundleBinding) paramOf(w *walker, node *astNode) *astNode {
	node = unwrapConfigTypeNode(node)
	if node == nil || node.Kind != kindTypeReference {
		return nil
	}
	id, ok := w.identityOf(node.AsTypeReferenceNode().TypeName)
	if !ok || id.decl == nil {
		return nil
	}
	if id.decl == b.table || (b.settings != nil && id.decl == b.settings) {
		return id.decl
	}
	return nil
}

// absentSetting is the value of a setting the class leaves out: the
// object's member that reads it is left out with it.
type absentSetting struct{}

// setting reads node when it reads the bundle's settings, S or S["name"]
// (S["a"]["b"] in turn), as the class's value of it; handled is false for
// any other node. A reference to the table's type parameter is refused:
// a config names fields by setting or as written, not through T.
func (r *configReader) setting(node *astNode, at string, depth int) (any, bool, *SchemaError) {
	b := r.bundle
	switch node.Kind {
	case kindTypeReference:
		switch b.paramOf(r.w, node) {
		case nil:
			return nil, false, nil
		case b.table:
			return nil, true, r.fail(node, at, "bundle %s's config reads its table only as a carrier's first type argument; read a setting, S[\"name\"]", b.name)
		}
		v, serr := r.value(b.arg, b.argNode, at, depth+1)
		return v, true, serr
	case kindIndexedAccessType:
		t, member, present, ok, serr := r.settingMember(node, at)
		if !ok {
			return nil, false, nil
		}
		if serr != nil {
			return nil, true, serr
		}
		if !present {
			return absentSetting{}, true, nil
		}
		v, serr := r.value(t, member, at, depth+1)
		return v, true, serr
	}
	return nil, false, nil
}

// settingMember resolves node, S["name"] or a member of one, to the type
// the class's settings give it and the node it is written at; present is
// false when the class leaves it out, and ok false when node reads no
// setting.
func (r *configReader) settingMember(node *astNode, at string) (t *checkerType, member *astNode, present, ok bool, serr *SchemaError) {
	b := r.bundle
	access := node.AsIndexedAccessTypeNode()
	object := unwrapConfigTypeNode(access.ObjectType)
	var base *checkerType
	switch {
	case object != nil && object.Kind == kindTypeReference && b.settings != nil && b.paramOf(r.w, object) == b.settings:
		base = b.arg
	case object != nil && object.Kind == kindIndexedAccessType:
		inner, _, innerPresent, innerOK, innerErr := r.settingMember(object, at)
		if !innerOK || innerErr != nil || !innerPresent {
			return inner, nil, innerPresent, innerOK, innerErr
		}
		base = inner
	default:
		return nil, nil, false, false, nil
	}
	key, isString := literalValue(r.w.checker.GetTypeFromTypeNode(access.IndexType))
	name, _ := key.(string)
	if !isString || name == "" {
		return nil, nil, false, true, r.fail(access.IndexType, at, "bundle %s's config reads a setting by its name, S[\"name\"]", b.name)
	}
	c := r.w.checker
	prop := c.GetPropertyOfType(base, name)
	if prop == nil {
		return nil, nil, false, true, nil
	}
	pt, present := withoutUndefined(c, c.GetTypeOfSymbol(prop))
	if !present {
		return nil, nil, false, true, nil
	}
	if decl := prop.ValueDeclaration; decl != nil && decl.Kind == kindPropertySignature {
		member = decl.AsPropertySignatureDeclaration().Type
	}
	return pt, member, true, true, nil
}
