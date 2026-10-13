package typegen

import (
	"strings"

	"github.com/parable-work/superschematic/internal/generator/behaviortypes"
)

// HasBehaviors reports whether the module writes behaviors.go: a type of
// the schema composes behaviors.
func (o *ModuleOutput) HasBehaviors() bool {
	return len(o.Behaviors) > 0
}

// BehaviorsUseJSON reports whether behaviors.go holds a field of any JSON,
// a json.RawMessage, and so imports encoding/json.
func (o *ModuleOutput) BehaviorsUseJSON() bool {
	return behaviortypes.UsesKind(o.Behaviors, behaviortypes.KindJSON)
}

// behaviorGoType is a behavior member's Go type: a pointer for a value a
// read may leave out or hold null for, except a slice, a map and a
// json.RawMessage, which are nil then.
func behaviorGoType(shape behaviortypes.Shape, required bool) string {
	if (!required || shape.Nullable) && !nilable(shape) {
		return "*" + behaviorGoValueType(shape)
	}
	return behaviorGoValueType(shape)
}

// behaviorGoValueType is the Go type of one value of a shape: an element
// or a map value that takes null is a pointer.
func behaviorGoValueType(shape behaviortypes.Shape) string {
	switch shape.Kind {
	case behaviortypes.KindString:
		return "string"
	case behaviortypes.KindInteger:
		return "int64"
	case behaviortypes.KindNumber:
		return "float64"
	case behaviortypes.KindBoolean:
		return "bool"
	case behaviortypes.KindArray:
		return "[]" + behaviorGoType(*shape.Elem, !shape.Elem.Nullable)
	case behaviortypes.KindMap:
		return "map[string]" + behaviorGoType(*shape.Elem, !shape.Elem.Nullable)
	case behaviortypes.KindStruct, behaviortypes.KindEnum:
		return shape.Ref
	}
	return "json.RawMessage"
}

// nilable reports whether a shape's Go type has nil for no value.
func nilable(shape behaviortypes.Shape) bool {
	switch shape.Kind {
	case behaviortypes.KindArray, behaviortypes.KindMap, behaviortypes.KindJSON:
		return true
	}
	return false
}

// behaviorGoTag is a behavior member's struct tag: its exact JSON key, and
// omitempty for one a read may leave out.
func behaviorGoTag(member behaviortypes.Member) string {
	tag := member.JSON
	if !member.Required {
		tag += ",omitempty"
	}
	return "`json:\"" + tag + "\"`"
}

// goComment writes text as a // comment at an indent, "" for no text.
func goComment(indent, text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return ""
	}
	return indent + "// " + text
}
