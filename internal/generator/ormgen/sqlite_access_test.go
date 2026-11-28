package ormgen

import (
	_ "embed"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
)

//go:embed testdata/sqlite/access_results_test.go
var sqliteAccessResultsTest string

// TestTheGoRepositoriesOnSQLiteServePerRecordAccess holds the Go ORM of a
// database that lists sqlite and declares per-record access (D156) to the
// same access as on Postgres (D180, D182, #611): every table of a held
// database has a repository on SQLite, but one SQLite does not hold for a
// reason of its own (Contract's Encrypted<T> terms), and access_sqlite.go
// holds their reads and writes to the caller in SQLite's words. Outside
// -short mode the generated module runs access_results_test.go over a
// SQLite file: an audience private, named and inherited, a filter through
// a relation, an agent run's readOnly rows, a party's grants and the rows
// their role's paths reach, an expired and a revoked grant refused on the
// next call, and a reveal's row of the access log; with the Postgres at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL, the same script on Postgres
// answers alike.
func TestTheGoRepositoriesOnSQLiteServePerRecordAccess(t *testing.T) {
	const fixture = "fixture-access-db"
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.Join(fixturesDir, fixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", fixture, err)
	}
	clock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	opts := Options{SchemaName: fixture, ModulePath: "example.com/schemas/orm/" + fixture, TypesModule: "example.com/schemas/types/go/" + fixture, Registry: reg, Clock: clock, SQLite: true}
	output, err := Generate(schema, opts)
	if err != nil {
		t.Fatalf("the ORM of %s on SQLite: %v", fixture, err)
	}
	if !output.HasSQLiteAccess() {
		t.Fatalf("the ORM of %s on SQLite writes no access_sqlite.go", fixture)
	}
	seen := map[string]bool{}
	for _, repo := range output.Repositories {
		seen[repo.TypeName] = true
		refusal := refusalOf(repo)
		if repo.TypeName == "Contract" {
			if !strings.Contains(refusal, "Encrypted<T>") {
				t.Errorf("Contract on SQLite: refusal %q, want its Encrypted<T> field's", refusal)
			}
			continue
		}
		if refusal != "" {
			t.Errorf("%s on SQLite is refused: %s", repo.TypeName, refusal)
		}
	}
	for _, name := range []string{"Folder", "Document", "Grant", "AccessEntry", "Request", "Unit"} {
		if !seen[name] {
			t.Errorf("the ORM of %s has no repository of %s", fixture, name)
		}
	}
	if testing.Short() {
		return
	}
	ormDir := generateORMModuleAt(t, filepath.Join(fixturesDir, fixture), fixture, func(o *Options) { o.SQLite = true })
	if _, err := os.Stat(filepath.Join(ormDir, "access_sqlite.go")); err != nil {
		t.Fatalf("the ORM module on SQLite: %v", err)
	}
	out := runSQLiteTestIn(t, ormDir, schema, fixture, sqliteAccessResultsTest, "TestAccessOnSQLiteAndPostgres")
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") != "" {
		alike := regexp.MustCompile(`SQLite and Postgres answered \d+ calls alike`).FindString(out)
		if alike == "" {
			t.Fatalf("the comparison with Postgres did not run:\n%s", out)
		}
		t.Log(alike)
	}
}

func refusalOf(repo Repository) string {
	if repo.SQLite == nil {
		return "(no table on SQLite)"
	}
	return repo.SQLite.Refusal
}
