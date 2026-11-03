package tsgen

import (
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// TestAColumnDecodesOneWay: the codec reads a column as an Encrypted<T>
// envelope (D85), a vector or a decimal (D84), JSON text or JSON, and one
// of them alone, so a column that would take two fails the generation,
// whichever two (D84, D85).
func TestAColumnDecodesOneWay(t *testing.T) {
	field := &ir.FieldDef{Name: "embedding", TypeRef: ir.TypeRef{Name: "string"}}
	column := func(edit func(c *sqlutil.ColumnModel)) sqlutil.ColumnModel {
		c := sqlutil.ColumnModel{Name: "embedding", Quoted: `"embedding"`, Qualified: `"chunk"."embedding"`, Field: "embedding", Def: field, Type: "TEXT", Required: true}
		edit(&c)
		return c
	}
	info := &TypeInfo{Name: "Chunk", Fields: []FieldInfo{{Name: "embedding", TSType: "string"}}}
	for _, tc := range []struct {
		name string
		edit func(c *sqlutil.ColumnModel)
		want string
	}{
		{"an envelope alone", func(c *sqlutil.ColumnModel) { c.Encrypted = true }, ""},
		{"a vector alone", func(c *sqlutil.ColumnModel) { c.Type, c.Class.Vector = "vector(3)", true }, ""},
		{"an encrypted vector", func(c *sqlutil.ColumnModel) { c.Encrypted, c.Class.Vector = true, true }, "would decode as an envelope and as a vector"},
		{"an encrypted decimal", func(c *sqlutil.ColumnModel) { c.Encrypted, c.Class.SQLType = true, "NUMERIC" }, "would decode as an envelope and as a decimal"},
		{"a vector of JSON text", func(c *sqlutil.ColumnModel) { c.Class.Vector, c.Class.JSON = true, true }, "would decode as a vector and as JSON text"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ormColumn(column(tc.edit), info, nil, "string", nil)
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("err = %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("err = %v, want one that says %q", err, tc.want)
			}
		})
	}
}
