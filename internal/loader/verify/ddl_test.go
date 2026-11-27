package verify

import (
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestIndexOptionsOnATable: a table's index takes the options only a
// table's index has (D83).
func TestIndexOptionsOnATable(t *testing.T) {
	schema := indexSchema()
	post := schema.Types["Post"]
	post.Indexes = []ir.IndexDef{{Keys: []string{"displayTitle"}, Expression: "lower(display_title)"}}
	r := &Result{}
	checkIndexOptions(schema, r)
	if err := r.Err(); err != nil {
		t.Fatalf("a table's expression index: %v", err)
	}
}

// TestRawSQLPlacement: @rawSql lives on a table; on a queue's message or a
// base class it is refused (D83).
func TestRawSQLPlacement(t *testing.T) {
	piece := []ir.RawSQLDef{{ID: "v", Up: "CREATE OR REPLACE VIEW v AS SELECT 1"}}
	for _, tc := range []struct {
		name string
		edit func(td *ir.TypeDef)
		want string
	}{
		{"a table", func(*ir.TypeDef) {}, ""},
		{"a queue's message", func(td *ir.TypeDef) { td.Queue = &ir.QueueDef{} }, "Comment: @rawSql is on a @queue message, which gets no table"},
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
