package rustgen

import (
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/behaviortypes"
	"github.com/parable-work/superschematic/internal/generator/rustutil"
)

// checkBehaviorNames refuses two members of a behaviors struct that one
// snake_case field name would take, as MemberName's PascalCase names
// cannot: AB and Ab are both ab.
func checkBehaviorNames(types []behaviortypes.Type) error {
	for _, t := range types {
		for _, s := range t.Structs {
			seen := map[string]string{}
			for _, member := range s.Members {
				field := rustBehaviorField(member)
				if prev, taken := seen[field]; taken {
					return fmt.Errorf("the behaviors of type %s: %s and %s would both be the field %s of %s", t.Name, prev, member.JSON, field, s.Name)
				}
				seen[field] = member.JSON
			}
		}
	}
	return nil
}

// rustBehaviorField is a behavior member's field name: its PascalCase name
// in snake_case, a keyword escaped.
func rustBehaviorField(member behaviortypes.Member) string {
	return toRustFieldName(member.Name)
}

// rustBehaviorVariant is an enum value's variant: its PascalCase name, Self
// with an underscore after it.
func rustBehaviorVariant(name string) string {
	if rustutil.IsRustKeyword(name) {
		return name + "_"
	}
	return name
}

// rustBehaviorAttr is a behavior member's serde attribute: its exact JSON
// key when the field's name differs, and for a member a read may leave out
// a default of None that is not written. "" when it needs none.
func rustBehaviorAttr(member behaviortypes.Member) string {
	var parts []string
	if !member.Required {
		parts = append(parts, "default")
	}
	if strings.TrimPrefix(rustBehaviorField(member), "r#") != member.JSON {
		parts = append(parts, "rename = "+rustutil.Quote(member.JSON))
	}
	if !member.Required {
		parts = append(parts, `skip_serializing_if = "Option::is_none"`)
	}
	if len(parts) == 0 {
		return ""
	}
	return "#[serde(" + strings.Join(parts, ", ") + ")]"
}

// rustBehaviorType is a behavior member's Rust type: an Option for a value
// a read may leave out or hold null for.
func rustBehaviorType(shape behaviortypes.Shape, required bool) string {
	return rustShapeType(shape, required, "serde_json::Value")
}

// rustOperationType is the Rust type of a member of a behavior operation's
// parameters or result, as src/orm.rs writes it: a JSON value is the HTTP
// runtime's, which orm.rs names without a serde_json of its own.
func rustOperationType(shape behaviortypes.Shape, required bool) string {
	return rustShapeType(shape, required, "orm::Value")
}

// rustShapeType is the Rust type of a shape, json the type of any JSON
// value: an Option for a value a read may leave out or hold null for.
func rustShapeType(shape behaviortypes.Shape, required bool, json string) string {
	if !required || shape.Nullable {
		return "Option<" + rustShapeValueType(shape, json) + ">"
	}
	return rustShapeValueType(shape, json)
}

// rustShapeValueType is the Rust type of one value of a shape.
func rustShapeValueType(shape behaviortypes.Shape, json string) string {
	switch shape.Kind {
	case behaviortypes.KindString:
		return "String"
	case behaviortypes.KindInteger:
		return "i64"
	case behaviortypes.KindNumber:
		return "f64"
	case behaviortypes.KindBoolean:
		return "bool"
	case behaviortypes.KindArray:
		return "Vec<" + rustShapeType(*shape.Elem, !shape.Elem.Nullable, json) + ">"
	case behaviortypes.KindMap:
		return "std::collections::HashMap<String, " + rustShapeType(*shape.Elem, !shape.Elem.Nullable, json) + ">"
	case behaviortypes.KindStruct, behaviortypes.KindEnum:
		return shape.Ref
	}
	return json
}
