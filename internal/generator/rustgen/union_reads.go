package rustgen

import (
	"strconv"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
)

// rustReadInterpreter is the Rust interpreter of the structural predicate
// (codegen.ReadModel, D31 amended), which validators.rs writes after the
// model's tables: pick_union_member, which every union's Deserialize and
// validate_<union> pick their member by.
const rustReadInterpreter = `
/// One field of a type as the structural predicate reads it
/// (codegen.ReadModel, D31 amended): the Go, TypeScript and Python unions
/// hold the same table.
#[allow(dead_code)]
struct ReadField {
    name: &'static str,
    required: bool,
    depth: u8,
    is_map: bool,
    kind: ReadKind,
}

/// The kind of a field's values. A scalar is held to its validator, the
/// scalar's format, or else to its JSON kind.
#[allow(dead_code)]
enum ReadKind {
    String,
    Number,
    Boolean,
    Enum(&'static [&'static str]),
    Scalar(&'static str, Option<fn(Option<&Value>) -> rt::ScalarResult>),
    Object(&'static str),
    Union(&'static str),
    Any,
}

/// One object type of the predicate.
struct ReadType {
    name: &'static str,
    strict: bool,
    fields: &'static [ReadField],
}

/// One member of a union: its type, the discriminator value that names it,
/// and its shape.
#[allow(dead_code)]
struct ReadMember {
    type_name: &'static str,
    tag: &'static str,
    fields: &'static [&'static str],
    tags: &'static [(&'static str, &'static str)],
}

/// One union of the predicate.
struct ReadUnion {
    name: &'static str,
    discriminator: Option<&'static str>,
    members: &'static [ReadMember],
}

/// Reports whether value has a scalar's JSON kind.
fn reads_json(kind: &str, value: &Value) -> bool {
    match kind {
        "string" => value.is_string(),
        "number" => value.is_number(),
        "integer" => value.is_i64() || value.is_u64() || value.as_f64().is_some_and(|number| number.fract() == 0.0),
        "boolean" => value.is_boolean(),
        "jsonObject" => value.is_object(),
        "jsonArray" => value.is_array(),
        _ => true,
    }
}

/// Reports whether one value, not null, has the field's kind.
fn reads_value(field: &ReadField, value: &Value) -> bool {
    match &field.kind {
        ReadKind::String => value.is_string(),
        ReadKind::Number => value.is_number(),
        ReadKind::Boolean => value.is_boolean(),
        ReadKind::Enum(values) => value.as_str().is_some_and(|text| values.contains(&text)),
        ReadKind::Scalar(json, check) => match check {
            Some(check) => check(Some(value)).is_ok(),
            None => reads_json(json, value),
        },
        ReadKind::Object(name) => reads_type(name, value),
        ReadKind::Union(name) => value.is_object() && pick_union_member(name, value).is_some(),
        ReadKind::Any => true,
    }
}

/// Reports whether value holds depth list levels of the field's values, no
/// element null.
fn reads_levels(field: &ReadField, value: &Value, depth: u8) -> bool {
    if depth == 0 {
        return reads_value(field, value);
    }
    value
        .as_array()
        .is_some_and(|items| items.iter().all(|item| !item.is_null() && reads_levels(field, item, depth - 1)))
}

/// The structural predicate: whether value decodes under the type's types.
/// It is a JSON object; a @strictJSON type's holds no key the type does not
/// declare; each required field is present and not null; and each field
/// present and not null has its kind at every list level and in every map
/// value, none of which is null. A field's own rules take no part.
fn reads_type(name: &str, value: &Value) -> bool {
    let Some(object) = value.as_object() else {
        return false;
    };
    let Some(read) = READ_TYPES.iter().find(|read| read.name == name) else {
        return true;
    };
    if read.strict && !object.keys().all(|key| read.fields.iter().any(|field| field.name == key)) {
        return false;
    }
    read.fields.iter().all(|field| match object.get(field.name) {
        None | Some(Value::Null) => !field.required,
        Some(field_value) if field.is_map => field_value.as_object().is_some_and(|entries| {
            entries
                .values()
                .all(|entry| !entry.is_null() && reads_levels(field, entry, field.depth))
        }),
        Some(field_value) => reads_levels(field, field_value, field.depth),
    })
}

/// The index of the member of union name that value is, or None. A
/// discriminated union's only candidate is the member its discriminator
/// names. Any other union's candidates are the members whose fields include
/// every payload key and whose tag values the payload does not contradict,
/// then, when none of those reads it, the members whose tags the payload
/// allows. The first candidate that reads the payload (reads_type) is the
/// member, as the Go, TypeScript and Python unions pick it.
pub fn pick_union_member(name: &str, value: &Value) -> Option<usize> {
    let payload = value.as_object()?;
    let union = READ_UNIONS.iter().find(|union| union.name == name)?;
    if let Some(discriminator) = union.discriminator {
        let tag = payload.get(discriminator).and_then(Value::as_str)?;
        let index = union.members.iter().position(|member| member.tag == tag)?;
        return reads_type(union.members[index].type_name, value).then_some(index);
    }
    let tags_allow = |member: &ReadMember| {
        member
            .tags
            .iter()
            .all(|(field, tag)| payload.get(*field).is_none_or(|raw| raw.as_str() == Some(*tag)))
    };
    union
        .members
        .iter()
        .position(|member| {
            payload.keys().all(|key| member.fields.contains(&key.as_str()))
                && tags_allow(member)
                && reads_type(member.type_name, value)
        })
        // No member declares every payload key and reads it: tolerate the
        // unknown keys.
        .or_else(|| {
            union
                .members
                .iter()
                .position(|member| tags_allow(member) && reads_type(member.type_name, value))
        })
}
`

// renderRustReads renders the read model's tables and its interpreter for
// validators.rs. scalarFns maps a scalar's canonical name to its validator
// in validators::scalars; a scalar without one is held to its JSON kind.
func renderRustReads(model *codegen.ReadModel, scalarFns map[string]string) string {
	var b strings.Builder
	b.WriteString("\n/// The object types the unions reach, as the structural predicate reads them.\n")
	b.WriteString("static READ_TYPES: &[ReadType] = &[\n")
	for _, t := range model.Types {
		b.WriteString("    ReadType {\n        name: " + strconv.Quote(t.Name) + ",\n        strict: " + strconv.FormatBool(t.Strict) + ",\n        fields: &[\n")
		for _, f := range t.Fields {
			b.WriteString("            ReadField { name: " + strconv.Quote(f.Name) + ", required: " + strconv.FormatBool(f.Required) +
				", depth: " + strconv.Itoa(f.Depth) + ", is_map: " + strconv.FormatBool(f.Map) + ", kind: " + rustReadKind(f, scalarFns) + " },\n")
		}
		b.WriteString("        ],\n    },\n")
	}
	b.WriteString("];\n\n/// The unions the predicate picks members of.\nstatic READ_UNIONS: &[ReadUnion] = &[\n")
	for _, u := range model.Unions {
		discriminator := "None"
		if u.IsDiscriminated() {
			discriminator = "Some(" + strconv.Quote(u.Discriminator) + ")"
		}
		b.WriteString("    ReadUnion {\n        name: " + strconv.Quote(u.Name) + ",\n        discriminator: " + discriminator + ",\n        members: &[\n")
		for _, m := range u.Members {
			tags := make([]string, len(m.Tags))
			for i, tag := range m.Tags {
				tags[i] = "(" + strconv.Quote(tag.Field) + ", " + strconv.Quote(tag.Value) + ")"
			}
			b.WriteString("            ReadMember { type_name: " + strconv.Quote(m.Type) + ", tag: " + strconv.Quote(m.Tag) +
				", fields: &[" + strings.Join(quoteAll(m.Fields), ", ") + "], tags: &[" + strings.Join(tags, ", ") + "] },\n")
		}
		b.WriteString("        ],\n    },\n")
	}
	b.WriteString("];\n")
	b.WriteString(rustReadInterpreter)
	return b.String()
}

// rustReadKind writes a field's ReadKind.
func rustReadKind(f codegen.ReadField, scalarFns map[string]string) string {
	switch f.Kind {
	case codegen.ReadString:
		return "ReadKind::String"
	case codegen.ReadNumber:
		return "ReadKind::Number"
	case codegen.ReadBoolean:
		return "ReadKind::Boolean"
	case codegen.ReadEnum:
		return "ReadKind::Enum(&[" + strings.Join(quoteAll(f.Values), ", ") + "])"
	case codegen.ReadScalar:
		check := "None"
		if fn := scalarFns[f.Scalar]; fn != "" {
			check = "Some(" + fn + ")"
		}
		return "ReadKind::Scalar(" + strconv.Quote(f.ScalarJSON) + ", " + check + ")"
	case codegen.ReadObject:
		return "ReadKind::Object(" + strconv.Quote(f.Ref) + ")"
	case codegen.ReadUnionKind:
		return "ReadKind::Union(" + strconv.Quote(f.Ref) + ")"
	}
	return "ReadKind::Any"
}

// quoteAll quotes each value as a Rust string literal.
func quoteAll(values []string) []string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = strconv.Quote(v)
	}
	return quoted
}
