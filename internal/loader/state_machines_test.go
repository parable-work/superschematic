package loader

import (
	"path/filepath"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// stateMachineFixture is a database whose Ticket declares a machine on a
// field with a default, on two nullable fields and on a field typed by a
// dependency's enum, beside a @jsonField type with a machine of its own
// (D118).
const stateMachineFixture = "tsreader/testdata/services/fixture-state-machine-db"

// transitionsFrom lists the states a machine's transitions leave, in
// their order.
func transitionsFrom(m *ir.StateMachine) string {
	var from []string
	for _, tr := range m.Transitions {
		from = append(from, tr.From)
	}
	return strings.Join(from, ",")
}

// TestStateMachineFixtureLoads: the fixture loads and verifies; each
// machine's transitions are in the order of its enum's members, the
// schema's own and the dependency's, whatever order the decorator's
// object wrote them in, and each list of states keeps the order written;
// and the loaded IR is held to its golden (D118).
func TestStateMachineFixtureLoads(t *testing.T) {
	db, err := LoadService(filepath.FromSlash(stateMachineFixture))
	if err != nil {
		t.Fatal(err)
	}
	ticket := db.Types["Ticket"]
	if ticket == nil || len(ticket.StateMachines) != 4 {
		t.Fatalf("Ticket = %s", marshal(t, ticket))
	}
	for field, want := range map[string]string{
		// The decorator writes Resolved, Open, InProgress.
		"state":         "open,in_progress,resolved",
		"review":        "RETRY",
		"escalation":    "raised,acknowledged",
		"accountStatus": "active,suspended",
	} {
		machine := ticket.StateMachineOf(field)
		if machine == nil {
			t.Fatalf("Ticket has no machine on %s", field)
		}
		if got := transitionsFrom(machine); got != want {
			t.Errorf("the transitions of %s leave %s, want %s", field, got, want)
		}
	}
	state := ticket.StateMachineOf("state")
	if got := strings.Join(state.Transitions[1].To, ","); got != "resolved,open" {
		t.Errorf("in_progress moves to %s, want resolved,open as written", got)
	}
	if db.Types["SignOff"].StateMachineOf("step") == nil {
		t.Errorf("SignOff = %s", marshal(t, db.Types["SignOff"]))
	}
	compareExpanded(t, db, "fixture-state-machine-db.expanded.json")
}

// stateMachineService writes a DB service whose Ticket's stateMachines are
// the JSON given, in a JSON schema file.
func stateMachineService(t *testing.T, machines string) string {
	t.Helper()
	return writeService(t, map[string]string{
		"schema.config.json": `{"name": "temp-db", "kind": "DB", "dependencies": [{"name": "plans", "kind": "General"}], "outputs": {}}`,
		"src/ticket.schema.json": `{
			"imports": [{"package": "@schemas/plans", "types": ["Tier"]}],
			"enums": {
				"TicketState": {"name": "TicketState", "values": [
					{"name": "Open", "serializedAs": "open"}, {"name": "Resolved", "serializedAs": "resolved"}, {"name": "Closed", "serializedAs": "closed"}]}
			},
			"types": {
				"Ticket": {
					"name": "Ticket",
					"role": "DBTable",
					"fields": [
						{"name": "id", "typeRef": {"name": "string"}, "required": true, "key": true},
						{"name": "state", "typeRef": {"name": "TicketState"}, "required": true, "default": "open"},
						{"name": "review", "typeRef": {"name": "TicketState"}},
						{"name": "tier", "typeRef": {"name": "Tier"}, "required": true}
					],
					"stateMachines": ` + machines + `
				}
			}
		}`,
	})
}

// TestStateMachineLoadsFromADataForm: a JSON schema file writes a type's
// machines under stateMachines, each state as its member's value, and the
// loader gives the transitions the enum's order there too (D118).
func TestStateMachineLoadsFromADataForm(t *testing.T) {
	dir := stateMachineService(t, `[
		{"field": "state", "terminal": ["closed"], "transitions": [
			{"from": "resolved", "to": ["closed", "open"]},
			{"from": "open", "to": ["resolved", "closed"]}]},
		{"field": "review", "fromNull": ["open"], "terminal": ["closed"], "transitions": [
			{"from": "resolved", "to": ["closed"], "toNull": true},
			{"from": "open", "to": ["resolved"]}]}
	]`)
	schema, err := LoadService(dir)
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	ticket := schema.Types["Ticket"]
	state, review := ticket.StateMachineOf("state"), ticket.StateMachineOf("review")
	if state == nil || review == nil {
		t.Fatalf("Ticket = %s", marshal(t, ticket))
	}
	if got := transitionsFrom(state); got != "open,resolved" {
		t.Errorf("the transitions of state leave %s, want open,resolved", got)
	}
	if got := strings.Join(state.Transitions[1].To, ","); got != "closed,open" {
		t.Errorf("resolved moves to %s, want closed,open as written", got)
	}
	if got := transitionsFrom(review); got != "open,resolved" || !review.Transitions[1].ToNull || strings.Join(review.FromNull, ",") != "open" {
		t.Errorf("review = %s", marshal(t, review))
	}
}

// TestStateMachineRefusedInADataForm: what the build refuses of a machine
// a JSON schema file writes (D118): the JSON Schema an empty fromNull,
// ir.Schema.Validate what the declaration decides alone, and verification
// what needs the field and its enum, a dependency's enum among it, which
// a data form has no resolver for.
func TestStateMachineRefusedInADataForm(t *testing.T) {
	for _, tc := range []struct {
		name     string
		machines string
		want     string
	}{
		{"an empty fromNull", `[{"field": "review", "fromNull": []}]`, "minItems: got 0, want 1"},
		{"a terminal state with a transition", `[{"field": "state", "terminal": ["closed"], "transitions": [
			{"from": "open", "to": ["resolved", "closed"]}, {"from": "resolved", "to": ["open"]}, {"from": "closed", "to": ["open"]}]}]`,
			`temp-db: @stateMachine on Ticket.state: "closed" is terminal and has a transition; take it out of terminal, or drop its transitions`},
		{"two machines on a field", `[{"field": "state"}, {"field": "state"}]`,
			"temp-db: @stateMachine on Ticket.state: Ticket declares two state machines on state"},
		{"a member's name for its value", `[{"field": "state", "terminal": ["Closed"], "transitions": [{"from": "open", "to": ["resolved", "closed"]}]}]`,
			`@stateMachine on Ticket.state: terminal names "Closed", which is no member of TicketState; a state is a member's value, one of "open", "resolved", "closed"`},
		{"a state no move reaches", `[{"field": "state", "terminal": ["closed"], "transitions": [{"from": "open", "to": ["closed"]}]}]`,
			`@stateMachine on Ticket.state: no move reaches "resolved" from state's default, "open", so no update could put a row there; list a move to it, or take the member out of TicketState`},
		{"a dependency's enum", `[{"field": "tier", "terminal": ["paid"], "transitions": [{"from": "free", "to": ["paid"]}]}]`,
			"@stateMachine on Ticket.tier: tier is typed by Tier, which temp-db imports and the build does not read as an enum"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadService(stateMachineService(t, tc.machines))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LoadService = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

// TestStateMachineRefusedOnABehaviorsField: a table's Workflow owns its
// status, whose moves are the behavior's, so a @stateMachine on it is
// refused with the behavior's name (D118).
func TestStateMachineRefusedOnABehaviorsField(t *testing.T) {
	dir := writeService(t, map[string]string{
		"schema.config.json": `{"name": "temp-db", "kind": "DB", "outputs": {}}`,
		"src/ticket.schema.json": `{
			"types": {
				"Ticket": {
					"name": "Ticket",
					"role": "DBTable",
					"fields": [{"name": "id", "typeRef": {"name": "string"}, "required": true, "key": true}],
					"behaviors": [{"name": "Workflow", "field": "status", "config": {
						"states": ["open", "closed"], "transitions": [{"from": "open", "to": "closed"}]}}],
					"stateMachines": [{"field": "status", "terminal": ["closed"], "transitions": [{"from": "open", "to": ["closed"]}]}]
				}
			}
		}`,
	})
	_, err := LoadService(dir)
	const want = "@stateMachine on Ticket.status: status is a field the Workflow behavior owns, so its moves are the behavior's; declare them in its config, and take this declaration off"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("LoadService = %v, want an error containing %q", err, want)
	}
}
