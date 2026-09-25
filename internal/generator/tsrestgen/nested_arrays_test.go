package tsrestgen

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

const nestedArraysAPI = "fixture-nested-arrays-api"

func nestedList(name string) ir.TypeRef {
	return ir.TypeRef{Name: name, IsArray: true, IsArrayOfArrays: true}
}

func loadNestedArraysAPI(t *testing.T) *ir.Schema {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, nestedArraysAPI))
	if err != nil {
		t.Fatalf("load %s: %v", nestedArraysAPI, err)
	}
	return schema
}

// addPaintOperation adds grid.paint to the loaded fixture-nested-arrays-api
// schema: a PUT whose body arguments are a required list of lists of the
// Shade enum and an optional list of lists of the Point object, and whose
// response is a list of lists of Point. The fixture's own list-of-lists
// argument and response are strings; paint covers the element types that
// carry their own decoding.
func addPaintOperation(t *testing.T, schema *ir.Schema) {
	t.Helper()
	for _, set := range schema.OperationSets {
		if set.Name != "GridMutations" {
			continue
		}
		set.Operations = append(set.Operations, &ir.FieldDef{
			Name:     "paint",
			Comment:  "Paint a grid's cells and polygons; returns the stored polygons.",
			TypeRef:  nestedList("Point"),
			Required: true,
			Arguments: []*ir.ArgumentDef{
				{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
				{Name: "shades", TypeRef: nestedList("Shade"), Required: true},
				{Name: "polygons", TypeRef: nestedList("Point")},
			},
			HTTPMethod: "PUT",
			RestPath:   "grids/{id}/paint",
		})
		return
	}
	t.Fatalf("schema %s has no GridMutations operation set", schema.Name)
}

// nestedArraysFixture is fixture-nested-arrays-api with apigen's endpoints
// for it; withPaint adds grid.paint first.
func nestedArraysFixture(t *testing.T, withPaint bool) apiFixture {
	t.Helper()
	schema := loadNestedArraysAPI(t)
	if withPaint {
		addPaintOperation(t, schema)
	}
	endpoints, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: nestedArraysAPI,
		Clock:      fixedClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return apiFixture{name: nestedArraysAPI, schema: schema, endpoints: endpoints}
}

func generateFixture(t *testing.T, fixture apiFixture) *APIOutput {
	t.Helper()
	output, err := Generate(fixture.schema, fixture.endpoints, Options{SchemaName: fixture.name, Clock: fixedClock})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if output == nil {
		t.Fatalf("expected TypeScript API output for %s", fixture.name)
	}
	return output
}

func endpointsByName(output *APIOutput) map[string]EndpointInfo {
	byName := map[string]EndpointInfo{}
	for _, ep := range output.Endpoints {
		byName[ep.Name] = ep
	}
	return byName
}

// TestWriteAPIGoldenNestedArrays pins the package for the arrays-of-arrays
// fixture: an input type whose fields are lists of lists, a PUT whose body
// argument is string[][], and a bare string[][] response. Regenerate with
// go test ./internal/generator/tsrestgen -run TestWriteAPIGoldenNestedArrays -update
func TestWriteAPIGoldenNestedArrays(t *testing.T) {
	checkGolden(t, generateFixture(t, nestedArraysFixture(t, false)), nestedArraysAPI)
}

// TestGenerateNestedArraysShape: a T[][] body argument and response render
// with two list levels, and the operation table marks both for the runtime.
func TestGenerateNestedArraysShape(t *testing.T) {
	byName := endpointsByName(generateFixture(t, nestedArraysFixture(t, false)))

	replace := byName["replaceLabels"]
	if replace.Method != "PUT" || len(replace.BodyParams) != 1 {
		t.Fatalf("replaceLabels = %s with body params %+v", replace.Method, replace.BodyParams)
	}
	labels := replace.BodyParams[0]
	if labels.TSType != "string[][]" || labels.Kind != "string" || !labels.IsArray || !labels.IsArrayOfArrays || !labels.Required {
		t.Errorf("replaceLabels labels = %+v", labels)
	}
	if want := "{ name: 'labels', kind: 'string', required: true, isArray: true, isArrayOfArrays: true }"; labels.SpecLiteral != want {
		t.Errorf("labels spec = %s, want %s", labels.SpecLiteral, want)
	}
	if replace.OutputType != "GridView" || replace.OutputIsArrayOfArrays {
		t.Errorf("replaceLabels output = %q (list of lists %t)", replace.OutputType, replace.OutputIsArrayOfArrays)
	}

	gridLabels := byName["gridLabels"]
	if gridLabels.OutputType != "string[][]" || !gridLabels.OutputIsArrayOfArrays {
		t.Errorf("gridLabels output = %q (list of lists %t)", gridLabels.OutputType, gridLabels.OutputIsArrayOfArrays)
	}
	if len(gridLabels.QueryParams) != 1 || gridLabels.QueryParams[0].IsArrayOfArrays {
		t.Errorf("gridLabels query params = %+v", gridLabels.QueryParams)
	}

	save := byName["saveGrid"]
	if !save.HasInput || save.InputType != "SaveGridInput" || save.InputValidator != "parseSaveGridInputJson" || save.OutputIsArrayOfArrays {
		t.Errorf("saveGrid = %+v", save)
	}
}

// TestGenerateNestedArraysElementKinds: an enum element carries its values,
// and an object element is parsed by the generated strict parser of its
// type, which the router imports from the types package's validators.
func TestGenerateNestedArraysElementKinds(t *testing.T) {
	output := generateFixture(t, nestedArraysFixture(t, true))
	paint := endpointsByName(output)["paint"]
	if len(paint.BodyParams) != 2 {
		t.Fatalf("paint body params = %+v", paint.BodyParams)
	}
	shades, polygons := paint.BodyParams[0], paint.BodyParams[1]
	if want := "{ name: 'shades', kind: 'enum', required: true, isArray: true, isArrayOfArrays: true, enumValues: ['light', 'dark'] }"; shades.TSType != "Shade[][]" || shades.SpecLiteral != want {
		t.Errorf("shades = %s %s, want Shade[][] %s", shades.TSType, shades.SpecLiteral, want)
	}
	if want := "{ name: 'polygons', kind: 'object', required: false, isArray: true, isArrayOfArrays: true, parse: (value: unknown) => parsePointFromJSON(parsePointJson(value)) }"; polygons.TSType != "Point[][]" || polygons.SpecLiteral != want {
		t.Errorf("polygons = %s %s, want Point[][] %s", polygons.TSType, polygons.SpecLiteral, want)
	}
	if paint.OutputType != "Point[][]" || !paint.OutputIsArrayOfArrays {
		t.Errorf("paint output = %q (list of lists %t)", paint.OutputType, paint.OutputIsArrayOfArrays)
	}

	importsOf := func(imports []PackageImport) string {
		var parts []string
		for _, imp := range imports {
			parts = append(parts, imp.Package+": "+strings.Join(imp.Symbols, ","))
		}
		return strings.Join(parts, "; ")
	}
	const pkg = "@schemas/fixture-nested-arrays-api-types: "
	if got, want := importsOf(output.ValidatorImports), pkg+"parsePointFromJSON,parsePointJson,parseSaveGridInputFromJSON,parseSaveGridInputJson"; got != want {
		t.Errorf("validator imports = %s, want %s", got, want)
	}
	if got, want := importsOf(output.RouterTypeImports), pkg+"Point,SaveGridInput,Shade"; got != want {
		t.Errorf("router type imports = %s, want %s", got, want)
	}
}

// TestGenerateRefusesNestedArraysWithoutParser: a list of lists whose
// element has no generated parser (a union) is refused with the argument
// named, never rendered as a string list.
func TestGenerateRefusesNestedArraysWithoutParser(t *testing.T) {
	schema := loadNestedArraysAPI(t)
	schema.Unions["Mark"] = &ir.UnionDef{Name: "Mark", Types: []string{"Point", "GridView"}}
	for _, set := range schema.OperationSets {
		if set.Name != "GridMutations" {
			continue
		}
		set.Operations = append(set.Operations, &ir.FieldDef{
			Name:       "mark",
			TypeRef:    ir.TypeRef{Name: "GridView"},
			Arguments:  []*ir.ArgumentDef{{Name: "marks", TypeRef: nestedList("Mark"), Required: true}},
			HTTPMethod: "PUT",
			RestPath:   "grids/marks",
		})
	}
	endpoints, err := apigen.Generate(schema, apigen.Options{Provider: sessionauth.Provider{}, SchemaName: nestedArraysAPI, Clock: fixedClock})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	_, err = Generate(schema, endpoints, Options{SchemaName: nestedArraysAPI})
	want := "tsrestgen does not support arrays of arrays yet (grid.mark(marks)): element type Mark is not a scalar, enum or object type"
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

// TestGeneratedNestedArraysRouter type-checks the generated package for
// fixture-nested-arrays-api (with grid.paint) and drives it under bun over
// HTTP: nested bodies round-trip, a null inner list and a bad element are
// refused at their index, and list-of-lists responses keep their shape.
func TestGeneratedNestedArraysRouter(t *testing.T) {
	tree := materializeAPI(t, nestedArraysFixture(t, true))
	tree.typeCheck(t)
	tree.runTest(t, "nested_arrays_runtime.test.ts")
}
