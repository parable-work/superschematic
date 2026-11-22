package sqlmigrate

import "testing"

// TestPostgresIndexWritesNullsNotDistinct: a migration plan creates a
// unique index that makes nulls collide with NULLS NOT DISTINCT, and an
// index that changes only in that flag is replaced, since the two do not
// accept the same rows (D125).
func TestPostgresIndexWritesNullsNotDistinct(t *testing.T) {
	idx := &Index{Name: "uq_invoice_line_sku", Columns: []string{"invoice_id", "sku"}, Method: "BTREE", Unique: true, NullsNotDistinct: true}
	got := indexSQL("invoice_line", idx, false)
	want := `CREATE UNIQUE INDEX uq_invoice_line_sku ON invoice_line USING BTREE (invoice_id, sku) NULLS NOT DISTINCT`
	if got != want {
		t.Fatalf("indexSQL = %q, want %q", got, want)
	}

	from := &Table{Name: "invoice_line", Indexes: []*Index{{Name: "uq_invoice_line_sku", Columns: []string{"invoice_id", "sku"}, Method: "BTREE", Unique: true}}}
	to := &Table{Name: "invoice_line", Indexes: []*Index{idx}}
	d := &differ{}
	d.diffIndexes(from, to, func(columns []string) []string { return columns })
	if len(d.changes) != 1 || d.changes[0].op != opReplaceIndex || d.changes[0].index != idx {
		t.Fatalf("changing nullsNotDistinct planned %+v, want the index replaced", d.changes)
	}
}
