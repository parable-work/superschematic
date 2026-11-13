package tsgen

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// StateMachinesFile is the generated file that holds a schema's state
// machines (D117), beside index.ts.
const StateMachinesFile = "state-machines.ts"

// StateMachineInfo is one @stateMachine of a type as state-machines.ts
// writes it (D117): the names of its three exports, the field's enum, and
// every member of the enum with the moves the machine gives it. A state is
// written as the enum's member, `TicketState.InProgress`, which the IR
// holds by its serialized value.
type StateMachineInfo struct {
	// TypeName and Field are the type and the field the machine governs,
	// as the schema names them.
	TypeName string
	Field    string

	// Enum is the field's enum, local or a dependency's, as types/ exports
	// it.
	Enum string

	// Nullable is whether the field may be null: the helper then takes
	// `Enum | null`, and a transition's list may hold null.
	Nullable bool

	// TransitionsConst, TerminalConst and CanTransitionFunc are the
	// exports' names: <type><Field>Transitions, <type><Field>Terminal and
	// <type>CanTransition<Field>.
	TransitionsConst  string
	TerminalConst     string
	CanTransitionFunc string

	// States are the enum's members in its order, each with its moves. A
	// member the machine gives no transition moves nowhere.
	States []StateMachineStateInfo

	// Terminal are the states no move leaves, in the order written.
	Terminal []string

	// FromNull is the condition under which a null field may take the
	// state `to`, "false" when the machine has no fromNull. Only a
	// nullable machine reads it.
	FromNull string
}

// StateMachineStateInfo is one member of a machine's enum.
type StateMachineStateInfo struct {
	// Member is the member's expression, `TicketState.Open`.
	Member string

	// To are the states the member may move to as the machine lists them,
	// a move to itself among them, with null last where the transition
	// returns the field to null.
	To []string

	// Admits is the condition under which the member may move to the
	// state `to`, which is another state by then, or "" when it moves
	// nowhere.
	Admits string
}

// MovingStates are the members with a move to another state, each a case
// of the helper's switch.
func (m StateMachineInfo) MovingStates() []StateMachineStateInfo {
	var states []StateMachineStateInfo
	for _, state := range m.States {
		if state.Admits != "" {
			states = append(states, state)
		}
	}
	return states
}

// RestingStates are the members with no move to another state, which
// share one case of the helper's switch.
func (m StateMachineInfo) RestingStates() []StateMachineStateInfo {
	var states []StateMachineStateInfo
	for _, state := range m.States {
		if state.Admits == "" {
			states = append(states, state)
		}
	}
	return states
}

// StateMachineEnums returns the enums the schema's machines govern fields
// of, sorted, for the import at the top of state-machines.ts.
func (o *ModuleOutput) StateMachineEnums() []string {
	var names []string
	for _, machine := range o.StateMachines {
		if !slices.Contains(names, machine.Enum) {
			names = append(names, machine.Enum)
		}
	}
	sort.Strings(names)
	return names
}

// stateMachines builds the schema's machines for state-machines.ts (D117):
// every type's, a table's or not, by type name and then in the order
// declared. dependencyEnums are the enums the schema imports from its
// dependencies, by name, which types/types.ts exports beside the schema's
// own.
func stateMachines(schema *ir.Schema, dependencyEnums map[string]*ir.EnumDef) ([]StateMachineInfo, error) {
	names := make([]string, 0, len(schema.Types))
	for name, typeDef := range schema.Types {
		if typeDef != nil && len(typeDef.StateMachines) > 0 {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	var machines []StateMachineInfo
	for _, name := range names {
		typeDef := schema.Types[name]
		for _, machine := range typeDef.StateMachines {
			if machine == nil {
				continue
			}
			info, err := stateMachine(schema, dependencyEnums, typeDef, machine)
			if err != nil {
				return nil, fmt.Errorf("tsgen: @stateMachine on %s.%s: %w", typeDef.Name, machine.Field, err)
			}
			machines = append(machines, info)
		}
	}
	return machines, nil
}

func stateMachine(schema *ir.Schema, dependencyEnums map[string]*ir.EnumDef, typeDef *ir.TypeDef, machine *ir.StateMachine) (StateMachineInfo, error) {
	var field *ir.FieldDef
	for _, f := range typeDef.Fields {
		if f != nil && f.Name == machine.Field {
			field = f
		}
	}
	if field == nil {
		return StateMachineInfo{}, fmt.Errorf("%s has no field %s", typeDef.Name, machine.Field)
	}
	enum := schema.Enums[field.TypeRef.Name]
	if enum == nil {
		enum = dependencyEnums[field.TypeRef.Name]
	}
	if enum == nil {
		return StateMachineInfo{}, fmt.Errorf("%s is no enum of %s or of a dependency it imports", field.TypeRef.Name, schema.Name)
	}
	member := func(state string) (string, error) {
		value := enum.MemberSerializedAs(state)
		if value == nil {
			return "", fmt.Errorf("%q is no member of %s", state, enum.Name)
		}
		return enum.Name + "." + value.Name, nil
	}
	members := func(states []string) ([]string, error) {
		out := make([]string, 0, len(states))
		for _, state := range states {
			expr, err := member(state)
			if err != nil {
				return nil, err
			}
			out = append(out, expr)
		}
		return out, nil
	}

	prefix, suffix := lowerFirst(typeDef.Name), codegen.ToPascalCase(machine.Field)
	info := StateMachineInfo{
		TypeName:          typeDef.Name,
		Field:             machine.Field,
		Enum:              enum.Name,
		Nullable:          !field.Required,
		TransitionsConst:  prefix + suffix + "Transitions",
		TerminalConst:     prefix + suffix + "Terminal",
		CanTransitionFunc: prefix + "CanTransition" + suffix,
	}

	var err error
	if info.Terminal, err = members(machine.Terminal); err != nil {
		return StateMachineInfo{}, err
	}
	fromNull, err := members(machine.FromNull)
	if err != nil {
		return StateMachineInfo{}, err
	}
	info.FromNull = stateCondition(fromNull, false)
	if info.FromNull == "" {
		info.FromNull = "false"
	}

	transitions := map[string]*ir.StateTransition{}
	for _, tr := range machine.Transitions {
		if tr == nil {
			continue
		}
		if enum.MemberSerializedAs(tr.From) == nil {
			return StateMachineInfo{}, fmt.Errorf("%q is no member of %s", tr.From, enum.Name)
		}
		transitions[tr.From] = tr
	}
	for i := range enum.Values {
		value := &enum.Values[i]
		state := StateMachineStateInfo{Member: enum.Name + "." + value.Name}
		// No move leaves a terminal state, which the build gives no
		// transition, and only a nullable field returns to null.
		if tr := transitions[value.Serialized()]; tr != nil && !slices.Contains(machine.Terminal, tr.From) {
			if state.To, err = members(tr.To); err != nil {
				return StateMachineInfo{}, err
			}
			// A move to the same state is no move, which the helper
			// passes before it reads the state.
			var others []string
			for _, to := range state.To {
				if to != state.Member {
					others = append(others, to)
				}
			}
			toNull := tr.ToNull && info.Nullable
			state.Admits = stateCondition(others, toNull)
			if toNull {
				state.To = append(state.To, "null")
			}
		}
		info.States = append(info.States, state)
	}
	return info, nil
}

// stateCondition is the TypeScript condition that `to` is null, when
// toNull, or one of members, or "" when it can be neither.
func stateCondition(members []string, toNull bool) string {
	var terms []string
	if toNull {
		terms = append(terms, "to === null")
	}
	for _, member := range members {
		terms = append(terms, "to === "+member)
	}
	return strings.Join(terms, " || ")
}
