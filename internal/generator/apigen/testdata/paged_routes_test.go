// This file runs inside the generated API module of fixture-paged-api
// (TestPagedRoutesServePages copies it there, with the HTTP runtimes'
// page vectors as testdata/page_parity.json, their filter vectors as
// testdata/filter_parity.json, the scaffolds as scaffolds/ and the
// implementation scaffold as impl/). It registers the generated routes
// with an in-memory catalog and pages its lists over httptest (D60): the
// first page, the next by its cursor, the last with a null next; filters
// applied to each page; the limit and its bounds; the items sorted by
// name or sku, each way, with the next cursor bound to the sort (D60,
// amended); and every refusal the vectors hold, worded as they word it.
// It filters the parts through listParts's FilterBy (D106): a filter on a
// field of each kind, every operator, beside a sort and a cursor, the
// typed filter the implementation takes, the reader's refusals as the
// filter vectors word them, and a value its field's type refuses, a 400
// at filter[field].
package fixturepagedapi_test

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/parable-work/superschematic/runtime/http/go/filterparse"
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
// theirs, its parts, and every page request and every filter the routes
// passed it.
type shelf struct {
	items   []types.ItemView
	tags    []string
	parts   []types.PartView
	pages   []paging.Request
	filters []api.CatalogListPartsFilter
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

// partKey is the position of a part in the page's order: the value of the
// field the page is sorted by, and its id, which breaks ties.
type partKey struct {
	Name     string             `json:"name,omitempty"`
	Quantity *int64             `json:"quantity,omitempty"`
	ID       types.IdentityUUID `json:"id"`
}

// keepEquals reports whether a field passes its filter (D106), as a
// query's WHERE would: every member set holds at once, and a null field,
// a nil value, passes no comparison, only isNull=true. A nil filter keeps
// every row.
func keepEquals[T comparable](filter *filterparse.Equals[T], value *T) bool {
	if filter == nil {
		return true
	}
	return keepNull(filter.IsNull, value) &&
		(filter.Eq == nil || value != nil && *value == *filter.Eq) &&
		(filter.In == nil || value != nil && slices.Contains(filter.In, *value))
}

// keepOrdered is keepEquals for an ordered field, whose values compare
// orders.
func keepOrdered[T any](filter *filterparse.Ordered[T], value *T, compare func(a, b T) int) bool {
	if filter == nil {
		return true
	}
	holds := func(bound *T, ok func(order int) bool) bool {
		return bound == nil || value != nil && ok(compare(*value, *bound))
	}
	return keepNull(filter.IsNull, value) &&
		holds(filter.Eq, func(order int) bool { return order == 0 }) &&
		holds(filter.Gt, func(order int) bool { return order > 0 }) &&
		holds(filter.Gte, func(order int) bool { return order >= 0 }) &&
		holds(filter.Lt, func(order int) bool { return order < 0 }) &&
		holds(filter.Lte, func(order int) bool { return order <= 0 }) &&
		(filter.In == nil || value != nil && slices.ContainsFunc(filter.In, func(one T) bool { return compare(*value, one) == 0 }))
}

// keepText is keepEquals for a field of text, whose contains is the text a
// value holds, in any case.
func keepText(filter *filterparse.Text[string], value *string) bool {
	if filter == nil {
		return true
	}
	return keepNull(filter.IsNull, value) &&
		(filter.Eq == nil || value != nil && *value == *filter.Eq) &&
		(filter.In == nil || value != nil && slices.Contains(filter.In, *value)) &&
		(filter.Contains == nil || value != nil && strings.Contains(strings.ToLower(*value), strings.ToLower(*filter.Contains)))
}

// keepNull is a filter's isNull: true keeps a null field, false any
// other.
func keepNull[T any](isNull *bool, value *T) bool {
	return isNull == nil || *isNull == (value == nil)
}

// compareDecimals orders two decimals by their values, not their text.
func compareDecimals(a, b types.GenericDecimal) int {
	x, _ := new(big.Rat).SetString(string(a))
	y, _ := new(big.Rat).SetString(string(b))
	return x.Cmp(y)
}

// ListParts reads the parts the filter keeps, in the page's sort, name or
// quantity, ties broken by id in the same direction, up to one past the
// limit after the cursor's, and builds the page from them.
func (s *shelf) ListParts(_ context.Context, filter api.CatalogListPartsFilter, page paging.Request) (paging.Page[types.PartView], error) {
	s.pages = append(s.pages, page)
	s.filters = append(s.filters, filter)
	sorted := slices.Clone(s.parts)
	slices.SortFunc(sorted, func(a, b types.PartView) int {
		order := strings.Compare(a.Name, b.Name)
		if page.Sort.Field == "quantity" {
			order = cmp.Compare(a.Quantity, b.Quantity)
		}
		order = cmp.Or(order, strings.Compare(a.Id.String(), b.Id.String()))
		if page.Sort.Desc {
			return -order
		}
		return order
	})
	start := 0
	if page.Cursor != "" {
		var key partKey
		if err := paging.DecodeCursor(page.Cursor, &key); err != nil {
			return paging.Page[types.PartView]{}, err
		}
		at := slices.IndexFunc(sorted, func(part types.PartView) bool { return part.Id == key.ID })
		if at < 0 {
			return paging.Page[types.PartView]{}, paging.RefusedCursor()
		}
		start = at + 1
	}
	var rows []types.PartView
	for _, part := range sorted[start:] {
		quantity := int64(part.Quantity)
		keep := keepEquals(filter.Id, &part.Id) &&
			keepText(filter.Name, &part.Name) &&
			keepEquals(filter.Kind, &part.Kind) &&
			keepOrdered(filter.Quantity, &quantity, cmp.Compare[int64]) &&
			keepOrdered(filter.Weight, &part.Weight, cmp.Compare[float64]) &&
			keepOrdered(filter.Price, &part.Price, compareDecimals) &&
			keepEquals(filter.Discontinued, &part.Discontinued) &&
			keepOrdered(filter.MadeAt, &part.MadeAt, func(a, b types.TemporalDateTime) int { return time.Time(a).Compare(time.Time(b)) }) &&
			keepOrdered(filter.RetiredOn, part.RetiredOn, func(a, b types.TemporalDate) int { return strings.Compare(string(a), string(b)) }) &&
			keepEquals(filter.SupplierId, part.SupplierId)
		if !keep {
			continue
		}
		if rows = append(rows, part); len(rows) > page.Limit {
			break
		}
	}
	return paging.FromRows(rows, page.Limit, func(part types.PartView) any {
		if page.Sort.Field == "quantity" {
			quantity := int64(part.Quantity)
			return partKey{Quantity: &quantity, ID: part.Id}
		}
		return partKey{Name: part.Name, ID: part.Id}
	})
}

// The catalog's parts, in the order of their names: a value of every kind
// a filter compares. An empty retiredOn or supplier is null. Prices order
// by value, not by text (9.5 before 10.25), and two parts hold a
// quantity, which their ids order.
var bins = []struct {
	id, name, kind string
	quantity       int64
	weight         float64
	price          string
	discontinued   bool
	madeAt         string
	retiredOn      string
	supplier       string
}{
	{"1c9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e51", "Washer-M6", "washer", 3, 0.2, "10.25", true, "2026-05-20T00:00:00Z", "", acme},
	{"1c9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e52", "bolt-m4", "bolt", 40, 1.5, "0.10", false, "2026-01-10T00:00:00Z", "", acme},
	{"1c9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e53", "bolt-m6", "bolt", 12, 2.5, "9.5", false, "2026-02-10T08:30:00Z", "", zenith},
	{"1c9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e54", "nut-m4", "nut", 40, 0.5, "0.05", true, "2026-03-01T00:00:00Z", "2026-06-30", acme},
	{"1c9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e55", "nut-m6", "nut", 7, 0.75, "0.08", false, "2026-03-15T12:00:00Z", "", ""},
	{"1c9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e56", "washer-m4", "washer", 100, 0.1, "0.02", false, "2026-04-01T00:00:00Z", "2026-09-01", ""},
}

// The parts' suppliers.
const (
	acme   = "2d9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e61"
	zenith = "2d9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e62"
)

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
	for _, bin := range bins {
		part := types.PartView{
			Id: mustParse(t, types.ParseIdentityUUID, bin.id), Name: bin.name, Kind: types.PartKind(bin.kind),
			Quantity: types.GenericInt64(bin.quantity), Weight: bin.weight, Price: types.GenericDecimal(bin.price),
			Discontinued: bin.discontinued, MadeAt: mustParse(t, types.ParseTemporalDateTime, bin.madeAt),
		}
		if bin.retiredOn != "" {
			retiredOn := mustParse(t, types.ParseTemporalDate, bin.retiredOn)
			part.RetiredOn = &retiredOn
		}
		if bin.supplier != "" {
			supplier := mustParse(t, types.ParseIdentityUUID, bin.supplier)
			part.SupplierId = &supplier
		}
		impl.parts = append(impl.parts, part)
	}
	return serveWith(t, api.Implementations{Catalog: impl}), impl
}

// mustParse is a scalar's value of its text.
func mustParse[T any](t *testing.T, parse func(string) (T, error), text string) T {
	t.Helper()
	value, err := parse(text)
	if err != nil {
		t.Fatal(err)
	}
	return value
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

// walk follows next from the first page of the items under query, and
// returns the names of the items of each page.
func walk(t *testing.T, server *httptest.Server, query url.Values) [][]string {
	t.Helper()
	return walkPath(t, server, "/api/items", query)
}

// walkPath is walk over the list at path.
func walkPath(t *testing.T, server *httptest.Server, path string, query url.Values) [][]string {
	t.Helper()
	var pages [][]string
	for {
		if len(pages) > len(stock)+len(bins) {
			t.Fatalf("more pages than items: %v", pages)
		}
		items, next := page(t, get(t, server, path, query.Encode()))
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
	for _, path := range []string{"/api/items", "/api/tags", "/api/parts"} {
		if got := get(t, server, path, "limit=1"); got.status != http.StatusNotImplemented {
			t.Errorf("GET %s = %d %v, want 501", path, got.status, got.body)
		}
	}
	// A filtered list's scaffold takes the filter and answers the same.
	if got := get(t, server, "/api/parts", "filter[kind]=nut&filter[quantity][gte]=1"); got.status != http.StatusNotImplemented {
		t.Errorf("GET /api/parts under a filter = %d %v, want 501", got.status, got.body)
	}
}

// parts is the names of the parts a query of listParts answers, every
// page of it in one list.
func parts(t *testing.T, server *httptest.Server, query string) []string {
	t.Helper()
	values, err := url.ParseQuery(query)
	if err != nil {
		t.Fatal(err)
	}
	values.Set("limit", "5")
	var names []string
	for _, page := range walkPath(t, server, "/api/parts", values) {
		names = append(names, page...)
	}
	return names
}

// A filter on a field of each kind, and each operator a kind takes
// (D106): filter[field]=value is equality, filter[field][op]=value names
// an operator, an in lists its values between commas, and the members
// hold at once. The parts answer in the order of their names.
func TestListPartsFiltersByEachKind(t *testing.T) {
	server, _ := serve(t)
	for _, tc := range []struct {
		name, query string
		want        []string
	}{
		{"no filter", "", []string{"Washer-M6", "bolt-m4", "bolt-m6", "nut-m4", "nut-m6", "washer-m4"}},

		// Equality, one per kind.
		{"a uuid", "filter[id]=" + bins[3].id, []string{"nut-m4"}},
		{"a string", "filter[name]=bolt-m6", []string{"bolt-m6"}},
		{"a string in another case is another string", "filter[name]=BOLT-M6", nil},
		{"an enum", "filter[kind]=nut", []string{"nut-m4", "nut-m6"}},
		{"an integer", "filter[quantity]=40", []string{"bolt-m4", "nut-m4"}},
		{"a float", "filter[weight]=0.5", []string{"nut-m4"}},
		{"a decimal, by its value", "filter[price]=0.1", []string{"bolt-m4"}},
		{"a boolean", "filter[discontinued]=true", []string{"Washer-M6", "nut-m4"}},
		{"a datetime", "filter[madeAt]=2026-03-01T00:00:00Z", []string{"nut-m4"}},
		{"a datetime in another offset", "filter[madeAt]=2026-03-01T01:00:00%2B01:00", []string{"nut-m4"}},
		{"a date", "filter[retiredOn]=2026-06-30", []string{"nut-m4"}},
		{"a uuid that may be null", "filter[supplierId]=" + zenith, []string{"bolt-m6"}},
		{"equality by its operator", "filter[kind][eq]=washer", []string{"Washer-M6", "washer-m4"}},
		{"brackets percent-encoded", "filter%5Bkind%5D=bolt", []string{"bolt-m4", "bolt-m6"}},

		// in.
		{"an in of enums", "filter[kind][in]=bolt,washer", []string{"Washer-M6", "bolt-m4", "bolt-m6", "washer-m4"}},
		{"an in of uuids, trimmed", "filter[id][in]=" + bins[0].id + ",+" + bins[5].id + ",", []string{"Washer-M6", "washer-m4"}},
		{"an in of integers", "filter[quantity][in]=7,12,99", []string{"bolt-m6", "nut-m6"}},
		{"an in of decimals", "filter[price][in]=0.050,10.25", []string{"Washer-M6", "nut-m4"}},
		{"an in of booleans", "filter[discontinued][in]=true,false", []string{"Washer-M6", "bolt-m4", "bolt-m6", "nut-m4", "nut-m6", "washer-m4"}},
		{"an in of dates leaves a null out", "filter[retiredOn][in]=2026-06-30,2026-09-01", []string{"nut-m4", "washer-m4"}},

		// Ranges.
		{"an integer range", "filter[quantity][gte]=7&filter[quantity][lt]=40", []string{"bolt-m6", "nut-m6"}},
		{"an integer's bounds, both inside", "filter[quantity][gt]=7&filter[quantity][lte]=40", []string{"bolt-m4", "bolt-m6", "nut-m4"}},
		{"a float range", "filter[weight][gt]=0.5&filter[weight][lte]=1.5", []string{"bolt-m4", "nut-m6"}},
		{"a decimal range orders values, not text", "filter[price][gt]=9.9", []string{"Washer-M6"}},
		{"a decimal's upper bound", "filter[price][lte]=0.08", []string{"nut-m4", "nut-m6", "washer-m4"}},
		{"a datetime range", "filter[madeAt][gte]=2026-02-10T08:30:00Z&filter[madeAt][lt]=2026-03-15T12:00:00Z", []string{"bolt-m6", "nut-m4"}},
		{"a date range leaves a null out", "filter[retiredOn][lt]=2026-07-01", []string{"nut-m4"}},

		// contains.
		{"contains, in any case", "filter[name][contains]=M6", []string{"Washer-M6", "bolt-m6", "nut-m6"}},
		{"contains keeps its spaces", "filter[name][contains]=+m6", nil},

		// isNull.
		{"isNull true", "filter[supplierId][isNull]=true", []string{"nut-m6", "washer-m4"}},
		{"isNull false", "filter[retiredOn][isNull]=false", []string{"nut-m4", "washer-m4"}},
		{"isNull false beside a range", "filter[retiredOn][isNull]=false&filter[retiredOn][gt]=2026-06-30", []string{"washer-m4"}},

		// Members hold at once.
		{"two fields", "filter[kind]=nut&filter[discontinued]=false", []string{"nut-m6"}},
		{"three fields and an operator each", "filter[kind][in]=bolt,nut&filter[quantity][gte]=12&filter[supplierId]=" + acme, []string{"bolt-m4", "nut-m4"}},
		{"two members no part holds at once", "filter[kind]=bolt&filter[discontinued]=true", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := parts(t, server, tc.query); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("%s: parts = %v, want %v", tc.query, got, tc.want)
			}
		})
	}
}

// The filter reaches the implementation typed (D106): a member per field,
// nil for a field the request does not filter by, each operator the
// request sent set on it and no other, its values decoded as the field's
// type.
func TestListPartsHandsTheImplementationATypedFilter(t *testing.T) {
	server, impl := serve(t)
	page(t, get(t, server, "/api/parts", ""))
	if got := impl.filters[0]; !reflect.DeepEqual(got, api.CatalogListPartsFilter{}) {
		t.Fatalf("no filter sent: the implementation got %+v", got)
	}

	query := "filter[quantity][gte]=10&filter[quantity][lt]=100&filter[kind][in]=bolt,+nut&filter[supplierId][isNull]=false" +
		"&filter[name][contains]=m&filter[madeAt][gt]=2026-01-01T00:00:00Z&filter[price]=0.10&filter[discontinued]=false" +
		"&filter[weight][lte]=2.5&filter[id][in]=" + bins[1].id + "&filter[retiredOn][isNull]=true"
	page(t, get(t, server, "/api/parts", query))
	ten, hundred, no, yes, m, weight := int64(10), int64(100), false, true, "m", 2.5
	madeAt, price := mustParse(t, types.ParseTemporalDateTime, "2026-01-01T00:00:00Z"), types.GenericDecimal("0.10")
	want := api.CatalogListPartsFilter{
		Id:           &filterparse.Equals[types.IdentityUUID]{In: []types.IdentityUUID{mustParse(t, types.ParseIdentityUUID, bins[1].id)}},
		Name:         &filterparse.Text[string]{Contains: &m},
		Kind:         &filterparse.Equals[types.PartKind]{In: []types.PartKind{types.PartKind_Bolt, types.PartKind_Nut}},
		Quantity:     &filterparse.Ordered[int64]{Gte: &ten, Lt: &hundred},
		Weight:       &filterparse.Ordered[float64]{Lte: &weight},
		Price:        &filterparse.Ordered[types.GenericDecimal]{Eq: &price},
		Discontinued: &filterparse.Equals[bool]{Eq: &no},
		MadeAt:       &filterparse.Ordered[types.TemporalDateTime]{Gt: &madeAt},
		RetiredOn:    &filterparse.Ordered[types.TemporalDate]{IsNull: &yes},
		SupplierId:   &filterparse.Equals[types.IdentityUUID]{IsNull: &no},
	}
	got := impl.filters[1]
	// A time compares by its instant, not its fields.
	if got.MadeAt == nil || got.MadeAt.Gt == nil || !time.Time(*got.MadeAt.Gt).Equal(time.Time(madeAt)) {
		t.Fatalf("madeAt = %+v", got.MadeAt)
	}
	got.MadeAt.Gt = &madeAt
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the implementation got\n%s\nwant\n%s", describe(got), describe(want))
	}
}

// describe is a filter as JSON, for a failure's message.
func describe(filter api.CatalogListPartsFilter) string {
	data, err := json.Marshal(filter)
	if err != nil {
		return err.Error()
	}
	return string(data)
}

// A filter beside a sort and a cursor (D106, D60): the filters apply to
// each page, in the page's sort, and a cursor carries none, so the next
// page is asked with the same filters and one taken under other filters
// goes on from its position under these.
func TestListPartsFiltersEachPageOfASort(t *testing.T) {
	server, impl := serve(t)
	query := func(sort string) url.Values {
		return url.Values{"filter[kind][in]": {"bolt,nut"}, "filter[discontinued]": {"false"}, "sort": {sort}, "limit": {"1"}}
	}
	for _, tc := range []struct {
		sort string
		want [][]string
	}{
		{"name", [][]string{{"bolt-m4"}, {"bolt-m6"}, {"nut-m6"}}},
		{"-name", [][]string{{"nut-m6"}, {"bolt-m6"}, {"bolt-m4"}}},
		{"quantity", [][]string{{"nut-m6"}, {"bolt-m6"}, {"bolt-m4"}}},
		{"-quantity", [][]string{{"bolt-m4"}, {"bolt-m6"}, {"nut-m6"}}},
	} {
		before := len(impl.pages)
		if got := walkPath(t, server, "/api/parts", query(tc.sort)); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("sort=%s: pages = %v, want %v", tc.sort, got, tc.want)
		}
		// Every page's request carried the sort and the same filter.
		sort, _ := paging.ParseSort(tc.sort)
		for i, request := range impl.pages[before:] {
			filter := impl.filters[before+i]
			if request.Sort != sort || request.Limit != 1 || (request.Cursor == "") != (i == 0) ||
				filter.Kind == nil || len(filter.Kind.In) != 2 || filter.Discontinued == nil || *filter.Discontinued.Eq {
				t.Errorf("sort=%s: page %d: request %+v, filter %s", tc.sort, i, request, describe(filter))
			}
		}
	}

	// Two parts hold 40, which their ids order, in the sort's direction.
	if got, want := walkPath(t, server, "/api/parts", url.Values{"filter[quantity]": {"40"}, "sort": {"-quantity"}, "limit": {"1"}}), [][]string{{"nut-m4"}, {"bolt-m4"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("a tie under -quantity: pages = %v, want %v", got, want)
	}

	// A cursor taken under no filter, after bolt-m4, goes on under one.
	_, next := page(t, get(t, server, "/api/parts", ""))
	after := url.Values{"filter[kind]": {"washer"}, "cursor": {next.(string)}}
	if got, want := walkPath(t, server, "/api/parts", after), [][]string{{"washer-m4"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("washers after bolt-m4: pages = %v, want %v", got, want)
	}

	// The next of a filtered page names its sort and no filter.
	_, next = page(t, get(t, server, "/api/parts", "filter[kind]=nut&sort=-quantity&limit=1"))
	data, err := base64.RawURLEncoding.DecodeString(next.(string))
	if err != nil || !strings.HasPrefix(string(data), `{"sort":"-quantity","key":{"quantity":40,"id":`) || strings.Contains(string(data), "nut") {
		t.Fatalf("next = %s, %v", data, err)
	}
}

// filterVectors are the HTTP runtimes' filter vectors
// (runtime/http/testdata/filter_parity.json).
type filterVectors struct {
	Reads []struct {
		Name    string
		Query   string
		Problem map[string]any
	}
}

// refusedFilter is the 400 the filter reader answers at a parameter, as
// the filter vectors hold one: its detail, and details that name the
// parameter and the reason.
func refusedFilter(parameter, reason string) map[string]any {
	return map[string]any{
		"status": float64(http.StatusBadRequest), "code": "bad_request",
		"detail":  "Invalid query parameter " + parameter + ": " + reason,
		"details": map[string]any{"location": "query", "parameter": parameter, "reason": reason},
	}
}

// The reader's refusals (D106), each the 400 every server answers at
// filter[field], which no implementation sees: a field FilterBy does not
// name, an operator the field's kind does not take, an operator sent
// twice, and a value an operator does not take. The vectors' refusals
// that hold under any FilterBy, a key that is no filter[field] and more
// filters than a request may hold, go through the route as they are.
func TestListPartsRefusesAFilterAsTheVectors(t *testing.T) {
	server, impl := serve(t)
	fields := "id, name, kind, quantity, weight, price, discontinued, madeAt, retiredOn, supplierId"
	ordered := "eq, in, gt, gte, lt, lte"
	type refusal struct {
		name, query string
		want        map[string]any
	}
	cases := []refusal{
		{"a field FilterBy does not name", "filter[secret]=x", refusedFilter("filter[secret]", "is not a field this operation filters by; expected one of "+fields)},
		{"a field in another case", "filter[Kind]=nut", refusedFilter("filter[Kind]", "is not a field this operation filters by; expected one of "+fields)},
		{"an operator no field takes", "filter[kind][neq]=nut", refusedFilter("filter[kind]", "takes no operator neq; expected one of eq, in")},
		{"an order on an enum", "filter[kind][gt]=nut", refusedFilter("filter[kind]", "takes no operator gt; expected one of eq, in")},
		{"an order on a uuid", "filter[id][lte]=" + bins[0].id, refusedFilter("filter[id]", "takes no operator lte; expected one of eq, in")},
		{"an order on a boolean", "filter[discontinued][gte]=true", refusedFilter("filter[discontinued]", "takes no operator gte; expected one of eq, in")},
		{"an order on text", "filter[name][lt]=m", refusedFilter("filter[name]", "takes no operator lt; expected one of eq, in, contains")},
		{"contains on a number", "filter[quantity][contains]=4", refusedFilter("filter[quantity]", "takes no operator contains; expected one of "+ordered)},
		{"contains on a date", "filter[retiredOn][contains]=2026", refusedFilter("filter[retiredOn]", "takes no operator contains; expected one of "+ordered+", isNull")},
		{"isNull on a field that is never null", "filter[madeAt][isNull]=true", refusedFilter("filter[madeAt]", "takes no operator isNull; expected one of "+ordered)},
		{"isNull that is no boolean", "filter[supplierId][isNull]=1", refusedFilter("filter[supplierId]", "isNull expects true or false")},
		{"equality sent both ways", "filter[kind]=nut&filter[kind][eq]=bolt", refusedFilter("filter[kind]", "holds eq more than once")},
		{"an in of no value", "filter[kind][in]=,", refusedFilter("filter[kind]", "in names no value")},
		{"contains of no text", "filter[name][contains]=", refusedFilter("filter[name]", "contains needs text")},
		// The reader refuses before any value is decoded, and before the
		// page is read.
		{"a refused field beside a refused value", "filter[quantity]=many&filter[secret]=x", refusedFilter("filter[secret]", "is not a field this operation filters by; expected one of "+fields)},
		{"a refused field beside a refused limit", "limit=0&filter[secret]=x", refusedFilter("filter[secret]", "is not a field this operation filters by; expected one of "+fields)},
	}
	data, err := os.ReadFile("testdata/filter_parity.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors filterVectors
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	fromVectors := 0
	for _, vector := range vectors.Reads {
		details, _ := vector.Problem["details"].(map[string]any)
		// A refusal that names no field of the vector's own FilterBy.
		if details["parameter"] == "filter" || details["reason"] == "expected filter[field] or filter[field][operator]" {
			fromVectors++
			cases = append(cases, refusal{"vector: " + vector.Name, vector.Query, vector.Problem})
		}
	}
	if fromVectors < 5 {
		t.Fatalf("%d filter vectors hold under any FilterBy, want the malformed keys and the limit", fromVectors)
	}
	before := len(impl.filters)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			checkProblem(t, get(t, server, "/api/parts", tc.query), tc.want)
		})
	}
	if len(impl.filters) != before {
		t.Errorf("a refused filter reached the implementation: %d calls", len(impl.filters)-before)
	}

	// A list without a FilterBy leaves a filter parameter alone, as it
	// leaves any parameter it does not declare.
	if got := walk(t, server, url.Values{"filter[name]": {"anvil"}, "limit": {"5"}}); len(got) != 1 || len(got[0]) != len(stock) {
		t.Errorf("listItems under a filter parameter: pages = %v", got)
	}
}

// A value its field's type refuses (D106) is a 400 that names
// filter[field]: the route decodes each value as an item of a query list
// of the field's type, after the reader, and answers every refused field
// at once. No implementation sees the request.
func TestListPartsRefusesAValueAtItsField(t *testing.T) {
	server, impl := serve(t)
	before := len(impl.filters)
	for _, tc := range []struct {
		name, query string
		fields      []string
	}{
		{"an integer that is a word", "filter[quantity]=many", []string{"filter[quantity]"}},
		{"an integer with a fraction", "filter[quantity][gte]=1.5", []string{"filter[quantity]"}},
		{"an integer past its range", "filter[quantity]=9007199254740993", []string{"filter[quantity]"}},
		{"a float that is a word", "filter[weight][lt]=heavy", []string{"filter[weight]"}},
		{"a float that is no JSON number", "filter[weight]=NaN", []string{"filter[weight]"}},
		{"a decimal that is a word", "filter[price]=cheap", []string{"filter[price]"}},
		{"an enum's unknown member", "filter[kind]=gear", []string{"filter[kind]"}},
		{"an enum's member in another case", "filter[kind]=Nut", []string{"filter[kind]"}},
		// Identity.UUID reads base62 too, so a short word is one: nope is.
		{"a uuid that is none", "filter[id]=not-a-uuid", []string{"filter[id]"}},
		{"a uuid that may be null and is none", "filter[supplierId]=not+a+uuid", []string{"filter[supplierId]"}},
		{"a boolean that is a word", "filter[discontinued]=maybe", []string{"filter[discontinued]"}},
		{"a datetime that is a word", "filter[madeAt]=yesterday", []string{"filter[madeAt]"}},
		{"a datetime bound that is a date of no month", "filter[madeAt][gte]=2026-13-01T00:00:00Z", []string{"filter[madeAt]"}},
		{"a date that is a word", "filter[retiredOn]=soon", []string{"filter[retiredOn]"}},
		{"one value of an in", "filter[quantity][in]=1,two,3", []string{"filter[quantity]"}},
		{"one enum of an in", "filter[kind][in]=bolt,gear", []string{"filter[kind]"}},
		{"one uuid of an in", "filter[id][in]=" + bins[0].id + ",not-a-uuid", []string{"filter[id]"}},
		{"two fields, each named", "filter[quantity]=many&filter[kind]=gear&filter[name]=bolt-m4", []string{"filter[kind]", "filter[quantity]"}},
		{"a refused value before a refused limit", "filter[madeAt]=yesterday&limit=0", []string{"filter[madeAt]"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := get(t, server, "/api/parts", tc.query)
			if got.status != http.StatusBadRequest || got.body["code"] != "bad_request" || got.body["status"] != float64(http.StatusBadRequest) {
				t.Fatalf("GET /api/parts?%s = %d %v, want a 400 bad_request", tc.query, got.status, got.body)
			}
			errors, _ := got.body["errors"].(map[string]any)
			var named []string
			for field, reasons := range errors {
				if list, _ := reasons.([]any); len(list) == 0 {
					t.Errorf("errors[%s] holds no reason: %v", field, reasons)
				}
				named = append(named, field)
			}
			slices.Sort(named)
			if !reflect.DeepEqual(named, tc.fields) {
				t.Errorf("GET /api/parts?%s: errors name %v, want %v: %v", tc.query, named, tc.fields, got.body)
			}
		})
	}
	if len(impl.filters) != before {
		t.Errorf("a refused value reached the implementation: %d calls", len(impl.filters)-before)
	}
}
