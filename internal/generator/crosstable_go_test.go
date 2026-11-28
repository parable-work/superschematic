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

// TestAGoServerOverASQLiteDatabaseServesSearch: the Go server's Deps
// check a database whose only feature is a @searchable table, which the
// crosstable package searches on SQLite over FTS5 as on Postgres (#615),
// so it builds whichever dialect the database lists first; a notifier's
// table beside it is still refused on SQLite (D190). The check runs for
// every Go server over a database, whatever its tables declare.
func TestAGoServerOverASQLiteDatabaseServesSearch(t *testing.T) {
	db := ir.NewSchema("notes-db", ir.SchemaKindDB)
	db.Types["Note"] = &ir.TypeDef{Name: "Note", Role: ir.RoleDBTable, Searchable: &ir.SearchableDef{Title: "title"}}
	reg := CoreRegistry(naming.Default())
	for _, tc := range []struct {
		dialects []any
		refused  bool
	}{{[]any{"sqlite", "postgres"}, false}, {[]any{"postgres", "sqlite"}, false}, {[]any{"sqlite", "postgres"}, true}} {
		if tc.refused {
			db.Types["Alert"] = &ir.TypeDef{Name: "Alert", Role: ir.RoleDBTable, Notifier: &ir.NotifierDef{}}
		}
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
		case tc.refused && (err == nil || !strings.Contains(err.Error(), "lists sqlite first, and serves Alert: @notifier on Postgres alone so far (D153, D157, D158, D190, D192)") || strings.Contains(err.Error(), "Note")):
			t.Errorf("%v: %v, want the refusal of Alert's notifier alone", tc.dialects, err)
		case !tc.refused && err != nil:
			t.Errorf("%v: %v, want none", tc.dialects, err)
		}
	}
}
