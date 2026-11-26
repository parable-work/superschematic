package sqlgen

import (
	"os"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
)

// referencesFixture is fixture-references-db (D154): tickets and notes
// with references and attachments, notes with an audience.
const referencesFixture = "../../loader/tsreader/testdata/services/fixture-references-db"

// TestRowFeaturesDDL: the sub-tables of fixture-references-db's
// @references and @attachments tables, each a child of its row; the
// trigger on each table a reference may point at, which clears the
// references to a deleted row and enqueues one cleared message for each;
// and the trigger on each attachments table, which enqueues a file's
// release (D154).
func TestRowFeaturesDDL(t *testing.T) {
	schema, err := loader.LoadService(referencesFixture)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Generate(schema, Options{SchemaName: "fixture-references-db"})
	if err != nil {
		t.Fatal(err)
	}
	createBytes, err := renderTemplate("create.tmpl", out)
	if err != nil {
		t.Fatal(err)
	}
	dropBytes, err := renderTemplate("drop.tmpl", out)
	if err != nil {
		t.Fatal(err)
	}
	create, drop := string(createBytes), string(dropBytes)
	if os.Getenv("ROWREFS_PRINT") != "" {
		t.Logf("%s\n----\n%s", create, drop)
	}
	for _, want := range []string{
		"CREATE TABLE ticket_references (",
		"CREATE TABLE note_attachments (",
		"CREATE OR REPLACE FUNCTION ticket_references_clear()",
		"CREATE OR REPLACE FUNCTION note_references_clear()",
		"CREATE TRIGGER trg_ticket_references_clear\n  AFTER DELETE ON ticket\n  REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT",
		"DELETE FROM note_references AS r USING old_rows AS o WHERE r.target_table = 'Ticket' AND r.target_key = o.id::text",
		"CREATE TRIGGER trg_ticket_attachments_release\n  AFTER DELETE ON ticket_attachments\n  REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT",
	} {
		if !strings.Contains(create, want) {
			t.Errorf("create.sql lacks %s", want)
		}
	}
	for _, want := range []string{
		"DROP TRIGGER IF EXISTS trg_ticket_references_clear ON ticket;",
		"DROP FUNCTION IF EXISTS ticket_attachments_release();",
	} {
		if !strings.Contains(drop, want) {
			t.Errorf("drop.sql lacks %s", want)
		}
	}
}
