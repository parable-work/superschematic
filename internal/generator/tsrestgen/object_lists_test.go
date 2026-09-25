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

// objectListsAPI is a schema local to this package's testdata, so its
// operations change no other generator's goldens.
const objectListsAPI = "object-lists-api"

func loadObjectListsAPI(t *testing.T) *ir.Schema {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join("testdata", "services", objectListsAPI))
	if err != nil {
		t.Fatalf("load %s: %v", objectListsAPI, err)
	}
	return schema
}

// objectListsFixture is object-lists-api with apigen's endpoints for it.
func objectListsFixture(t *testing.T, schema *ir.Schema) apiFixture {
	t.Helper()
	endpoints, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: objectListsAPI,
		Clock:      fixedClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return apiFixture{name: objectListsAPI, schema: schema, endpoints: endpoints}
}

// addDrawingOperation appends one PUT operation to DrawingMutations.
func addDrawingOperation(t *testing.T, schema *ir.Schema, op *ir.FieldDef) {
	t.Helper()
	for _, set := range schema.OperationSets {
		if set.Name == "DrawingMutations" {
			op.HTTPMethod = "PUT"
			set.Operations = append(set.Operations, op)
			return
		}
	}
	t.Fatalf("schema %s has no DrawingMutations operation set", schema.Name)
}

// TestWriteAPIGoldenObjectLists pins the package for object-lists-api: a
// PUT whose body arguments are a required and an optional Point[] and a
// Shade[], and whose response is Point[]. Regenerate with
// go test ./internal/generator/tsrestgen -run TestWriteAPIGoldenObjectLists -update
func TestWriteAPIGoldenObjectLists(t *testing.T) {
	checkGolden(t, generateFixture(t, objectListsFixture(t, loadObjectListsAPI(t))), objectListsAPI)
}

// TestGenerateObjectListsShape: a T[] body argument of an object type is
// typed T[] and parsed by the generated strict parser of T, like an object
// element of T[][]; an enum list keeps its values, and a Point[] response
// is typed Point[].
func TestGenerateObjectListsShape(t *testing.T) {
	output := generateFixture(t, objectListsFixture(t, loadObjectListsAPI(t)))
	save := endpointsByName(output)["saveOutline"]
	if save.Method != "PUT" || len(save.PathParams) != 1 || len(save.BodyParams) != 3 {
		t.Fatalf("saveOutline = %s with path params %+v and body params %+v", save.Method, save.PathParams, save.BodyParams)
	}
	points, markers, shades := save.BodyParams[0], save.BodyParams[1], save.BodyParams[2]
	if want := "{ name: 'points', kind: 'object', required: true, isArray: true, listMax: 4, parse: (value: unknown) => parsePointFromJSON(parsePointJson(value)) }"; points.TSType != "Point[]" || points.SpecLiteral != want {
		t.Errorf("points = %s %s, want Point[] %s", points.TSType, points.SpecLiteral, want)
	}
	if want := "{ name: 'markers', kind: 'object', required: false, isArray: true, parse: (value: unknown) => parsePointFromJSON(parsePointJson(value)) }"; markers.TSType != "Point[]" || markers.SpecLiteral != want {
		t.Errorf("markers = %s %s, want Point[] %s", markers.TSType, markers.SpecLiteral, want)
	}
	if want := "{ name: 'shades', kind: 'enum', required: false, isArray: true, enumValues: ['light', 'dark'] }"; shades.TSType != "Shade[]" || shades.SpecLiteral != want {
		t.Errorf("shades = %s %s, want Shade[] %s", shades.TSType, shades.SpecLiteral, want)
	}
	if save.OutputType != "Point[]" || save.OutputIsArrayOfArrays {
		t.Errorf("saveOutline output = %q (list of lists %t)", save.OutputType, save.OutputIsArrayOfArrays)
	}

	const pkg = "@schemas/object-lists-api-types: "
	importsOf := func(imports []PackageImport) string {
		var parts []string
		for _, imp := range imports {
			parts = append(parts, imp.Package+": "+strings.Join(imp.Symbols, ","))
		}
		return strings.Join(parts, "; ")
	}
	if got, want := importsOf(output.ValidatorImports), pkg+"parsePointFromJSON,parsePointJson"; got != want {
		t.Errorf("validator imports = %s, want %s", got, want)
	}
	if got, want := importsOf(output.RouterTypeImports), pkg+"Point,Shade"; got != want {
		t.Errorf("router type imports = %s, want %s", got, want)
	}
}

// TestGenerateObjectBodyArgument: a body argument of an object type that
// apigen does not take as the operation's input (a DB table type) is
// parsed by its generated strict parser as well, not typed as a string.
func TestGenerateObjectBodyArgument(t *testing.T) {
	schema := loadObjectListsAPI(t)
	schema.Types["Point"].Role = ir.RoleDBTable
	addDrawingOperation(t, schema, &ir.FieldDef{
		Name:      "moveOrigin",
		TypeRef:   ir.TypeRef{Name: "Point"},
		Arguments: []*ir.ArgumentDef{{Name: "origin", TypeRef: ir.TypeRef{Name: "Point"}, Required: true}},
		RestPath:  "drawings/origin",
	})
	move := endpointsByName(generateFixture(t, objectListsFixture(t, schema)))["moveOrigin"]
	if len(move.BodyParams) != 1 || move.HasInput {
		t.Fatalf("moveOrigin body params = %+v (input %t)", move.BodyParams, move.HasInput)
	}
	if origin, want := move.BodyParams[0], "{ name: 'origin', kind: 'object', required: true, parse: (value: unknown) => parsePointFromJSON(parsePointJson(value)) }"; origin.TSType != "Point" || origin.SpecLiteral != want {
		t.Errorf("origin = %s %s, want Point %s", origin.TSType, origin.SpecLiteral, want)
	}
}

// TestGenerateRefusesBodyArgumentsWithoutParser: a body argument whose type
// has no generated parser (a union), alone or as a list element, is refused
// with the argument named, never rendered as a string.
func TestGenerateRefusesBodyArgumentsWithoutParser(t *testing.T) {
	for _, tc := range []struct {
		name string
		ref  ir.TypeRef
		want string
	}{
		{"list", ir.TypeRef{Name: "Mark", IsArray: true}, "tsrestgen cannot decode body argument drawing.mark(marks): element type Mark is not a scalar, enum or object type"},
		{"value", ir.TypeRef{Name: "Mark"}, "tsrestgen cannot decode body argument drawing.mark(marks): type Mark is not a scalar, enum or object type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := loadObjectListsAPI(t)
			schema.Unions["Mark"] = &ir.UnionDef{Name: "Mark", Types: []string{"Point"}}
			addDrawingOperation(t, schema, &ir.FieldDef{
				Name:      "mark",
				TypeRef:   ir.TypeRef{Name: "Point"},
				Arguments: []*ir.ArgumentDef{{Name: "marks", TypeRef: tc.ref, Required: true}},
				RestPath:  "drawings/marks",
			})
			fixture := objectListsFixture(t, schema)
			_, err := Generate(fixture.schema, fixture.endpoints, Options{SchemaName: objectListsAPI})
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

// TestGeneratedObjectListsRouter type-checks the generated package for
// object-lists-api and drives it under bun over HTTP: Point[] bodies reach
// the implementation as parsed objects, a null element, a non-object
// element and an element with a bad field are refused at name[i], and a
// Point[] response is sent as returned.
func TestGeneratedObjectListsRouter(t *testing.T) {
	tree := materializeAPI(t, objectListsFixture(t, loadObjectListsAPI(t)))
	tree.typeCheck(t)
	tree.runTest(t, "object_lists_runtime.test.ts")
}
