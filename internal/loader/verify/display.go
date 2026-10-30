package verify

import (
	"encoding/json"
	"slices"
	"strings"

	scalars "github.com/parable-work/superscalar/go"

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
//   - each summaryFields entry names one of the type's own fields that is
//     neither secret nor @uiHidden, or a field one of its behaviors adds,
//     by its qualified name (Workflow.status): an instance keeps a
//     behavior's fields under the behavior's name (D16, amended: a
//     behavior's fields sit under its name);
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
	checkFieldDisplays(schema, r)
}

// checkFieldDisplays holds each field's @display (D48, amended) to a field
// whose control or value it can say:
//
//   - multiline is for text, so the field holds a string or a scalar whose
//     values are strings, or a list of them (a control per row), not a
//     map; and the field is not secret, which a UI renders as a password
//     input, one line by nature;
//   - currency is for an amount in minor units, so the field holds an
//     integer scalar (Finance.Money, Generic.Int64), or a list of them,
//     not a map. A literal is an active ISO 4217 code, as Finance.Currency
//     takes one (in capitals: the scalar refuses usd); a field is one of
//     the same type's own, by its name or its JSON key, that holds a
//     single text value, a Finance.Currency or a string.
//
// It reads the fields of every type and input, and of a configurable
// trait's config, which Schema.Validate holds to a field display's shape
// too.
func checkFieldDisplays(schema *ir.Schema, r *Result) {
	check := func(td *ir.TypeDef, what string, fields []*ir.FieldDef) {
		for _, f := range fields {
			if f == nil || f.Display == nil {
				continue
			}
			if f.Display.Multiline != nil {
				switch {
				case f.TypeRef.IsMap || !ir.TextValue(schema, ir.TypeRef{Name: f.TypeRef.Name}):
					r.errorf(td.Owner, "type %s: %s %s takes @display multiline, which is for text, but it is %s", td.Name, what, f.Name, typeLabel(f.TypeRef))
				case f.Secret:
					r.errorf(td.Owner, "type %s: %s %s takes @display multiline, but it is secret, a password input", td.Name, what, f.Name)
				}
			}
			if c := f.Display.Currency; c != nil {
				checkDisplayCurrency(schema, td, what, fields, f, c, r)
			}
		}
	}
	for _, types := range []map[string]*ir.TypeDef{schema.Types, schema.Inputs} {
		for _, name := range sortedTypeNames(types) {
			td := types[name]
			if td == nil {
				continue
			}
			check(td, "field", td.Fields)
			if td.TraitConfig != nil {
				check(td, "trait config field", td.TraitConfig.Fields)
			}
		}
	}
}

// checkDisplayCurrency holds field f's @display currency, c, to an amount
// field of td and a currency: an active code, or one of fields, f's
// siblings, that holds text.
func checkDisplayCurrency(schema *ir.Schema, td *ir.TypeDef, what string, fields []*ir.FieldDef, f *ir.FieldDef, c *ir.DisplayCurrency, r *Result) {
	if f.TypeRef.IsMap || !integerValue(schema, ir.TypeRef{Name: f.TypeRef.Name}) {
		r.errorf(td.Owner, "type %s: %s %s takes @display currency, which is for an amount in minor units, but it is %s", td.Name, what, f.Name, typeLabel(f.TypeRef))
	}
	if c.Code != "" {
		if _, err := scalars.ParseFinanceCurrency(c.Code); err != nil {
			r.errorf(td.Owner, "type %s: %s %s takes @display currency %q, which is not an active ISO 4217 currency code", td.Name, what, f.Name, c.Code)
		}
		return
	}
	sibling := currencyField(fields, c.Field)
	names := make([]string, 0, len(fields))
	for _, other := range fields {
		if other != nil && other != f {
			names = append(names, other.Name)
		}
	}
	switch {
	case sibling == f:
		r.errorf(td.Owner, "type %s: %s %s takes @display currency { field: %q }, which is the amount itself; the currency is another field of the type", td.Name, what, f.Name, c.Field)
	case sibling == nil:
		list := ""
		if len(names) > 0 {
			list = " (fields: " + strings.Join(names, ", ") + ")"
		}
		r.errorf(td.Owner, "type %s: %s %s takes @display currency { field: %q }, which is not a field of the type%s", td.Name, what, f.Name, c.Field, list)
	case !ir.TextValue(schema, sibling.TypeRef):
		r.errorf(td.Owner, "type %s: %s %s takes @display currency { field: %q }, which is %s; a currency field holds a single Finance.Currency or text value", td.Name, what, f.Name, c.Field, typeLabel(sibling.TypeRef))
	}
}

// currencyField finds the field a @display currency { field } names: by its
// name first, then by its JSON key, as the engine's describe and Topcoat's
// views read it, so a name that is also another field's JSON key names the
// field of that name.
func currencyField(fields []*ir.FieldDef, name string) *ir.FieldDef {
	for _, other := range fields {
		if other != nil && other.Name == name {
			return other
		}
	}
	for _, other := range fields {
		if other != nil && other.JSONTag != "" && other.JSONTag == name {
			return other
		}
	}
	return nil
}

// integerValue reports whether ref, read in schema, is a single whole
// number: a scalar of the schema whose values are JSON integers, by its
// json_schema type mapping (Finance.Money, Generic.Int64), not a list or a
// map. It is the rule of a field's @display currency (D48, amended): an
// amount in a currency's minor units.
func integerValue(schema *ir.Schema, ref ir.TypeRef) bool {
	if ref.IsArray || ref.IsMap || schema == nil {
		return false
	}
	scalar := schema.Scalars[ref.Name]
	return scalar != nil && scalar.TypeMappings["json_schema"] == "integer"
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
	// bare holds each behavior field's qualified names by its bare name,
	// for the hint a summary field named bare gets.
	bare := map[string][]string{}
	// A field-anchored use's fields are the type's own columns, which the
	// loader adds before this check (D103), so own finds them and only a
	// type-anchored use's sit under its name here.
	var workflow *ir.BehaviorRef
	onType := td.TypeBehaviors()
	for i, ref := range onType {
		if ref.Name == workflowBehavior {
			workflow = &onType[i]
		}
		if behavior, ok := reg.Behavior(ref.Name); ok {
			for _, field := range behavior.Fields {
				path := registry.BehaviorFieldPath(ref.Name, field.Name)
				behaviorField[path] = ref.Name
				bare[field.Name] = append(bare[field.Name], path)
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
			hint := ""
			if qualified := bare[name]; len(qualified) > 0 {
				hint = "; a behavior's field is named by its qualified name: " + strings.Join(qualified, " or ")
			}
			r.errorf(td.Owner, "type %s: @display summaryFields lists %q, which is not a field of the type or of its behaviors%s%s", td.Name, name, fieldList(td), hint)
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
// primitive, or a scalar whose values are JSON strings (ir.TextValue, the
// rule a reference's label and a search argument share).
func textField(schema *ir.Schema, f *ir.FieldDef) bool {
	return ir.TextValue(schema, f.TypeRef)
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
