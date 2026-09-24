package generator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/loader"
)

const projectionMigration = "20260902120000_app_preferences_projection"

// TestRunWritesProjectionsUnderTheSQLOutput: with no outputs.sql block the
// view's migrations, Arrow schema and docs file land under
// <out>/sql/<service>/projections, and the Arrow keys carry the naming
// file's metadata_key_prefix.
func TestRunWritesProjectionsUnderTheSQLOutput(t *testing.T) {
	schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, "fixture-projection"))
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	names := naming.Default()
	names.MetadataKeyPrefix = "acme."
	if _, err := Run(schema, cfg, Options{OutputRoot: out, Naming: names}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(SQLDir(out, "fixture-projection"), sqlgen.ProjectionsSubdir)
	for _, name := range []string{
		filepath.Join("migrations", projectionMigration+".up.sql"),
		filepath.Join("migrations", projectionMigration+".down.sql"),
		"app.preferences.arrow.json",
		"app.preferences.docs.json",
	} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "app.preferences.arrow.json"))
	if err != nil {
		t.Fatal(err)
	}
	var arrow struct {
		Metadata map[string]string `json:"metadata"`
	}
	if err := json.Unmarshal(raw, &arrow); err != nil {
		t.Fatal(err)
	}
	if arrow.Metadata["acme.projection.relation"] != "app.preferences" {
		t.Errorf("Arrow metadata = %v, want acme.-prefixed keys", arrow.Metadata)
	}
	create, err := os.ReadFile(filepath.Join(SQLDir(out, "fixture-projection"), "create.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(create), "CREATE VIEW app.preferences WITH (security_barrier = true) AS") {
		t.Error("create.sql lacks the view")
	}
}

// TestRunPlacesProjectionMigrationsFromOutputsSQL: outputs.sql.migrationsDir
// is relative to the service directory, and outputs.sql.viewOwner renders
// SET ROLE in the up migration.
func TestRunPlacesProjectionMigrationsFromOutputsSQL(t *testing.T) {
	schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, "fixture-projection"))
	if err != nil {
		t.Fatal(err)
	}
	service := t.TempDir()
	cfg.Outputs = map[string]any{"sql": map[string]any{"migrationsDir": "db/migrations", "viewOwner": "app_view_owner"}}
	out := t.TempDir()
	if _, err := Run(schema, cfg, Options{OutputRoot: out, ServicePath: service, Naming: naming.Default()}); err != nil {
		t.Fatal(err)
	}
	up, err := os.ReadFile(filepath.Join(service, "db", "migrations", projectionMigration+".up.sql"))
	if err != nil {
		t.Fatalf("the migration did not land in the configured directory: %v", err)
	}
	if !strings.Contains(string(up), "SET ROLE app_view_owner;") || !strings.Contains(string(up), "RESET ROLE;") {
		t.Errorf("the up migration does not create the view as its owner:\n%s", up)
	}
	if _, err := os.Stat(filepath.Join(SQLDir(out, "fixture-projection"), sqlgen.ProjectionsSubdir, "migrations")); !os.IsNotExist(err) {
		t.Errorf("migrations were also written under the output root: %v", err)
	}

	cfg.Outputs = map[string]any{"sql": map[string]any{"viewOwner": "Owner; DROP ROLE x"}}
	if _, err := Run(schema, cfg, Options{OutputRoot: t.TempDir(), ServicePath: service, Naming: naming.Default()}); err == nil || !strings.Contains(err.Error(), "viewOwner") {
		t.Fatalf("an unsafe view owner generated: %v", err)
	}
	cfg.Outputs = map[string]any{"sql": map[string]any{"migrationDir": "db"}}
	if _, err := Run(schema, cfg, Options{OutputRoot: t.TempDir(), ServicePath: service, Naming: naming.Default()}); err == nil {
		t.Fatal("an unknown outputs.sql key was accepted")
	}
}
