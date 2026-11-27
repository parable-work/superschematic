package ormgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// sqliteRowFeaturesCheck is the test the ORM of fixture-references-db on
// SQLite runs: References and Attachments, on the database and on a
// transaction, answer ErrSQLiteUnsupported for a call that reaches the
// database, never a row or a success.
const sqliteRowFeaturesCheck = `package orm

import (
	"context"
	"errors"
	"testing"

	"%s/rowrefs"
)

func TestReferencesAndAttachmentsRefuseOnSQLite(t *testing.T) {
	ctx := context.Background()
	row := rowrefs.RowRef{Table: "Ticket", Key: "00000000-0000-0000-0000-000000000001"}
	for name, refs := range map[string]*rowrefs.References{"database": (&SQLiteDatabase{}).References(), "transaction": (&SQLiteTx{}).References()} {
		if refs == nil {
			t.Fatalf("References on the SQLite %s is nil", name)
		}
		if _, err := refs.Of(ctx, row); !errors.Is(err, ErrSQLiteUnsupported) {
			t.Errorf("References().Of on the SQLite %s answered %v, want ErrSQLiteUnsupported", name, err)
		}
		if _, err := refs.ReferencedBy(ctx, row, rowrefs.ReferencedByOptions{}); !errors.Is(err, ErrSQLiteUnsupported) {
			t.Errorf("References().ReferencedBy on the SQLite %s answered %v, want ErrSQLiteUnsupported", name, err)
		}
	}
	if (&SQLiteDatabase{}).Attachments() == nil || (&SQLiteTx{}).Attachments() == nil {
		t.Error("Attachments on SQLite is nil")
	}
}
`

// TestTheGoReferencesAndAttachmentsOnSQLiteRefuse holds the Go ORM of a
// database that lists sqlite to D184's References and Attachments, which
// its DatabaseInterface and TxInterface carry: the SQLite database and
// transaction carry them too, so the ORM builds, and each call that
// reaches the database answers ErrSQLiteUnsupported, since the Go ORM
// serves them on Postgres alone and the loader refuses a sqlite service's
// Go types for @references and @attachments (D182, D184).
// fixture-references-db holds both; fixture-comments-db's Comments ref
// blocks give it References.
func TestTheGoReferencesAndAttachmentsOnSQLiteRefuse(t *testing.T) {
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
			if !strings.Contains(string(rowrefs), "func (db *SQLiteDatabase) References() *rowrefs.References") {
				t.Errorf("rowrefs.go of %s on SQLite gives the SQLite database no References", fixture)
			}
			if fixture == "fixture-references-db" {
				check := strings.Replace(sqliteRowFeaturesCheck, "%s", naming.Default().SchemaRuntimeGoModule, 1)
				if err := os.WriteFile(filepath.Join(ormDir, "rowrefs_sqlite_test.go"), []byte(check), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			out := runORMModule(t, ormDir)
			if fixture == "fixture-references-db" && !strings.Contains(out, "--- PASS: TestReferencesAndAttachmentsRefuseOnSQLite") {
				t.Errorf("the SQLite check did not pass:\n%s", out)
			}
		})
	}
}
