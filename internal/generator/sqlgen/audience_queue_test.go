package sqlgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	"github.com/parable-work/superschematic/internal/loader"
)

// TestAudienceQueueOfTheFixture: a table whose @audience names a queue
// (#734) is resolved by the queue's worker, not by its parent's cascade:
// fixture-audience-queue-db's folder's cascade enqueues the folder on
// AudienceResolving, once per folder and table, and updates no page. The
// page table has the index over its folder and its key the worker's
// batches read, and its model the statement that resolves one batch.
func TestAudienceQueueOfTheFixture(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-audience-queue-db"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := Generate(schema, Options{SchemaName: "fixture-audience-queue-db"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.AudienceCascades) != 1 || out.AudienceCascades[0].Table != "folder" {
		t.Fatalf("the cascades are %+v, want folder's alone", out.AudienceCascades)
	}
	body := out.AudienceCascades[0].FunctionSQL()
	for _, want := range []string{
		"INSERT INTO audience_resolving_queue (child_table, parent_key)",
		"VALUES ('Page', NEW.id::text)",
		"ON CONFLICT",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the cascade lacks %s:\n%s", want, body)
		}
	}
	if strings.Contains(body, "UPDATE page") {
		t.Errorf("the cascade resolves the pages in the writer's transaction:\n%s", body)
	}
	dir := t.TempDir()
	if err := WriteDDL(out, dir); err != nil {
		t.Fatal(err)
	}
	create, err := os.ReadFile(filepath.Join(dir, "create.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "CREATE INDEX idx_page_audience_parent ON page USING BTREE (folder_id, id);"; !strings.Contains(string(create), want) {
		t.Errorf("create.sql lacks %s", want)
	}
	tables, err := sqlutil.TablesOf(schema, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		resolve := ""
		if table.Audience != nil {
			resolve = table.Audience.Resolve
		}
		switch table.Type {
		case "Page":
			want := "WITH next AS (SELECT id AS k FROM page WHERE folder_id = $1::text::uuid AND audience = 'inherit' AND ($2::text IS NULL OR id > $2::text::uuid) ORDER BY 1 LIMIT $3), " +
				"resolved AS (UPDATE page SET audience = audience WHERE id IN (SELECT k FROM next) RETURNING id AS k) " +
				"SELECT (SELECT count(*) FROM resolved) AS resolved, (SELECT k::text FROM resolved ORDER BY k DESC LIMIT 1) AS last"
			if resolve != want {
				t.Errorf("Page's resolve is\n%s\nwant\n%s", resolve, want)
			}
		case "Folder":
			if resolve != "" {
				t.Errorf("Folder, which inherits from none, has the resolve %s", resolve)
			}
		}
	}
}
