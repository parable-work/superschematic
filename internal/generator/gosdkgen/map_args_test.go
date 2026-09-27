package gosdkgen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// mapArgsService is a schema local to this package's testdata, so its
// operations change no other generator's goldens.
const mapArgsService = "map-args-api"

// loadMapArgsAPI loads map-args-api: map body arguments of an enum, of
// lists of a string scalar, of an object type and of a builtin.
func loadMapArgsAPI(t *testing.T) (*ir.Schema, *apigen.APIOutput) {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join("testdata", "services", mapArgsService))
	if err != nil {
		t.Fatalf("load %s: %v", mapArgsService, err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:    sessionauth.Provider{},
		SchemaName:  mapArgsService,
		ModulePath:  "example.com/schemas/api/" + mapArgsService,
		TypesModule: "example.com/schemas/types/go/" + mapArgsService,
		Clock:       nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return schema, apiOutput
}

// TestMapBodyArgumentsAreGoMaps: the input struct types a map argument as
// the Go route does, map[string]T or map[string][]T, not as its value type.
func TestMapBodyArgumentsAreGoMaps(t *testing.T) {
	_, apiOutput := loadMapArgsAPI(t)
	sdkOutput, err := Generate(apiOutput, "example.com/schemas/sdk/go/"+mapArgsService, "sdk", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(sdkOutput.Namespaces) != 1 || len(sdkOutput.Namespaces[0].Endpoints) != 1 {
		t.Fatalf("namespaces = %+v", sdkOutput.Namespaces)
	}
	args := sdkOutput.Namespaces[0].Endpoints[0].ScalarArgs
	want := []struct {
		name, goType string
	}{
		{"shadeByName", "map[string]types.Shade"},
		{"linksByLocale", "map[string][]types.NetworkUrl"},
		{"pointByName", "map[string]types.Point"},
		{"weightByName", "map[string]float64"},
	}
	if len(args) != len(want) {
		t.Fatalf("scalar args = %+v", args)
	}
	for i, w := range want {
		if got := args[i]; got.Name != w.name || got.GoType != w.goType || got.Pointer {
			t.Errorf("argument %d = %s %s (pointer %t), want %s %s", i, got.Name, got.GoType, got.Pointer, w.name, w.goType)
		}
	}
	if !sdkOutput.ValidatesListElements {
		t.Error("ValidatesListElements = false with map arguments whose values validate")
	}
}

// TestWriteSDKGoldenMapArgs pins the namespace and validation files of the
// Go SDK for map-args-api. Regenerate with
// go test ./internal/generator/gosdkgen -run TestWriteSDKGoldenMapArgs -update
func TestWriteSDKGoldenMapArgs(t *testing.T) {
	_, apiOutput := loadMapArgsAPI(t)
	root := t.TempDir()
	sdkDir := filepath.Join(root, "sdk", "go", mapArgsService)
	typesDir := filepath.Join(root, "types", "go", mapArgsService)
	if err := os.MkdirAll(typesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sdkOutput, err := Generate(apiOutput, "example.com/schemas/sdk/go/"+mapArgsService, "sdk", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, typesDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	goldenDir := filepath.Join("testdata", "golden", mapArgsService)
	for _, name := range []string{"namespaces/post.go", "namespaces/validation.go"} {
		got, err := os.ReadFile(filepath.Join(sdkDir, name))
		if err != nil {
			t.Fatal(err)
		}
		goldenPath := filepath.Join(goldenDir, name)
		if *update {
			if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(goldenPath)
		if err != nil {
			t.Fatalf("read golden %s (run with -update): %v", name, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from golden (run with -update to accept)", name)
		}
	}
}

// TestMapArgsSDKBuildsAndRuns runs mapArgsSDKTest in the generated SDK: a
// map argument crosses an httptest server as a JSON object, an optional one
// is sent only when it is not nil, and a nil required map, a nil list value
// or a value that fails its own validation is refused at its path before
// any request.
func TestMapArgsSDKBuildsAndRuns(t *testing.T) {
	schema, apiOutput := loadMapArgsAPI(t)
	runInSDK(t, schema, apiOutput, mapArgsService, "map_args_test.go", mapArgsSDKTest)
}

// mapArgsSDKTest runs in the generated SDK module against an httptest
// server that answers every request with the success envelope around true.
const mapArgsSDKTest = `package sdk_test

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

	sdk "example.com/schemas/sdk/go/map-args-api"
	"example.com/schemas/sdk/go/map-args-api/namespaces"
	types "example.com/schemas/types/go/map-args-api"
)

type call struct {
	method, path string
	body         any
}

func serve(t *testing.T) (*sdk.MapArgsApiSDK, *[]call) {
	t.Helper()
	calls := &[]call{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("request body is not JSON: %s", raw)
		}
		*calls = append(*calls, call{r.Method, r.URL.Path, body})
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, ` + "`" + `{"data":true,"meta":{"requestId":"req-1"}}` + "`" + `)
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

func TestNameThingsSendsMapsAsJSONObjects(t *testing.T) {
	client, calls := serve(t)
	named, err := client.PostNamespace.NameThings(context.Background(), "p1", namespaces.PostNameThingsInput{
		ShadeByName:   map[string]types.Shade{"a": types.Shade_Light},
		LinksByLocale: map[string][]types.NetworkUrl{"en": {"https://a.test"}, "fr": {}},
		PointByName:   map[string]types.Point{"p": {X: 1, Y: 2}},
		WeightByName:  map[string]float64{"w": 0.5},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !named {
		t.Errorf("NameThings = %v, want true", named)
	}
	if len(*calls) != 1 || (*calls)[0].method != "PUT" || (*calls)[0].path != "/api/posts/p1/names" {
		t.Fatalf("calls = %+v", *calls)
	}
	want := decode(t, ` + "`" + `{"shadeByName":{"a":"light"},"linksByLocale":{"en":["https://a.test"],"fr":[]},"pointByName":{"p":{"x":1,"y":2}},"weightByName":{"w":0.5}}` + "`" + `)
	if !reflect.DeepEqual((*calls)[0].body, want) {
		t.Errorf("body = %v, want %v", (*calls)[0].body, want)
	}
}

func TestNameThingsSendsAnOptionalMapOnlyWhenSet(t *testing.T) {
	client, calls := serve(t)
	if _, err := client.PostNamespace.NameThings(context.Background(), "p1", namespaces.PostNameThingsInput{
		ShadeByName: map[string]types.Shade{},
	}); err != nil {
		t.Fatal(err)
	}
	if want := decode(t, ` + "`" + `{"shadeByName":{}}` + "`" + `); !reflect.DeepEqual((*calls)[0].body, want) {
		t.Errorf("body = %v, want %v", (*calls)[0].body, want)
	}
}

func TestNameThingsValidatesEachValueAtItsKey(t *testing.T) {
	client, calls := serve(t)
	_, err := client.PostNamespace.NameThings(context.Background(), "p1", namespaces.PostNameThingsInput{
		ShadeByName:   map[string]types.Shade{"a": types.Shade_Light, "b": "dim"},
		LinksByLocale: map[string][]types.NetworkUrl{"en": {"https://a.test", "not a url"}, "fr": nil},
		PointByName:   map[string]types.Point{"p": {X: -1}},
	})
	if got, want := fields(t, err), []string{"linksByLocale[en][1]", "linksByLocale[fr]", "pointByName[p]", "shadeByName[b]"}; !reflect.DeepEqual(got, want) {
		t.Errorf("fields = %v, want %v", got, want)
	}
	_, err = client.PostNamespace.NameThings(context.Background(), "p1", namespaces.PostNameThingsInput{})
	if got := fields(t, err); !reflect.DeepEqual(got, []string{"shadeByName"}) {
		t.Errorf("fields = %v, want [shadeByName]", got)
	}
	if len(*calls) != 0 {
		t.Errorf("a refused request was sent: %+v", *calls)
	}
}
`
