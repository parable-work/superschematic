package rustgen

import (
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// TestTheRustORMWritesADefaultTheDDLLeavesOut: under column_defaults "off"
// a column that the core's DDL gives its Default<T, V> carries none, so the
// create writes the schema's default itself, as JSON of the column's type.
// Under the core's DDL it is the database's, and a Default<T, "now"> or a
// default no column carries is never the create's.
func TestTheRustORMWritesADefaultTheDDLLeavesOut(t *testing.T) {
	text, number, flag, now, id := "free", "3", "true", "now", "00000000-0000-4000-8000-000000000001"
	column := func(name, sqlType, irType string, def *string) sqlutil.ColumnModel {
		f := &ir.FieldDef{Name: name, TypeRef: ir.TypeRef{Name: irType}, Required: true, Default: def}
		return sqlutil.ColumnModel{Field: name, Def: f, Required: true, Default: def, Type: sqlType}
	}
	off := sqlutil.DDLPolicy{NoColumnDefaults: true}
	for _, tc := range []struct {
		c      sqlutil.ColumnModel
		policy sqlutil.DDLPolicy
		want   string
	}{
		{column("plan", "TEXT", "string", &text), off, `"free"`},
		{column("tries", "BIGINT", "int64", &number), off, `3`},
		{column("active", "BOOLEAN", "boolean", &flag), off, `true`},
		{column("plan", "TEXT", "string", &text), sqlutil.DDLPolicy{}, ``},
		{column("seenAt", "TIMESTAMPTZ", "Temporal.DateTime", &now), off, ``},
		{column("owner", "UUID", "Identity.UUID", &id), off, ``},
		{column("plan", "TEXT", "string", nil), off, ``},
	} {
		got, ok := writtenDefault(tc.c, tc.policy)
		if string(got) != tc.want || ok != (tc.want != "") {
			t.Errorf("writtenDefault(%s %s, %+v) = %s, %v; want %q", tc.c.Field, tc.c.Type, tc.policy, got, ok, tc.want)
		}
	}
}
