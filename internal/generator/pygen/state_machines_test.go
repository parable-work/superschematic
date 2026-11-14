package pygen

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
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

// stateMachineAllows is D118's rule, read from the IR alone: an unchanged
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
			{Name: "stuckOn", TypeRef: ir.TypeRef{Name: "Light"}, Required: true},
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
				Field:    "stuckOn",
				Terminal: []string{"red", "amber", "green"},
			},
		},
	}
	return schema
}

// pythonMachineCase is one machine as the Python probe checks it: where
// its helpers are, and what the IR says of every pair of states. A state
// is its serialized value, and null is None.
type pythonMachineCase struct {
	Module string `json:"module"`
	Type   string `json:"type"`
	Field  string `json:"field"`
	Enum   string `json:"enum"`
	// Const and Method are D118's names, written here from the field's.
	Const  string `json:"const"`
	Method string `json:"method"`
	// Transitions holds, per state that has a move, the states it may
	// move to; the key "" is null.
	Transitions map[string][]*string `json:"transitions"`
	Terminal    []string             `json:"terminal"`
	Moves       []pythonMove         `json:"moves"`
}

type pythonMove struct {
	From *string `json:"from"`
	To   *string `json:"to"`
	Want bool    `json:"want"`
}

// pythonMachineCases reads the machines of schema off the IR: for each,
// every pair of the enum's states and null, with what stateMachineAllows
// answers. A required field is asked about null too: Python hands the
// helper any value, and a null there neither takes a state nor is
// returned to.
func pythonMachineCases(t *testing.T, module string, schema *ir.Schema, deps map[string]*ir.Schema) []pythonMachineCase {
	t.Helper()
	typeNames := make([]string, 0, len(schema.Types))
	for name := range schema.Types {
		typeNames = append(typeNames, name)
	}
	sort.Strings(typeNames)

	var cases []pythonMachineCase
	for _, typeName := range typeNames {
		td := schema.Types[typeName]
		for _, machine := range td.StateMachines {
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

			snake := codegen.ToSnakeCase(machine.Field)
			tc := pythonMachineCase{
				Module:      module,
				Type:        td.Name,
				Field:       machine.Field,
				Enum:        enum.Name,
				Const:       strings.ToUpper(snake),
				Method:      "can_transition_" + snake,
				Transitions: map[string][]*string{},
				Terminal:    append([]string{}, machine.Terminal...),
			}
			if len(machine.FromNull) > 0 {
				for _, state := range machine.FromNull {
					tc.Transitions[""] = append(tc.Transitions[""], &state)
				}
			}
			for _, transition := range machine.Transitions {
				to := []*string{}
				for _, state := range transition.To {
					to = append(to, &state)
				}
				if transition.ToNull {
					to = append(to, nil)
				}
				tc.Transitions[transition.From] = to
			}

			states := []*string{nil}
			for _, value := range enum.Values {
				serialized := value.Serialized()
				states = append(states, &serialized)
			}
			allowed, refused := 0, 0
			for _, from := range states {
				for _, to := range states {
					want := stateMachineAllows(machine, from, to)
					if want {
						allowed++
					} else {
						refused++
					}
					tc.Moves = append(tc.Moves, pythonMove{From: from, To: to, Want: want})
				}
			}
			if allowed == 0 || refused == 0 {
				t.Fatalf("%s.%s: %d moves allowed and %d refused; the machine proves nothing", td.Name, machine.Field, allowed, refused)
			}
			cases = append(cases, tc)
		}
	}
	return cases
}

// stateMachineProbe checks every machine of the cases it reads as JSON
// (D118). A move is asked with the enum's members and with their values,
// which a model built with use_enum_values holds, and the two constants
// are held to the IR's lists and to being read-only. A model keeps its
// fields: the constants are no field of it.
const stateMachineProbe = `
import importlib
import json
import sys

cases = json.loads(sys.argv[1])
assert cases, "no machine to check"
for case in cases:
    module = importlib.import_module(case["module"])
    model = getattr(module, case["type"])
    enum = getattr(module, case["enum"])
    where = case["type"] + "." + case["field"]
    can_transition = getattr(model, case["method"])
    transitions = getattr(model, case["const"] + "_TRANSITIONS")
    terminal = getattr(model, case["const"] + "_TERMINAL")

    def member(value):
        return None if value is None else enum(value)

    for move in case["moves"]:
        current, target, want = move["from"], move["to"], move["want"]
        got = can_transition(member(current), member(target))
        assert got is want, f"{where}: {current!r} -> {target!r} = {got!r}, want {want!r}"
        got = can_transition(current, target)
        assert got is want, f"{where}: by value, {current!r} -> {target!r} = {got!r}, want {want!r}"

    assert terminal == frozenset(enum(value) for value in case["terminal"]), f"{where}: terminal {terminal!r}"
    assert all(isinstance(state, enum) for state in terminal), f"{where}: terminal {terminal!r}"
    want = {
        member(current or None): frozenset(member(target) for target in targets)
        for current, targets in case["transitions"].items()
    }
    assert dict(transitions) == want, f"{where}: transitions {dict(transitions)!r}, want {want!r}"
    for current, targets in transitions.items():
        assert current is None or isinstance(current, enum), f"{where}: {current!r}"
        assert all(target is None or isinstance(target, enum) for target in targets), f"{where}: {targets!r}"
    try:
        transitions[None] = frozenset()
    except TypeError:
        pass
    else:
        raise AssertionError(f"{where}: the transitions took a write")
    for name in (case["const"] + "_TRANSITIONS", case["const"] + "_TERMINAL", case["method"]):
        assert name not in model.model_fields, f"{where}: {name} is a field of the model"
`

// TestGeneratedStateMachineHelpers generates the Python types packages of
// the state machine fixture and of stateMachineEdges and runs
// stateMachineProbe on them (D118): for every machine, every pair of
// states and null is asked of can_transition_<field> and held to
// stateMachineAllows on the IR, and <FIELD>_TRANSITIONS and
// <FIELD>_TERMINAL to the IR's lists.
func TestGeneratedStateMachineHelpers(t *testing.T) {
	fixedClock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	enums, err := loader.LoadService(filepath.Join(fixturesDir, stateMachineEnumService))
	if err != nil {
		t.Fatalf("load %s: %v", stateMachineEnumService, err)
	}
	db, err := loader.LoadService(filepath.Join(fixturesDir, stateMachineService))
	if err != nil {
		t.Fatalf("load %s: %v", stateMachineService, err)
	}
	edges := stateMachineEdges()

	tempRoot := t.TempDir()
	services := []struct {
		name   string
		schema *ir.Schema
		deps   map[string]*ir.Schema
		// machines is how many machines the probe must cover.
		machines int
	}{
		{name: stateMachineEnumService, schema: enums},
		{name: stateMachineService, schema: db, deps: map[string]*ir.Schema{stateMachineEnumService: enums}, machines: 5},
		{name: edges.Name, schema: edges, machines: 3},
	}
	var cases []pythonMachineCase
	var importPaths []string
	outDirs := map[string]string{}
	for _, service := range services {
		output, err := Generate(service.schema, Options{SchemaName: service.name, Dependencies: service.deps, Clock: fixedClock})
		if err != nil {
			t.Fatalf("generate %s: %v", service.name, err)
		}
		outDir := filepath.Join(tempRoot, service.name)
		if err := WriteTypes(output, outDir); err != nil {
			t.Fatalf("write %s: %v", service.name, err)
		}
		outDirs[service.name] = outDir
		importPaths = append(importPaths, outDir)
		if output.HasStateMachines() != (service.machines > 0) {
			t.Fatalf("%s: HasStateMachines = %v, with %d machines", service.name, output.HasStateMachines(), service.machines)
		}
		found := pythonMachineCases(t, output.PythonModuleName, service.schema, service.deps)
		if len(found) != service.machines {
			t.Fatalf("%s: the probe covers %d machines, want %d", service.name, len(found), service.machines)
		}
		cases = append(cases, found...)

		typesPy, err := os.ReadFile(filepath.Join(outDir, output.PythonModuleName, "types.py"))
		if service.machines == 0 {
			// A package with no model writes no types.py; one with models
			// and no machine imports nothing for one.
			if err == nil && strings.Contains(string(typesPy), "MappingProxyType") {
				t.Fatalf("%s: types.py imports MappingProxyType with no machine", service.name)
			}
			continue
		}
		if err != nil {
			t.Fatalf("read %s types.py: %v", service.name, err)
		}
		for _, tc := range found {
			for _, expected := range []string{tc.Const + "_TRANSITIONS: ClassVar[", tc.Const + "_TERMINAL: ClassVar[", "def " + tc.Method + "(cls, "} {
				if !strings.Contains(string(typesPy), expected) {
					t.Fatalf("%s: types.py is missing %q:\n%s", service.name, expected, typesPy)
				}
			}
		}
	}

	// A machine names the members of the dependency's enum, so its
	// package is imported with no fallback.
	dbTypes, err := os.ReadFile(filepath.Join(outDirs[stateMachineService], "schemas_types_fixture_state_machine_db", "types.py"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"AccountStatus.Active: frozenset({AccountStatus.Suspended, AccountStatus.Closed}),",
		"    from schemas_types_fixture_state_machine_enums import (\n        AccountStatus\n    )\n",
	} {
		if !strings.Contains(string(dbTypes), expected) {
			t.Fatalf("types.py is missing %q:\n%s", expected, dbTypes)
		}
	}
	if strings.Contains(string(dbTypes), "AccountStatus = Any") {
		t.Fatalf("types.py binds the enum of a state machine to Any:\n%s", dbTypes)
	}

	if testing.Short() {
		t.Skip("skipping generated-package run in -short mode")
	}
	uv, err := exec.LookPath("uv")
	if err != nil {
		t.Skip("uv unavailable; generated source assertions passed")
	}
	project := filepath.Join(testpaths.RepoRoot(t), "runtime", "schema", "python")
	if out, err := exec.Command(uv, "run", "--project", project, "python", "-c", "import pydantic").CombinedOutput(); err != nil {
		t.Skipf("runtime/schema/python's environment lacks pydantic: %v\n%s", err, out)
	}
	run := func(pythonPath []string, script string, args ...string) ([]byte, error) {
		command := exec.Command(uv, append([]string{"run", "--project", project, "python", "-B", "-c", script}, args...)...)
		command.Env = append(os.Environ(), "PYTHONPATH="+strings.Join(pythonPath, string(os.PathListSeparator)))
		return command.CombinedOutput()
	}

	encoded, err := json.Marshal(cases)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := run(importPaths, stateMachineProbe, string(encoded)); err != nil {
		t.Fatalf("the generated state machine helpers disagree with the IR: %v\n%s", err, out)
	}

	// A model holds a state as its value, which the helper takes as it
	// takes the member.
	if out, err := run(importPaths, `
from schemas_types_fixture_state_machine_db import SignOff, SignOffStep

sign_off = SignOff()
assert sign_off.step == "requested" and not isinstance(sign_off.step, SignOffStep), sign_off.step
assert SignOff.can_transition_step(sign_off.step, SignOffStep.Granted)
sign_off.step = SignOffStep.Granted
assert not SignOff.can_transition_step(sign_off.step, SignOffStep.Requested)
assert sign_off.can_transition_step(sign_off.step, sign_off.step)
`); err != nil {
		t.Fatalf("a model's own state: %v\n%s", err, out)
	}

	// Without the dependency's package the import fails by its name.
	out, err := run([]string{outDirs[stateMachineService]}, "import schemas_types_fixture_state_machine_db")
	if err == nil || !strings.Contains(string(out), "ModuleNotFoundError: No module named 'schemas_types_fixture_state_machine_enums'") {
		t.Fatalf("import without the dependency's package: %v\n%s", err, out)
	}
}

// A machine whose constants or method would take the name of a field of
// the model, or of another machine's, fails the build with both named.
func TestStateMachineNameClash(t *testing.T) {
	machine := func(field string) *ir.StateMachine {
		return &ir.StateMachine{Field: field, Transitions: []*ir.StateTransition{{From: "A", To: []string{"B"}}}, Terminal: []string{"B"}}
	}
	step := func(name string) *ir.FieldDef {
		return &ir.FieldDef{Name: name, TypeRef: ir.TypeRef{Name: "Step"}, Required: true}
	}
	build := func(fields []*ir.FieldDef, machines ...*ir.StateMachine) *ir.Schema {
		schema := ir.NewSchema("clash", ir.SchemaKindGeneral)
		schema.Enums["Step"] = &ir.EnumDef{Name: "Step", Values: []ir.EnumValueDef{{Name: "A"}, {Name: "B"}}}
		schema.Types["Job"] = &ir.TypeDef{Name: "Job", Role: ir.RoleEmbeddedStruct, Fields: fields, StateMachines: machines}
		return schema
	}
	cases := []struct {
		name   string
		schema *ir.Schema
		want   string
	}{
		{
			name: "a field",
			schema: build([]*ir.FieldDef{
				step("step"),
				{Name: "canTransitionStep", TypeRef: ir.TypeRef{Name: "boolean"}, Required: true},
			}, machine("step")),
			want: "the state machine on Job.step needs the name can_transition_step on the model Job, which is also the name of the field canTransitionStep",
		},
		{
			name:   "another machine",
			schema: build([]*ir.FieldDef{step("nextStep"), step("next_step")}, machine("nextStep"), machine("next_step")),
			want:   "the state machine on Job.next_step needs the name NEXT_STEP_TRANSITIONS on the model Job, which is also the name of the state machine on Job.nextStep",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Generate(tc.schema, Options{SchemaName: "clash", Clock: codegen.FixedClock(time.Unix(0, 0).UTC())})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Generate error = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

// A base declares a machine and the package writes no model for it, so
// there is none to carry its helpers: types.py holds no machine.
func TestStateMachineOfABaseHasNoModel(t *testing.T) {
	schema := ir.NewSchema("bases", ir.SchemaKindGeneral)
	schema.Enums["Step"] = &ir.EnumDef{Name: "Step", Values: []ir.EnumValueDef{{Name: "A"}, {Name: "B"}}}
	schema.Types["Tracked"] = &ir.TypeDef{
		Name:          "Tracked",
		Role:          ir.RoleTrait,
		IsTrait:       true,
		Fields:        []*ir.FieldDef{{Name: "step", TypeRef: ir.TypeRef{Name: "Step"}, Required: true}},
		StateMachines: []*ir.StateMachine{{Field: "step", Transitions: []*ir.StateTransition{{From: "A", To: []string{"B"}}}, Terminal: []string{"B"}}},
	}
	output, err := Generate(schema, Options{SchemaName: "bases", Clock: codegen.FixedClock(time.Unix(0, 0).UTC())})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(output.Types) != 0 || output.HasStateMachines() || output.StateMachinesOf("Tracked") != nil {
		t.Fatalf("a base with no model carries a machine: %+v", output.StateMachinesOf("Tracked"))
	}
}
