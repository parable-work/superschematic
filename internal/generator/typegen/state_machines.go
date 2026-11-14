package typegen

import (
	"fmt"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// StateMachineInfo is one @stateMachine declaration as state_machines.go
// writes its helpers (D118): <Type><Field>Terminal and
// <Type>CanTransition<Field>. Every state is the Go constant of the
// enum's member, which enums.go declares for the schema's own enum and
// aliases for a dependency's.
type StateMachineInfo struct {
	// TypeName and FieldName are the type and the field the machine
	// governs, as the schema names them.
	TypeName  string
	FieldName string

	// GoField is the field's Go name, as the type's struct has it.
	GoField string

	// EnumType is the Go type of a state: the field's enum, by the name
	// the field's type has in this package.
	EnumType string

	// Nullable makes the helper take *EnumType, where nil is null.
	Nullable bool

	// Terminal are the states no update leaves, in the order written.
	Terminal []string

	// FromNull are the states a null may take, and ToNull the states that
	// may return to null. Only a nullable machine has them.
	FromNull []string
	ToNull   []string

	// Moves are the states that may move to another state, in the order
	// of the enum's members.
	Moves []StateMoves
}

// StateMoves is one state of a machine and the states it may move to,
// null apart.
type StateMoves struct {
	From string
	To   []string
}

// TerminalFunc is the name of the function that returns the machine's
// terminal states.
func (m StateMachineInfo) TerminalFunc() string {
	return m.TypeName + m.GoField + "Terminal"
}

// CanTransitionFunc is the name of the function that reports whether the
// machine allows a move.
func (m StateMachineInfo) CanTransitionFunc() string {
	return m.TypeName + "CanTransition" + m.GoField
}

// HasStateMachines reports whether the module writes state_machines.go: a
// type of the schema declares a @stateMachine.
func (o *ModuleOutput) HasStateMachines() bool {
	return len(o.StateMachines) > 0
}

// buildStateMachines returns the helpers of every @stateMachine the
// schema declares (D118), the types in name order. A type the module
// writes no struct for, a base among them, keeps its helpers: they name
// the field's enum alone. A dependency's enum is named as a field of its
// type names it, by the alias and the constants enums.go binds to the
// dependency's module.
//
// It fails when two helpers would share a name, or one would take the
// name of a type, an enum or a union of the package, which Go would
// refuse to compile.
func buildStateMachines(schema *ir.Schema, deps map[string]*ir.Schema, output *ModuleOutput) ([]StateMachineInfo, error) {
	machines, err := codegen.ExtractStateMachines(schema, deps)
	if err != nil {
		return nil, err
	}
	if len(machines) == 0 {
		return nil, nil
	}

	declared := map[string]string{}
	for _, t := range output.Types {
		declared[t.Name] = "the type " + t.Name
	}
	for _, t := range output.ImportedTypes {
		declared[t.Name] = "the type " + t.Name
	}
	for _, e := range output.Enums {
		declared[e.Name] = "the enum " + e.Name
	}
	for _, e := range output.ImportedEnums {
		declared[e.Name] = "the enum " + e.Name
	}
	for _, u := range output.Unions {
		declared[u.Name] = "the union " + u.Name
	}
	for _, u := range output.ImportedUnions {
		declared[u.Name] = "the union " + u.Name
	}

	infos := make([]StateMachineInfo, 0, len(machines))
	for _, machine := range machines {
		constant := func(value codegen.EnumValueInfo) string {
			return machine.EnumName + "_" + value.Name
		}
		constants := func(values []codegen.EnumValueInfo) []string {
			names := make([]string, 0, len(values))
			for _, value := range values {
				names = append(names, constant(value))
			}
			return names
		}
		info := StateMachineInfo{
			TypeName:  machine.TypeName,
			FieldName: machine.FieldName,
			GoField:   codegen.ToPascalCase(machine.FieldName),
			EnumType:  machine.EnumName,
			Nullable:  machine.Nullable,
			Terminal:  constants(machine.Terminal),
			FromNull:  constants(machine.FromNull),
		}
		for _, transition := range machine.Transitions {
			if transition.ToNull {
				info.ToNull = append(info.ToNull, constant(transition.From))
			}
			if len(transition.To) > 0 {
				info.Moves = append(info.Moves, StateMoves{From: constant(transition.From), To: constants(transition.To)})
			}
		}
		where := fmt.Sprintf("the state machine on %s.%s", info.TypeName, info.FieldName)
		for _, name := range []string{info.TerminalFunc(), info.CanTransitionFunc()} {
			if other, taken := declared[name]; taken {
				return nil, fmt.Errorf("typegen: %s needs the Go function %s, which is also the name of %s; rename the field or the other", where, name, other)
			}
			declared[name] = where
		}
		infos = append(infos, info)
	}
	return infos, nil
}
