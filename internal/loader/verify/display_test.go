package verify

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// displaySchema is a General schema whose Ticket type composes Workflow
// and Comments and declares the display change gives it, with a field of
// each kind a title may or may not be.
func displaySchema(change func(*ir.TypeDef)) *ir.Schema {
	schema := ir.NewSchema("tickets", ir.SchemaKindGeneral)
	schema.Scalars["Identity.Name"] = &ir.ScalarDef{Name: "Identity.Name", LanguagePrimitive: ir.LanguageString}
	schema.Scalars["Generic.JSON"] = &ir.ScalarDef{Name: "Generic.JSON", LanguagePrimitive: ir.LanguageString, TypeMappings: map[string]string{"json_schema": "any"}}
	schema.Scalars["Embedding.Vector"] = &ir.ScalarDef{Name: "Embedding.Vector", LanguagePrimitive: ir.LanguageString, TypeMappings: map[string]string{"json_schema": "array"}}
	schema.Scalars["Generic.Int64"] = &ir.ScalarDef{Name: "Generic.Int64", LanguagePrimitive: ir.LanguageNumber}
	td := &ir.TypeDef{
		Name:  "Ticket",
		Owner: "src/ticket.schema.json",
		Role:  ir.RoleEmbeddedStruct,
		Behaviors: []ir.BehaviorRef{
			{Name: "Workflow", Config: json.RawMessage(`{"states":["todo","doing","done"],"transitions":[{"from":"todo","to":"doing"},{"from":"doing","to":"done","permission":"tickets.close"}]}`)},
			{Name: "Comments"},
		},
		Fields: []*ir.FieldDef{
			{Name: "title", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
			{Name: "owner", TypeRef: ir.TypeRef{Name: "Identity.Name"}},
			{Name: "legacy", JSONTag: "legacyTitle", TypeRef: ir.TypeRef{Name: "string"}},
			{Name: "tags", TypeRef: ir.TypeRef{Name: "string", IsArray: true}},
			{Name: "labels", TypeRef: ir.TypeRef{Name: "string", IsMap: true}},
			{Name: "points", TypeRef: ir.TypeRef{Name: "Generic.Int64"}},
			{Name: "payload", TypeRef: ir.TypeRef{Name: "Generic.JSON"}},
			{Name: "vector", TypeRef: ir.TypeRef{Name: "Embedding.Vector"}},
			{Name: "token", TypeRef: ir.TypeRef{Name: "string"}, Secret: true},
			{Name: "internal", TypeRef: ir.TypeRef{Name: "string"}, UIHidden: true},
		},
		Display: &ir.TypeDisplay{
			Noun:          "Ticket",
			TitleField:    "title",
			SummaryFields: []string{"Workflow.status", "Comments.commentCount", "owner"},
			States:        map[string]ir.DisplayState{"todo": {Label: "To do"}, "doing": {Label: "Do", ActiveForm: "Doing", Tone: ir.DisplayToneActive}},
			Transitions:   map[string]map[string]string{"todo": {"doing": "Start"}, "doing": {"done": "Finish"}},
		},
	}
	if change != nil {
		change(td)
	}
	schema.Types["Ticket"] = td
	return schema
}

// D48: the loader holds a display to its type in every form: its title to
// one of the type's own single text fields, its summary fields to the
// type's own and its behaviors', both to fields a reader is shown, and its
// states and transitions to its Workflow's config.
func TestDisplaysVerify(t *testing.T) {
	reg := registry.New(naming.Naming{})
	title := func(name string) func(*ir.TypeDef) {
		return func(td *ir.TypeDef) { td.Display.TitleField = name }
	}
	const prefix = "src/ticket.schema.json: type Ticket: @display "
	const fields = " (fields: title, owner, legacy, tags, labels, points, payload, vector, token, internal)"
	const text = "; a title is a single text value: a string, or a scalar whose values are strings"
	for _, test := range []struct {
		name   string
		change func(*ir.TypeDef)
		want   []string
	}{
		{"accepted", nil, nil},
		{"a scalar title whose values are strings", title("owner"), nil},
		{"a title by its JSON key", title("legacyTitle"), nil},
		{"no display", func(td *ir.TypeDef) { td.Display = nil }, nil},
		{"a title that is no field", title("ghost"), []string{prefix + `titleField "ghost" is not a field of the type` + fields}},
		{"a title a behavior adds", title("Workflow.status"), []string{prefix + `titleField "Workflow.status" is a field behavior Workflow adds; a title is one of the type's own fields` + fields}},
		{"a list title", title("tags"), []string{prefix + "titleField tags has type string[]" + text}},
		{"a map title", title("labels"), []string{prefix + "titleField labels has type a map of string" + text}},
		{"a number title", title("points"), []string{prefix + "titleField points has type Generic.Int64" + text}},
		{"a JSON title", title("payload"), []string{prefix + "titleField payload has type Generic.JSON" + text}},
		{"an array scalar title", title("vector"), []string{prefix + "titleField vector has type Embedding.Vector" + text}},
		{"a secret title", title("token"), []string{prefix + "titleField names field token, which is secret"}},
		{"a hidden title", title("internal"), []string{prefix + "titleField names field internal, which is @uiHidden"}},
		{"summary fields that are no fields", func(td *ir.TypeDef) { td.Display.SummaryFields = []string{"owner", "ghost", "Workflow.status"} },
			[]string{prefix + `summaryFields lists "ghost", which is not a field of the type or of its behaviors` + fields}},
		// A behavior's field is named by its qualified name: its bare name
		// is no field of the type's.
		{"a behavior's field by its bare name", func(td *ir.TypeDef) { td.Display.SummaryFields = []string{"status"} },
			[]string{prefix + `summaryFields lists "status", which is not a field of the type or of its behaviors` + fields + `; a behavior's field is named by its qualified name: Workflow.status`}},
		{"a secret summary field", func(td *ir.TypeDef) { td.Display.SummaryFields = []string{"token"} },
			[]string{prefix + "summaryFields names field token, which is secret"}},
		{"unknown states and transitions", func(td *ir.TypeDef) {
			td.Display.States["lost"] = ir.DisplayState{Label: "Lost"}
			td.Display.Transitions["done"] = map[string]string{"todo": "Reopen"}
			td.Display.Transitions["todo"]["done"] = "Skip"
		}, []string{
			prefix + `states labels "lost", which is not a state of its Workflow (todo, doing, done)`,
			prefix + "transitions labels the move from done to todo, which is not a transition of its Workflow",
			prefix + "transitions labels the move from todo to done, which is not a transition of its Workflow",
		}},
		{"states without Workflow", func(td *ir.TypeDef) {
			td.Behaviors = td.Behaviors[1:]
			td.Display.SummaryFields = []string{"Comments.commentCount"}
		}, []string{prefix + "states and transitions label a Workflow's; the type does not compose Workflow"}},
		{"transitions alone without Workflow", func(td *ir.TypeDef) {
			td.Behaviors = nil
			td.Display.SummaryFields = nil
			td.Display.States = nil
		}, []string{prefix + "states and transitions label a Workflow's; the type does not compose Workflow"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var got []string
			for _, d := range Run(displaySchema(test.change), Input{Registry: reg}).Errors {
				got = append(got, d.Error())
			}
			if strings.Join(got, "\n") != strings.Join(test.want, "\n") {
				t.Fatalf("errors:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(test.want, "\n"))
			}
		})
	}
}
