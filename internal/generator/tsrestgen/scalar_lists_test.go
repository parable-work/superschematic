package tsrestgen

import (
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/loader"
)

// scalarListsAPI is a schema local to this package's testdata, so its
// operations change no other generator's goldens.
const scalarListsAPI = "scalar-lists-api"

// scalarListsFixture is scalar-lists-api with apigen's endpoints for it.
func scalarListsFixture(t *testing.T) apiFixture {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join("testdata", "services", scalarListsAPI))
	if err != nil {
		t.Fatalf("load %s: %v", scalarListsAPI, err)
	}
	endpoints, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: scalarListsAPI,
		Clock:      fixedClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return apiFixture{name: scalarListsAPI, schema: schema, endpoints: endpoints}
}

// TestWriteAPIGoldenScalarLists pins the package for scalar-lists-api: body
// arguments that are lists of strings, numbers, an integer scalar, an enum,
// a string scalar and Generic.JSON values, single ones, a Generic.JSON list
// of lists, and a GET list in the query string. Regenerate with
// go test ./internal/generator/tsrestgen -run TestWriteAPIGoldenScalarLists -update
func TestWriteAPIGoldenScalarLists(t *testing.T) {
	checkGolden(t, generateFixture(t, scalarListsFixture(t)), scalarListsAPI)
}

// TestGenerateScalarListsShape: a scalar-typed parameter carries the
// scalar's own lengths, pattern or range next to the argument's
// constraints, a Generic.JSON body argument has kind json and the scalar's
// type at every list depth, and a GET list stays a query parameter.
func TestGenerateScalarListsShape(t *testing.T) {
	fixture := scalarListsFixture(t)
	byName := endpointsByName(generateFixture(t, fixture))

	save := byName["saveTags"]
	if save.Method != "PUT" || len(save.PathParams) != 1 || len(save.BodyParams) != 7 {
		t.Fatalf("saveTags = %s with path params %+v and body params %+v", save.Method, save.PathParams, save.BodyParams)
	}
	urlPattern := tsString(fixture.schema.Scalars["Network.Url"].Pattern)
	for i, want := range []struct{ tsType, spec string }{
		{"string[]", "{ name: 'labels', kind: 'string', required: true, isArray: true, listMax: 3 }"},
		{"number[]", "{ name: 'weights', kind: 'number', required: false, isArray: true }"},
		{"number[]", "{ name: 'ranks', kind: 'integer', required: false, isArray: true, scalar: { name: 'Ordering.Rank', min: 1, max: 9007199254740991 } }"},
		{"Shade[]", "{ name: 'shades', kind: 'enum', required: false, isArray: true, enumValues: ['light', 'dark'] }"},
		{"NetworkUrl[]", "{ name: 'links', kind: 'string', required: false, isArray: true, listMin: 1, listMax: 2, pattern: '^https://', scalar: { name: 'Network.Url', maxLength: 2048, pattern: " + urlPattern + " } }"},
		{"string", "{ name: 'title', kind: 'string', required: false }"},
		{"number", "{ name: 'priority', kind: 'number', required: false }"},
	} {
		if got := save.BodyParams[i]; got.TSType != want.tsType || got.SpecLiteral != want.spec {
			t.Errorf("saveTags body param %d = %s %s, want %s %s", i, got.TSType, got.SpecLiteral, want.tsType, want.spec)
		}
	}

	store := byName["storeDocument"]
	if len(store.BodyParams) != 4 {
		t.Fatalf("storeDocument body params = %+v", store.BodyParams)
	}
	for i, want := range []struct{ tsType, spec string }{
		{"GenericJSON", "{ name: 'document', kind: 'json', required: true }"},
		{"GenericJSON", "{ name: 'note', kind: 'json', required: false }"},
		{"GenericJSON[]", "{ name: 'extras', kind: 'json', required: false, isArray: true }"},
		{"GenericJSON[][]", "{ name: 'grid', kind: 'json', required: false, isArray: true, isArrayOfArrays: true }"},
	} {
		if got := store.BodyParams[i]; got.TSType != want.tsType || got.SpecLiteral != want.spec {
			t.Errorf("storeDocument body param %d = %s %s, want %s %s", i, got.TSType, got.SpecLiteral, want.tsType, want.spec)
		}
	}

	find := byName["findTags"]
	if len(find.QueryParams) != 1 || len(find.BodyParams) != 0 {
		t.Fatalf("findTags query params %+v, body params %+v", find.QueryParams, find.BodyParams)
	}
	if want := "{ name: 'labels', kind: 'string', required: true, isArray: true }"; find.QueryParams[0].SpecLiteral != want {
		t.Errorf("findTags labels = %s, want %s", find.QueryParams[0].SpecLiteral, want)
	}
}

// TestGeneratedScalarListsRouter type-checks the generated package for
// scalar-lists-api and drives it under bun over HTTP with the same vectors
// for every element kind: a body list is its JSON array (a comma stays in
// its element, an empty string is kept), a null element is refused at
// name[i] as required and a wrong-type one as type, "5" is not a number,
// [] satisfies a required list, list bounds and the scalar's own pattern,
// lengths and range apply, and a Generic.JSON argument is any JSON value
// but null. A GET list still reads comma-separated query values.
func TestGeneratedScalarListsRouter(t *testing.T) {
	tree := materializeAPI(t, scalarListsFixture(t))
	tree.typeCheck(t)
	tree.runTest(t, "scalar_lists_runtime.test.ts")
}
