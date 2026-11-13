package ir

import (
	"fmt"
	"slices"
	"sort"
)

// StateMachine is a `@stateMachine` declaration of a type (D117): the
// moves one enum field of the type may make. Every state is the enum
// member's serialized value, which is what a column of the field holds
// and what FieldDef.Default carries, so a generator writes a state as it
// stands in SQL and maps it to the member's name in a language's types.
//
// An unchanged value is no move: a guard and a helper pass it whether the
// machine lists it or not. An insert is not held: a row is born in the
// state its writer gives it.
//
// In TypeScript: @stateMachine<T>({ field, transitions, terminal, fromNull? })
type StateMachine struct {
	// Field is the field of the type the machine governs: one typed by an
	// enum, required, nullable or with a default.
	Field string `json:"field" yaml:"field"`

	// Transitions are the states that have a move and where each may go,
	// one entry per state, in the order of the enum's members, which the
	// loader gives them in every authoring form
	// ([StateMachine.OrderTransitions]). A state with no entry moves
	// nowhere.
	Transitions []*StateTransition `json:"transitions,omitempty" yaml:"transitions,omitempty"`

	// Terminal are the states no update leaves, in the order written.
	Terminal []string `json:"terminal,omitempty" yaml:"terminal,omitempty"`

	// FromNull are the states a null field may take, in the order written.
	// Only a nullable field has them. Without them an update cannot set a
	// null field, so no transition returns to null either.
	FromNull []string `json:"fromNull,omitempty" yaml:"fromNull,omitempty" jsonschema:"minItems=1"`
}

// StateTransition is one state of a StateMachine and the states it may
// move to.
type StateTransition struct {
	// From is the state the moves leave.
	From string `json:"from" yaml:"from"`

	// To are the states From may move to, in the order written.
	To []string `json:"to,omitempty" yaml:"to,omitempty"`

	// ToNull lets the field return to null from From, which TypeScript
	// writes as null in the state's list. Only a nullable field whose
	// machine has FromNull takes it.
	ToNull bool `json:"toNull,omitempty" yaml:"toNull,omitempty"`
}

// Serialized is the value the member is stored and sent as: SerializedAs
// when the member has one, else its Name.
func (v EnumValueDef) Serialized() string {
	if v.SerializedAs != "" {
		return v.SerializedAs
	}
	return v.Name
}

// MemberSerializedAs returns the member of the enum whose serialized value
// is value, or nil when it has none.
func (e *EnumDef) MemberSerializedAs(value string) *EnumValueDef {
	if e == nil {
		return nil
	}
	for i := range e.Values {
		if e.Values[i].Serialized() == value {
			return &e.Values[i]
		}
	}
	return nil
}

// StateMachineOf returns td's machine on the field named field, or nil
// when the type declares none on it.
func (td *TypeDef) StateMachineOf(field string) *StateMachine {
	if td == nil {
		return nil
	}
	for _, m := range td.StateMachines {
		if m != nil && m.Field == field {
			return m
		}
	}
	return nil
}

// OrderTransitions puts the machine's transitions in the order of enum's
// members (D117), so the IR, and every guard and helper written from it,
// is the same whatever order the author's form kept: a TypeScript object's
// keys reach a decorator in no order. A transition from a state that is
// no member, which verification refuses, follows the members' and keeps
// its place among its like. The To, Terminal and FromNull lists keep the
// order written.
func (m *StateMachine) OrderTransitions(enum *EnumDef) {
	if m == nil || enum == nil {
		return
	}
	place := func(tr *StateTransition) int {
		if tr != nil {
			for i := range enum.Values {
				if enum.Values[i].Serialized() == tr.From {
					return i
				}
			}
		}
		return len(enum.Values)
	}
	sort.SliceStable(m.Transitions, func(i, j int) bool {
		return place(m.Transitions[i]) < place(m.Transitions[j])
	})
}

// validateStateMachines checks td's @stateMachine declarations (D117) for
// what the declarations alone decide, in every authoring form: one machine
// per field, one entry per state, each move once, no transition out of a
// terminal state, no entry without a target, no empty fromNull, and no
// return to null without one. Each error names the type, the field and
// the state. What needs the field and its enum (that the type has the
// field, that every state is a member, that a null is the field's to
// take, and that every state can be reached) is the loader's verification
// pass's to check.
func (s *Schema) validateStateMachines(td *TypeDef) []error {
	var errs []error
	seen := map[string]bool{}
	for i, m := range td.StateMachines {
		switch {
		case m == nil:
			errs = append(errs, fmt.Errorf("@stateMachine %d on %s is null; write { field, transitions, terminal }", i, td.Name))
		case m.Field == "":
			errs = append(errs, fmt.Errorf("@stateMachine %d on %s names no field; name the enum field whose moves it declares", i, td.Name))
		case seen[m.Field]:
			errs = append(errs, fmt.Errorf("@stateMachine on %s.%s: %s declares two state machines on %s; a field takes one, so write its moves in one declaration", td.Name, m.Field, td.Name, m.Field))
		default:
			seen[m.Field] = true
			errs = append(errs, validateStateMachine(td, m)...)
		}
	}
	return errs
}

func validateStateMachine(td *TypeDef, m *StateMachine) []error {
	var errs []error
	fail := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("@stateMachine on %s.%s: %s", td.Name, m.Field, fmt.Sprintf(format, args...)))
	}
	// once reports a state a list names twice, or an empty one.
	once := func(list []string, name string) {
		seen := map[string]bool{}
		for _, state := range list {
			switch {
			case state == "":
				fail("%s names an empty state; name a member of the field's enum", name)
			case seen[state]:
				fail("%s lists %q twice; list each state once", name, state)
			}
			seen[state] = true
		}
	}
	once(m.Terminal, "terminal")
	// An empty fromNull admits nothing, and the IR's JSON leaves it out,
	// so a schema read back would hold a machine with no null start.
	if m.FromNull != nil && len(m.FromNull) == 0 {
		fail("fromNull is empty, so a null field could take no state; list the states a null may take, or leave fromNull out")
	}
	once(m.FromNull, "fromNull")

	from := map[string]bool{}
	for i, tr := range m.Transitions {
		switch {
		case tr == nil:
			fail("transition %d is null; write { from, to }", i)
			continue
		case tr.From == "":
			fail("transition %d names no state to move from; name a member of the field's enum", i)
			continue
		case from[tr.From]:
			fail("transitions has two entries for %q; write every move from %q in one entry", tr.From, tr.From)
			continue
		}
		from[tr.From] = true
		if slices.Contains(m.Terminal, tr.From) {
			fail("%q is terminal and has a transition; take it out of terminal, or drop its transitions", tr.From)
		}
		if len(tr.To) == 0 && !tr.ToNull {
			fail("the transition from %q names no target; list the states it moves to, or drop the entry", tr.From)
		}
		to := map[string]bool{}
		for _, state := range tr.To {
			switch {
			case state == "":
				fail("the transition from %q names an empty state; name a member of the field's enum", tr.From)
			case to[state]:
				fail("the move from %q to %q is listed twice; list it once", tr.From, state)
			}
			to[state] = true
		}
		if tr.ToNull && len(m.FromNull) == 0 {
			fail("%q returns the field to null, and the machine has no fromNull, so the row could not change again; list in fromNull the states a null may take", tr.From)
		}
	}
	return errs
}
