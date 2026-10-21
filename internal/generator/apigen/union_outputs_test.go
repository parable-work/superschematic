package apigen_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sdkgen/sdktest"
)

// TestUnionListsGoAPI generates the Go types module and the Go API module
// of union-lists-api (sdktest.LoadUnionListsService) and runs two tests in
// the API module. unionOutputsAPIServerTest: an implementation returns a
// map and a map of lists of the union Shape, which the routes send as JSON
// objects of its members, a nil map as {}, and a nil Nullable<Shape> as
// null. unionRequestsAPIServerTest sends union_requests.json's requests
// (sdktest.UnionRequestsPath) to the routes and records what they answer,
// which sdktest.CheckUnionResponses holds to the requests' expectations: a
// union body argument decodes through its wrapper, which picks the member
// and validates it, and an input's union fields are validated as their
// members.
func TestUnionListsGoAPI(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	schema, err := sdktest.LoadUnionListsService()
	if err != nil {
		t.Fatalf("load %s: %v", sdktest.UnionListsService, err)
	}
	requests, err := sdktest.LoadUnionRequests()
	if err != nil {
		t.Fatal(err)
	}
	requestsPath, err := sdktest.UnionRequestsPath()
	if err != nil {
		t.Fatal(err)
	}
	apiDir := writeGoAPIModule(t, schema, sdktest.UnionListsService)
	for name, source := range map[string]string{
		"union_outputs_server_test.go":  unionOutputsAPIServerTest,
		"union_requests_server_test.go": unionRequestsAPIServerTest,
	} {
		if err := os.WriteFile(filepath.Join(apiDir, name), []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	responses := filepath.Join(t.TempDir(), "responses.json")
	t.Setenv("UNION_REQUESTS", requestsPath)
	t.Setenv("UNION_RESPONSES", responses)
	runGoAPIModule(t, apiDir)
	problems, err := sdktest.CheckUnionResponses(requests, responses)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) > 0 {
		t.Fatalf("the Go routes answered union_requests.json otherwise:\n%s", strings.Join(problems, "\n"))
	}
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

func (s shapes) DrawShape(_ context.Context, shape types.Shape) (*types.Shape, error) {
	return &shape, nil
}

func (s shapes) Pay(context.Context, types.Payment) (*string, error) {
	return nil, nil
}

func (s shapes) Checkout(context.Context, *types.Checkout) (*string, error) {
	return nil, nil
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

// unionRequestsAPIServerTest runs in the generated API module. Its
// implementation answers drawShape with the shape it received, pay with
// the payment's kind, and checkout with the payment's kind, a colon and the
// number of shapes. It sends each request of $UNION_REQUESTS and writes
// what the routes answered to $UNION_RESPONSES.
const unionRequestsAPIServerTest = `package unionlistsapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	api "example.com/schemas/api/union-lists-api"
	types "example.com/schemas/types/go/union-lists-api"
)

type unionRequests struct{ shapes }

func kindOf(payment types.Payment) string {
	switch member := payment.(type) {
	case types.Card:
		return member.Kind
	case types.Invoice:
		return member.Kind
	}
	return fmt.Sprintf("%T", payment)
}

func (unionRequests) Pay(_ context.Context, payment types.Payment) (*string, error) {
	kind := kindOf(payment)
	return &kind, nil
}

func (unionRequests) Checkout(_ context.Context, input *types.Checkout) (*string, error) {
	answer := fmt.Sprintf("%s:%d", kindOf(input.Payment), len(input.Shapes.Value))
	return &answer, nil
}

func TestUnionRequests(t *testing.T) {
	data, err := os.ReadFile(os.Getenv("UNION_REQUESTS"))
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Requests []struct {
			Name string          ` + "`json:\"name\"`" + `
			Path string          ` + "`json:\"path\"`" + `
			Body json.RawMessage ` + "`json:\"body\"`" + `
		} ` + "`json:\"requests\"`" + `
	}
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	if err := api.RegisterRoutes(router, api.Config{
		Logger:          zap.NewNop(),
		Implementations: api.Implementations{Shape: unionRequests{}},
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	defer server.Close()
	type response struct {
		Name   string          ` + "`json:\"name\"`" + `
		Status int             ` + "`json:\"status\"`" + `
		Body   json.RawMessage ` + "`json:\"body\"`" + `
	}
	var responses []response
	for _, request := range file.Requests {
		resp, err := http.Post(server.URL+request.Path, "application/json", bytes.NewReader(request.Body))
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		responses = append(responses, response{Name: request.Name, Status: resp.StatusCode, Body: body})
	}
	out, err := json.Marshal(responses)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("UNION_RESPONSES"), out, 0o644); err != nil {
		t.Fatal(err)
	}
}
`
