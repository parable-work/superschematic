package pygen

import (
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/behaviortypes"
)

// pydanticReserved are names a model's field cannot take without shadowing
// an attribute of BaseModel, or a method behaviors.py gives
// <T>Behaviors; such a field takes a trailing underscore, as a keyword
// does, and keeps its key through its alias.
var pydanticReserved = map[string]bool{
	"construct": true, "copy": true, "dict": true, "fields": true, "from_orm": true,
	"json": true, "parse_file": true, "parse_obj": true, "parse_raw": true, "schema": true,
	"schema_json": true, "update_forward_refs": true, "validate": true,
	"from_dict": true, "from_json": true, "to_json_dict": true,
}

// BehaviorNames are the names behaviors.py defines, which __init__.py
// re-exports: each type's enums, then its structs, in order.
func (o *ModuleOutput) BehaviorNames() []string {
	var names []string
	for _, t := range o.Behaviors {
		for _, e := range t.Enums {
			names = append(names, e.Name)
		}
		for _, s := range t.Structs {
			names = append(names, s.Name)
		}
	}
	return names
}

// BehaviorsUseLiteral reports whether a behavior's field is a string of
// listed values, which behaviors.py types as a Literal.
func (o *ModuleOutput) BehaviorsUseLiteral() bool {
	for _, t := range o.Behaviors {
		for _, s := range t.Structs {
			for _, member := range s.Members {
				for shape := &member.Shape; shape != nil; shape = shape.Elem {
					if shape.Kind == behaviortypes.KindString && len(shape.Values) > 0 {
						return true
					}
				}
			}
		}
	}
	return false
}

// checkBehaviorNames refuses two members of a behaviors model that one
// field name would take, as MemberName's PascalCase names cannot: AB and
// Ab are both ab.
func checkBehaviorNames(types []behaviortypes.Type) error {
	for _, t := range types {
		for _, s := range t.Structs {
			seen := map[string]string{}
			for _, member := range s.Members {
				field := pythonBehaviorField(member)
				if prev, taken := seen[field]; taken {
					return fmt.Errorf("the behaviors of type %s: %s and %s would both be the field %s of %s", t.Name, prev, member.JSON, field, s.Name)
				}
				seen[field] = member.JSON
			}
		}
	}
	return nil
}

// pythonBehaviorField is a behavior member's field name: its PascalCase
// name in snake_case, with a trailing underscore for a keyword, a name
// pydantic reserves and one in its model_ namespace.
func pythonBehaviorField(member behaviortypes.Member) string {
	name := toPythonFieldName(member.Name)
	if pydanticReserved[name] || strings.HasPrefix(name, "model_") {
		return name + "_"
	}
	return name
}

// pythonEnumMember is an enum value's member name: its PascalCase name,
// with a trailing underscore for a keyword (None, True, False).
func pythonEnumMember(name string) string {
	if _, keyword := pythonKeywords[name]; keyword {
		return name + "_"
	}
	return name
}

// pythonBehaviorType is a behavior member's annotation: Optional for a
// value a read may leave out or hold null for, but Any, which holds None.
func pythonBehaviorType(shape behaviortypes.Shape, required bool) string {
	value := pythonBehaviorValueType(shape)
	if (!required || shape.Nullable) && value != "Any" {
		return "Optional[" + value + "]"
	}
	return value
}

// pythonBehaviorValueType is the annotation of one value of a shape: a
// string of listed values is a Literal of them.
func pythonBehaviorValueType(shape behaviortypes.Shape) string {
	switch shape.Kind {
	case behaviortypes.KindString:
		if len(shape.Values) == 0 {
			return "str"
		}
		quoted := make([]string, len(shape.Values))
		for i, value := range shape.Values {
			quoted[i] = `"` + pythonString(value) + `"`
		}
		return "Literal[" + strings.Join(quoted, ", ") + "]"
	case behaviortypes.KindInteger:
		return "int"
	case behaviortypes.KindNumber:
		return "float"
	case behaviortypes.KindBoolean:
		return "bool"
	case behaviortypes.KindArray:
		return "List[" + pythonBehaviorType(*shape.Elem, !shape.Elem.Nullable) + "]"
	case behaviortypes.KindMap:
		return "Dict[str, " + pythonBehaviorType(*shape.Elem, !shape.Elem.Nullable) + "]"
	case behaviortypes.KindStruct, behaviortypes.KindEnum:
		return shape.Ref
	}
	return "Any"
}

// pythonBehaviorDefault is a behavior member's Field default: required
// (...) for one every read holds, None for one a read may leave out.
func pythonBehaviorDefault(member behaviortypes.Member) string {
	if member.Required {
		return "..."
	}
	return "default=None"
}

// leavesFirst lists a type's structs with each after the structs it
// holds: Build lists each before them, so reversed.
func leavesFirst(structs []behaviortypes.Struct) []behaviortypes.Struct {
	out := slices.Clone(structs)
	slices.Reverse(out)
	return out
}
