package sqlutil

import (
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
)

// VariantCheck is the CHECK a variant field's column takes (D149): its
// JSON holds the member its sibling column names, or nothing. Of each
// member it states the shape one level deep: the value is a JSON object,
// each required key is present and not null (a RequiredNull key present
// and null), each key present holds its JSON kind (a string, a number, a
// boolean, an object or an array), and an enum key one of its values. It
// does not state what a list, a nested object, a map or a union key holds,
// a scalar's format, a number's integrality, a field's own rules (lengths,
// ranges, patterns), or a @strictJSON member's unknown keys: those are the
// validators'. Every arm's shape has one, so every variant field gets a
// CHECK; a member whose keys the CHECK can say nothing of still holds the
// value to an object.
type VariantCheck struct {
	// By is the sibling column whose value names the member.
	By string `json:"by"`
	// Arms are the members, in the union's order, each with the values of
	// By that name it.
	Arms []VariantArmCheck `json:"arms"`
}

// VariantArmCheck is one member of a VariantCheck: the values of By that
// name it and the keys of its JSON object.
type VariantArmCheck struct {
	Values []string          `json:"values"`
	Keys   []VariantKeyCheck `json:"keys,omitempty"`
}

// VariantKeyCheck is one key of a member's JSON object: whether it is
// required, the JSON kind it holds when present (empty for any), and an
// enum key's values.
type VariantKeyCheck struct {
	Key      string `json:"key"`
	Required bool   `json:"required,omitempty"`
	// Kind is one of string, number, boolean, object, array, null (a
	// RequiredNull key's), or empty for any JSON value.
	Kind string   `json:"kind,omitempty"`
	In   []string `json:"in,omitempty"`
}

// VariantColumnCheck returns the check of the column of field, a variant
// field of td (codegen.ResolveVariant), named column: ok is false for any
// other field. byColumn is the sibling's column, and model is the
// structural predicate of the schema's unions, which holds each member's
// fields as a decoder reads them.
func VariantColumnCheck(v *codegen.VariantField, column, byColumn string, model *codegen.ReadModel) (Check, bool, error) {
	if v == nil {
		return Check{}, false, nil
	}
	types := map[string]codegen.ReadType{}
	if model != nil {
		for _, t := range model.Types {
			types[t.Name] = t
		}
	}
	vc := &VariantCheck{By: byColumn}
	for i, member := range v.Union.Types {
		values := v.MemberValues()[i]
		if len(values) == 0 {
			continue
		}
		read, ok := types[member]
		if !ok {
			return Check{}, false, fmt.Errorf("sqlutil: %s's member %s has no fields to check", v.Union.Name, member)
		}
		arm := VariantArmCheck{Values: slices.Clone(values)}
		for _, f := range read.Fields {
			arm.Keys = append(arm.Keys, variantKey(f))
		}
		vc.Arms = append(vc.Arms, arm)
	}
	return Check{Column: column, Variant: vc}, true, nil
}

// variantKey is a member's field as the CHECK states it.
func variantKey(f codegen.ReadField) VariantKeyCheck {
	key := VariantKeyCheck{Key: f.Name, Required: f.Required}
	switch {
	case f.Kind == codegen.ReadNull:
		key.Kind, key.Required = "null", true
	case f.Map:
		key.Kind = "object"
	case f.Depth > 0:
		key.Kind = "array"
	case f.Kind == codegen.ReadString:
		key.Kind = "string"
	case f.Kind == codegen.ReadNumber:
		key.Kind = "number"
	case f.Kind == codegen.ReadBoolean:
		key.Kind = "boolean"
	case f.Kind == codegen.ReadEnum:
		key.Kind, key.In = "string", slices.Clone(f.Values)
	case f.Kind == codegen.ReadObject || f.Kind == codegen.ReadUnionKind:
		key.Kind = "object"
	case f.Kind == codegen.ReadScalar:
		switch f.ScalarJSON {
		case codegen.ReadString:
			key.Kind = "string"
		case codegen.ReadNumber, codegen.ReadInteger:
			key.Kind = "number"
		case codegen.ReadBoolean:
			key.Kind = "boolean"
		case codegen.ReadJSONObject:
			key.Kind = "object"
		case codegen.ReadJSONArray:
			key.Kind = "array"
		}
	}
	return key
}

// variantDialect is how a dialect asks a JSON column about its value:
// the JSON kind of the value or of one of its keys, and a key's text.
type variantDialect struct {
	// typeOf is the column's JSON kind; typeAt is a key's, NULL when the
	// key is absent; textAt is a string key's text.
	typeOf func(col string) string
	typeAt func(col, key string) string
	textAt func(col, key string) string
	// kinds maps a JSON kind to the names the dialect's typeOf gives it.
	kinds map[string][]string
	// falsy is the dialect's false.
	falsy string
}

var postgresVariant = variantDialect{
	typeOf: func(col string) string { return "jsonb_typeof(" + col + ")" },
	typeAt: func(col, key string) string { return "jsonb_typeof(" + col + " -> " + QuoteLiteral(key) + ")" },
	textAt: func(col, key string) string { return "(" + col + " ->> " + QuoteLiteral(key) + ")" },
	kinds: map[string][]string{
		"string": {"string"}, "number": {"number"}, "boolean": {"boolean"},
		"object": {"object"}, "array": {"array"}, "null": {"null"},
	},
	falsy: "false",
}

var sqliteVariant = variantDialect{
	typeOf: func(col string) string { return "json_type(" + col + ")" },
	typeAt: func(col, key string) string {
		return "json_type(" + col + ", " + QuoteLiteral(sqliteJSONPath(key)) + ")"
	},
	textAt: func(col, key string) string {
		return "json_extract(" + col + ", " + QuoteLiteral(sqliteJSONPath(key)) + ")"
	},
	kinds: map[string][]string{
		"string": {"text"}, "number": {"integer", "real"}, "boolean": {"true", "false"},
		"object": {"object"}, "array": {"array"}, "null": {"null"},
	},
	falsy: "0",
}

// sqliteJSONPath is the path of an object's key: $."key".
func sqliteJSONPath(key string) string {
	return `$."` + strings.ReplaceAll(key, `"`, `\"`) + `"`
}

// expression renders the variant check in dialect d: a CASE on the
// sibling, each arm's values holding the column to nothing or to its
// member's object, and any other value, or none, to nothing. A JSON null
// is nothing, as SQL NULL is. Each arm's object test is wrapped in
// COALESCE so a test that reads NULL fails rather than passes.
func (vc *VariantCheck) expression(d variantDialect, column string, quote func(string) string) string {
	col := quote(column)
	by := quote(vc.By)
	none := "COALESCE(" + d.typeOf(col) + ", 'null') = 'null'"
	var b strings.Builder
	b.WriteString("CASE")
	for _, arm := range vc.Arms {
		values := make([]string, len(arm.Values))
		for i, v := range arm.Values {
			values[i] = QuoteLiteral(v)
		}
		terms := []string{d.typeOf(col) + " = 'object'"}
		for _, key := range arm.Keys {
			if term := keyTerm(d, col, key); term != "" {
				terms = append(terms, term)
			}
		}
		match := by + " = " + values[0]
		if len(values) > 1 {
			match = by + " IN (" + strings.Join(values, ", ") + ")"
		}
		fmt.Fprintf(&b, " WHEN %s THEN %s OR COALESCE(%s, %s)", match, none, strings.Join(terms, " AND "), d.falsy)
	}
	fmt.Fprintf(&b, " ELSE %s END", none)
	return b.String()
}

// keyTerm is one key's test in dialect d, or "" for an optional key of any
// JSON value.
func keyTerm(d variantDialect, col string, key VariantKeyCheck) string {
	at := d.typeAt(col, key.Key)
	kinds := d.kinds[key.Kind]
	is := func(names []string) string {
		quoted := make([]string, len(names))
		for i, name := range names {
			quoted[i] = QuoteLiteral(name)
		}
		if len(quoted) == 1 {
			return at + " = " + quoted[0]
		}
		return at + " IN (" + strings.Join(quoted, ", ") + ")"
	}
	holds := at + " <> 'null'"
	if key.Kind != "" {
		holds = is(kinds)
	}
	if len(key.In) > 0 {
		values := make([]string, len(key.In))
		for i, v := range key.In {
			values[i] = QuoteLiteral(v)
		}
		holds = "(" + holds + " AND " + d.textAt(col, key.Key) + " IN (" + strings.Join(values, ", ") + "))"
	}
	switch {
	case key.Kind == "null", key.Required:
		return holds
	case key.Kind == "":
		return ""
	}
	return "(COALESCE(" + at + ", 'null') = 'null' OR " + holds + ")"
}

// variantBreach says what a row that fails a variant check holds, ending a
// sentence.
func (vc *VariantCheck) breach() string {
	var named []string
	for _, arm := range vc.Arms {
		named = append(named, arm.Values...)
	}
	return fmt.Sprintf("a value its %s names no member for, or one not in the shape of the member %s names (%s)", vc.By, vc.By, strings.Join(named, ", "))
}

// Breach is what a row that fails the check holds, ending a sentence, for
// a check whose rule is a variant's; "" for any other.
func (c Check) Breach() string {
	if c.Variant == nil {
		return ""
	}
	return c.Variant.breach()
}
