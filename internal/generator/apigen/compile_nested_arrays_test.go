package apigen_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sdkgen/sdktest"
	"github.com/parable-work/superschematic/internal/loader"
)

// TestNestedArraysAPIServesNestedJSON generates the Go types module and the
// Go API module of fixture-nested-arrays-api, with grid.paint added
// (sdktest.AddPaintOperation), into a temp tree laid out as a build writes
// it, then runs go mod tidy, go build, go vet and go test on the API module
// with nestedArraysAPIServerTest: the generated routes serve an
// implementation over httptest, decode lists of lists from request bodies,
// answer 400 at name[i] for a null inner list and at name[i][j] for a bad
// element, answer 400 for a null element of an input body before the
// implementation runs, and send lists of lists back with a nil inner list
// as [].
func TestNestedArraysAPIServesNestedJSON(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	const service = "fixture-nested-arrays-api"
	schema, err := loader.LoadService(filepath.Join(fixturesDir, service))
	if err != nil {
		t.Fatalf("load %s: %v", service, err)
	}
	if err := sdktest.AddPaintOperation(schema); err != nil {
		t.Fatal(err)
	}
	apiDir := writeGoAPIModule(t, schema, service)
	if err := os.WriteFile(filepath.Join(apiDir, "nested_arrays_server_test.go"), []byte(nestedArraysAPIServerTest), 0o644); err != nil {
		t.Fatal(err)
	}
	runGoAPIModule(t, apiDir)
}

// nestedArraysAPIServerTest runs in the generated API module. It registers
// the routes with an in-memory grid implementation and calls them through
// httptest.
const nestedArraysAPIServerTest = `package fixturenestedarraysapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	api "example.com/schemas/api/fixture-nested-arrays-api"
	types "example.com/schemas/types/go/fixture-nested-arrays-api"
)

const gridID = "0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40"

// grids keeps the last grid it stored and records what each call received.
type grids struct {
	saved    *types.SaveGridInput
	labels   [][]string
	shades   [][]types.Shade
	polygons [][]types.Point
}

func (g *grids) view(id types.IdentityUUID) *types.GridView {
	view := &types.GridView{Id: id, Labels: g.saved.Labels, Shades: g.saved.Shades, Polygons: g.saved.Polygons}
	// An optional input field is an InputField: set, null or a value.
	if g.saved.Weights.Set && !g.saved.Weights.Null {
		view.Weights = g.saved.Weights.Value
	}
	return view
}

func (g *grids) SaveGrid(_ context.Context, input *types.SaveGridInput) (*types.GridView, error) {
	g.saved = input
	id, err := types.ParseIdentityUUID(gridID)
	if err != nil {
		return nil, err
	}
	return g.view(id), nil
}

func (g *grids) GetGrid(_ context.Context, id types.IdentityUUID) (*types.GridView, error) {
	return g.view(id), nil
}

func (g *grids) GridLabels(_ context.Context, _ types.IdentityUUID, _ *float64) ([][]string, error) {
	// The nil inner list goes out as [].
	return [][]string{{"a", "b"}, nil, {"c"}}, nil
}

func (g *grids) ReplaceLabels(_ context.Context, id types.IdentityUUID, labels [][]string) (*types.GridView, error) {
	g.labels = labels
	g.saved.Labels = labels
	return g.view(id), nil
}

func (g *grids) Paint(_ context.Context, _ types.IdentityUUID, shades [][]types.Shade, polygons [][]types.Point) ([][]types.Point, error) {
	g.shades, g.polygons = shades, polygons
	return [][]types.Point{{{X: 1, Y: 2}}, nil}, nil
}

func serve(t *testing.T) (*httptest.Server, *grids) {
	t.Helper()
	impl := &grids{saved: &types.SaveGridInput{}}
	router := chi.NewRouter()
	if err := api.RegisterRoutes(router, api.Config{
		Logger:          zap.NewNop(),
		Implementations: api.Implementations{Grid: impl},
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return server, impl
}

// call sends body (nil for none) and returns the status and decoded JSON.
func call(t *testing.T, server *httptest.Server, method, path, body string) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = bytes.NewBufferString(body)
	}
	req, err := http.NewRequest(method, server.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("%s %s: response is not a JSON object: %s", method, path, raw)
	}
	return resp.StatusCode, decoded
}

func decode(t *testing.T, value string) any {
	t.Helper()
	var decoded any
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

// errorPaths returns the sorted field paths of a 400 validation response.
func errorPaths(t *testing.T, status int, body map[string]any) []string {
	t.Helper()
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body %v", status, body)
	}
	errs, ok := body["errors"].(map[string]any)
	if !ok {
		t.Fatalf("body has no errors object: %v", body)
	}
	var paths []string
	for path := range errs {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

const grid = ` + "`" + `{
	"labels": [["a", "b"], []],
	"shades": [["light", "dark"], []],
	"polygons": [[{"x": 1, "y": 2}, {"x": 3, "y": 4}], []],
	"weights": [[0.5, 1.5], []]
}` + "`" + `

func TestSaveGridRoundTripsNestedLists(t *testing.T) {
	server, impl := serve(t)
	status, body := call(t, server, http.MethodPost, "/api/grids", grid)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body %v", status, body)
	}
	// The id is sent in the scalar's own wire form; the lists are what
	// this test is about.
	want := decode(t, ` + "`" + `{
		"labels": [["a", "b"], []],
		"shades": [["light", "dark"], []],
		"polygons": [[{"x": 1, "y": 2}, {"x": 3, "y": 4}], []],
		"weights": [[0.5, 1.5], []]
	}` + "`" + `)
	if got := withoutID(t, body); !reflect.DeepEqual(got, want) {
		t.Errorf("data = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(impl.saved.Polygons, [][]types.Point{{{X: 1, Y: 2}, {X: 3, Y: 4}}, {}}) {
		t.Errorf("implementation received polygons %+v", impl.saved.Polygons)
	}

	status, body = call(t, server, http.MethodGet, "/api/grids/"+gridID, "")
	if status != http.StatusOK || !reflect.DeepEqual(withoutID(t, body), want) {
		t.Errorf("GET = %d %v, want the saved grid", status, body)
	}
}

// withoutID returns a response's data object without its id, after
// checking the id is there.
func withoutID(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	data, ok := body["data"].(map[string]any)
	if !ok {
		t.Fatalf("data is not an object: %v", body)
	}
	if id, _ := data["id"].(string); id == "" {
		t.Fatalf("data has no id: %v", data)
	}
	rest := map[string]any{}
	for key, value := range data {
		if key != "id" {
			rest[key] = value
		}
	}
	return rest
}

func TestSaveGridRefusesNullInnerListsAndBadElements(t *testing.T) {
	server, impl := serve(t)
	for _, tc := range []struct {
		name, body string
		want       []string
	}{
		{"null inner list", ` + "`" + `{"labels": [["a"], null], "shades": [], "polygons": []}` + "`" + `, []string{"labels[1]"}},
		{"bad enum element", ` + "`" + `{"labels": [], "shades": [["light"], ["dark", "dim"]], "polygons": []}` + "`" + `, []string{"shades[1][1]"}},
		{"null inner list of objects", ` + "`" + `{"labels": [], "shades": [], "polygons": [null]}` + "`" + `, []string{"polygons[0]"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := call(t, server, http.MethodPost, "/api/grids", tc.body)
			if got := errorPaths(t, status, body); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("error paths = %v, want %v; body %v", got, tc.want, body)
			}
		})
	}
	if impl.saved.Labels != nil {
		t.Errorf("a refused grid reached the implementation: %+v", impl.saved)
	}
}

// A null innermost element is refused by the input type's decoder, so the
// route answers 400 before the implementation sees a zero value in its
// place.
func TestSaveGridRefusesNullElements(t *testing.T) {
	server, impl := serve(t)
	for _, tc := range []struct{ name, body string }{
		{"string element", ` + "`" + `{"labels": [["a", null]], "shades": [], "polygons": []}` + "`" + `},
		{"enum element", ` + "`" + `{"labels": [], "shades": [[null]], "polygons": []}` + "`" + `},
		{"object element", ` + "`" + `{"labels": [], "shades": [], "polygons": [[{"x": 1, "y": 2}, null]]}` + "`" + `},
		{"element of an optional input field", ` + "`" + `{"labels": [], "shades": [], "polygons": [], "weights": [[0.5, null]]}` + "`" + `},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := call(t, server, http.MethodPost, "/api/grids", tc.body)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body %v", status, body)
			}
		})
	}
	if impl.saved.Labels != nil || impl.saved.Shades != nil || impl.saved.Polygons != nil || impl.saved.Weights.Set {
		t.Errorf("a grid with a null element reached the implementation: %+v", impl.saved)
	}
}

func TestReplaceLabelsBodyArgument(t *testing.T) {
	server, impl := serve(t)
	if status, body := call(t, server, http.MethodPost, "/api/grids", grid); status != http.StatusOK {
		t.Fatalf("save: %d %v", status, body)
	}
	status, body := call(t, server, http.MethodPut, "/api/grids/"+gridID+"/labels", ` + "`" + `{"labels": [[], ["x", "y"]]}` + "`" + `)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body %v", status, body)
	}
	if !reflect.DeepEqual(impl.labels, [][]string{{}, {"x", "y"}}) {
		t.Errorf("implementation received %#v", impl.labels)
	}
	data, _ := body["data"].(map[string]any)
	if want := decode(t, ` + "`" + `[[], ["x", "y"]]` + "`" + `); !reflect.DeepEqual(data["labels"], want) {
		t.Errorf("labels = %v, want %v", data["labels"], want)
	}

	for _, tc := range []struct {
		name, body string
		want       []string
	}{
		{"null inner list", ` + "`" + `{"labels": [["x"], null, []]}` + "`" + `, []string{"labels[1]"}},
		{"missing", ` + "`" + `{}` + "`" + `, []string{"labels"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := call(t, server, http.MethodPut, "/api/grids/"+gridID+"/labels", tc.body)
			if got := errorPaths(t, status, body); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("error paths = %v, want %v; body %v", got, tc.want, body)
			}
		})
	}
}

func TestPaintValidatesEachElement(t *testing.T) {
	server, impl := serve(t)
	for _, tc := range []struct {
		name, body string
		want       []string
	}{
		{"bad enum element", ` + "`" + `{"shades": [["light", "dim"]]}` + "`" + `, []string{"shades[0][1]"}},
		{"null inner list", ` + "`" + `{"shades": [["light"], null]}` + "`" + `, []string{"shades[1]"}},
		{"null inner list of an optional argument", ` + "`" + `{"shades": [], "polygons": [[], null]}` + "`" + `, []string{"polygons[1]"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := call(t, server, http.MethodPut, "/api/grids/"+gridID+"/paint", tc.body)
			if got := errorPaths(t, status, body); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("error paths = %v, want %v; body %v", got, tc.want, body)
			}
		})
	}
	if impl.shades != nil {
		t.Fatalf("a refused paint reached the implementation: %v", impl.shades)
	}

	status, body := call(t, server, http.MethodPut, "/api/grids/"+gridID+"/paint", ` + "`" + `{"shades": [["light", "dark"], []], "polygons": [[{"x": 5, "y": 6}]]}` + "`" + `)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body %v", status, body)
	}
	if !reflect.DeepEqual(impl.shades, [][]types.Shade{{types.Shade_Light, types.Shade_Dark}, {}}) ||
		!reflect.DeepEqual(impl.polygons, [][]types.Point{{{X: 5, Y: 6}}}) {
		t.Errorf("implementation received shades %v, polygons %v", impl.shades, impl.polygons)
	}
	if want := decode(t, ` + "`" + `[[{"x": 1, "y": 2}], []]` + "`" + `); !reflect.DeepEqual(body["data"], want) {
		t.Errorf("data = %v, want %v", body["data"], want)
	}
}

func TestGridLabelsSendsNestedResponse(t *testing.T) {
	server, _ := serve(t)
	status, body := call(t, server, http.MethodGet, "/api/grids/"+gridID+"/labels?limit=2", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, body %v", status, body)
	}
	if want := decode(t, ` + "`" + `[["a", "b"], [], ["c"]]` + "`" + `); !reflect.DeepEqual(body["data"], want) {
		t.Errorf("data = %v, want %v", body["data"], want)
	}
}

func TestServedOpenAPINestsItems(t *testing.T) {
	server, _ := serve(t)
	status, body := call(t, server, http.MethodGet, "/api/openapi.json", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	components, _ := body["components"].(map[string]any)
	schemas, _ := components["schemas"].(map[string]any)
	input, _ := schemas["SaveGridInput"].(map[string]any)
	properties, _ := input["properties"].(map[string]any)
	labels, _ := properties["labels"].(map[string]any)
	want := decode(t, ` + "`" + `{"type": "array", "items": {"type": "string"}}` + "`" + `)
	if labels["type"] != "array" || !reflect.DeepEqual(labels["items"], want) {
		t.Errorf("SaveGridInput.labels = %v, want an array whose items are %v", labels, want)
	}
}
`
