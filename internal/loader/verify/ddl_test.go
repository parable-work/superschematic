package verify

import (
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestIndexOptionsOnEngineType: the engine keeps an engine type's index
// over its fields alone, so the options only a table's index has are
// refused there, and accepted on a table (D78).
func TestIndexOptionsOnEngineType(t *testing.T) {
	schema := indexSchema()
	post := schema.Types["Post"]
	post.Indexes = []ir.IndexDef{{Keys: []string{"displayTitle"}, Expression: "lower(display_title)"}}
	r := &Result{}
	checkIndexOptions(schema, r)
	if err := r.Err(); err != nil {
		t.Fatalf("a table's expression index: %v", err)
	}

	post.Behaviors = []ir.BehaviorRef{{Name: "Comments"}}
	r = &Result{}
	checkIndexOptions(schema, r)
	want := `Post: @index(["displayTitle"]) sets method, ops, where, expression or concurrently, which only a table's index has; Post is an engine type`
	if err := r.Err(); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("an engine type's expression index: %v, want %q", err, want)
	}
}

// TestRawSQLPlacement: @rawSql lives on a table; on a queue's message, a
// base class or an engine type it is refused (D78).
func TestRawSQLPlacement(t *testing.T) {
	piece := []ir.RawSQLDef{{ID: "v", Up: "CREATE OR REPLACE VIEW v AS SELECT 1"}}
	for _, tc := range []struct {
		name string
		edit func(td *ir.TypeDef)
		want string
	}{
		{"a table", func(*ir.TypeDef) {}, ""},
		{"a queue's message", func(td *ir.TypeDef) { td.Queue = &ir.QueueDef{} }, "Comment: @rawSql is on a @queue message, which gets no table"},
		{"an engine type", func(td *ir.TypeDef) { td.Behaviors = []ir.BehaviorRef{{Name: "Comments"}} }, "Comment: @rawSql is on an engine type"},
	} {
		schema := indexSchema()
		comment := schema.Types["Comment"]
		comment.RawSQL = piece
		tc.edit(comment)
		r := &Result{}
		checkRawSQL(schema, r)
		err := r.Err()
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: %v, want %q", tc.name, err, tc.want)
		}
	}
}
