package gosdkgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/typegen"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// TestNestedArraysSDKBuildsAndRuns runs nestedArraysSDKTest in the generated
// SDK: [][]T arguments and responses cross an httptest server as nested JSON
// arrays, a nil inner list or a bad element fails validation at its index
// path before any request, and an optional list argument is sent only when
// it is not nil.
func TestNestedArraysSDKBuildsAndRuns(t *testing.T) {
	sdkOutput := runInNestedArraysSDK(t, "nested_arrays_test.go", nestedArraysSDKTest)
	if !sdkOutput.ValidatesListElements {
		t.Fatal("ValidatesListElements = false with grid.paint's Shade[][] and Point[][] arguments")
	}
}

// runInNestedArraysSDK generates the Go types module and the Go SDK of
// fixture-nested-arrays-api, with grid.paint added, into a temp tree laid
// out as a build writes it, writes testSource into the SDK module as
// testFile, then runs go mod tidy, go build, go vet and go test there.
func runInNestedArraysSDK(t *testing.T, testFile, testSource string) *SDKOutput {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	paths := testpaths.Local(t)
	schema, apiOutput := loadNestedArraysAPI(t, true)

	root := t.TempDir()
	typesDir := filepath.Join(root, "types", "go", nestedArraysService)
	sdkDir := filepath.Join(root, "sdk", "go", nestedArraysService)

	typesOutput, err := typegen.Generate(schema, typegen.Options{
		SchemaName: nestedArraysService,
		ModulePath: nestedArraysTypesModule,
		Clock:      nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("typegen.Generate: %v", err)
	}
	if err := typegen.SetReplacePaths(typesOutput, paths, typesDir); err != nil {
		t.Fatalf("typegen.SetReplacePaths: %v", err)
	}
	if err := typegen.WriteTypes(typesOutput, typesDir); err != nil {
		t.Fatalf("typegen.WriteTypes: %v", err)
	}

	sdkOutput := writeNestedArraysSDK(t, apiOutput, sdkDir, typesDir)
	if err := os.WriteFile(filepath.Join(sdkDir, testFile), []byte(testSource), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{"mod", "tidy"},
		{"build", "./..."},
		{"vet", "./..."},
		{"test", "-count=1", "./..."},
	} {
		cmd := exec.Command("go", args...)
		cmd.Dir = sdkDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %s in the generated SDK: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	return sdkOutput
}

// nestedArraysSDKTest runs in the generated SDK module against an httptest
// server that answers every request with the success envelope around a
// canned body.
const nestedArraysSDKTest = `package sdk_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"

	sdk "example.com/schemas/sdk/go/fixture-nested-arrays-api"
	"example.com/schemas/sdk/go/fixture-nested-arrays-api/namespaces"
	types "example.com/schemas/types/go/fixture-nested-arrays-api"
)

const gridID = "0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40"

type call struct {
	method, path, query string
	body                any
}

func serve(t *testing.T, data string) (*sdk.FixtureNestedArraysApiSDK, *[]call) {
	t.Helper()
	calls := &[]call{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body any
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Errorf("request body is not JSON: %s", raw)
			}
		}
		*calls = append(*calls, call{r.Method, r.URL.Path, r.URL.RawQuery, body})
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, ` + "`" + `{"data":` + "`" + `+data+` + "`" + `,"meta":{"requestId":"req-1"}}` + "`" + `)
	}))
	t.Cleanup(server.Close)
	client, err := sdk.New(sdk.SDKConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	return client, calls
}

func decode(t *testing.T, value string) any {
	t.Helper()
	var decoded any
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

// fields returns the sorted field paths of a client-side validation error.
func fields(t *testing.T, err error) []string {
	t.Helper()
	var validation *namespaces.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("err = %v, want a validation error", err)
	}
	var names []string
	for name := range validation.Errors {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

const view = ` + "`" + `{"id":"` + "`" + ` + gridID + ` + "`" + `","labels":[["a","b"],[]],"shades":[["light"]],"polygons":[[{"x":1,"y":2}],[]]}` + "`" + `

func TestReplaceLabelsSendsNestedArrays(t *testing.T) {
	client, calls := serve(t, view)
	got, err := client.GridNamespace.ReplaceLabels(context.Background(), gridID, namespaces.GridReplaceLabelsInput{
		Labels: [][]string{{"a", "b"}, {}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0].method != "PUT" || (*calls)[0].path != "/api/grids/"+gridID+"/labels" {
		t.Fatalf("calls = %+v", *calls)
	}
	if want := decode(t, ` + "`" + `{"labels":[["a","b"],[]]}` + "`" + `); !reflect.DeepEqual((*calls)[0].body, want) {
		t.Errorf("body = %v, want %v", (*calls)[0].body, want)
	}
	if !reflect.DeepEqual(got.Labels, [][]string{{"a", "b"}, {}}) || !reflect.DeepEqual(got.Polygons, [][]types.Point{{{X: 1, Y: 2}}, {}}) {
		t.Errorf("view = %+v", got)
	}
}

func TestReplaceLabelsRefusesNilLists(t *testing.T) {
	client, calls := serve(t, view)
	_, err := client.GridNamespace.ReplaceLabels(context.Background(), gridID, namespaces.GridReplaceLabelsInput{
		Labels: [][]string{{"a"}, nil},
	})
	if got := fields(t, err); !reflect.DeepEqual(got, []string{"labels[1]"}) {
		t.Errorf("fields = %v, want [labels[1]]", got)
	}
	_, err = client.GridNamespace.ReplaceLabels(context.Background(), gridID, namespaces.GridReplaceLabelsInput{})
	if got := fields(t, err); !reflect.DeepEqual(got, []string{"labels"}) {
		t.Errorf("fields = %v, want [labels]", got)
	}
	if len(*calls) != 0 {
		t.Errorf("a refused request was sent: %+v", *calls)
	}
}

func TestGridLabelsDecodesNestedArrays(t *testing.T) {
	client, calls := serve(t, ` + "`" + `[["a","b"],[],["c"]]` + "`" + `)
	limit := 2.0
	got, err := client.GridNamespace.GridLabels(context.Background(), gridID, &namespaces.GridGridLabelsQueryParams{Limit: &limit})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, [][]string{{"a", "b"}, {}, {"c"}}) {
		t.Errorf("labels = %v", got)
	}
	if (*calls)[0].method != "GET" || (*calls)[0].query != "limit=2" {
		t.Errorf("call = %+v", (*calls)[0])
	}
}

func TestPaintValidatesElements(t *testing.T) {
	client, calls := serve(t, ` + "`" + `[[{"x":1,"y":2}],[]]` + "`" + `)
	_, err := client.GridNamespace.Paint(context.Background(), gridID, namespaces.GridPaintInput{
		Shades:   [][]types.Shade{{types.Shade_Light, "dim"}, nil},
		Polygons: [][]types.Point{nil},
	})
	if got := fields(t, err); !reflect.DeepEqual(got, []string{"polygons[0]", "shades[0][1]", "shades[1]"}) {
		t.Errorf("fields = %v", got)
	}
	if len(*calls) != 0 {
		t.Fatalf("a refused request was sent: %+v", *calls)
	}

	got, err := client.GridNamespace.Paint(context.Background(), gridID, namespaces.GridPaintInput{
		Shades:   [][]types.Shade{{types.Shade_Dark}, {}},
		Polygons: [][]types.Point{{{X: 3, Y: 4}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := decode(t, ` + "`" + `{"shades":[["dark"],[]],"polygons":[[{"x":3,"y":4}]]}` + "`" + `); !reflect.DeepEqual((*calls)[0].body, want) {
		t.Errorf("body = %v, want %v", (*calls)[0].body, want)
	}
	if !reflect.DeepEqual(got, [][]types.Point{{{X: 1, Y: 2}}, {}}) {
		t.Errorf("polygons = %v", got)
	}
}

func TestPaintSendsAnOptionalListOnlyWhenSet(t *testing.T) {
	client, calls := serve(t, ` + "`" + `[]` + "`" + `)
	for _, polygons := range [][][]types.Point{nil, {}} {
		if _, err := client.GridNamespace.Paint(context.Background(), gridID, namespaces.GridPaintInput{
			Shades:   [][]types.Shade{},
			Polygons: polygons,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if want := decode(t, ` + "`" + `{"shades":[]}` + "`" + `); !reflect.DeepEqual((*calls)[0].body, want) {
		t.Errorf("nil polygons: body = %v, want %v", (*calls)[0].body, want)
	}
	if want := decode(t, ` + "`" + `{"shades":[],"polygons":[]}` + "`" + `); !reflect.DeepEqual((*calls)[1].body, want) {
		t.Errorf("empty polygons: body = %v, want %v", (*calls)[1].body, want)
	}
}
`
