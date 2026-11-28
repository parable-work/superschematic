package sqlgen

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
)

// TestSearchTriggersOnSQLite: on SQLite each @searchable table of
// fixture-search-db has a trigger after each insert, update and delete,
// which enqueue the row, delete its index row with it and copy its audience,
// each statement guarded by its own WHERE; its sub-tables' enqueue the row
// they belong to; and the index's words are an FTS5 table whose triggers
// on the index find a row's words by its ref token (D186).
func TestSearchTriggersOnSQLite(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-search-db"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := Generate(schema, Options{SchemaName: "fixture-search-db"})
	if err != nil {
		t.Fatal(err)
	}
	definitions := map[string]string{}
	for _, s := range out.SearchTriggers {
		for _, trigger := range s.SQLiteTriggers(sqliteQuote) {
			definitions[trigger.Name] = trigger.Definition
		}
	}
	for _, c := range out.SearchChildTriggers {
		for _, trigger := range c.SQLiteTriggers(sqliteQuote) {
			definitions[trigger.Name] = trigger.Definition
		}
	}
	const skip = `ON CONFLICT (source_table, source_key) WHERE state = 'ready' DO NOTHING;`
	for name, wants := range map[string][]string{
		"trg_note_search_insert": {
			`CREATE TRIGGER "trg_note_search_insert" AFTER INSERT ON "note" FOR EACH ROW` + "\n" + `WHEN NEW."deleted_at" IS NULL`,
			`SELECT 'Note', CAST(NEW."id" AS TEXT) WHERE 1`, skip,
		},
		"trg_note_search_update": {
			`DELETE FROM "search_index" WHERE "source_table" = 'Note' AND "source_key" = CAST(OLD."id" AS TEXT) AND OLD."id" IS NOT NEW."id";`,
			`DELETE FROM "search_index" WHERE "source_table" = 'Note' AND "source_key" = CAST(NEW."id" AS TEXT) AND NEW."deleted_at" IS NOT NULL;`,
			`WHERE NEW."deleted_at" IS NULL` + "\n" + `    AND (OLD."id" IS NOT NEW."id"` + "\n" + `      OR OLD."deleted_at" IS NOT NULL`,
			`OR OLD."project" IS NOT NEW."project")`, skip,
		},
		"trg_note_search_delete": {
			`DELETE FROM "search_index" WHERE "source_table" = 'Note' AND "source_key" = CAST(OLD."id" AS TEXT);`,
		},
		"trg_folder_search_update": {
			`UPDATE "search_index" SET "audience" = NEW."effective_audience", "agents" = NEW."effective_agents"` + "\n" +
				`    WHERE "source_table" = 'Folder' AND "source_key" = CAST(NEW."id" AS TEXT)` + "\n" +
				`      AND ("audience" IS NOT NEW."effective_audience" OR "agents" IS NOT NEW."effective_agents");`,
		},
		"trg_unit_comments_messages_search_update": {
			`SELECT 'Unit', CAST(NEW."unit_id" AS TEXT) WHERE (OLD."body" IS NOT NEW."body" OR OLD."state" IS NOT NEW."state" OR OLD."unit_id" IS NOT NEW."unit_id")`,
			`SELECT 'Unit', CAST(OLD."unit_id" AS TEXT) WHERE OLD."unit_id" IS NOT NEW."unit_id"`,
		},
		"trg_unit_attachments_search_delete": {`SELECT 'Unit', CAST(OLD."unit_id" AS TEXT) WHERE 1`, skip},
	} {
		got, ok := definitions[name]
		if !ok {
			t.Errorf("no trigger %s among %d", name, len(definitions))
			continue
		}
		for _, want := range wants {
			if !strings.Contains(got, want) {
				t.Errorf("%s lacks %s:\n%s", name, want, got)
			}
		}
	}
	if strings.Contains(definitions["trg_folder_search_insert"], "WHEN") {
		t.Errorf("a table with no deleted_at enqueues every insert:\n%s", definitions["trg_folder_search_insert"])
	}

	up, down := SearchFTS("search_index", sqliteQuote, false)
	if len(up) != 9 || len(down) != 4 {
		t.Fatalf("the FTS5 piece is %d statements and its down %d", len(up), len(down))
	}
	// It runs again over what it made: its down first, its words filled
	// from the index rows last (#740).
	if !slices.Equal(up[:4], down) || !strings.HasPrefix(up[8], `INSERT INTO "search_index_fts" (title, body, comments, files, ref, source_table, source_key)`) {
		t.Errorf("the FTS5 piece does not drop first and fill last:\n%s", strings.Join(up, "\n"))
	}
	if folded, _ := SearchFTS("search_index", sqliteQuote, true); !strings.Contains(folded[4], "tokenize = 'unicode61 remove_diacritics 2'") {
		t.Errorf("a folding index's FTS5 table is %s", folded[4])
	}
	for _, want := range []string{
		`CREATE VIRTUAL TABLE "search_index_fts" USING fts5(title, body, comments, files, ref, source_table UNINDEXED, source_key UNINDEXED, tokenize = 'unicode61 remove_diacritics 0')`,
		`AFTER UPDATE OF title, body, comments, files, source_table, source_key ON "search_index"`,
		`DELETE FROM "search_index_fts" WHERE "search_index_fts" MATCH 'ref : "' || 'r' || hex(OLD.source_table || char(31) || OLD.source_key) || '"';`,
		`VALUES (NEW.title, NEW.body, NEW.comments, NEW.files, 'r' || hex(NEW.source_table || char(31) || NEW.source_key), NEW.source_table, NEW.source_key);`,
	} {
		if !strings.Contains(strings.Join(up, "\n"), want) {
			t.Errorf("the FTS5 piece lacks %s:\n%s", want, strings.Join(up, "\n"))
		}
	}
	if down[len(down)-1] != `DROP TABLE IF EXISTS "search_index_fts"` {
		t.Errorf("the down ends with %s", down[len(down)-1])
	}
}
