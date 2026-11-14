package registry

import (
	"fmt"

	ir "github.com/parable-work/superschematic/ir"
)

// stateMachineDecorator returns @stateMachine, from @superschematic/schema
// and in a schema of any kind: the moves one enum field of the class may
// make (D118). Apply reads the declaration's shape, and every state as
// the value its enum member serializes to, which is what the argument
// evaluator makes of a member. ir.Schema.Validate checks what the
// declaration alone decides, and verification checks it against the field
// and its enum, in every authoring form.
func stateMachineDecorator() DecoratorSpec {
	return DecoratorSpec{
		Name: "stateMachine", Packages: []string{pkgSchema}, Target: TargetType,
		Apply: func(n Node, args []any, _ Site) error {
			machine, err := stateMachineOf(n.Type.Name, args)
			if err != nil {
				return err
			}
			n.Type.StateMachines = append(n.Type.StateMachines, machine)
			return nil
		},
	}
}

// stateMachineOf reads @stateMachine<T>({ field, transitions, terminal,
// fromNull }) on the class named typeName. An object's keys reach it in no
// order, so it writes the transitions by their state's value, an order
// that is the same on every run; the loader then gives them the order of
// the enum's members (ir.StateMachine.OrderTransitions).
func stateMachineOf(typeName string, args []any) (*ir.StateMachine, error) {
	const shape = "@stateMachine<T>({ field, transitions, terminal })"
	if len(args) != 1 {
		return nil, fmt.Errorf("@stateMachine on %s takes one options object: %s", typeName, shape)
	}
	obj, ok := args[0].(map[string]any)
	if !ok {
		return nil, ArgErrorf(0, "@stateMachine on %s takes an options object: %s", typeName, shape)
	}
	field, ok := obj["field"].(string)
	if !ok || field == "" {
		return nil, ArgErrorf(0, "@stateMachine on %s names no field; name the enum field whose moves it declares, as field: \"status\"", typeName)
	}
	where := fmt.Sprintf("@stateMachine on %s.%s", typeName, field)
	for _, key := range sortedKeys(obj) {
		switch key {
		case "field", "transitions", "terminal", "fromNull":
		default:
			return nil, ArgErrorf(0, "%s has no option %q; it takes field, transitions, terminal and fromNull", where, key)
		}
	}

	machine := &ir.StateMachine{Field: field}
	transitions, ok := obj["transitions"].(map[string]any)
	if !ok {
		return nil, ArgErrorf(0, "%s: transitions maps each state to the states it may move to, as { [Status.Open]: [Status.Closed] }, and {} when no state moves", where)
	}
	for _, from := range sortedKeys(transitions) {
		role := fmt.Sprintf("transitions[%q]", from)
		to, toNull, err := stateList(transitions[from], role, true)
		if err != nil {
			return nil, ArgErrorf(0, "%s: %v", where, err)
		}
		machine.Transitions = append(machine.Transitions, &ir.StateTransition{From: from, To: to, ToNull: toNull})
	}

	terminal, present := obj["terminal"]
	if !present {
		return nil, ArgErrorf(0, "%s: terminal lists the states no update leaves, and is [] when there is none", where)
	}
	var err error
	if machine.Terminal, _, err = stateList(terminal, "terminal", false); err != nil {
		return nil, ArgErrorf(0, "%s: %v", where, err)
	}

	if fromNull, present := obj["fromNull"]; present {
		if machine.FromNull, _, err = stateList(fromNull, "fromNull", false); err != nil {
			return nil, ArgErrorf(0, "%s: %v", where, err)
		}
		// An empty list admits nothing, and the IR's JSON would leave it
		// out, so a schema read back would hold a machine with no null
		// start.
		if len(machine.FromNull) == 0 {
			return nil, ArgErrorf(0, "%s: fromNull is empty, so a null field could take no state; list the states a null may take, or leave fromNull out", where)
		}
	}
	return machine, nil
}

// stateList reads a list of states, each the string its enum member
// serializes to. In a state's transitions a null returns the field to
// null, once; in terminal and fromNull, which name states alone, it is
// refused. role names the list in an error.
func stateList(value any, role string, takesNull bool) (states []string, toNull bool, err error) {
	list, ok := value.([]any)
	if !ok {
		return nil, false, fmt.Errorf("%s is a list of members of the field's enum, as [Status.Closed]; got %v", role, value)
	}
	for _, entry := range list {
		switch state := entry.(type) {
		case string:
			states = append(states, state)
		case nil:
			switch {
			case !takesNull:
				return nil, false, fmt.Errorf("%s lists null, which is no state; a null start is fromNull's, and a return to null a null in a state's transitions", role)
			case toNull:
				return nil, false, fmt.Errorf("%s lists null twice; list it once", role)
			}
			toNull = true
		default:
			return nil, false, fmt.Errorf("%s lists %v; a state is a member of the field's enum, whose value is a string", role, entry)
		}
	}
	return states, toNull, nil
}
