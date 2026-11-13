package codegen

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// StateMachineInfo is one @stateMachine declaration (D117) as a type
// generator writes its helpers: the type and the field it governs, the
// field's enum, and every state as the enum's member. The IR holds a state
// as the member's serialized value; a member's Name is what a language's
// types call it, and its Value is what the IR and the column hold.
//
// The rule every helper written from it follows, and every guard: an
// unchanged value is no move and is allowed, null to null among them;
// otherwise no move leaves a Terminal state; otherwise a move is allowed
// only when the machine lists it, which from null is a FromNull state and
// to null is a transition with ToNull.
type StateMachineInfo struct {
	// TypeName is the type that declares the machine.
	TypeName string

	// FieldName is the field the machine governs, as the schema names it.
	FieldName string

	// EnumName is the enum that types the field.
	EnumName string

	// EnumOwner is the dependency service that owns the enum, as
	// schema.Imports names it ("shop-db" for "@schemas/shop-db"). It is
	// empty when the schema itself owns the enum.
	EnumOwner string

	// Nullable reports whether the field may be null, so the helpers take
	// null as a state.
	Nullable bool

	// Transitions are the states that have a move, in the order of the
	// enum's members, which the loader gives them.
	Transitions []StateTransitionInfo

	// Terminal are the states no update leaves, in the order written.
	Terminal []EnumValueInfo

	// FromNull are the states a null field may take, in the order written.
	FromNull []EnumValueInfo
}

// StateTransitionInfo is one state of a StateMachineInfo and the states it
// may move to.
type StateTransitionInfo struct {
	// From is the state the moves leave.
	From EnumValueInfo

	// To are the states From may move to, in the order written.
	To []EnumValueInfo

	// ToNull lets the field return to null from From.
	ToNull bool
}

// ExtractStateMachines returns the schema's @stateMachine declarations
// (D117) with each field's enum resolved, the types in name order and a
// type's machines in the order it declares them. deps holds the loaded
// schema of every service the schema imports from, by service name: a
// field's enum may be a dependency's, which schema.Enums does not hold.
//
// The loader's verification pass refuses a machine whose field the type
// lacks, whose field no enum types, or whose state is no member; one that
// reaches a generator all the same is an error here, since no helper could
// name its states.
func ExtractStateMachines(schema *ir.Schema, deps map[string]*ir.Schema) ([]StateMachineInfo, error) {
	names := make([]string, 0, len(schema.Types))
	for name, td := range schema.Types {
		if td != nil && len(td.StateMachines) > 0 {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	var machines []StateMachineInfo
	for _, name := range names {
		td := schema.Types[name]
		for _, machine := range td.StateMachines {
			if machine == nil {
				continue
			}
			info, err := extractStateMachine(schema, deps, td, machine)
			if err != nil {
				return nil, err
			}
			machines = append(machines, info)
		}
	}
	return machines, nil
}

func extractStateMachine(schema *ir.Schema, deps map[string]*ir.Schema, td *ir.TypeDef, machine *ir.StateMachine) (StateMachineInfo, error) {
	where := fmt.Sprintf("@stateMachine on %s.%s", td.Name, machine.Field)
	var field *ir.FieldDef
	for _, candidate := range td.Fields {
		if candidate != nil && candidate.Name == machine.Field {
			field = candidate
			break
		}
	}
	if field == nil {
		return StateMachineInfo{}, fmt.Errorf("%s: %s has no field %s", where, td.Name, machine.Field)
	}
	enum, owner := stateMachineEnum(schema, deps, field.TypeRef.Name)
	if enum == nil || field.TypeRef.IsArray || field.TypeRef.IsMap {
		return StateMachineInfo{}, fmt.Errorf("%s: %s is typed by %s, which is no enum of %s or of a loaded dependency it imports the type from", where, machine.Field, field.TypeRef.Name, schema.Name)
	}

	member := func(state string) (EnumValueInfo, error) {
		value := enum.MemberSerializedAs(state)
		if value == nil {
			return EnumValueInfo{}, fmt.Errorf("%s: %q is no member of %s", where, state, enum.Name)
		}
		return EnumValueInfo{Name: value.Name, Value: value.Serialized(), Description: value.Description, Comment: value.Comment}, nil
	}
	members := func(states []string) ([]EnumValueInfo, error) {
		var values []EnumValueInfo
		for _, state := range states {
			value, err := member(state)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		return values, nil
	}

	info := StateMachineInfo{
		TypeName:  td.Name,
		FieldName: field.Name,
		EnumName:  enum.Name,
		EnumOwner: owner,
		Nullable:  !field.Required,
	}
	var err error
	if info.Terminal, err = members(machine.Terminal); err != nil {
		return StateMachineInfo{}, err
	}
	if info.FromNull, err = members(machine.FromNull); err != nil {
		return StateMachineInfo{}, err
	}
	for _, transition := range machine.Transitions {
		if transition == nil {
			continue
		}
		from, err := member(transition.From)
		if err != nil {
			return StateMachineInfo{}, err
		}
		to, err := members(transition.To)
		if err != nil {
			return StateMachineInfo{}, err
		}
		info.Transitions = append(info.Transitions, StateTransitionInfo{From: from, To: to, ToNull: transition.ToNull})
	}
	return info, nil
}

// stateMachineEnum returns the enum named name as a field of schema reads
// it, and the dependency service that owns it, empty for the schema's own.
// A dependency's enum is found as the type generators bind it: among the
// symbols an import of a DB dependency names, or anywhere in the catalog
// of a dependency of another kind, the imports walked in package order
// and the first that has the enum taken. A type or a union of the schema
// that has the name shadows it, and is no enum.
func stateMachineEnum(schema *ir.Schema, deps map[string]*ir.Schema, name string) (*ir.EnumDef, string) {
	if enum := schema.Enums[name]; enum != nil {
		return enum, ""
	}
	if schema.Types[name] != nil || schema.Unions[name] != nil {
		return nil, ""
	}
	imports := append([]ir.Import{}, schema.Imports...)
	sort.SliceStable(imports, func(i, j int) bool { return imports[i].Package < imports[j].Package })
	for _, imp := range imports {
		owner := imp.Package[strings.LastIndex(imp.Package, "/")+1:]
		dep := deps[owner]
		if dep == nil || dep.Enums[name] == nil {
			continue
		}
		if dep.Kind == ir.SchemaKindDB && !slices.Contains(imp.Types, name) {
			continue
		}
		return dep.Enums[name], owner
	}
	return nil, ""
}
