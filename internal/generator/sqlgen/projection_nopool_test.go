package sqlgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
)

// TestProjectionWithoutPool generates fixture-finances-db, whose
// TransactionListRow projection has no pool (D90): its view is created
// beside the tables, with no CREATE SCHEMA, in create.sql, drop.sql and its
// migration pair, whose files, Arrow schema and docs file are named for
// the view alone; with outputs.sql.sourceSchema it is created in that
// schema, beside the tables it reads.
func TestProjectionWithoutPool(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-finances-db"))
	if err != nil {
		t.Fatal(err)
	}
	output, err := Generate(schema, Options{SchemaName: "fixture-finances-db"})
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Projections) != 1 {
		t.Fatalf("projections = %d, want one", len(output.Projections))
	}
	view := output.Projections[0]
	if view.Relation != "transaction_list" || view.QualifiedView != "transaction_list" || view.Schema != "" || view.QuotedPool != "" {
		t.Errorf("view = %s (%s) in schema %q", view.Relation, view.QualifiedView, view.Schema)
	}
	if view.UpFileName != "20261026120000_transaction_list_projection.up.sql" || view.DownFileName != "20261026120000_transaction_list_projection.down.sql" {
		t.Errorf("migration pair = %s, %s", view.UpFileName, view.DownFileName)
	}
	dir := t.TempDir()
	if err := WriteDDL(output, dir); err != nil {
		t.Fatal(err)
	}
	if err := WriteProjections(schema, output, dir, dir); err != nil {
		t.Fatal(err)
	}
	read := func(name string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	create, up := read("create.sql"), read(view.UpFileName)
	for name, text := range map[string]string{"create.sql": create, view.UpFileName: up} {
		if strings.Contains(text, "CREATE SCHEMA") {
			t.Errorf("%s creates a schema for a view without a pool:\n%s", name, text)
		}
		if !strings.Contains(text, "CREATE VIEW transaction_list WITH (security_barrier = true) AS") {
			t.Errorf("%s does not create the view beside the tables:\n%s", name, text)
		}
	}
	if !strings.Contains(read("drop.sql"), "DROP VIEW IF EXISTS transaction_list;") || !strings.Contains(read(view.DownFileName), "DROP VIEW IF EXISTS transaction_list;") {
		t.Error("drop.sql and the down migration drop the view by its name")
	}
	for _, name := range []string{"transaction_list.arrow.json", "transaction_list.docs.json"} {
		if _, err := os.Stat(filepath.Join(dir, ProjectionsSubdir, name)); err != nil {
			t.Errorf("artifact %s: %v", name, err)
		}
	}

	scoped, err := Generate(schema, Options{SchemaName: "fixture-finances-db", SourceSchema: "finances"})
	if err != nil {
		t.Fatal(err)
	}
	if v := scoped.Projections[0]; v.QualifiedView != "finances.transaction_list" || v.Schema != "finances" || v.QuotedBaseTable != "finances.line_item" {
		t.Errorf("with sourceSchema the view = %s in %q over %s", v.QualifiedView, v.Schema, v.QuotedBaseTable)
	}
}
