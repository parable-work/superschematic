package tsrestgen

import (
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// addAnnotateOperation adds drawing.annotate to object-lists-api: a PUT
// whose input, Annotation, holds Note, a union of the @strictJSON Pin and
// of Flag, whose raisedAt is a Temporal.DateTime, which every member's kind
// discriminates, as a field and as a list. The router parses the input with
// parse<Input>FromJSON, which reads each Note through parseNoteFromJSON. The
// edit stays here, so the fixture's golden does not change.
func addAnnotateOperation(t *testing.T, schema *ir.Schema) {
	t.Helper()
	const dateTime = "Temporal.DateTime"
	schema.Scalars[dateTime] = &ir.ScalarDef{Name: dateTime}
	if err := loader.HydrateScalars(schema, registry.CoreScalars()); err != nil {
		t.Fatalf("hydrate %s: %v", dateTime, err)
	}
	pin, flag := "pin", "flag"
	kind := func(value *string) *ir.FieldDef {
		return &ir.FieldDef{Name: "kind", TypeRef: ir.TypeRef{Name: "string"}, Required: true, InternalMetadata: true, Default: value}
	}
	schema.Types["Pin"] = &ir.TypeDef{
		Name:       "Pin",
		Role:       ir.RoleEmbeddedStruct,
		StrictJSON: true,
		Fields:     []*ir.FieldDef{kind(&pin), {Name: "label", TypeRef: ir.TypeRef{Name: "string"}, Required: true}},
	}
	schema.Types["Flag"] = &ir.TypeDef{
		Name:   "Flag",
		Role:   ir.RoleEmbeddedStruct,
		Fields: []*ir.FieldDef{kind(&flag), {Name: "raisedAt", TypeRef: ir.TypeRef{Name: dateTime}, Required: true}},
	}
	schema.Unions["Note"] = &ir.UnionDef{Name: "Note", Types: []string{"Pin", "Flag"}}
	schema.Types["Annotation"] = &ir.TypeDef{
		Name: "Annotation",
		Role: ir.RoleAPIInput,
		Fields: []*ir.FieldDef{
			{Name: "note", TypeRef: ir.TypeRef{Name: "Note"}, Required: true},
			{Name: "notes", TypeRef: ir.TypeRef{Name: "Note", IsArray: true}},
		},
	}
	addDrawingOperation(t, schema, &ir.FieldDef{
		Name:    "annotate",
		TypeRef: ir.TypeRef{Name: "string"},
		Arguments: []*ir.ArgumentDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
			{Name: "annotation", TypeRef: ir.TypeRef{Name: "Annotation"}, Required: true},
		},
		Required: true,
		RestPath: "drawings/{id}/annotation",
	})
}

// TestGenerateUnionFieldInput: an input whose fields hold a union is parsed
// by its generated strict decoder and parse<Input>FromJSON, which reads the
// union fields; the router names nothing for the union itself.
func TestGenerateUnionFieldInput(t *testing.T) {
	schema := loadObjectListsAPI(t)
	addAnnotateOperation(t, schema)
	annotate := endpointsByName(generateFixture(t, objectListsFixture(t, schema)))["annotate"]
	if !annotate.HasInput || annotate.InputParser != "parseAnnotationFromJSON" || annotate.InputValidator != "parseAnnotationJson" {
		t.Fatalf("annotate input = %t %s(%s)", annotate.HasInput, annotate.InputParser, annotate.InputValidator)
	}
	if len(annotate.BodyParams) != 0 || strings.Contains(annotate.OutputType, "Note") {
		t.Errorf("annotate body params = %+v, output %s", annotate.BodyParams, annotate.OutputType)
	}
}

// TestGeneratedUnionFieldsRouter type-checks the generated package for
// object-lists-api with drawing.annotate and drives it under bun over HTTP:
// a Note in the input reaches the implementation as its member, with a
// Flag's raisedAt a Date, and a Pin the body breaks, or a kind no member
// has, is refused with the 400 of an input the parser refuses.
func TestGeneratedUnionFieldsRouter(t *testing.T) {
	schema := loadObjectListsAPI(t)
	addAnnotateOperation(t, schema)
	tree := materializeAPI(t, objectListsFixture(t, schema))
	tree.typeCheck(t)
	tree.runTest(t, "union_fields_runtime.test.ts")
}
