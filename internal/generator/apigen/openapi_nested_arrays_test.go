package apigen

import (
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestOpenAPIArrayOfArraysConstraints: on T[][] the field's value
// constraints and the scalar's own go on the innermost items, two levels
// down, and the list bounds stay on the outer array. The inner array
// carries neither.
func TestOpenAPIArrayOfArraysConstraints(t *testing.T) {
	listMin, listMax := 1, 64
	minimum, maximum := 0.0, 1.0
	schema := ir.NewSchema("nested-contract", ir.SchemaKindGeneral)
	schema.Scalars["Example.Handle"] = &ir.ScalarDef{
		Name: "Example.Handle", LanguagePrimitive: ir.LanguageString,
		Pattern: "^[a-z]+$", MaxLength: 40,
	}
	def := &ir.TypeDef{Name: "Grid", Fields: []*ir.FieldDef{
		{
			Name: "weights", TypeRef: ir.TypeRef{Name: "number", IsArray: true, IsArrayOfArrays: true},
			ValidateMin: &minimum, ValidateMax: &maximum,
			ValidateListMin: &listMin, ValidateListMax: &listMax,
		},
		{
			Name: "handles", TypeRef: ir.TypeRef{Name: "Example.Handle", IsArray: true, IsArrayOfArrays: true},
			Required: true,
		},
	}}
	result := openAPITypeSchema(def, map[string]interface{}{}, schema, nil, nil, nil, buildOpenAPIScalarMap(schema, nil), map[string]bool{})
	properties := result["properties"].(map[string]interface{})

	weights := properties["weights"].(map[string]interface{})
	if weights["type"] != "array" || weights["nullable"] != true || weights["minItems"] != 1 || weights["maxItems"] != 64 {
		t.Fatalf("outer weights = %#v, want a nullable array with minItems 1 and maxItems 64", weights)
	}
	for _, misplaced := range []string{"minimum", "maximum"} {
		if _, present := weights[misplaced]; present {
			t.Fatalf("outer array carries %s: %#v", misplaced, weights)
		}
	}
	inner := weights["items"].(map[string]interface{})
	if inner["type"] != "array" {
		t.Fatalf("inner weights = %#v, want an array", inner)
	}
	for _, misplaced := range []string{"minimum", "maximum", "minItems", "maxItems", "nullable"} {
		if _, present := inner[misplaced]; present {
			t.Fatalf("inner array carries %s: %#v", misplaced, inner)
		}
	}
	element := inner["items"].(map[string]interface{})
	if element["type"] != "number" || element["minimum"] != 0.0 || element["maximum"] != 1.0 {
		t.Fatalf("weights element = %#v, want a number in [0, 1]", element)
	}

	handles := properties["handles"].(map[string]interface{})
	handleElement := handles["items"].(map[string]interface{})["items"].(map[string]interface{})
	if handleElement["pattern"] != "^[a-z]+$" || handleElement["maxLength"] != 40 {
		t.Fatalf("handles element = %#v, want the scalar's pattern and maxLength", handleElement)
	}
	if _, present := handles["pattern"]; present {
		t.Fatalf("outer handles array carries the scalar pattern: %#v", handles)
	}
}

// TestOpenAPIArrayOfArraysOfObjectsReferencesComponent: the items of the
// inner array of an object type are a component reference, and the
// component is emitted.
func TestOpenAPIArrayOfArraysOfObjectsReferencesComponent(t *testing.T) {
	schema := ir.NewSchema("nested-contract", ir.SchemaKindGeneral)
	schema.Types["Point"] = &ir.TypeDef{Name: "Point", Fields: []*ir.FieldDef{
		{Name: "x", TypeRef: ir.TypeRef{Name: "number"}, Required: true},
	}}
	schema.Types["Shape"] = &ir.TypeDef{Name: "Shape", Fields: []*ir.FieldDef{
		{Name: "polygons", TypeRef: ir.TypeRef{Name: "Point", IsArray: true, IsArrayOfArrays: true}, Required: true},
	}}
	schemas := map[string]interface{}{}
	addSchemaFromIR(schemas, "Shape", schema, nil, nil, nil, buildOpenAPIScalarMap(schema, nil), map[string]bool{})
	polygons := schemas["Shape"].(map[string]interface{})["properties"].(map[string]interface{})["polygons"].(map[string]interface{})
	ref := polygons["items"].(map[string]interface{})["items"].(map[string]interface{})["$ref"]
	if ref != "#/components/schemas/Point" {
		t.Fatalf("polygons = %#v, want items of items referencing Point", polygons)
	}
	if _, ok := schemas["Point"]; !ok {
		t.Fatal("Point component was not emitted")
	}
}

// TestArraysOfArraysOutsideRequestBodies: apigen refuses an array of arrays
// in a path or query parameter and in a GET argument even when the IR did
// not come through the loader, which refuses them first. A list argument,
// nested or not, is never taken as a path parameter or as the operation's
// input type.
func TestArraysOfArraysOutsideRequestBodies(t *testing.T) {
	nested := func(name string) ir.TypeRef {
		return ir.TypeRef{Name: name, IsArray: true, IsArrayOfArrays: true}
	}
	newSchema := func(op *ir.FieldDef) *ir.Schema {
		schema := ir.NewSchema("nested-contract", ir.SchemaKindAPI)
		schema.Scalars["Identity.UUID"] = &ir.ScalarDef{Name: "Identity.UUID", LanguagePrimitive: ir.LanguageString}
		schema.Types["Point"] = &ir.TypeDef{Name: "Point", Role: ir.RoleAPIInput, Fields: []*ir.FieldDef{
			{Name: "x", TypeRef: ir.TypeRef{Name: "number"}, Required: true},
		}}
		schema.OperationSets = []*ir.OperationSet{{Name: "GridMutations", Operations: []*ir.FieldDef{op}}}
		return schema
	}
	generate := func(op *ir.FieldDef) (*APIOutput, error) {
		return Generate(newSchema(op), Options{Provider: stubProvider{}, SchemaName: "nested-contract"})
	}

	for _, tc := range []struct {
		name string
		op   *ir.FieldDef
		want string
	}{
		{
			name: "query parameter",
			op: &ir.FieldDef{Name: "list", HTTPMethod: "GET", RestPath: "grids", TypeRef: ir.TypeRef{Name: "string"},
				Arguments: []*ir.ArgumentDef{{Name: "rows", TypeRef: nested("string"), IsQuery: true, Required: true}}},
			want: "argument rows: a query parameter cannot be an array of arrays",
		},
		{
			name: "path parameter",
			op: &ir.FieldDef{Name: "save", HTTPMethod: "POST", RestPath: "grids/{rows}", TypeRef: ir.TypeRef{Name: "string"},
				Arguments: []*ir.ArgumentDef{{Name: "rows", TypeRef: nested("string"), Required: true}}},
			want: "argument rows: a path parameter cannot be an array of arrays",
		},
		{
			name: "GET argument",
			op: &ir.FieldDef{Name: "list", HTTPMethod: "GET", RestPath: "grids", TypeRef: ir.TypeRef{Name: "string"},
				Arguments: []*ir.ArgumentDef{{Name: "rows", TypeRef: nested("string"), Required: true}}},
			want: "argument rows: a GET operation sends its arguments in the query string",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := generate(tc.op)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}

	// A multipart request has no list of lists of files.
	uploads := newSchema(&ir.FieldDef{Name: "upload", HTTPMethod: "POST", RestPath: "uploads", TypeRef: ir.TypeRef{Name: "string"},
		Arguments: []*ir.ArgumentDef{{Name: "input", TypeRef: ir.TypeRef{Name: "UploadInput"}, Required: true}}})
	uploads.Scalars["Asset.File"] = &ir.ScalarDef{Name: "Asset.File", LanguagePrimitive: ir.LanguageString}
	uploads.Types["UploadInput"] = &ir.TypeDef{Name: "UploadInput", Role: ir.RoleAPIInput, Fields: []*ir.FieldDef{
		{Name: "attachments", TypeRef: nested("Asset.File"), Required: true},
	}}
	if _, err := Generate(uploads, Options{Provider: stubProvider{}, SchemaName: "nested-contract"}); err == nil ||
		!strings.Contains(err.Error(), "input field UploadInput.attachments: a file upload cannot be an array of arrays") {
		t.Fatalf("nested file upload: err = %v", err)
	}

	// Without a rest path a UUID argument becomes a path parameter and a
	// Point argument the input type; lists of them are body arguments.
	single := func(ref ir.TypeRef) EndpointInfo {
		output, err := generate(&ir.FieldDef{Name: "save", HTTPMethod: "POST", TypeRef: ir.TypeRef{Name: "string"},
			Arguments: []*ir.ArgumentDef{{Name: "value", TypeRef: ref, Required: true}}})
		if err != nil {
			t.Fatalf("%+v: %v", ref, err)
		}
		return output.Endpoints[0]
	}
	if endpoint := single(ir.TypeRef{Name: "Identity.UUID"}); len(endpoint.PathParams) != 1 {
		t.Fatalf("a UUID argument without a rest path: path params %+v, want one", endpoint.PathParams)
	}
	if endpoint := single(ir.TypeRef{Name: "Point"}); endpoint.InputType != "Point" {
		t.Fatalf("a Point argument: input type %q, want Point", endpoint.InputType)
	}
	for _, ref := range []ir.TypeRef{
		nested("Identity.UUID"), {Name: "Identity.UUID", IsArray: true},
		nested("Point"), {Name: "Point", IsArray: true},
	} {
		op := &ir.FieldDef{Name: "save", HTTPMethod: "POST", TypeRef: ir.TypeRef{Name: "string"},
			Arguments: []*ir.ArgumentDef{{Name: "values", TypeRef: ref, Required: true}}}
		output, err := generate(op)
		if err != nil {
			t.Fatalf("%+v: %v", ref, err)
		}
		endpoint := output.Endpoints[0]
		if len(endpoint.PathParams) != 0 || endpoint.HasInput || len(endpoint.ScalarArgs) != 1 {
			t.Fatalf("%+v: path %+v, input %q, body %+v; want one body argument", ref, endpoint.PathParams, endpoint.InputType, endpoint.ScalarArgs)
		}
		if got, want := endpoint.ScalarArgs[0].ArrayDepth(), ref.ArrayDepth(); got != want {
			t.Fatalf("%+v: body argument depth %d, want %d", ref, got, want)
		}
	}
}
