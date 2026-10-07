package verify

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// workflowBehavior is the core behavior whose states and transitions a
// display labels.
const workflowBehavior = "Workflow"

// checkDisplays holds each type's @display to the type (D48), in every
// form, as the engine does at define:
//
//   - titleField names one of the type's own fields, by its name or its
//     JSON key, that holds a single text value: the string primitive or a
//     scalar whose values are strings, not a list or a map, and neither
//     secret nor @uiHidden;
//   - each summaryFields entry names one of the type's own fields, or a
//     field one of its behaviors adds, that is neither secret nor
//     @uiHidden;
//   - states and transitions label the states and transitions of the
//     type's Workflow config, so a type that does not compose Workflow
//     takes neither.
func checkDisplays(schema *ir.Schema, reg *registry.Registry, r *Result) {
	for _, types := range []map[string]*ir.TypeDef{schema.Types, schema.Inputs} {
		for _, name := range sortedTypeNames(types) {
			if td := types[name]; td != nil && td.Display != nil {
				checkDisplay(schema, td, reg, r)
			}
		}
	}
}

func checkDisplay(schema *ir.Schema, td *ir.TypeDef, reg *registry.Registry, r *Result) {
	display := td.Display
	own := func(name string) *ir.FieldDef {
		for _, f := range td.Fields {
			if f != nil && (f.Name == name || f.JSONTag != "" && f.JSONTag == name) {
				return f
			}
		}
		return nil
	}
	shown := func(f *ir.FieldDef, member string) {
		switch {
		case f.Secret:
			r.errorf(td.Owner, "type %s: @display %s names field %s, which is secret", td.Name, member, f.Name)
		case f.UIHidden:
			r.errorf(td.Owner, "type %s: @display %s names field %s, which is @uiHidden", td.Name, member, f.Name)
		}
	}

	behaviorField := map[string]string{}
	var workflow *ir.BehaviorRef
	for i, ref := range td.Behaviors {
		if ref.Name == workflowBehavior {
			workflow = &td.Behaviors[i]
		}
		if behavior, ok := reg.Behavior(ref.Name); ok {
			for _, field := range behavior.Fields {
				behaviorField[field.Name] = ref.Name
			}
		}
	}

	if name := display.TitleField; name != "" {
		switch f := own(name); {
		case f == nil && behaviorField[name] != "":
			r.errorf(td.Owner, "type %s: @display titleField %q is a field behavior %s adds; a title is one of the type's own fields%s", td.Name, name, behaviorField[name], fieldList(td))
		case f == nil:
			r.errorf(td.Owner, "type %s: @display titleField %q is not a field of the type%s", td.Name, name, fieldList(td))
		case !textField(schema, f):
			r.errorf(td.Owner, "type %s: @display titleField %s has type %s; a title is a single text value: a string, or a scalar whose values are strings", td.Name, f.Name, typeLabel(f.TypeRef))
		default:
			shown(f, "titleField")
		}
	}

	for _, name := range display.SummaryFields {
		if f := own(name); f != nil {
			shown(f, "summaryFields")
			continue
		}
		if behaviorField[name] == "" {
			r.errorf(td.Owner, "type %s: @display summaryFields lists %q, which is not a field of the type or of its behaviors%s", td.Name, name, fieldList(td))
		}
	}

	if len(display.States) == 0 && len(display.Transitions) == 0 {
		return
	}
	if workflow == nil {
		r.errorf(td.Owner, "type %s: @display states and transitions label a Workflow's; the type does not compose Workflow", td.Name)
		return
	}
	var config workflowStates
	if err := json.Unmarshal(workflow.Config, &config); err != nil || len(config.States) == 0 {
		// checkBehaviors reports a config Workflow's schema refuses.
		return
	}
	for _, state := range sortedTypeNames(display.States) {
		if !slices.Contains(config.States, state) {
			r.errorf(td.Owner, "type %s: @display states labels %q, which is not a state of its Workflow (%s)", td.Name, state, strings.Join(config.States, ", "))
		}
	}
	for _, from := range sortedTypeNames(display.Transitions) {
		for _, to := range sortedTypeNames(display.Transitions[from]) {
			if !slices.Contains(config.Transitions, workflowTransition{From: from, To: to}) {
				r.errorf(td.Owner, "type %s: @display transitions labels the move from %s to %s, which is not a transition of its Workflow", td.Name, from, to)
			}
		}
	}
}

// workflowStates is what a display reads of a Workflow config.
type workflowStates struct {
	States      []string             `json:"states"`
	Transitions []workflowTransition `json:"transitions"`
}

// workflowTransition is one transition of a Workflow config, without the
// permission it may name.
type workflowTransition struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// textField reports whether a field holds a single text value: the string
// primitive, or a scalar whose values are JSON strings. The catalog gives
// a scalar that holds any JSON value, a JSON object or a JSON array the
// string primitive too, so its json_schema mapping rules it out.
func textField(schema *ir.Schema, f *ir.FieldDef) bool {
	if f.TypeRef.IsArray || f.TypeRef.IsMap {
		return false
	}
	if f.TypeRef.Name == "string" {
		return true
	}
	scalar := schema.Scalars[f.TypeRef.Name]
	return scalar != nil && scalar.LanguagePrimitive == ir.LanguageString && !scalar.IsAnyJSON() && scalar.StructuredJSONType() == ""
}

// typeLabel writes a field's type as a message names it: Name, Name[] or
// a map of Name.
func typeLabel(ref ir.TypeRef) string {
	label := ref.Name + strings.Repeat("[]", ref.ArrayDepth())
	if ref.IsMap {
		return "a map of " + label
	}
	return label
}

// fieldList names the type's own fields for a message, or nothing for a
// type without any.
func fieldList(td *ir.TypeDef) string {
	names := make([]string, 0, len(td.Fields))
	for _, f := range td.Fields {
		if f != nil {
			names = append(names, f.Name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return " (fields: " + strings.Join(names, ", ") + ")"
}
