package verify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/behaviorlayout"
	ir "github.com/parable-work/superschematic/ir"
)

// derivedBehavior and provenanceBehavior are the names of the core's
// Derived and Provenance (D73), which a table implements (D103's step 4).
const (
	derivedBehavior    = "Derived"
	provenanceBehavior = "Provenance"
)

// derivedField is one derived field of a Derived config, as the build
// reads it.
type derivedField struct {
	From   []string `json:"from"`
	States []string `json:"states"`
}

// checkDerivedConfig holds the config of a Derived use the table td
// implements to the table (D73, D103's step 4), each problem `type <T>:
// behavior Derived config: <reason>` and the first reported, in the
// core's words and order (superschematic_rules_behaviors::derived): each
// derived field, in the config's order, an own field of the table, none
// Constants keeps, each input an own field, never the field itself or
// another derived field, and the states it names those of the table's one
// Workflow. What only the IR shows is the build's: an input that is
// Secret<T>, which never goes to a model (D74). A config the configSchema
// refuses is reported by checkTypeBehaviors and left alone here.
func checkDerivedConfig(schema *ir.Schema, td *ir.TypeDef, ref ir.BehaviorRef, r *Result) {
	var config struct {
		Fields json.RawMessage `json:"fields"`
	}
	if len(ref.Config) == 0 || json.Unmarshal(ref.Config, &config) != nil {
		return
	}
	names, fields, err := orderedDerived(config.Fields)
	if err != nil {
		return
	}
	if why := derivedProblem(schema, td, names, fields); why != "" {
		r.errorf(td.Owner, "type %s: behavior %s config: %s", td.Name, ref.Name, why)
	}
}

// derivedProblem is the first problem of a table's Derived config, or "".
func derivedProblem(schema *ir.Schema, td *ir.TypeDef, names []string, fields []derivedField) string {
	own := specFieldNames(schema, td)
	kept := constantsFields(td)
	secret := map[string]bool{}
	for _, fd := range td.Fields {
		if fd != nil && !fd.Owned() && fd.Secret {
			secret[fd.Name] = true
		}
	}
	for i, name := range names {
		if !slices.Contains(own, name) {
			return fmt.Sprintf("fields: %s has no field %s", td.Name, name)
		}
		if slices.Contains(kept, name) {
			return fmt.Sprintf("fields: %s is a field Constants keeps, which no model may write after the create", name)
		}
		for _, input := range fields[i].From {
			switch {
			case input == name:
				return fmt.Sprintf("fields: %s is derived from itself", name)
			case slices.Contains(names, input):
				return fmt.Sprintf("fields: %s is derived from %s, which is derived too", name, input)
			case !slices.Contains(own, input):
				return fmt.Sprintf("fields: %s is derived from %s, which %s does not have", name, input, td.Name)
			case secret[input]:
				return fmt.Sprintf("fields: %s is derived from %s, which is secret: a secret never goes to a model", name, input)
			}
		}
		if fields[i].States == nil {
			continue
		}
		if len(workflowUses(td)) == 0 {
			return fmt.Sprintf("fields: %s names states, and the type has no Workflow", name)
		}
		flow, why := flowOf(td, nil)
		if why != "" {
			return why
		}
		for _, state := range fields[i].States {
			if !flow.has(state) {
				return fmt.Sprintf("fields: %s: state %q is not a state of the type's Workflow (%s)", name, state, strings.Join(flow.states, ", "))
			}
		}
	}
	return ""
}

// orderedDerived reads a config's derived fields in the order it writes
// them.
func orderedDerived(raw json.RawMessage) ([]string, []derivedField, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil {
		return nil, nil, err
	}
	var names []string
	var fields []derivedField
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		name, _ := key.(string)
		var field derivedField
		if err := dec.Decode(&field); err != nil {
			return nil, nil, err
		}
		names = append(names, name)
		fields = append(fields, field)
	}
	return names, fields, nil
}

// checkProvenanceConfig holds the config of a Provenance use the table td
// implements to the table (D73, D103's step 4), each problem `type <T>:
// behavior Provenance config: <reason>` and the first reported, in the
// core's words and order (superschematic_rules_behaviors::provenance):
// each recorded field an own field of the table, each review source
// another behavior the table composes, and each gate a state of the
// table's one Workflow whose listed fields are recorded. A config the
// configSchema refuses is reported by checkTypeBehaviors and left alone
// here.
func checkProvenanceConfig(schema *ir.Schema, td *ir.TypeDef, ref ir.BehaviorRef, r *Result) {
	var config struct {
		Fields []string `json:"fields"`
		Review *struct {
			Sources []string `json:"sources"`
		} `json:"review"`
		Gates json.RawMessage `json:"gates"`
	}
	if len(ref.Config) == 0 || json.Unmarshal(ref.Config, &config) != nil {
		return
	}
	fail := func(format string, args ...any) {
		r.errorf(td.Owner, "type %s: behavior %s config: %s", td.Name, ref.Name, fmt.Sprintf(format, args...))
	}
	own := specFieldNames(schema, td)
	recorded := own
	if config.Fields != nil {
		for _, field := range config.Fields {
			if !slices.Contains(own, field) {
				fail("fields: %s has no field %s", td.Name, field)
				return
			}
		}
		recorded = config.Fields
	}
	if config.Review != nil {
		for _, source := range config.Review.Sources {
			composed := false
			for _, use := range td.TableBehaviors() {
				composed = composed || use.Name == source
			}
			if source == provenanceBehavior || !composed {
				fail("review: source %s is not another behavior %s composes", source, td.Name)
				return
			}
		}
	}
	states, gates, err := orderedGates(config.Gates)
	if err != nil || len(states) == 0 {
		return
	}
	if len(workflowUses(td)) == 0 {
		fail("gates: the type has no Workflow whose moves they gate")
		return
	}
	flow, why := flowOf(td, nil)
	if why != "" {
		fail("%s", why)
		return
	}
	for i, state := range states {
		if !flow.has(state) {
			fail("gate %q is not a state of the type's Workflow (%s)", state, strings.Join(flow.states, ", "))
			return
		}
		var listed []string
		if json.Unmarshal(gates[i], &listed) != nil {
			continue
		}
		for _, field := range listed {
			if !slices.Contains(recorded, field) {
				fail("gate %q: %s is not a field Provenance records", state, field)
				return
			}
		}
	}
}

// orderedGates reads a config's gates in the order it writes them, each
// state with its reviewed member.
func orderedGates(raw json.RawMessage) ([]string, []json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil {
		return nil, nil, err
	}
	var states []string
	var reviewed []json.RawMessage
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		state, _ := key.(string)
		var gate struct {
			Reviewed json.RawMessage `json:"reviewed"`
		}
		if err := dec.Decode(&gate); err != nil {
			return nil, nil, err
		}
		states = append(states, state)
		reviewed = append(reviewed, gate.Reviewed)
	}
	return states, reviewed, nil
}

// specFieldNames are the names of the fields the core's spec of td lists
// (behaviorlayout.SpecFields), what a Derived or Provenance config that
// names a field is held to.
func specFieldNames(schema *ir.Schema, td *ir.TypeDef) []string {
	var names []string
	for _, column := range behaviorlayout.SpecFields(schema, td) {
		names = append(names, column.Field)
	}
	return names
}

// constantsFields are the fields td's Constants keeps.
func constantsFields(td *ir.TypeDef) []string {
	var out []string
	for _, use := range td.TableBehaviors() {
		if use.Name != constantsBehavior {
			continue
		}
		var config struct {
			Fields []string `json:"fields"`
		}
		if json.Unmarshal(use.Config, &config) == nil {
			out = append(out, config.Fields...)
		}
	}
	return out
}
