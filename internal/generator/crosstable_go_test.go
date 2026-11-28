package generator

import (
	"errors"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// TestAGoServerOverASQLiteDatabaseServesThePlatformFeatures: the Go
// server's Deps build over a database with a @searchable table (#615),
// the notifier's tables, a party and the agents' tables (#618), whichever
// dialect the database lists first: the Go ORM serves each on SQLite as on
// Postgres, so no Go server over a database is refused by its dialect.
func TestAGoServerOverASQLiteDatabaseServesThePlatformFeatures(t *testing.T) {
	db := ir.NewSchema("notes-db", ir.SchemaKindDB)
	db.Types["Note"] = &ir.TypeDef{Name: "Note", Role: ir.RoleDBTable, Searchable: &ir.SearchableDef{Title: "title"}}
	db.Types["Alert"] = &ir.TypeDef{Name: "Alert", Role: ir.RoleDBTable, Events: []*ir.EventDef{{Kind: "alert.raised"}}, Notifier: &ir.NotifierDef{Part: ir.NotifierDispatch}}
	db.Types["Identity"] = &ir.TypeDef{Name: "Identity", Role: ir.RoleDBTable, ChannelIdentities: &ir.ChannelIdentitiesDef{}}
	db.Types["Contact"] = &ir.TypeDef{Name: "Contact", Role: ir.RoleDBTable, Party: &ir.PartyDef{}}
	db.Types["Run"] = &ir.TypeDef{Name: "Run", Role: ir.RoleDBTable, Agents: &ir.AgentsDef{Part: ir.AgentRuns}}
	reg := CoreRegistry(naming.Default())
	for _, dialects := range [][]any{{"sqlite", "postgres"}, {"postgres", "sqlite"}} {
		api := ir.NewSchema("notes-api", ir.SchemaKindAPI)
		api.AuthDB = "notes-db"
		cfg := &schemaconfig.SchemaConfig{Name: "notes-api", Kind: ir.SchemaKindAPI}
		dbConfig := &schemaconfig.SchemaConfig{Name: "notes-db", Kind: ir.SchemaKindDB, Outputs: map[string]any{
			"types": map[string]any{"go": map[string]any{"enabled": true}},
			"sql":   map[string]any{"dialects": dialects},
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
		deps, _, err := r.goDeps(&apigen.APIOutput{SchemaName: "notes-api", Naming: naming.Default()})
		if err != nil {
			t.Errorf("%v: %v, want none", dialects, err)
			continue
		}
		if want := dialects[0] == "sqlite"; deps.SQLite != want {
			t.Errorf("%v: Deps on SQLite %v, want %v", dialects, deps.SQLite, want)
		}
	}
}
