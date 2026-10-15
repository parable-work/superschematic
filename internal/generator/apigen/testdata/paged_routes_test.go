// This file runs inside the generated API module of fixture-paged-api
// (TestPagedRoutesServePages copies it there, with the HTTP runtimes'
// page vectors as testdata/page_parity.json, the scaffolds as scaffolds/
// and the implementation scaffold as impl/). It registers the generated
// routes with an in-memory catalog and pages its lists over httptest
// (D60): the first page, the next by its cursor, the last with a null
// next; filters applied to each page; the limit and its bounds; and every
// refusal the vectors hold, worded as they word it.
package fixturepagedapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"slices"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/parable-work/superschematic/runtime/http/go/paging"

	api "example.com/schemas/api/fixture-paged-api"
	scaffold "example.com/schemas/api/fixture-paged-api/impl"
	"example.com/schemas/api/fixture-paged-api/scaffolds/catalog"
	types "example.com/schemas/types/go/fixture-paged-api"
)

// The per-endpoint scaffolds implement the interface they were written
// for, paged methods included.
var _ api.CatalogImplementation = (*catalog.Implementation)(nil)

// itemKey is the position of an item in the catalog's order: its id.
type itemKey struct {
	ID types.IdentityUUID `json:"id"`
}

// shelf is an in-memory catalog: its items in their order, its tags in
// theirs, and every page request the routes passed it.
type shelf struct {
	items []types.ItemView
	tags  []string
	pages []paging.Request
}

func (s *shelf) PlaceBasket(context.Context, *types.BasketInput) (*types.BasketView, error) {
	return nil, api.NotImplementedError("Catalog.PlaceBasket")
}

func (s *shelf) GetItem(_ context.Context, id types.IdentityUUID) (*types.ItemView, error) {
	for i := range s.items {
		if s.items[i].Id == id {
			return &s.items[i], nil
		}
	}
	return nil, api.NotFoundError("item", nil)
}

// ListItems reads, as a query would, up to one item past the limit after
// the cursor's, under the filters, and builds the page from them.
func (s *shelf) ListItems(_ context.Context, ids []types.IdentityUUID, inStock *bool, page paging.Request) (paging.Page[types.ItemView], error) {
	s.pages = append(s.pages, page)
	start := 0
	if page.Cursor != "" {
		var key itemKey
		if err := paging.DecodeCursor(page.Cursor, &key); err != nil {
			return paging.Page[types.ItemView]{}, err
		}
		at := slices.IndexFunc(s.items, func(item types.ItemView) bool { return item.Id == key.ID })
		if at < 0 {
			// A cursor that reads but names no position of the order.
			return paging.Page[types.ItemView]{}, paging.RefusedCursor()
		}
		start = at + 1
	}
	var rows []types.ItemView
	for _, item := range s.items[start:] {
		if inStock != nil && item.InStock != *inStock {
			continue
		}
		if len(ids) > 0 && !slices.Contains(ids, item.Id) {
			continue
		}
		if rows = append(rows, item); len(rows) > page.Limit {
			break
		}
	}
	return paging.FromRows(rows, page.Limit, func(item types.ItemView) any { return itemKey{ID: item.Id} })
}

// ListTags pages the sorted tags by the last tag of a page: any string
// reads as a position, so only a cursor that is not a JSON string's is
// refused.
func (s *shelf) ListTags(_ context.Context, page paging.Request) (paging.Page[string], error) {
	s.pages = append(s.pages, page)
	start := 0
	if page.Cursor != "" {
		var after string
		if err := paging.DecodeCursor(page.Cursor, &after); err != nil {
			return paging.Page[string]{}, err
		}
		start = len(s.tags)
		if at := slices.IndexFunc(s.tags, func(tag string) bool { return tag > after }); at >= 0 {
			start = at
		}
	}
	var rows []string
	rows = append(rows, s.tags[start:min(start+page.Limit+1, len(s.tags))]...)
	return paging.FromRows(rows, page.Limit, func(tag string) any { return tag })
}

// The catalog's items, in its order; ids are the scalar's text.
var stock = []struct {
	id, name string
	inStock  bool
}{
	{"0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e41", "anvil", true},
	{"0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e42", "bucket", false},
	{"0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e43", "crate", true},
	{"0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e44", "drum", true},
	{"0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e45", "easel", false},
}

func serve(t *testing.T) (*httptest.Server, *shelf) {
	t.Helper()
	impl := &shelf{tags: []string{"blue", "green", "red"}}
	for _, item := range stock {
		id, err := types.ParseIdentityUUID(item.id)
		if err != nil {
			t.Fatal(err)
		}
		impl.items = append(impl.items, types.ItemView{Id: id, Name: item.name, Sku: types.IdentitySlug(item.name), InStock: item.inStock})
	}
	return serveWith(t, api.Implementations{Catalog: impl}), impl
}

func serveWith(t *testing.T, implementations api.Implementations) *httptest.Server {
	t.Helper()
	router := chi.NewRouter()
	if err := api.RegisterRoutes(router, api.Config{Logger: zap.NewNop(), Implementations: implementations}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return server
}

// reply is a response: its status and its decoded body.
type reply struct {
	status int
	body   map[string]any
}

// get sends GET path?query, the query as it is.
func get(t *testing.T, server *httptest.Server, path, query string) reply {
	t.Helper()
	target := server.URL + path
	if query != "" {
		target += "?" + query
	}
	resp, err := http.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("GET %s: the body is not a JSON object: %s", target, raw)
	}
	return reply{resp.StatusCode, body}
}

// page is a 200's data: the page.
func page(t *testing.T, got reply) (items []any, next any) {
	t.Helper()
	body := got.body
	if got.status != http.StatusOK {
		t.Fatalf("status = %d, body %v", got.status, body)
	}
	data, ok := body["data"].(map[string]any)
	if !ok || len(data) != 2 {
		t.Fatalf("data is not a page, {items, next}: %v", body)
	}
	items, ok = data["items"].([]any)
	if !ok {
		t.Fatalf("data.items is not a list: %v", data)
	}
	next, ok = data["next"]
	if !ok {
		t.Fatalf("data has no next: %v", data)
	}
	// meta keeps the request id alone: no total, no links.
	if meta, _ := body["meta"].(map[string]any); len(meta) != 1 || meta["requestId"] == nil || body["links"] != nil {
		t.Fatalf("meta = %v, links = %v", body["meta"], body["links"])
	}
	return items, next
}

// walk follows next from the first page under query, and returns the
// names of the items of each page.
func walk(t *testing.T, server *httptest.Server, query url.Values) [][]string {
	t.Helper()
	var pages [][]string
	for {
		if len(pages) > len(stock) {
			t.Fatalf("more pages than items: %v", pages)
		}
		items, next := page(t, get(t, server, "/api/items", query.Encode()))
		var names []string
		for _, item := range items {
			names = append(names, item.(map[string]any)["name"].(string))
		}
		pages = append(pages, names)
		if next == nil {
			return pages
		}
		cursor, ok := next.(string)
		if !ok || cursor == "" {
			t.Fatalf("next = %#v, want a cursor or null", next)
		}
		query.Set("cursor", cursor)
	}
}

func TestListItemsPagesTheCatalog(t *testing.T) {
	server, impl := serve(t)
	got := walk(t, server, url.Values{})
	want := [][]string{{"anvil", "bucket"}, {"crate", "drum"}, {"easel"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pages = %v, want %v", got, want)
	}
	// Each request had the default limit; only the first had no cursor.
	for i, request := range impl.pages {
		if request.Limit != 2 || (request.Cursor == "") != (i == 0) {
			t.Errorf("page request %d = %+v", i, request)
		}
	}

	for _, tc := range []struct {
		limit string
		want  [][]string
	}{
		{"1", [][]string{{"anvil"}, {"bucket"}, {"crate"}, {"drum"}, {"easel"}}},
		{"3", [][]string{{"anvil", "bucket", "crate"}, {"drum", "easel"}}},
		{"5", [][]string{{"anvil", "bucket", "crate", "drum", "easel"}}},
	} {
		if got := walk(t, server, url.Values{"limit": {tc.limit}}); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("limit=%s: pages = %v, want %v", tc.limit, got, tc.want)
		}
	}
}

// The filters apply to each page; a cursor carries none, so one taken
// under other filters goes on from its position under these.
func TestListItemsFiltersEachPage(t *testing.T) {
	server, _ := serve(t)
	if got, want := walk(t, server, url.Values{"inStock": {"true"}, "limit": {"1"}}), [][]string{{"anvil"}, {"crate"}, {"drum"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("in stock: pages = %v, want %v", got, want)
	}
	ids := url.Values{"ids": {stock[0].id, stock[2].id, stock[4].id}}
	if got, want := walk(t, server, ids), [][]string{{"anvil", "crate"}, {"easel"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("by ids: pages = %v, want %v", got, want)
	}

	_, next := page(t, get(t, server, "/api/items", ""))
	query := url.Values{"inStock": {"false"}, "cursor": {next.(string)}}
	if got, want := walk(t, server, query), [][]string{{"easel"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("not in stock after bucket: pages = %v, want %v", got, want)
	}
}

func TestListTagsPagesAPrimitive(t *testing.T) {
	server, impl := serve(t)
	items, next := page(t, get(t, server, "/api/tags", ""))
	if !reflect.DeepEqual(items, []any{"blue", "green", "red"}) || next != nil {
		t.Fatalf("default limit: items %v, next %v", items, next)
	}
	if got := impl.pages[0]; got != (paging.Request{Limit: 20}) {
		t.Fatalf("page request = %+v, want the default limit of @paginated()", got)
	}

	items, next = page(t, get(t, server, "/api/tags", "limit=2"))
	if !reflect.DeepEqual(items, []any{"blue", "green"}) || next == nil {
		t.Fatalf("limit=2: items %v, next %v", items, next)
	}
	items, next = page(t, get(t, server, "/api/tags", "limit=2&cursor="+next.(string)))
	if !reflect.DeepEqual(items, []any{"red"}) || next != nil {
		t.Fatalf("second page: items %v, next %v", items, next)
	}

	// A page past the last holds no item, written [], not null.
	past, err := paging.EncodeCursor("zzz")
	if err != nil {
		t.Fatal(err)
	}
	items, next = page(t, get(t, server, "/api/tags", "cursor="+past))
	if items == nil || len(items) != 0 || next != nil {
		t.Fatalf("past the last: items %#v, next %v", items, next)
	}
}

// vectors are the HTTP runtimes' page vectors
// (runtime/http/testdata/page_parity.json).
type vectors struct {
	Reads []struct {
		Name    string
		Spec    paging.Spec
		Query   string
		Request *struct {
			Cursor *string
			Limit  int
		}
		Problem map[string]any
	}
	Refused []struct {
		Name    string
		Cursor  string
		Problem map[string]any
	}
}

func readVectors(t *testing.T) vectors {
	t.Helper()
	data, err := os.ReadFile("testdata/page_parity.json")
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// checkProblem compares a refusal with a vector's problem: its status, and
// the body's code, detail and details.
func checkProblem(t *testing.T, refusal reply, want map[string]any) {
	t.Helper()
	got := map[string]any{"status": float64(refusal.status)}
	for _, key := range []string{"code", "detail", "details"} {
		if value, ok := refusal.body[key]; ok {
			got[key] = value
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("problem = %v, want %v", got, want)
	}
}

// Every read vector of either route's limits goes through the route: a
// refusal is the vector's 400, and a request the reader takes reaches the
// implementation as the vector's page request.
func TestRoutesReadThePageAsTheVectors(t *testing.T) {
	server, impl := serve(t)
	routes := map[paging.Spec]string{
		{DefaultLimit: 2, MaxLimit: 5}:    "/api/items",
		{DefaultLimit: 20, MaxLimit: 100}: "/api/tags",
	}
	ran := 0
	for _, vector := range readVectors(t).Reads {
		path, ok := routes[vector.Spec]
		if !ok {
			continue
		}
		ran++
		t.Run(vector.Name, func(t *testing.T) {
			before := len(impl.pages)
			got := get(t, server, path, vector.Query)
			if vector.Problem != nil {
				checkProblem(t, got, vector.Problem)
				if len(impl.pages) != before {
					t.Errorf("a refused page reached the implementation: %+v", impl.pages[before:])
				}
				return
			}
			if len(impl.pages) != before+1 {
				t.Fatalf("the implementation got %d page requests, want 1; status %d, body %v", len(impl.pages)-before, got.status, got.body)
			}
			want := paging.Request{Limit: vector.Request.Limit}
			if vector.Request.Cursor != nil {
				want.Cursor = *vector.Request.Cursor
			}
			if got := impl.pages[before]; got != want {
				t.Errorf("page request = %+v, want %+v", got, want)
			}
		})
	}
	if ran == 0 {
		t.Fatal("no read vector has either route's limits")
	}
}

// A cursor the reader takes and the implementation cannot read back is
// the vectors' 400 at cursor, through either route; so is one that reads
// but names no item, and one forged from another key.
func TestRoutesRefuseCursorsAsTheVectors(t *testing.T) {
	server, _ := serve(t)
	vectors := readVectors(t).Refused
	ran := 0
	for _, vector := range vectors {
		if _, err := paging.Parse(url.Values{"cursor": {vector.Cursor}}, paging.Spec{DefaultLimit: 1, MaxLimit: 1}); err != nil || vector.Cursor == "" {
			continue // the reader refuses it first, or reads none
		}
		ran++
		for _, path := range []string{"/api/items", "/api/tags"} {
			t.Run(vector.Name+" "+path, func(t *testing.T) {
				checkProblem(t, get(t, server, path, "cursor="+url.QueryEscape(vector.Cursor)), vector.Problem)
			})
		}
	}
	if ran == 0 {
		t.Fatal("no refused vector reaches the implementation")
	}

	// Every refused vector is the one 400 at cursor.
	refused := vectors[0].Problem
	unknown, err := types.ParseIdentityUUID("0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e49")
	if err != nil {
		t.Fatal(err)
	}
	for name, key := range map[string]any{
		"an item not in the catalog": itemKey{ID: unknown},
		"another key":                map[string]string{"id": "anvil"},
		"a key without an id":        map[string]any{"position": 3},
	} {
		cursor, err := paging.EncodeCursor(key)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(name, func(t *testing.T) {
			checkProblem(t, get(t, server, "/api/items", "cursor="+cursor), refused)
		})
	}
}

// The implementation scaffold answers every paged list 501 until it is
// written, with the page's zero value beside its error.
func TestScaffoldAnswersNotImplemented(t *testing.T) {
	implementations, err := scaffold.New(api.Deps{})
	if err != nil {
		t.Fatal(err)
	}
	server := serveWith(t, implementations)
	for _, path := range []string{"/api/items", "/api/tags"} {
		if got := get(t, server, path, "limit=1"); got.status != http.StatusNotImplemented {
			t.Errorf("GET %s = %d %v, want 501", path, got.status, got.body)
		}
	}
}
