package pygen

import (
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// StateMachineInfo is one @stateMachine declaration as types.py writes it
// on the type's model (D118): <Const>_TRANSITIONS, <Const>_TERMINAL and
// the classmethod Method. Every state is a member of the field's enum,
// which the module imports from enums.py, or from the package of the
// dependency that owns it, as a field of its type names it.
type StateMachineInfo struct {
	// FieldName is the field the machine governs, as the schema names it.
	FieldName string

	// Const is the field's name in upper snake case, which the two
	// constants begin with, and Method the classmethod's name.
	Const  string
	Method string

	// EnumType is the field's enum, and StateType the annotation of a
	// state: the enum, Optional when the field may be null.
	EnumType  string
	StateType string

	// Nullable makes None, which is null, a state.
	Nullable bool

	// Transitions are the entries of <Const>_TRANSITIONS: None first when
	// a null may take a state, then each state that has a move, in the
	// order of the enum's members.
	Transitions []StateMoves

	// Terminal is the frozenset of the states no update leaves.
	Terminal string
}

// StateMoves is one entry of a machine's transitions: a state, or None,
// and the frozenset of the states it may move to, None among them when it
// may return to null.
type StateMoves struct {
	From string
	To   string
}

// HasStateMachines reports whether a model of types.py carries a state
// machine, which the module then imports ClassVar, Mapping and
// MappingProxyType for.
func (o *ModuleOutput) HasStateMachines() bool {
	return len(o.stateMachines) > 0
}

// StateMachinesOf returns the state machines of the model named typeName,
// in the order the type declares them.
func (o *ModuleOutput) StateMachinesOf(typeName string) []StateMachineInfo {
	return o.stateMachines[typeName]
}

// buildStateMachines gives each model of output the state machines its
// type declares (D118). A type the module writes no model for has none to
// carry. A machine on a dependency's enum names the enum's members, so the
// import of the dependency's package loses its Any fallback
// (TypeImport.Required).
//
// It fails when a machine's constants or its method would share a name
// with a field of the model or with another machine's.
func buildStateMachines(schema *ir.Schema, opts Options, output *ModuleOutput) error {
	machines, err := codegen.ExtractStateMachines(schema, opts.Dependencies)
	if err != nil {
		return err
	}
	models := make(map[string]codegen.TypeInfo, len(output.Types))
	for _, typeInfo := range output.Types {
		models[typeInfo.Name] = typeInfo
	}
	taken := map[string]map[string]string{}
	required := map[string]bool{}
	for _, machine := range machines {
		model, ok := models[machine.TypeName]
		if !ok {
			continue
		}
		names, ok := taken[model.Name]
		if !ok {
			names = map[string]string{}
			for _, field := range model.Fields {
				names[field.TargetName] = "the field " + field.Name
			}
			taken[model.Name] = names
		}

		member := func(value codegen.EnumValueInfo) string {
			return machine.EnumName + "." + value.Name
		}
		stem := strings.Trim(codegen.ToSnakeCase(machine.FieldName), "_")
		info := StateMachineInfo{
			FieldName: machine.FieldName,
			Const:     strings.ToUpper(stem),
			Method:    "can_transition_" + stem,
			EnumType:  machine.EnumName,
			StateType: machine.EnumName,
			Nullable:  machine.Nullable,
			Terminal:  pythonFrozenSet(machine.Terminal, member, false),
		}
		if machine.Nullable {
			info.StateType = "Optional[" + machine.EnumName + "]"
		}
		if len(machine.FromNull) > 0 {
			info.Transitions = append(info.Transitions, StateMoves{From: "None", To: pythonFrozenSet(machine.FromNull, member, false)})
		}
		for _, transition := range machine.Transitions {
			info.Transitions = append(info.Transitions, StateMoves{From: member(transition.From), To: pythonFrozenSet(transition.To, member, transition.ToNull)})
		}

		where := fmt.Sprintf("the state machine on %s.%s", machine.TypeName, machine.FieldName)
		for _, name := range []string{info.Const + "_TRANSITIONS", info.Const + "_TERMINAL", info.Method} {
			if other, clash := names[name]; clash {
				return fmt.Errorf("pygen: %s needs the name %s on the model %s, which is also the name of %s; rename one of the two fields", where, name, model.Name, other)
			}
			names[name] = where
		}

		if output.stateMachines == nil {
			output.stateMachines = map[string][]StateMachineInfo{}
		}
		output.stateMachines[model.Name] = append(output.stateMachines[model.Name], info)
		if machine.EnumOwner != "" {
			required[opts.Naming.PythonTypesModule(moduleStem(machine.EnumOwner))] = true
		}
	}
	for i := range output.TypeImports {
		if required[output.TypeImports[i].ModuleName] {
			output.TypeImports[i].Required = true
		}
	}
	return nil
}

// pythonFrozenSet writes the frozenset of the members of values, each
// named by member, with None after them when withNone is set.
func pythonFrozenSet(values []codegen.EnumValueInfo, member func(codegen.EnumValueInfo) string, withNone bool) string {
	items := make([]string, 0, len(values)+1)
	for _, value := range values {
		items = append(items, member(value))
	}
	if withNone {
		items = append(items, "None")
	}
	if len(items) == 0 {
		return "frozenset()"
	}
	return "frozenset({" + strings.Join(items, ", ") + "})"
}
