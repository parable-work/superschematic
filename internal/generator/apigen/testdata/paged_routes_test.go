// This file runs inside the generated API module of fixture-paged-api
// (TestPagedRoutesServePages copies it there, with the HTTP runtimes'
// page vectors as testdata/page_parity.json, the scaffolds as scaffolds/
// and the implementation scaffold as impl/). It registers the generated
// routes with an in-memory catalog and pages its lists over httptest
// (D60): the first page, the next by its cursor, the last with a null
// next; filters applied to each page; the limit and its bounds; the items
// sorted by name or sku, each way, with the next cursor bound to the sort
// (D60, amended); and every refusal the vectors hold, worded as they word
// it.
package fixturepagedapi_test

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strings"
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

// itemKey is the position of an item in the catalog's order: the value of
// the field the page is sorted by, and its id, which breaks ties.
type itemKey struct {
	Name string             `json:"name,omitempty"`
	Sku  string             `json:"sku,omitempty"`
	ID   types.IdentityUUID `json:"id"`
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
// the cursor's, in the page's sort, ties broken by id in the same
// direction, under the filters, and builds the page from them.
func (s *shelf) ListItems(_ context.Context, ids []types.IdentityUUID, inStock *bool, page paging.Request) (paging.Page[types.ItemView], error) {
	s.pages = append(s.pages, page)
	value := func(item types.ItemView) string {
		if page.Sort.Field == "sku" {
			return string(item.Sku)
		}
		return item.Name
	}
	sorted := slices.Clone(s.items)
	slices.SortFunc(sorted, func(a, b types.ItemView) int {
		order := cmp.Or(strings.Compare(value(a), value(b)), strings.Compare(a.Id.String(), b.Id.String()))
		if page.Sort.Desc {
			return -order
		}
		return order
	})
	start := 0
	if page.Cursor != "" {
		var key itemKey
		if err := paging.DecodeCursor(page.Cursor, &key); err != nil {
			return paging.Page[types.ItemView]{}, err
		}
		at := slices.IndexFunc(sorted, func(item types.ItemView) bool { return item.Id == key.ID })
		if at < 0 {
			// A cursor that reads but names no position of the order.
			return paging.Page[types.ItemView]{}, paging.RefusedCursor()
		}
		start = at + 1
	}
	var rows []types.ItemView
	for _, item := range sorted[start:] {
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
	return paging.FromRows(rows, page.Limit, func(item types.ItemView) any {
		if page.Sort.Field == "sku" {
			return itemKey{Sku: string(item.Sku), ID: item.Id}
		}
		return itemKey{Name: item.Name, ID: item.Id}
	})
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

// The catalog's items, in the order of their names; ids are the scalar's
// text. Two share a sku, which their ids order.
var stock = []struct {
	id, name, sku string
	inStock       bool
}{
	{"0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e41", "anvil", "sku-2", true},
	{"0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e42", "bucket", "sku-1", false},
	{"0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e43", "crate", "sku-5", true},
	{"0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e44", "drum", "sku-2", true},
	{"0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e45", "easel", "sku-4", false},
}

func serve(t *testing.T) (*httptest.Server, *shelf) {
	t.Helper()
	impl := &shelf{tags: []string{"blue", "green", "red"}}
	for _, item := range stock {
		id, err := types.ParseIdentityUUID(item.id)
		if err != nil {
			t.Fatal(err)
		}
		impl.items = append(impl.items, types.ItemView{Id: id, Name: item.name, Sku: types.IdentitySlug(item.sku), InStock: item.inStock})
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
	// Each request had the default limit and sort; only the first had no
	// cursor.
	for i, request := range impl.pages {
		if request.Limit != 2 || request.Sort != (paging.Sort{Field: "name"}) || (request.Cursor == "") != (i == 0) {
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

// The page's sort, read and bound (D60, amended): each direction of each
// field pages the whole catalog, ties broken by id in the same direction;
// the implementation reads its own cursor, and the next the route answers
// names the sort; a sort not offered, a cursor of another sort and the
// implementation's own cursor are each the route's 400, which no
// implementation sees.
func TestListItemsSortsThePage(t *testing.T) {
	server, impl := serve(t)
	for _, tc := range []struct {
		sort string
		want [][]string
	}{
		{"name", [][]string{{"anvil", "bucket"}, {"crate", "drum"}, {"easel"}}},
		{"-name", [][]string{{"easel", "drum"}, {"crate", "bucket"}, {"anvil"}}},
		{"sku", [][]string{{"bucket", "anvil"}, {"drum", "easel"}, {"crate"}}},
		{"-sku", [][]string{{"crate", "easel"}, {"drum", "anvil"}, {"bucket"}}},
	} {
		before := len(impl.pages)
		if got := walk(t, server, url.Values{"sort": {tc.sort}}); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("sort=%s: pages = %v, want %v", tc.sort, got, tc.want)
		}
		sort, _ := paging.ParseSort(tc.sort)
		for _, request := range impl.pages[before:] {
			if request.Sort != sort {
				t.Errorf("sort=%s: page request %+v", tc.sort, request)
			}
		}
	}

	// The next names its sort; the implementation reads its own key.
	_, next := page(t, get(t, server, "/api/items", "sort=-sku"))
	data, err := base64.RawURLEncoding.DecodeString(next.(string))
	if err != nil || !strings.HasPrefix(string(data), `{"sort":"-sku","key":{"sku":"sku-4","id":`) {
		t.Fatalf("next = %s, %v", data, err)
	}
	before := len(impl.pages)
	page(t, get(t, server, "/api/items", "sort=-sku&cursor="+next.(string)))
	var key itemKey
	if err := paging.DecodeCursor(impl.pages[before].Cursor, &key); err != nil || key.Sku != "sku-4" || key.Name != "" {
		t.Fatalf("the implementation's cursor reads %+v, %v", key, err)
	}
	own := impl.pages[before].Cursor

	sorted := map[string]any{
		"status": float64(http.StatusBadRequest), "code": "bad_request", "detail": paging.SortedCursorDetail,
		"details": map[string]any{"location": "query", "parameter": "cursor", "reason": paging.SortedCursorReason},
	}
	refused := map[string]any{
		"status": float64(http.StatusBadRequest), "code": "bad_request", "detail": paging.RefusedCursorDetail,
		"details": map[string]any{"location": "query", "parameter": "cursor", "reason": paging.RefusedCursorReason},
	}
	values := "expected one of name, -name, sku, -sku"
	notOffered := map[string]any{
		"status": float64(http.StatusBadRequest), "code": "bad_request", "detail": "Invalid query parameter sort: " + values,
		"details": map[string]any{"location": "query", "parameter": "sort", "reason": values},
	}
	before = len(impl.pages)
	for _, tc := range []struct {
		name, query string
		want        map[string]any
	}{
		{"a cursor of another sort", "sort=sku&cursor=" + next.(string), sorted},
		{"a cursor of another sort under the default", "cursor=" + next.(string), sorted},
		{"the implementation's own cursor", "sort=-sku&cursor=" + own, refused},
		{"a sort not offered", "sort=price", notOffered},
		{"a sort not offered before a cursor of another sort", "sort=price&cursor=" + next.(string), notOffered},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkProblem(t, get(t, server, "/api/items", tc.query), tc.want)
		})
	}
	if len(impl.pages) != before {
		t.Errorf("a refused page reached the implementation: %+v", impl.pages[before:])
	}
}

// vectors are the HTTP runtimes' page vectors
// (runtime/http/testdata/page_parity.json).
type vectors struct {
	Reads []struct {
		Name    string
		Spec    vectorSpec
		Query   string
		Request *struct {
			Cursor *string
			Limit  int
			Sort   string
		}
		Problem map[string]any
	}
	Refused []struct {
		Name    string
		Cursor  string
		Problem map[string]any
	}
}

// vectorSpec is a vector's spec: the page's limits and its sort.
type vectorSpec struct {
	DefaultLimit int
	MaxLimit     int
	Sort         *struct {
		Param   string
		Fields  []string
		Default string
	}
}

// route is the spec as a route's key: its limits and its sort.
func (s vectorSpec) route() string {
	if s.Sort == nil {
		return fmt.Sprintf("%d/%d", s.DefaultLimit, s.MaxLimit)
	}
	return fmt.Sprintf("%d/%d/%s/%s/%s", s.DefaultLimit, s.MaxLimit, s.Sort.Param, strings.Join(s.Sort.Fields, ","), s.Sort.Default)
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

// Every read vector of either route's limits and sort goes through the
// route: a refusal is the vector's 400, and a request the reader takes
// reaches the implementation as the vector's page request, its cursor
// unbound and its sort read.
func TestRoutesReadThePageAsTheVectors(t *testing.T) {
	server, impl := serve(t)
	routes := map[string]string{
		"2/5/sort/name,sku/name": "/api/items",
		"20/100":                 "/api/tags",
	}
	ran := map[string]int{}
	for _, vector := range readVectors(t).Reads {
		path, ok := routes[vector.Spec.route()]
		if !ok {
			continue
		}
		ran[path]++
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
			if vector.Request.Sort != "" {
				want.Sort, _ = paging.ParseSort(vector.Request.Sort)
			}
			if got := impl.pages[before]; got != want {
				t.Errorf("page request = %+v, want %+v", got, want)
			}
		})
	}
	if ran["/api/items"] == 0 || ran["/api/tags"] == 0 {
		t.Fatalf("read vectors run on each route: %v", ran)
	}
}

// boundTo is cursor bound to sort as a router binds a next, whatever its
// key: a cursor that is no base64url stays as it is, and is refused as
// no envelope.
func boundTo(cursor, sort string) string {
	key, err := base64.RawURLEncoding.Strict().DecodeString(cursor)
	if err != nil || cursor == "" {
		return cursor
	}
	return base64.RawURLEncoding.EncodeToString([]byte(`{"sort":"` + sort + `","key":` + string(key) + `}`))
}

// A cursor the reader takes and the implementation cannot read back is
// the vectors' 400 at cursor, through either route, bound to the items'
// sort for theirs; so is one that reads but names no item, and one forged
// from another key.
func TestRoutesRefuseCursorsAsTheVectors(t *testing.T) {
	server, _ := serve(t)
	vectors := readVectors(t).Refused
	ran := 0
	for _, vector := range vectors {
		if _, err := paging.Parse(url.Values{"cursor": {vector.Cursor}}, paging.Spec{DefaultLimit: 1, MaxLimit: 1}); err != nil || vector.Cursor == "" {
			continue // the reader refuses it first, or reads none
		}
		ran++
		for path, cursor := range map[string]string{"/api/items": boundTo(vector.Cursor, "name"), "/api/tags": vector.Cursor} {
			t.Run(vector.Name+" "+path, func(t *testing.T) {
				checkProblem(t, get(t, server, path, "cursor="+url.QueryEscape(cursor)), vector.Problem)
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
			checkProblem(t, get(t, server, "/api/items", "cursor="+boundTo(cursor, "name")), refused)
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
