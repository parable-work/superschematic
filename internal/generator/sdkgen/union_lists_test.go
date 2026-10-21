package sdkgen

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/sdkgen/sdktest"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// loadUnionListsAPI loads union-lists-api (sdktest.LoadUnionListsService):
// the union Shape as a list, a list of lists, one value, a map, a map of
// lists, and one value or a list that may be null, none paged, with
// addUnionReadOperations' additions. Each mutate then changes the schema
// before the API is generated.
func loadUnionListsAPI(t *testing.T, mutate ...func(*ir.Schema) error) (*ir.Schema, *apigen.APIOutput, map[string]bool) {
	t.Helper()
	schema, err := sdktest.LoadUnionListsService()
	if err != nil {
		t.Fatalf("load %s: %v", sdktest.UnionListsService, err)
	}
	addUnionReadOperations(t, schema)
	for _, m := range mutate {
		if err := m(schema); err != nil {
			t.Fatal(err)
		}
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: sdktest.UnionListsService,
		Clock:      nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	tsOutput, err := tsgen.Generate(schema, tsgen.Options{SchemaName: sdktest.UnionListsService, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("tsgen.Generate: %v", err)
	}
	return schema, apiOutput, tsgen.ParseableTypeNames(tsOutput)
}

// addUnionReadOperations adds what a TypeScript caller's union needs its
// parser for to union-lists-api: Circle gets drawnAt, a Temporal.DateTime,
// so a Shape can hold a Date. ShapeQueries gets pageShapes, a page of Shape
// (two to a page, at most five); getDrawing, a Drawing whose fields hold a
// Shape and a list of them; and getMarker, one Marker, a union of the
// @strictJSON Pin and of Flag, whose raisedAt is a Temporal.DateTime, which
// every member's kind discriminates. The edit stays here, so the Go, Python
// and Rust goldens of the fixture do not change.
func addUnionReadOperations(t *testing.T, schema *ir.Schema) {
	t.Helper()
	const dateTime = "Temporal.DateTime"
	schema.Scalars[dateTime] = &ir.ScalarDef{Name: dateTime}
	if err := loader.HydrateScalars(schema, registry.CoreScalars()); err != nil {
		t.Fatalf("hydrate %s: %v", dateTime, err)
	}
	schema.Types["Circle"].Fields = append(schema.Types["Circle"].Fields,
		&ir.FieldDef{Name: "drawnAt", TypeRef: ir.TypeRef{Name: dateTime}})
	pin, flag := "pin", "flag"
	kind := func(value *string) *ir.FieldDef {
		return &ir.FieldDef{Name: "kind", TypeRef: ir.TypeRef{Name: "string"}, Required: true, InternalMetadata: true, Default: value}
	}
	schema.Types["Pin"] = &ir.TypeDef{
		Name:       "Pin",
		Role:       ir.RoleAPIView,
		StrictJSON: true,
		Fields:     []*ir.FieldDef{kind(&pin), {Name: "label", TypeRef: ir.TypeRef{Name: "string"}, Required: true}},
	}
	schema.Types["Flag"] = &ir.TypeDef{
		Name:   "Flag",
		Role:   ir.RoleAPIView,
		Fields: []*ir.FieldDef{kind(&flag), {Name: "raisedAt", TypeRef: ir.TypeRef{Name: dateTime}, Required: true}},
	}
	schema.Unions["Marker"] = &ir.UnionDef{Name: "Marker", Types: []string{"Pin", "Flag"}}
	schema.Types["Drawing"] = &ir.TypeDef{
		Name: "Drawing",
		Role: ir.RoleAPIView,
		Fields: []*ir.FieldDef{
			{Name: "title", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
			{Name: "shape", TypeRef: ir.TypeRef{Name: "Shape"}, Required: true},
			{Name: "shapes", TypeRef: ir.TypeRef{Name: "Shape", IsArray: true}, Required: true},
		},
	}
	byID := func(name, comment string, output ir.TypeRef, path string) *ir.FieldDef {
		return &ir.FieldDef{
			Name:       name,
			Comment:    comment,
			TypeRef:    output,
			Required:   true,
			Arguments:  []*ir.ArgumentDef{{Name: "id", TypeRef: ir.TypeRef{Name: "string"}, Required: true}},
			HTTPMethod: "GET",
			RestPath:   path,
		}
	}
	for _, set := range schema.OperationSets {
		if set.Name != "ShapeQueries" {
			continue
		}
		set.Operations = append(set.Operations,
			&ir.FieldDef{
				Name:       "pageShapes",
				Comment:    "Every shape, a page at a time.",
				TypeRef:    ir.TypeRef{Name: "Shape", IsArray: true},
				Required:   true,
				HTTPMethod: "GET",
				RestPath:   "shape-pages",
				Page:       &ir.OperationPage{DefaultLimit: 2, MaxLimit: 5},
			},
			byID("getDrawing", "One drawing, by its id.", ir.TypeRef{Name: "Drawing"}, "drawings/{id}"),
			byID("getMarker", "One marker, by its id.", ir.TypeRef{Name: "Marker"}, "markers/{id}"),
		)
		return
	}
	t.Fatalf("schema %s has no ShapeQueries operation set", schema.Name)
}

// writeUnionListsSDK writes the TypeScript SDK of union-lists-api into dir.
func writeUnionListsSDK(t *testing.T, apiOutput *apigen.APIOutput, parseable map[string]bool, dir string) {
	t.Helper()
	sdkOutput, err := Generate(apiOutput, parseable, nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, dir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
}

// TestWriteSDKGoldenUnionLists pins the namespace of the TypeScript SDK for
// union-lists-api with addUnionReadOperations' additions: each operation
// reads its union response, alone, in a list, a list of lists or a page,
// with the types package's parse<Union>FromJSON, and a model response with
// its parse<Type>FromJSON. listShapes has a description of two lines.
// Regenerate with:
// go test ./internal/generator/sdkgen -run TestWriteSDKGoldenUnionLists -update
func TestWriteSDKGoldenUnionLists(t *testing.T) {
	_, apiOutput, parseable := loadUnionListsAPI(t)
	outDir := t.TempDir()
	writeUnionListsSDK(t, apiOutput, parseable, outDir)
	compareGolden(t, outDir, filepath.Join("testdata", "golden", sdktest.UnionListsService), "namespaces/shape.ts")
}

// TestUnionListsSDKOutputTypes: a map response is a Record of the output
// at its list levels, and one that may be null adds | null, for the union
// and for Circle (sdktest.AddCircleOperations) alike, beside
// addUnionReadOperations' page, model and discriminated union.
func TestUnionListsSDKOutputTypes(t *testing.T) {
	_, apiOutput, parseable := loadUnionListsAPI(t, sdktest.AddCircleOperations)
	sdkOutput, err := Generate(apiOutput, parseable, nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := map[string]string{
		"listShapes":    "Shape[]",
		"shapeRows":     "Shape[][]",
		"getShape":      "Shape",
		"shapesByName":  "Record<string, Shape>",
		"shapeGroups":   "Record<string, Shape[]>",
		"findShape":     "Shape | null",
		"findShapes":    "Shape[] | null",
		"circlesByName": "Record<string, Circle>",
		"circleGroups":  "Record<string, Circle[]>",
		"findCircle":    "Circle | null",
		"pageShapes":    "Page<Shape>",
		"getDrawing":    "Drawing",
		"getMarker":     "Marker",
		"drawShape":     "Shape",
		"pay":           "string",
		"checkout":      "string",
		"drawShapes":    "string",
		"shapeGrid":     "string",
		"shapesByKey":   "string",
		"probe":         "string",
	}
	for _, ns := range sdkOutput.Namespaces {
		for _, ep := range ns.Endpoints {
			w, ok := want[ep.Name]
			if !ok {
				t.Errorf("unexpected endpoint %s", ep.Name)
				continue
			}
			delete(want, ep.Name)
			if got := ep.TSOutputType(); got != w {
				t.Errorf("%s returns %s, want %s", ep.Name, got, w)
			}
		}
	}
	if len(want) > 0 {
		t.Errorf("missing endpoints %v", want)
	}
}

// TestUnionListsSDKCompilesAndRuns type-checks the TypeScript SDK of
// union-lists-api, with sdktest.AddCircleOperations, against its generated
// types package, then runs test_union_lists.js: a list, a list of lists, a
// page, one value, a map and a map of lists of a union, and a model's union
// fields, reach the caller as their members, with a member's date-time
// field a Date, as do a map and a map of lists of Circle, parsed value by
// value; a response that may be null is null when the server answers null;
// a @strictJSON member the payload breaks is refused with the types
// package's ParseError; and union_requests.json's arguments
// (sdktest.UnionRequestsPath) are sent as they are, or refused before
// sending with the member's errors at the argument's paths, as the
// requests' sdk says.
func TestUnionListsSDKCompilesAndRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	bunPath, err := exec.LookPath("bun")
	if err != nil {
		requireOrSkipTSTooling(t, fmt.Sprintf("bun not available: %v", err))
	}
	tempRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}

	schema, apiOutput, parseable := loadUnionListsAPI(t, sdktest.AddCircleOperations)
	typesDir := writeTypesPackage(t, bunPath, schema, sdktest.UnionListsService, tempRoot)
	sdkDir := filepath.Join(tempRoot, "sdk", "typescript", sdktest.UnionListsService)
	writeUnionListsSDK(t, apiOutput, parseable, sdkDir)
	if err := CompileSDK(sdkDir, typesDir); err != nil {
		t.Fatalf("generated SDK does not type-check: %v", err)
	}

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current test file path")
	}
	requestsPath, err := sdktest.UnionRequestsPath()
	if err != nil {
		t.Fatal(err)
	}
	run := exec.Command(bunPath, "test", filepath.Join(filepath.Dir(currentFile), "test_union_lists.js"))
	run.Env = append(os.Environ(), "SDK_DIR="+sdkDir, "UNION_REQUESTS="+requestsPath)
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("test_union_lists.js failed: %v\n%s", err, out)
	}
}
