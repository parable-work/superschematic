package sqlgen

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
)

// TestAudienceTriggersOnSQLite: each @audience table of fixture-access-db
// gets, on SQLite, an insert's and an update's trigger, each of which
// refuses an audience it does not know and sets the row's resolved
// columns, the update's only when they are not what the audience resolves
// to, and folder the cascade that sets its inheriting documents' (D182).
func TestAudienceTriggersOnSQLite(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-access-db"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := Generate(schema, Options{SchemaName: "fixture-access-db"})
	if err != nil {
		t.Fatal(err)
	}
	document := out.AudienceTriggers[0].SQLiteTriggers(sqliteQuote)
	if len(document) != 2 || document[0].Name != "trg_document_audience_insert" || document[1].Name != "trg_document_audience_update" {
		t.Fatalf("document's triggers are %+v", document)
	}
	for _, trigger := range document {
		if !trigger.Refuses {
			t.Errorf("%s refuses no write", trigger.Name)
		}
		for _, want := range []string{
			`SELECT RAISE(ABORT, 'document.audience is none of everyone, private, named, inherit') WHERE NEW."audience" IS NULL OR NEW."audience" NOT IN ('everyone', 'private', 'named', 'inherit');`,
			`UPDATE "document" SET "effective_audience" = CASE "document"."audience" WHEN 'everyone' THEN NULL WHEN 'private' THEN CASE WHEN "document"."created_by" IS NULL THEN '[]' ELSE json_array("document"."created_by") END`,
			`json_each(COALESCE("document"."audience_named", '[]'))`,
			`(SELECT audience_parent."effective_audience" FROM "folder" AS audience_parent WHERE audience_parent."id" = "document"."folder_id")`,
			`WHERE "document"."id" = NEW."id";`,
		} {
			if !strings.Contains(trigger.Definition, want) {
				t.Errorf("%s lacks %s:\n%s", trigger.Name, want, trigger.Definition)
			}
		}
	}
	if !strings.HasPrefix(document[0].Definition, `CREATE TRIGGER "trg_document_audience_insert" AFTER INSERT ON "document" FOR EACH ROW`+"\nBEGIN") {
		t.Errorf("the insert's trigger has a WHEN:\n%s", document[0].Definition)
	}
	if !strings.Contains(document[1].Definition, `OR NEW."effective_audience" IS NOT CASE NEW."audience"`) {
		t.Errorf("the update's trigger fires whatever the row resolves to:\n%s", document[1].Definition)
	}
	if got := out.AudienceTriggers[0].SQLiteBackfillSQL(sqliteQuote); got != `UPDATE "document" SET "audience" = "audience"` {
		t.Errorf("the backfill is %s", got)
	}
	cascade, err := out.AudienceCascades[0].SQLiteTrigger(out.AudienceTriggers, sqliteQuote)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`CREATE TRIGGER "trg_folder_audience_cascade" AFTER UPDATE ON "folder" FOR EACH ROW` + "\n" + `WHEN NEW."effective_audience" IS NOT OLD."effective_audience" OR NEW."effective_agents" IS NOT OLD."effective_agents"`,
		`WHERE "document"."audience" = 'inherit' AND "document"."folder_id" = NEW."id";`,
	} {
		if !strings.Contains(cascade.Definition, want) {
			t.Errorf("the cascade lacks %s:\n%s", want, cascade.Definition)
		}
	}
	if strings.Contains(cascade.Definition, "WITH RECURSIVE") {
		t.Errorf("folder inherits from no folder, and its cascade walks a tree:\n%s", cascade.Definition)
	}
}

// TestAudienceCascadeOfATableThatInheritsFromItself: a folder in a folder,
// with documents in folders. Its cascade cannot count on its own trigger to
// fire again for a child folder, so it sets every inheriting folder below
// the row through a recursive CTE, and then the documents of any of them
// (D182).
func TestAudienceCascadeOfATableThatInheritsFromItself(t *testing.T) {
	folder := AudienceTrigger{Origin: "Folder", Table: "folder", QuotedTable: "folder", Key: "id", TriggerName: "trg_folder_audience",
		Audience: "audience", Named: "audience_named", Agents: "agent_access", Effective: "effective_audience", EffectiveAgents: "effective_agents",
		Owner: `"owner"`, Parent: "folder", ParentKey: "id", ParentColumn: "parent_id"}
	document := AudienceTrigger{Origin: "Document", Table: "document", QuotedTable: "document", Key: "id", TriggerName: "trg_document_audience",
		Audience: "audience", Named: "audience_named", Agents: "agent_access", Effective: "effective_audience", EffectiveAgents: "effective_agents",
		Owner: "created_by", Parent: "folder", ParentKey: "id", ParentColumn: "folder_id"}
	cascade := AudienceCascade{Origin: "Folder", Table: "folder", QuotedTable: "folder", Key: "id", TriggerName: "trg_folder_audience_cascade",
		Children: []AudienceChild{{Table: "folder"}, {Table: "document"}}}
	trigger, err := cascade.SQLiteTrigger([]AudienceTrigger{folder, document}, sqliteQuote)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`UPDATE "folder" SET "effective_audience" = (SELECT audience_root."effective_audience" FROM "folder" AS audience_root WHERE audience_root."id" = NEW."id"),`,
		`WITH RECURSIVE audience_tree(k, g) AS (SELECT audience_child."id"`,
		`JOIN audience_tree ON audience_child."parent_id" = audience_tree.k WHERE audience_child."audience" = 'inherit')`,
		`WHERE "folder"."id" IN (WITH RECURSIVE`,
		`WHERE "document"."audience" = 'inherit' AND ("document"."folder_id" = NEW."id" OR "document"."folder_id" IN (WITH RECURSIVE`,
	} {
		if !strings.Contains(trigger.Definition, want) {
			t.Errorf("the cascade lacks %s:\n%s", want, trigger.Definition)
		}
	}
	if _, err := cascade.SQLiteTrigger([]AudienceTrigger{folder}, sqliteQuote); err == nil {
		t.Error("a cascade whose child has no audience trigger is no error")
	}
}

// TestRowFeaturesTriggersOnSQLite: on SQLite a target's delete clears its
// references and enqueues their cleared messages, and a file's delete its
// object's release, each a row trigger (D182).
func TestRowFeaturesTriggersOnSQLite(t *testing.T) {
	schema, err := loader.LoadService(referencesFixture)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Generate(schema, Options{SchemaName: "fixture-references-db"})
	if err != nil {
		t.Fatal(err)
	}
	var clear, release string
	for _, c := range out.ReferencesClears {
		if c.Table == "ticket" {
			clear = c.SQLiteTrigger(sqliteQuote).Definition
		}
	}
	for _, r := range out.AttachmentsReleases {
		if r.Table == "ticket_attachments" {
			release = r.SQLiteTrigger(sqliteQuote).Definition
		}
	}
	for _, want := range []string{
		`CREATE TRIGGER "trg_ticket_references_clear" AFTER DELETE ON "ticket" FOR EACH ROW`,
		`INSERT INTO "reference_cleared_queue" ("holder_table", "holder_key", "target_table", "target_key", "role")` + "\n" +
			`    SELECT 'Note', CAST("note_id" AS TEXT), "target_table", "target_key", "role" FROM "note_references" WHERE "target_table" = 'Ticket' AND "target_key" = CAST(OLD."id" AS TEXT);`,
		`DELETE FROM "note_references" WHERE "target_table" = 'Ticket' AND "target_key" = CAST(OLD."id" AS TEXT);`,
	} {
		if !strings.Contains(clear, want) {
			t.Errorf("the clear lacks %s:\n%s", want, clear)
		}
	}
	want := `INSERT INTO "attachment_released_queue" ("object_key", "content_hash", "reason")` + "\n" +
		`    SELECT OLD."object_key", OLD."content_hash", 'detached' WHERE NOT EXISTS (SELECT 1 FROM "ticket_attachments" WHERE "object_key" = OLD."object_key");`
	if !strings.Contains(release, want) {
		t.Errorf("the release lacks %s:\n%s", want, release)
	}
}
