package rustgen

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
)

// TestNotifierOnSQLiteIsRefused (D153, D191): a Rust types crate that
// lists the sqlite dialect is refused the notifier, which runs on Postgres
// alone, rather than given a definition no notifier reads; on Postgres
// alone it builds.
func TestNotifierOnSQLiteIsRefused(t *testing.T) {
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-notifications-db"), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	_, err = Generate(schema, Options{SchemaName: "fixture-notifications-db", Registry: reg, SQLite: true})
	if err == nil || !strings.Contains(err.Error(), "the notifier (D153) runs on Postgres alone") {
		t.Fatalf("Generate on SQLite = %v, want the notifier refused", err)
	}
	if _, err := Generate(schema, Options{SchemaName: "fixture-notifications-db", Registry: reg}); err != nil {
		t.Fatalf("Generate on Postgres: %v", err)
	}
}
