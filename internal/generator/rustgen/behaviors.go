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
		parts = append(parts, "rename = "+rustString(member.JSON))
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
	if !required || shape.Nullable {
		return "Option<" + rustBehaviorValueType(shape) + ">"
	}
	return rustBehaviorValueType(shape)
}

// rustBehaviorValueType is the Rust type of one value of a shape.
func rustBehaviorValueType(shape behaviortypes.Shape) string {
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
		return "Vec<" + rustBehaviorType(*shape.Elem, !shape.Elem.Nullable) + ">"
	case behaviortypes.KindMap:
		return "std::collections::HashMap<String, " + rustBehaviorType(*shape.Elem, !shape.Elem.Nullable) + ">"
	case behaviortypes.KindStruct, behaviortypes.KindEnum:
		return shape.Ref
	}
	return "serde_json::Value"
}
