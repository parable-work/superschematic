package generator

import (
	"errors"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// TestAGoServerOverASQLiteDatabaseRefusesSearch: the Go server's Deps
// check a database whose only feature is a @searchable table, and refuse
// it when the database lists sqlite first, since the crosstable package
// ranks by Postgres statements alone (D155, D187); listed second, it
// builds. The refusal runs for every Go server over a database, whatever
// its tables declare.
func TestAGoServerOverASQLiteDatabaseRefusesSearch(t *testing.T) {
	db := ir.NewSchema("notes-db", ir.SchemaKindDB)
	db.Types["Note"] = &ir.TypeDef{Name: "Note", Role: ir.RoleDBTable, Searchable: &ir.SearchableDef{Title: "title"}}
	reg := CoreRegistry(naming.Default())
	for _, tc := range []struct {
		dialects []any
		refused  bool
	}{{[]any{"sqlite", "postgres"}, true}, {[]any{"postgres", "sqlite"}, false}} {
		api := ir.NewSchema("notes-api", ir.SchemaKindAPI)
		api.AuthDB = "notes-db"
		cfg := &schemaconfig.SchemaConfig{Name: "notes-api", Kind: ir.SchemaKindAPI}
		dbConfig := &schemaconfig.SchemaConfig{Name: "notes-db", Kind: ir.SchemaKindDB, Outputs: map[string]any{
			"types": map[string]any{"go": map[string]any{"enabled": true}},
			"sql":   map[string]any{"dialects": tc.dialects},
		}}
		r, _ := newRun(api, cfg, &registry.Outputs{}, Options{
			OutputRoot: t.TempDir(),
			Registry:   reg,
			LoadDependency: func(name string) (*ir.Schema, error) {
				if name == "notes-db" {
					return db, nil
				}
				return nil, errors.New("no such service")
			},
			DependencyConfig: func(name string) (*schemaconfig.SchemaConfig, bool) { return dbConfig, name == "notes-db" },
		}, reg)
		_, _, err := r.goDeps(&apigen.APIOutput{SchemaName: "notes-api", Naming: naming.Default()})
		switch {
		case tc.refused && (err == nil || !strings.Contains(err.Error(), "lists sqlite first, and serves Note: @searchable on Postgres alone so far (D152, D155, D187)")):
			t.Errorf("%v: %v, want the refusal of Note's search", tc.dialects, err)
		case !tc.refused && err != nil:
			t.Errorf("%v: %v, want none", tc.dialects, err)
		}
	}
}
