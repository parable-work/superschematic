package ir

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// WorkflowBehavior is the core behavior whose transitions may list states.
const WorkflowBehavior = "Workflow"

// ExpandTransitions is ExpandTransitionLists for a use: a Workflow a table
// composes, on a field or by implements (D103). Any other use is left as
// it is: an engine type's @behavior takes the schema package's config,
// whose transitions list no states.
func (ref *BehaviorRef) ExpandTransitions() error {
	if ref == nil || ref.Name != WorkflowBehavior || len(ref.Config) == 0 {
		return nil
	}
	if !ref.FieldAnchored() && !ref.Implements {
		return nil
	}
	config, err := ExpandTransitionLists(ref.Config)
	if err != nil {
		return err
	}
	ref.Config = config
	return nil
}

// ExpandTransitionLists writes out, in a table's Workflow config (D103),
// each transition that lists states (D109): `{ from: ["backlog", "todo"],
// to: ["started", "done"] }` is one transition from each of its from
// states to each of its to states, in that order, each with the
// transition's other members, its permission and its by. A state both
// lists hold moves to every other and not to itself, so one list in both
// is every move among its states. It returns raw itself, byte for byte,
// when no transition lists states.
//
// The TypeScript reader calls it before it holds a config to its
// configSchema, so the schema, the build's rules, the generators and the
// core read plain transitions, each a from and a to, and none of them
// knows the short form. It is the TypeScript form's: a JSON or YAML schema
// file is held to the document's schema as written, which the engine's
// declaration of Workflow shares, and writes each transition out.
//
// What only the short form can get wrong is refused here, in the words of
// the checks that follow: a list with no state or with one twice, a
// transition whose lists leave no move, and a move two transitions make
// when one of them lists it, which a reader of the config would not see.
// Two plain transitions that repeat each other stay the core's to refuse,
// as before. A config that is not what the configSchema takes (a from
// that is a number, transitions that are no list) is left for it to
// report.
func ExpandTransitionLists(raw json.RawMessage) (json.RawMessage, error) {
	var config map[string]json.RawMessage
	if err := json.Unmarshal(raw, &config); err != nil {
		return raw, nil
	}
	var transitions []map[string]json.RawMessage
	if err := json.Unmarshal(config["transitions"], &transitions); err != nil {
		return raw, nil
	}
	type end struct {
		states []string
		listed bool
	}
	// read is one end of a transition, a state or a list of them; ok is
	// false for anything else, which the configSchema reports.
	read := func(value json.RawMessage) (end, bool) {
		var state string
		if json.Unmarshal(value, &state) == nil {
			return end{states: []string{state}}, true
		}
		var states []string
		if len(bytes.TrimSpace(value)) == 0 || bytes.TrimSpace(value)[0] != '[' || json.Unmarshal(value, &states) != nil {
			return end{}, false
		}
		return end{states: states, listed: true}, true
	}
	type written struct{ from, to end }
	ends := make([]written, len(transitions))
	short := false
	for i, transition := range transitions {
		from, ok := read(transition["from"])
		if !ok {
			return raw, nil
		}
		to, ok := read(transition["to"])
		if !ok {
			return raw, nil
		}
		ends[i] = written{from, to}
		short = short || from.listed || to.listed
	}
	if !short {
		return raw, nil
	}

	// label words a transition as it is written, for a message.
	label := func(w written) string {
		side := func(e end) string {
			if !e.listed {
				return fmt.Sprintf("%q", e.states[0])
			}
			quoted := make([]string, len(e.states))
			for i, state := range e.states {
				quoted[i] = fmt.Sprintf("%q", state)
			}
			return "[" + strings.Join(quoted, ", ") + "]"
		}
		return "the transition from " + side(w.from) + " to " + side(w.to)
	}
	type move struct{ from, to string }
	made := map[move]int{} // the transition that made each move, by its place
	var out []map[string]json.RawMessage
	for i, w := range ends {
		for _, side := range []struct {
			name string
			end  end
		}{{"from", w.from}, {"to", w.to}} {
			if !side.end.listed {
				continue
			}
			if len(side.end.states) == 0 {
				return nil, fmt.Errorf("%s lists no state in %s", label(w), side.name)
			}
			seen := map[string]bool{}
			for _, state := range side.end.states {
				if seen[state] {
					return nil, fmt.Errorf("%s lists %q twice in %s", label(w), state, side.name)
				}
				seen[state] = true
			}
		}
		moves := 0
		for _, from := range w.from.states {
			for _, to := range w.to.states {
				if from == to && (w.from.listed || w.to.listed) {
					// A state of both lists moves to the others.
					continue
				}
				if first, twice := made[move{from, to}]; twice && (ends[first].from.listed || ends[first].to.listed || w.from.listed || w.to.listed) {
					if first == i {
						return nil, fmt.Errorf("%s lists the move from %q to %q twice", label(w), from, to)
					}
					return nil, fmt.Errorf("the transition from %q to %q is listed twice: by %s, and by %s", from, to, label(ends[first]), label(w))
				}
				if _, twice := made[move{from, to}]; !twice {
					made[move{from, to}] = i
				}
				one := make(map[string]json.RawMessage, len(transitions[i]))
				for member, value := range transitions[i] {
					one[member] = value
				}
				one["from"], _ = json.Marshal(from)
				one["to"], _ = json.Marshal(to)
				out = append(out, one)
				moves++
			}
		}
		if moves == 0 {
			return nil, fmt.Errorf("%s makes no move: a state both lists hold moves to the others, and there are none", label(w))
		}
	}
	expanded, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	config["transitions"] = expanded
	return json.Marshal(config)
}
