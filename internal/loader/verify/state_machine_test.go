package verify

import (
	"encoding/json"
	"slices"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// machineSchema is a DB schema with a Ticket whose state, a required field
// with a default, moves among four states; whose review, a nullable field,
// starts at null and may return to it; and whose tier is typed by an enum
// a dependency owns, which the reader hands over (D118).
func machineSchema() (*ir.Schema, Input) {
	schema := ir.NewSchema("support-db", ir.SchemaKindDB)
	schema.Enums["TicketState"] = &ir.EnumDef{Name: "TicketState", Values: []ir.EnumValueDef{
		{Name: "Open", SerializedAs: "open"}, {Name: "InProgress", SerializedAs: "in_progress"},
		{Name: "Resolved", SerializedAs: "resolved"}, {Name: "Closed", SerializedAs: "closed"},
	}}
	schema.Enums["Review"] = &ir.EnumDef{Name: "Review", Values: []ir.EnumValueDef{
		{Name: "RETRY"}, {Name: "APPROVED"}, {Name: "REJECTED"},
	}}
	schema.Imports = []ir.Import{{Package: "@schemas/plans", Types: []string{"Tier", "Plan"}}}
	open, free := "open", "free"
	schema.Types["Ticket"] = &ir.TypeDef{
		Name: "Ticket",
		Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Key: true},
			{Name: "state", TypeRef: ir.TypeRef{Name: "TicketState"}, Required: true, Default: &open},
			{Name: "review", TypeRef: ir.TypeRef{Name: "Review"}},
			{Name: "tier", TypeRef: ir.TypeRef{Name: "Tier"}, Required: true, Default: &free},
			{Name: "title", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
			{Name: "plan", TypeRef: ir.TypeRef{Name: "Plan"}},
		},
		StateMachines: []*ir.StateMachine{
			{
				Field: "state",
				Transitions: []*ir.StateTransition{
					{From: "open", To: []string{"in_progress", "resolved", "closed"}},
					{From: "in_progress", To: []string{"open", "resolved"}},
					{From: "resolved", To: []string{"open", "closed"}},
				},
				Terminal: []string{"closed"},
			},
			{
				Field:       "review",
				FromNull:    []string{"RETRY", "APPROVED", "REJECTED"},
				Transitions: []*ir.StateTransition{{From: "RETRY", To: []string{"APPROVED", "REJECTED"}, ToNull: true}},
				Terminal:    []string{"APPROVED", "REJECTED"},
			},
			{
				Field:       "tier",
				Transitions: []*ir.StateTransition{{From: "free", To: []string{"paid"}}},
				Terminal:    []string{"paid"},
			},
		},
	}
	in := Input{ExternalEnums: map[string]*ir.EnumDef{
		"Tier": {Name: "Tier", Owner: "@schemas/plans", Values: []ir.EnumValueDef{{Name: "Free", SerializedAs: "free"}, {Name: "Paid", SerializedAs: "paid"}}},
	}}
	return schema, in
}

// machineErrors runs the state machines' checks alone and returns what
// they refuse.
func machineErrors(schema *ir.Schema, in Input) []string {
	r := &Result{}
	checkStateMachines(schema, in, r)
	var got []string
	for _, d := range r.Errors {
		got = append(got, d.Msg)
	}
	return got
}

// wantMachineErrors holds the checks of a schema to the errors listed.
func wantMachineErrors(t *testing.T, schema *ir.Schema, in Input, want ...string) {
	t.Helper()
	if got := machineErrors(schema, in); !slices.Equal(got, want) {
		t.Errorf("errors =\n%q\nwant\n%q", got, want)
	}
}

// Shorthands for the fixture's parts.
func ticketOf(schema *ir.Schema) *ir.TypeDef { return schema.Types["Ticket"] }
func machineOf(schema *ir.Schema, field string) *ir.StateMachine {
	return ticketOf(schema).StateMachineOf(field)
}

// TestStateMachinesVerify: a machine on a field with a default, one on a
// nullable field and one on a dependency's enum pass, in a DB service and
// in every other kind, on a table and on a class that is none.
func TestStateMachinesVerify(t *testing.T) {
	schema, in := machineSchema()
	wantMachineErrors(t, schema, in)

	for _, kind := range []ir.SchemaKind{ir.SchemaKindAPI, ir.SchemaKindGeneral} {
		schema, in = machineSchema()
		schema.Kind = kind
		ticketOf(schema).Role = ir.RoleEmbeddedStruct
		wantMachineErrors(t, schema, in)
	}

	// A JsonField type of a DB service is no table: it keeps its machine.
	schema, in = machineSchema()
	ticketOf(schema).JsonField = true
	wantMachineErrors(t, schema, in)
}

func TestStateMachineRefusesAFieldTheTypeLacks(t *testing.T) {
	schema, in := machineSchema()
	machineOf(schema, "state").Field = "status"
	wantMachineErrors(t, schema, in, "@stateMachine on Ticket.status: Ticket has no field status; name the enum field whose moves the machine declares")
}

func TestStateMachineRefusesAFieldNoEnumTypes(t *testing.T) {
	schema, in := machineSchema()
	machineOf(schema, "state").Field = "title"
	wantMachineErrors(t, schema, in, "@stateMachine on Ticket.title: title is typed by string, which is no enum; a machine governs a field an enum types, as E, Nullable<E> or Default<E, V>")
}

func TestStateMachineRefusesAListOrAMapOfAnEnum(t *testing.T) {
	schema, in := machineSchema()
	fieldNamed(ticketOf(schema), "state").TypeRef.IsArray = true
	wantMachineErrors(t, schema, in, "@stateMachine on Ticket.state: state is a list of TicketState, and a machine governs a field that holds one state; type it by the enum itself, as E, Nullable<E> or Default<E, V>")

	schema, in = machineSchema()
	fieldNamed(ticketOf(schema), "state").TypeRef.IsMap = true
	wantMachineErrors(t, schema, in, "@stateMachine on Ticket.state: state is a map of TicketState, and a machine governs a field that holds one state; type it by the enum itself, as E, Nullable<E> or Default<E, V>")
}

// A dependency's enum is read from Input.ExternalEnums, which the
// TypeScript reader fills. A JSON or YAML schema file names the type and
// resolves nothing, so its machine is refused, as is one on a dependency's
// class.
func TestStateMachineRefusesADependencysTypeTheBuildCannotRead(t *testing.T) {
	schema, _ := machineSchema()
	wantMachineErrors(t, schema, Input{}, "@stateMachine on Ticket.tier: tier is typed by Tier, which support-db imports and the build does not read as an enum; a machine governs a field an enum types, and a dependency's enum is read from a TypeScript schema alone, so in a JSON or YAML schema file type the field by an enum of support-db")

	schema, in := machineSchema()
	machineOf(schema, "tier").Field = "plan"
	wantMachineErrors(t, schema, in, "@stateMachine on Ticket.plan: plan is typed by Plan, which support-db imports and the build does not read as an enum; a machine governs a field an enum types, and a dependency's enum is read from a TypeScript schema alone, so in a JSON or YAML schema file type the field by an enum of support-db")
}

// A state is the value a member serializes to: the member's name is not
// one when the two differ. Each stray state is reported once.
func TestStateMachineRefusesAStateThatIsNoMember(t *testing.T) {
	const members = `a state is a member's value, one of "open", "in_progress", "resolved", "closed"`
	schema, in := machineSchema()
	machineOf(schema, "state").Terminal = []string{"closed", "Closed"}
	wantMachineErrors(t, schema, in, `@stateMachine on Ticket.state: terminal names "Closed", which is no member of TicketState; `+members)

	schema, in = machineSchema()
	state := machineOf(schema, "state")
	state.Transitions[0].To = []string{"in_progress", "resolved", "closed", "archived"}
	state.Transitions = append(state.Transitions, &ir.StateTransition{From: "archived", To: []string{"open"}})
	wantMachineErrors(t, schema, in, `@stateMachine on Ticket.state: the transition from "open" names "archived", which is no member of TicketState; `+members)

	schema, in = machineSchema()
	machineOf(schema, "state").Transitions = append(machineOf(schema, "state").Transitions, &ir.StateTransition{From: "held", To: []string{"open"}})
	wantMachineErrors(t, schema, in, `@stateMachine on Ticket.state: transitions names "held", which is no member of TicketState; `+members)

	schema, in = machineSchema()
	machineOf(schema, "review").FromNull = []string{"RETRY", "APPROVED", "REJECTED", "Pending"}
	wantMachineErrors(t, schema, in, `@stateMachine on Ticket.review: fromNull names "Pending", which is no member of Review; a state is a member's value, one of "RETRY", "APPROVED", "REJECTED"`)

	// A dependency's enum is held to its members too.
	schema, in = machineSchema()
	machineOf(schema, "tier").Terminal = []string{"paid", "Paid"}
	wantMachineErrors(t, schema, in, `@stateMachine on Ticket.tier: terminal names "Paid", which is no member of Tier; a state is a member's value, one of "free", "paid"`)
}

func TestStateMachineRefusesANullStartOnARequiredField(t *testing.T) {
	schema, in := machineSchema()
	fieldNamed(ticketOf(schema), "review").Required = true
	wantMachineErrors(t, schema, in,
		"@stateMachine on Ticket.review: fromNull lists the states a null takes, and review is required, so it is never null; type it Nullable<Review>, or leave fromNull out",
		`@stateMachine on Ticket.review: "RETRY" returns the field to null, and review is required; type it Nullable<Review>, or take null out of the state's transitions`,
	)
}

func TestStateMachineRefusesAReturnToNullOnARequiredField(t *testing.T) {
	schema, in := machineSchema()
	machineOf(schema, "state").Transitions[2].ToNull = true
	wantMachineErrors(t, schema, in, `@stateMachine on Ticket.state: "resolved" returns the field to null, and state is required; type it Nullable<TicketState>, or take null out of the state's transitions`)
}

func TestStateMachineRefusesAStateNoMoveReachesFromTheDefault(t *testing.T) {
	schema, in := machineSchema()
	state := machineOf(schema, "state")
	state.Transitions = []*ir.StateTransition{{From: "open", To: []string{"closed"}}, {From: "in_progress", To: []string{"resolved"}}}
	wantMachineErrors(t, schema, in,
		`@stateMachine on Ticket.state: no move reaches "in_progress" from state's default, "open", so no update could put a row there; list a move to it, or take the member out of TicketState`,
		`@stateMachine on Ticket.state: no move reaches "resolved" from state's default, "open", so no update could put a row there; list a move to it, or take the member out of TicketState`,
	)
}

func TestStateMachineRefusesAStateNoMoveReachesFromNull(t *testing.T) {
	schema, in := machineSchema()
	review := machineOf(schema, "review")
	review.FromNull = []string{"RETRY"}
	review.Transitions = []*ir.StateTransition{{From: "RETRY", To: []string{"APPROVED"}}}
	wantMachineErrors(t, schema, in, `@stateMachine on Ticket.review: no move reaches "REJECTED" from null, which fromNull leaves, so no update could put a row there; list a move to it, or take the member out of Review`)
}

// A state only a null start gives is reached from a default through a
// return to null.
func TestStateMachineReachesAStateThroughAReturnToNull(t *testing.T) {
	schema, in := machineSchema()
	retry := "RETRY"
	fieldNamed(ticketOf(schema), "review").Default = &retry
	review := machineOf(schema, "review")
	review.FromNull = []string{"APPROVED", "REJECTED"}
	review.Transitions = []*ir.StateTransition{{From: "RETRY", ToNull: true}}
	wantMachineErrors(t, schema, in)

	// Without the return, the default reaches neither.
	review.Transitions = nil
	review.Terminal = nil
	wantMachineErrors(t, schema, in,
		`@stateMachine on Ticket.review: no move reaches "APPROVED" from review's default, "RETRY", so no update could put a row there; list a move to it, or take the member out of Review`,
		`@stateMachine on Ticket.review: no move reaches "REJECTED" from review's default, "RETRY", so no update could put a row there; list a move to it, or take the member out of Review`,
	)
}

// A field with no default and no null start is not walked: its rows are
// born in whatever state their writer gives them.
func TestStateMachineWithNoStartIsNotWalked(t *testing.T) {
	schema, in := machineSchema()
	fieldNamed(ticketOf(schema), "state").Default = nil
	machineOf(schema, "state").Transitions = []*ir.StateTransition{{From: "resolved", To: []string{"closed"}}}
	wantMachineErrors(t, schema, in)
}

// A DB service's engine type has no table for a guard to watch.
func TestStateMachineRefusesAnEngineType(t *testing.T) {
	schema, in := machineSchema()
	ticket := ticketOf(schema)
	ticket.Behaviors = []ir.BehaviorRef{{Name: "Workflow"}}
	ticket.StateMachines = ticket.StateMachines[:1]
	wantMachineErrors(t, schema, in, "@stateMachine on Ticket.state: Ticket composes a behavior with @behavior, so the engine keeps its instances in tables of its own, which no guard watches; declare the machine on a table")

	// A table that implements a behavior stays a table, and keeps its
	// machine.
	ticket.Behaviors = []ir.BehaviorRef{{Name: "Timers", Implements: true}}
	wantMachineErrors(t, schema, in)
}

func TestStateMachineRefusesAQueueClass(t *testing.T) {
	schema, in := machineSchema()
	ticket := ticketOf(schema)
	ticket.Role, ticket.Queue = ir.RoleEmbeddedStruct, &ir.QueueDef{}
	ticket.StateMachines = ticket.StateMachines[:1]
	wantMachineErrors(t, schema, in, "@stateMachine on Ticket.state: Ticket is a @queue class, whose rows are messages the queue's workers move; declare the machine on a table")
}

// A field a behavior owns moves as the behavior says: as the loader adds
// it to the table, and as the use alone names it when the behaviors'
// checks failed and it was not added.
func TestStateMachineRefusesAFieldABehaviorOwns(t *testing.T) {
	const want = "@stateMachine on Ticket.status: status is a field the Workflow behavior owns, so its moves are the behavior's; declare them in its config, and take this declaration off"
	config := json.RawMessage(`{"states":["open","closed"],"transitions":[{"from":"open","to":"closed"}]}`)

	schema, in := machineSchema()
	ticket := ticketOf(schema)
	ticket.Behaviors = []ir.BehaviorRef{{Name: "Workflow", Field: "status", Config: config}}
	ticket.Fields = append(ticket.Fields, &ir.FieldDef{Name: "status", TypeRef: ir.TypeRef{Name: "string"}, Required: true,
		Behavior: &ir.FieldBehavior{Use: "status", Behavior: "Workflow", Field: "state", Value: true}})
	ticket.StateMachines = []*ir.StateMachine{{Field: "status", Transitions: []*ir.StateTransition{{From: "open", To: []string{"closed"}}}, Terminal: []string{"closed"}}}
	wantMachineErrors(t, schema, in, want)

	schema, in = machineSchema()
	ticket = ticketOf(schema)
	ticket.Behaviors = []ir.BehaviorRef{{Name: "Workflow", Field: "status", Config: config}}
	ticket.StateMachines = []*ir.StateMachine{{Field: "status", Terminal: []string{"closed"}}}
	wantMachineErrors(t, schema, in, want)
}

// TestRunChecksStateMachines: the pass runs the state machines' checks.
func TestRunChecksStateMachines(t *testing.T) {
	schema, in := machineSchema()
	machineOf(schema, "state").Field = "status"
	res := Run(schema, in)
	const want = "@stateMachine on Ticket.status: Ticket has no field status; name the enum field whose moves the machine declares"
	if !slices.ContainsFunc(res.Errors, func(d *Diagnostic) bool { return d.Msg == want }) {
		t.Errorf("Run did not report %q; errors = %v", want, res.Err())
	}
}
