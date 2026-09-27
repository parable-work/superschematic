package tsrestgen

import (
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/loader"
)

// mapArgsAPI is a schema local to this package's testdata, so its
// operations change no other generator's goldens.
const mapArgsAPI = "map-args-api"

// mapArgsFixture is map-args-api with apigen's endpoints for it.
func mapArgsFixture(t *testing.T) apiFixture {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join("testdata", "services", mapArgsAPI))
	if err != nil {
		t.Fatalf("load %s: %v", mapArgsAPI, err)
	}
	endpoints, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: mapArgsAPI,
		Clock:      fixedClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return apiFixture{name: mapArgsAPI, schema: schema, endpoints: endpoints}
}

// TestWriteAPIGoldenMapArgs pins the package for map-args-api: body
// arguments that are maps of an enum, of lists of a string scalar, of an
// object type and of a number. Regenerate with
// go test ./internal/generator/tsrestgen -run TestWriteAPIGoldenMapArgs -update
func TestWriteAPIGoldenMapArgs(t *testing.T) {
	checkGolden(t, generateFixture(t, mapArgsFixture(t)), mapArgsAPI)
}

// TestGenerateMapArgsShape: a map body argument is typed Record<string, T>
// (Record<string, T[]> for a map of lists) and its spec says isMap, with
// the value's kind and constraints and without list bounds, which do not
// bound a map.
func TestGenerateMapArgsShape(t *testing.T) {
	fixture := mapArgsFixture(t)
	name := endpointsByName(generateFixture(t, fixture))["nameThings"]
	if name.Method != "PUT" || len(name.PathParams) != 1 || len(name.BodyParams) != 4 {
		t.Fatalf("nameThings = %s with path params %+v and body params %+v", name.Method, name.PathParams, name.BodyParams)
	}
	urlPattern := tsString(fixture.schema.Scalars["Network.Url"].Pattern)
	for i, want := range []struct{ tsType, spec string }{
		{"Record<string, Shade>", "{ name: 'shadeByName', kind: 'enum', required: true, isMap: true, enumValues: ['light', 'dark'] }"},
		{"Record<string, NetworkUrl[]>", "{ name: 'linksByLocale', kind: 'string', required: false, isArray: true, isMap: true, pattern: '^https://', scalar: { name: 'Network.Url', maxLength: 2048, pattern: " + urlPattern + " } }"},
		{"Record<string, Point>", "{ name: 'pointByName', kind: 'object', required: false, isMap: true, parse: (value: unknown) => parsePointFromJSON(parsePointJson(value)) }"},
		{"Record<string, number>", "{ name: 'weightByName', kind: 'number', required: false, isMap: true, max: 1 }"},
	} {
		if got := name.BodyParams[i]; got.TSType != want.tsType || got.SpecLiteral != want.spec {
			t.Errorf("nameThings body param %d = %s %s, want %s %s", i, got.TSType, got.SpecLiteral, want.tsType, want.spec)
		}
	}
}

// TestGeneratedMapArgsRouter type-checks the generated package for
// map-args-api and drives it under bun over HTTP: a map is a JSON object
// whose values reach the implementation checked, absent or null is
// refused when required, a value is never null and is checked at
// name[key], and a map of lists checks its elements at name[key][i].
func TestGeneratedMapArgsRouter(t *testing.T) {
	tree := materializeAPI(t, mapArgsFixture(t))
	tree.typeCheck(t)
	tree.runTest(t, "map_args_runtime.test.ts")
}
