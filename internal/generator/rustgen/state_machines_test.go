package rustgen

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// The state machine services (D117): the DB's Ticket has a machine on a
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

// stateMachineShapes is a schema of the machines the fixture has none of,
// each a shape of its own in the helper's match: Switch.latch, whose one
// state moves only to itself, so the helper passes an unchanged value and
// has no match; Door.lock, nullable with no fromNull, so the null rests
// with the states that have no move; and Post.phase, which returns to null
// or moves to one state.
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

// machineCase is one machine of a schema as a test asks its helper: the
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

// mayMove is D117's rule for a move, read from the IR: an unchanged value
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

// TestStateMachinesModuleIsWrittenForAMachine: a schema with a machine
// gets src/state_machines.rs, which the crate root declares and
// re-exports; a schema without one gets neither.
func TestStateMachinesModuleIsWrittenForAMachine(t *testing.T) {
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
		b, err := os.ReadFile(filepath.Join(dir, "src", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	dir, output := write(schema, Options{
		SchemaName:   stateMachineService,
		Dependencies: map[string]*ir.Schema{stateMachineEnumsService: enums},
	})
	var names []string
	for _, machine := range output.StateMachines {
		names = append(names, machine.TerminalConst, machine.CanTransitionFunc)
	}
	want := []string{
		"SIGN_OFF_STEP_TERMINAL", "sign_off_can_transition_step",
		"TICKET_STATE_TERMINAL", "ticket_can_transition_state",
		"TICKET_REVIEW_TERMINAL", "ticket_can_transition_review",
		"TICKET_ESCALATION_TERMINAL", "ticket_can_transition_escalation",
		"TICKET_ACCOUNT_STATUS_TERMINAL", "ticket_can_transition_account_status",
	}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("the machines' items are\n%v\nwant\n%v", names, want)
	}
	source := read(dir, StateMachinesFile)
	for _, name := range want {
		if !strings.Contains(source, "pub const "+name+":") && !strings.Contains(source, "pub fn "+name+"(") {
			t.Errorf("%s does not declare %s:\n%s", StateMachinesFile, name, source)
		}
	}
	// The schema's own enums come from its enums module, and the
	// dependency's by the alias a field of its type names it by.
	for _, line := range []string{
		"use crate::enums::{Escalation, ReviewOutcome, SignOffStep, TicketState};\n",
		"use crate::types::AccountStatus;\n",
		"pub fn ticket_can_transition_review(from: Option<ReviewOutcome>, to: Option<ReviewOutcome>) -> bool {\n",
		"pub fn ticket_can_transition_account_status(from: AccountStatus, to: AccountStatus) -> bool {\n",
	} {
		if !strings.Contains(source, line) {
			t.Errorf("%s lacks %q:\n%s", StateMachinesFile, line, source)
		}
	}
	// A state is the enum's variant, never its text.
	if strings.Contains(source, `"`) {
		t.Errorf("%s writes a state as text:\n%s", StateMachinesFile, source)
	}
	lib := read(dir, "lib.rs")
	for _, line := range []string{"pub mod state_machines;\n", "pub use state_machines::*;\n"} {
		if !strings.Contains(lib, line) {
			t.Errorf("lib.rs lacks %q:\n%s", line, lib)
		}
	}

	dir, output = write(enums, Options{SchemaName: stateMachineEnumsService})
	if len(output.StateMachines) != 0 {
		t.Fatalf("%s has machines: %+v", stateMachineEnumsService, output.StateMachines)
	}
	if _, err := os.Stat(filepath.Join(dir, "src", StateMachinesFile)); !os.IsNotExist(err) {
		t.Errorf("a schema without a machine has %s: %v", StateMachinesFile, err)
	}
	if lib := read(dir, "lib.rs"); strings.Contains(lib, "state_machines") {
		t.Errorf("lib.rs of a schema without a machine names state_machines:\n%s", lib)
	}
}

// TestStateMachineArms pins the helper's match for the shapes the fixture
// has none of (stateMachineShapes).
func TestStateMachineArms(t *testing.T) {
	output, err := Generate(stateMachineShapes(), Options{SchemaName: "state-machine-shapes"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]StateMachineArm{}
	for _, machine := range output.StateMachines {
		got[machine.TypeName+"."+machine.Field] = machine.Arms
	}
	want := map[string][]StateMachineArm{
		"Switch.latch": nil,
		"Door.lock": {
			{Pattern: "Some(Lock::Locked)", Admits: "to == Some(Lock::Open)"},
			{Pattern: "None | Some(Lock::Open)", Admits: "false"},
		},
		"Post.phase": {
			{Pattern: "None", Admits: "to == Some(Phase::Draft)"},
			{Pattern: "Some(Phase::Draft)", Admits: "matches!(to, None | Some(Phase::Live))"},
			{Pattern: "Some(Phase::Live)", Admits: "false"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the helpers' arms are\n%+v\nwant\n%+v", got, want)
	}
}

// TestStateMachinesRefuseWhatTheyCannotName: a machine whose field no enum
// of the schema or of a dependency it imports types, one with a state that
// is no member, and one on a field the type lacks fail the generation with
// the type and the field. The build's checks refuse each first (D117); the
// generator does not write a helper it cannot name the states of.
func TestStateMachinesRefuseWhatTheyCannotName(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(schema *ir.Schema)
		want   string
	}{
		{
			name:   "a dependency that was not imported",
			change: func(schema *ir.Schema) { schema.Imports = nil },
			want:   "rustgen: @stateMachine on Ticket.accountStatus: AccountStatus is no enum of fixture-state-machine-db or of a dependency it imports",
		},
		{
			name: "a state that is no member",
			change: func(schema *ir.Schema) {
				machine := schema.Types["Ticket"].StateMachineOf("state")
				machine.Terminal = append(machine.Terminal, "archived")
			},
			want: `rustgen: @stateMachine on Ticket.state: "archived" is no member of TicketState`,
		},
		{
			name: "a field the type lacks",
			change: func(schema *ir.Schema) {
				schema.Types["SignOff"].StateMachineOf("step").Field = "stage"
			},
			want: "rustgen: @stateMachine on SignOff.stage: SignOff has no field stage",
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

// stateMachinesRustTest writes a Rust test per machine of cases, which
// output's machines name in the same order: the helper has the field's
// type, each of its answers on every pair of the field's states is D117's
// rule read from the IR, and the terminal states are the IR's. The states
// are the enum's ALL, with None first on a nullable field, held to the
// IR's by their as_str.
func stateMachinesRustTest(t *testing.T, output *ModuleOutput, cases []machineCase) string {
	t.Helper()
	if len(output.StateMachines) != len(cases) {
		t.Fatalf("%s has %d machines, want %d", output.SchemaName, len(output.StateMachines), len(cases))
	}
	quoted := func(states []*string) string {
		names := make([]string, len(states))
		for i, state := range states {
			names[i] = strconv.Quote(stateName(state))
		}
		return strings.Join(names, ", ")
	}
	var uses []string
	var b strings.Builder
	for i, mc := range cases {
		info := output.StateMachines[i]
		if info.TypeName != mc.typeName || info.Field != mc.machine.Field {
			t.Fatalf("machine %d is %s.%s, want %s.%s", i, info.TypeName, info.Field, mc.typeName, mc.machine.Field)
		}
		for _, name := range []string{info.Enum, info.TerminalConst, info.CanTransitionFunc} {
			if !slices.Contains(uses, name) {
				uses = append(uses, name)
			}
		}
		fmt.Fprintf(&b, "\n#[test]\nfn %s_moves_as_its_machine_says() {\n", info.CanTransitionFunc)
		fmt.Fprintf(&b, "    let helper: fn(%[1]s, %[1]s) -> bool = %[2]s;\n", info.StateType(), info.CanTransitionFunc)
		if info.Nullable {
			fmt.Fprintf(&b, "    let states: Vec<Option<%[1]s>> = std::iter::once(None).chain(%[1]s::ALL.iter().copied().map(Some)).collect();\n", info.Enum)
			b.WriteString("    let names: Vec<&str> = states.iter().map(|state| state.map_or(\"null\", |state| state.as_str())).collect();\n")
		} else {
			fmt.Fprintf(&b, "    let states: Vec<%[1]s> = %[1]s::ALL.to_vec();\n", info.Enum)
			b.WriteString("    let names: Vec<&str> = states.iter().map(|state| state.as_str()).collect();\n")
		}
		fmt.Fprintf(&b, "    assert_eq!(names, [%s]);\n", quoted(mc.states))
		b.WriteString("    let want = [\n")
		for _, from := range mc.states {
			row := make([]string, len(mc.states))
			for j, to := range mc.states {
				row[j] = strconv.FormatBool(mayMove(mc.machine, from, to))
			}
			fmt.Fprintf(&b, "        [%s],\n", strings.Join(row, ", "))
		}
		b.WriteString("    ];\n")
		b.WriteString("    for (i, from) in states.iter().enumerate() {\n")
		b.WriteString("        for (j, to) in states.iter().enumerate() {\n")
		b.WriteString("            assert_eq!(helper(*from, *to), want[i][j], \"from {} to {}\", names[i], names[j]);\n")
		b.WriteString("        }\n    }\n")
		terminal := make([]*string, len(mc.machine.Terminal))
		for j := range mc.machine.Terminal {
			terminal[j] = &mc.machine.Terminal[j]
		}
		fmt.Fprintf(&b, "    let terminal: Vec<&str> = %s.iter().map(|state| state.as_str()).collect();\n", info.TerminalConst)
		fmt.Fprintf(&b, "    let want_terminal: &[&str] = &[%s];\n", quoted(terminal))
		b.WriteString("    assert_eq!(terminal, want_terminal);\n}\n")
	}
	sort.Strings(uses)
	crate := strings.ReplaceAll(output.CrateName, "-", "_")
	return "use " + crate + "::{" + strings.Join(uses, ", ") + "};\n" + b.String()
}

// TestStateMachineHelpers builds the state machine services' crates, and
// a crate of the shapes they have none of, without a warning from rustc or
// clippy, and runs every machine's helper on every pair of its field's
// states, None among them where the field is nullable: each answer is
// D117's rule read from the IR. Skips when cargo is unavailable.
func TestStateMachineHelpers(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compiled state machine check in short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping compiled state machine check")
	}
	schema, enums := stateMachineSchemas(t)
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

	// A crate names its dependency's by ../<service>, so the three stand
	// side by side, and share a target directory.
	root := testpaths.TempDir(t)
	paths := testpaths.Local(t)
	write := func(schema *ir.Schema, opts Options) (string, *ModuleOutput) {
		t.Helper()
		opts.Clock = codegen.FixedClock(time.Unix(0, 0).UTC())
		output, err := Generate(schema, opts)
		if err != nil {
			t.Fatalf("generate %s: %v", opts.SchemaName, err)
		}
		dir := filepath.Join(root, opts.SchemaName)
		if err := SetLocalPaths(output, paths, dir); err != nil {
			t.Fatal(err)
		}
		if err := WriteTypes(output, dir); err != nil {
			t.Fatalf("write %s: %v", opts.SchemaName, err)
		}
		return dir, output
	}
	write(enums, Options{SchemaName: stateMachineEnumsService})
	dbDir, dbOutput := write(schema, Options{
		SchemaName:   stateMachineService,
		Dependencies: map[string]*ir.Schema{stateMachineEnumsService: enums},
		SQLite:       true,
	})
	shapesDir, shapesOutput := write(shapes, Options{SchemaName: shapes.Name})

	// The dependency's enum is the one the helper takes: a state read
	// from the dependency's crate moves as the alias does.
	const dependencyTest = `
#[test]
fn a_state_of_the_dependency_s_enum_moves() {
    use schemas_fixture_state_machine_enums_types::AccountStatus as Owned;
    assert!(ticket_can_transition_account_status(Owned::Active, AccountStatus::Closed));
    assert!(!ticket_can_transition_account_status(Owned::Closed, AccountStatus::Active));
    assert_eq!(TICKET_ACCOUNT_STATUS_TERMINAL, [Owned::Closed]);
}
`
	for _, crate := range []struct {
		dir  string
		test string
	}{
		{dbDir, stateMachinesRustTest(t, dbOutput, cases) + dependencyTest},
		{shapesDir, stateMachinesRustTest(t, shapesOutput, machineCases(t, shapes))},
	} {
		testsDir := filepath.Join(crate.dir, "tests")
		if err := os.Mkdir(testsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(testsDir, "state_machines.rs"), []byte(crate.test), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{
			{"clippy", "--quiet", "--", "-D", "warnings"},
			{"test", "--quiet"},
		} {
			cmd := exec.Command(cargo, args...)
			cmd.Dir = crate.dir
			cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+filepath.Join(root, "target"), "RUSTFLAGS=-D warnings")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("cargo %s in %s: %v\n%s\nthe test:\n%s", args[0], crate.dir, err, out, crate.test)
			}
		}
	}
}
