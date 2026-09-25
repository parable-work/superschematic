package apigen_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// generateNestedArraysAPI generates the API output for
// fixture-nested-arrays-api, whose body fields, body argument and bare
// response are arrays of arrays.
func generateNestedArraysAPI(t *testing.T) *apigen.APIOutput {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-nested-arrays-api"))
	if err != nil {
		t.Fatalf("load fixture-nested-arrays-api: %v", err)
	}
	output, err := apigen.Generate(schema, apigen.Options{
		Provider:    sessionauth.Provider{},
		SchemaName:  "fixture-nested-arrays-api",
		ModulePath:  "example.com/schemas/api/fixture-nested-arrays-api",
		TypesModule: "example.com/schemas/types/go/fixture-nested-arrays-api",
		Clock:       codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if output == nil {
		t.Fatal("expected API output for fixture-nested-arrays-api")
	}
	return output
}

// TestWriteAPIGoldenNestedArrays pins the route code and the OpenAPI
// document for arrays of arrays: [][]T in the handler signatures and the
// decoded body, element validation reported at name[i][j], and two levels
// of array items in the OpenAPI schemas. Regenerate with:
// go test ./internal/generator/apigen -run TestWriteAPIGoldenNestedArrays -update
func TestWriteAPIGoldenNestedArrays(t *testing.T) {
	output := generateNestedArraysAPI(t)
	outDir := t.TempDir()
	paths := naming.LocalPaths{
		ScalarGo:        "/repo/third_party/superscalar/go",
		SchemaIR:        "/repo/ir",
		SchemaRuntimeGo: "/repo/runtime/schema/go",
		HTTPRuntimeGo:   "/repo/runtime/http/go",
	}
	if err := apigen.SetReplacePaths(output, paths, "/repo/schemas/dist/api/fixture-nested-arrays-api"); err != nil {
		t.Fatalf("set replace paths: %v", err)
	}
	if err := apigen.WriteAPI(output, outDir); err != nil {
		t.Fatalf("write api: %v", err)
	}
	scaffoldsDir := filepath.Join(outDir, "scaffolds")
	if _, err := apigen.WriteScaffolds(output, scaffoldsDir); err != nil {
		t.Fatalf("write scaffolds: %v", err)
	}

	checkGoldenFiles(t, outDir, filepath.Join("testdata", "golden", "fixture-nested-arrays-api"), []string{
		"interfaces.go",
		"routes.go",
		"openapi.json",
		"scaffolds/grid/replace_labels.go",
		"scaffolds/grid/grid_labels.go",
	})
}

// TestTypeOpenAPISchemaNestedArrays pins the standalone OpenAPI schema of a
// General type with arrays of arrays of a scalar, an enum and an object,
// and an optional one whose list bound sits on the outer array. Regenerate
// with -update.
func TestTypeOpenAPISchemaNestedArrays(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-nested-arrays"))
	if err != nil {
		t.Fatalf("load fixture-nested-arrays: %v", err)
	}
	drawing, err := apigen.TypeOpenAPISchema("Drawing", schema, nil)
	if err != nil {
		t.Fatalf("TypeOpenAPISchema: %v", err)
	}
	encoded, err := json.MarshalIndent(drawing, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outDir, "Drawing.openapi.json"), append(encoded, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	checkGoldenFiles(t, outDir, filepath.Join("testdata", "golden", "fixture-nested-arrays"), []string{"Drawing.openapi.json"})
}

// TestNestedArraysAPIShape: the body argument of replaceLabels and the
// response of gridLabels keep their depth on the API output, which the SDK
// generators read.
func TestNestedArraysAPIShape(t *testing.T) {
	output := generateNestedArraysAPI(t)
	byName := map[string]apigen.EndpointInfo{}
	for _, endpoint := range output.Endpoints {
		byName[endpoint.Name] = endpoint
	}
	replace := byName["replaceLabels"]
	if len(replace.PathParams) != 1 || replace.PathParams[0].Name != "id" {
		t.Fatalf("replaceLabels path params = %+v", replace.PathParams)
	}
	if len(replace.ScalarArgs) != 1 || replace.ScalarArgs[0].ArrayDepth() != 2 || replace.ScalarArgs[0].GoListType() != "[][]string" {
		t.Fatalf("replaceLabels body args = %+v", replace.ScalarArgs)
	}
	labels := byName["gridLabels"]
	if labels.OutputArrayDepth() != 2 || labels.OutputGoListType() != "[][]string" {
		t.Fatalf("gridLabels output depth %d, Go type %q", labels.OutputArrayDepth(), labels.OutputGoListType())
	}
	if len(replace.BodyArgs) != 1 || replace.BodyArgs[0].Decoder() != "ListOfLists" || replace.BodyArgs[0].Kind != "String" {
		t.Fatalf("replaceLabels decodes its body arguments as %+v", replace.BodyArgs)
	}
	for _, field := range output.TypeFields["SaveGridInput"] {
		if field.Name == "polygons" && (field.ArrayDepth() != 2 || field.Type != "Point") {
			t.Fatalf("SaveGridInput.polygons = %+v", field)
		}
	}
}

// TestNestedArraysRouteValidatesElements: a body argument that is an array
// of arrays of an enum or an object is decoded by the HTTP runtime's
// bodyargs.ListOfLists, which validates each element at name[i][j], and a
// T[][] response of objects sends a nil inner list as [].
func TestNestedArraysRouteValidatesElements(t *testing.T) {
	nested := func(name string) ir.TypeRef { return ir.TypeRef{Name: name, IsArray: true, IsArrayOfArrays: true} }
	schema := ir.NewSchema("nested-routes", ir.SchemaKindAPI)
	schema.Enums["Shade"] = &ir.EnumDef{Name: "Shade", Values: []ir.EnumValueDef{{Name: "light"}, {Name: "dark"}}}
	schema.Types["Point"] = &ir.TypeDef{Name: "Point", Role: ir.RoleAPIView, Fields: []*ir.FieldDef{
		{Name: "x", TypeRef: ir.TypeRef{Name: "number"}, Required: true},
	}}
	schema.OperationSets = []*ir.OperationSet{{Name: "GridMutations", Operations: []*ir.FieldDef{{
		Name: "paint", HTTPMethod: "PUT", RestPath: "grids/paint", TypeRef: nested("Point"),
		Arguments: []*ir.ArgumentDef{
			{Name: "shades", TypeRef: nested("Shade"), Required: true},
			{Name: "polygons", TypeRef: nested("Point")},
		},
	}}}}
	output, err := apigen.Generate(schema, apigen.Options{
		Provider:    sessionauth.Provider{},
		SchemaName:  "nested-routes",
		ModulePath:  "example.com/schemas/api/nested-routes",
		TypesModule: "example.com/schemas/types/go/nested-routes",
		Clock:       codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !output.HasBodyArgs() {
		t.Fatal("HasBodyArgs = false")
	}
	outDir := t.TempDir()
	if err := apigen.WriteAPI(output, outDir); err != nil {
		t.Fatalf("write api: %v", err)
	}
	routes, err := os.ReadFile(filepath.Join(outDir, "routes.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Shades   [][]types.Shade\n",
		"Polygons [][]types.Point\n",
		`bodyShadesArg := bodyargs.NewArg("shades", bodyargs.String, bodyargs.Required())`,
		`bodyPolygonsArg := bodyargs.NewArg("polygons", bodyargs.Object)`,
		`requestBody.Shades = bodyargs.ListOfLists[types.Shade](validationErrors, body, bodyShadesArg)`,
		`requestBody.Polygons = bodyargs.ListOfLists[types.Point](validationErrors, body, bodyPolygonsArg)`,
		"result[i] = []types.Point{}",
	} {
		if !strings.Contains(string(routes), want) {
			t.Errorf("routes.go lacks %s", want)
		}
	}
}

// checkGoldenFiles compares each named file under outDir with its copy
// under goldenDir, or rewrites the copies with -update.
func checkGoldenFiles(t *testing.T, outDir, goldenDir string, files []string) {
	t.Helper()
	for _, name := range files {
		got, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatalf("read generated %s: %v", name, err)
		}
		goldenPath := filepath.Join(goldenDir, name)
		if *update {
			if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
				t.Fatalf("create golden dir: %v", err)
			}
			if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
				t.Fatalf("write golden %s: %v", name, err)
			}
			continue
		}
		want, err := os.ReadFile(goldenPath)
		if err != nil {
			t.Fatalf("read golden %s: %v", name, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from golden (run with -update to accept)", name)
		}
	}
}
