package tsreader

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// behaviorFieldPackage is the authoring package whose exports a field
// takes as a behavior (D103): each is a registered behavior's name, as
// Workflow<C> is.
const behaviorFieldPackage = "@superschematic/db"

// fieldUse is a behavior a class composes on one of its fields (D103): the
// use as the IR records it and the property that declares it.
type fieldUse struct {
	ref  ir.BehaviorRef
	node *astNode
}

// behaviorCarrier reports the registered behavior a type reference names
// when its symbol is one of @superschematic/db's behavior field types. It
// resolves the symbol's origin, as Relation resolves, so a renamed import
// or a re-export still names the behavior and a schema's own type of the
// same name does not.
func (w *walker) behaviorCarrier(node *astNode) (registry.Behavior, symbolIdentity, bool) {
	if node == nil || node.Kind != kindTypeReference {
		return registry.Behavior{}, symbolIdentity{}, false
	}
	id, ok := w.identityOf(node.AsTypeReferenceNode().TypeName)
	if !ok {
		return registry.Behavior{}, symbolIdentity{}, false
	}
	behavior, ok := w.behaviorOfIdentity(id)
	return behavior, id, ok
}

// behaviorOfIdentity reports the registered behavior a resolved symbol is
// the field type of.
func (w *walker) behaviorOfIdentity(id symbolIdentity) (registry.Behavior, bool) {
	if id.pkg != behaviorFieldPackage {
		return registry.Behavior{}, false
	}
	return w.reg.Behavior(id.name)
}

// behaviorFieldOf reads a property whose type is a behavior's field type,
// `status: Workflow<{ ... }>`, into the use it anchors on that field
// (D103): the behavior's name, the field's name and the config its type
// argument gives, read through the checker into canonical JSON and held
// to the behavior's config schema as @behavior's is. handled is false for
// any other property, which the caller reads as a field. The field takes
// no FieldDef here: the loader adds each of the behavior's declared fields
// as a column, the anchor field among them, before the verification
// pass's field checks, so every authoring form gets the same fields
// (internal/loader/behaviorfields).
// class names the class for the errors.
func (w *walker) behaviorFieldOf(class string, member *astNode) (use fieldUse, handled bool, serr *SchemaError) {
	prop := member.AsPropertyDeclaration()
	behavior, id, ok := w.behaviorCarrier(prop.Type)
	if !ok {
		return fieldUse{}, false, nil
	}
	field := member.Name().Text()
	where := fmt.Sprintf("type %s: field %s", class, field)
	if behavior.Value == "" {
		return fieldUse{}, true, errorAtNode(prop.Type, "%s: behavior %s names no value field in its declaration, so it is not anchored on a field; compose it on the type (D103)", where, behavior.Name)
	}
	if prop.PostfixToken != nil && prop.PostfixToken.Kind == kindQuestionToken {
		return fieldUse{}, true, errorAtNode(member, "%s is a %s, which is never optional: the behavior sets it when a row is created; write %s: %s<...> (D103)", where, behavior.Name, field, id.name)
	}
	if decorators := member.Decorators(); len(decorators) > 0 {
		return fieldUse{}, true, errorAtNode(decorators[0], "%s is a %s, which takes no decorator: the behavior declares the field (D103)", where, behavior.Name)
	}
	var config json.RawMessage
	at := prop.Type
	if args := prop.Type.AsTypeReferenceNode().TypeArguments; args != nil && len(args.Nodes) > 0 {
		if len(args.Nodes) > 1 {
			return fieldUse{}, true, errorAtNode(prop.Type, "%s: %s<C> takes one type argument, its config", where, id.name)
		}
		at = args.Nodes[0]
		reader := configReader{w: w, where: fmt.Sprintf("%s: behavior %s config", where, behavior.Name), root: at}
		value, serr := reader.value(w.checker.GetTypeFromTypeNode(at), at, "", 0)
		if serr != nil {
			return fieldUse{}, true, serr
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return fieldUse{}, true, errorAtNode(at, "%s: behavior %s config: %v", where, behavior.Name, err)
		}
		config = raw
	}
	refs := []ir.BehaviorRef{{Name: behavior.Name, Field: field, Config: config}}
	if err := ir.CanonicalizeBehaviors(refs); err != nil {
		return fieldUse{}, true, errorAtNode(at, "type %s: %v", class, err)
	}
	if err := behavior.ValidateConfig(refs[0].Config); err != nil {
		return fieldUse{}, true, errorAtNode(at, "%s: %s", where, err)
	}
	return fieldUse{ref: refs[0], node: member}, true, nil
}

// applyFieldBehaviors appends to td the uses its class composes on its
// fields, in field order, after the @behavior decorators' uses, once the
// decorators have settled td's role and @jsonField: a behavior is anchored
// on a field of a DB table (D103), whose columns its declared fields
// become. A type that composes behaviors both ways is the verification
// pass's to refuse (verify.checkTypeBehaviors), as it is for the data
// forms.
func (w *walker) applyFieldBehaviors(node *astNode, td *ir.TypeDef) {
	uses := w.classUses[node]
	if len(uses) == 0 {
		return
	}
	if w.cfg.Kind != ir.SchemaKindDB || td.Role != ir.RoleDBTable || td.JsonField {
		use := uses[0]
		w.addErr(errorAtNode(use.node, "type %s: field %s is a %s, and a behavior is anchored on a field of a DB table, which %s is not (D103)", td.Name, use.ref.Field, use.ref.Name, td.Name))
		return
	}
	for _, use := range uses {
		td.Behaviors = append(td.Behaviors, use.ref)
	}
}

// inheritedBehaviorField refuses a base class or a trait whose fields
// include a behavior: a use is anchored on a field of the table that
// declares it, and a class that extends or implements another copies its
// fields, not its uses (D103). heritage is the clause entry that names
// base.
func (w *walker) inheritedBehaviorField(class string, heritage, base *astNode, baseName string) *SchemaError {
	uses := w.classUses[base]
	if len(uses) == 0 {
		return nil
	}
	return errorAtNode(heritage, "type %s: %s's field %s is a %s, which %s does not inherit: a behavior is anchored on a field of the table that declares it (D103)", class, baseName, uses[0].ref.Field, uses[0].ref.Name, class)
}

// maxConfigDepth bounds how deeply a behavior config's types nest, so a
// recursive type fails instead of recursing forever.
const maxConfigDepth = 32

// configReader reads a behavior config, a type argument of literal types,
// into the JSON value it stands for (D103), through the checker: an object
// type (a type literal, an interface, an alias of one, or `typeof` a
// constant declared `as const`) is an object of its members; a tuple is a
// list of its elements; a string, number or boolean literal type is its
// value and null is null; and a union of literal types, alone or as an
// array type's element ((A | B)[], Array<A | B>, ReadonlyArray<A | B>), is
// the list of its members, in the order the union is written in, in place
// or in the type alias the config names it by. An optional member that is
// absent, or whose type is undefined, is left out. Anything else, a
// widened string, a function or an index signature among them, is refused
// with the path to it and what it is, and so is a union of more than one
// literal whose written order the reader cannot find, since the checker
// orders a union's members by when it first met each.
type configReader struct {
	w *walker
	// where begins each error: the type, the field and the behavior.
	where string
	// root is the type argument, where an error without a node of its own
	// is located.
	root *astNode
}

// fail is an error at node (the root when node is nil) and the JSON pointer
// at into the config.
func (r *configReader) fail(node *astNode, at, format string, args ...any) *SchemaError {
	if node == nil {
		node = r.root
	}
	where := r.where
	if at != "" {
		where += " " + at
	}
	return errorAtNode(node, "%s: %s", where, fmt.Sprintf(format, args...))
}

// notLiteral refuses t, which stands for no fixed value.
func (r *configReader) notLiteral(t *checkerType, node *astNode, at string) *SchemaError {
	return r.fail(node, at, "%s is not a literal type; a config is written in literal types ('draft', 3, true, a tuple or an object type of them), or read with typeof from a constant declared as const", r.w.checker.TypeToString(t))
}

// value reads t, the type of node when the config writes it in place (nil
// when it does not, as in a constant read with typeof), at the pointer at.
func (r *configReader) value(t *checkerType, node *astNode, at string, depth int) (any, *SchemaError) {
	node = r.written(node)
	if depth > maxConfigDepth {
		return nil, r.fail(node, at, "the config nests more than %d levels deep", maxConfigDepth)
	}
	if t == nil {
		return nil, r.fail(node, at, "the checker cannot resolve this type")
	}
	flags := t.Flags()
	switch {
	case flags&(typeFlagsStringLiteral|typeFlagsNumberLiteral|typeFlagsBooleanLiteral) != 0:
		if v, ok := literalValue(t); ok {
			return v, nil
		}
		return nil, r.notLiteral(t, node, at)
	case flags&typeFlagsNull != 0:
		return nil, nil
	case flags&typeFlagsUndefined != 0:
		return nil, r.fail(node, at, "undefined is not a value; leave the member out")
	case flags&typeFlagsBoolean != 0:
		return nil, r.fail(node, at, "boolean is not a literal type; write true or false")
	case flags&typeFlagsUnion != 0:
		return r.union(t, node, at)
	case flags&typeFlagsObject != 0:
		switch {
		case t.IsTupleType():
			return r.tuple(t, node, at, depth)
		case r.w.checker.IsArrayType(t):
			return r.array(t, node, at)
		}
		return r.object(t, node, at, depth)
	case flags&typeFlagsIntersection != 0:
		for _, member := range t.Types() {
			if member.Flags()&typeFlagsObject == 0 || member.IsTupleType() || r.w.checker.IsArrayType(member) {
				return nil, r.notLiteral(t, node, at)
			}
		}
		return r.object(t, node, at, depth)
	}
	return nil, r.notLiteral(t, node, at)
}

// object reads an object type's members.
func (r *configReader) object(t *checkerType, node *astNode, at string, depth int) (any, *SchemaError) {
	c := r.w.checker
	// A function's type, or a class's (typeof Order), has no fixed value.
	if len(c.GetCallSignatures(t)) > 0 || len(c.GetConstructSignatures(t)) > 0 {
		return nil, r.notLiteral(t, node, at)
	}
	if len(c.GetIndexInfosOfType(t)) > 0 {
		return nil, r.fail(node, at, "%s has an index signature, which names no members; write each member", c.TypeToString(t))
	}
	out := map[string]any{}
	for _, prop := range c.GetPropertiesOfType(t) {
		memberAt := at + "/" + pointerToken(prop.Name)
		var memberNode *astNode
		if decl := prop.ValueDeclaration; decl != nil && decl.Kind == kindPropertySignature {
			memberNode = decl.AsPropertySignatureDeclaration().Type
		}
		pt, present := withoutUndefined(c, c.GetTypeOfSymbol(prop))
		if !present {
			continue
		}
		v, serr := r.value(pt, memberNode, memberAt, depth+1)
		if serr != nil {
			return nil, serr
		}
		out[prop.Name] = v
	}
	return out, nil
}

// tuple reads a tuple's elements, each required: an optional or a rest
// element is no fixed value.
func (r *configReader) tuple(t *checkerType, node *astNode, at string, depth int) (any, *SchemaError) {
	flags := t.TargetTupleType().ElementFlags()
	elements := r.w.checker.GetTypeArguments(t)
	if len(elements) < len(flags) {
		return nil, r.notLiteral(t, node, at)
	}
	var elementNodes []*astNode
	if node != nil && node.Kind == kindTupleType && len(node.Elements()) == len(flags) {
		elementNodes = node.Elements()
	}
	out := make([]any, 0, len(flags))
	for i, flag := range flags {
		var elementNode *astNode
		if elementNodes != nil {
			elementNode = elementNodes[i]
		}
		elementAt := at + "/" + strconv.Itoa(i)
		if flag != elementFlagsRequired {
			return nil, r.fail(elementNode, elementAt, "an optional or rest element of a tuple is not a value; write each element")
		}
		v, serr := r.value(elements[i], elementNode, elementAt, depth+1)
		if serr != nil {
			return nil, serr
		}
		out = append(out, v)
	}
	return out, nil
}

// array reads an array type whose element is a literal type or a union of
// them, (A | B)[] or Array<A | B>, as the list of those literals. An array
// of anything else has no fixed elements.
func (r *configReader) array(t *checkerType, node *astNode, at string) (any, *SchemaError) {
	args := r.w.checker.GetTypeArguments(t)
	if len(args) == 0 {
		return nil, r.notLiteral(t, node, at)
	}
	// The element as written: an array type's (T[]), or the one type
	// argument of a generic reference to it (Array<T>, ReadonlyArray<T>).
	// union holds what it reads there to the checker's element.
	var elementNode *astNode
	switch {
	case node == nil:
	case node.Kind == kindArrayType:
		elementNode = r.written(node.AsArrayTypeNode().ElementType)
	case node.Kind == kindTypeReference:
		if typeArgs := node.AsTypeReferenceNode().TypeArguments; typeArgs != nil && len(typeArgs.Nodes) == 1 {
			elementNode = r.written(typeArgs.Nodes[0])
		}
	}
	element := args[0]
	if element.Flags()&typeFlagsUnion != 0 && element.Flags()&typeFlagsBoolean == 0 {
		return r.union(element, elementNode, at)
	}
	if v, ok := literalOrNull(element); ok {
		return []any{v}, nil
	}
	return nil, r.fail(node, at, "%s is a list of no fixed elements; write a tuple ([A, B]) or a list of literal types ((A | B)[])", r.w.checker.TypeToString(t))
}

// union reads a union of literal types as the list of its members, in the
// order node, the union as written (in place, or in the alias it names),
// lists them. The checker orders a union's members by when it first met
// each, so a union of more than one member whose written order node does
// not give, or gives other members than t's, is refused: a list's order is
// part of its value (Workflow's initial state defaults to its first).
func (r *configReader) union(t *checkerType, node *astNode, at string) (any, *SchemaError) {
	c := r.w.checker
	members := []any{}
	for _, member := range t.Types() {
		if member.Flags()&typeFlagsUndefined != 0 {
			continue
		}
		v, ok := literalOrNull(member)
		if !ok {
			return nil, r.fail(node, at, "%s is a union, which a config reads as a list of literal types, and %s is not one", c.TypeToString(t), c.TypeToString(member))
		}
		members = append(members, v)
	}
	if written, ok := r.unionAsWritten(node); ok && sameMembers(written, members) {
		return written, nil
	}
	if len(members) > 1 {
		return nil, r.fail(node, at, "%s is a union, and a list read from a union keeps the order it is written in, which the reader cannot find here; write the list as a tuple ([A, B]) or as an array of a union written in place ((A | B)[])", c.TypeToString(t))
	}
	return members, nil
}

// unionAsWritten is the members of node, a union type written of literal
// types, in the order it writes them; ok is false for any other node.
func (r *configReader) unionAsWritten(node *astNode) ([]any, bool) {
	if node == nil || node.Kind != kindUnionType {
		return nil, false
	}
	out := []any{}
	for _, member := range node.AsUnionTypeNode().Types.Nodes {
		mt := r.w.checker.GetTypeFromTypeNode(member)
		if mt != nil && mt.Flags()&typeFlagsUndefined != 0 {
			continue
		}
		v, ok := literalOrNull(mt)
		if !ok {
			return nil, false
		}
		out = append(out, v)
	}
	return out, true
}

// sameMembers reports whether a and b hold the same values, each once,
// whatever their order.
func sameMembers(a, b []any) bool {
	if len(a) != len(b) {
		return false
	}
	for _, v := range a {
		if !slices.Contains(b, v) {
			return false
		}
	}
	return true
}

// written is node as the config writes it: through parentheses and
// readonly, and through each reference to a type alias with no type
// parameters, to the alias's own type node, so a union or an array an
// alias declares keeps the order it is written in. A chain of aliases is
// read at most maxConfigDepth deep.
func (r *configReader) written(node *astNode) *astNode {
	for range maxConfigDepth {
		node = unwrapConfigTypeNode(node)
		if node == nil || node.Kind != kindTypeReference {
			return node
		}
		ref := node.AsTypeReferenceNode()
		if ref.TypeArguments != nil && len(ref.TypeArguments.Nodes) > 0 {
			return node
		}
		id, ok := r.w.identityOf(ref.TypeName)
		if !ok || id.decl == nil || id.decl.Kind != kindTypeAliasDeclaration {
			return node
		}
		alias := id.decl.AsTypeAliasDeclaration()
		if alias.TypeParameters != nil && len(alias.TypeParameters.Nodes) > 0 {
			return node
		}
		node = alias.Type
	}
	return unwrapConfigTypeNode(node)
}

// literalOrNull is the value of a string, number or boolean literal type,
// or nil for null.
func literalOrNull(t *checkerType) (any, bool) {
	if t == nil {
		return nil, false
	}
	if t.Flags()&typeFlagsNull != 0 {
		return nil, true
	}
	if t.Flags()&(typeFlagsStringLiteral|typeFlagsNumberLiteral|typeFlagsBooleanLiteral) == 0 {
		return nil, false
	}
	return literalValue(t)
}

// withoutUndefined is t without undefined, the type an optional member
// adds it to; present is false when nothing else is left, a member the
// config leaves out.
func withoutUndefined(c *typeChecker, t *checkerType) (*checkerType, bool) {
	if t == nil {
		return nil, true
	}
	if t.Flags()&typeFlagsUndefined != 0 {
		return nil, false
	}
	if t.Flags()&typeFlagsUnion == 0 {
		return t, true
	}
	var kept []*checkerType
	for _, member := range t.Types() {
		if member.Flags()&typeFlagsUndefined == 0 {
			kept = append(kept, member)
		}
	}
	switch {
	case len(kept) == len(t.Types()):
		return t, true
	case len(kept) == 0:
		return nil, false
	case len(kept) == 1:
		return kept[0], true
	}
	return c.GetUnionType(kept), true
}

// unwrapConfigTypeNode reads through parentheses and readonly, which give
// a config value nothing.
func unwrapConfigTypeNode(node *astNode) *astNode {
	for node != nil {
		switch node.Kind {
		case kindParenthesizedType:
			node = node.AsParenthesizedTypeNode().Type
		case kindTypeOperator:
			node = node.AsTypeOperatorNode().Type
		default:
			return node
		}
	}
	return node
}

// pointerToken escapes a member name as a JSON pointer token (RFC 6901).
func pointerToken(name string) string {
	return strings.ReplaceAll(strings.ReplaceAll(name, "~", "~0"), "/", "~1")
}
