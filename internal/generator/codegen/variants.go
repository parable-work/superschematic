package codegen

import (
	"fmt"
	"slices"
	"sort"

	ir "github.com/parable-work/superschematic/ir"
)

// VariantField is a field whose type is a variant union (ir.UnionDef.By,
// D149) as every generator and validator reads it: the field, the sibling
// field whose value names its member, the union and its arms. The rule
// every validator holds it to, given the payload's (or value's) sibling:
//
//   - a required field absent or null is "required", as any field is;
//   - a value present while the sibling holds a string Arms maps is checked
//     as that arm's member: "type" when it is no object, else the member's
//     own errors under the field's path;
//   - a value present while the sibling holds no string Arms maps (another
//     value, none, null, or a value of another JSON type) is "variant"
//     (ir.VariantRule), and so is a Go or Rust value holding a member
//     other than the one the sibling names.
//
// The sibling's own rules (its enum, its presence) are its own field's.
type VariantField struct {
	Field *ir.FieldDef
	By    *ir.FieldDef
	Union *ir.UnionDef
	// Enum is By's enum, nil when By is a string.
	Enum *ir.EnumDef
	// Arms are the union's arms in ir.UnionDef.ArmList order.
	Arms []ir.UnionArm
}

// FieldKey is the JSON key of the variant field.
func (v *VariantField) FieldKey() string { return JSONKey(v.Field) }

// ByKey is the JSON key of the sibling.
func (v *VariantField) ByKey() string { return JSONKey(v.By) }

// JSONKey is a field's key in a JSON payload: its JSONTag, else its name.
func JSONKey(field *ir.FieldDef) string {
	if field.JSONTag != "" {
		return field.JSONTag
	}
	return field.Name
}

// ArmOf is the member the sibling's value names, and whether it names one.
func (v *VariantField) ArmOf(value string) (string, bool) {
	member, ok := v.Union.Arms[value]
	return member, ok
}

// MemberValues lists, for each member of the union in Types order, the
// values of By that name it.
func (v *VariantField) MemberValues() [][]string {
	out := make([][]string, len(v.Union.Types))
	for _, arm := range v.Arms {
		if i := slices.Index(v.Union.Types, arm.Type); i >= 0 {
			out[i] = append(out[i], arm.Value)
		}
	}
	return out
}

// LookupUnion finds a union by name, the schema's own first, then the
// dependencies' in name order.
func LookupUnion(schema *ir.Schema, deps map[string]*ir.Schema, name string) *ir.UnionDef {
	if schema != nil && schema.Unions[name] != nil {
		return schema.Unions[name]
	}
	for _, dep := range sortedDeps(deps) {
		if dep.Unions[name] != nil {
			return dep.Unions[name]
		}
	}
	return nil
}

// LookupEnum finds an enum by name, the schema's own first, then the
// dependencies' in name order.
func LookupEnum(schema *ir.Schema, deps map[string]*ir.Schema, name string) *ir.EnumDef {
	if schema != nil && schema.Enums[name] != nil {
		return schema.Enums[name]
	}
	for _, dep := range sortedDeps(deps) {
		if dep.Enums[name] != nil {
			return dep.Enums[name]
		}
	}
	return nil
}

func sortedDeps(deps map[string]*ir.Schema) []*ir.Schema {
	names := make([]string, 0, len(deps))
	for name, dep := range deps {
		if dep != nil {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	out := make([]*ir.Schema, len(names))
	for i, name := range names {
		out[i] = deps[name]
	}
	return out
}

// IsVariantUnion reports whether name is a variant union of the schema or
// its dependencies.
func IsVariantUnion(schema *ir.Schema, deps map[string]*ir.Schema, name string) bool {
	return LookupUnion(schema, deps, name).IsVariant()
}

// ResolveVariant reads field of td as a variant field: nil when its type is
// no variant union, and an error when the field or its sibling breaks the
// rules a variant field keeps (D149): it is a single value, not a list or a
// map, with no default; the type has a field named by the union's By; and
// that sibling is a single string or enum, whose enum, when it is one, has
// a member for every arm's value. union and enum look a name up where the
// caller's schema and dependencies declare it.
func ResolveVariant(td *ir.TypeDef, field *ir.FieldDef, union func(string) *ir.UnionDef, enum func(string) *ir.EnumDef) (*VariantField, error) {
	if td == nil || field == nil {
		return nil, nil
	}
	u := union(field.TypeRef.Name)
	if !u.IsVariant() {
		return nil, nil
	}
	where := td.Name + "." + field.Name
	if field.TypeRef.IsArray || field.TypeRef.IsMap {
		return nil, fmt.Errorf("%s: a field of the variant union %s is a single value, not a list or a map: %s's member is named by the field %s beside it (D149)", where, u.Name, u.Name, u.By)
	}
	if field.Default != nil || field.PlatformDefault != "" {
		return nil, fmt.Errorf("%s: a field of the variant union %s takes no default (D149)", where, u.Name)
	}
	var by *ir.FieldDef
	for _, sibling := range td.Fields {
		if sibling != nil && sibling.Name == u.By {
			by = sibling
			break
		}
	}
	if by == nil {
		return nil, fmt.Errorf("%s: the variant union %s's member is named by the field %s, which %s has none of; declare %s beside %s (D149)", where, u.Name, u.By, td.Name, u.By, field.Name)
	}
	if by == field || by.TypeRef.IsArray || by.TypeRef.IsMap {
		return nil, fmt.Errorf("%s: the variant union %s's member is named by %s.%s, which must be a single string or enum (D149)", where, u.Name, td.Name, by.Name)
	}
	v := &VariantField{Field: field, By: by, Union: u, Arms: u.ArmList()}
	switch name := by.TypeRef.Name; {
	case name == PrimitiveString:
	case enum(name) != nil:
		v.Enum = enum(name)
		for _, arm := range v.Arms {
			if v.Enum.MemberSerializedAs(arm.Value) == nil {
				return nil, fmt.Errorf("%s: the variant union %s's arm %q is no value of %s.%s's enum %s (D149)", where, u.Name, arm.Value, td.Name, by.Name, name)
			}
		}
	default:
		return nil, fmt.Errorf("%s: the variant union %s's member is named by %s.%s, which must be a string or an enum, not %s (D149)", where, u.Name, td.Name, by.Name, name)
	}
	return v, nil
}

// VariantFieldsOf returns td's variant fields in field order (ResolveVariant),
// looked up in the schema and its dependencies, or the first error.
func VariantFieldsOf(td *ir.TypeDef, schema *ir.Schema, deps map[string]*ir.Schema) ([]*VariantField, error) {
	if td == nil {
		return nil, nil
	}
	union := func(name string) *ir.UnionDef { return LookupUnion(schema, deps, name) }
	enum := func(name string) *ir.EnumDef { return LookupEnum(schema, deps, name) }
	var out []*VariantField
	for _, field := range td.Fields {
		v, err := ResolveVariant(td, field, union, enum)
		if err != nil {
			return nil, err
		}
		if v != nil {
			out = append(out, v)
		}
	}
	return out, nil
}

// VariantFieldOf is field's variant in td (ResolveVariant), nil when the
// field is none or breaks the rules, which the loader has refused.
func VariantFieldOf(td *ir.TypeDef, field *ir.FieldDef, schema *ir.Schema, deps map[string]*ir.Schema) *VariantField {
	v, _ := ResolveVariant(td, field,
		func(name string) *ir.UnionDef { return LookupUnion(schema, deps, name) },
		func(name string) *ir.EnumDef { return LookupEnum(schema, deps, name) })
	return v
}
