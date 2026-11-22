package verify

import (
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

// TestTheUserTableRuleReadsTheNamingFilesSQLTable: a User table's required
// field counts a default only as the DDL carries it. Under column_defaults
// "off" a Default<T, V> other than "now" is the decoders' alone, so the
// routes' insert has no value for it; under now_defaults "required" every
// required timestamp is the time of the insert.
func TestTheUserTableRuleReadsTheNamingFilesSQLTable(t *testing.T) {
	dateTime := &ir.ScalarDef{Name: "Temporal.DateTime", LanguagePrimitive: ir.LanguageString, TypeMappings: map[string]string{"sql": "TIMESTAMPTZ", "json_schema": "string"}}
	addAccountField := func(db *ir.Schema, fd *ir.FieldDef) {
		db.Types["Account"].Fields = append(db.Types["Account"].Fields, fd)
	}
	sqlOff := naming.Default()
	sqlOff.SQL.ColumnDefaults = naming.SQLOff
	sqlOff.SQL.NowDefaults = naming.SQLNowRequired
	plan, now := "free", "now"
	for _, tc := range []struct {
		name   string
		naming naming.Naming
		field  *ir.FieldDef
		want   string
	}{
		{name: "a literal default under the core's DDL", naming: naming.Default(),
			field: &ir.FieldDef{Name: "plan", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Default: &plan}},
		{name: "a literal default under column_defaults off", naming: sqlOff,
			field: &ir.FieldDef{Name: "plan", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Default: &plan},
			want:  `Account.plan in the authDb users is required and has no default its column carries; make it optional, or give a date or time Default<T, "now"> (the naming file's sql.column_defaults is "off", so no other default is its column's)`},
		{name: "a declared now under column_defaults off", naming: sqlOff,
			field: &ir.FieldDef{Name: "joinedAt", TypeRef: ir.TypeRef{Name: "Temporal.DateTime"}, Required: true, Default: &now}},
		{name: "a timestamp under the core's DDL", naming: naming.Default(),
			field: &ir.FieldDef{Name: "joinedAt", TypeRef: ir.TypeRef{Name: "Temporal.DateTime"}, Required: true},
			want:  "Account.joinedAt in the authDb users is required and has no default its column carries"},
		{name: "a timestamp under now_defaults required", naming: sqlOff,
			field: &ir.FieldDef{Name: "joinedAt", TypeRef: ir.TypeRef{Name: "Temporal.DateTime"}, Required: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := Input{Naming: tc.naming, AuthDB: userModel()}
			in.AuthDB.Scalars[dateTime.Name] = dateTime
			addAccountField(in.AuthDB, tc.field)
			r := Run(routesAPI(), in)
			if tc.want == "" {
				if len(r.Errors) > 0 {
					t.Fatalf("want a clean verification, got %v", errorStrings(r))
				}
				return
			}
			if !hasError(r, tc.want) {
				t.Fatalf("want an error containing %q, got %v", tc.want, errorStrings(r))
			}
		})
	}
}
