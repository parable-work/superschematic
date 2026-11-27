package ormgen

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/typegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// TestTheGoRepositoriesOnSQLiteRefuseUnderPerRecordAccess holds the Go ORM
// of a database that lists sqlite and declares per-record access (D156) to
// the loader's refusal of its Go types there (D180, D182), for a
// generation that did not read the service's config: its repositories on
// SQLite keep to no audience or grant, so each of a held database's tables
// has a repository there that refuses every call, naming why, as a table
// SQLite does not hold has. On Postgres alone none is refused. Outside
// -short mode the ORM on SQLite is written, built and vetted.
func TestTheGoRepositoriesOnSQLiteRefuseUnderPerRecordAccess(t *testing.T) {
	const fixture = "fixture-access-db"
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.Join(fixturesDir, fixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", fixture, err)
	}
	clock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	typesModule := "example.com/schemas/types/go/" + fixture
	opts := Options{SchemaName: fixture, ModulePath: "example.com/schemas/orm/" + fixture, TypesModule: typesModule, Registry: reg, Clock: clock}
	postgres, err := Generate(schema, opts)
	if err != nil {
		t.Fatalf("the ORM of %s on Postgres: %v", fixture, err)
	}
	for _, repo := range postgres.Repositories {
		if repo.SQLite != nil {
			t.Errorf("%s has a table on SQLite in an ORM of Postgres alone", repo.TypeName)
		}
	}
	opts.SQLite = true
	output, err := Generate(schema, opts)
	if err != nil {
		t.Fatalf("the ORM of %s on SQLite: %v", fixture, err)
	}
	const held = "its database keeps its rows to per-record access (D156), an audience or grants, which the Go repositories on SQLite do not hold yet (D180, D182)"
	seen := map[string]bool{}
	for _, repo := range output.Repositories {
		seen[repo.TypeName] = true
		if repo.SQLite == nil || !strings.Contains(repo.SQLite.Refusal, held) {
			t.Errorf("%s on SQLite: refusal %q, want %q", repo.TypeName, refusalOf(repo), held)
		}
	}
	for _, name := range []string{"Folder", "Document", "Grant", "AccessEntry"} {
		if !seen[name] {
			t.Errorf("the ORM of %s has no repository of %s", fixture, name)
		}
	}
	if testing.Short() {
		return
	}
	paths := testpaths.Local(t)
	root := t.TempDir()
	typesDir := filepath.Join(root, "types", "go", fixture)
	ormDir := filepath.Join(root, "orm", fixture)
	typesOutput, err := typegen.Generate(schema, typegen.Options{SchemaName: fixture, ModulePath: typesModule, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	if err := typegen.SetReplacePaths(typesOutput, paths, typesDir); err != nil {
		t.Fatal(err)
	}
	if err := typegen.WriteTypes(typesOutput, typesDir); err != nil {
		t.Fatal(err)
	}
	if err := SetReplacePaths(output, paths, ormDir); err != nil {
		t.Fatal(err)
	}
	if err := WriteORM(output, ormDir); err != nil {
		t.Fatal(err)
	}
	tidyGeneratedModule(t, ormDir)
	for _, args := range [][]string{{"build", "./..."}, {"vet", "./..."}} {
		cmd := exec.Command("go", args...)
		cmd.Dir = ormDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %s in the ORM of %s on SQLite: %v\n%s", strings.Join(args, " "), fixture, err, out)
		}
	}
}

func refusalOf(repo Repository) string {
	if repo.SQLite == nil {
		return "(no table on SQLite)"
	}
	return repo.SQLite.Refusal
}
