package rustgen

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/rustutil"
	ir "github.com/parable-work/superschematic/ir"
)

// StateMachinesFile is the generated module that holds a schema's state
// machines (D117), under src/.
const StateMachinesFile = "state_machines.rs"

// StateMachineInfo is one @stateMachine of a type as src/state_machines.rs
// writes it (D117): the names of its constant and its helper, the field's
// enum, and the helper's match. A state is written as the enum's variant,
// `TicketState::InProgress`, which the IR holds by its serialized value.
type StateMachineInfo struct {
	// TypeName and Field are the type and the field the machine governs,
	// as the schema names them.
	TypeName string
	Field    string

	// Enum is the field's enum, local or a dependency's, as the crate
	// names it.
	Enum string

	// Nullable is whether the field may be null: the helper then takes
	// `Option<Enum>`.
	Nullable bool

	// TerminalConst and CanTransitionFunc are the items' names:
	// <TYPE>_<FIELD>_TERMINAL and <type>_can_transition_<field>.
	TerminalConst     string
	CanTransitionFunc string

	// Terminal are the states no move leaves, in the order written.
	Terminal []string

	// Arms are the arms of the helper's match on the state a move leaves,
	// every state in one: the null first on a nullable machine, then each
	// state with a move to another in the enum's order, then the states
	// with none together. Nil when no state has a move to another, and the
	// helper passes an unchanged value alone.
	Arms []StateMachineArm
}

// StateMachineArm is one arm of a helper's match.
type StateMachineArm struct {
	// Pattern matches the states the move leaves.
	Pattern string

	// Admits is whether `to` is a state they may move to, which is another
	// state by then.
	Admits string
}

// StateType is the type of the helper's arguments.
func (m StateMachineInfo) StateType() string {
	if m.Nullable {
		return "Option<" + m.Enum + ">"
	}
	return m.Enum
}

// StateMachineImports returns the use declarations of src/state_machines.rs:
// the schema's own enums from the enums module, and a dependency's by the
// alias the types module gives it, as a field of its type names it.
func (o *ModuleOutput) StateMachineImports() []string {
	local := map[string]bool{}
	for _, enum := range o.Enums {
		local[enum.Name] = true
	}
	var own, imported []string
	for _, machine := range o.StateMachines {
		list := &imported
		if local[machine.Enum] {
			list = &own
		}
		if !slices.Contains(*list, machine.Enum) {
			*list = append(*list, machine.Enum)
		}
	}
	var lines []string
	if len(own) > 0 {
		lines = append(lines, rustutil.UseLine("crate::enums", own))
	}
	if len(imported) > 0 {
		lines = append(lines, rustutil.UseLine("crate::types", imported))
	}
	return lines
}

// stateMachines builds the schema's machines for src/state_machines.rs
// (D117): every type's, a table's or not, by type name and then in the
// order declared. enums are the schema's enums and the ones it imports
// from its dependencies, which src/types.rs aliases.
func stateMachines(schema *ir.Schema, enums []codegen.EnumInfo) ([]StateMachineInfo, error) {
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
			info, err := stateMachine(schema, enums, typeDef, machine)
			if err != nil {
				return nil, fmt.Errorf("rustgen: @stateMachine on %s.%s: %w", typeDef.Name, machine.Field, err)
			}
			machines = append(machines, info)
		}
	}
	return machines, nil
}

func stateMachine(schema *ir.Schema, enums []codegen.EnumInfo, typeDef *ir.TypeDef, machine *ir.StateMachine) (StateMachineInfo, error) {
	var field *ir.FieldDef
	for _, f := range typeDef.Fields {
		if f != nil && f.Name == machine.Field {
			field = f
		}
	}
	if field == nil {
		return StateMachineInfo{}, fmt.Errorf("%s has no field %s", typeDef.Name, machine.Field)
	}
	var enum *codegen.EnumInfo
	for i := range enums {
		if enums[i].Name == field.TypeRef.Name {
			enum = &enums[i]
			break
		}
	}
	if enum == nil {
		return StateMachineInfo{}, fmt.Errorf("%s is no enum of %s or of a dependency it imports", field.TypeRef.Name, schema.Name)
	}
	variant := func(state string) (string, error) {
		for _, value := range enum.Values {
			if value.Value == state {
				return enum.Name + "::" + toRustEnumVariant(value.Name), nil
			}
		}
		return "", fmt.Errorf("%q is no member of %s", state, enum.Name)
	}
	variants := func(states []string) ([]string, error) {
		out := make([]string, 0, len(states))
		for _, state := range states {
			path, err := variant(state)
			if err != nil {
				return nil, err
			}
			out = append(out, path)
		}
		return out, nil
	}

	typeSnake, fieldSnake := codegen.ToSnakeCase(typeDef.Name), codegen.ToSnakeCase(machine.Field)
	info := StateMachineInfo{
		TypeName:          typeDef.Name,
		Field:             machine.Field,
		Enum:              enum.Name,
		Nullable:          !field.Required,
		TerminalConst:     strings.ToUpper(typeSnake + "_" + fieldSnake + "_TERMINAL"),
		CanTransitionFunc: typeSnake + "_can_transition_" + fieldSnake,
	}

	var err error
	if info.Terminal, err = variants(machine.Terminal); err != nil {
		return StateMachineInfo{}, err
	}
	fromNull, err := variants(machine.FromNull)
	if err != nil {
		return StateMachineInfo{}, err
	}

	transitions := map[string]*ir.StateTransition{}
	for _, tr := range machine.Transitions {
		if tr == nil {
			continue
		}
		if _, err := variant(tr.From); err != nil {
			return StateMachineInfo{}, err
		}
		transitions[tr.From] = tr
	}

	// The arms: a null's first, then each state's with a move to another,
	// then one for the states with none, the null among them when no
	// state follows it.
	var moving []StateMachineArm
	var resting []string
	moves := false
	for _, value := range enum.Values {
		from := enum.Name + "::" + toRustEnumVariant(value.Name)
		admits := ""
		// No move leaves a terminal state, which the build gives no
		// transition, and only a nullable field returns to null.
		if tr := transitions[value.Value]; tr != nil && !slices.Contains(machine.Terminal, tr.From) {
			to, err := variants(tr.To)
			if err != nil {
				return StateMachineInfo{}, err
			}
			// A move to the same state is no move, which the helper
			// passes before it reads the state.
			var others []string
			for _, state := range to {
				if state != from {
					others = append(others, state)
				}
			}
			admits = stateCondition(others, info.Nullable, tr.ToNull && info.Nullable)
		}
		if admits == "" {
			resting = append(resting, from)
			continue
		}
		moves = true
		moving = append(moving, StateMachineArm{Pattern: statePattern([]string{from}, info.Nullable, false), Admits: admits})
	}
	nullAdmits := ""
	if info.Nullable {
		if nullAdmits = stateCondition(fromNull, true, false); nullAdmits != "" {
			moves = true
			info.Arms = append(info.Arms, StateMachineArm{Pattern: "None", Admits: nullAdmits})
		}
	}
	if !moves {
		return info, nil
	}
	info.Arms = append(info.Arms, moving...)
	if nullRests := info.Nullable && nullAdmits == ""; len(resting) > 0 || nullRests {
		info.Arms = append(info.Arms, StateMachineArm{Pattern: statePattern(resting, info.Nullable, nullRests), Admits: "false"})
	}
	return info, nil
}

// statePattern is the pattern that matches a state that is one of
// variants, or null when withNull: `A | B` on a required field, and
// `Some(A | B)`, `None` or `None | Some(A | B)` on a nullable one.
func statePattern(variants []string, nullable, withNull bool) string {
	if !nullable {
		return strings.Join(variants, " | ")
	}
	var alternatives []string
	if withNull {
		alternatives = append(alternatives, "None")
	}
	if len(variants) > 0 {
		alternatives = append(alternatives, "Some("+strings.Join(variants, " | ")+")")
	}
	return strings.Join(alternatives, " | ")
}

// stateCondition is the Rust condition that `to` is one of variants, or
// null when toNull, or "" when it can be neither: an equality for one
// state, `is_none` for null alone, and `matches!` for more.
func stateCondition(variants []string, nullable, toNull bool) string {
	switch {
	case len(variants) == 0 && !toNull:
		return ""
	case len(variants) == 0:
		return "to.is_none()"
	case len(variants) == 1 && !toNull && nullable:
		return "to == Some(" + variants[0] + ")"
	case len(variants) == 1 && !toNull:
		return "to == " + variants[0]
	}
	return "matches!(to, " + statePattern(variants, nullable, toNull) + ")"
}
