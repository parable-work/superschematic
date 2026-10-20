package apigen_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sdkgen/sdktest"
)

// TestUnionOutputsAPIServesMaps generates the Go types module and the Go API
// module of union-lists-api (sdktest.LoadUnionListsService) and runs
// unionOutputsAPIServerTest in the API module: an implementation returns a
// map and a map of lists of the union Shape, which the routes send as JSON
// objects of its members, a nil map as {}, and a nil Nullable<Shape> as
// null.
func TestUnionOutputsAPIServesMaps(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	schema, err := sdktest.LoadUnionListsService()
	if err != nil {
		t.Fatalf("load %s: %v", sdktest.UnionListsService, err)
	}
	apiDir := writeGoAPIModule(t, schema, sdktest.UnionListsService)
	if err := os.WriteFile(filepath.Join(apiDir, "union_outputs_server_test.go"), []byte(unionOutputsAPIServerTest), 0o644); err != nil {
		t.Fatal(err)
	}
	runGoAPIModule(t, apiDir)
}

// unionOutputsAPIServerTest runs in the generated API module. Its
// implementation returns circles and squares, or nil for every map and
// value when empty is set.
const unionOutputsAPIServerTest = `package unionlistsapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	api "example.com/schemas/api/union-lists-api"
	types "example.com/schemas/types/go/union-lists-api"
)

type shapes struct{ empty bool }

func (s shapes) ListShapes(context.Context) ([]types.Shape, error) {
	return []types.Shape{types.Circle{Radius: 1}}, nil
}

func (s shapes) ShapeRows(context.Context) ([][]types.Shape, error) {
	return [][]types.Shape{{types.Circle{Radius: 1}}}, nil
}

func (s shapes) GetShape(context.Context, string) (*types.Shape, error) {
	var shape types.Shape = types.Square{Side: 2}
	return &shape, nil
}

func (s shapes) ShapesByName(context.Context) (map[string]types.Shape, error) {
	if s.empty {
		return nil, nil
	}
	return map[string]types.Shape{"a": types.Circle{Radius: 1}, "b": types.Square{Side: 2}}, nil
}

func (s shapes) ShapeGroups(context.Context) (map[string][]types.Shape, error) {
	if s.empty {
		return nil, nil
	}
	return map[string][]types.Shape{
		"round": {types.Circle{Radius: 1}},
		"mixed": {types.Square{Side: 2}, types.Circle{Radius: 1}},
	}, nil
}

func (s shapes) FindShape(context.Context, string) (*types.Shape, error) {
	if s.empty {
		return nil, nil
	}
	var shape types.Shape = types.Circle{Radius: 1}
	return &shape, nil
}

func (s shapes) FindShapes(context.Context) ([]types.Shape, error) {
	if s.empty {
		return nil, nil
	}
	return []types.Shape{types.Square{Side: 2}}, nil
}

// data serves impl, sends GET path and returns the envelope's data.
func data(t *testing.T, impl shapes, path string) any {
	t.Helper()
	router := chi.NewRouter()
	if err := api.RegisterRoutes(router, api.Config{
		Logger:          zap.NewNop(),
		Implementations: api.Implementations{Shape: impl},
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	defer server.Close()
	resp, err := http.Get(server.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d: %s", path, resp.StatusCode, raw)
	}
	var envelope struct {
		Data any ` + "`json:\"data\"`" + `
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("GET %s: %v: %s", path, err, raw)
	}
	return envelope.Data
}

func decode(t *testing.T, value string) any {
	t.Helper()
	var decoded any
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestAMapOfTheUnionIsAnObjectOfItsMembers(t *testing.T) {
	got := data(t, shapes{}, "/api/shape-index")
	if want := decode(t, ` + "`" + `{"a": {"radius": 1}, "b": {"side": 2}}` + "`" + `); !reflect.DeepEqual(got, want) {
		t.Fatalf("shape-index = %v, want %v", got, want)
	}
	got = data(t, shapes{}, "/api/shape-groups")
	if want := decode(t, ` + "`" + `{"round": [{"radius": 1}], "mixed": [{"side": 2}, {"radius": 1}]}` + "`" + `); !reflect.DeepEqual(got, want) {
		t.Fatalf("shape-groups = %v, want %v", got, want)
	}
}

func TestANilMapIsAnEmptyObject(t *testing.T) {
	for _, path := range []string{"/api/shape-index", "/api/shape-groups"} {
		if got := data(t, shapes{empty: true}, path); !reflect.DeepEqual(got, map[string]any{}) {
			t.Errorf("%s = %#v, want {}", path, got)
		}
	}
}

func TestANilNullableShapeIsNull(t *testing.T) {
	if got := data(t, shapes{empty: true}, "/api/shape-search/none"); got != nil {
		t.Fatalf("shape-search/none = %#v, want null", got)
	}
	if got := data(t, shapes{}, "/api/shape-search/c-1"); !reflect.DeepEqual(got, decode(t, ` + "`" + `{"radius": 1}` + "`" + `)) {
		t.Fatalf("shape-search/c-1 = %#v", got)
	}
}
`
