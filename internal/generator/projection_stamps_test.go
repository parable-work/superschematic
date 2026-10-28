package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	ir "github.com/parable-work/superschematic/ir"
)

const projectionStamp = "20260902120000"

// loadProjectionFixture loads fixture-projection, whose AppPreference
// declares migration 20260902120000.
func loadProjectionFixture(t *testing.T) (*ir.Schema, *schemaconfig.SchemaConfig) {
	t.Helper()
	schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, "fixture-projection"))
	if err != nil {
		t.Fatal(err)
	}
	return schema, cfg
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func recordedStamps(t *testing.T, service string) map[string]BuiltMigration {
	t.Helper()
	lock, err := ReadProjectionsLock(filepath.Join(service, ProjectionsLockFile))
	if err != nil {
		t.Fatal(err)
	}
	if lock == nil {
		return nil
	}
	stamps := map[string]BuiltMigration{}
	for _, m := range lock.Migrations {
		stamps[m.Migration] = m
	}
	return stamps
}

// TestABuiltProjectionMigrationIsNeverRewritten is J2's acceptance in the
// generator (D102): the first build records the stamp's files in
// projections.lock beside the service; a build of the same declaration
// passes; a changed declaration under the recorded stamp fails before any
// file is written, naming the projection, the stamp and the next stamp;
// --rewrite-migration writes and records it again; and a new stamp is
// recorded beside the old one, which stays.
func TestABuiltProjectionMigrationIsNeverRewritten(t *testing.T) {
	schema, cfg := loadProjectionFixture(t)
	service := t.TempDir()
	out := t.TempDir()
	build := func(rewrite ...string) error {
		_, err := Run(schema, cfg, Options{OutputRoot: out, ServicePath: service, Naming: naming.Default(), RewriteMigrations: rewrite})
		return err
	}
	if err := build(); err != nil {
		t.Fatal(err)
	}
	up := filepath.Join(SQLDir(out, "fixture-projection"), sqlgen.ProjectionsSubdir, "migrations", projectionMigration+".up.sql")
	built := readFile(t, up)
	first := recordedStamps(t, service)[projectionStamp]
	if first.Projection != "AppPreference" || !strings.HasPrefix(first.Hash, "sha256:") {
		t.Fatalf("the first build recorded %+v", first)
	}
	if err := build(); err != nil {
		t.Fatalf("a build of the same declaration: %v", err)
	}

	// Drop the last column under the same stamp.
	view := schema.Types["AppPreference"]
	fields := view.Fields
	view.Fields = fields[:len(fields)-1]
	err := build()
	if err == nil {
		t.Fatal("a changed declaration under a recorded stamp built")
	}
	for _, want := range []string{
		"projection AppPreference changed under its migration stamp 20260902120000, which projections.lock records as built",
		`give it a new stamp, migration: "20260902120001"`,
		"superschematic build " + service + " --rewrite-migration 20260902120000",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal lacks %q:\n%v", want, err)
		}
	}
	if readFile(t, up) != built {
		t.Error("the refused build rewrote the up migration")
	}

	if err := build(projectionStamp); err != nil {
		t.Fatalf("--rewrite-migration: %v", err)
	}
	if readFile(t, up) == built {
		t.Error("--rewrite-migration did not write the up migration again")
	}
	rewritten := recordedStamps(t, service)[projectionStamp]
	if rewritten.Hash == first.Hash {
		t.Error("--rewrite-migration did not record the new files")
	}
	if err := build("20260902120009"); err == nil || !strings.Contains(err.Error(), "--rewrite-migration 20260902120009: no projection of the service declares that migration stamp") {
		t.Errorf("--rewrite-migration of a stamp no projection declares: %v", err)
	}

	// The column back, under a new stamp: both stamps stay recorded.
	view.Fields = fields
	view.Projection.Migration = "20260902120001"
	if err := build(); err != nil {
		t.Fatalf("a new stamp: %v", err)
	}
	stamps := recordedStamps(t, service)
	if len(stamps) != 2 || stamps[projectionStamp].Hash != rewritten.Hash || stamps["20260902120001"].Projection != "AppPreference" {
		t.Errorf("the record after a new stamp: %+v", stamps)
	}

	// Two projections changed at once take a stamp each.
	second := *view
	second.Name = "AppPreferenceCopy"
	second.Projection = &ir.ProjectionDef{}
	*second.Projection = *view.Projection
	second.Projection.Name = "preferences_copy"
	second.Projection.Migration = "20260902120005"
	schema.Types[second.Name] = &second
	if err := build(); err != nil {
		t.Fatal(err)
	}
	view.Description = "Preferences, described again."
	second.Description = "Preferences, described again."
	err = build()
	if err == nil {
		t.Fatal("two changed projections built")
	}
	for _, want := range []string{
		`projection AppPreference changed under its migration stamp 20260902120001, which projections.lock records as built, and an applied migration is never rewritten: give it a new stamp, migration: "20260902120006"`,
		`projection AppPreferenceCopy changed under its migration stamp 20260902120005, which projections.lock records as built, and an applied migration is never rewritten: give it a new stamp, migration: "20260902120007"`,
		"--rewrite-migration 20260902120001 --rewrite-migration 20260902120005",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal lacks %q:\n%v", want, err)
		}
	}
}

// TestAPairInTheServicesMigrationsDirIsARecord: with no record of a stamp,
// a pair outputs.sql.migrationsDir already holds, committed beside the
// service's own migrations, is its record (D102); the next stamp follows
// the newest migration there, and a service built without a service path
// records nothing.
func TestAPairInTheServicesMigrationsDirIsARecord(t *testing.T) {
	schema, cfg := loadProjectionFixture(t)
	service := t.TempDir()
	cfg.Outputs = map[string]any{
		"types": map[string]any{"go": map[string]any{"enabled": true}},
		"sql":   map[string]any{"migrationsDir": "db/migrations"},
	}
	migrations := filepath.Join(service, "db", "migrations")
	if err := os.MkdirAll(migrations, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		projectionMigration + ".up.sql":    "-- applied before\n",
		"20261101090000_grants.up.sql":     "GRANT SELECT ON ALL TABLES IN SCHEMA app TO app_reader;\n",
		"20261101090000_grants.down.sql":   "REVOKE SELECT ON ALL TABLES IN SCHEMA app FROM app_reader;\n",
		"1_init_sequential.up.sql":         "SELECT 1;\n",
		"20261101090000123456_late.up.sql": "SELECT 1;\n",
	} {
		if err := os.WriteFile(filepath.Join(migrations, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, err := Run(schema, cfg, Options{OutputRoot: t.TempDir(), ServicePath: service, Naming: naming.Default()})
	if err == nil {
		t.Fatal("a build rewrote a pair the service's migrations directory holds")
	}
	for _, want := range []string{
		"projection AppPreference changed under its migration stamp 20260902120000, whose " + projectionMigration + ".up.sql is already in db/migrations with other contents",
		`give it a new stamp, migration: "20261101090001"`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal lacks %q:\n%v", want, err)
		}
	}
	if readFile(t, filepath.Join(migrations, projectionMigration+".up.sql")) != "-- applied before\n" {
		t.Error("the refused build rewrote the committed up migration")
	}
	if _, err := os.Stat(filepath.Join(service, ProjectionsLockFile)); !os.IsNotExist(err) {
		t.Errorf("the refused build wrote the record: %v", err)
	}

	if _, err := Run(schema, cfg, Options{OutputRoot: t.TempDir(), ServicePath: service, Naming: naming.Default(), RewriteMigrations: []string{projectionStamp}}); err != nil {
		t.Fatalf("--rewrite-migration: %v", err)
	}
	if recordedStamps(t, service)[projectionStamp].Hash == "" {
		t.Error("--rewrite-migration recorded nothing")
	}

	if _, err := Run(schema, cfgWithoutOutputs(cfg), Options{OutputRoot: t.TempDir(), Naming: naming.Default()}); err != nil {
		t.Fatalf("a build without a service path: %v", err)
	}
}

// cfgWithoutOutputs is cfg with no outputs.sql block, which a build
// without a service path takes.
func cfgWithoutOutputs(cfg *schemaconfig.SchemaConfig) *schemaconfig.SchemaConfig {
	copied := *cfg
	copied.Outputs = map[string]any{"types": map[string]any{"go": map[string]any{"enabled": true}}}
	return &copied
}

// TestNextMigrationStamp: the next stamp is a second after the latest as a
// time, rolling over minutes, days and years; a number after one that is
// no time; and versions longer than a stamp are left out.
func TestNextMigrationStamp(t *testing.T) {
	for _, tc := range []struct{ stamp, next string }{
		{"20261028120000", "20261028120001"},
		{"20261028125959", "20261028130000"},
		{"20261231235959", "20270101000000"},
		{"20261399000000", "20261399000001"},
	} {
		if got := nextStamp(tc.stamp); got != tc.next {
			t.Errorf("nextStamp(%s) = %s, want %s", tc.stamp, got, tc.next)
		}
	}
	if got := latestStamp([]string{"7", "20261028120000", "20261101090000", "20261101090000123456", "2026"}); got != "20261101090000" {
		t.Errorf("latestStamp = %s", got)
	}
}
