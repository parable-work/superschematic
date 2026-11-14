package sqlgen

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// guardSchema is one table, typeName, whose field fieldName is typed by an
// enum of the states given, with a machine that moves the first state to
// each of the others, which are terminal.
func guardSchema(typeName, fieldName string, states ...string) *ir.Schema {
	s := ir.NewSchema("guards-db", ir.SchemaKindDB)
	enum := &ir.EnumDef{Name: "Stage"}
	for i, state := range states {
		enum.Values = append(enum.Values, ir.EnumValueDef{Name: "S" + string(rune('A'+i)), SerializedAs: state})
	}
	s.Enums["Stage"] = enum
	s.Types[typeName] = &ir.TypeDef{Name: typeName, Role: ir.RoleDBTable, Fields: []*ir.FieldDef{
		{Name: "title", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		{Name: fieldName, TypeRef: ir.TypeRef{Name: "Stage"}, Required: true},
	}, StateMachines: []*ir.StateMachine{{
		Field:       fieldName,
		Transitions: []*ir.StateTransition{{From: states[0], To: states[1:]}},
		Terminal:    states[1:],
	}}}
	return s
}

// TestStateGuardsOfTheFixture: each @stateMachine of a table gets a guard,
// by table and column, named after both, whose trigger refuses a write; a
// class that is no table, the @jsonField SignOff, gets none (D118).
func TestStateGuardsOfTheFixture(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-state-machine-db"))
	if err != nil {
		t.Fatal(err)
	}
	if signOff := schema.Types["SignOff"]; signOff == nil || len(signOff.StateMachines) != 1 {
		t.Fatalf("the fixture's SignOff declares no machine: %+v", signOff)
	}
	out, err := Generate(schema, Options{SchemaName: "fixture-state-machine-db"})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, g := range out.StateGuards {
		if g.Origin != "Ticket" || g.Table != "ticket" {
			t.Errorf("guard %s is of %s on %s, want Ticket on ticket", g.TriggerName, g.Origin, g.Table)
		}
		if g.FunctionName != g.Table+"_"+g.Column+"_transition_guard" || g.TriggerName != "trg_"+g.FunctionName {
			t.Errorf("the guard of %s.%s is the function %s and the trigger %s", g.Table, g.Column, g.FunctionName, g.TriggerName)
		}
		trigger := g.Trigger()
		if !trigger.Refuses || trigger.Timing != "BEFORE UPDATE OF "+g.QuotedColumn || trigger.Function != g.FunctionName {
			t.Errorf("the trigger of %s is %+v", g.TriggerName, trigger)
		}
		if sqlite := g.SQLiteTrigger(sqlutil.QuoteIdentifier); !sqlite.Refuses || sqlite.Name != g.TriggerName || sqlite.Table != g.Table {
			t.Errorf("the SQLite trigger of %s is %+v", g.TriggerName, sqlite)
		}
		names = append(names, g.TriggerName)
	}
	want := []string{
		"trg_ticket_account_status_transition_guard", "trg_ticket_escalation_transition_guard",
		"trg_ticket_review_transition_guard", "trg_ticket_state_transition_guard",
	}
	if !slices.Equal(names, want) {
		t.Errorf("the guards are %v, want %v", names, want)
	}
}

// TestStateGuardRule: the two dialects write one rule. An unchanged value
// passes first, a terminal state is not left, a null takes a state of
// fromNull, and a state moves to its targets or back to null; Postgres
// raises check_violation with the two values, and SQLite fails with a
// fixed message (D118).
func TestStateGuardRule(t *testing.T) {
	g := StateGuard{
		Origin: "Ticket", Field: "review", Table: "ticket", QuotedTable: "ticket", Column: "review", QuotedColumn: "review",
		FunctionName: "ticket_review_transition_guard", TriggerName: "trg_ticket_review_transition_guard",
		Terminal: []string{"APPROVED"}, FromNull: []string{"RETRY", "APPROVED"},
		Moves: []StateGuardMove{{From: "RETRY", To: []string{"APPROVED"}, ToNull: true}, {From: "HELD", ToNull: true}, {From: "IDLE"}},
	}
	pg := g.FunctionSQL()
	for _, want := range []string{
		"CREATE OR REPLACE FUNCTION ticket_review_transition_guard() RETURNS trigger AS $$\nBEGIN\n",
		"  IF NEW.review IS NOT DISTINCT FROM OLD.review THEN\n    RETURN NEW;\n  END IF;\n" +
			"  IF OLD.review IN ('APPROVED') THEN\n" +
			"    RAISE EXCEPTION 'ticket.review is terminal at %: it cannot move to %', OLD.review, NEW.review\n" +
			"      USING ERRCODE = 'check_violation';\n  END IF;\n" +
			"  IF (OLD.review IS NULL AND NEW.review IN ('RETRY', 'APPROVED'))\n" +
			"    OR (OLD.review = 'RETRY' AND (NEW.review IS NULL OR NEW.review IN ('APPROVED')))\n" +
			"    OR (OLD.review = 'HELD' AND NEW.review IS NULL)\n" +
			"  THEN\n    RETURN NEW;\n  END IF;\n" +
			"  RAISE EXCEPTION 'ticket.review cannot move from % to %', OLD.review, NEW.review\n" +
			"    USING ERRCODE = 'check_violation';\nEND;\n$$ LANGUAGE plpgsql",
	} {
		if !strings.Contains(pg, want) {
			t.Errorf("the function lacks %q:\n%s", want, pg)
		}
	}
	sqlite := g.SQLiteTrigger(func(name string) string { return `"` + name + `"` }).Definition
	want := `CREATE TRIGGER "trg_ticket_review_transition_guard" BEFORE UPDATE OF "review" ON "ticket" FOR EACH ROW` + "\n" +
		`WHEN NEW."review" IS NOT OLD."review" AND (` + "\n" +
		`  OLD."review" IN ('APPROVED')` + "\n" +
		`  OR NOT COALESCE(` + "\n" +
		`    (OLD."review" IS NULL AND NEW."review" IN ('RETRY', 'APPROVED'))` + "\n" +
		`    OR (OLD."review" IS 'RETRY' AND (NEW."review" IS NULL OR NEW."review" IN ('APPROVED')))` + "\n" +
		`    OR (OLD."review" IS 'HELD' AND NEW."review" IS NULL), 0))` + "\n" +
		"BEGIN\n  SELECT RAISE(ABORT, 'ticket.review cannot make this move: its state machine does not list it');\nEND"
	if sqlite != want {
		t.Errorf("the SQLite trigger is\n%s\nwant\n%s", sqlite, want)
	}

	// A machine that lists no move refuses every change.
	g.Terminal, g.FromNull, g.Moves = nil, nil, nil
	if pg := g.FunctionSQL(); strings.Contains(pg, "IN (") || strings.Count(pg, "RETURN NEW") != 1 {
		t.Errorf("the function of a machine with no move is\n%s", pg)
	}
	if sqlite := g.SQLiteTrigger(sqlutil.QuoteIdentifier).Definition; !strings.Contains(sqlite, "WHEN NEW.review IS NOT OLD.review AND (\n  1)\n") {
		t.Errorf("the SQLite trigger of a machine with no move is\n%s", sqlite)
	}
}

// TestStateGuardQuotesStates: a state is written as a string literal, its
// quote doubled; a function whose body holds $$ is quoted with a tag the
// body does not hold; and a column named by a keyword is quoted (D118).
func TestStateGuardQuotesStates(t *testing.T) {
	out, err := Generate(guardSchema("Task", "order", "it's", "a$$b", "$guard$"), Options{SchemaName: "guards-db"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.StateGuards) != 1 {
		t.Fatalf("StateGuards = %+v", out.StateGuards)
	}
	g := out.StateGuards[0]
	pg := g.FunctionSQL()
	for _, want := range []string{
		"CREATE OR REPLACE FUNCTION task_order_transition_guard() RETURNS trigger AS $guard1$\nBEGIN\n",
		`  IF NEW."order" IS NOT DISTINCT FROM OLD."order" THEN`,
		`  IF OLD."order" IN ('a$$b', '$guard$') THEN`,
		`  IF (OLD."order" = 'it''s' AND NEW."order" IN ('a$$b', '$guard$'))`,
		"END;\n$guard1$ LANGUAGE plpgsql",
	} {
		if !strings.Contains(pg, want) {
			t.Errorf("the function lacks %q:\n%s", want, pg)
		}
	}
	if trigger, err := g.Trigger().SQL(); err != nil || !strings.Contains(trigger, `BEFORE UPDATE OF "order" ON task`) {
		t.Errorf("the trigger is %q (%v)", trigger, err)
	}
	sqlite := g.SQLiteTrigger(func(name string) string { return `"` + name + `"` }).Definition
	if !strings.Contains(sqlite, `(OLD."order" IS 'it''s' AND NEW."order" IN ('a$$b', '$guard$'))`) {
		t.Errorf("the SQLite trigger is\n%s", sqlite)
	}
}

// TestStateGuardNameLimit: a guard's trigger is named after its table and
// its column, and one over PostgreSQL's 63 bytes fails the build, naming
// the type, the field and the table; one of 63 bytes builds (D118).
func TestStateGuardNameLimit(t *testing.T) {
	// trg_ and _transition_guard take 21 bytes, and the underscore between
	// the table and the column one more: 41 are left for the two names.
	fits := guardSchema("Shipment"+strings.Repeat("x", 27), "status", "new", "done")
	out, err := Generate(fits, Options{SchemaName: "guards-db"})
	if err != nil {
		t.Fatalf("a guard whose trigger's name is 63 bytes: %v", err)
	}
	if name := out.StateGuards[0].TriggerName; len(name) != sqlutil.PostgresIdentifierLimit {
		t.Fatalf("the trigger %s is %d bytes, want %d", name, len(name), sqlutil.PostgresIdentifierLimit)
	}

	typeName := "Shipment" + strings.Repeat("x", 28)
	_, err = Generate(guardSchema(typeName, "status", "new", "done"), Options{SchemaName: "guards-db"})
	if err == nil {
		t.Fatal("a guard whose trigger's name is 64 bytes built")
	}
	table := strings.ToLower(typeName)
	for _, want := range []string{
		"@stateMachine on " + typeName + ".status",
		"table " + table,
		"trg_" + table + "_status_transition_guard, 64 bytes, over PostgreSQL's 63-byte limit",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error lacks %q: %v", want, err)
		}
	}
}

// TestStateGuardNeedsATable: a machine on a class that is no table, here a
// @jsonField one and an API type, gets no guard and fails nothing.
func TestStateGuardNeedsATable(t *testing.T) {
	s := guardSchema("Task", "stage", "new", "done")
	for name, change := range map[string]func(td *ir.TypeDef){
		"Payload": func(td *ir.TypeDef) { td.JsonField = true },
		"View":    func(td *ir.TypeDef) { td.Role = ir.RoleAPIView },
	} {
		td := *s.Types["Task"]
		td.Name = name
		change(&td)
		s.Types[name] = &td
	}
	out, err := Generate(s, Options{SchemaName: "guards-db"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.StateGuards) != 1 || out.StateGuards[0].Origin != "Task" {
		t.Errorf("StateGuards = %+v, want Task's alone", out.StateGuards)
	}
}
