package tsreader

import (
	"reflect"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestStateMachinesAreRead: each @stateMachine of a class is recorded in
// the order written, every state as the value its enum member serializes
// to, a null in a state's list as a return to null, and each list of
// states as written (D118). An object's keys reach the decorator in no
// order, so the reader writes a machine's transitions by their state's
// value; the loader gives them the enum's order.
func TestStateMachinesAreRead(t *testing.T) {
	schema, _, in, err := LoadServiceWithConfig("testdata/services/fixture-state-machine-db")
	if err != nil {
		t.Fatal(err)
	}
	ticket := schema.Types["Ticket"]
	if ticket == nil || ticket.Role != ir.RoleDBTable {
		t.Fatalf("Ticket = %+v, want a table", ticket)
	}
	want := []*ir.StateMachine{
		{
			Field: "state",
			Transitions: []*ir.StateTransition{
				{From: "in_progress", To: []string{"resolved", "open"}},
				{From: "open", To: []string{"open", "in_progress", "resolved", "closed"}},
				{From: "resolved", To: []string{"open", "closed"}},
			},
			Terminal: []string{"closed"},
		},
		{
			Field:       "review",
			Transitions: []*ir.StateTransition{{From: "RETRY", To: []string{"APPROVED", "REJECTED"}, ToNull: true}},
			Terminal:    []string{"APPROVED", "REJECTED"},
			FromNull:    []string{"RETRY", "APPROVED", "REJECTED"},
		},
		{
			Field: "escalation",
			Transitions: []*ir.StateTransition{
				{From: "acknowledged", ToNull: true},
				{From: "raised", To: []string{"acknowledged"}},
			},
			FromNull: []string{"raised"},
		},
		{
			Field: "accountStatus",
			Transitions: []*ir.StateTransition{
				{From: "active", To: []string{"suspended", "closed"}},
				{From: "suspended", To: []string{"active", "closed"}},
			},
			Terminal: []string{"closed"},
		},
	}
	if !reflect.DeepEqual(ticket.StateMachines, want) {
		t.Errorf("Ticket.StateMachines = %s", describeStateMachines(ticket.StateMachines))
	}

	// A class that is no table keeps its machine.
	signOff := schema.Types["SignOff"]
	if signOff == nil || !signOff.JsonField {
		t.Fatalf("SignOff = %+v, want a @jsonField type", signOff)
	}
	wantSignOff := []*ir.StateMachine{{
		Field:       "step",
		Transitions: []*ir.StateTransition{{From: "requested", To: []string{"granted", "refused"}}},
		Terminal:    []string{"granted", "refused"},
	}}
	if !reflect.DeepEqual(signOff.StateMachines, wantSignOff) {
		t.Errorf("SignOff.StateMachines = %s", describeStateMachines(signOff.StateMachines))
	}

	// The dependency's enum is not the schema's own: the reader hands it
	// to the loader's checks, with each member's value.
	if schema.Enums["AccountStatus"] != nil {
		t.Errorf("the schema holds the dependency's enum AccountStatus")
	}
	status := in.ExternalEnums["AccountStatus"]
	if status == nil || len(status.Values) != 3 || status.Values[1].Name != "Suspended" || status.Values[1].Serialized() != "suspended" {
		t.Errorf("ExternalEnums[AccountStatus] = %+v", status)
	}
}

func describeStateMachines(machines []*ir.StateMachine) string {
	var b strings.Builder
	for _, m := range machines {
		b.WriteString("\n" + m.Field + ":")
		for _, tr := range m.Transitions {
			b.WriteString(" " + tr.From + "->[" + strings.Join(tr.To, ","))
			if tr.ToNull {
				b.WriteString(",null")
			}
			b.WriteString("]")
		}
		b.WriteString(" terminal=[" + strings.Join(m.Terminal, ",") + "] fromNull=[" + strings.Join(m.FromNull, ",") + "]")
	}
	return b.String()
}

// TestStateMachineRefusesASpreadInTransitions: the compiler takes a spread
// in transitions, whose moves the static read cannot see where the
// decorator is written, so the reader refuses it at the spread (D118).
func TestStateMachineRefusesASpreadInTransitions(t *testing.T) {
	_, _, err := LoadService("testdata/services/broken-state-machine-spread")
	if err == nil {
		t.Fatal("LoadService took a spread in a @stateMachine's transitions")
	}
	if want := "src/broken.schema.ts:16:18: object arguments must use plain property assignments"; !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want it to contain %q", err, want)
	}
}

// TestStateMachineRefusesATerminalStateWithATransition: the compiler takes
// a graph whose terminal state has a transition, so the build refuses it,
// with the type, the field and the state (D118).
func TestStateMachineRefusesATerminalStateWithATransition(t *testing.T) {
	_, _, err := LoadService("testdata/services/broken-state-machine-terminal-exit")
	if err == nil {
		t.Fatal("LoadService took a terminal state with a transition")
	}
	if want := `broken-state-machine-terminal-exit: @stateMachine on Job.state: "DONE" is terminal and has a transition; take it out of terminal, or drop its transitions`; !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want it to contain %q", err, want)
	}
}
