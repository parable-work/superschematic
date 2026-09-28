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

// queryListsService is a schema local to this package's testdata, so its
// operations change no other generator's goldens.
const queryListsService = "query-lists-api"

// loadQueryListsAPI loads query-lists-api: list query parameters of an
// enum, a UUID scalar, an integer scalar, strings and booleans.
func loadQueryListsAPI(t *testing.T) (*ir.Schema, *apigen.APIOutput) {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join("testdata", "services", queryListsService))
	if err != nil {
		t.Fatalf("load %s: %v", queryListsService, err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:    sessionauth.Provider{},
		SchemaName:  queryListsService,
		ModulePath:  "example.com/schemas/api/" + queryListsService,
		TypesModule: "example.com/schemas/types/go/" + queryListsService,
		Clock:       nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return schema, apiOutput
}

// TestListQueryParamsAreGoSlices: the query struct types a list query
// parameter as the Go route takes it, []T of the enum, of the scalar's type
// or of the primitive, not as one string.
func TestListQueryParamsAreGoSlices(t *testing.T) {
	_, apiOutput := loadQueryListsAPI(t)
	sdkOutput, err := Generate(apiOutput, "example.com/schemas/sdk/go/"+queryListsService, "sdk", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(sdkOutput.Namespaces) != 1 || len(sdkOutput.Namespaces[0].Endpoints) != 1 {
		t.Fatalf("namespaces = %+v", sdkOutput.Namespaces)
	}
	params := sdkOutput.Namespaces[0].Endpoints[0].QueryParams
	want := []struct {
		name, goType     string
		pointer, isArray bool
	}{
		{"ids", "[]types.IdentityUUID", false, true},
		{"shades", "[]types.Shade", false, true},
		{"ranks", "[]int64", false, true},
		{"codes", "[]string", false, true},
		{"tags", "[]string", false, true},
		{"flags", "[]bool", false, true},
		{"limit", "float64", true, false},
	}
	if len(params) != len(want) {
		t.Fatalf("query params = %+v", params)
	}
	for i, w := range want {
		if got := params[i]; got.Name != w.name || got.GoType != w.goType || got.Pointer != w.pointer || got.IsArray != w.isArray {
			t.Errorf("parameter %d = %s %s (pointer %t, list %t), want %s %s (pointer %t, list %t)",
				i, got.Name, got.GoType, got.Pointer, got.IsArray, w.name, w.goType, w.pointer, w.isArray)
		}
	}
	if !sdkOutput.HasQueryLists || !sdkOutput.ChecksQueryListItems || !sdkOutput.ValidatesListElements {
		t.Errorf("HasQueryLists %t, ChecksQueryListItems %t, ValidatesListElements %t, want all true",
			sdkOutput.HasQueryLists, sdkOutput.ChecksQueryListItems, sdkOutput.ValidatesListElements)
	}
}

// TestListQueryParamElementTypes pins the element type of a list query
// parameter for each kind of schema type, beside the scalar parameter's.
func TestListQueryParamElementTypes(t *testing.T) {
	for _, test := range []struct {
		param apigen.Param
		want  string
	}{
		{apigen.Param{Type: "Generic.Int64", IsInt: true}, "[]int64"},
		{apigen.Param{Type: "Generic.Probability", IsFloat: true}, "[]float64"},
		{apigen.Param{Type: "Acme.Flag", IsBool: true}, "[]bool"},
		{apigen.Param{Type: "Identity.UUID", IsUUID: true}, "[]types.IdentityUUID"},
		{apigen.Param{Type: "Temporal.DateTime", IsDateTime: true}, "[]types.TemporalDateTime"},
		{apigen.Param{Type: "OrderStatus"}, "[]types.OrderStatus"},
		{apigen.Param{Type: "number"}, "[]float64"},
		{apigen.Param{Type: "boolean"}, "[]bool"},
		{apigen.Param{Type: "string", IsString: true}, "[]string"},
	} {
		t.Run(test.param.Type, func(t *testing.T) {
			test.param.Name = "values"
			test.param.IsArray = true
			endpoint := convertEndpoint(apigen.EndpointInfo{
				Path: "/items", Method: "GET",
				QueryParams: []apigen.Param{test.param},
			}, false, "", "Items", map[string]string{
				"Identity.UUID":     "IdentityUUID",
				"Temporal.DateTime": "TemporalDateTime",
			})
			if got := endpoint.QueryParams[0]; got.GoType != test.want || got.Pointer || !got.IsArray {
				t.Errorf("list query parameter = %+v, want %s", got, test.want)
			}
		})
	}
}

// TestWriteSDKGoldenQueryLists pins the namespace, validation and runtime
// files of the Go SDK for query-lists-api. Regenerate with
// go test ./internal/generator/gosdkgen -run TestWriteSDKGoldenQueryLists -update
func TestWriteSDKGoldenQueryLists(t *testing.T) {
	_, apiOutput := loadQueryListsAPI(t)
	root := t.TempDir()
	sdkDir := filepath.Join(root, "sdk", "go", queryListsService)
	typesDir := filepath.Join(root, "types", "go", queryListsService)
	if err := os.MkdirAll(typesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sdkOutput, err := Generate(apiOutput, "example.com/schemas/sdk/go/"+queryListsService, "sdk", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, typesDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	goldenDir := filepath.Join("testdata", "golden", queryListsService)
	for _, name := range []string{"namespaces/post.go", "namespaces/validation.go", "runtime/runtime.go"} {
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

// TestQueryListsSDKBuildsAndRuns runs queryListsSDKTest in the generated
// SDK: a list query parameter crosses an httptest server as one
// comma-separated value, an empty list is left out, and a missing required
// list, a list out of its bounds or an item that fails a rule, its own
// validation or the comma-separated form is refused at its path before any
// request.
func TestQueryListsSDKBuildsAndRuns(t *testing.T) {
	schema, apiOutput := loadQueryListsAPI(t)
	runInSDK(t, schema, apiOutput, queryListsService, "query_lists_test.go", queryListsSDKTest)
}

// queryListsSDKTest runs in the generated SDK module against an httptest
// server that answers every request with the success envelope around 3.
const queryListsSDKTest = `package sdk_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	sdk "example.com/schemas/sdk/go/query-lists-api"
	"example.com/schemas/sdk/go/query-lists-api/namespaces"
	types "example.com/schemas/types/go/query-lists-api"
)

const (
	idA = "0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40"
	idB = "5d2f8a3c-1e40-4c1a-9b7e-0b9a4e1c6f2d"
)

func serve(t *testing.T) (*sdk.QueryListsApiSDK, *[]url.Values) {
	t.Helper()
	queries := &[]url.Values{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/posts/count" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		*queries = append(*queries, r.URL.Query())
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, ` + "`" + `{"data":3,"meta":{"requestId":"req-1"}}` + "`" + `)
	}))
	t.Cleanup(server.Close)
	client, err := sdk.New(sdk.SDKConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	return client, queries
}

func id(t *testing.T, value string) types.IdentityUUID {
	t.Helper()
	parsed, err := types.ParseIdentityUUID(value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

// validators maps each field path of a client-side validation error to
// its first validator.
func validators(t *testing.T, err error) map[string]string {
	t.Helper()
	var validation *namespaces.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("err = %v, want a validation error", err)
	}
	got := map[string]string{}
	for path := range validation.Errors {
		fieldErrors := validation.Errors.GetFieldErrors(path)
		if len(fieldErrors) == 0 {
			t.Fatalf("%s has no field errors", path)
		}
		got[path] = fieldErrors[0].Validator
	}
	return got
}

func TestCountPostsSendsListsAsCommaSeparatedValues(t *testing.T) {
	client, queries := serve(t)
	limit := 20.0
	a, b := id(t, idA), id(t, idB)
	count, err := client.PostNamespace.CountPosts(context.Background(), &namespaces.PostCountPostsQueryParams{
		Ids:    []types.IdentityUUID{a, b},
		Shades: []types.Shade{types.Shade_Light, types.Shade_Dark},
		Ranks:  []int64{1, 100},
		Codes:  []string{"ab", "wxyz"},
		Tags:   []string{"go", "sdk"},
		Flags:  []bool{true, false},
		Limit:  &limit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Errorf("count = %v, want 3", count)
	}
	// A UUID travels in its JSON form, which parses back to the same UUID.
	want := url.Values{
		"ids":    {a.String() + "," + b.String()},
		"shades": {"light,dark"},
		"ranks":  {"1,100"},
		"codes":  {"ab,wxyz"},
		"tags":   {"go,sdk"},
		"flags":  {"true,false"},
		"limit":  {"20"},
	}
	if len(*queries) != 1 || !reflect.DeepEqual((*queries)[0], want) {
		t.Fatalf("queries = %v, want [%v]", *queries, want)
	}
	for i, item := range strings.Split((*queries)[0].Get("ids"), ",") {
		if parsed := id(t, item); parsed != []types.IdentityUUID{a, b}[i] {
			t.Errorf("ids[%d] = %s parses to %v", i, item, parsed)
		}
	}
}

func TestCountPostsLeavesOutEmptyLists(t *testing.T) {
	client, queries := serve(t)
	a := id(t, idA)
	if _, err := client.PostNamespace.CountPosts(context.Background(), &namespaces.PostCountPostsQueryParams{
		Ids:    []types.IdentityUUID{a},
		Shades: []types.Shade{},
		Codes:  []string{},
	}); err != nil {
		t.Fatal(err)
	}
	if want := (url.Values{"ids": {a.String()}}); len(*queries) != 1 || !reflect.DeepEqual((*queries)[0], want) {
		t.Errorf("queries = %v, want [%v]", *queries, want)
	}
}

func TestCountPostsRefusesBadListsBeforeTheRequest(t *testing.T) {
	client, queries := serve(t)
	for _, query := range []*namespaces.PostCountPostsQueryParams{nil, {}, {Ids: []types.IdentityUUID{}}} {
		_, err := client.PostNamespace.CountPosts(context.Background(), query)
		if got, want := validators(t, err), map[string]string{"ids": "required"}; !reflect.DeepEqual(got, want) {
			t.Errorf("query %+v: validators = %v, want %v", query, got, want)
		}
	}

	limit := 51.0
	_, err := client.PostNamespace.CountPosts(context.Background(), &namespaces.PostCountPostsQueryParams{
		Ids:    []types.IdentityUUID{id(t, idA), {}},
		Shades: []types.Shade{types.Shade_Light},
		Ranks:  []int64{0, 5, 101},
		Codes:  []string{"a", "abcde", "AB", "ab"},
		Tags:   []string{"", "a,b", " c", "ok"},
		Limit:  &limit,
	})
	want := map[string]string{
		"ids[1]":   "required",
		"shades":   "listMin",
		"ranks[0]": "min",
		"ranks[2]": "max",
		"codes[0]": "minLength",
		"codes[1]": "maxLength",
		"codes[2]": "pattern",
		"tags[0]":  "required",
		"tags[1]":  "pattern",
		"tags[2]":  "pattern",
		"limit":    "max",
	}
	if got := validators(t, err); !reflect.DeepEqual(got, want) {
		t.Errorf("validators = %v, want %v", got, want)
	}

	_, err = client.PostNamespace.CountPosts(context.Background(), &namespaces.PostCountPostsQueryParams{
		Ids:    []types.IdentityUUID{id(t, idA)},
		Shades: []types.Shade{types.Shade_Light, types.Shade_Dark, types.Shade_Light, "dim"},
	})
	if got, want := validators(t, err), map[string]string{"shades": "listMax", "shades[3]": "enum"}; !reflect.DeepEqual(got, want) {
		t.Errorf("validators = %v, want %v", got, want)
	}
	if len(*queries) != 0 {
		t.Errorf("a refused request was sent: %v", *queries)
	}
}
`
