package gosdkgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/sdkgen/sdktest"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

const (
	pagedService   = "fixture-paged-api"
	pagedSDKModule = "example.com/schemas/sdk/go/fixture-paged-api"
)

// loadPagedAPI loads fixture-paged-api: Catalog.listItems pages ItemView
// two to a page, up to five, beside its ids and inStock query parameters,
// and Catalog.listTags pages strings with the default limits (D60). Each
// edit then changes the schema (sdktest.AddShelfItemsOperation).
func loadPagedAPI(t *testing.T, edits ...func(*ir.Schema) error) (*ir.Schema, *apigen.APIOutput) {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, pagedService))
	if err != nil {
		t.Fatalf("load %s: %v", pagedService, err)
	}
	for _, edit := range edits {
		if err := edit(schema); err != nil {
			t.Fatal(err)
		}
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:    sessionauth.Provider{},
		SchemaName:  pagedService,
		ModulePath:  "example.com/schemas/api/" + pagedService,
		TypesModule: "example.com/schemas/types/go/" + pagedService,
		Clock:       nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return schema, apiOutput
}

// TestPagedMethodsReturnAPageAndIterate pins the Go SDK's paged endpoints
// (D60): each returns runtime.Page of its list's item, takes a
// runtime.PageRequest after its arguments and has a <Method>All; an
// endpoint that does not page is unchanged.
func TestPagedMethodsReturnAPageAndIterate(t *testing.T) {
	_, apiOutput := loadPagedAPI(t)
	sdkOutput, err := Generate(apiOutput, pagedSDKModule, "sdk", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !sdkOutput.HasPagedEndpoints {
		t.Fatal("HasPagedEndpoints = false")
	}
	want := map[string]struct{ output, item, all string }{
		"GetItem":     {"types.ItemView", "", ""},
		"ListItems":   {"runtime.Page[types.ItemView]", "types.ItemView", "ListItemsAll"},
		"ListTags":    {"runtime.Page[string]", "string", "ListTagsAll"},
		"PlaceBasket": {"types.BasketView", "", ""},
	}
	for _, ns := range sdkOutput.Namespaces {
		for _, ep := range ns.Endpoints {
			w, ok := want[ep.MethodName]
			if !ok {
				t.Errorf("unexpected endpoint %s", ep.MethodName)
				continue
			}
			delete(want, ep.MethodName)
			if ep.OutputGoType != w.output || ep.ItemGoType != w.item || ep.AllMethodName != w.all || (ep.Page != nil) != (w.all != "") {
				t.Errorf("%s = (%s, %s, %s, page %v), want (%s, %s, %s)", ep.MethodName, ep.OutputGoType, ep.ItemGoType, ep.AllMethodName, ep.Page, w.output, w.item, w.all)
			}
		}
	}
	if len(want) > 0 {
		t.Errorf("missing endpoints %v", want)
	}
}

// TestWriteSDKGoldenPaged pins every file of the Go SDK for
// fixture-paged-api. Regenerate with:
// go test ./internal/generator/gosdkgen -run TestWriteSDKGoldenPaged -update
func TestWriteSDKGoldenPaged(t *testing.T) {
	_, apiOutput := loadPagedAPI(t)
	root := t.TempDir()
	sdkDir := filepath.Join(root, "sdk", "go", pagedService)
	typesDir := filepath.Join(root, "types", "go", pagedService)
	if err := os.MkdirAll(typesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sdkOutput, err := Generate(apiOutput, pagedSDKModule, "sdk", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, typesDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	compareGoldenTree(t, sdkDir, filepath.Join("testdata", "golden", pagedService))
}

// TestPagesChangeNoOtherSDKFile: the page's runtime, validation and
// iterator code is written only for an SDK with a paged endpoint.
func TestPagesChangeNoOtherSDKFile(t *testing.T) {
	_, apiOutput := loadNestedArraysAPI(t)
	sdkDir := t.TempDir()
	writeNestedArraysSDK(t, apiOutput, sdkDir, t.TempDir())
	for _, name := range []string{"runtime/runtime.go", "namespaces/validation.go", "namespaces/grid.go", "README.md"} {
		source, err := os.ReadFile(filepath.Join(sdkDir, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, marker := range []string{"PageRequest", "AllItems", "pageLimitErrors", "iter.Seq2", "Paged lists"} {
			if strings.Contains(string(source), marker) {
				t.Errorf("%s of an SDK without pages carries %s", name, marker)
			}
		}
	}
}

// TestPagedSDKBuildsAndRuns runs pagedSDKTest in the generated SDK, with
// Catalog.listShelfItems added (a path parameter and body arguments beside
// the page), against an httptest server that pages as D60's routers do:
// the first page, the next and the last, the query each request sends, the
// limit refused before any request, and the iterators collecting every
// item lazily, stopping at a null next and ending with an error.
func TestPagedSDKBuildsAndRuns(t *testing.T) {
	schema, apiOutput := loadPagedAPI(t, sdktest.AddShelfItemsOperation)
	runInSDK(t, schema, apiOutput, pagedService, "paged_test.go", pagedSDKTest)
}

// pagedSDKTest runs in the generated SDK module. Its server pages a list as
// runtime/http/testdata/page_parity.json's fromRows does: a cursor is the
// unpadded base64url of the compact JSON of the position of the last item
// of the page before ("Mg" after the second), a page holds up to limit
// items, and next is null after the last.
const pagedSDKTest = `package sdk_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	sdk "example.com/schemas/sdk/go/fixture-paged-api"
	"example.com/schemas/sdk/go/fixture-paged-api/namespaces"
	"example.com/schemas/sdk/go/fixture-paged-api/runtime"
	types "example.com/schemas/types/go/fixture-paged-api"
)

var names = []string{"anvil", "bucket", "crate", "drum", "easel"}

var tags = []string{"blue", "green", "red"}

const shelfID = "5d0c7e2a-1b3f-4a6d-8c9e-0f1a2b3c4d5e"

// server pages names (as items) and tags. repeat makes a page's next the
// cursor it was asked with; refuse is a cursor it answers with the 400 of
// a cursor no page returned.
type server struct {
	mu      sync.Mutex
	paths   []string
	queries []url.Values
	repeat  bool
	refuse  string
}

func cursorOf(position int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(position)))
}

func (s *server) requests() []url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]url.Values(nil), s.queries...)
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.paths = append(s.paths, r.URL.Path)
	s.queries = append(s.queries, r.URL.Query())
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")

	defaultLimit, maxLimit, count := 2, 5, len(names)
	if r.URL.Path == "/api/tags" {
		defaultLimit, maxLimit, count = 20, 100, len(tags)
	} else if r.URL.Path != "/api/items" && r.URL.Path != "/api/shelves/"+shelfID+"/items" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	query := r.URL.Query()
	limit := defaultLimit
	if text := query.Get("limit"); text != "" {
		parsed, err := strconv.Atoi(text)
		if err != nil || parsed < 1 || parsed > maxLimit {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, ` + "`" + `{"status":400,"code":"bad_request","detail":"Invalid query parameter limit: must be at most %d"}` + "`" + `, maxLimit)
			return
		}
		limit = parsed
	}
	cursor := query.Get("cursor")
	after := 0
	if cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err == nil {
			err = json.Unmarshal(raw, &after)
		}
		if err != nil || cursor == s.refuse {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(` + "`" + `{"status":400,"code":"bad_request","detail":"cursor is not one this operation returned"}` + "`" + `))
			return
		}
	}

	var items []any
	for position := after + 1; position <= count && len(items) < limit; position++ {
		if r.URL.Path == "/api/tags" {
			items = append(items, tags[position-1])
			continue
		}
		items = append(items, map[string]any{
			"id":      fmt.Sprintf("00000000-0000-4000-8000-%012d", position),
			"name":    names[position-1],
			"sku":     names[position-1] + "-sku",
			"inStock": position%2 == 1,
		})
	}
	var next any
	if last := after + len(items); last < count {
		next = cursorOf(last)
	}
	if s.repeat && cursor != "" {
		next = cursor
	}
	if items == nil {
		items = []any{}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"data": map[string]any{"items": items, "next": next},
		"meta": map[string]any{"requestId": "req-1"},
	})
}

func serve(t *testing.T) (*sdk.FixturePagedApiSDK, *server) {
	t.Helper()
	pages := &server{}
	httpServer := httptest.NewServer(pages)
	t.Cleanup(httpServer.Close)
	client, err := sdk.New(sdk.SDKConfig{BaseURL: httpServer.URL})
	if err != nil {
		t.Fatal(err)
	}
	return client, pages
}

func itemNames(t *testing.T, page runtime.Page[types.ItemView]) []string {
	t.Helper()
	var got []string
	for _, item := range page.Items {
		got = append(got, item.Name)
	}
	return got
}

func next(page runtime.Page[types.ItemView]) string {
	if page.Next == nil {
		return "<nil>"
	}
	return *page.Next
}

func TestListItemsFirstNextAndLastPage(t *testing.T) {
	client, pages := serve(t)
	ctx := context.Background()

	first, err := client.CatalogNamespace.ListItems(ctx, nil, runtime.PageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	// The vectors' fromRows: five rows two to a page, next "Mg".
	if got := itemNames(t, first); !reflect.DeepEqual(got, []string{"anvil", "bucket"}) || next(first) != "Mg" {
		t.Fatalf("first page = %v, next %s", got, next(first))
	}
	id, err := types.ParseIdentityUUID("00000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	if first.Items[0].Id != id || first.Items[0].Sku != "anvil-sku" || !first.Items[0].InStock {
		t.Errorf("first item = %+v", first.Items[0])
	}

	inStock := true
	second, err := client.CatalogNamespace.ListItems(ctx, &namespaces.CatalogListItemsQueryParams{InStock: &inStock}, runtime.PageRequest{Cursor: *first.Next, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got := itemNames(t, second); !reflect.DeepEqual(got, []string{"crate", "drum"}) || next(second) != cursorOf(4) {
		t.Fatalf("second page = %v, next %s", got, next(second))
	}

	last, err := client.CatalogNamespace.ListItems(ctx, nil, runtime.PageRequest{Cursor: *second.Next, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if got := itemNames(t, last); !reflect.DeepEqual(got, []string{"easel"}) || last.Next != nil {
		t.Fatalf("last page = %v, next %s", got, next(last))
	}

	want := []url.Values{
		{},
		{"cursor": {"Mg"}, "limit": {"2"}, "inStock": {"true"}},
		{"cursor": {cursorOf(4)}, "limit": {"5"}},
	}
	if got := pages.requests(); !reflect.DeepEqual(got, want) {
		t.Errorf("queries = %v, want %v", got, want)
	}
}

func TestListItemsRefusesALimitOutOfBoundsBeforeTheRequest(t *testing.T) {
	client, pages := serve(t)
	for limit, validator := range map[int]string{6: "max", -1: "min"} {
		_, err := client.CatalogNamespace.ListItems(context.Background(), nil, runtime.PageRequest{Limit: limit})
		var validation *namespaces.ValidationError
		if !errors.As(err, &validation) {
			t.Fatalf("limit %d: err = %v, want a validation error", limit, err)
		}
		if fieldErrors := validation.Errors.GetFieldErrors("limit"); len(fieldErrors) != 1 || fieldErrors[0].Validator != validator {
			t.Errorf("limit %d: errors = %v, want %s", limit, validation.Errors, validator)
		}
	}
	if got := pages.requests(); len(got) != 0 {
		t.Errorf("a refused request was sent: %v", got)
	}
}

func TestListItemsAllCollectsEveryItemLazily(t *testing.T) {
	client, pages := serve(t)
	ctx := context.Background()

	var got []string
	for item, err := range client.CatalogNamespace.ListItemsAll(ctx, nil, runtime.PageRequest{Limit: 2}) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, item.Name)
	}
	if !reflect.DeepEqual(got, names) {
		t.Fatalf("items = %v, want %v", got, names)
	}
	want := []url.Values{
		{"limit": {"2"}},
		{"cursor": {"Mg"}, "limit": {"2"}},
		{"cursor": {cursorOf(4)}, "limit": {"2"}},
	}
	if requests := pages.requests(); !reflect.DeepEqual(requests, want) {
		t.Fatalf("queries = %v, want %v", requests, want)
	}

	// Nothing is asked before the loop, and a page only when the loop
	// reaches it: three items take two pages.
	client, pages = serve(t)
	items := client.CatalogNamespace.ListItemsAll(ctx, nil, runtime.PageRequest{})
	if requests := pages.requests(); len(requests) != 0 {
		t.Fatalf("ListItemsAll asked before the loop: %v", requests)
	}
	taken := 0
	for _, err := range items {
		if err != nil {
			t.Fatal(err)
		}
		if taken++; taken == 3 {
			break
		}
	}
	if requests := pages.requests(); len(requests) != 2 {
		t.Fatalf("three items asked %d pages: %v", len(requests), requests)
	}

	// The iterator starts at the cursor it is given.
	client, _ = serve(t)
	got = nil
	for item, err := range client.CatalogNamespace.ListItemsAll(ctx, nil, runtime.PageRequest{Cursor: cursorOf(3)}) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, item.Name)
	}
	if !reflect.DeepEqual(got, []string{"drum", "easel"}) {
		t.Fatalf("items after the third = %v", got)
	}
}

func TestListItemsAllEndsWithAnError(t *testing.T) {
	ctx := context.Background()

	// A page that fails ends the iteration with its error.
	client, pages := serve(t)
	pages.refuse = cursorOf(4)
	var got []string
	var failures []error
	for item, err := range client.CatalogNamespace.ListItemsAll(ctx, nil, runtime.PageRequest{}) {
		if err != nil {
			failures = append(failures, err)
			continue
		}
		got = append(got, item.Name)
	}
	var apiErr *sdk.APIError
	if !reflect.DeepEqual(got, names[:4]) || len(failures) != 1 || !errors.As(failures[0], &apiErr) || apiErr.StatusCode != http.StatusBadRequest ||
		apiErr.Message != "cursor is not one this operation returned" {
		t.Fatalf("items %v, errors %v", got, failures)
	}

	// A page whose next is the cursor it was asked with ends it rather
	// than loop.
	client, pages = serve(t)
	pages.repeat = true
	got, failures = nil, nil
	for item, err := range client.CatalogNamespace.ListItemsAll(ctx, nil, runtime.PageRequest{}) {
		if err != nil {
			failures = append(failures, err)
			continue
		}
		got = append(got, item.Name)
	}
	if !reflect.DeepEqual(got, []string{"anvil", "bucket", "crate", "drum"}) || len(failures) != 1 ||
		failures[0].Error() != "Catalog.listItems returned the cursor it was sent" {
		t.Fatalf("items %v, errors %v", got, failures)
	}
	if requests := pages.requests(); len(requests) != 2 {
		t.Fatalf("queries = %v", requests)
	}
}

func TestListTagsPagesAPrimitive(t *testing.T) {
	client, pages := serve(t)
	ctx := context.Background()
	page, err := client.CatalogNamespace.ListTags(ctx, runtime.PageRequest{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(page.Items, []string{"blue", "green"}) || page.Next == nil || *page.Next != "Mg" {
		t.Fatalf("page = %+v", page)
	}
	var all []string
	for tag, err := range client.CatalogNamespace.ListTagsAll(ctx, runtime.PageRequest{}) {
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, tag)
	}
	if !reflect.DeepEqual(all, tags) {
		t.Fatalf("tags = %v", all)
	}
	if got := pages.requests(); len(got) != 2 || len(got[1]) != 0 {
		t.Fatalf("queries = %v", got)
	}
}

func TestPageWireShape(t *testing.T) {
	var page runtime.Page[string]
	if err := json.Unmarshal([]byte(` + "`" + `{"items":[],"next":null}` + "`" + `), &page); err != nil || page.Items == nil || page.Next != nil {
		t.Fatalf("page = %+v, %v", page, err)
	}
	values := url.Values{}
	runtime.AddPageRequest(values, runtime.PageRequest{})
	if len(values) != 0 {
		t.Fatalf("the first page at the default limit sends %v", values)
	}
	runtime.AddPageRequest(values, runtime.PageRequest{Cursor: "Mg", Limit: 3})
	if got := values.Encode(); !strings.Contains(got, "cursor=Mg") || !strings.Contains(got, "limit=3") {
		t.Fatalf("query = %s", got)
	}
}

func TestListShelfItemsSendsItsArgumentsBesideThePage(t *testing.T) {
	client, pages := serve(t)
	ctx := context.Background()
	maker := "acme"
	input := namespaces.CatalogListShelfItemsInput{Maker: &maker}

	page, err := client.CatalogNamespace.ListShelfItems(ctx, shelfID, input, runtime.PageRequest{Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if got := itemNames(t, page); !reflect.DeepEqual(got, names[:3]) || next(page) != cursorOf(3) {
		t.Fatalf("page = %v, next %s", got, next(page))
	}
	var got []string
	for item, err := range client.CatalogNamespace.ListShelfItemsAll(ctx, shelfID, input, runtime.PageRequest{Cursor: *page.Next, Limit: 1}) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, item.Name)
	}
	if !reflect.DeepEqual(got, names[3:]) {
		t.Fatalf("items = %v", got)
	}
	want := []url.Values{
		{"maker": {"acme"}, "limit": {"3"}},
		{"maker": {"acme"}, "cursor": {cursorOf(3)}, "limit": {"1"}},
		{"maker": {"acme"}, "cursor": {cursorOf(4)}, "limit": {"1"}},
	}
	if requests := pages.requests(); !reflect.DeepEqual(requests, want) {
		t.Fatalf("queries = %v, want %v", requests, want)
	}
	for _, path := range pages.paths {
		if path != "/api/shelves/"+shelfID+"/items" {
			t.Fatalf("path = %s", path)
		}
	}
}
`
