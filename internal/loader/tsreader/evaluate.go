package tsreader

import (
	"encoding/json"
	"reflect"
	"strconv"
)

// serviceHandle is the evaluated value of a @superschematic/schema-config service({...})
// sentinel expression.
type serviceHandle struct {
	name string
	kind string
}

// MarshalJSON renders the handle as the object service({...}) was called
// with, so a decorator argument that carries sentinels reaches
// DecoratorSpec.Apply through the JSON round trip as
// {"name": ..., "kind": ...}: the shape the data forms write for the same
// reference.
func (h serviceHandle) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
	}{Name: h.name, Kind: h.kind})
}

// maxEvalDepth bounds identifier reach-through so initializer cycles fail
// instead of recursing forever.
const maxEvalDepth = 16

// evaluateExpression statically evaluates a decorator or config argument.
// Only literals are supported: strings, numbers, booleans, null, arrays,
// object literals, enum member references, and service sentinel constants
// (identifiers whose initializer is a service({...}) call). Anything computed
// is rejected -- the execution path is the backstop for runtime
// values.
//
// Results are Go values: string, float64, bool, nil, []any, map[string]any,
// and serviceHandle.
func (w *walker) evaluateExpression(node *astNode) (any, *SchemaError) {
	return w.evaluateExpressionDepth(node, 0)
}

func (w *walker) evaluateExpressionDepth(node *astNode, depth int) (any, *SchemaError) {
	if depth > maxEvalDepth {
		return nil, errorAtNode(node, "argument expression is too deeply nested to evaluate statically")
	}

	switch node.Kind {
	case kindStringLiteral:
		return node.Text(), nil

	case kindNumericLiteral:
		v, err := strconv.ParseFloat(node.Text(), 64)
		if err != nil {
			return nil, errorAtNode(node, "invalid numeric literal %q", node.Text())
		}
		return v, nil

	case kindTrueKeyword:
		return true, nil

	case kindFalseKeyword:
		return false, nil

	case kindNullKeyword:
		return nil, nil

	case kindPrefixUnaryExpression:
		unary := node.AsPrefixUnaryExpression()
		if unary.Operator != kindMinusToken {
			return nil, errorAtNode(node, "unsupported unary operator in argument; only negation of numeric literals is allowed")
		}
		inner, serr := w.evaluateExpressionDepth(unary.Operand, depth+1)
		if serr != nil {
			return nil, serr
		}
		n, ok := inner.(float64)
		if !ok {
			return nil, errorAtNode(node, "negation is only supported on numeric literals")
		}
		return -n, nil

	case kindArrayLiteralExpression:
		elems := node.AsArrayLiteralExpression().Elements
		var out []any
		if elems != nil {
			for _, e := range elems.Nodes {
				v, serr := w.evaluateExpressionDepth(e, depth+1)
				if serr != nil {
					return nil, serr
				}
				out = append(out, v)
			}
		}
		return out, nil

	case kindObjectLiteralExpression:
		props := node.AsObjectLiteralExpression().Properties
		out := make(map[string]any)
		if props != nil {
			for _, p := range props.Nodes {
				if p.Kind != kindPropertyAssignment {
					return nil, errorAtNode(p, "object arguments must use plain property assignments")
				}
				assignment := p.AsPropertyAssignment()
				key, serr := w.evaluatePropertyName(p.Name(), depth+1)
				if serr != nil {
					return nil, serr
				}
				v, serr := w.evaluateExpressionDepth(assignment.Initializer, depth+1)
				if serr != nil {
					return nil, serr
				}
				out[key] = v
			}
		}
		return out, nil

	case kindAsExpression:
		// A type assertion has no runtime value of its own; an extension
		// config writes `kind: "Catalog" as SchemaKind` until the
		// defineConfig type widens (extension-model.md, 6.3).
		return w.evaluateExpressionDepth(node.AsAsExpression().Expression, depth+1)

	case kindPropertyAccessExpression, kindIdentifier:
		return w.evaluateReference(node, depth)

	case kindCallExpression:
		return w.evaluateCall(node, depth)
	}

	return nil, errorAtNode(node, "argument must be a literal; computed values are read by the execution path, not the static walk")
}

// evaluatePropertyName evaluates an object literal key: identifiers, string
// literals, and computed names that statically evaluate to strings (for
// example [TargetLanguage.TypeScript]).
func (w *walker) evaluatePropertyName(name *astNode, depth int) (string, *SchemaError) {
	if name == nil {
		return "", &SchemaError{Msg: "object property has no name"}
	}
	switch name.Kind {
	case kindIdentifier, kindStringLiteral:
		return name.Text(), nil
	case kindComputedPropertyName:
		v, serr := w.evaluateExpressionDepth(name.AsComputedPropertyName().Expression, depth)
		if serr != nil {
			return "", serr
		}
		s, ok := v.(string)
		if !ok {
			return "", errorAtNode(name, "computed property keys must evaluate to strings")
		}
		return s, nil
	}
	return "", errorAtNode(name, "unsupported object property key")
}

// evaluateReference evaluates an identifier or property access: enum members
// resolve to their literal value; const variables resolve through their
// initializer (service sentinels).
func (w *walker) evaluateReference(node *astNode, depth int) (any, *SchemaError) {
	target := node
	if node.Kind == kindPropertyAccessExpression {
		target = node.Name()
	}
	sym := w.checker.GetSymbolAtLocation(target)
	if sym == nil {
		return nil, errorAtNode(node, "cannot resolve %q in argument position", target.Text())
	}
	sym = w.checker.SkipAlias(sym)

	if sym.Flags&symbolFlagsEnumMember != 0 {
		t := w.checker.GetTypeOfSymbol(sym)
		if v, ok := literalValue(t); ok {
			return v, nil
		}
		return nil, errorAtNode(node, "enum member %q has no literal value", sym.Name)
	}

	if decl := sym.ValueDeclaration; decl != nil && decl.Kind == kindVariableDeclaration {
		init := decl.AsVariableDeclaration().Initializer
		if init == nil {
			return nil, errorAtNode(node, "const %q has no initializer to evaluate", sym.Name)
		}
		return w.evaluateExpressionDepth(init, depth+1)
	}

	return nil, errorAtNode(node, "%q does not statically evaluate to a literal", sym.Name)
}

// evaluateCall evaluates the one call form the static walk understands: the
// service({...}) sentinel constructor from @superschematic/schema-config.
func (w *walker) evaluateCall(node *astNode, depth int) (any, *SchemaError) {
	call := node.AsCallExpression()
	id, ok := w.identityOf(call.Expression)
	if !ok || !id.is("@superschematic/schema-config", "service") {
		return nil, errorAtNode(node, "function calls are not statically evaluable (only service({...}) sentinels are)")
	}
	if call.Arguments == nil || len(call.Arguments.Nodes) != 1 {
		return nil, errorAtNode(node, "service(...) takes exactly one object literal argument")
	}
	v, serr := w.evaluateExpressionDepth(call.Arguments.Nodes[0], depth+1)
	if serr != nil {
		return nil, serr
	}
	cfg, ok := v.(map[string]any)
	if !ok {
		return nil, errorAtNode(node, "service(...) argument must be an object literal")
	}
	name, _ := cfg["name"].(string)
	kind, _ := cfg["kind"].(string)
	if name == "" || kind == "" {
		return nil, errorAtNode(node, "service(...) requires literal name and kind")
	}
	return serviceHandle{name: name, kind: kind}, nil
}

// literalValue extracts the Go value of a literal type: string, number
// (float64), or boolean.
func literalValue(t *checkerType) (any, bool) {
	if t == nil {
		return nil, false
	}
	flags := t.Flags()
	switch {
	case flags&typeFlagsStringLiteral != 0:
		v := t.AsLiteralType().Value()
		if s, ok := v.(string); ok {
			return s, true
		}
	case flags&typeFlagsNumberLiteral != 0:
		v := t.AsLiteralType().Value()
		// The compiler represents numeric literal values with its own
		// float64-backed number type; read it reflectively to avoid
		// depending on that package.
		rv := reflect.ValueOf(v)
		if rv.Kind() == reflect.Float64 {
			return rv.Float(), true
		}
	case flags&typeFlagsBooleanLiteral != 0:
		if lt, ok := literalTypeValueAny(t); ok {
			if b, isBool := lt.(bool); isBool {
				return b, true
			}
		}
		// Boolean literal types are intrinsics in some compiler versions;
		// fall back to the type's string form.
		return nil, false
	}
	return nil, false
}

// literalTypeValueAny guards AsLiteralType for types whose data may not be a
// literal payload.
func literalTypeValueAny(t *checkerType) (v any, ok bool) {
	defer func() {
		if recover() != nil {
			v, ok = nil, false
		}
	}()
	return t.AsLiteralType().Value(), true
}
