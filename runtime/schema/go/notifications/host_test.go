package notifications

import "testing"

// TestMustParseReadsTheDefinitions: the definitions the generated ORM
// holds read back as parts and tables, an event found by its table and
// kind, and none for a kind the table does not declare (D190).
func TestMustParseReadsTheDefinitions(t *testing.T) {
	defs := MustParse(`{"parts":{"inbox":{"type":"Notice","table":"notice","columns":{"recipient":{"name":"recipient","uuid":true}}}},"tables":{"Ticket":{"key":"id","events":[{"kind":"ticket.overdue","payload":["title"],"title":"title","urgency":"urgent"}],"relations":{"project":{"target":"Project","member":"project"}}}}}`)
	if inbox := defs.Parts["inbox"]; inbox.Type != "Notice" || !inbox.Columns["recipient"].UUID {
		t.Fatalf("the inbox: %+v", inbox)
	}
	event, ok := defs.EventOf("Ticket", "ticket.overdue")
	if !ok || event.Urgency != "urgent" || event.Title != "title" || len(event.Payload) != 1 {
		t.Fatalf("the event: %+v %v", event, ok)
	}
	if _, ok := defs.EventOf("Ticket", "ticket.lost"); ok {
		t.Fatal("an undeclared kind was found")
	}
	if relation := defs.Tables["Ticket"].Relations["project"]; relation.Target != "Project" {
		t.Fatalf("the relation: %+v", relation)
	}
	if empty := MustParse(`{}`); empty.Parts == nil || empty.Tables == nil {
		t.Fatalf("empty definitions: %+v", empty)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("MustParse read JSON no generator wrote")
		}
	}()
	MustParse(`{`)
}
