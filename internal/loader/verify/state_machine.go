package verify

import (
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// checkStateMachines checks a schema's @stateMachine declarations (D118)
// against the field each governs and the enum that types it, in every
// form: the type has the field, and one enum types it, the schema's own or
// a dependency's (Input.ExternalEnums); every state is a member, by the
// value it serializes to; a null start and a return to null are a
// nullable field's; and every member can be reached from the field's
// default, or from null when it has none and the machine has a fromNull.
// A field with neither is not walked: its rows are born in any state.
//
// In a DB service it also refuses the declarations no guard could hold:
// one on an engine type, whose instances no trigger watches, one on a
// @queue class, and one on a field a behavior owns, whose moves are the
// behavior's. A class that is no table keeps its machine, and the types'
// helpers with it.
//
// What a declaration decides alone (one machine per field, one entry per
// state, no transition out of a terminal state) is ir.Schema.Validate's.
func checkStateMachines(schema *ir.Schema, in Input, r *Result) {
	for _, name := range sortedTypeNames(schema.Types) {
		td := schema.Types[name]
		if td == nil {
			continue
		}
		for _, machine := range td.StateMachines {
			if machine != nil {
				checkStateMachine(schema, in, td, machine, r)
			}
		}
	}
}

func checkStateMachine(schema *ir.Schema, in Input, td *ir.TypeDef, machine *ir.StateMachine, r *Result) {
	where := fmt.Sprintf("@stateMachine on %s.%s", td.Name, machine.Field)
	fail := func(format string, args ...any) {
		r.errorf(td.Owner, "%s: %s", where, fmt.Sprintf(format, args...))
	}
	if schema.Kind == ir.SchemaKindDB {
		switch {
		case codegen.IsEngineType(td):
			fail("%s composes a behavior with @behavior, so the engine keeps its instances in tables of its own, which no guard watches; declare the machine on a table", td.Name)
			return
		case td.Queue != nil:
			fail("%s is a @queue class, whose rows are messages the queue's workers move; declare the machine on a table", td.Name)
			return
		}
	}
	field := fieldNamed(td, machine.Field)
	// A field a behavior owns joins the table before this pass; when the
	// behaviors' own checks fail it does not, and its use still names it.
	owner := ""
	if field != nil && field.Behavior != nil {
		owner = field.Behavior.Behavior
	} else if field == nil {
		for _, use := range td.FieldBehaviors() {
			if use.Field == machine.Field {
				owner = use.Name
			}
		}
	}
	switch {
	case owner != "":
		fail("%s is a field the %s behavior owns, so its moves are the behavior's; declare them in its config, and take this declaration off", machine.Field, owner)
		return
	case field == nil:
		fail("%s has no field %s; name the enum field whose moves the machine declares", td.Name, machine.Field)
		return
	}
	enum := schema.Enums[field.TypeRef.Name]
	if enum == nil {
		enum = in.ExternalEnums[field.TypeRef.Name]
	}
	switch {
	case field.TypeRef.IsArray || field.TypeRef.IsMap:
		fail("%s is %s, and a machine governs a field that holds one state; type it by the enum itself, as E, Nullable<E> or Default<E, V>", machine.Field, typeName(field.TypeRef))
		return
	case enum == nil && importsSymbol(schema, field.TypeRef.Name):
		// A TypeScript schema's reader hands over each dependency's enum
		// a field uses; the data forms name the type and cannot read it.
		fail("%s is typed by %s, which %s imports and the build does not read as an enum; a machine governs a field an enum types, and a dependency's enum is read from a TypeScript schema alone, so in a JSON or YAML schema file type the field by an enum of %s", machine.Field, field.TypeRef.Name, schema.Name, schema.Name)
		return
	case enum == nil:
		fail("%s is typed by %s, which is no enum; a machine governs a field an enum types, as E, Nullable<E> or Default<E, V>", machine.Field, field.TypeRef.Name)
		return
	}

	sound := true
	// member reports a state that is no member of the enum, once.
	reported := map[string]bool{}
	member := func(state, role string) {
		if enum.MemberSerializedAs(state) != nil || reported[state] {
			return
		}
		reported[state] = true
		sound = false
		fail("%s names %q, which is no member of %s; a state is a member's value, one of %s", role, state, enum.Name, memberValues(enum))
	}
	for _, state := range machine.Terminal {
		member(state, "terminal")
	}
	for _, state := range machine.FromNull {
		member(state, "fromNull")
	}
	if len(machine.FromNull) > 0 && field.Required {
		sound = false
		fail("fromNull lists the states a null takes, and %s is required, so it is never null; type it Nullable<%s>, or leave fromNull out", machine.Field, enum.Name)
	}
	for _, tr := range machine.Transitions {
		if tr == nil {
			continue
		}
		member(tr.From, "transitions")
		for _, state := range tr.To {
			member(state, fmt.Sprintf("the transition from %q", tr.From))
		}
		if tr.ToNull && field.Required {
			sound = false
			fail("%q returns the field to null, and %s is required; type it Nullable<%s>, or take null out of the state's transitions", tr.From, machine.Field, enum.Name)
		}
	}
	if !sound {
		return
	}

	// Every member is one a row can come to hold: the walk starts where
	// the schema says a row starts, at the field's default, or at null,
	// which the machine leaves by fromNull.
	var start []string
	from := ""
	switch {
	case field.Default != nil:
		if enum.MemberSerializedAs(*field.Default) == nil {
			// The loader's check of every default has refused it.
			return
		}
		start, from = []string{*field.Default}, fmt.Sprintf("%s's default, %q", machine.Field, *field.Default)
	case len(machine.FromNull) > 0:
		start, from = machine.FromNull, "null, which fromNull leaves"
	default:
		return
	}
	moves := map[string]*ir.StateTransition{}
	for _, tr := range machine.Transitions {
		if tr != nil {
			moves[tr.From] = tr
		}
	}
	reached := map[string]bool{}
	throughNull := field.Default == nil
	queue := slices.Clone(start)
	for len(queue) > 0 {
		state := queue[0]
		queue = queue[1:]
		if reached[state] {
			continue
		}
		reached[state] = true
		tr := moves[state]
		if tr == nil {
			continue
		}
		queue = append(queue, tr.To...)
		// A return to null reaches what a null may take.
		if tr.ToNull && !throughNull {
			throughNull = true
			queue = append(queue, machine.FromNull...)
		}
	}
	for _, value := range enum.Values {
		if state := value.Serialized(); !reached[state] {
			fail("no move reaches %q from %s, so no update could put a row there; list a move to it, or take the member out of %s", state, from, enum.Name)
		}
	}
}

// importsSymbol reports whether schema imports a symbol named name from a
// dependency.
func importsSymbol(schema *ir.Schema, name string) bool {
	for _, imp := range schema.Imports {
		if slices.Contains(imp.Types, name) {
			return true
		}
	}
	return false
}

// memberValues lists an enum's members by the values they serialize to,
// quoted, for an error.
func memberValues(enum *ir.EnumDef) string {
	values := make([]string, len(enum.Values))
	for i, v := range enum.Values {
		values[i] = fmt.Sprintf("%q", v.Serialized())
	}
	return strings.Join(values, ", ")
}
