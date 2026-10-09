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
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
	ir "github.com/parable-work/superschematic/ir"
)

// withDialects returns the fixture's config with outputs.sql.dialects set,
// and its Go types on, which the ORM needs.
func withDialects(cfg *schemaconfig.SchemaConfig, dialects ...any) *schemaconfig.SchemaConfig {
	out := *cfg
	out.Outputs = map[string]any{
		"types": map[string]any{"go": map[string]any{"enabled": true}},
		"sql":   map[string]any{"dialects": dialects},
	}
	return &out
}

// TestRunWritesSQLiteDDL: with sqlite in outputs.sql.dialects the build
// writes sqlite/create.sql, the SQLite plan from an empty database, beside
// a Postgres create.sql that is the same byte for byte as without it.
func TestRunWritesSQLiteDDL(t *testing.T) {
	schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, "fixture-list-defaults-db"))
	if err != nil {
		t.Fatal(err)
	}
	postgresOnly, both := t.TempDir(), t.TempDir()
	if _, err := Run(schema, withDialects(cfg, "postgres"), Options{OutputRoot: postgresOnly, Naming: naming.Default()}); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(schema, withDialects(cfg, "postgres", "sqlite"), Options{OutputRoot: both, Naming: naming.Default()}); err != nil {
		t.Fatal(err)
	}
	read := func(root string, parts ...string) []byte {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(append([]string{SQLDir(root, cfg.Name)}, parts...)...))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	for _, name := range []string{"create.sql", "drop.sql"} {
		if string(read(postgresOnly, name)) != string(read(both, name)) {
			t.Errorf("%s changes when sqlite is listed", name)
		}
	}
	if _, err := os.Stat(filepath.Join(SQLDir(postgresOnly, cfg.Name), SQLiteSubdir)); !os.IsNotExist(err) {
		t.Errorf("a service that does not list sqlite has a sqlite directory: %v", err)
	}

	model, err := sqlmigrate.BuildModel(schema, sqlgen.Options{SchemaName: cfg.Name}, sqlmigrate.SQLite)
	if err != nil {
		t.Fatal(err)
	}
	want, err := sqlmigrate.CreateSQL(model)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(read(both, SQLiteSubdir, "create.sql")); got != want {
		t.Errorf("sqlite/create.sql is not the SQLite plan from an empty database:\n%s", got)
	}
	if !strings.Contains(want, `CREATE TABLE "tasting"`) {
		t.Errorf("sqlite/create.sql creates no table:\n%s", want)
	}
}

// TestRunWritesSQLiteSearchText: a @searchField builds for SQLite as its
// search_text column, VIRTUAL, with the expression Postgres's has, each
// field quoted as SQLite quotes it, and no trigram index, which SQLite has
// no operator class for (D27, amended).
func TestRunWritesSQLiteSearchText(t *testing.T) {
	schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, "fixture-list-defaults-db"))
	if err != nil {
		t.Fatal(err)
	}
	tasting := schema.Types["Tasting"]
	tasting.Fields = append(tasting.Fields, &ir.FieldDef{Name: "note", TypeRef: ir.TypeRef{Name: "string"}, SearchField: true})
	root := t.TempDir()
	if _, err := Run(schema, withDialects(cfg, "postgres", "sqlite"), Options{OutputRoot: root, Naming: naming.Default()}); err != nil {
		t.Fatal(err)
	}
	read := func(parts ...string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(append([]string{SQLDir(root, cfg.Name)}, parts...)...))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if postgres := read("create.sql"); !strings.Contains(postgres, "gin_trgm_ops") {
		t.Errorf("create.sql has no trigram index:\n%s", postgres)
	}
	sqlite := read(SQLiteSubdir, "create.sql")
	if want := `"search_text" TEXT GENERATED ALWAYS AS (COALESCE("note", '')) VIRTUAL`; !strings.Contains(sqlite, want) {
		t.Errorf("sqlite/create.sql does not contain %s:\n%s", want, sqlite)
	}
	if strings.Contains(sqlite, "search_trgm") {
		t.Errorf("sqlite/create.sql has the trigram index:\n%s", sqlite)
	}
}

// TestOutputsSQLDialects: outputs.sql's JSON Schema takes a list of
// dialects, and the core registry refuses one without postgres, with an
// unknown dialect or with a dialect twice, saying why.
func TestOutputsSQLDialects(t *testing.T) {
	reg := CoreRegistry(naming.Default())
	parse := func(dialects any) error {
		_, err := registry.ParseOutputs(map[string]any{"sql": map[string]any{"dialects": dialects}}, reg)
		return err
	}
	if err := parse([]any{"postgres", "sqlite"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		dialects any
		want     string
	}{
		{[]any{"sqlite"}, "outputs.sql.dialects must list postgres: the DB kind always generates the Go ORM, which runs on Postgres"},
		{[]any{"postgres", "mysql"}, `unknown dialect "mysql"`},
		{[]any{"postgres", "postgres"}, "lists postgres twice"},
		{"postgres", "outputs.sql: "},
	} {
		if err := parse(tc.dialects); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("dialects %v: %v, want an error containing %q", tc.dialects, err, tc.want)
		}
	}
}

// TestRunRefusesWhatSQLiteDoesNotSupport: a service that lists sqlite fails
// its build when its schema uses a feature the SQLite dialect refuses, with
// the feature and the dialect named.
func TestRunRefusesWhatSQLiteDoesNotSupport(t *testing.T) {
	load := func(fixture string) (*ir.Schema, *schemaconfig.SchemaConfig) {
		t.Helper()
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, fixture))
		if err != nil {
			t.Fatal(err)
		}
		return schema, cfg
	}
	tasting := func(s *ir.Schema) *ir.TypeDef { return s.Types["Tasting"] }
	scalar := func(s *ir.Schema, sql string) ir.TypeRef {
		name := "Geo." + strings.ToLower(sql)
		s.Scalars[name] = &ir.ScalarDef{Name: name, LanguagePrimitive: ir.LanguageString, TypeMappings: map[string]string{"sql": sql}}
		return ir.TypeRef{Name: name}
	}
	typed := func(sql string) func(*ir.Schema) {
		return func(s *ir.Schema) {
			tasting(s).Fields = append(tasting(s).Fields, &ir.FieldDef{Name: "place", TypeRef: scalar(s, sql)})
		}
	}
	tests := []struct {
		feature string
		fixture string
		change  func(*ir.Schema)
		want    string
	}{
		{"@versioned", "", func(s *ir.Schema) { tasting(s).Versioned = true }, "Tasting is @versioned"},
		{"@optimistic", "", func(s *ir.Schema) { tasting(s).Optimistic = true }, "Tasting is @optimistic"},
		{"projection", "fixture-projection", nil, "is a projection (app.preferences)"},
		{"GIN index", "", func(s *ir.Schema) {
			tasting(s).Indexes = []ir.IndexDef{{Keys: []string{"retastedAt"}}}
		}, "is a GIN index"},
		{"GIST index", "", func(s *ir.Schema) {
			typed("LTREE")(s)
			tasting(s).Indexes = []ir.IndexDef{{Keys: []string{"place"}}}
		}, "is a GIST index"},
		{"LTREE", "", typed("LTREE"), "is LTREE, which the sqlite dialect has no storage for"},
		{"POINT", "", typed("POINT"), "is POINT, which the sqlite dialect has no storage for"},
		{"GEOGRAPHY", "", typed("GEOGRAPHY"), "is GEOGRAPHY, which the sqlite dialect has no storage for"},
		{"GEOMETRY", "", typed("GEOMETRY"), "is GEOMETRY, which the sqlite dialect has no storage for"},
	}
	for _, tc := range tests {
		t.Run(tc.feature, func(t *testing.T) {
			fixture := tc.fixture
			if fixture == "" {
				fixture = "fixture-list-defaults-db"
			}
			schema, cfg := load(fixture)
			if tc.change != nil {
				tc.change(schema)
			}
			_, err := Run(schema, withDialects(cfg, "postgres", "sqlite"), Options{OutputRoot: t.TempDir(), Naming: naming.Default()})
			if err == nil {
				t.Fatalf("the build succeeded, want an error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "sqlite") {
				t.Fatalf("error %q does not contain %q and the dialect", err, tc.want)
			}
		})
	}
}
