package generator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/identitydesc"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
)

// identityFixtureDir holds the fixture the identity runtimes' stores run
// against (runtime/http/go/identity and its TypeScript and Rust peers):
// fixture-user-model-db's identity descriptor, its Postgres DDL and its
// SQLite DDL.
const identityFixtureDir = "../../runtime/http/testdata/identity"

// TestIdentityStoreFixtureIsCurrent builds fixture-user-model-db's identity
// descriptor and both dialects' DDL, and fails when the checked-in copies
// the identity runtimes read differ. Rewrite them with:
//
//	go test ./internal/generator -run TestIdentityStoreFixtureIsCurrent -update
func TestIdentityStoreFixtureIsCurrent(t *testing.T) {
	const fixture = "fixture-user-model-db"
	schema, err := loader.LoadService(filepath.Join(tsFixtures, fixture))
	if err != nil {
		t.Fatalf("load %s: %v", fixture, err)
	}
	descriptor, ok, err := identitydesc.Describe(schema)
	if err != nil || !ok {
		t.Fatalf("describe the identity tables: %v (described: %v)", err, ok)
	}
	descriptorJSON, err := descriptor.JSON()
	if err != nil {
		t.Fatal(err)
	}
	opts := sqlgen.Options{SchemaName: fixture}
	ddl, err := sqlgen.Generate(schema, opts)
	if err != nil {
		t.Fatalf("generate the DDL: %v", err)
	}
	ddlDir := t.TempDir()
	if err := sqlgen.WriteDDL(ddl, ddlDir); err != nil {
		t.Fatalf("write the DDL: %v", err)
	}
	createSQL, err := os.ReadFile(filepath.Join(ddlDir, "create.sql"))
	if err != nil {
		t.Fatal(err)
	}
	model, err := sqlmigrate.BuildModel(schema, opts, sqlmigrate.SQLite)
	if err != nil {
		t.Fatalf("build the SQLite model: %v", err)
	}
	sqliteSQL, err := sqlmigrate.CreateSQL(model)
	if err != nil {
		t.Fatalf("render the SQLite DDL: %v", err)
	}

	for name, want := range map[string][]byte{
		fixture + ".json": descriptorJSON,
		"create.sql":      createSQL,
		filepath.Join(SQLiteSubdir, "create.sql"): []byte(sqliteSQL),
	} {
		path := filepath.Join(identityFixtureDir, name)
		if *updateNamingGolden {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, want, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v (rewrite it with -update)", path, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s is stale: the compiler writes another %s for %s; rewrite it with -update", path, name, fixture)
		}
	}
}
