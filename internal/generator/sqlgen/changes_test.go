package sqlgen

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// sqliteQuote quotes an identifier as the SQLite dialect does.
func sqliteQuote(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

// A hearing table's change trigger (D103): fixture-reactions-db's tables
// each get one, named for the table, over the columns the layout maps for
// the core, the key, _version and audit columns left out; Postgres's is a
// function and an AFTER INSERT OR UPDATE OR DELETE trigger, SQLite's one
// trigger per write, the update's WHEN comparing the image's columns.
func TestChangeTriggers(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-reactions-db"))
	if err != nil {
		t.Fatal(err)
	}
	output, err := Generate(schema, Options{SchemaName: "fixture-reactions-db"})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range output.ChangeTriggers {
		names = append(names, fmt.Sprintf("%s %s %s %s %s %v", c.Origin, c.Queue, c.QueueTable, c.FunctionName, c.TriggerName, c.Image))
	}
	want := []string{
		"Project ProjectChange project_change_queue project_changes trg_project_changes [name status status_entered_at]",
		"Task TaskChange task_change_queue task_changes trg_task_changes [title priority project_id status status_entered_at]",
	}
	if strings.Join(names, "\n") != strings.Join(want, "\n") {
		t.Fatalf("change triggers =\n%s\nwant\n%s", strings.Join(names, "\n"), strings.Join(want, "\n"))
	}
	task := output.ChangeTriggers[1]
	if trigger := task.Trigger(); trigger.Timing != "AFTER INSERT OR UPDATE OR DELETE" || trigger.Function != "task_changes" {
		t.Errorf("Postgres trigger = %+v", trigger)
	}
	var sqlite []string
	for _, trigger := range task.SQLiteTriggers(sqliteQuote) {
		sqlite = append(sqlite, trigger.Name)
	}
	if strings.Join(sqlite, ",") != "trg_task_changes_insert,trg_task_changes_update,trg_task_changes_delete" {
		t.Errorf("SQLite triggers = %v", sqlite)
	}
	update := task.SQLiteTriggers(sqliteQuote)[1].Definition
	if !strings.Contains(update, `WHEN OLD."title" IS NOT NEW."title" OR OLD."priority" IS NOT NEW."priority" OR OLD."project_id" IS NOT NEW."project_id" OR OLD."status" IS NOT NEW."status" OR OLD."status_entered_at" IS NOT NEW."status_entered_at"`) {
		t.Errorf("SQLite's update trigger compares other columns:\n%s", update)
	}
	// SQLite's image writes a BLOB a raw write stored as its hex, which
	// json_object would refuse.
	if !strings.Contains(update, `json_object('title', CASE WHEN typeof(NEW."title") = 'blob' THEN hex(NEW."title") ELSE NEW."title" END, `) {
		t.Errorf("SQLite's image takes a BLOB as it is:\n%s", update)
	}
	// Postgres's message is created and due when the write was made, the
	// order the change queue's claim keeps one row's line in.
	if pg := task.FunctionSQL(); !strings.Contains(pg, "DECLARE\n  written timestamptz := clock_timestamp();\n") ||
		!strings.Contains(pg, `"before", "after", "due_at", "created_at")`) || !strings.Contains(pg, "floor(extract(epoch FROM written) * 1000)::bigint") ||
		!strings.Contains(pg, "-- Writes a message of TaskChange for each write of a row of task\n") {
		t.Errorf("Postgres's function does not create its message at one read of the clock:\n%s", pg)
	}
}

// A soft-deletable table's live column is not in the image, and the
// update's guard compares it beside the image's columns, so a soft delete
// and a restore, which move it alone, are heard (D103). A POINT, which
// Postgres has no equality for, is left out of the image and the guard,
// whose comparison would fail every write of the table.
func TestChangeTriggerHearsASoftDeleteAndLeavesAPointOut(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-reactions-db"))
	if err != nil {
		t.Fatal(err)
	}
	withSoftDeleteAndPoint(schema)
	output, err := Generate(schema, Options{SchemaName: "fixture-reactions-db"})
	if err != nil {
		t.Fatal(err)
	}
	trigger := output.ChangeTriggers[1]
	if trigger.Live != "deleted_at" || strings.Join(trigger.Image, ",") != "title,priority,project_id,status,status_entered_at" {
		t.Fatalf("Task's live column is %q and image %v, want deleted_at and the image without it or location", trigger.Live, trigger.Image)
	}
	pg := trigger.FunctionSQL()
	if !strings.Contains(pg, `OLD."status_entered_at" IS DISTINCT FROM NEW."status_entered_at" OR OLD."deleted_at" IS DISTINCT FROM NEW."deleted_at") THEN`) || strings.Contains(pg, "location") ||
		strings.Contains(pg, `'deleted_at'`) {
		t.Errorf("Postgres's guard does not compare deleted_at alone of the two:\n%s", pg)
	}
	update := trigger.SQLiteTriggers(sqliteQuote)[1].Definition
	if !strings.Contains(update, `OLD."status_entered_at" IS NOT NEW."status_entered_at" OR OLD."deleted_at" IS NOT NEW."deleted_at"`+"\n") || strings.Contains(update, `'deleted_at'`) {
		t.Errorf("SQLite's guard does not compare deleted_at:\n%s", update)
	}

	// A table whose image is empty and that is soft-deletable still hears
	// an update, of its live column.
	empty := trigger
	empty.Image = nil
	if pg := empty.FunctionSQL(); !strings.Contains(pg, `IF TG_OP = 'UPDATE' AND NOT (OLD."deleted_at" IS DISTINCT FROM NEW."deleted_at") THEN`) {
		t.Errorf("Postgres's function of an empty image of a soft-deletable table:\n%s", pg)
	}
	if n := len(empty.SQLiteTriggers(sqliteQuote)); n != 3 {
		t.Errorf("SQLite has %d triggers of an empty image of a soft-deletable table, want an update one too", n)
	}
}

// withSoftDeleteAndPoint makes fixture-reactions-db's Task soft-deletable,
// with a deletedAt audit field, and gives it a location, a Geo.Location the
// catalog stores as a POINT.
func withSoftDeleteAndPoint(schema *ir.Schema) {
	if schema.Scalars == nil {
		schema.Scalars = map[string]*ir.ScalarDef{}
	}
	schema.Scalars["Geo.Location"] = &ir.ScalarDef{Name: "Geo.Location", LanguagePrimitive: ir.LanguageString, TypeMappings: map[string]string{"sql": "POINT"}}
	task := schema.Types["Task"]
	task.Fields = append(task.Fields,
		&ir.FieldDef{Name: "deletedAt", TypeRef: ir.TypeRef{Name: "Temporal.DateTime"}},
		&ir.FieldDef{Name: "location", TypeRef: ir.TypeRef{Name: "Geo.Location"}})
}

// An image wider than one JSON call takes is joined from several: on
// Postgres jsonb_build_object calls of 50 columns joined with ||, on SQLite
// json_object calls of 16, D1's limit of 32 arguments, nested in
// json_patch. An empty image writes no update: Postgres's function returns
// at an update, and SQLite has no update trigger.
func TestChangeTriggerImages(t *testing.T) {
	wide := ChangeTrigger{Origin: "Wide", Queue: "WideChange", Table: "wide", QuotedTable: "wide", QueueTable: "wide_change_queue",
		FunctionName: "wide_changes", TriggerName: "trg_wide_changes", Key: "id"}
	for i := 1; i <= 60; i++ {
		wide.Image = append(wide.Image, fmt.Sprintf("c%02d", i))
	}
	pg := wide.FunctionSQL()
	if n := strings.Count(pg, "jsonb_build_object("); n != 8 || !strings.Contains(pg, `'c50', NEW."c50") || jsonb_build_object('c51', NEW."c51"`) {
		t.Errorf("Postgres's four images (%d calls) are not each two calls of 50 and 10 joined:\n%s", n, pg)
	}
	insert := wide.SQLiteTriggers(sqliteQuote)[0].Definition
	value := func(c string) string {
		return `CASE WHEN typeof(NEW."` + c + `") = 'blob' THEN hex(NEW."` + c + `") ELSE NEW."` + c + `" END`
	}
	if strings.Count(insert, "json_object(") != 4 || strings.Count(insert, "json_patch(") != 3 ||
		!strings.Contains(insert, `json_patch(json_patch(json_patch(json_object('c01', `+value("c01")) || !strings.Contains(insert, `'c16', `+value("c16")+`), json_object('c17', `+value("c17")) {
		t.Errorf("SQLite's image is not four calls of at most 16 nested in json_patch:\n%s", insert)
	}

	empty := wide
	empty.Image = nil
	if pg := empty.FunctionSQL(); !strings.Contains(pg, "  IF TG_OP = 'UPDATE' THEN\n    RETURN NULL;\n  END IF;\n") || !strings.Contains(pg, "'{}'::jsonb") {
		t.Errorf("Postgres's function of an empty image:\n%s", pg)
	}
	var names []string
	for _, trigger := range empty.SQLiteTriggers(sqliteQuote) {
		names = append(names, trigger.Name)
		if !strings.Contains(trigger.Definition, "'{}'") {
			t.Errorf("SQLite's %s writes no empty image:\n%s", trigger.Name, trigger.Definition)
		}
	}
	if strings.Join(names, ",") != "trg_wide_changes_insert,trg_wide_changes_delete" {
		t.Errorf("SQLite's triggers of an empty image = %v, want no update trigger", names)
	}
}
