package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/stack/local"
	ir "github.com/parable-work/superschematic/ir"
)

// TestPrintLocalSummaryNamesTheSQLiteFile: a sqlite-first database's line
// in stack dev's summary is its file's sqlite: URL under the state
// directory (D64), beside a Postgres database's connection string.
func TestPrintLocalSummaryNamesTheSQLiteFile(t *testing.T) {
	env := &ir.ResolvedEnvironment{Stack: "notes-stack", Environment: "Dev", Resources: &ir.ResourceGraph{Resources: []*ir.Resource{
		{ID: "notes-db.database.notes-db", Type: local.TypeSQLiteDatabase, Properties: map[string]any{"name": "notes_db", "service": "notes-db", "file": "databases/notes_db.sqlite"}},
		{ID: "shop-db.database.shop-db", Type: local.TypeDatabase, Properties: map[string]any{"service": "shop-db", "url": "postgres://postgres@127.0.0.1:30001/shop_db?sslmode=disable"}},
	}}}
	var out bytes.Buffer
	printLocalSummary(&out, env, "/state")
	for _, want := range []string{
		"  database notes-db                 sqlite:/state/databases/notes_db.sqlite\n",
		"  database shop-db                  postgres://postgres@127.0.0.1:30001/shop_db?sslmode=disable\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the summary lacks %q:\n%s", want, out.String())
		}
	}
}
