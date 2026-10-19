package registry

import (
	"encoding/json"
	"fmt"

	ir "github.com/parable-work/superschematic/ir"
)

// displayArgs is the JSON Schema of @display's argument (D48), which is
// also what the data forms write under a type's `display` key: the
// schema-file JSON Schema takes its $defs, TypeDisplay and DisplayState,
// from the registered spec in place of the ones it reflects from the IR
// structs, so the TypeScript frontend, the data forms, `superschematic
// json-schema` and the strict loader an engine runs check one schema. A
// state name, a states key or either key of transitions, is a Workflow
// state's (ir.DisplayStateName).
var displayArgs = json.RawMessage(`{
	"$ref": "#/$defs/TypeDisplay",
	"$defs": {
		"TypeDisplay": {
			"description": "What a UI reads to render the instances of a type (@display): what to call one and many, which field is an instance's title, what a create button says, the glyph shown beside its name, which fields summarize it in a list, and the labels of its Workflow's states and transitions.",
			"type": "object",
			"additionalProperties": false,
			"minProperties": 1,
			"properties": {
				"noun": {"description": "What to call one instance.", "type": "string", "pattern": "\\S"},
				"plural": {"description": "What to call several.", "type": "string", "pattern": "\\S"},
				"titleField": {"description": "The field whose value is an instance's title: one of the type's own, a single text value.", "type": "string", "minLength": 1},
				"createLabel": {"description": "What a button that creates an instance says.", "type": "string", "pattern": "\\S"},
				"icon": {"description": "The glyph a UI shows beside the type's name, by a name an app's icon set draws.", "type": "string", "pattern": "^\\S(.*\\S)?$"},
				"summaryFields": {"description": "The fields that summarize an instance in a list, in order: the type's own, by name, or fields its behaviors add, by qualified name (Workflow.status).", "type": "array", "minItems": 1, "uniqueItems": true, "items": {"type": "string", "minLength": 1}},
				"states": {"description": "Labels of states of the type's Workflow, by state.", "type": "object", "minProperties": 1, "propertyNames": {"pattern": "^[A-Za-z][A-Za-z0-9_-]*$"}, "additionalProperties": {"$ref": "#/$defs/DisplayState"}},
				"transitions": {"description": "Labels of transitions of the type's Workflow, by the state a transition leaves, then by the state it enters.", "type": "object", "minProperties": 1, "propertyNames": {"pattern": "^[A-Za-z][A-Za-z0-9_-]*$"}, "additionalProperties": {"type": "object", "minProperties": 1, "propertyNames": {"pattern": "^[A-Za-z][A-Za-z0-9_-]*$"}, "additionalProperties": {"type": "string", "pattern": "\\S"}}}
			}
		},
		"DisplayState": {
			"description": "How a UI shows one Workflow state: its label, the present-progressive form it shows while an instance is in the state, and a tone, what the state means to a reader.",
			"type": "object",
			"additionalProperties": false,
			"minProperties": 1,
			"properties": {
				"label": {"type": "string", "pattern": "\\S"},
				"activeForm": {"type": "string", "pattern": "\\S"},
				"tone": {"type": "string", "enum": ["muted", "active", "success", "warning", "danger"]}
			}
		}
	}
}`)

// displayDecorator is @display({ noun, plural, titleField, createLabel,
// icon, summaryFields, states, transitions }) from @superschematic/schema,
// on a class of any kind: it writes TypeDef.Display, once per type. The
// frontend has held the argument to displayArgs; the loader holds the
// fields and states it names to the type (verify.checkDisplays).
func displayDecorator() DecoratorSpec {
	return DecoratorSpec{
		Name: "display", Packages: []string{pkgSchema}, Target: TargetType, Args: displayArgs,
		Apply: func(n Node, args []any, _ Site) error {
			if n.Type.Display != nil {
				return fmt.Errorf("type %s has more than one @display decorator", n.Type.Name)
			}
			var display ir.TypeDisplay
			if err := DecodeArgs(args, &display); err != nil {
				return ArgErrorf(0, "@display: %v", err)
			}
			if err := ir.ValidateTypeDisplay(&display); err != nil {
				return ArgErrorf(0, "invalid @display: %v", err)
			}
			n.Type.Display = &display
			return nil
		},
	}
}

// fieldDisplayArgs is the JSON Schema of @display's argument on a field
// (D48, amended), which is also what the data forms write under a field's
// `display` key: the schema-file JSON Schema takes $defs/FieldDisplay from
// it, as it takes a type's display from displayArgs.
var fieldDisplayArgs = json.RawMessage(`{
	"$ref": "#/$defs/FieldDisplay",
	"$defs": {
		"FieldDisplay": {
			"description": "How a UI renders a field's control (@display on a field).",
			"type": "object",
			"additionalProperties": false,
			"minProperties": 1,
			"properties": {
				"multiline": {"description": "A control that holds several lines (true) or one line (false), whatever the field's maxLength; absent leaves it to the UI.", "type": "boolean"}
			}
		}
	}
}`)

// fieldDisplayDecorator is @display({ multiline }) from
// @superschematic/schema on a field: it writes FieldDef.Display, once per
// field. It is a spec of its own beside the type's, since the registry keys
// a decorator by name and target. The frontend has held the argument to
// fieldDisplayArgs; the loader holds the field to text that is not secret
// (verify.checkDisplays).
func fieldDisplayDecorator() DecoratorSpec {
	return DecoratorSpec{
		Name: "display", Packages: []string{pkgSchema}, Target: TargetField, Args: fieldDisplayArgs,
		Apply: func(n Node, args []any, _ Site) error {
			if n.Field.Display != nil {
				return fmt.Errorf("field %s has more than one @display decorator", n.Field.Name)
			}
			var display ir.FieldDisplay
			if err := DecodeArgs(args, &display); err != nil {
				return ArgErrorf(0, "@display: %v", err)
			}
			if err := ir.ValidateFieldDisplay(&display); err != nil {
				return ArgErrorf(0, "invalid @display: %v", err)
			}
			n.Field.Display = &display
			return nil
		},
	}
}
