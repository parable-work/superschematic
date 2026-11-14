package registry

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

// applyStateMachine applies @stateMachine to a type named Ticket as the
// TypeScript reader does: its one argument an evaluated object literal.
func applyStateMachine(t *testing.T, td *ir.TypeDef, args ...any) error {
	t.Helper()
	spec, ok := New(naming.Naming{}).Decorator("stateMachine", TargetType)
	if !ok {
		t.Fatal("@stateMachine is not registered")
	}
	return spec.Apply(Node{Type: td}, args, Site{})
}

// TestStateMachineIsASchemaPackageDecoratorOfEveryKind: @stateMachine comes
// from @superschematic/schema and stands on a class of any schema kind
// (D118).
func TestStateMachineIsASchemaPackageDecoratorOfEveryKind(t *testing.T) {
	spec, ok := New(naming.Naming{}).Decorator("stateMachine", TargetType)
	if !ok {
		t.Fatal("@stateMachine is not registered")
	}
	if !reflect.DeepEqual(spec.Packages, []string{pkgSchema}) {
		t.Errorf("Packages = %v, want %s alone", spec.Packages, pkgSchema)
	}
	for _, kind := range []ir.SchemaKind{ir.SchemaKindDB, ir.SchemaKindAPI, ir.SchemaKindGeneral} {
		if !spec.AllowsKind(string(kind)) {
			t.Errorf("@stateMachine is refused in a %s schema", kind)
		}
	}
}

// TestStateMachineApplyReadsTheDeclaration: Apply records the field, each
// state's moves with a null as a return to null, the terminal states and
// the null start, the transitions by their state's value whatever order
// the object's keys came in, and every list as written (D118).
func TestStateMachineApplyReadsTheDeclaration(t *testing.T) {
	td := &ir.TypeDef{Name: "Ticket"}
	err := applyStateMachine(t, td, map[string]any{
		"field": "review",
		"transitions": map[string]any{
			"RETRY":   []any{nil, "REJECTED", "APPROVED"},
			"PENDING": []any{"RETRY"},
			"HELD":    []any{nil},
		},
		"terminal": []any{"REJECTED", "APPROVED"},
		"fromNull": []any{"RETRY", "PENDING"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []*ir.StateMachine{{
		Field: "review",
		Transitions: []*ir.StateTransition{
			{From: "HELD", ToNull: true},
			{From: "PENDING", To: []string{"RETRY"}},
			{From: "RETRY", To: []string{"REJECTED", "APPROVED"}, ToNull: true},
		},
		Terminal: []string{"REJECTED", "APPROVED"},
		FromNull: []string{"RETRY", "PENDING"},
	}}
	if !reflect.DeepEqual(td.StateMachines, want) {
		t.Errorf("StateMachines = %s", describeMachines(td.StateMachines))
	}

	// A second machine joins the first, and one with no move and no
	// terminal state holds neither list.
	if err := applyStateMachine(t, td, map[string]any{"field": "state", "transitions": map[string]any{}, "terminal": []any{}}); err != nil {
		t.Fatal(err)
	}
	if len(td.StateMachines) != 2 || !reflect.DeepEqual(td.StateMachines[1], &ir.StateMachine{Field: "state"}) {
		t.Errorf("StateMachines = %s", describeMachines(td.StateMachines))
	}
}

func describeMachines(machines []*ir.StateMachine) string {
	var b strings.Builder
	for _, m := range machines {
		b.WriteString(m.Field + "{")
		for _, tr := range m.Transitions {
			b.WriteString(tr.From + "->" + strings.Join(tr.To, ","))
			if tr.ToNull {
				b.WriteString(",null")
			}
			b.WriteString(";")
		}
		b.WriteString(" terminal=" + strings.Join(m.Terminal, ",") + " fromNull=" + strings.Join(m.FromNull, ",") + "} ")
	}
	return b.String()
}

// TestStateMachineApplyRefusesAMalformedDeclaration: each shape Apply
// refuses, with the type and the field once it knows it, and what to
// write (D118). An empty fromNull is refused here, since the IR's JSON
// would leave it out.
func TestStateMachineApplyRefusesAMalformedDeclaration(t *testing.T) {
	valid := func() map[string]any {
		return map[string]any{
			"field":       "state",
			"transitions": map[string]any{"open": []any{"closed"}},
			"terminal":    []any{"closed"},
		}
	}
	with := func(key string, value any) []any {
		obj := valid()
		obj[key] = value
		return []any{obj}
	}
	without := func(key string) []any {
		obj := valid()
		delete(obj, key)
		return []any{obj}
	}
	for _, tc := range []struct {
		name string
		args []any
		want string
	}{
		{"no argument", nil, "@stateMachine on Ticket takes one options object: @stateMachine<T>({ field, transitions, terminal })"},
		{"two arguments", []any{valid(), valid()}, "@stateMachine on Ticket takes one options object"},
		{"a string", []any{"state"}, "@stateMachine on Ticket takes an options object"},
		{"no field", without("field"), `@stateMachine on Ticket names no field; name the enum field whose moves it declares, as field: "status"`},
		{"a field that is no name", with("field", 3.0), "@stateMachine on Ticket names no field"},
		{"an unknown option", with("initial", "open"), `@stateMachine on Ticket.state has no option "initial"; it takes field, transitions, terminal and fromNull`},
		{"no transitions", without("transitions"), "@stateMachine on Ticket.state: transitions maps each state to the states it may move to"},
		{"transitions that are a list", with("transitions", []any{"open"}), "@stateMachine on Ticket.state: transitions maps each state to the states it may move to"},
		{"a state's transitions that are no list", with("transitions", map[string]any{"open": "closed"}), `@stateMachine on Ticket.state: transitions["open"] is a list of members of the field's enum, as [Status.Closed]; got closed`},
		{"a target that is a number", with("transitions", map[string]any{"open": []any{2.0}}), `@stateMachine on Ticket.state: transitions["open"] lists 2; a state is a member of the field's enum, whose value is a string`},
		{"null twice", with("transitions", map[string]any{"open": []any{nil, "closed", nil}}), `@stateMachine on Ticket.state: transitions["open"] lists null twice; list it once`},
		{"no terminal", without("terminal"), "@stateMachine on Ticket.state: terminal lists the states no update leaves, and is [] when there is none"},
		{"a null terminal state", with("terminal", []any{nil}), "@stateMachine on Ticket.state: terminal lists null, which is no state"},
		{"a null in fromNull", with("fromNull", []any{"open", nil}), "@stateMachine on Ticket.state: fromNull lists null, which is no state"},
		{"an empty fromNull", with("fromNull", []any{}), "@stateMachine on Ticket.state: fromNull is empty, so a null field could take no state; list the states a null may take, or leave fromNull out"},
		{"a fromNull that is no list", with("fromNull", true), "@stateMachine on Ticket.state: fromNull is a list of members of the field's enum, as [Status.Closed]; got true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			td := &ir.TypeDef{Name: "Ticket"}
			err := applyStateMachine(t, td, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Apply = %v, want an error containing %q", err, tc.want)
			}
			if len(td.StateMachines) != 0 {
				t.Errorf("a refused declaration was recorded: %s", describeMachines(td.StateMachines))
			}
			// An error of the options object points at it.
			var argErr *ArgError
			if len(tc.args) == 1 && (!errors.As(err, &argErr) || argErr.Index != 0) {
				t.Errorf("Apply = %T, want an ArgError at the options object", err)
			}
		})
	}
}
