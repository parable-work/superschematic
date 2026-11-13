package typegen

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

const (
	stateMachineService     = "fixture-state-machine-db"
	stateMachineEnumService = "fixture-state-machine-enums"
)

// stateMachineAllows is D117's rule, read from the IR alone: an unchanged
// value is no move and is allowed, null to null among them; from null a
// field takes a fromNull state; no move leaves a terminal state; and any
// other move is allowed only when the machine lists it, to null where the
// transition has toNull. nil is null.
func stateMachineAllows(machine *ir.StateMachine, from, to *string) bool {
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
	for _, transition := range machine.Transitions {
		if transition.From != *from {
			continue
		}
		if to == nil {
			return transition.ToNull
		}
		return slices.Contains(transition.To, *to)
	}
	return false
}

// TestStateMachineRule holds stateMachineAllows, which the generated
// helpers are held to below, to moves of the fixture read by hand.
func TestStateMachineRule(t *testing.T) {
	schema, _ := loadFixture(t, stateMachineService)
	null := "null"
	cases := []struct {
		field    string
		from, to string
		want     bool
	}{
		{"state", "open", "in_progress", true},
		{"state", "open", "open", true},
		{"state", "in_progress", "in_progress", true},
		{"state", "in_progress", "closed", false},
		{"state", "resolved", "closed", true},
		{"state", "closed", "open", false},
		{"state", "closed", "closed", true},
		{"review", null, null, true},
		{"review", null, "APPROVED", true},
		{"review", "RETRY", null, true},
		{"review", "RETRY", "REJECTED", true},
		{"review", "APPROVED", null, false},
		{"review", "APPROVED", "RETRY", false},
		{"review", "REJECTED", "REJECTED", true},
		{"escalation", null, "raised", true},
		{"escalation", null, "acknowledged", false},
		{"escalation", "raised", "acknowledged", true},
		{"escalation", "raised", null, false},
		{"escalation", "acknowledged", null, true},
		{"escalation", "acknowledged", "raised", false},
		{"accountStatus", "active", "closed", true},
		{"accountStatus", "suspended", "active", true},
		{"accountStatus", "closed", "active", false},
	}
	for _, tc := range cases {
		machine := schema.Types["Ticket"].StateMachineOf(tc.field)
		if machine == nil {
			t.Fatalf("Ticket has no machine on %s", tc.field)
		}
		state := func(s string) *string {
			if s == null {
				return nil
			}
			return &s
		}
		if got := stateMachineAllows(machine, state(tc.from), state(tc.to)); got != tc.want {
			t.Errorf("Ticket.%s %s -> %s = %v, want %v", tc.field, tc.from, tc.to, got, tc.want)
		}
	}
}

// stateMachineEdges is a schema whose machines take the shapes the
// fixture's do not: a nullable field no null start reaches and no state
// returns from, one a null start reaches whose only other move is back to
// null, and a required field whose states move nowhere.
func stateMachineEdges() *ir.Schema {
	schema := ir.NewSchema("state-machine-edges", ir.SchemaKindGeneral)
	schema.Enums["Light"] = &ir.EnumDef{Name: "Light", Values: []ir.EnumValueDef{
		{Name: "Red", SerializedAs: "red"},
		{Name: "Amber", SerializedAs: "amber"},
		{Name: "Green", SerializedAs: "green"},
	}}
	schema.Types["Signal"] = &ir.TypeDef{
		Name: "Signal",
		Role: ir.RoleEmbeddedStruct,
		Fields: []*ir.FieldDef{
			{Name: "unlit", TypeRef: ir.TypeRef{Name: "Light"}},
			{Name: "blinking", TypeRef: ir.TypeRef{Name: "Light"}},
			{Name: "stuck", TypeRef: ir.TypeRef{Name: "Light"}, Required: true},
		},
		StateMachines: []*ir.StateMachine{
			{
				Field: "unlit",
				Transitions: []*ir.StateTransition{
					{From: "red", To: []string{"green"}},
					{From: "amber", To: []string{"red"}},
				},
				Terminal: []string{"green"},
			},
			{
				Field:       "blinking",
				Transitions: []*ir.StateTransition{{From: "amber", ToNull: true}},
				Terminal:    []string{"red", "green"},
				FromNull:    []string{"amber"},
			},
			{
				Field:    "stuck",
				Terminal: []string{"red", "amber", "green"},
			},
		},
	}
	return schema
}

// TestGeneratedStateMachineHelpers generates the Go types modules of the
// state machine fixture and of stateMachineEdges, and runs in each a test
// of its helpers (D117): for every machine, every pair of states, null
// among them on a nullable field, is asked of <Type>CanTransition<Field>
// and held to stateMachineAllows on the IR, and <Type><Field>Terminal
// returns the machine's terminal states, a new slice a call. A state is
// written by its serialized value, so the constants the helper names are
// held to the values the IR holds.
func TestGeneratedStateMachineHelpers(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated module's test in -short mode")
	}
	paths := testpaths.Local(t)
	typesGoDir := filepath.Join(t.TempDir(), "types", "go")
	fixedClock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))

	enums, _ := loadFixture(t, stateMachineEnumService)
	db, _ := loadFixture(t, stateMachineService)
	edges := stateMachineEdges()
	cases := []struct {
		name   string
		schema *ir.Schema
		deps   map[string]*ir.Schema
		// machines is how many machines the module's test must cover.
		machines int
	}{
		{name: stateMachineEnumService, schema: enums},
		{name: stateMachineService, schema: db, deps: map[string]*ir.Schema{stateMachineEnumService: enums}, machines: 5},
		{name: edges.Name, schema: edges, machines: 3},
	}
	for _, tc := range cases {
		depModules := map[string]string{}
		for depName := range tc.deps {
			depModules[depName] = "example.com/schemas/types/go/" + depName
		}
		output, err := Generate(tc.schema, Options{
			SchemaName:        tc.name,
			ModulePath:        "example.com/schemas/types/go/" + tc.name,
			Dependencies:      tc.deps,
			DependencyModules: depModules,
			Clock:             fixedClock,
		})
		if err != nil {
			t.Fatalf("generate %s: %v", tc.name, err)
		}
		outDir := filepath.Join(typesGoDir, tc.name)
		if err := SetReplacePaths(output, paths, outDir); err != nil {
			t.Fatalf("set replace paths for %s: %v", tc.name, err)
		}
		if err := WriteTypes(output, outDir); err != nil {
			t.Fatalf("write %s: %v", tc.name, err)
		}
		_, err = os.Stat(filepath.Join(outDir, "state_machines.go"))
		if wrote := err == nil; wrote != (tc.machines > 0) {
			t.Fatalf("%s: state_machines.go written = %v, with %d machines", tc.name, wrote, tc.machines)
		}
	}

	for _, tc := range cases {
		outDir := filepath.Join(typesGoDir, tc.name)
		tidy := exec.Command("go", "mod", "tidy")
		tidy.Dir = outDir
		if out, err := tidy.CombinedOutput(); err != nil {
			t.Skipf("go mod tidy failed for %s (likely offline): %v\n%s", tc.name, err, out)
		}
		if tc.machines == 0 {
			continue
		}
		source, machines := goStateMachineTest(t, tc.schema, tc.deps)
		if machines != tc.machines {
			t.Fatalf("%s: the test covers %d machines, want %d", tc.name, machines, tc.machines)
		}
		if err := os.WriteFile(filepath.Join(outDir, "state_machines_test.go"), []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
		test := exec.Command("go", "test", "./...")
		test.Dir = outDir
		if out, err := test.CombinedOutput(); err != nil {
			t.Errorf("%s: the generated state machine helpers disagree with the IR: %v\n%s", tc.name, err, out)
		}
	}
}

// goStateMachineTest writes a Go test of the helpers of every machine of
// schema, and returns it with the number of machines it covers. Each move
// is one line that calls the helper and carries what stateMachineAllows
// answers; a machine whose moves are all allowed or all refused would
// prove little, so it fails here.
func goStateMachineTest(t *testing.T, schema *ir.Schema, deps map[string]*ir.Schema) (string, int) {
	t.Helper()
	var moves, terminals strings.Builder
	machines := 0

	typeNames := make([]string, 0, len(schema.Types))
	for name := range schema.Types {
		typeNames = append(typeNames, name)
	}
	sort.Strings(typeNames)
	for _, typeName := range typeNames {
		td := schema.Types[typeName]
		for _, machine := range td.StateMachines {
			machines++
			var field *ir.FieldDef
			for _, candidate := range td.Fields {
				if candidate.Name == machine.Field {
					field = candidate
				}
			}
			if field == nil {
				t.Fatalf("%s has no field %s", td.Name, machine.Field)
			}
			enum := schema.Enums[field.TypeRef.Name]
			for _, dep := range deps {
				if enum == nil {
					enum = dep.Enums[field.TypeRef.Name]
				}
			}
			if enum == nil {
				t.Fatalf("%s.%s: no enum %s", td.Name, machine.Field, field.TypeRef.Name)
			}
			nullable := !field.Required

			var states []*string
			if nullable {
				states = append(states, nil)
			}
			for _, value := range enum.Values {
				serialized := value.Serialized()
				states = append(states, &serialized)
			}
			// A state as the helper takes it: the enum's type over the
			// serialized value, behind a pointer on a nullable field.
			literal := func(state *string) string {
				switch {
				case state == nil:
					return "nil"
				case nullable:
					return fmt.Sprintf("ptr(%s(%q))", enum.Name, *state)
				}
				return fmt.Sprintf("%s(%q)", enum.Name, *state)
			}
			label := func(state *string) string {
				if state == nil {
					return "null"
				}
				return *state
			}

			goField := codegen.ToPascalCase(machine.Field)
			allowed, refused := 0, 0
			for _, from := range states {
				for _, to := range states {
					want := stateMachineAllows(machine, from, to)
					if want {
						allowed++
					} else {
						refused++
					}
					fmt.Fprintf(&moves, "\t\t{%q, %sCanTransition%s(%s, %s), %v},\n",
						fmt.Sprintf("%s.%s %s -> %s", td.Name, machine.Field, label(from), label(to)),
						td.Name, goField, literal(from), literal(to), want)
				}
			}
			if allowed == 0 || refused == 0 {
				t.Fatalf("%s.%s: %d moves allowed and %d refused; the machine proves nothing", td.Name, machine.Field, allowed, refused)
			}

			terminal := make([]string, 0, len(machine.Terminal))
			for _, state := range machine.Terminal {
				terminal = append(terminal, strconv.Quote(state))
			}
			fmt.Fprintf(&terminals, "\tcheckTerminal(t, %q, %s%sTerminal, []%s{%s})\n",
				td.Name+"."+machine.Field, td.Name, goField, enum.Name, strings.Join(terminal, ", "))
		}
	}

	return `package types

import (
	"slices"
	"testing"
)

func ptr[T any](value T) *T { return &value }

func TestCanTransition(t *testing.T) {
	moves := []struct {
		name      string
		got, want bool
	}{
` + moves.String() + `	}
	for _, move := range moves {
		if move.got != move.want {
			t.Errorf("%s: allowed = %v, want %v", move.name, move.got, move.want)
		}
	}
}

func checkTerminal[E ~string](t *testing.T, name string, terminal func() []E, want []E) {
	t.Helper()
	got := terminal()
	if got == nil || !slices.Equal(got, want) {
		t.Errorf("%s: terminal = %#v, want %#v", name, got, want)
	}
	if len(got) > 0 {
		got[0] = "changed"
		if again := terminal(); !slices.Equal(again, want) {
			t.Errorf("%s: a caller's change reached the next call: %#v", name, again)
		}
	}
}

func TestTerminal(t *testing.T) {
` + terminals.String() + `}
`, machines
}

// A helper that would take the name of a type of the package, or of
// another helper, fails the build with both named: Go would not compile
// the two.
func TestStateMachineHelperNameClash(t *testing.T) {
	machine := func(field string) *ir.StateMachine {
		return &ir.StateMachine{Field: field, Transitions: []*ir.StateTransition{{From: "A", To: []string{"B"}}}, Terminal: []string{"B"}}
	}
	base := func() *ir.Schema {
		schema := ir.NewSchema("clash", ir.SchemaKindGeneral)
		schema.Enums["Step"] = &ir.EnumDef{Name: "Step", Values: []ir.EnumValueDef{{Name: "A"}, {Name: "B"}}}
		return schema
	}
	step := func(name string) *ir.FieldDef {
		return &ir.FieldDef{Name: name, TypeRef: ir.TypeRef{Name: "Step"}, Required: true}
	}

	withType := base()
	withType.Types["Job"] = &ir.TypeDef{Name: "Job", Role: ir.RoleEmbeddedStruct, Fields: []*ir.FieldDef{step("step")}, StateMachines: []*ir.StateMachine{machine("step")}}
	withType.Types["JobStepTerminal"] = &ir.TypeDef{Name: "JobStepTerminal", Role: ir.RoleEmbeddedStruct}

	withHelper := base()
	withHelper.Types["Job"] = &ir.TypeDef{Name: "Job", Role: ir.RoleEmbeddedStruct, Fields: []*ir.FieldDef{step("stepNext")}, StateMachines: []*ir.StateMachine{machine("stepNext")}}
	withHelper.Types["JobStep"] = &ir.TypeDef{Name: "JobStep", Role: ir.RoleEmbeddedStruct, Fields: []*ir.FieldDef{step("next")}, StateMachines: []*ir.StateMachine{machine("next")}}

	cases := []struct {
		name   string
		schema *ir.Schema
		want   string
	}{
		{"a type", withType, "the state machine on Job.step needs the Go function JobStepTerminal, which is also the name of the type JobStepTerminal"},
		{"another helper", withHelper, "the state machine on JobStep.next needs the Go function JobStepNextTerminal, which is also the name of the state machine on Job.stepNext"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Generate(tc.schema, Options{ModulePath: "example.com/clash", Clock: codegen.FixedClock(time.Unix(0, 0).UTC())})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Generate error = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

// A schema that is the loader's own reads the fixture's machines in the
// order of each enum's members, which state_machines.go's cases keep.
func TestStateMachinesFollowTheLoadedOrder(t *testing.T) {
	enums, err := loader.LoadService(filepath.Join(fixturesDir, stateMachineEnumService))
	if err != nil {
		t.Fatalf("load %s: %v", stateMachineEnumService, err)
	}
	db, err := loader.LoadService(filepath.Join(fixturesDir, stateMachineService))
	if err != nil {
		t.Fatalf("load %s: %v", stateMachineService, err)
	}
	output, err := Generate(db, Options{
		SchemaName:   stateMachineService,
		ModulePath:   "example.com/schemas/types/go/" + stateMachineService,
		Dependencies: map[string]*ir.Schema{stateMachineEnumService: enums},
		Clock:        codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	var got []string
	for _, machine := range output.StateMachines {
		froms := make([]string, 0, len(machine.Moves))
		for _, moves := range machine.Moves {
			froms = append(froms, moves.From)
		}
		got = append(got, machine.CanTransitionFunc()+": "+strings.Join(froms, " "))
	}
	want := []string{
		"SignOffCanTransitionStep: SignOffStep_Requested",
		"TicketCanTransitionState: TicketState_Open TicketState_InProgress TicketState_Resolved",
		"TicketCanTransitionReview: ReviewOutcome_RETRY",
		"TicketCanTransitionEscalation: Escalation_Raised",
		"TicketCanTransitionAccountStatus: AccountStatus_Active AccountStatus_Suspended",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("helpers and the states that move:\n got %q\nwant %q", got, want)
	}
}

// A base declares a machine and the module writes no struct for it: its
// helpers are written all the same, since they name the field's enum
// alone (D117).
func TestStateMachineOfABaseKeepsItsHelpers(t *testing.T) {
	schema := ir.NewSchema("bases", ir.SchemaKindGeneral)
	schema.Enums["Step"] = &ir.EnumDef{Name: "Step", Values: []ir.EnumValueDef{{Name: "A"}, {Name: "B"}}}
	schema.Types["Tracked"] = &ir.TypeDef{
		Name:          "Tracked",
		Role:          ir.RoleTrait,
		IsTrait:       true,
		Fields:        []*ir.FieldDef{{Name: "step", TypeRef: ir.TypeRef{Name: "Step"}, Required: true}},
		StateMachines: []*ir.StateMachine{{Field: "step", Transitions: []*ir.StateTransition{{From: "A", To: []string{"B"}}}, Terminal: []string{"B"}}},
	}
	output, err := Generate(schema, Options{ModulePath: "example.com/bases", Clock: codegen.FixedClock(time.Unix(0, 0).UTC())})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(output.Types) != 0 {
		t.Fatalf("the module writes a struct for the base: %+v", output.Types)
	}
	if len(output.StateMachines) != 1 || output.StateMachines[0].CanTransitionFunc() != "TrackedCanTransitionStep" || !output.HasStateMachines() {
		t.Fatalf("the base's helpers: %+v", output.StateMachines)
	}
}
