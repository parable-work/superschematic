package ormgen

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
)

// TestTheGoORMOnSQLiteRefusesPerRecordAccess holds the Go ORM of a database
// that lists sqlite and declares per-record access (D156) to the loader's
// refusal of its Go types there (D180, D182): its repositories on SQLite
// keep to no audience or grant and record no access log, so the
// generation refuses, naming each table, rather than write repositories
// that would read every row for a held caller. On Postgres alone it
// generates.
func TestTheGoORMOnSQLiteRefusesPerRecordAccess(t *testing.T) {
	const fixture = "fixture-access-db"
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.Join(fixturesDir, fixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", fixture, err)
	}
	opts := Options{SchemaName: fixture, ModulePath: "example.com/schemas/orm/" + fixture, TypesModule: "example.com/schemas/types/go/" + fixture, Registry: reg}
	if _, err := Generate(schema, opts); err != nil {
		t.Fatalf("the ORM of %s on Postgres: %v", fixture, err)
	}
	opts.SQLite = true
	_, err = Generate(schema, opts)
	if err == nil {
		t.Fatalf("the ORM of %s on SQLite generated; want a refusal of its per-record access", fixture)
	}
	for _, want := range []string{
		"lists the sqlite dialect and declares @audience on Document, @audience on Folder",
		"@accessGrants on Grant, @accessLog on AccessEntry",
		"the Go repositories on SQLite keep to no audience or grant and record no access log yet (D180, D182)",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not say %q", err, want)
		}
	}
}
