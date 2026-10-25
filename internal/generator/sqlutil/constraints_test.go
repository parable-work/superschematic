package sqlutil

import (
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

func strPtr(s string) *string     { return &s }
func floatPtr(f float64) *float64 { return &f }
func intPtr(n int) *int           { return &n }

// TestColumnDefault: a required field's Default<T, V> is its column's
// DEFAULT when V has a literal form in the column's type; every other field
// keeps its default in the decoders (D81).
func TestColumnDefault(t *testing.T) {
	field := func(def string, required bool, mods ...func(*ir.FieldDef)) *ir.FieldDef {
		f := &ir.FieldDef{Name: "f", TypeRef: ir.TypeRef{Name: "x"}, Required: required, Default: strPtr(def)}
		for _, m := range mods {
			m(f)
		}
		return f
	}
	list := func(f *ir.FieldDef) { f.TypeRef.IsArray = true }
	key := func(f *ir.FieldDef) { f.Key = true }
	for _, tc := range []struct {
		name   string
		field  *ir.FieldDef
		column string
		want   string
	}{
		{"text", field("placed", true), "TEXT", "'placed'"},
		{"quoted text", field("it's", true), "VARCHAR(80)", "'it''s'"},
		{"empty text", field("", true), "CITEXT", "''"},
		{"boolean", field("false", true), "BOOLEAN", "false"},
		{"integer", field("-5", true), "BIGINT", "-5"},
		{"float", field("0.5", true), "DOUBLE PRECISION", "0.5"},
		{"float as integer", field("5", true), "DOUBLE PRECISION", "5"},
		{"numeric", field("12.50", true), "NUMERIC(10,2)", "12.50"},
		{"exponent", field("1e3", true), "REAL", "1e3"},
		{"not a number", field("ten", true), "REAL", ""},
		{"optional", field("placed", false), "TEXT", ""},
		{"key", field("placed", true, key), "TEXT", ""},
		{"list", field("[]", true, list), "TEXT[]", ""},
		{"uuid", field("00000000-0000-4000-8000-000000000001", true), "UUID", ""},
		{"timestamp", field("2026-01-02T03:04:05Z", true), "TIMESTAMPTZ", ""},
		{"jsonb", field("{}", true), "JSONB", ""},
		{"not a boolean", field("yes", true), "BOOLEAN", ""},
		{"not an integer", field("1.5", true), "BIGINT", ""},
	} {
		got, ok := ColumnDefault(tc.field, tc.column)
		if got != tc.want || ok != (tc.want != "") {
			t.Errorf("%s: ColumnDefault = %q, %v; want %q", tc.name, got, ok, tc.want)
		}
	}
	if _, ok := ColumnDefault(&ir.FieldDef{Name: "f", Required: true}, "TEXT"); ok {
		t.Error("a field with no default has none")
	}
}

// TestColumnCheck: an enum column holds its members, a number column its
// Validate bounds and a text column its length bounds, each as one CHECK;
// a list, a JSON value and a pattern get none.
func TestColumnCheck(t *testing.T) {
	enum := &ir.EnumDef{Name: "Status", Values: []ir.EnumValueDef{{Name: "Placed", SerializedAs: "placed"}, {Name: "Shipped"}, {Name: "It", SerializedAs: "it's"}}}
	for _, tc := range []struct {
		name   string
		field  *ir.FieldDef
		column string
		enum   *ir.EnumDef
		want   string
	}{
		{"enum", &ir.FieldDef{TypeRef: ir.TypeRef{Name: "Status"}}, "TEXT", enum, `status IN ('placed', 'Shipped', 'it''s')`},
		{"bounds", &ir.FieldDef{ValidateMin: floatPtr(0), ValidateMax: floatPtr(1.5)}, "DOUBLE PRECISION", nil, "status >= 0 AND status <= 1.5"},
		{"minimum", &ir.FieldDef{ValidateMin: floatPtr(-10)}, "BIGINT", nil, "status >= -10"},
		{"lengths", &ir.FieldDef{ValidateMinLength: intPtr(1), ValidateMaxLength: intPtr(60)}, "VARCHAR(80)", nil, "length(status) >= 1 AND length(status) <= 60"},
		{"pattern", &ir.FieldDef{ValidatePattern: "^a$"}, "TEXT", nil, ""},
		{"lengths on a number", &ir.FieldDef{ValidateMaxLength: intPtr(3)}, "BIGINT", nil, ""},
		{"list of an enum", &ir.FieldDef{TypeRef: ir.TypeRef{Name: "Status", IsArray: true}}, "TEXT[]", enum, ""},
		{"json", &ir.FieldDef{JsonField: true, ValidateMax: floatPtr(1)}, "JSONB", nil, ""},
	} {
		check, ok := ColumnCheck(tc.field, "status", tc.column, tc.enum)
		got := ""
		if ok {
			got = check.Expression(QuoteIdentifier)
		}
		if got != tc.want {
			t.Errorf("%s: check %q, want %q", tc.name, got, tc.want)
		}
	}
	quoted, _ := ColumnCheck(&ir.FieldDef{ValidateMax: floatPtr(5)}, "order", "BIGINT", nil)
	if got := quoted.Expression(QuoteIdentifier); got != `"order" <= 5` {
		t.Errorf("a keyword column is quoted: %s", got)
	}
}

func TestCheckName(t *testing.T) {
	if got := CheckName("line_item", "kind"); got != "ck_line_item_kind" {
		t.Errorf("CheckName = %s", got)
	}
}

func TestValidOpclass(t *testing.T) {
	for s, want := range map[string]bool{
		"gin_trgm_ops": true, "public.vector_cosine_ops": true, "text_pattern_ops": true,
		"": false, "Gin": false, "a b": false, "ops; DROP": false,
	} {
		if ValidOpclass(s) != want {
			t.Errorf("ValidOpclass(%q) = %v", s, !want)
		}
	}
}
