package tsgen

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// The state machine services (D118): the DB's Ticket has a machine on a
// required field that lists a move to the same state, two on nullable
// fields, one of which only returns to null from a state, and one on a
// field typed by an enum of the enums service; its SignOff is a
// @jsonField class with a machine.
const (
	stateMachineService      = "fixture-state-machine-db"
	stateMachineEnumsService = "fixture-state-machine-enums"
)

// stateMachineSchemas loads the state machine service and the service
// that owns one of its enums.
func stateMachineSchemas(t *testing.T) (schema, enums *ir.Schema) {
	t.Helper()
	var err error
	if enums, err = loader.LoadService(filepath.Join(fixturesDir, stateMachineEnumsService)); err != nil {
		t.Fatalf("load %s: %v", stateMachineEnumsService, err)
	}
	if schema, err = loader.LoadService(filepath.Join(fixturesDir, stateMachineService)); err != nil {
		t.Fatalf("load %s: %v", stateMachineService, err)
	}
	return schema, enums
}

// stateMachinePackages are the state machine services' packages, the
// dependency first, with a file tsc checks the helpers' types by: a
// nullable machine takes null and a required one refuses it, a state is a
// member of the field's enum and no string, and the dependency's enum
// keys its machine's transitions.
func stateMachinePackages(t *testing.T) []tsPackageCase {
	t.Helper()
	schema, enums := stateMachineSchemas(t)
	const check = `import {
  AccountStatus,
  ReviewOutcome,
  TicketState,
  ticketAccountStatusTransitions,
  ticketCanTransitionReview,
  ticketCanTransitionState,
  ticketReviewTerminal,
} from './index';
import { signOffCanTransitionStep } from './state-machines';
import { SignOffStep } from './types';

export const fromNull: boolean = ticketCanTransitionReview(null, ReviewOutcome.RETRY);
export const toNull: boolean = ticketCanTransitionReview(ReviewOutcome.RETRY, null);
export const terminal: readonly ReviewOutcome[] = ticketReviewTerminal;
export const fromActive: readonly AccountStatus[] = ticketAccountStatusTransitions[AccountStatus.Active];
export const signedOff: boolean = signOffCanTransitionStep(SignOffStep.Requested, SignOffStep.Granted);
// @ts-expect-error Ticket.state is required, so null is no state of it.
export const required: boolean = ticketCanTransitionState(null, TicketState.Open);
// @ts-expect-error A state is a member of the field's enum, not its text.
export const text: boolean = ticketCanTransitionState('open', TicketState.Open);
// @ts-expect-error The terminal states are read, not written.
ticketReviewTerminal.push(ReviewOutcome.RETRY);
`
	return []tsPackageCase{
		{name: stateMachineEnumsService, schema: enums},
		{
			name:   stateMachineService,
			schema: schema,
			deps:   map[string]*ir.Schema{stateMachineEnumsService: enums},
			extra:  map[string]string{"state-machines.check.ts": check},
		},
	}
}

// stateMachineShapes is a schema of the machines the fixture has none of,
// each a shape of its own in the helper: Switch.latch, whose enum has one
// member, which moves only to itself; Door.lock, nullable with no
// fromNull, so a null takes no state; and Post.phase, which returns to
// null or moves to one state.
func stateMachineShapes() *ir.Schema {
	schema := ir.NewSchema("state-machine-shapes", ir.SchemaKindGeneral)
	enum := func(name string, values ...string) {
		def := &ir.EnumDef{Name: name}
		for _, value := range values {
			def.Values = append(def.Values, ir.EnumValueDef{Name: strings.ToUpper(value), SerializedAs: value})
		}
		schema.Enums[name] = def
	}
	enum("Latch", "set")
	enum("Lock", "locked", "open")
	enum("Phase", "draft", "live")
	machine := func(typeName, field, enumName string, required bool, m *ir.StateMachine) {
		m.Field = field
		schema.Types[typeName] = &ir.TypeDef{
			Name: typeName, Role: ir.RoleEmbeddedStruct,
			Fields:        []*ir.FieldDef{{Name: field, TypeRef: ir.TypeRef{Name: enumName}, Required: required}},
			StateMachines: []*ir.StateMachine{m},
		}
	}
	machine("Switch", "latch", "Latch", true, &ir.StateMachine{
		Transitions: []*ir.StateTransition{{From: "set", To: []string{"set"}}},
	})
	machine("Door", "lock", "Lock", false, &ir.StateMachine{
		Transitions: []*ir.StateTransition{{From: "locked", To: []string{"open"}}},
	})
	machine("Post", "phase", "Phase", false, &ir.StateMachine{
		Transitions: []*ir.StateTransition{{From: "draft", To: []string{"live"}, ToNull: true}},
		Terminal:    []string{"live"},
		FromNull:    []string{"draft"},
	})
	return schema
}

// machineCase is one machine of a schema as a test asks its helpers: the
// IR's machine, its type, and the states of its field, each a member's
// serialized value, with nil for null when the field is nullable.
type machineCase struct {
	typeName string
	machine  *ir.StateMachine
	states   []*string
}

// machineCases returns every machine of schema, by type name and then in
// the order declared, with its field's enum read from schema or, when a
// dependency owns it, from deps.
func machineCases(t *testing.T, schema *ir.Schema, deps ...*ir.Schema) []machineCase {
	t.Helper()
	names := make([]string, 0, len(schema.Types))
	for name := range schema.Types {
		names = append(names, name)
	}
	sort.Strings(names)
	var cases []machineCase
	for _, name := range names {
		typeDef := schema.Types[name]
		for _, machine := range typeDef.StateMachines {
			var field *ir.FieldDef
			for _, f := range typeDef.Fields {
				if f.Name == machine.Field {
					field = f
				}
			}
			if field == nil {
				t.Fatalf("%s has no field %s", name, machine.Field)
			}
			enum := schema.Enums[field.TypeRef.Name]
			for _, dep := range deps {
				if enum == nil {
					enum = dep.Enums[field.TypeRef.Name]
				}
			}
			if enum == nil {
				t.Fatalf("%s.%s: no enum %s", name, machine.Field, field.TypeRef.Name)
			}
			mc := machineCase{typeName: name, machine: machine}
			if !field.Required {
				mc.states = append(mc.states, nil)
			}
			for i := range enum.Values {
				state := enum.Values[i].Serialized()
				mc.states = append(mc.states, &state)
			}
			cases = append(cases, mc)
		}
	}
	return cases
}

// mayMove is D118's rule for a move, read from the IR: an unchanged value
// is no move and passes; no move leaves a terminal state; a null takes a
// fromNull state; and any other move is one the machine lists, to null
// where the transition has toNull.
func mayMove(machine *ir.StateMachine, from, to *string) bool {
	switch {
	case from == nil && to == nil:
		return true
	case from == nil:
		return slices.Contains(machine.FromNull, *to)
	case to != nil && *from == *to:
		return true
	case slices.Contains(machine.Terminal, *from):
		return false
	}
	for _, tr := range machine.Transitions {
		if tr.From != *from {
			continue
		}
		if to == nil {
			return tr.ToNull
		}
		return slices.Contains(tr.To, *to)
	}
	return false
}

func stateName(state *string) string {
	if state == nil {
		return "null"
	}
	return *state
}

// TestStateMachinesFileIsWrittenForAMachine: a schema with a machine gets
// state-machines.ts, which index.ts re-exports and package.json exports
// as "./state-machines"; a schema without one gets none of the three.
func TestStateMachinesFileIsWrittenForAMachine(t *testing.T) {
	schema, enums := stateMachineSchemas(t)
	clock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	write := func(schema *ir.Schema, opts Options) (string, *ModuleOutput) {
		t.Helper()
		opts.Clock = clock
		output, err := Generate(schema, opts)
		if err != nil {
			t.Fatalf("generate %s: %v", opts.SchemaName, err)
		}
		dir := t.TempDir()
		if err := WriteTypes(output, dir); err != nil {
			t.Fatalf("write %s: %v", opts.SchemaName, err)
		}
		return dir, output
	}
	read := func(dir, name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	exports := func(dir string) map[string]map[string]string {
		t.Helper()
		var manifest struct {
			Exports map[string]map[string]string `json:"exports"`
		}
		if err := json.Unmarshal([]byte(read(dir, "package.json")), &manifest); err != nil {
			t.Fatalf("package.json: %v", err)
		}
		return manifest.Exports
	}

	dir, output := write(schema, Options{
		SchemaName:   stateMachineService,
		Dependencies: map[string]*ir.Schema{stateMachineEnumsService: enums},
	})
	var names []string
	for _, machine := range output.StateMachines {
		names = append(names, machine.TransitionsConst, machine.TerminalConst, machine.CanTransitionFunc)
	}
	want := []string{
		"signOffStepTransitions", "signOffStepTerminal", "signOffCanTransitionStep",
		"ticketStateTransitions", "ticketStateTerminal", "ticketCanTransitionState",
		"ticketReviewTransitions", "ticketReviewTerminal", "ticketCanTransitionReview",
		"ticketEscalationTransitions", "ticketEscalationTerminal", "ticketCanTransitionEscalation",
		"ticketAccountStatusTransitions", "ticketAccountStatusTerminal", "ticketCanTransitionAccountStatus",
	}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("the machines' exports are\n%v\nwant\n%v", names, want)
	}
	source := read(dir, StateMachinesFile)
	for _, name := range want {
		if !strings.Contains(source, "export const "+name+":") && !strings.Contains(source, "export function "+name+"(") {
			t.Errorf("%s does not export %s:\n%s", StateMachinesFile, name, source)
		}
	}
	// A state is the enum's member, the dependency's enum among them, and
	// never its text.
	for _, state := range []string{"TicketState.InProgress", "ReviewOutcome.RETRY", "AccountStatus.Suspended"} {
		if !strings.Contains(source, state) {
			t.Errorf("%s does not name %s:\n%s", StateMachinesFile, state, source)
		}
	}
	if strings.Contains(source, `"`) || strings.Contains(source, " as ") {
		t.Errorf("%s writes a state as text or casts one:\n%s", StateMachinesFile, source)
	}
	const reexport = "\n\n// Re-export the state machines\nexport * from './state-machines';\n"
	if got := read(dir, "index.ts"); !strings.HasSuffix(got, reexport) {
		t.Errorf("index.ts does not re-export the state machines:\n%s", got)
	}
	if got := exports(dir)["./state-machines"]; got["types"] != "./state-machines.ts" || got["import"] != "./state-machines.ts" {
		t.Errorf(`package.json exports "./state-machines" as %v, want ./state-machines.ts`, got)
	}

	dir, output = write(enums, Options{SchemaName: stateMachineEnumsService})
	if len(output.StateMachines) != 0 {
		t.Fatalf("%s has machines: %+v", stateMachineEnumsService, output.StateMachines)
	}
	if _, err := os.Stat(filepath.Join(dir, StateMachinesFile)); !os.IsNotExist(err) {
		t.Errorf("a schema without a machine has %s: %v", StateMachinesFile, err)
	}
	if got := read(dir, "index.ts"); strings.Contains(got, "state-machines") {
		t.Errorf("index.ts of a schema without a machine names state-machines:\n%s", got)
	}
	if _, ok := exports(dir)["./state-machines"]; ok {
		t.Errorf(`package.json of a schema without a machine exports "./state-machines"`)
	}
}

// TestStateMachinesRefuseWhatTheyCannotName: a machine whose field no enum
// of the schema or of a dependency it imports types, and one with a state
// that is no member, fail the generation with the type and the field.
// The build's checks refuse both first (D118); the generator does not
// write a helper it cannot name the states of.
func TestStateMachinesRefuseWhatTheyCannotName(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(schema *ir.Schema)
		want   string
	}{
		{
			name:   "a dependency that was not imported",
			change: func(schema *ir.Schema) { schema.Imports = nil },
			want:   "tsgen: @stateMachine on Ticket.accountStatus: AccountStatus is no enum of fixture-state-machine-db or of a dependency it imports",
		},
		{
			name: "a state that is no member",
			change: func(schema *ir.Schema) {
				machine := schema.Types["Ticket"].StateMachineOf("state")
				machine.Terminal = append(machine.Terminal, "archived")
			},
			want: `tsgen: @stateMachine on Ticket.state: "archived" is no member of TicketState`,
		},
		{
			name: "a field the type lacks",
			change: func(schema *ir.Schema) {
				schema.Types["SignOff"].StateMachineOf("step").Field = "stage"
			},
			want: "tsgen: @stateMachine on SignOff.stage: SignOff has no field stage",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema, enums := stateMachineSchemas(t)
			tc.change(schema)
			_, err := Generate(schema, Options{
				SchemaName:   stateMachineService,
				Dependencies: map[string]*ir.Schema{stateMachineEnumsService: enums},
			})
			if err == nil || err.Error() != tc.want {
				t.Fatalf("Generate = %v, want %s", err, tc.want)
			}
		})
	}
}

// stateMachinesDriver asks each machine's helper every pair of its states
// and prints the answers beside the machine's transitions and terminal
// states, from state-machines.ts, after checking index.ts exports the
// same three. STATE_MACHINES names the exports and lists the states.
const stateMachinesDriver = `import * as fromFile from './state-machines.ts';
import * as fromRoot from './index.ts';

type Machine = { transitions: string; terminal: string; canTransition: string; states: (string | null)[] };
type Helper = (from: string | null, to: string | null) => unknown;

const file = fromFile as Record<string, unknown>;
const root = fromRoot as Record<string, unknown>;
const machines: Machine[] = JSON.parse(process.env.STATE_MACHINES ?? '[]');
const answers = machines.map((machine) => {
  for (const name of [machine.transitions, machine.terminal, machine.canTransition]) {
    if (file[name] === undefined || root[name] !== file[name]) {
      throw new Error('index.ts does not export the ' + name + ' of state-machines.ts');
    }
  }
  const canTransition = file[machine.canTransition] as Helper;
  return {
    transitions: file[machine.transitions],
    terminal: file[machine.terminal],
    moves: machine.states.map((from) => machine.states.map((to) => canTransition(from, to))),
    // A value that is no member of the enum moves nowhere, and stays.
    stranger: [canTransition('no such state', machine.states[machine.states.length - 1]), canTransition('no such state', 'no such state')],
  };
});
console.log(JSON.stringify(answers));
`

// TestStateMachineHelpers type-checks the state machine services' packages,
// and a package of the shapes they have none of, and runs every machine's
// helper under bun on every pair of its field's states, null among them
// where the field is nullable: each answer is D118's rule read from the
// IR, and the transitions and terminal states are the IR's, as the enum's
// values. Skips when bun is unavailable.
func TestStateMachineHelpers(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping generated state machine check in -short mode")
	}
	packages := stateMachinePackages(t)
	schema, enums := packages[1].schema, packages[0].schema
	shapes := stateMachineShapes()
	cases := machineCases(t, schema, enums)
	if len(cases) != 5 {
		t.Fatalf("%s has %d machines, want 5", stateMachineService, len(cases))
	}
	// The rule the answers are held to, held to the fixture's own words.
	state := func(s string) *string { return &s }
	for _, known := range []struct {
		typeName, field string
		from, to        *string
		want            bool
	}{
		{"Ticket", "state", state("open"), state("open"), true},
		{"Ticket", "state", state("closed"), state("closed"), true},
		{"Ticket", "state", state("open"), state("closed"), true},
		{"Ticket", "state", state("in_progress"), state("closed"), false},
		{"Ticket", "state", state("closed"), state("open"), false},
		{"Ticket", "review", nil, nil, true},
		{"Ticket", "review", nil, state("APPROVED"), true},
		{"Ticket", "review", state("RETRY"), nil, true},
		{"Ticket", "review", state("APPROVED"), nil, false},
		{"Ticket", "escalation", nil, state("acknowledged"), false},
		{"Ticket", "escalation", state("acknowledged"), nil, true},
		{"Ticket", "escalation", state("acknowledged"), state("raised"), false},
		{"Ticket", "accountStatus", state("suspended"), state("active"), true},
		{"Ticket", "accountStatus", state("closed"), state("active"), false},
		{"SignOff", "step", state("requested"), state("refused"), true},
		{"SignOff", "step", state("granted"), state("refused"), false},
	} {
		machine := schema.Types[known.typeName].StateMachineOf(known.field)
		if got := mayMove(machine, known.from, known.to); got != known.want {
			t.Fatalf("the rule lets %s.%s move from %s to %s: %v, want %v", known.typeName, known.field, stateName(known.from), stateName(known.to), got, known.want)
		}
	}

	typesRoot, bunPath := buildTSPackages(t, append(packages, tsPackageCase{name: shapes.Name, schema: shapes}))
	if t.Failed() {
		return
	}
	// SignOff.step and Ticket.state, of 3 and 4 members; Ticket.review and
	// Ticket.escalation, of 3 and 2 with null; Ticket.accountStatus, of 3.
	if got, want := askStateMachines(t, bunPath, filepath.Join(typesRoot, stateMachineService), cases), 3*3+4*4+4*4+3*3+3*3; got != want {
		t.Errorf("%s: asked %d pairs of states, want %d", stateMachineService, got, want)
	}
	// Door.lock and Post.phase, of 2 members with null; Switch.latch, of 1.
	if got, want := askStateMachines(t, bunPath, filepath.Join(typesRoot, shapes.Name), machineCases(t, shapes)), 3*3+3*3+1; got != want {
		t.Errorf("%s: asked %d pairs of states, want %d", shapes.Name, got, want)
	}
}

// askStateMachines runs stateMachinesDriver in the package at pkgDir for
// cases, its schema's machines, and holds every answer to the IR: each
// move to mayMove, and the transitions and terminal states to the
// machine's, as the enum's values. It returns the number of pairs of
// states it asked.
func askStateMachines(t *testing.T, bunPath, pkgDir string, cases []machineCase) int {
	t.Helper()
	type asked struct {
		Transitions   string    `json:"transitions"`
		Terminal      string    `json:"terminal"`
		CanTransition string    `json:"canTransition"`
		States        []*string `json:"states"`
	}
	var machines []asked
	for _, mc := range cases {
		prefix, suffix := lowerFirst(mc.typeName), codegen.ToPascalCase(mc.machine.Field)
		machines = append(machines, asked{
			Transitions:   prefix + suffix + "Transitions",
			Terminal:      prefix + suffix + "Terminal",
			CanTransition: prefix + "CanTransition" + suffix,
			States:        mc.states,
		})
	}
	request, err := json.Marshal(machines)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "state_machines_driver.ts"), []byte(stateMachinesDriver), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bunPath, "run", "state_machines_driver.ts")
	cmd.Dir = pkgDir
	cmd.Env = append(os.Environ(), "STATE_MACHINES="+string(request))
	out, err := cmd.Output()
	if err != nil {
		var stderr []byte
		if exit, ok := err.(*exec.ExitError); ok {
			stderr = exit.Stderr
		}
		t.Fatalf("driver failed in %s: %v\n%s%s", pkgDir, err, out, stderr)
	}
	var answers []struct {
		Transitions map[string][]*string `json:"transitions"`
		Terminal    []string             `json:"terminal"`
		Moves       [][]any              `json:"moves"`
		Stranger    []any                `json:"stranger"`
	}
	if err := json.Unmarshal(out, &answers); err != nil {
		t.Fatalf("decode the driver's answers: %v\n%s", err, out)
	}
	if len(answers) != len(cases) {
		t.Fatalf("the driver answered for %d machines, want %d:\n%s", len(answers), len(cases), out)
	}

	pairs := 0
	for i, mc := range cases {
		name := mc.typeName + "." + mc.machine.Field
		answer := answers[i]

		wantTransitions := map[string][]*string{}
		for _, s := range mc.states {
			if s != nil {
				wantTransitions[*s] = []*string{}
			}
		}
		for _, tr := range mc.machine.Transitions {
			to := []*string{}
			for _, s := range tr.To {
				to = append(to, &s)
			}
			if tr.ToNull {
				to = append(to, nil)
			}
			wantTransitions[tr.From] = to
		}
		if !reflect.DeepEqual(answer.Transitions, wantTransitions) {
			got, _ := json.Marshal(answer.Transitions)
			want, _ := json.Marshal(wantTransitions)
			t.Errorf("%s: the transitions are\n%s\nwant\n%s", name, got, want)
		}
		if !slices.Equal(answer.Terminal, mc.machine.Terminal) {
			t.Errorf("%s: the terminal states are %v, want %v", name, answer.Terminal, mc.machine.Terminal)
		}

		if len(answer.Moves) != len(mc.states) {
			t.Fatalf("%s: the driver answered from %d states, want %d", name, len(answer.Moves), len(mc.states))
		}
		for f, from := range mc.states {
			if len(answer.Moves[f]) != len(mc.states) {
				t.Fatalf("%s: the driver answered %d moves from %s, want %d", name, len(answer.Moves[f]), stateName(from), len(mc.states))
			}
			for g, to := range mc.states {
				pairs++
				// The answer is a boolean, never undefined: any holds what
				// the helper returned.
				if got, want := answer.Moves[f][g], any(mayMove(mc.machine, from, to)); got != want {
					t.Errorf("%s: from %s to %s is %v, want %v", name, stateName(from), stateName(to), got, want)
				}
			}
		}
		if !reflect.DeepEqual(answer.Stranger, []any{false, true}) {
			t.Errorf("%s: a value that is no member moves and stays as %v, want [false true]", name, answer.Stranger)
		}
	}
	return pairs
}
