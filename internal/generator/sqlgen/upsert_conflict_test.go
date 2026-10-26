package sqlgen

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	"github.com/parable-work/superschematic/internal/loader"
)

// TestUpsertConflictTargetsInferTheirIndexes holds the conflict target of
// an upsert by a unique @index (sqlutil.UniqueKey, D79) to the index this
// package writes for it (D83): over the index's expression when it has
// one, else its columns, and naming its predicate, the @index's where and
// a soft-deletable table's live rows, so that Postgres infers the index.
func TestUpsertConflictTargetsInferTheirIndexes(t *testing.T) {
	checked := map[string]bool{}
	for _, fixture := range []string{"fixture-ddl-db", "fixture-ledger-db", "fixture-db", "fixture-user-model-db"} {
		schema, err := loader.LoadService(filepath.Join(fixturesDir, fixture))
		if err != nil {
			t.Fatalf("load %s: %v", fixture, err)
		}
		out, err := Generate(schema, Options{SchemaName: fixture})
		if err != nil {
			t.Fatal(err)
		}
		models, err := sqlutil.TablesOf(schema, nil)
		if err != nil {
			t.Fatal(err)
		}
		indexes := map[string]Index{}
		for _, table := range out.Tables {
			for _, idx := range table.Indexes {
				indexes[table.Name+"/"+idx.Name] = idx
			}
		}
		for _, model := range models {
			td := schema.Types[model.Type]
			full := map[string]bool{model.KeyField: true}
			for _, f := range td.Fields {
				if f.Unique {
					full[f.Name] = true
				}
			}
			for _, decl := range td.Indexes {
				name := strings.Join(decl.Keys, ",")
				if !decl.Unique || full[name] {
					continue // a key a full unique constraint holds first
				}
				indexName, err := sqlutil.IndexName(model.Name, decl.Keys, decl.Name, true)
				if err != nil {
					t.Fatal(err)
				}
				idx, ok := indexes[model.Name+"/"+indexName]
				if !ok {
					t.Fatalf("%s: no index %s on %s", fixture, indexName, model.Name)
				}
				key := model.UniqueKeyNamed(name)
				if key == nil {
					t.Fatalf("%s: %s has no unique key %s", fixture, model.Type, name)
				}
				want := "(" + strings.Join(idx.Columns, ", ") + ")"
				if idx.Expression != "" {
					want = "((" + idx.Expression + "))"
				}
				if idx.Where != "" {
					want += " WHERE " + idx.Where
				}
				if key.Conflict != want {
					t.Errorf("%s: the upsert by %s.%s names %q, and the index %s is %q", fixture, model.Type, name, key.Conflict, indexName, want)
				}
				checked[fixture+": "+indexName] = true
			}
		}
	}
	// An expression, a where, and the live rows of a soft-deletable table.
	for _, want := range []string{"fixture-ddl-db: uq_category_label_lower", "fixture-ddl-db: uq_line_item_external", "fixture-ledger-db: uq_entry_external"} {
		if !checked[want] {
			t.Errorf("checked %v, not %s", checked, want)
		}
	}
}
