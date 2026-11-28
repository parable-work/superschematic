package ormgen

import (
	_ "embed"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/testpaths"
)

//go:embed testdata/sqlite/references_results_test.go
var sqliteReferencesResultsTest string

// sqliteReferencesCases are the cases references_results_test.go runs on
// SQLite: the cases of testdata/references_go.go on Postgres (D184).
var sqliteReferencesCases = []string{
	"a file posted on one row's thread is filed onto a second row, which lists the first in its backlinks",
	"a ref block in a row's thread is a reference of the row, which the target lists",
	"a caller who cannot read a referenced row sees neither the reference nor a count, and its owner sees a count",
	"a target's delete clears the references to it and tells each row that held one",
	"an upload's bytes are held to what it declared, a row to its bounds, and an object is deleted once no file names it",
}

// TestTheGoReferencesAndAttachmentsOnSQLite holds the Go ORM of a
// database that lists sqlite to D154's References and Attachments on a
// SQLite file (D182, #611): the SQLite database and its transactions carry
// them over a host whose statements rowrefs writes in SQLite's words, and
// whose reads of a table's rows go through its repository on SQLite, which
// keeps them to the caller. fixture-references-db holds both, a note's
// audience among them; fixture-comments-db's Comments ref blocks give it
// References. Outside -short mode the ORM builds and vets, and
// references_results_test.go runs every case of sqliteReferencesCases over
// a SQLite file: backlinks held to what the reader may read and a count
// for the owner alone, a ref block of a thread, a target's delete clearing
// its references by the SQLite DDL's triggers, an upload's begin and
// completion, a file filed elsewhere reusing its object, and an object
// released once no file names it. With the Postgres at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL the same cases run there too.
func TestTheGoReferencesAndAttachmentsOnSQLite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated ORM's build in -short mode")
	}
	paths := testpaths.Local(t)
	for _, fixture := range []string{"fixture-references-db", "fixture-comments-db"} {
		t.Run(fixture, func(t *testing.T) {
			ormDir := generateBehaviorORMModule(t, paths, fixture, func(o *Options) { o.SQLite = true })
			rowrefs, err := os.ReadFile(filepath.Join(ormDir, "rowrefs.go"))
			if err != nil {
				t.Fatalf("the ORM of %s on SQLite has no rowrefs.go: %v", fixture, err)
			}
			for _, want := range []string{
				"func (db *SQLiteDatabase) References() *rowrefs.References",
				"func (sqliteRowFeaturesHost) Dialect() string { return rowrefs.DialectSQLite }",
			} {
				if !strings.Contains(string(rowrefs), want) {
					t.Errorf("rowrefs.go of %s on SQLite holds no %q", fixture, want)
				}
			}
			if strings.Contains(string(rowrefs), "ErrSQLiteUnsupported") {
				t.Errorf("rowrefs.go of %s on SQLite still refuses", fixture)
			}
			if fixture != "fixture-references-db" {
				runORMModule(t, ormDir)
				return
			}
			schema, err := loader.LoadService(filepath.Join(fixturesDir, fixture), loader.WithRegistry(registry.New(naming.Default())))
			if err != nil {
				t.Fatal(err)
			}
			out := runSQLiteTestIn(t, ormDir, schema, fixture, sqliteReferencesResultsTest, "TestReferencesOnSQLite")
			for _, name := range sqliteReferencesCases {
				if want := "--- PASS: TestReferencesOnSQLite/sqlite/" + strings.ReplaceAll(name, " ", "_") + " "; !strings.Contains(out, want) {
					t.Errorf("the generated module's test did not pass %q on SQLite:\n%s", name, out)
				}
			}
			if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") != "" && !strings.Contains(out, "--- PASS: TestReferencesOnSQLite/postgres ") {
				t.Errorf("the cases did not pass on Postgres:\n%s", out)
			}
		})
	}
}
