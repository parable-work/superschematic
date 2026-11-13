package ir

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// ticketSchema is a DB schema with a Ticket whose state, a required field
// with a default, moves among four states, and whose review, a nullable
// field, starts at null and may return to it (D117).
func ticketSchema() *Schema {
	s := NewSchema("support-db", SchemaKindDB)
	s.Scalars["Identity.UUID"] = &ScalarDef{Name: "Identity.UUID", LanguagePrimitive: LanguageString}
	s.Enums["TicketState"] = &EnumDef{Name: "TicketState", Values: []EnumValueDef{
		{Name: "Open", SerializedAs: "open"}, {Name: "InProgress", SerializedAs: "in_progress"},
		{Name: "Resolved", SerializedAs: "resolved"}, {Name: "Closed", SerializedAs: "closed"},
	}}
	s.Enums["Review"] = &EnumDef{Name: "Review", Values: []EnumValueDef{
		{Name: "RETRY"}, {Name: "APPROVED"}, {Name: "REJECTED"},
	}}
	open := "open"
	s.Types["Ticket"] = &TypeDef{
		Name: "Ticket",
		Role: RoleDBTable,
		Fields: []*FieldDef{
			{Name: "id", TypeRef: TypeRef{Name: "Identity.UUID"}, Required: true, Key: true},
			{Name: "state", TypeRef: TypeRef{Name: "TicketState"}, Required: true, Default: &open},
			{Name: "review", TypeRef: TypeRef{Name: "Review"}},
		},
		StateMachines: []*StateMachine{
			{
				Field: "state",
				Transitions: []*StateTransition{
					{From: "open", To: []string{"in_progress", "resolved", "closed"}},
					{From: "in_progress", To: []string{"open", "resolved"}},
					{From: "resolved", To: []string{"open", "closed"}},
				},
				Terminal: []string{"closed"},
			},
			{
				Field:       "review",
				FromNull:    []string{"RETRY", "APPROVED", "REJECTED"},
				Transitions: []*StateTransition{{From: "RETRY", To: []string{"APPROVED", "REJECTED"}, ToNull: true}},
				Terminal:    []string{"APPROVED", "REJECTED"},
			},
		},
	}
	return s
}

// stateMachine and reviewMachine are the fixture's two machines.
func stateMachine(s *Schema) *StateMachine  { return s.Types["Ticket"].StateMachines[0] }
func reviewMachine(s *Schema) *StateMachine { return s.Types["Ticket"].StateMachines[1] }

// wantOneError holds errs to one error that contains each fragment.
func wantOneError(t *testing.T, errs []error, fragments ...string) {
	t.Helper()
	if len(errs) != 1 {
		t.Fatalf("Validate() = %v, want one error", errs)
	}
	for _, fragment := range fragments {
		if !strings.Contains(errs[0].Error(), fragment) {
			t.Fatalf("Validate() = %q, want it to contain %q", errs[0], fragment)
		}
	}
}

func TestStateMachineDeclarationsValidate(t *testing.T) {
	if errs := ticketSchema().Validate(); len(errs) != 0 {
		t.Fatalf("Validate() = %v, want no errors", errs)
	}
}

// A move a state makes to itself may be listed: it is no move, and a
// guard passes it either way.
func TestStateMachineTakesAListedMoveToTheSameState(t *testing.T) {
	s := ticketSchema()
	stateMachine(s).Transitions[0].To = []string{"open", "in_progress", "resolved", "closed"}
	if errs := s.Validate(); len(errs) != 0 {
		t.Fatalf("Validate() = %v, want no errors", errs)
	}
}

// A machine with no transition at all is one whose every state is final
// once a row holds it.
func TestStateMachineTakesNoTransitions(t *testing.T) {
	s := ticketSchema()
	review := reviewMachine(s)
	review.Transitions = nil
	review.Terminal = []string{"RETRY", "APPROVED", "REJECTED"}
	if errs := s.Validate(); len(errs) != 0 {
		t.Fatalf("Validate() = %v, want no errors", errs)
	}
}

func TestStateMachineRefusesTwoMachinesOnAField(t *testing.T) {
	s := ticketSchema()
	td := s.Types["Ticket"]
	td.StateMachines = append(td.StateMachines, &StateMachine{Field: "state", Terminal: []string{"closed"}})
	wantOneError(t, s.Validate(), "@stateMachine on Ticket.state: Ticket declares two state machines on state", "write its moves in one declaration")
}

func TestStateMachineRefusesAMachineWithNoField(t *testing.T) {
	s := ticketSchema()
	stateMachine(s).Field = ""
	wantOneError(t, s.Validate(), "@stateMachine 0 on Ticket names no field")

	s = ticketSchema()
	s.Types["Ticket"].StateMachines[1] = nil
	wantOneError(t, s.Validate(), "@stateMachine 1 on Ticket is null")
}

func TestStateMachineRefusesTwoEntriesForAState(t *testing.T) {
	s := ticketSchema()
	m := stateMachine(s)
	m.Transitions = append(m.Transitions, &StateTransition{From: "open", To: []string{"closed"}})
	wantOneError(t, s.Validate(), `@stateMachine on Ticket.state: transitions has two entries for "open"`, `write every move from "open" in one entry`)
}

func TestStateMachineRefusesAMoveListedTwice(t *testing.T) {
	s := ticketSchema()
	stateMachine(s).Transitions[2].To = []string{"open", "closed", "open"}
	wantOneError(t, s.Validate(), `@stateMachine on Ticket.state: the move from "resolved" to "open" is listed twice`)
}

func TestStateMachineRefusesAStateListedTwice(t *testing.T) {
	s := ticketSchema()
	stateMachine(s).Terminal = []string{"closed", "closed"}
	wantOneError(t, s.Validate(), `@stateMachine on Ticket.state: terminal lists "closed" twice`)

	s = ticketSchema()
	reviewMachine(s).FromNull = []string{"RETRY", "APPROVED", "REJECTED", "RETRY"}
	wantOneError(t, s.Validate(), `@stateMachine on Ticket.review: fromNull lists "RETRY" twice`)
}

func TestStateMachineRefusesATerminalStateWithATransition(t *testing.T) {
	s := ticketSchema()
	m := stateMachine(s)
	m.Transitions = append(m.Transitions, &StateTransition{From: "closed", To: []string{"open"}})
	wantOneError(t, s.Validate(), `@stateMachine on Ticket.state: "closed" is terminal and has a transition`, "take it out of terminal, or drop its transitions")
}

func TestStateMachineRefusesATransitionWithNoTarget(t *testing.T) {
	s := ticketSchema()
	stateMachine(s).Transitions[1].To = nil
	wantOneError(t, s.Validate(), `@stateMachine on Ticket.state: the transition from "in_progress" names no target`, "or drop the entry")
}

// A transition whose one target is null has a target.
func TestStateMachineTakesATransitionToNullAlone(t *testing.T) {
	s := ticketSchema()
	reviewMachine(s).Transitions[0].To = nil
	if errs := s.Validate(); len(errs) != 0 {
		t.Fatalf("Validate() = %v, want no errors", errs)
	}
}

func TestStateMachineRefusesAnEmptyFromNull(t *testing.T) {
	s := ticketSchema()
	review := reviewMachine(s)
	review.FromNull = []string{}
	review.Transitions[0].ToNull = false
	wantOneError(t, s.Validate(), "@stateMachine on Ticket.review: fromNull is empty, so a null field could take no state", "or leave fromNull out")
}

func TestStateMachineRefusesAReturnToNullWithNoFromNull(t *testing.T) {
	s := ticketSchema()
	reviewMachine(s).FromNull = nil
	wantOneError(t, s.Validate(), `@stateMachine on Ticket.review: "RETRY" returns the field to null, and the machine has no fromNull, so the row could not change again`)
}

func TestStateMachineRefusesAnEmptyState(t *testing.T) {
	s := ticketSchema()
	stateMachine(s).Transitions[0].From = ""
	wantOneError(t, s.Validate(), "@stateMachine on Ticket.state: transition 0 names no state to move from")

	s = ticketSchema()
	stateMachine(s).Transitions[0].To = []string{"resolved", ""}
	wantOneError(t, s.Validate(), `@stateMachine on Ticket.state: the transition from "open" names an empty state`)

	s = ticketSchema()
	stateMachine(s).Terminal = []string{""}
	wantOneError(t, s.Validate(), "@stateMachine on Ticket.state: terminal names an empty state")

	s = ticketSchema()
	stateMachine(s).Transitions[0] = nil
	wantOneError(t, s.Validate(), "@stateMachine on Ticket.state: transition 0 is null")
}

// OrderTransitions puts the entries in the order of the enum's members and
// leaves every list of states as written; an entry for a state that is no
// member follows the members'.
func TestStateMachineOrderTransitionsFollowsTheEnum(t *testing.T) {
	s := ticketSchema()
	m := stateMachine(s)
	m.Transitions = []*StateTransition{
		{From: "gone", To: []string{"open"}},
		{From: "resolved", To: []string{"open", "closed"}},
		{From: "in_progress", To: []string{"resolved", "open"}},
		{From: "open", To: []string{"resolved", "in_progress"}},
	}
	m.OrderTransitions(s.Enums["TicketState"])
	var got []string
	for _, tr := range m.Transitions {
		got = append(got, tr.From+":"+strings.Join(tr.To, ","))
	}
	want := []string{"open:resolved,in_progress", "in_progress:resolved,open", "resolved:open,closed", "gone:open"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("transitions = %v, want %v", got, want)
	}
	// No enum, no order to give.
	m.OrderTransitions(nil)
	if m.Transitions[0].From != "open" {
		t.Fatalf("transitions moved without an enum: %v", m.Transitions[0])
	}
}

func TestStateMachineOfFindsTheFieldsMachine(t *testing.T) {
	td := ticketSchema().Types["Ticket"]
	if m := td.StateMachineOf("review"); m == nil || m.Field != "review" {
		t.Fatalf("StateMachineOf(review) = %+v", m)
	}
	if m := td.StateMachineOf("id"); m != nil {
		t.Fatalf("StateMachineOf(id) = %+v, want none", m)
	}
}

func TestEnumMemberSerializedAs(t *testing.T) {
	s := ticketSchema()
	if m := s.Enums["TicketState"].MemberSerializedAs("in_progress"); m == nil || m.Name != "InProgress" {
		t.Fatalf("MemberSerializedAs(in_progress) = %+v", m)
	}
	if m := s.Enums["TicketState"].MemberSerializedAs("InProgress"); m != nil {
		t.Fatalf("MemberSerializedAs(InProgress) = %+v, want none: a state is the serialized value", m)
	}
	if m := s.Enums["Review"].MemberSerializedAs("RETRY"); m == nil || m.Serialized() != "RETRY" {
		t.Fatalf("MemberSerializedAs(RETRY) = %+v", m)
	}
}

// A machine is written under stateMachines, with the keys D117 names, in
// JSON and in YAML, and reads back as it was.
func TestStateMachineJSONAndYAMLRoundTrip(t *testing.T) {
	td := ticketSchema().Types["Ticket"]
	raw, err := json.Marshal(td.StateMachines[1])
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"field":"review","transitions":[{"from":"RETRY","to":["APPROVED","REJECTED"],"toNull":true}],"terminal":["APPROVED","REJECTED"],"fromNull":["RETRY","APPROVED","REJECTED"]}`
	if string(raw) != want {
		t.Fatalf("JSON = %s\nwant   %s", raw, want)
	}
	whole, err := json.Marshal(td)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(whole), `"stateMachines":[{"field":"state"`) {
		t.Fatalf("TypeDef JSON has no stateMachines: %s", whole)
	}
	var fromJSON TypeDef
	if err := json.Unmarshal(whole, &fromJSON); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromJSON.StateMachines, td.StateMachines) {
		t.Fatalf("JSON round trip = %+v", fromJSON.StateMachines)
	}
	text, err := yaml.Marshal(td)
	if err != nil {
		t.Fatal(err)
	}
	var fromYAML TypeDef
	if err := yaml.Unmarshal(text, &fromYAML); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromYAML.StateMachines, td.StateMachines) {
		t.Fatalf("YAML round trip = %+v\n%s", fromYAML.StateMachines, text)
	}
}
