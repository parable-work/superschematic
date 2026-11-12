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
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

const (
	pagedService   = "fixture-paged-api"
	pagedSDKModule = "example.com/schemas/sdk/go/fixture-paged-api"
)

// loadPagedAPI loads fixture-paged-api: Catalog.listItems pages ItemView
// two to a page, up to five, beside its ids and inStock query parameters,
// Catalog.listTags pages strings with the default limits (D60), and
// Catalog.listParts pages PartView, sorted by name or quantity and kept to
// the parts its filter finds, a field of every kind (D106). Each edit then
// changes the schema (sdktest.AddShelfItemsOperation).
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
		"ListParts":   {"runtime.Page[types.PartView]", "types.PartView", "ListPartsAll"},
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
// item lazily, stopping at a null next and ending with an error. Its
// listItems sorts (D60, amended), so the iterator is seen sending the sort
// with every page, and the SDK sends a sort as it is, for the server to
// refuse one it does not offer. Catalog.findParts is added too, a list
// with a filter alone under another name, and the server reads the parts'
// filters as a route does, with the HTTP runtime's filterparse.Parse
// (D106): the typed filter is read back for each kind and operator, beside
// a sort and with every page, a tool call's filter decodes into the query
// struct, and the writer sends the queries of
// runtime/http/testdata/filter_parity.json, which FILTER_PARITY names. A
// filter the reader would refuse or read as something else is refused
// before the request (filterparse.Check): Catalog.auditParts is added for
// a filter of more filters than a request may hold.
func TestPagedSDKBuildsAndRuns(t *testing.T) {
	schema, apiOutput := loadPagedAPI(t, sdktest.AddShelfItemsOperation, sdktest.AddFindPartsOperation, sdktest.AddAuditPartsOperation)
	t.Setenv("FILTER_PARITY", filepath.Join(testpaths.RepoRoot(t), "runtime", "http", "testdata", "filter_parity.json"))
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
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "example.com/schemas/sdk/go/fixture-paged-api"
	"example.com/schemas/sdk/go/fixture-paged-api/namespaces"
	"example.com/schemas/sdk/go/fixture-paged-api/runtime"
	types "example.com/schemas/types/go/fixture-paged-api"
	"github.com/parable-work/superschematic/runtime/http/go/filterparse"
)

var names = []string{"anvil", "bucket", "crate", "drum", "easel"}

// The parts /api/parts and /api/parts/all list, and the filters the routes
// of Catalog.listParts and Catalog.findParts read (D106): the fields, each
// with the operators of its kind.
var partNames = []string{"axle", "bracket", "clamp", "dowel", "eyelet"}

var (
	equalsOps  = []filterparse.Operator{filterparse.OperatorEq, filterparse.OperatorNe, filterparse.OperatorIn, filterparse.OperatorNotIn}
	orderedOps = append(slices.Clone(equalsOps), filterparse.OperatorGT, filterparse.OperatorGTE, filterparse.OperatorLT, filterparse.OperatorLTE)
	textOps    = append(slices.Clone(equalsOps), filterparse.OperatorContains)
)

func nullable(operators []filterparse.Operator) []filterparse.Operator {
	return append(slices.Clone(operators), filterparse.OperatorIsNull)
}

var partsSpec = filterparse.Spec{Fields: []filterparse.FieldSpec{
	{Name: "id", Operators: equalsOps},
	{Name: "name", Operators: textOps},
	{Name: "kind", Operators: equalsOps},
	{Name: "quantity", Operators: orderedOps},
	{Name: "weight", Operators: orderedOps},
	{Name: "price", Operators: orderedOps},
	{Name: "discontinued", Operators: equalsOps},
	{Name: "madeAt", Operators: orderedOps},
	{Name: "retiredOn", Operators: nullable(orderedOps)},
	{Name: "supplierId", Operators: nullable(equalsOps)},
	// Catalog.auditParts filters by three fields more.
	{Name: "nameB", Operators: textOps},
	{Name: "quantityB", Operators: orderedOps},
	{Name: "weightB", Operators: orderedOps},
}}

var tags = []string{"blue", "green", "red"}

const shelfID = "5d0c7e2a-1b3f-4a6d-8c9e-0f1a2b3c4d5e"

// server pages names (as items) and tags. repeat makes a page's next the
// cursor it was asked with; refuse is a cursor it answers with the 400 of
// a cursor no page returned. /api/items reads its sort: by name or sku,
// which agree, ascending, or descending after a -. filters are the filters
// each request for the parts was read to hold.
type server struct {
	mu      sync.Mutex
	paths   []string
	queries []url.Values
	filters []filterparse.Request
	repeat  bool
	refuse  string
}

// serveParts answers the parts: /api/parts a page of them, two to a page
// unless limit says, and /api/parts/all and /api/parts/audit every one. It reads the request's
// filters as a route does, with filterparse.Parse under partsSpec, records
// them, and answers the reader's refusal; it keeps every part.
func (s *server) serveParts(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	read, refusal := filterparse.Parse(query, partsSpec)
	if refusal != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": 400, "code": "bad_request", "detail": refusal.Message})
		return
	}
	s.mu.Lock()
	s.filters = append(s.filters, read)
	s.mu.Unlock()

	parts := make([]any, len(partNames))
	for i, name := range partNames {
		parts[i] = map[string]any{
			"id":           fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1),
			"name":         name,
			"kind":         "bolt",
			"quantity":     i + 1,
			"weight":       float64(i) + 0.5,
			"price":        fmt.Sprintf("%d.50", i+1),
			"discontinued": i%2 == 1,
			"madeAt":       fmt.Sprintf("2026-0%d-01T00:00:00Z", i+1),
			"retiredOn":    nil,
			"supplierId":   nil,
		}
	}
	var data any = parts
	if r.URL.Path == "/api/parts" {
		after, limit := 0, 2
		if cursor := query.Get("cursor"); cursor != "" {
			raw, _ := base64.RawURLEncoding.DecodeString(cursor)
			_ = json.Unmarshal(raw, &after)
		}
		if text := query.Get("limit"); text != "" {
			limit, _ = strconv.Atoi(text)
		}
		last := min(after+limit, len(parts))
		var next any
		if last < len(parts) {
			next = cursorOf(last)
		}
		data = map[string]any{"items": parts[after:last], "next": next}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "meta": map[string]any{"requestId": "req-1"}})
}

func (s *server) read() []filterparse.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]filterparse.Request(nil), s.filters...)
}

func ptr[T any](value T) *T {
	return &value
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
	if r.URL.Path == "/api/parts" || r.URL.Path == "/api/parts/all" || r.URL.Path == "/api/parts/audit" {
		s.serveParts(w, r)
		return
	}

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
	order := func(position int) int { return position }
	if sort := query.Get("sort"); r.URL.Path == "/api/items" && sort != "" {
		switch sort {
		case "name", "sku":
		case "-name", "-sku":
			order = func(position int) int { return count + 1 - position }
		default:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("{\"status\":400,\"code\":\"bad_request\",\"detail\":\"Invalid query parameter sort: expected one of name, -name, sku, -sku\"}"))
			return
		}
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
		n := order(position)
		items = append(items, map[string]any{
			"id":      fmt.Sprintf("00000000-0000-4000-8000-%012d", n),
			"name":    names[n-1],
			"sku":     names[n-1] + "-sku",
			"inStock": n%2 == 1,
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

func TestListItemsAllKeepsTheSort(t *testing.T) {
	client, pages := serve(t)
	ctx := context.Background()
	sort := "-name"
	var got []string
	for item, err := range client.CatalogNamespace.ListItemsAll(ctx, &namespaces.CatalogListItemsQueryParams{Sort: &sort}, runtime.PageRequest{Limit: 2}) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, item.Name)
	}
	if want := []string{"easel", "drum", "crate", "bucket", "anvil"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("items = %v, want %v", got, want)
	}
	want := []url.Values{
		{"sort": {"-name"}, "limit": {"2"}},
		{"sort": {"-name"}, "cursor": {"Mg"}, "limit": {"2"}},
		{"sort": {"-name"}, "cursor": {cursorOf(4)}, "limit": {"2"}},
	}
	if requests := pages.requests(); !reflect.DeepEqual(requests, want) {
		t.Fatalf("queries = %v, want %v", requests, want)
	}

	// The SDK sends a sort as it is; the server refuses one it does not
	// offer.
	client, pages = serve(t)
	unknown := "price"
	_, err := client.CatalogNamespace.ListItems(ctx, &namespaces.CatalogListItemsQueryParams{Sort: &unknown}, runtime.PageRequest{})
	var apiErr *sdk.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest ||
		apiErr.Message != "Invalid query parameter sort: expected one of name, -name, sku, -sku" {
		t.Fatalf("err = %v", err)
	}
	if requests := pages.requests(); !reflect.DeepEqual(requests, []url.Values{{"sort": {"price"}}}) {
		t.Fatalf("queries = %v", requests)
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

const partID = "00000000-0000-4000-8000-000000000001"

const supplierID = "7c1d2e3f-4a5b-4c6d-8e9f-0a1b2c3d4e5f"

// A filter's values are of the types package's types where a query list's
// items are (D106): a UUID, an enum, a decimal, a date and a time. The
// query carries each as its JSON text, a UUID in base62.
var (
	partUUID     = mustUUID(partID)
	supplierUUID = mustUUID(supplierID)
)

func mustUUID(text string) types.IdentityUUID {
	id, err := types.ParseIdentityUUID(text)
	if err != nil {
		panic(err)
	}
	return id
}

func at(year int, month time.Month, day int) types.TemporalDateTime {
	return types.TemporalDateTime(time.Date(year, month, day, 0, 0, 0, 0, time.UTC))
}

// everyFilter is a filter of every field of the parts and every operator
// a field's kind takes.
func everyFilter() *namespaces.CatalogListPartsFilter {
	return &namespaces.CatalogListPartsFilter{
		Id:           &filterparse.Equals[types.IdentityUUID]{Eq: ptr(partUUID), In: []types.IdentityUUID{partUUID, supplierUUID}},
		Name:         &filterparse.Text[string]{Eq: ptr("axle"), In: []string{"axle", "bracket"}, Contains: ptr("X l")},
		Kind:         &filterparse.Equals[types.PartKind]{In: []types.PartKind{types.PartKind_Bolt, types.PartKind_Nut}, Ne: ptr(types.PartKind_Washer)},
		Quantity:     &filterparse.Ordered[int64]{Eq: ptr(int64(1)), Ne: ptr(int64(7)), In: []int64{1, 2}, NotIn: []int64{5, 6}, Gt: ptr(int64(0)), Gte: ptr(int64(1)), Lt: ptr(int64(9)), Lte: ptr(int64(8))},
		Weight:       &filterparse.Ordered[float64]{Lt: ptr(2.5), Gte: ptr(0.5)},
		Price:        &filterparse.Ordered[types.GenericDecimal]{Gte: ptr(types.GenericDecimal("1.00")), Lte: ptr(types.GenericDecimal("9.99"))},
		Discontinued: &filterparse.Equals[bool]{Eq: ptr(false), In: []bool{true, false}},
		MadeAt:       &filterparse.Ordered[types.TemporalDateTime]{Gt: ptr(at(2025, 12, 31))},
		RetiredOn:    &filterparse.Ordered[types.TemporalDate]{Lte: ptr(types.TemporalDate("2026-06-01")), IsNull: ptr(false)},
		SupplierId:   &filterparse.Equals[types.IdentityUUID]{NotIn: []types.IdentityUUID{supplierUUID}, IsNull: ptr(true)},
	}
}

func TestListPartsWritesTheFilterForEachKindAndOperator(t *testing.T) {
	client, pages := serve(t)
	filter := everyFilter()
	page, err := client.CatalogNamespace.ListParts(context.Background(), &namespaces.CatalogListPartsQueryParams{Filter: filter}, runtime.PageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[0].Name != "axle" || page.Items[0].Quantity != 1 || page.Next == nil {
		t.Fatalf("page = %+v", page)
	}
	want := url.Values{
		"filter[id]":                 {partUUID.String()},
		"filter[id][in]":             {partUUID.String() + "," + supplierUUID.String()},
		"filter[name]":               {"axle"},
		"filter[name][in]":           {"axle,bracket"},
		"filter[name][contains]":     {"X l"},
		"filter[kind][ne]":           {"washer"},
		"filter[kind][in]":           {"bolt,nut"},
		"filter[quantity]":           {"1"},
		"filter[quantity][ne]":       {"7"},
		"filter[quantity][in]":       {"1,2"},
		"filter[quantity][notIn]":    {"5,6"},
		"filter[quantity][gt]":       {"0"},
		"filter[quantity][gte]":      {"1"},
		"filter[quantity][lt]":       {"9"},
		"filter[quantity][lte]":      {"8"},
		"filter[weight][gte]":        {"0.5"},
		"filter[weight][lt]":         {"2.5"},
		"filter[price][gte]":         {"1.00"},
		"filter[price][lte]":         {"9.99"},
		"filter[discontinued]":       {"false"},
		"filter[discontinued][in]":   {"true,false"},
		"filter[madeAt][gt]":         {"2025-12-31T00:00:00Z"},
		"filter[retiredOn][lte]":     {"2026-06-01"},
		"filter[retiredOn][isNull]":  {"false"},
		"filter[supplierId][notIn]":  {supplierUUID.String()},
		"filter[supplierId][isNull]": {"true"},
	}
	if got := pages.requests(); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Errorf("query = %v, want %v", got, want)
	}
	// The route's reader reads back the filters the members set, in the
	// spec's field order and each field's operators in theirs.
	if got := pages.read(); len(got) != 1 || !reflect.DeepEqual([]filterparse.Filter(got[0]), filter.Filters()) || len(got[0]) != len(want) {
		t.Errorf("the route read %v, want %v", got, filter.Filters())
	}
}

func TestListPartsSendsNoFilterItsMembersDoNotSet(t *testing.T) {
	client, pages := serve(t)
	ctx := context.Background()
	for _, query := range []*namespaces.CatalogListPartsQueryParams{
		nil,
		{},
		{Filter: &namespaces.CatalogListPartsFilter{}},
		{Filter: &namespaces.CatalogListPartsFilter{Name: &filterparse.Text[string]{}, Kind: &filterparse.Equals[types.PartKind]{In: []types.PartKind{}}}},
	} {
		if _, err := client.CatalogNamespace.ListParts(ctx, query, runtime.PageRequest{}); err != nil {
			t.Fatal(err)
		}
	}
	for i, query := range pages.requests() {
		if len(query) != 0 {
			t.Errorf("request %d sent %v", i, query)
		}
	}
}

func TestListPartsAllKeepsTheSortAndTheFilter(t *testing.T) {
	client, pages := serve(t)
	ctx := context.Background()
	sort := "-quantity"
	query := &namespaces.CatalogListPartsQueryParams{
		Sort: &sort,
		Filter: &namespaces.CatalogListPartsFilter{
			Quantity:     &filterparse.Ordered[int64]{Gte: ptr(int64(1))},
			Discontinued: &filterparse.Equals[bool]{In: []bool{true, false}},
			RetiredOn:    &filterparse.Ordered[types.TemporalDate]{IsNull: ptr(true)},
		},
	}
	var got []string
	for part, err := range client.CatalogNamespace.ListPartsAll(ctx, query, runtime.PageRequest{Limit: 2}) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, part.Name)
	}
	if !reflect.DeepEqual(got, partNames) {
		t.Fatalf("parts = %v, want %v", got, partNames)
	}
	page := func(cursor string) url.Values {
		values := url.Values{
			"sort":                      {"-quantity"},
			"limit":                     {"2"},
			"filter[quantity][gte]":     {"1"},
			"filter[discontinued][in]":  {"true,false"},
			"filter[retiredOn][isNull]": {"true"},
		}
		if cursor != "" {
			values.Set("cursor", cursor)
		}
		return values
	}
	want := []url.Values{page(""), page("Mg"), page(cursorOf(4))}
	if requests := pages.requests(); !reflect.DeepEqual(requests, want) {
		t.Fatalf("queries = %v, want %v", requests, want)
	}
	for i, read := range pages.read() {
		if !reflect.DeepEqual([]filterparse.Filter(read), query.Filter.Filters()) {
			t.Errorf("page %d was read to hold %v", i, read)
		}
	}
}

func TestFindPartsSendsAFilterAloneUnderAnotherName(t *testing.T) {
	client, pages := serve(t)
	ctx := context.Background()
	parts, err := client.CatalogNamespace.FindParts(ctx, &namespaces.CatalogFindPartsQueryParams{
		Where: &namespaces.CatalogFindPartsFilter{
			Name:       &filterparse.Text[string]{Contains: ptr("a&b=c d")},
			SupplierId: &filterparse.Equals[types.IdentityUUID]{Eq: ptr(supplierUUID)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != len(partNames) || parts[4].Name != "eyelet" {
		t.Fatalf("parts = %+v", parts)
	}
	if _, err := client.CatalogNamespace.FindParts(ctx, nil); err != nil {
		t.Fatal(err)
	}
	want := []url.Values{
		{"filter[name][contains]": {"a&b=c d"}, "filter[supplierId]": {supplierUUID.String()}},
		{},
	}
	if requests := pages.requests(); !reflect.DeepEqual(requests, want) {
		t.Fatalf("queries = %v, want %v", requests, want)
	}
	for _, path := range pages.paths {
		if path != "/api/parts/all" {
			t.Fatalf("path = %s", path)
		}
	}
}

func TestTheRouteRefusesAFilterItsFieldDoesNotTake(t *testing.T) {
	client, _ := serve(t)
	// Equals holds IsNull for a field that may be null; id may not.
	_, err := client.CatalogNamespace.ListParts(context.Background(), &namespaces.CatalogListPartsQueryParams{
		Filter: &namespaces.CatalogListPartsFilter{Id: &filterparse.Equals[types.IdentityUUID]{IsNull: ptr(true)}},
	}, runtime.PageRequest{})
	var apiErr *sdk.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest || !strings.HasPrefix(apiErr.Message, "Invalid query parameter filter[id]: ") {
		t.Fatalf("err = %v", err)
	}
}

// refusal is the one field error of the validation error err is, or a
// failure when err is none.
func refusal(t *testing.T, err error) (string, string, string) {
	t.Helper()
	var validation *namespaces.ValidationError
	if !errors.As(err, &validation) || len(validation.Errors) != 1 {
		t.Fatalf("err = %v, want a validation error of one field", err)
	}
	for path := range validation.Errors {
		fieldErrors := validation.Errors.GetFieldErrors(path)
		if len(fieldErrors) != 1 {
			t.Fatalf("errors at %s = %v", path, fieldErrors)
		}
		return path, fieldErrors[0].Validator, fieldErrors[0].Message
	}
	return "", "", ""
}

func TestAFilterTheRouteWouldNotReadBackIsRefusedBeforeTheRequest(t *testing.T) {
	client, pages := serve(t)
	ctx := context.Background()
	many := func(n int) []int64 {
		values := make([]int64, n)
		for i := range values {
			values[i] = int64(i)
		}
		return values
	}
	const noComma = "must not contain a comma or surrounding space"
	for _, test := range []struct {
		name                     string
		filter                   namespaces.CatalogListPartsFilter
		path, validator, message string
	}{
		{"an in's value with a comma", namespaces.CatalogListPartsFilter{Name: &filterparse.Text[string]{In: []string{"axle", "a,b"}}},
			"filter[name][in][1]", "pattern", noComma},
		{"an in's value with a leading space", namespaces.CatalogListPartsFilter{Kind: &filterparse.Equals[types.PartKind]{In: []types.PartKind{" bolt"}}},
			"filter[kind][in][0]", "pattern", noComma},
		{"an in's value with a trailing space", namespaces.CatalogListPartsFilter{Price: &filterparse.Ordered[types.GenericDecimal]{In: []types.GenericDecimal{"1.50", "2.50", "3.50\t"}}},
			"filter[price][in][2]", "pattern", noComma},
		{"an in's empty value", namespaces.CatalogListPartsFilter{Kind: &filterparse.Equals[types.PartKind]{In: []types.PartKind{types.PartKind_Bolt, ""}}},
			"filter[kind][in][1]", "required", "is required"},
		{"an in's blank value", namespaces.CatalogListPartsFilter{RetiredOn: &filterparse.Ordered[types.TemporalDate]{In: []types.TemporalDate{"  "}}},
			"filter[retiredOn][in][0]", "required", "is required"},
		{"an in of more values than it may list", namespaces.CatalogListPartsFilter{Quantity: &filterparse.Ordered[int64]{In: many(101)}},
			"filter[quantity][in]", "max", "must hold at most 100 values"},
		{"a notIn's value with a comma", namespaces.CatalogListPartsFilter{Name: &filterparse.Text[string]{NotIn: []string{"axle", "a,b"}}},
			"filter[name][notIn][1]", "pattern", noComma},
		{"a notIn's blank value", namespaces.CatalogListPartsFilter{Kind: &filterparse.Equals[types.PartKind]{NotIn: []types.PartKind{" "}}},
			"filter[kind][notIn][0]", "required", "is required"},
		{"a notIn of more values than it may list", namespaces.CatalogListPartsFilter{Quantity: &filterparse.Ordered[int64]{NotIn: many(101)}},
			"filter[quantity][notIn]", "max", "must hold at most 100 values"},
		{"a contains of no text", namespaces.CatalogListPartsFilter{Name: &filterparse.Text[string]{Eq: ptr("axle"), Contains: ptr("")}},
			"filter[name][contains]", "minLength", "needs text"},
	} {
		t.Run(test.name, func(t *testing.T) {
			query := &namespaces.CatalogListPartsQueryParams{Filter: &test.filter}
			_, err := client.CatalogNamespace.ListParts(ctx, query, runtime.PageRequest{})
			if path, validator, message := refusal(t, err); path != test.path || validator != test.validator || message != test.message {
				t.Errorf("refused at %s for %s: %s; want %s, %s: %s", path, validator, message, test.path, test.validator, test.message)
			}
			// The iterator ends with the same error, and a filter under
			// another argument's name is named filter too.
			var failures []error
			for _, err := range client.CatalogNamespace.ListPartsAll(ctx, query, runtime.PageRequest{}) {
				failures = append(failures, err)
			}
			if len(failures) != 1 {
				t.Fatalf("the iterator yielded %v", failures)
			}
			if path, _, _ := refusal(t, failures[0]); path != test.path {
				t.Errorf("the iterator refused at %s, want %s", path, test.path)
			}
			where := namespaces.CatalogFindPartsFilter(test.filter)
			_, err = client.CatalogNamespace.FindParts(ctx, &namespaces.CatalogFindPartsQueryParams{Where: &where})
			if path, _, _ := refusal(t, err); path != test.path {
				t.Errorf("findParts refused at %s, want %s", path, test.path)
			}
		})
	}
	if got := pages.requests(); len(got) != 0 {
		t.Fatalf("a refused request was sent: %v", got)
	}

	// The most values an in may list are sent, a comma or a space in any
	// other operator's value travels as it is, and an in of no value is
	// left out.
	sent := []*namespaces.CatalogListPartsFilter{
		{Quantity: &filterparse.Ordered[int64]{In: many(100)}},
		{Name: &filterparse.Text[string]{Eq: ptr(" a, b "), Contains: ptr(" , "), In: []string{}}},
	}
	for _, filter := range sent {
		if _, err := client.CatalogNamespace.ListParts(ctx, &namespaces.CatalogListPartsQueryParams{Filter: filter}, runtime.PageRequest{}); err != nil {
			t.Fatal(err)
		}
	}
	read := pages.read()
	if len(read) != 2 || len(read[0]) != 1 || len(read[0][0].Values) != 100 ||
		!reflect.DeepEqual([]filterparse.Filter(read[1]), sent[1].Filters()) || len(read[1]) != 2 {
		t.Errorf("the route read %v", read)
	}
}

func TestAFilterOfMoreFiltersThanARequestHoldsIsRefused(t *testing.T) {
	client, pages := serve(t)
	ctx := context.Background()
	ordered := func() *filterparse.Ordered[int64] {
		return &filterparse.Ordered[int64]{Eq: ptr(int64(1)), In: []int64{1}, Gt: ptr(int64(0))}
	}
	// The operators set here of listParts's fields are 46 filters, a ne and
	// a notIn among them, quantityB's three make 49, and weightB's one 50,
	// the most a request holds.
	every := everyFilter()
	every.Kind.Eq = ptr(types.PartKind_Bolt)
	every.Weight = &filterparse.Ordered[float64]{Eq: ptr(0.5), In: []float64{0.5}, Gt: ptr(0.0), Gte: ptr(0.5), Lt: ptr(9.0), Lte: ptr(8.0)}
	decimal := func(text string) *types.GenericDecimal { return ptr(types.GenericDecimal(text)) }
	date := func(text string) *types.TemporalDate { return ptr(types.TemporalDate(text)) }
	every.Price = &filterparse.Ordered[types.GenericDecimal]{Eq: decimal("1.50"), In: []types.GenericDecimal{"1.50"}, Gt: decimal("1.00"), Gte: decimal("1.50"), Lt: decimal("9.00"), Lte: decimal("8.00")}
	every.MadeAt = &filterparse.Ordered[types.TemporalDateTime]{Eq: ptr(at(2026, 1, 1)), In: []types.TemporalDateTime{at(2026, 1, 1)}, Gt: ptr(at(2025, 1, 1)), Gte: ptr(at(2025, 1, 1)), Lt: ptr(at(2027, 1, 1)), Lte: ptr(at(2027, 1, 1))}
	every.RetiredOn = &filterparse.Ordered[types.TemporalDate]{Eq: date("2026-06-01"), In: []types.TemporalDate{"2026-06-01"}, Gt: date("2026-01-01"), Gte: date("2026-01-01"), Lt: date("2027-01-01"), Lte: date("2027-01-01"), IsNull: ptr(false)}
	every.SupplierId = &filterparse.Equals[types.IdentityUUID]{Eq: ptr(supplierUUID), In: []types.IdentityUUID{supplierUUID}, IsNull: ptr(false)}
	filter := &namespaces.CatalogAuditPartsFilter{
		Id: every.Id, Name: every.Name, Kind: every.Kind, Quantity: every.Quantity, Weight: every.Weight, Price: every.Price,
		Discontinued: every.Discontinued, MadeAt: every.MadeAt, RetiredOn: every.RetiredOn, SupplierId: every.SupplierId,
		QuantityB: ordered(),
		WeightB:   &filterparse.Ordered[float64]{Eq: ptr(0.5)},
	}
	if got := len(filter.Filters()); got != 50 {
		t.Fatalf("the filter holds %d filters, want 50", got)
	}
	if _, err := client.CatalogNamespace.AuditParts(ctx, &namespaces.CatalogAuditPartsQueryParams{Filter: filter}); err != nil {
		t.Fatalf("a filter of 50 filters: %v", err)
	}
	if read := pages.read(); len(read) != 1 || len(read[0]) != 50 {
		t.Fatalf("the route read %v", read)
	}

	filter.WeightB.Gt = ptr(0.0)
	_, err := client.CatalogNamespace.AuditParts(ctx, &namespaces.CatalogAuditPartsQueryParams{Filter: filter})
	if path, validator, message := refusal(t, err); path != "filter" || validator != "max" || message != "must hold at most 50 filters" {
		t.Errorf("refused at %s for %s: %s", path, validator, message)
	}
	if got := pages.requests(); len(got) != 1 {
		t.Fatalf("a refused request was sent: %v", got)
	}
}

func TestAToolCallsFilterDecodesIntoTheQuery(t *testing.T) {
	const arguments = "{\"sort\":\"-quantity\",\"filter\":{\"id\":\"" + partID + "\",\"kind\":{\"in\":[\"bolt\",\"nut\"]}," +
		"\"quantity\":{\"gt\":1,\"lte\":4},\"weight\":2.5,\"discontinued\":false,\"name\":{\"contains\":\"x\"}," +
		"\"retiredOn\":{\"isNull\":true},\"supplierId\":null}}"
	var query namespaces.CatalogListPartsQueryParams
	if err := json.Unmarshal([]byte(arguments), &query); err != nil {
		t.Fatal(err)
	}
	want := namespaces.CatalogListPartsFilter{
		Id:           &filterparse.Equals[types.IdentityUUID]{Eq: ptr(partUUID)},
		Name:         &filterparse.Text[string]{Contains: ptr("x")},
		Kind:         &filterparse.Equals[types.PartKind]{In: []types.PartKind{types.PartKind_Bolt, types.PartKind_Nut}},
		Quantity:     &filterparse.Ordered[int64]{Gt: ptr(int64(1)), Lte: ptr(int64(4))},
		Weight:       &filterparse.Ordered[float64]{Eq: ptr(2.5)},
		Discontinued: &filterparse.Equals[bool]{Eq: ptr(false)},
		RetiredOn:    &filterparse.Ordered[types.TemporalDate]{IsNull: ptr(true)},
	}
	if query.Sort == nil || *query.Sort != "-quantity" || query.Filter == nil || !reflect.DeepEqual(*query.Filter, want) {
		t.Fatalf("query = %+v, filter %+v", query, query.Filter)
	}

	client, pages := serve(t)
	if _, err := client.CatalogNamespace.ListParts(context.Background(), &query, runtime.PageRequest{Limit: 1}); err != nil {
		t.Fatal(err)
	}
	sent := url.Values{
		"sort":                      {"-quantity"},
		"limit":                     {"1"},
		"filter[id]":                {partUUID.String()},
		"filter[name][contains]":    {"x"},
		"filter[kind][in]":          {"bolt,nut"},
		"filter[quantity][gt]":      {"1"},
		"filter[quantity][lte]":     {"4"},
		"filter[weight]":            {"2.5"},
		"filter[discontinued]":      {"false"},
		"filter[retiredOn][isNull]": {"true"},
	}
	if got := pages.requests(); len(got) != 1 || !reflect.DeepEqual(got[0], sent) {
		t.Errorf("query = %v, want %v", got, sent)
	}

	// A filter is absent when the call leaves it out or sends null.
	for _, absent := range []string{"{}", "{\"filter\":null}"} {
		var none namespaces.CatalogListPartsQueryParams
		if err := json.Unmarshal([]byte(absent), &none); err != nil || none.Filter != nil {
			t.Errorf("%s decodes to %+v, %v", absent, none.Filter, err)
		}
	}
	// A field the filter does not hold, an operator its field does not
	// take and a value of another type are refused, by the member's name.
	for arguments, want := range map[string]string{
		"{\"filter\":{\"sku\":\"axle-sku\"}}":              "filter.sku: the filter has no such field",
		"{\"filter\":{\"quantity\":{\"contains\":\"1\"}}}": "filter.quantity: ",
		"{\"filter\":{\"quantity\":\"1\"}}":                "filter.quantity: ",
		"{\"filter\":{\"kind\":{\"in\":\"bolt\"}}}":        "filter.kind: ",
		"{\"filter\":[\"bolt\"]}":                          "",
	} {
		var refused namespaces.CatalogListPartsQueryParams
		if err := json.Unmarshal([]byte(arguments), &refused); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s decodes with %v, want an error holding %q", arguments, err, want)
		}
	}
	var where namespaces.CatalogFindPartsQueryParams
	if err := json.Unmarshal([]byte("{\"where\":{\"sku\":1}}"), &where); err == nil || !strings.Contains(err.Error(), "where.sku: ") {
		t.Errorf("findParts' filter decodes with %v", err)
	}
}

// parityFields are the fields of filter_parity.json's queries, each under
// the field of Catalog.auditParts's filter whose filter has its shape and
// whose values are of its type: a state's, any text, under nameB.
var parityFields = map[string]string{
	"stateId": "nameB", "priority": "quantity", "title": "name", "dueOn": "retiredOn", "assigneeId": "supplierId",
}

func TestTheFilterWriterSendsTheParityQueries(t *testing.T) {
	data, err := os.ReadFile(os.Getenv("FILTER_PARITY"))
	if err != nil {
		t.Fatalf("read the vectors FILTER_PARITY names: %v", err)
	}
	var file struct {
		Queries []struct {
			Name    string
			Filters []struct {
				Field, Operator string
				Values          []string
			}
			Pairs [][2]string
		}
	}
	if err := json.Unmarshal(data, &file); err != nil || len(file.Queries) == 0 {
		t.Fatalf("vectors: %d queries, %v", len(file.Queries), err)
	}
	for _, vector := range file.Queries {
		t.Run(vector.Name, func(t *testing.T) {
			// The vector's filters as a tool call's filter: a member per
			// field, holding each of its operators.
			members := map[string]map[string]any{}
			for _, filter := range vector.Filters {
				field, ok := parityFields[filter.Field]
				if !ok {
					t.Fatalf("no field of the parts stands in for %s", filter.Field)
				}
				value := func(text string) any {
					if field != "quantity" {
						return text
					}
					number, err := strconv.ParseInt(text, 10, 64)
					if err != nil {
						t.Fatal(err)
					}
					return number
				}
				if members[field] == nil {
					members[field] = map[string]any{}
				}
				switch filter.Operator {
				case "in", "notIn":
					values := make([]any, len(filter.Values))
					for i, text := range filter.Values {
						values[i] = value(text)
					}
					members[field][filter.Operator] = values
				case "isNull":
					members[field]["isNull"] = filter.Values[0] == "true"
				case "contains":
					members[field]["contains"] = filter.Values[0]
				default:
					members[field][filter.Operator] = value(filter.Values[0])
				}
			}
			encoded, err := json.Marshal(map[string]any{"filter": members})
			if err != nil {
				t.Fatal(err)
			}
			var query namespaces.CatalogAuditPartsQueryParams
			if err := json.Unmarshal(encoded, &query); err != nil {
				t.Fatal(err)
			}
			want := url.Values{}
			for _, pair := range vector.Pairs {
				name := pair[0]
				for from, to := range parityFields {
					name = strings.Replace(name, "["+from+"]", "["+to+"]", 1)
				}
				want[name] = []string{pair[1]}
			}
			if len(want) != len(vector.Pairs) {
				t.Fatalf("the vector's pairs repeat a name: %v", vector.Pairs)
			}

			// The typed filter writes the vector's pairs, and a request
			// carries them.
			if got := filterparse.Query(query.Filter.Filters()); !reflect.DeepEqual(got, want) {
				t.Errorf("the filter writes %v, want %v", got, want)
			}
			client, pages := serve(t)
			if _, err := client.CatalogNamespace.AuditParts(context.Background(), &query); err != nil {
				t.Fatal(err)
			}
			if got := pages.requests(); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
				t.Errorf("the request sends %v, want %v", got, want)
			}
		})
	}
}
`

// TestPagedUnionSDKBuildsAndRuns runs pagedUnionSDKTest in the generated
// SDK, with Catalog.listEntries added, a page of the union Entry: the
// method decodes each item through types.EntryWrapper, since a union is an
// interface that encoding/json cannot fill, and returns the page of the
// members the wrappers hold, which ListEntriesAll yields in turn.
func TestPagedUnionSDKBuildsAndRuns(t *testing.T) {
	schema, apiOutput := loadPagedAPI(t, sdktest.AddEntriesOperation)
	sdkOutput := runInSDK(t, schema, apiOutput, pagedService, "paged_union_test.go", pagedUnionSDKTest)
	for _, ns := range sdkOutput.Namespaces {
		for _, ep := range ns.Endpoints {
			if ep.MethodName == "ListEntries" && (ep.OutputGoType != "runtime.Page[types.Entry]" || ep.ItemGoType != "types.Entry") {
				t.Errorf("ListEntries = (%s, %s)", ep.OutputGoType, ep.ItemGoType)
			}
		}
	}
}

// pagedUnionSDKTest runs in the generated SDK module against a server that
// answers two pages of entries, items and baskets mixed, then an item the
// union cannot read, a string.
const pagedUnionSDKTest = `package sdk_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "example.com/schemas/sdk/go/fixture-paged-api"
	"example.com/schemas/sdk/go/fixture-paged-api/runtime"
	types "example.com/schemas/types/go/fixture-paged-api"
)

const item = ` + "`" + `{"id":"00000000-0000-4000-8000-000000000001","name":"anvil","sku":"anvil-sku","inStock":true}` + "`" + `

const basket = ` + "`" + `{"id":"00000000-0000-4000-8000-000000000002","lines":[{"itemId":"00000000-0000-4000-8000-000000000001","quantity":2}]}` + "`" + `

func serveEntries(t *testing.T) *sdk.FixturePagedApiSDK {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var data string
		switch r.URL.Query().Get("cursor") {
		case "":
			data = ` + "`" + `{"items":[` + "`" + ` + item + "," + basket + ` + "`" + `],"next":"Mg"}` + "`" + `
		case "Mg":
			data = ` + "`" + `{"items":[` + "`" + ` + basket + ` + "`" + `],"next":null}` + "`" + `
		default:
			data = ` + "`" + `{"items":["circle"],"next":null}` + "`" + `
		}
		_, _ = w.Write([]byte(` + "`" + `{"data":` + "`" + ` + data + ` + "`" + `,"meta":{"requestId":"req-1"}}` + "`" + `))
	}))
	t.Cleanup(server.Close)
	client, err := sdk.New(sdk.SDKConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func describe(entry types.Entry) string {
	switch member := entry.(type) {
	case types.ItemView:
		return "item " + member.Name
	case types.BasketView:
		return fmt.Sprintf("basket of %v", member.Lines[0].Quantity)
	}
	return "unknown"
}

func TestListEntriesDecodesEachItemThroughTheUnion(t *testing.T) {
	client := serveEntries(t)
	ctx := context.Background()

	page, err := client.CatalogNamespace.ListEntries(ctx, runtime.PageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || describe(page.Items[0]) != "item anvil" || describe(page.Items[1]) != "basket of 2" || page.Next == nil || *page.Next != "Mg" {
		t.Fatalf("page = %+v", page)
	}

	var got []string
	for entry, err := range client.CatalogNamespace.ListEntriesAll(ctx, runtime.PageRequest{}) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, describe(entry))
	}
	if strings.Join(got, "; ") != "item anvil; basket of 2; basket of 2" {
		t.Fatalf("entries = %v", got)
	}

	if _, err := client.CatalogNamespace.ListEntries(ctx, runtime.PageRequest{Cursor: "Mw"}); err == nil || !strings.Contains(err.Error(), "Entry") {
		t.Fatalf("an item the union cannot read = %v", err)
	}
}
`
