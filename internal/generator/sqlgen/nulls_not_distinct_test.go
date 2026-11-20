package sqlgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// nullsNotDistinctSchema is a table of invoice lines whose unique index over
// (invoiceId, sku) makes nulls collide (D125): one line per invoice has no
// sku.
func nullsNotDistinctSchema() *ir.Schema {
	s := ir.NewSchema("invoices", ir.SchemaKindDB)
	text := ir.TypeRef{Name: "string"}
	s.Types["InvoiceLine"] = &ir.TypeDef{
		Name: "InvoiceLine", Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: text, Required: true, Key: true},
			{Name: "invoiceId", TypeRef: text, Required: true},
			{Name: "sku", TypeRef: text},
		},
		Indexes: []ir.IndexDef{
			{Keys: []string{"invoiceId", "sku"}, Name: "invoice_sku", Unique: true, NullsNotDistinct: true},
		},
	}
	return s
}

// TestNullsNotDistinctReachesCreateSQL: a unique index that makes nulls
// collide is written with NULLS NOT DISTINCT, after its elements.
func TestNullsNotDistinctReachesCreateSQL(t *testing.T) {
	out, err := Generate(nullsNotDistinctSchema(), Options{SchemaName: "invoices"})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := WriteDDL(out, dir); err != nil {
		t.Fatal(err)
	}
	create, err := os.ReadFile(filepath.Join(dir, "create.sql"))
	if err != nil {
		t.Fatal(err)
	}
	want := "CREATE UNIQUE INDEX uq_invoice_line_invoice_sku ON invoice_line USING BTREE (invoice_id, sku) NULLS NOT DISTINCT;"
	if !strings.Contains(string(create), want) {
		t.Fatalf("create.sql lacks %q:\n%s", want, create)
	}
}

// TestNullsNotDistinctOnPostgresCreateSQL applies that create.sql to a real
// Postgres; testdata/pgcheck's TestNullsNotDistinctOnPostgres holds the
// checks: a second line of an invoice with no sku is refused, and two
// lines with different skus are not. Set
// SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL as for
// TestProjectionMigrationsOnPostgres.
func TestNullsNotDistinctOnPostgresCreateSQL(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL to apply a NULLS NOT DISTINCT index against Postgres")
	}
	out, err := Generate(nullsNotDistinctSchema(), Options{SchemaName: "invoices"})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := WriteDDL(out, dir); err != nil {
		t.Fatal(err)
	}
	runPGCheck(t, "TestNullsNotDistinctOnPostgres",
		"PGCHECK_DATABASE_URL="+dsn,
		"PGCHECK_SQL_DIR="+dir,
	)
}
