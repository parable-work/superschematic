package sqlgen

import (
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// enqueueSchema is a table of tasks whose deletes, when the task is done,
// open and not urgent, enqueue the queue TaskGone, which appends; and
// whose writes of any other kind enqueue nothing.
func enqueueSchema(name string) *ir.Schema {
	s := ir.NewSchema("tasks-db", ir.SchemaKindDB)
	s.Enums["Status"] = &ir.EnumDef{Name: "Status", Values: []ir.EnumValueDef{{Name: "Open", SerializedAs: "open"}}}
	s.Types[name] = &ir.TypeDef{Name: name, Role: ir.RoleDBTable, Fields: []*ir.FieldDef{
		{Name: "title", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		{Name: "done", TypeRef: ir.TypeRef{Name: "boolean"}, Required: true},
		{Name: "urgent", TypeRef: ir.TypeRef{Name: "boolean"}},
		{Name: "rank", TypeRef: ir.TypeRef{Name: "number"}, Required: true},
		{Name: "status", TypeRef: ir.TypeRef{Name: "Status"}, Required: true},
	}, EnqueueOnChange: []*ir.EnqueueOnChange{{
		Queue: "TaskGone",
		On:    []string{ir.EnqueueOnDelete},
		When: []*ir.EnqueueCondition{
			{Field: "done", Equals: true},
			{Field: "urgent", IsNull: true},
			{Field: "rank", Equals: 2.5},
			{Field: "title", Equals: "it's"},
			{Field: "status", Equals: "open"},
		},
		Message: map[string]string{"taskId": "id", "title": "title"},
	}}}
	s.Types["TaskGone"] = &ir.TypeDef{Name: "TaskGone", Role: ir.RoleEmbeddedStruct, Queue: &ir.QueueDef{}, Fields: []*ir.FieldDef{
		{Name: "taskId", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
		{Name: "title", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
	}}
	s.Scalars["Identity.UUID"] = &ir.ScalarDef{Name: "Identity.UUID", LanguagePrimitive: ir.LanguageString, TypeMappings: map[string]string{"sql": "UUID"}}
	return s
}

// TestEnqueueTriggerOfADelete: a declaration of a delete alone reads the
// row as it was, with no comparison of fields; each condition renders in
// its dialect, a boolean as TRUE on Postgres and 1 on SQLite, a text with
// its quote doubled; and a queue that appends ends the insert with no
// conflict (D80).
func TestEnqueueTriggerOfADelete(t *testing.T) {
	out, err := Generate(enqueueSchema("Task"), Options{SchemaName: "tasks-db"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.EnqueueTriggers) != 1 {
		t.Fatalf("EnqueueTriggers = %+v", out.EnqueueTriggers)
	}
	e := out.EnqueueTriggers[0]
	pg := e.FunctionSQL()
	for _, want := range []string{
		"CREATE OR REPLACE FUNCTION task_enqueue_task_gone() RETURNS trigger AS $$",
		"  IF OLD.done = TRUE AND OLD.urgent IS NULL AND OLD.rank = 2.5 AND OLD.title = 'it''s' AND OLD.status = 'open' THEN\n" +
			"    INSERT INTO task_gone_queue (task_id, title) VALUES (OLD.id, OLD.title);\n  END IF;\n  RETURN NULL;\nEND;",
	} {
		if !strings.Contains(pg, want) {
			t.Errorf("the function lacks\n%s\nin\n%s", want, pg)
		}
	}
	if strings.Contains(pg, "TG_OP") || strings.Contains(pg, "ON CONFLICT") {
		t.Errorf("a delete's function of a queue that appends reads TG_OP or writes a conflict:\n%s", pg)
	}
	trigger, err := e.Trigger().SQL()
	if err != nil {
		t.Fatal(err)
	}
	if trigger != "CREATE TRIGGER trg_task_enqueue_task_gone\n  AFTER DELETE ON task\n  FOR EACH ROW EXECUTE FUNCTION task_enqueue_task_gone()" {
		t.Errorf("trigger:\n%s", trigger)
	}
	quote := func(s string) string { return `"` + s + `"` }
	sqlite := e.SQLiteTriggers(quote)
	if len(sqlite) != 1 || sqlite[0].Name != "trg_task_enqueue_task_gone_delete" {
		t.Fatalf("SQLite triggers = %+v", sqlite)
	}
	want := `CREATE TRIGGER "trg_task_enqueue_task_gone_delete" AFTER DELETE ON "task" FOR EACH ROW
WHEN OLD."done" = 1 AND OLD."urgent" IS NULL AND OLD."rank" = 2.5 AND OLD."title" = 'it''s' AND OLD."status" = 'open'
BEGIN
  INSERT INTO "task_gone_queue" ("task_id", "title") VALUES (OLD."id", OLD."title");
END`
	if sqlite[0].Definition != want {
		t.Errorf("SQLite trigger:\n%s\nwant\n%s", sqlite[0].Definition, want)
	}
}

// TestEnqueueTriggerNamesFitPostgres: a trigger's name over Postgres's
// 63 bytes is refused, naming the fix.
func TestEnqueueTriggerNamesFitPostgres(t *testing.T) {
	_, err := Generate(enqueueSchema("TaskWithANameLongEnoughToPassTheLimitOfPostgres"), Options{SchemaName: "tasks-db"})
	if err == nil || !strings.Contains(err.Error(), "over PostgreSQL's 63-byte limit; give the table or the queue a shorter name") {
		t.Errorf("Generate = %v, want the long name refused", err)
	}
}

// TestEnqueueTriggerLeavesADefaultedFieldToItsColumn: a required field of
// the message the declaration does not map, with a Default<T, V> its column
// carries (D83), is left out of the trigger's insert, and the queue's
// column has the DEFAULT that fills it (D80).
func TestEnqueueTriggerLeavesADefaultedFieldToItsColumn(t *testing.T) {
	s := enqueueSchema("Task")
	zero := "0"
	s.Types["TaskGone"].Fields = append(s.Types["TaskGone"].Fields, &ir.FieldDef{Name: "retries", TypeRef: ir.TypeRef{Name: "number"}, Required: true, Default: &zero})
	out, err := Generate(s, Options{SchemaName: "tasks-db"})
	if err != nil {
		t.Fatal(err)
	}
	e := out.EnqueueTriggers[0]
	if strings.Join(e.Columns, ",") != "task_id,title" {
		t.Errorf("the insert sets %v, want task_id and title", e.Columns)
	}
	for _, table := range out.Tables {
		if table.Name != "task_gone_queue" {
			continue
		}
		for _, c := range table.Columns {
			if c.Name == "retries" && c.DefaultValue != "0" {
				t.Errorf("retries defaults to %q, want 0", c.DefaultValue)
			}
		}
	}
}
