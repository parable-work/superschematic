package verify

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/behaviorlayout"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// configReferencesHold holds the config of a use td implements to the
// references its behavior's declaration states (D103's step 5), the
// members that name a part of the table, which per-behavior code held
// before: a workflow reference names a Workflow use of the table, a field
// reference an own field (one that holds the scalar it names, when it
// names one), and a state reference a state of the Workflow the config's
// workflow reference names, or of the table's one Workflow. It reports the
// first problem, in the order the declaration lists its references and
// the config holds the values, as `type <T>: behavior <B> config: <path>
// ...`, the path the member's (deadlines.reminder.start.field), and
// returns false; true when every reference holds, or the config is one the
// config schema refuses, which checkTypeBehaviors reports. A behavior's
// own check runs only when its references hold, so a problem the
// declaration states is reported once, and first.
func configReferencesHold(schema *ir.Schema, td *ir.TypeDef, ref ir.BehaviorRef, behavior registry.Behavior, r *Result) bool {
	if len(behavior.References) == 0 || len(ref.Config) == 0 || behavior.ValidateConfig(ref.Config) != nil {
		return true
	}
	var config any
	if json.Unmarshal(ref.Config, &config) != nil {
		return true
	}
	fail := func(format string, args ...any) bool {
		r.errorf(td.Owner, "type %s: behavior %s config: %s", td.Name, ref.Name, fmt.Sprintf(format, args...))
		return false
	}
	workflows := workflowUses(td)
	listed := "none"
	if len(workflows) > 0 {
		listed = strings.Join(workflows, ", ")
	}
	// The Workflow the config's state references read: the one its
	// workflow reference names, or the table's one.
	var named *string
	for _, wf := range behavior.ReferencesOf(registry.ReferenceWorkflow) {
		for _, v := range registry.ConfigValues(config, wf.Path) {
			name, _ := v.Value.(string)
			if !slices.Contains(workflows, name) {
				return fail("%s %q is not a Workflow of the type (its Workflows: %s)", v.At, name, listed)
			}
			if named == nil {
				named = &name
			}
		}
	}
	var own []behaviorlayout.Column
	ownRead := false
	for _, reference := range behavior.References {
		values := registry.ConfigValues(config, reference.Path)
		switch reference.Kind {
		case registry.ReferenceField:
			if len(values) > 0 && !ownRead {
				own, ownRead = behaviorlayout.OwnFields(schema, td), true
			}
			for _, v := range values {
				if why := fieldReferenceProblem(td, own, v, reference); why != "" {
					return fail("%s", why)
				}
			}
		case registry.ReferenceState:
			for _, v := range values {
				state, _ := v.Value.(string)
				if named == nil && len(workflows) == 0 {
					return fail("%s reads the status of Workflow, which the type does not list", v.At)
				}
				if named == nil && len(workflows) > 1 {
					return fail("%s reads the status of Workflow, and the type has several (%s): status names the field whose Workflow it reads", v.At, listed)
				}
				// A Workflow whose config has another shape is Workflow's
				// to refuse, and its states are then not checked.
				if flow := timersFlowOf(td, named); flow != nil && !slices.Contains(flow.states, state) {
					return fail("%s %q is not a state of the type's Workflow (%s)", v.At, state, strings.Join(flow.states, ", "))
				}
			}
		}
	}
	return true
}

// fieldReferenceProblem is why the value v a field reference reaches does
// not name an own field of td, among own (behaviorlayout.OwnFields, the
// fields the core's spec lists), or one that holds the scalar the
// reference names; "" when it does.
func fieldReferenceProblem(td *ir.TypeDef, own []behaviorlayout.Column, v registry.ConfigValue, reference registry.BehaviorReference) string {
	field, _ := v.Value.(string)
	var found *behaviorlayout.Column
	known := make([]string, 0, len(own))
	for i := range own {
		known = append(known, own[i].Field)
		if own[i].Field == field {
			found = &own[i]
		}
	}
	fields := "none"
	if len(known) > 0 {
		fields = strings.Join(known, ", ")
	}
	if found == nil {
		return fmt.Sprintf("%s %q is not a field of %s (its fields: %s)", v.At, field, td.Name, fields)
	}
	if reference.Type == "" || holdsScalar(*found, reference.Type) {
		return ""
	}
	why := fmt.Sprintf("%s %q is not a %s field of %s", v.At, field, scalarWords(reference.Type), td.Name)
	if reference.Reason != "" {
		why += ": " + reference.Reason
	}
	return why
}

// holdsScalar reports whether an own field's column holds scalar: a
// date-time field is one whose column converts to epoch milliseconds, as
// the core reads a date-time; any other the column's IR type names.
func holdsScalar(column behaviorlayout.Column, scalar string) bool {
	if scalar == "Temporal.DateTime" {
		return column.Convert == behaviorlayout.ConvertEpochMillis
	}
	return column.Type == scalar
}

// scalarWords names a scalar as a problem reads it: date-time for
// Temporal.DateTime, the scalar's name otherwise.
func scalarWords(scalar string) string {
	if scalar == "Temporal.DateTime" {
		return "date-time"
	}
	return scalar
}
