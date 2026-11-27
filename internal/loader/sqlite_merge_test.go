package loader

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/loader/verify"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// TestATypeScriptMergeOnASQLiteFirstDatabaseIsRefused: a merge (D147) a
// TypeScript server serves over a database whose first dialect is sqlite
// is refused at the load, since the server opens that database on SQLite,
// where the TypeScript repositories refuse mergeInto (D182). A database
// that lists postgres first, a server in Go or Rust, which serves no merge
// route, a database whose dialects the loader did not read, and any other
// table operation on SQLite are not refused here.
func TestATypeScriptMergeOnASQLiteFirstDatabaseIsRefused(t *testing.T) {
	loaded, tracker := loadTracker(t)
	uuid := ir.TypeRef{Name: "Identity.UUID"}
	check := func(merge, typeScript bool, dialects []string) []string {
		db := copyOf(t, tracker)
		db.Types["Team"].Mergeable = true
		api := copyOf(t, loaded)
		if merge {
			op := trackerOperation(t, api, "getTeam")
			op.HTTPMethod, op.RestPath = "POST", "teams/{id}/merge/{into}"
			op.Arguments = append(op.Arguments, &ir.ArgumentDef{Name: "into", TypeRef: uuid, Required: true})
			op.TableOperation = &ir.TableOperation{}
		}
		var got []string
		for _, d := range verify.CheckTableOperations(api, verify.Input{Database: db, TypeScriptServer: typeScript, DatabaseDialects: dialects}).Errors {
			got = append(got, d.Error())
		}
		return got
	}
	sqliteFirst := []string{"sqlite", "postgres"}
	for _, dialects := range [][]string{sqliteFirst, {"sqlite"}} {
		errs := check(true, true, dialects)
		if len(errs) != 1 || !strings.Contains(errs[0], "whose first dialect is sqlite") || !strings.Contains(errs[0], "refuse mergeInto so far (D182)") {
			t.Errorf("a TypeScript merge over %v: %q", dialects, errs)
		}
	}
	for name, errs := range map[string][]string{
		"postgres first":          check(true, true, []string{"postgres", "sqlite"}),
		"a Go or a Rust server":   check(true, false, sqliteFirst),
		"dialects not read":       check(true, true, nil),
		"another table operation": check(false, true, sqliteFirst),
	} {
		if len(errs) != 0 {
			t.Errorf("%s: %q", name, errs)
		}
	}
}

// TestTheLoaderReadsATypeScriptServersDatabaseDialects: the loader reads
// whether an API's server is TypeScript from its config, and the dialects
// of its database from the config of the DB service beside it, for an API
// that serves a @tableOperation; nothing for any other server or API.
func TestTheLoaderReadsATypeScriptServersDatabaseDialects(t *testing.T) {
	root := t.TempDir()
	dbDir := filepath.Join(root, "shop-db")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	config := `{"name": "shop-db", "kind": "DB", "outputs": {"sql": {"dialects": ["sqlite", "postgres"]}}}`
	if err := os.WriteFile(filepath.Join(dbDir, "schema.config.json"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	api := &ir.Schema{
		Name:          "shop-api",
		Kind:          ir.SchemaKindAPI,
		Dependencies:  []ir.ServiceRef{{Name: "shop-db", Kind: ir.SchemaKindDB}},
		OperationSets: []*ir.OperationSet{{Name: "Teams", Operations: []*ir.FieldDef{{Name: "mergeTeam", TableOperation: &ir.TableOperation{}}}}},
	}
	database := &ir.Schema{Name: "shop-db", Kind: ir.SchemaKindDB}
	reg := registry.New(naming.Default())
	servicePath := filepath.Join(root, "shop-api")
	if got := databaseDialects(servicePath, api, database, true, reg); !slices.Equal(got, []string{"sqlite", "postgres"}) {
		t.Errorf("the database's dialects = %v", got)
	}
	if got := databaseDialects(servicePath, api, database, false, reg); got != nil {
		t.Errorf("a server in another language read %v", got)
	}
	if got := databaseDialects(servicePath, api, nil, true, reg); got != nil {
		t.Errorf("a database the loader did not read gave %v", got)
	}
	if got := databaseDialects(filepath.Join(root, "elsewhere", "shop-api"), api, database, true, reg); got != nil {
		t.Errorf("a database with no config beside the API gave %v", got)
	}
	for language, want := range map[string]bool{"TYPESCRIPT": true, "typescript": true, "GO": false, "": false} {
		cfg := &schemaconfig.SchemaConfig{Kind: ir.SchemaKindAPI, Outputs: map[string]any{"api": map[string]any{"enabled": true, "language": language}}}
		if got := typeScriptServer(cfg); got != want {
			t.Errorf("an API server in %q is TypeScript: %v, want %v", language, got, want)
		}
	}
}
