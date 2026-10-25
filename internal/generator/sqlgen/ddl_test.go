package sqlgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// ddlSchema is a soft-deletable table with indexes of every option, and
// raw SQL at both places (D81).
func ddlSchema() *ir.Schema {
	s := ir.NewSchema("ddl", ir.SchemaKindDB)
	text := ir.TypeRef{Name: "string"}
	s.Types["Note"] = &ir.TypeDef{
		Name: "Note", Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Key: true},
			{Name: "slug", TypeRef: text, Required: true},
			{Name: "body", TypeRef: text, Required: true},
			{Name: "postedAt", TypeRef: ir.TypeRef{Name: "Temporal.DateTime"}, Required: true},
			{Name: "deletedAt", TypeRef: ir.TypeRef{Name: "Temporal.DateTime"}},
		},
		Indexes: []ir.IndexDef{
			{Keys: []string{"postedAt"}, Method: ir.IndexMethodBRIN},
			{Keys: []string{"body"}, Name: "body_trgm", Method: ir.IndexMethodGIN, Ops: "gin_trgm_ops"},
			{Keys: []string{"slug"}, Name: "live_slug", Unique: true, Where: "slug <> ''"},
		},
		RawSQL: []ir.RawSQLDef{
			{ID: "note_words", Up: "CREATE OR REPLACE FUNCTION note_words(t text) RETURNS int AS $$ SELECT array_length(regexp_split_to_array(t, '\\s+'), 1) $$ LANGUAGE sql IMMUTABLE", After: ir.RawSQLAfterTables},
			{ID: "note_digest", Up: "CREATE OR REPLACE VIEW note_digest AS SELECT id, note_words(body) AS words FROM note", Down: "DROP VIEW IF EXISTS note_digest"},
		},
	}
	s.Scalars["Temporal.DateTime"] = &ir.ScalarDef{Name: "Temporal.DateTime", LanguagePrimitive: ir.LanguageString, TypeMappings: map[string]string{"sql": "TIMESTAMPTZ"}}
	return s
}

// TestIndexOptionsAndRawSQL: an @index's method, operator class and
// predicate reach create.sql, the operator class enables its extension, a
// unique index's predicate is ANDed with the soft-delete one, and the raw
// SQL runs at its place, its down first in drop.sql (D81).
func TestIndexOptionsAndRawSQL(t *testing.T) {
	out, err := Generate(ddlSchema(), Options{SchemaName: "ddl"})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := WriteDDL(out, dir); err != nil {
		t.Fatal(err)
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	create, drop := read("create.sql"), read("drop.sql")
	for _, want := range []string{
		"CREATE EXTENSION IF NOT EXISTS pg_trgm;",
		"CREATE INDEX idx_note_posted_at ON note USING BRIN (posted_at);",
		"CREATE INDEX idx_note_body_trgm ON note USING GIN (body gin_trgm_ops);",
		"CREATE UNIQUE INDEX uq_note_live_slug ON note USING BTREE (slug) WHERE (slug <> '') AND deleted_at IS NULL;",
		"-- note_words (Note)\nCREATE OR REPLACE FUNCTION note_words",
		"-- note_digest (Note)\nCREATE OR REPLACE VIEW note_digest AS SELECT id, note_words(body) AS words FROM note;",
	} {
		if !strings.Contains(create, want) {
			t.Errorf("create.sql lacks %q:\n%s", want, create)
		}
	}
	tables, indexes, view := strings.Index(create, "CREATE OR REPLACE FUNCTION note_words"), strings.Index(create, "-- Create indexes"), strings.Index(create, "CREATE OR REPLACE VIEW note_digest")
	if !(tables < indexes && indexes < view) {
		t.Errorf("the raw SQL that runs after the tables (%d) must precede the indexes (%d), and the rest follow them (%d)", tables, indexes, view)
	}
	if down := strings.Index(drop, "DROP VIEW IF EXISTS note_digest;"); down < 0 || down > strings.Index(drop, "DROP TABLE") {
		t.Errorf("drop.sql must undo the raw SQL before it drops the tables:\n%s", drop)
	}

	// A raw SQL piece that cannot run again fails the build, as a backstop
	// to verification.
	bad := ddlSchema()
	bad.Types["Note"].RawSQL = []ir.RawSQLDef{{ID: "plain", Up: "CREATE INDEX plain ON note (body)"}}
	if _, err := Generate(bad, Options{SchemaName: "ddl"}); err == nil || !strings.Contains(err.Error(), `@rawSql "plain" up statement 1 cannot run again`) {
		t.Errorf("Generate = %v, want the idempotence refusal", err)
	}
}
