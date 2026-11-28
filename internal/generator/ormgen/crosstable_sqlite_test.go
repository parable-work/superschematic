package ormgen

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
)

// TestTheSQLiteSearchDefinitionIsTSGens: the Go ORM's search definition on
// SQLite is the TypeScript repositories' on SQLite, member for member
// (D186, #615): the FTS5 table in place of the weighed document, the key's
// type SQLite's, and the index's audience compared through json_each.
func TestTheSQLiteSearchDefinitionIsTSGens(t *testing.T) {
	const svc = "fixture-search-db"
	output, _ := crossTablesOutput(t, svc)
	var got map[string]any
	if err := json.Unmarshal([]byte(output.CrossTables.SQLiteSearchable), &got); err != nil {
		t.Fatal(err)
	}
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.Join(fixturesDir, svc), loader.WithRegistry(reg))
	if err != nil {
		t.Fatal(err)
	}
	ts, err := tsgen.Generate(schema, tsgen.Options{SchemaName: svc, Registry: reg, SQLite: true})
	if err != nil {
		t.Fatal(err)
	}
	if ts.ORM == nil || ts.ORM.SQLite == nil {
		t.Fatal("tsgen wrote no ORM on SQLite")
	}
	want := map[string]any{}
	for _, table := range ts.ORM.SQLite.Tables {
		if table.Searchable == "" {
			continue
		}
		var def any
		if err := json.Unmarshal([]byte(table.Searchable), &def); err != nil {
			t.Fatal(err)
		}
		want[table.Type] = def
	}
	if len(want) != 6 {
		t.Fatalf("tsgen's searchable tables on SQLite: %d", len(want))
	}
	if !reflect.DeepEqual(got, want) {
		gotJSON, _ := json.MarshalIndent(got, "", " ")
		wantJSON, _ := json.MarshalIndent(want, "", " ")
		t.Errorf("the Go ORM's search definition on SQLite:\n%s\ntsgen's:\n%s", gotJSON, wantJSON)
	}
}
