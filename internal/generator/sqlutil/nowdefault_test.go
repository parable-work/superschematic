package sqlutil

import (
	"testing"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// TestNowDefaults: a required date, time or timestamp column takes the
// time of the insert from Default<T, "now">, or by name when it is an
// audit timestamp or the loader's own (D88); any other such column has no
// default, and "now" on another type is its literal text.
func TestNowDefaults(t *testing.T) {
	now := codegen.DefaultNow
	for _, tc := range []struct {
		name       string
		field      ir.FieldDef
		columnType string
		want       string
	}{
		{"a timestamp", ir.FieldDef{Name: "postedAt", TypeRef: ir.TypeRef{Name: "Temporal.DateTime"}, Required: true, Default: &now}, "TIMESTAMPTZ", "CURRENT_TIMESTAMP"},
		{"a date", ir.FieldDef{Name: "postedOn", TypeRef: ir.TypeRef{Name: "Temporal.Date"}, Required: true, Default: &now}, "DATE", "CURRENT_DATE"},
		{"a time", ir.FieldDef{Name: "pouredAt", TypeRef: ir.TypeRef{Name: "Temporal.Time"}, Required: true, Default: &now}, "TIME", "CURRENT_TIME"},
		{"an optional timestamp", ir.FieldDef{Name: "seenAt", TypeRef: ir.TypeRef{Name: "Temporal.DateTime"}, Default: &now}, "TIMESTAMPTZ", ""},
		{"a list of timestamps", ir.FieldDef{Name: "seenAt", TypeRef: ir.TypeRef{Name: "Temporal.DateTime", IsArray: true}, Required: true, Default: &now}, "TIMESTAMPTZ[]", ""},
		{"text", ir.FieldDef{Name: "label", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Default: &now}, "TEXT", "'now'"},
	} {
		got, ok := ColumnDefault(&tc.field, tc.columnType)
		if got != tc.want || ok != (tc.want != "") {
			t.Errorf("%s: ColumnDefault = %q, %v; want %q", tc.name, got, ok, tc.want)
		}
	}

	at := &ir.FieldDef{Name: "date", TypeRef: ir.TypeRef{Name: "Temporal.DateTime"}, Required: true}
	created := &ir.FieldDef{Name: AuditCreatedAt, TypeRef: at.TypeRef, Required: true}
	updated := &ir.FieldDef{Name: AuditUpdatedAt, TypeRef: at.TypeRef, Required: true}
	table := &ir.TypeDef{Name: "LineItem"}
	session := &ir.TypeDef{Name: ir.IdentitySessionTable, Origin: ir.OriginIdentity}
	commit := &ir.TypeDef{Name: "RecipeCommit", Origin: ir.OriginVersionGraph}
	for _, tc := range []struct {
		name  string
		td    *ir.TypeDef
		field *ir.FieldDef
		want  bool
	}{
		{"a posting date", table, at, false},
		{"createdAt", table, created, true},
		{"updatedAt", table, updated, true},
		{"a session's timestamp", session, &ir.FieldDef{Name: ir.IdentitySessionExpiresAtField, TypeRef: at.TypeRef, Required: true}, true},
		{"a version graph's timestamp", commit, at, true},
	} {
		if got := ImpliedNowDefault(tc.td, tc.field); got != tc.want {
			t.Errorf("%s: ImpliedNowDefault = %v, want %v", tc.name, got, tc.want)
		}
	}
	if got := NowDefault("TEXT", "string"); got != "" {
		t.Errorf("NowDefault of text = %q, want none", got)
	}
}
