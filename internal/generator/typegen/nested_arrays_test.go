package typegen

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// writeNestedModule generates the Go types of schema into a temp module
// wired against the real superscalar module and returns its directory.
func writeNestedModule(t *testing.T, service string, schema *ir.Schema) (string, *ModuleOutput) {
	t.Helper()
	output, err := Generate(schema, Options{
		SchemaName: service,
		ModulePath: "example.com/schemas/types/go/" + service,
		Clock:      codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("generate %s: %v", service, err)
	}
	dir := filepath.Join(t.TempDir(), service)
	if err := SetReplacePaths(output, testpaths.Local(t), dir); err != nil {
		t.Fatalf("set replace paths: %v", err)
	}
	if err := WriteTypes(output, dir); err != nil {
		t.Fatalf("write %s: %v", service, err)
	}
	return dir, output
}

// vetAndTestModule adds code as a test file to the generated module in dir,
// then runs go vet and go test on the module.
func vetAndTestModule(t *testing.T, dir, file, code string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(code), 0o644); err != nil {
		t.Fatal(err)
	}
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = dir
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Skipf("go mod tidy failed (likely offline): %v\n%s", err, out)
	}
	for _, args := range [][]string{{"vet", "./..."}, {"test", "-count=1", "./..."}} {
		command := exec.Command("go", args...)
		command.Dir = dir
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("go %s in the generated module: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
}

// nestedModuleTestHelpers is shared by the tests dropped into generated
// modules: errorKeys lists the paths a ValidationErrors reports.
const nestedModuleTestHelpers = `
func errorKeys(errs ValidationErrors) []string {
	keys := make([]string, 0, len(errs))
	for key := range errs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
`

// TestNestedArraysFixtureModules generates the Go types of the three
// fixture-nested-arrays services, then builds, vets and tests each module
// with a test that decodes and validates lists of lists: ragged rows, empty
// outer and inner lists, a null inner list (rejected at field[i]) and a bad
// element (reported at field[i][j]).
func TestNestedArraysFixtureModules(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping generated-module test run in -short mode")
	}
	cases := []struct {
		service string
		code    string
	}{
		{"fixture-nested-arrays", drawingModuleTest},
		{"fixture-nested-arrays-db", boardModuleTest},
		{"fixture-nested-arrays-api", gridModuleTest},
	}
	for _, tc := range cases {
		t.Run(tc.service, func(t *testing.T) {
			schema, err := loader.LoadService(filepath.Join(fixturesDir, tc.service))
			if err != nil {
				t.Fatalf("load %s: %v", tc.service, err)
			}
			dir, _ := writeNestedModule(t, tc.service, schema)
			vetAndTestModule(t, dir, "nested_lists_test.go", tc.code+nestedModuleTestHelpers)
		})
	}
}

const drawingModuleTest = `package types

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestDrawingListsOfLists(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    map[string]string
	}{
		{"ragged rows", ` + "`" + `{"labels":[["a","b","c"],["d"],[]],"shades":[["light"],["dark","light"]],"polygons":[[{"x":0,"y":0},{"x":1,"y":0},{"x":0,"y":1}],[]],"samples":[[0.5],[1,2,3]]}` + "`" + `, nil},
		{"empty outer lists", ` + "`" + `{"labels":[],"shades":[],"polygons":[]}` + "`" + `, nil},
		{"empty inner lists", ` + "`" + `{"labels":[[]],"shades":[[],[]],"polygons":[[]],"samples":[[]]}` + "`" + `, nil},
		{"null inner lists", ` + "`" + `{"labels":[["a"],null],"shades":[null],"polygons":[[],null],"samples":[null]}` + "`" + `,
			map[string]string{"labels[1]": "required", "shades[0]": "required", "polygons[1]": "required", "samples[0]": "required"}},
		{"bad element", ` + "`" + `{"labels":[["a"]],"shades":[["light"],["dark","purple"]],"polygons":[]}` + "`" + `,
			map[string]string{"shades[1][1]": "enum"}},
		{"absent required lists", ` + "`" + `{}` + "`" + `,
			map[string]string{"labels": "required", "shades": "required", "polygons": "required"}},
		{"outer list bound", ` + "`" + `{"labels":[],"shades":[],"polygons":[],"samples":[` + "`" + ` + strings.Repeat("[1],", 64) + ` + "`" + `[1]]}` + "`" + `,
			map[string]string{"samples": "listMax"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var drawing Drawing
			if err := json.Unmarshal([]byte(tc.payload), &drawing); err != nil {
				t.Fatalf("decode: %v", err)
			}
			errs := drawing.Validate()
			got := map[string]string{}
			for _, key := range errorKeys(errs) {
				got[key] = errs.GetFieldErrors(key)[0].Validator
			}
			if len(tc.want) == 0 && len(got) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("errors = %v, want %v", got, tc.want)
			}
		})
	}

	var ragged Drawing
	if err := ragged.FromJSON([]byte(` + "`" + `{"labels":[["a","b"],[],["c"]],"shades":[],"polygons":[[{"x":1,"y":2}]]}` + "`" + `)); err != nil {
		t.Fatalf("strict decode: %v", err)
	}
	if len(ragged.Labels) != 3 || len(ragged.Labels[0]) != 2 || ragged.Labels[1] == nil || len(ragged.Labels[1]) != 0 {
		t.Fatalf("ragged labels = %#v; an empty inner list must decode as an empty, non-nil slice", ragged.Labels)
	}
	if ragged.Polygons[0][0] != (Point{X: 1, Y: 2}) {
		t.Fatalf("polygons[0][0] = %#v", ragged.Polygons[0][0])
	}

	// A nil inner list built in Go encodes as [], like a nil list field.
	built := Drawing{Labels: [][]string{{"a"}, nil}, Shades: [][]Shade{{Shade_Dark}}, Polygons: [][]Point{}}
	masked := built.MaskSecrets()
	data, err := json.Marshal(&built)
	if err != nil {
		t.Fatal(err)
	}
	if want := ` + "`" + `{"labels":[["a"],[]],"shades":[["dark"]],"polygons":[]}` + "`" + `; string(data) != want {
		t.Fatalf("encoded %s, want %s", data, want)
	}

	// MaskSecrets copies each inner list rather than sharing it.
	masked.Labels[0][0] = "changed"
	if built.Labels[0][0] != "a" || masked.Labels[1] != nil {
		t.Fatalf("masked copy shares or fills inner lists: built %v, masked %#v", built.Labels, masked.Labels)
	}
}
`

const boardModuleTest = `package types

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
)

func TestBoardListsOfLists(t *testing.T) {
	var board Board
	payload := ` + "`" + `{"labels":[["a"],[]],"states":[["empty","filled"],["bogus"]],"walls":[[{"x":0,"y":0}],null],"scores":[[1,2],[]]}` + "`" + `
	if err := json.Unmarshal([]byte(payload), &board); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got, want := errorKeys(board.Validate()), []string{"states[1][0]", "walls[1]"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("errors at %v, want %v", got, want)
	}
	if board.Scores[1] == nil || board.Walls[0][0] != (BoardPoint{}) {
		t.Fatalf("decoded board = %#v", board)
	}
}
`

const gridModuleTest = `package types

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
)

func TestSaveGridInputListsOfLists(t *testing.T) {
	cases := []struct {
		payload string
		set     bool
		null    bool
		want    []string
	}{
		{` + "`" + `{"labels":[["a"]],"shades":[["light"]],"polygons":[[]]}` + "`" + `, false, false, []string{}},
		{` + "`" + `{"labels":[],"shades":[],"polygons":[],"weights":null}` + "`" + `, true, true, []string{}},
		{` + "`" + `{"labels":[],"shades":[],"polygons":[],"weights":[[0.5],[]]}` + "`" + `, true, false, []string{}},
		{` + "`" + `{"labels":[],"shades":[["dark","none"]],"polygons":[null],"weights":[[1],null]}` + "`" + `, true, false,
			[]string{"polygons[0]", "shades[0][1]", "weights[1]"}},
	}
	for _, tc := range cases {
		var input SaveGridInput
		if err := json.Unmarshal([]byte(tc.payload), &input); err != nil {
			t.Fatalf("decode %s: %v", tc.payload, err)
		}
		if input.Weights.IsSet() != tc.set || input.Weights.IsNull() != tc.null {
			t.Fatalf("%s: weights set=%v null=%v", tc.payload, input.Weights.IsSet(), input.Weights.IsNull())
		}
		if got := errorKeys(input.Validate()); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s: errors at %v, want %v", tc.payload, got, tc.want)
		}
	}

	view := GridView{Labels: [][]string{{"a"}}, Shades: [][]Shade{}, Polygons: [][]Point{{{X: 1, Y: 1}}}, Weights: [][]float64{{0.25, 0.75}}}
	data, err := json.Marshal(&view)
	if err != nil {
		t.Fatal(err)
	}
	var decoded GridView
	if err := decoded.FromJSON(data); err != nil {
		t.Fatalf("strict decode %s: %v", data, err)
	}
	if !reflect.DeepEqual(decoded.Weights, view.Weights) || !reflect.DeepEqual(decoded.Polygons, view.Polygons) {
		t.Fatalf("round trip = %#v, want %#v", decoded, view)
	}
}
`

// nestedShapesDocument is a data-form General service that uses T[][] with
// every element kind the fixtures do not: catalog scalars (a UUID, an
// integer and each custom-parse scalar), a discriminated union, a type with
// a secret field, element and list-bound rules, and an input/output pair
// whose ToRoute converts each inner element.
func nestedShapesDocument(customParse []string) map[string]any {
	nested := func(name string) map[string]any {
		return map[string]any{"name": name, "isArray": true, "isArrayOfArrays": true}
	}
	field := func(name string, typeRef map[string]any, required bool, extra ...map[string]any) map[string]any {
		f := map[string]any{"name": name, "typeRef": typeRef, "required": required}
		for _, e := range extra {
			for k, v := range e {
				f[k] = v
			}
		}
		return f
	}
	plain := func(name string) map[string]any { return map[string]any{"name": name} }
	number := func(name string) map[string]any { return field(name, plain("number"), true) }
	typeDef := func(name, role string, fields ...map[string]any) map[string]any {
		return map[string]any{"name": name, "role": role, "fields": fields}
	}

	scalarNames := append([]string{"Identity.UUID", "Generic.Int64"}, customParse...)
	scalars := map[string]any{}
	for _, name := range scalarNames {
		meta, _ := registry.CoreScalars().Scalar(name)
		scalars[name] = map[string]any{"name": name, "languagePrimitive": languagePrimitive(meta.Primitive)}
	}
	customFields := make([]map[string]any, 0, len(customParse))
	for _, name := range customParse {
		customFields = append(customFields, field(scalarFieldName(name), nested(name), true))
	}
	kind := func(value string) map[string]any {
		return field("kind", plain("ShapeKind"), true, map[string]any{"default": value, "internalMetadata": true})
	}
	pointType := typeDef("Point", "EmbeddedStruct", number("x"), number("y"))
	pointType["jsonField"] = true
	routeType := typeDef("Route", "EmbeddedStruct", field("legs", nested("Point"), true), field("detours", nested("Point"), false))
	routeType["jsonField"] = true

	return map[string]any{
		"scalars": scalars,
		"enums": map[string]any{
			"ShapeKind": map[string]any{"name": "ShapeKind", "values": []any{
				map[string]any{"name": "Circle", "serializedAs": "circle"},
				map[string]any{"name": "Square", "serializedAs": "square"},
			}},
		},
		"unions": map[string]any{
			"Shape": map[string]any{"name": "Shape", "types": []string{"Circle", "Square"}},
		},
		"types": map[string]any{
			"Circle":     typeDef("Circle", "EmbeddedStruct", kind("circle"), number("radius")),
			"Square":     typeDef("Square", "EmbeddedStruct", kind("square"), number("side")),
			"Credential": typeDef("Credential", "EmbeddedStruct", field("name", plain("string"), true), field("token", plain("string"), true, map[string]any{"secret": true})),
			"Point":      pointType,
			"Route":      routeType,
			"PointInput": typeDef("PointInput", "APIInput", number("x"), number("y")),
			"RouteInput": typeDef("RouteInput", "APIInput", field("legs", nested("PointInput"), true), field("detours", nested("PointInput"), false)),
			"NestedShapes": typeDef("NestedShapes", "EmbeddedStruct",
				field("ids", nested("Identity.UUID"), true),
				field("counts", nested("Generic.Int64"), false),
				field("words", nested("string"), true, map[string]any{
					"validateMinLength": 2, "validatePattern": "^[a-z]+$", "validateListMin": 1, "validateListMax": 3,
				}),
				field("scores", nested("number"), false, map[string]any{"validateMin": 0, "validateMax": 1}),
				field("shapes", nested("Shape"), true),
				field("maybeShapes", nested("Shape"), false),
				field("credentials", nested("Credential"), false),
			),
			"NestedShapesInput": typeDef("NestedShapesInput", "APIInput",
				field("shapes", nested("Shape"), false),
				field("ids", nested("Identity.UUID"), false),
			),
			"CustomParseLists": typeDef("CustomParseLists", "EmbeddedStruct", customFields...),
		},
	}
}

// scalarFieldName is the lowerCamel field name for a scalar: IdentityUUID
// becomes identityUUID.
func scalarFieldName(scalarName string) string {
	symbol := codegen.BuildScalarTokens(scalarName).Symbol
	return strings.ToLower(symbol[:1]) + symbol[1:]
}

func loadNestedShapesSchema(t *testing.T, customParse []string) *ir.Schema {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "nested-shapes")
	files := map[string]any{
		"schema.config.json": map[string]any{
			"name":    "nested-shapes",
			"kind":    "General",
			"outputs": map[string]any{"types": map[string]any{"go": map[string]any{"enabled": true}}},
		},
		filepath.Join("src", "shapes.schema.json"): nestedShapesDocument(customParse),
	}
	for rel, doc := range files {
		data, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	schema, err := loader.LoadService(dir)
	if err != nil {
		t.Fatalf("load nested-shapes: %v", err)
	}
	return schema
}

// TestNestedArraysFieldShapes: every T[][] field is [][]T, or
// InputField[[][]T] when optional on an input. Element pointers follow T[]:
// there are none, so [][]*T never appears, not even for an optional integer
// scalar or a union, whose single-value forms drop a pointer.
func TestNestedArraysFieldShapes(t *testing.T) {
	output, err := Generate(loadNestedShapesSchema(t, nil), Options{SchemaName: "nested-shapes", ModulePath: "example.com/nested-shapes"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"NestedShapes.ids":         "[][]IdentityUUID",
		"NestedShapes.counts":      "[][]GenericInt64",
		"NestedShapes.words":       "[][]string",
		"NestedShapes.scores":      "[][]float64",
		"NestedShapes.shapes":      "[][]Shape",
		"NestedShapes.maybeShapes": "[][]Shape",
		"NestedShapes.credentials": "[][]Credential",
		"NestedShapesInput.shapes": "InputField[[][]Shape]",
		"NestedShapesInput.ids":    "InputField[[][]IdentityUUID]",
		"RouteInput.legs":          "[][]PointInput",
		"RouteInput.detours":       "InputField[[][]PointInput]",
		"Route.legs":               "[][]Point",
		"Route.detours":            "[][]Point",
	}
	seen := 0
	for _, typeInfo := range output.Types {
		for _, field := range typeInfo.Fields {
			key := typeInfo.Name + "." + field.Name
			if strings.Contains(field.GoType, "[]*") {
				t.Errorf("%s: Go type %q has a pointer element", key, field.GoType)
			}
			if expected, ok := want[key]; ok {
				seen++
				if field.GoType != expected {
					t.Errorf("%s: Go type %q, want %q", key, field.GoType, expected)
				}
			}
			nested := strings.HasPrefix(field.GoType, "[][]") || strings.HasPrefix(field.GoType, "InputField[[][]")
			if field.IsArrayOfArrays != nested || (nested && (!field.IsArray || field.ArrayDepth() != 2)) {
				t.Errorf("%s: IsArrayOfArrays=%v IsArray=%v depth=%d for %q", key, field.IsArrayOfArrays, field.IsArray, field.ArrayDepth(), field.GoType)
			}
		}
	}
	if seen != len(want) {
		t.Errorf("matched %d fields, want %d", seen, len(want))
	}

	var route *TypePair
	for i := range output.TypePairs {
		if output.TypePairs[i].InputName == "RouteInput" {
			route = &output.TypePairs[i]
		}
	}
	if route == nil || len(route.Fields) != 2 {
		t.Fatalf("RouteInput pair = %+v", route)
	}
	for _, pf := range route.Fields {
		if !pf.IsArrayOfArrays || !pf.IsPaired || pf.PairedTypeName != "Point" {
			t.Errorf("RouteInput.%s pair field = %+v, want a paired list of lists of Point", pf.GoName, pf)
		}
	}
}

// TestNestedArraysShapesModule generates the nested-shapes module and runs a
// test inside it: union elements dispatch per [i][j], scalar elements decode
// through their own codecs and validate at [i][j], element rules run on each
// inner element while list bounds bound the outer list, MaskSecrets clears
// secrets in every inner element, ToRoute converts each inner element, and
// each custom-parse scalar's parsed catalog example round-trips inside a
// list of lists.
func TestNestedArraysShapesModule(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping generated-module test run in -short mode")
	}
	names := customParseScalars(t)
	dir, output := writeNestedModule(t, "nested-shapes", loadNestedShapesSchema(t, names))

	var custom *TypeInfo
	for i := range output.Types {
		if output.Types[i].Name == "CustomParseLists" {
			custom = &output.Types[i]
		}
	}
	if custom == nil || len(custom.Fields) != len(names) {
		t.Fatalf("CustomParseLists = %+v", custom)
	}
	var build strings.Builder
	for _, field := range custom.Fields {
		scalar := field.ScalarInfo
		meta, _ := registry.CoreScalars().Scalar(scalar.Name)
		if len(meta.Examples) == 0 {
			t.Fatalf("%s has no catalog example", scalar.Name)
		}
		fmt.Fprintf(&build, "\tif value, err := Parse%s(%q); err != nil {\n\t\tt.Fatalf(\"%s: %%v\", err)\n\t} else {\n\t\trecord.%s = %s{{value, value}, {}}\n\t}\n",
			scalar.Tokens.Symbol, meta.Examples[0], scalar.Name, field.GoName, field.GoType)
	}
	code := strings.Replace(nestedShapesModuleTest, "\t// CUSTOM PARSE FIELDS\n", build.String(), 1)
	vetAndTestModule(t, dir, "nested_shapes_test.go", code+nestedModuleTestHelpers)
}

const nestedShapesModuleTest = `package types

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

const validShapes = ` + "`" + `{
	"ids": [["5f0f7ad2-2c1f-4c8e-9a53-9f4a1c2b7d10"], [], ["0c2d7c5e-7b8f-4f37-8a55-7e4e4a3f2b11", "6a3e0e1b-3c3d-4b5f-9d8a-2d1a7c6b5e12"]],
	"counts": [[1, 2], []],
	"words": [["ab", "cd"], []],
	"scores": [[0, 0.5, 1]],
	"shapes": [[{"kind": "circle", "radius": 1}], [], [{"kind": "square", "side": 2}, {"kind": "circle", "radius": 3}]],
	"credentials": [[{"name": "a", "token": "secret"}], []]
}` + "`" + `

func decodeShapes(t *testing.T, patch map[string]any) NestedShapes {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(validShapes), &payload); err != nil {
		t.Fatal(err)
	}
	for key, value := range patch {
		payload[key] = value
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var shapes NestedShapes
	if err := json.Unmarshal(data, &shapes); err != nil {
		t.Fatalf("decode %s: %v", data, err)
	}
	return shapes
}

func TestNestedShapes(t *testing.T) {
	shapes := decodeShapes(t, nil)
	if errs := shapes.Validate(); errs.HasErrors() {
		t.Fatalf("valid payload: %v", errs)
	}
	if _, ok := shapes.Shapes[2][0].(Square); !ok || len(shapes.Shapes[1]) != 0 || shapes.Shapes[1] == nil {
		t.Fatalf("shapes = %#v", shapes.Shapes)
	}
	if len(shapes.Ids[2]) != 2 || shapes.Ids[1] == nil || len(shapes.Ids[1]) != 0 || shapes.Counts[0][1] != 2 {
		t.Fatalf("ids = %v, counts = %v", shapes.Ids, shapes.Counts)
	}

	cases := []struct {
		name  string
		patch map[string]any
		want  map[string]string
	}{
		{"null inner lists", map[string]any{"ids": []any{nil}, "shapes": []any{[]any{}, nil}, "maybeShapes": []any{nil}, "counts": []any{nil}},
			map[string]string{"ids[0]": "required", "shapes[1]": "required", "maybeShapes[0]": "required", "counts[0]": "required"}},
		{"bad scalar element", map[string]any{"ids": []any{[]any{}, []any{"00000000-0000-0000-0000-000000000000"}}},
			map[string]string{"ids[1][0]": "required"}},
		{"element rules", map[string]any{"words": []any{[]any{"ab"}, []any{"x", "Ab"}}, "scores": []any{[]any{0.5}, []any{-1, 2}}},
			map[string]string{"words[1][0]": "minLength", "words[1][1]": "pattern", "scores[1][0]": "min", "scores[1][1]": "max"}},
		{"outer list bounds", map[string]any{"words": []any{}},
			map[string]string{"words": "listMin"}},
		{"outer list bounds", map[string]any{"words": []any{[]any{}, []any{}, []any{}, []any{}}},
			map[string]string{"words": "listMax"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			shapes := decodeShapes(t, tc.patch)
			errs := shapes.Validate()
			got := map[string]string{}
			for _, key := range errorKeys(errs) {
				got[key] = errs.GetFieldErrors(key)[0].Validator
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("errors = %v, want %v", got, tc.want)
			}
		})
	}

	var bad NestedShapes
	err := json.Unmarshal([]byte(strings.Replace(validShapes, ` + "`" + `"side": 2` + "`" + `, ` + "`" + `"side": 2, "kind": "triangle"` + "`" + `, 1)), &bad)
	if err == nil || !strings.Contains(err.Error(), "decode shapes[2][0]") {
		t.Fatalf("bad union element error = %v", err)
	}

	masked := shapes.MaskSecrets()
	if masked.Credentials[0][0].Token != "" || shapes.Credentials[0][0].Token != "secret" || masked.Credentials[0][0].Name != "a" {
		t.Fatalf("masked credentials = %#v, source %#v", masked.Credentials, shapes.Credentials)
	}
	masked.Words[0][0] = "zz"
	if shapes.Words[0][0] != "ab" {
		t.Fatal("MaskSecrets shares an inner list with its source")
	}

	encoded, err := json.Marshal(&shapes)
	if err != nil {
		t.Fatal(err)
	}
	var again NestedShapes
	if err := again.FromJSON(encoded); err != nil {
		t.Fatalf("strict decode %s: %v", encoded, err)
	}
	reencoded, _ := json.Marshal(&again)
	if !bytes.Equal(encoded, reencoded) {
		t.Fatalf("round trip %s, want %s", reencoded, encoded)
	}
}

func TestNestedShapesInput(t *testing.T) {
	var absent, null, value NestedShapesInput
	for payload, target := range map[string]*NestedShapesInput{
		` + "`" + `{}` + "`" + `:                  &absent,
		` + "`" + `{"shapes":null}` + "`" + `:     &null,
		` + "`" + `{"shapes":[[{"kind":"circle","radius":1}],[],null],"ids":[[],null]}` + "`" + `: &value,
	} {
		if err := json.Unmarshal([]byte(payload), target); err != nil {
			t.Fatalf("decode %s: %v", payload, err)
		}
	}
	if absent.Shapes.IsSet() || !null.Shapes.IsNull() || !value.Shapes.IsSet() || value.Shapes.IsNull() {
		t.Fatalf("presence: absent %+v null %+v value %+v", absent.Shapes, null.Shapes, value.Shapes)
	}
	if _, ok := value.Shapes.Value[0][0].(Circle); !ok || value.Shapes.Value[1] == nil || value.Shapes.Value[2] != nil {
		t.Fatalf("shapes = %#v", value.Shapes.Value)
	}
	if got, want := errorKeys(value.Validate()), []string{"ids[1]", "shapes[2]"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("errors at %v, want %v", got, want)
	}
	masked := value.MaskSecrets()
	if !masked.Shapes.IsSet() || len(masked.Shapes.Value) != 3 || masked.Shapes.Value[2] != nil {
		t.Fatalf("masked shapes = %#v", masked.Shapes)
	}
}

func TestRouteInputToRoute(t *testing.T) {
	var input RouteInput
	if err := json.Unmarshal([]byte(` + "`" + `{"legs":[[{"x":1,"y":2},{"x":3,"y":4}],[],null],"detours":[[{"x":5,"y":6}]]}` + "`" + `), &input); err != nil {
		t.Fatal(err)
	}
	route := input.ToRoute()
	want := [][]Point{{{X: 1, Y: 2}, {X: 3, Y: 4}}, {}, nil}
	if !reflect.DeepEqual(route.Legs, want) || !reflect.DeepEqual(route.Detours, [][]Point{{{X: 5, Y: 6}}}) {
		t.Fatalf("route = %#v", route)
	}
}

func TestCustomParseLists(t *testing.T) {
	var record CustomParseLists
	// CUSTOM PARSE FIELDS
	if errs := record.Validate(); errs.HasErrors() {
		t.Fatalf("parsed catalog examples fail validation: %v", errs)
	}
	encoded, err := json.Marshal(&record)
	if err != nil {
		t.Fatal(err)
	}
	var decoded CustomParseLists
	if err := decoded.FromJSON(encoded); err != nil {
		t.Fatalf("strict decode %s: %v", encoded, err)
	}
	if errs := decoded.Validate(); errs.HasErrors() {
		t.Fatalf("decoded record fails validation: %v", errs)
	}
	reencoded, _ := json.Marshal(&decoded)
	if !bytes.Equal(encoded, reencoded) {
		t.Fatalf("round trip %s, want %s", reencoded, encoded)
	}
}
`
