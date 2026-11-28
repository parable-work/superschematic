package tsreader

import (
	"encoding/json"
	"strconv"
	"strings"

	scalars "github.com/parable-work/superscalar/go"

	"github.com/parable-work/superschematic/internal/registry"
)

// scalarKeyword is the keyword a schema names a superscalar scalar under,
// the one a tool's schema names its parameter's by
// (apigen.DefaultToolScalarKey).
const scalarKeyword = "x-superschematic-scalar"

// The authoring types of @superschematic/db that a behavior's class writes
// a JSON Schema with (packages/db/src/behaviorclass.ts).
const (
	jsonSchemaWrapper = "JsonSchema"
	integerType       = "Integer"
	constWrapper      = "Const"
	oneOfWrapper      = "OneOf"
	openWrapper       = "Open"
	namesWrapper      = "Names"
	hearsWrapper      = "Hears"
	rowKeyType        = "RowKey"
)

// maxSchemaDepth bounds the types a schema follows, so a type that names
// itself fails instead of recursing forever.
const maxSchemaDepth = 32

// schema is the JSON Schema of a type node of a behavior's class (D103),
// read from the node as written, so a union's members and an object's
// keep the order they are written in:
//
//   - string, number and boolean are their JSON types, Integer is
//     "integer", unknown is the empty schema;
//   - a superscalar scalar (Temporal.Period) is its JSON type with the
//     bounds and the pattern its definition declares, and its name under
//     x-superschematic-scalar;
//   - a string or number literal, or a union of them, is an enum of them;
//     true or false, or Const<V>, is a const;
//   - T | null is T's schema with "null" beside its type, and any other
//     union a oneOf of its members';
//   - X[], readonly X[] and Array<X> are arrays of X; a tuple an array of
//     exactly its elements (prefixItems);
//   - an interface or a type literal is an object that sets
//     additionalProperties false, its members' properties described by
//     their JSDoc and its required the members not marked ?, in the order
//     written; Record<K, V> or an index signature is an object of V, whose
//     propertyNames are K's schema when K is not string;
//   - a type alias is its type, and JsonSchema<T, K> T's schema with the
//     keywords the literal type K holds, K's winning.
func (r *behaviorClassReader) schema(node *astNode) (map[string]any, bool) {
	return r.schemaAt(node, 0)
}

func (r *behaviorClassReader) schemaAt(node *astNode, depth int) (map[string]any, bool) {
	if depth > maxSchemaDepth {
		r.fail(node, "the type is too deeply nested for a JSON Schema: does it name itself?")
		return nil, false
	}
	switch node.Kind {
	case kindStringKeyword:
		return map[string]any{"type": "string"}, true
	case kindNumberKeyword:
		return map[string]any{"type": "number"}, true
	case kindBooleanKeyword:
		return map[string]any{"type": "boolean"}, true
	case kindObjectKeyword:
		// Any object, whose members no schema holds.
		return map[string]any{"type": "object"}, true
	case kindUnknownKeyword, kindAnyKeyword:
		return map[string]any{}, true
	case kindParenthesizedType:
		return r.schemaAt(node.AsParenthesizedTypeNode().Type, depth+1)
	case kindTypeOperator:
		// readonly X[] reads as X[].
		return r.schemaAt(node.AsTypeOperatorNode().Type, depth+1)
	case kindLiteralType:
		value, ok := r.literal(node.AsLiteralTypeNode().Literal)
		if !ok {
			return nil, false
		}
		switch value.(type) {
		case nil:
			return map[string]any{"type": "null"}, true
		case bool:
			return map[string]any{"const": value}, true
		}
		return map[string]any{"enum": []any{value}}, true
	case kindUnionType:
		return r.unionSchema(node, depth, false)
	case kindArrayType:
		element := node.AsArrayTypeNode().ElementType
		if element.Kind == kindUnknownKeyword {
			// An array of anything: no items keyword.
			return map[string]any{"type": "array"}, true
		}
		items, ok := r.eachItem(element, depth)
		if !ok {
			return nil, false
		}
		return map[string]any{"type": "array", "items": items}, true
	case kindTupleType:
		var prefix []any
		for _, element := range node.AsTupleTypeNode().Elements.Nodes {
			if element.Kind == kindNamedTupleMember {
				r.fail(element, "a tuple of a behavior's schema names no members")
				return nil, false
			}
			s, ok := r.schemaAt(element, depth+1)
			if !ok {
				return nil, false
			}
			prefix = append(prefix, s)
		}
		n := json.Number(strconv.Itoa(len(prefix)))
		return map[string]any{"type": "array", "prefixItems": prefix, "minItems": n, "maxItems": n}, true
	case kindTypeLiteral:
		return r.objectSchema(node.AsTypeLiteralNode().Members.Nodes, depth)
	case kindTypeReference:
		return r.referenceSchema(node, depth)
	}
	r.fail(node, "a behavior's class writes no JSON Schema for this type: use string, number, boolean, Integer, literals, unions, arrays, tuples, interfaces, type literals, Record, a type alias of them, or JsonSchema<T, K> (D103)")
	return nil, false
}

// unionSchema is a union's schema: an enum of literals, T's with "null"
// beside its type, or a oneOf; with oneOfAlways (OneOf<U>), a oneOf of its
// members, null's {"type": "null"} among them.
func (r *behaviorClassReader) unionSchema(node *astNode, depth int, oneOfAlways bool) (map[string]any, bool) {
	members := node.AsUnionTypeNode().Types.Nodes
	var literals []any
	var others []*astNode
	// literalsAt is where the literals' enum stands among a oneOf's
	// members: at the first literal, the others in the order written.
	literalsAt := -1
	nullable := false
	for _, m := range members {
		if m.Kind == kindLiteralType {
			lit := m.AsLiteralTypeNode().Literal
			if lit.Kind == kindNullKeyword {
				nullable = true
				continue
			}
			if value, ok := r.literal(lit); ok {
				if _, isBool := value.(bool); !isBool {
					if literalsAt < 0 {
						literalsAt = len(others)
					}
					literals = append(literals, value)
					continue
				}
			}
		}
		others = append(others, m)
	}
	if len(others) == 0 && len(literals) > 0 && !oneOfAlways {
		if nullable {
			literals = append(literals, nil)
		}
		return map[string]any{"enum": literals}, true
	}
	if nullable && len(others) == 1 && len(literals) == 0 && !oneOfAlways {
		s, ok := r.schemaAt(others[0], depth+1)
		if !ok {
			return nil, false
		}
		switch t := s["type"].(type) {
		case string:
			s["type"] = []any{t, "null"}
		case []any:
			s["type"] = append(t, "null")
		default:
			r.fail(node, "T | null needs a T whose schema has a type")
			return nil, false
		}
		return s, true
	}
	var oneOf []any
	for i, m := range others {
		if i == literalsAt {
			oneOf = append(oneOf, map[string]any{"enum": literals})
		}
		s, ok := r.schemaAt(m, depth+1)
		if !ok {
			return nil, false
		}
		oneOf = append(oneOf, s)
	}
	if literalsAt == len(others) {
		oneOf = append(oneOf, map[string]any{"enum": literals})
	}
	if nullable {
		oneOf = append(oneOf, map[string]any{"type": "null"})
	}
	return map[string]any{"oneOf": oneOf}, true
}

// objectSchema is the schema of an object type's members: a closed object,
// or a map when its one member is an index signature.
func (r *behaviorClassReader) objectSchema(members []*astNode, depth int) (map[string]any, bool) {
	out := map[string]any{"type": "object", "additionalProperties": false}
	properties := map[string]any{}
	var required []any
	for _, m := range members {
		switch m.Kind {
		case kindPropertySignature:
			sig := m.AsPropertySignatureDeclaration()
			name := m.Name().Text()
			if sig.Type == nil {
				r.fail(m, "member %s names its type", name)
				return nil, false
			}
			r.at = append(r.at, name)
			s, ok := r.schemaAt(sig.Type, depth+1)
			r.at = r.at[:len(r.at)-1]
			if !ok {
				return nil, false
			}
			if doc := readDoc(m); doc.text != "" {
				s["description"] = doc.text
			}
			properties[name] = s
			if sig.PostfixToken == nil || sig.PostfixToken.Kind != kindQuestionToken {
				required = append(required, name)
			}
		case kindIndexSignature:
			index := m.AsIndexSignatureDeclaration()
			key := index.Parameters.Nodes[0].AsParameterDeclaration().Type
			value, ok := r.eachMember(index.Type, depth)
			if !ok {
				return nil, false
			}
			out["additionalProperties"] = value
			if key.Kind != kindStringKeyword {
				names, ok := r.schemaAt(key, depth+1)
				if !ok {
					return nil, false
				}
				delete(names, "type")
				out["propertyNames"] = names
			}
		default:
			r.fail(m, "a member of an object of a behavior's schema is a property or an index signature")
			return nil, false
		}
	}
	if len(properties) > 0 {
		out["properties"] = properties
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out, true
}

// referenceSchema is the schema of a type reference: an authoring type, a
// builtin generic, an interface or a type alias.
func (r *behaviorClassReader) referenceSchema(node *astNode, depth int) (map[string]any, bool) {
	ref := node.AsTypeReferenceNode()
	var args []*astNode
	if ref.TypeArguments != nil {
		args = ref.TypeArguments.Nodes
	}
	name := entityNameText(ref.TypeName)
	if r.params[name] {
		r.fail(node, "a behavior's schema names no type parameter of its class (%s): write the type it stands for", name)
		return nil, false
	}
	id, ok := r.w.identityOf(ref.TypeName)
	if !ok {
		r.fail(node, "cannot resolve type %s", name)
		return nil, false
	}
	if id.pkg == behaviorFieldPackage {
		switch id.name {
		case jsonSchemaWrapper:
			if len(args) != 2 {
				break
			}
			base, ok := r.schemaAt(args[0], depth+1)
			if !ok {
				return nil, false
			}
			keywords, ok := r.literalType(args[1])
			if !ok {
				return nil, false
			}
			object, isObject := keywords.(map[string]any)
			if !isObject {
				r.fail(args[1], "JsonSchema<T, K>'s K is an object type of JSON Schema keywords")
				return nil, false
			}
			for key, value := range object {
				base[key] = value
			}
			return base, true
		case namesWrapper:
			if len(args) != 2 {
				break
			}
			kind, ok := r.literalType(args[1])
			if !ok {
				return nil, false
			}
			return r.reference(node, args, kind, depth)
		case hearsWrapper:
			return r.reference(node, args, registry.ReferenceListen, depth)
		case integerType:
			return map[string]any{"type": "integer"}, true
		case rowKeyType:
			return map[string]any{"type": "string"}, true
		case openWrapper:
			if len(args) != 1 {
				break
			}
			base, ok := r.schemaAt(args[0], depth+1)
			if !ok {
				return nil, false
			}
			delete(base, "additionalProperties")
			return base, true
		case oneOfWrapper:
			if len(args) != 1 || args[0].Kind != kindUnionType {
				r.fail(node, "OneOf<U> takes a union")
				return nil, false
			}
			return r.unionSchema(args[0], depth+1, true)
		case constWrapper:
			if len(args) != 1 {
				break
			}
			value, ok := r.literalType(args[0])
			if !ok {
				return nil, false
			}
			return map[string]any{"const": value}, true
		}
	}
	if brand, ok := r.w.scalarBrand(r.w.checker.GetTypeFromTypeNode(node)); ok {
		return r.scalarSchema(node, brand)
	}
	switch name {
	case "Array", "ReadonlyArray":
		if len(args) == 1 {
			items, ok := r.eachItem(args[0], depth)
			if !ok {
				return nil, false
			}
			return map[string]any{"type": "array", "items": items}, true
		}
	case "Readonly":
		if len(args) == 1 {
			return r.schemaAt(args[0], depth+1)
		}
	case "Record":
		if len(args) == 2 {
			value, ok := r.eachMember(args[1], depth)
			if !ok {
				return nil, false
			}
			out := map[string]any{"type": "object", "additionalProperties": value}
			if args[0].Kind != kindStringKeyword {
				names, ok := r.schemaAt(args[0], depth+1)
				if !ok {
					return nil, false
				}
				delete(names, "type")
				out["propertyNames"] = names
			}
			return out, true
		}
	}
	if id.decl == nil {
		r.fail(node, "cannot resolve type %s", name)
		return nil, false
	}
	switch id.decl.Kind {
	case kindInterfaceDeclaration:
		iface := id.decl.AsInterfaceDeclaration()
		if iface.TypeParameters != nil || len(args) > 0 || iface.HeritageClauses != nil {
			r.fail(node, "interface %s of a behavior's schema takes no type parameter and extends nothing", name)
			return nil, false
		}
		return r.objectSchema(iface.Members.Nodes, depth)
	case kindTypeAliasDeclaration:
		alias := id.decl.AsTypeAliasDeclaration()
		if alias.TypeParameters != nil || len(args) > 0 {
			r.fail(node, "type %s of a behavior's schema takes no type parameter", name)
			return nil, false
		}
		return r.schemaAt(alias.Type, depth+1)
	}
	r.fail(node, "a behavior's class writes no JSON Schema for %s: an interface, a type alias or an authoring type of @superschematic/db (JsonSchema, Integer, Const)", name)
	return nil, false
}

// scalarSchema is the schema of the superscalar scalar a type names: the
// JSON type, the length and value bounds and the pattern its definition
// declares, which any JSON Schema validator holds a value to, and its name
// under x-superschematic-scalar. What only the scalar's own validator
// refuses (HasCustomValidate) a reader of the keyword checks, not the
// schema.
func (r *behaviorClassReader) scalarSchema(node *astNode, brand string) (map[string]any, bool) {
	meta, ok := scalars.ScalarMetadataByCanonical[brand]
	if !ok {
		r.fail(node, "a behavior's schema names scalar %s, which superscalar does not define", brand)
		return nil, false
	}
	switch meta.JSONSchemaType {
	case "string", "integer", "number", "boolean":
	default:
		r.fail(node, "a behavior's schema names scalar %s, whose JSON type (%s) is no string, number or boolean", brand, meta.JSONSchemaType)
		return nil, false
	}
	out := map[string]any{"type": meta.JSONSchemaType, scalarKeyword: brand}
	if meta.MinLength > 0 {
		out["minLength"] = json.Number(strconv.Itoa(meta.MinLength))
	}
	if meta.MaxLength > 0 {
		out["maxLength"] = json.Number(strconv.Itoa(meta.MaxLength))
	}
	if meta.Minimum != nil {
		out["minimum"] = json.Number(strconv.FormatInt(*meta.Minimum, 10))
	}
	if meta.Maximum != nil {
		out["maximum"] = json.Number(strconv.FormatInt(*meta.Maximum, 10))
	}
	if meta.Pattern != "" {
		out["pattern"] = meta.Pattern
	}
	return out, true
}

// eachItem is the schema of a list's items, read at the list's path with
// [] after its last segment (transitions[]), as a reference within it
// names it.
func (r *behaviorClassReader) eachItem(node *astNode, depth int) (map[string]any, bool) {
	if len(r.at) == 0 {
		return r.schemaAt(node, depth+1)
	}
	last := r.at[len(r.at)-1]
	r.at[len(r.at)-1] = last + "[]"
	defer func() { r.at[len(r.at)-1] = last }()
	return r.schemaAt(node, depth+1)
}

// eachMember is the schema of an object's members, Record's or an index
// signature's values, read at the object's path with a * segment
// (gates.*).
func (r *behaviorClassReader) eachMember(node *astNode, depth int) (map[string]any, bool) {
	r.at = append(r.at, "*")
	defer func() { r.at = r.at[:len(r.at)-1] }()
	return r.schemaAt(node, depth+1)
}

// reference records, while the config is read, the member at the current
// path as one that names a part of the table (Names<T, R>, R's kind) or
// whose object the use hears (Hears<T>), and reads its schema, T's.
func (r *behaviorClassReader) reference(node *astNode, args []*astNode, kind any, depth int) (map[string]any, bool) {
	if len(args) == 0 {
		r.fail(node, "a reference's first type argument is its value's type")
		return nil, false
	}
	if r.refs == nil {
		r.fail(node, "Names and Hears mark members of the config alone, which name a part of the table its use sits on (D103)")
		return nil, false
	}
	ref := registry.BehaviorReference{Path: strings.Join(r.at, ".")}
	switch k := kind.(type) {
	case string:
		ref.Kind = k
	case map[string]any:
		ref.Kind = registry.ReferenceField
		ref.Type, _ = k["field"].(string)
		ref.Reason, _ = k["reason"].(string)
	}
	*r.refs = append(*r.refs, ref)
	return r.schemaAt(args[0], depth+1)
}

// literal is a literal type's value: a string, a number as written, true,
// false or null.
func (r *behaviorClassReader) literal(node *astNode) (any, bool) {
	switch node.Kind {
	case kindStringLiteral, kindNoSubstitutionTemplate:
		return node.Text(), true
	case kindNumericLiteral:
		return json.Number(node.Text()), true
	case kindTrueKeyword:
		return true, true
	case kindFalseKeyword:
		return false, true
	case kindNullKeyword:
		return nil, true
	case kindPrefixUnaryExpression:
		unary := node.AsPrefixUnaryExpression()
		if unary.Operator == kindMinusToken && unary.Operand.Kind == kindNumericLiteral {
			return json.Number("-" + unary.Operand.Text()), true
		}
	}
	r.fail(node, "a literal type is a string, a number, true, false or null")
	return nil, false
}

// literalType is the JSON a literal type node spells, as JsonSchema's K
// and Const's V are written: literals, tuples of them and object types of
// them, read as written.
func (r *behaviorClassReader) literalType(node *astNode) (any, bool) {
	switch node.Kind {
	case kindLiteralType:
		return r.literal(node.AsLiteralTypeNode().Literal)
	case kindParenthesizedType:
		return r.literalType(node.AsParenthesizedTypeNode().Type)
	case kindTypeOperator:
		return r.literalType(node.AsTypeOperatorNode().Type)
	case kindTupleType:
		out := []any{}
		for _, element := range node.AsTupleTypeNode().Elements.Nodes {
			v, ok := r.literalType(element)
			if !ok {
				return nil, false
			}
			out = append(out, v)
		}
		return out, true
	case kindTypeLiteral:
		out := map[string]any{}
		for _, m := range node.AsTypeLiteralNode().Members.Nodes {
			if m.Kind != kindPropertySignature || m.AsPropertySignatureDeclaration().Type == nil {
				r.fail(m, "a member of a literal object type is a property with a literal type")
				return nil, false
			}
			v, ok := r.literalType(m.AsPropertySignatureDeclaration().Type)
			if !ok {
				return nil, false
			}
			out[strings.Trim(m.Name().Text(), `"'`)] = v
		}
		return out, true
	case kindTrueKeyword, kindFalseKeyword, kindNullKeyword:
		return r.literal(node)
	}
	r.fail(node, "a JSON Schema keyword's value is written as a literal type: a string, a number, true, false, null, a tuple or an object type of them")
	return nil, false
}
