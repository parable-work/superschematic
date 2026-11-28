package sqlmigrate

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/loader"
)

// TestAudienceIndexInThePlans: each @audience table of fixture-access-db
// has a GIN index over its effective audience in the Postgres model, which
// a plan from a model without it creates concurrently, outside a
// transaction, on a database whose tables hold rows (D156). The SQLite
// model has none, its audience a JSON array no index serves.
func TestAudienceIndexInThePlans(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join("..", "loader", "tsreader", "testdata", "services", "fixture-access-db"))
	if err != nil {
		t.Fatal(err)
	}
	opts := sqlgen.Options{SchemaName: "fixture-access-db"}
	model, err := BuildModel(schema, opts, Postgres)
	if err != nil {
		t.Fatal(err)
	}
	indexes := []string{"idx_document_effective_audience", "idx_folder_effective_audience"}
	before, err := BuildModel(schema, opts, Postgres)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range before.Tables {
		table.Indexes = slices.DeleteFunc(table.Indexes, func(idx *Index) bool { return slices.Contains(indexes, idx.Name) })
	}
	plan, err := Diff(before, model, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var created []string
	for _, step := range plan.Steps {
		if step.Op != string(opCreateIndex) {
			continue
		}
		created = append(created, step.Subject)
		text := strings.Join(step.Statements, "\n")
		if !strings.Contains(text, "CREATE INDEX CONCURRENTLY") || !strings.Contains(text, "USING GIN (effective_audience)") || step.Transactional {
			t.Errorf("step %s is %q, transactional %v; want a GIN index built concurrently", step.Subject, text, step.Transactional)
		}
	}
	want := []string{"table/document/index/idx_document_effective_audience", "table/folder/index/idx_folder_effective_audience"}
	if !slices.Equal(created, want) {
		t.Errorf("the plan creates the indexes %v, want %v", created, want)
	}
	sqlite, err := BuildModel(schema, opts, SQLite)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range sqlite.Tables {
		for _, idx := range table.Indexes {
			if slices.Contains(idx.Columns, "effective_audience") {
				t.Errorf("the SQLite model indexes %s's effective audience: %+v", table.Name, idx)
			}
		}
	}
}
