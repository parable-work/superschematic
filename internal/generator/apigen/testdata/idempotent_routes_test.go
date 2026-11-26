// This file runs inside the generated API module of fixture-idempotent-api
// (TestIdempotentRoutes copies it there, beside idempotency.sql, the SQLite
// DDL of the table its database declares for the keys). The implementation
// counts its runs, so a test can check a replayed request never reached it
// (D140).
package fixtureidempotentapi_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
	_ "modernc.org/sqlite"

	api "example.com/schemas/api/fixture-idempotent-api"
	types "example.com/schemas/types/go/fixture-idempotent-api"
	"github.com/parable-work/superschematic/runtime/http/go/idempotency"
)

type orders struct {
	placed []*types.Order
	runs   int
}

func (o *orders) PlaceOrder(_ context.Context, input *types.PlaceOrderInput) (*types.Order, error) {
	o.runs++
	switch input.Sku {
	case "broken":
		return nil, fmt.Errorf("the order broke")
	case "gone":
		return nil, api.ConflictError("Out of stock", nil)
	}
	order := &types.Order{Number: float64(len(o.placed) + 1), Sku: input.Sku, Quantity: input.Quantity}
	o.placed = append(o.placed, order)
	return order, nil
}

func (o *orders) CancelOrder(_ context.Context, number float64) (*types.Order, error) {
	o.runs++
	if number < 1 || int(number) > len(o.placed) {
		return nil, api.NotFoundError("Order", nil)
	}
	o.placed[int(number)-1].Cancelled = true
	return o.placed[int(number)-1], nil
}

func (o *orders) ListOrders(context.Context) ([]types.Order, error) {
	out := []types.Order{}
	for _, order := range o.placed {
		out = append(out, *order)
	}
	return out, nil
}

func serve(t *testing.T) (*httptest.Server, *orders, *sql.DB) {
	t.Helper()
	ddl, err := os.ReadFile("idempotency.sql")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "keys.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(string(ddl)); err != nil {
		t.Fatalf("apply the generated DDL: %v\n%s", err, ddl)
	}
	impl := &orders{}
	router := chi.NewRouter()
	config := api.Config{
		Logger:           zap.NewNop(),
		IdempotencyStore: idempotency.NewSQLStore(db, idempotency.SQLite, api.IdempotencyTable),
		Implementations:  api.Implementations{Orders: impl},
	}
	if err := api.RegisterRoutes(router, config); err != nil {
		t.Fatal(err)
	}
	config.IdempotencyStore = nil
	if err := api.RegisterRoutes(chi.NewRouter(), config); err == nil || !strings.Contains(err.Error(), "IdempotencyStore") {
		t.Errorf("RegisterRoutes with @idempotent routes and no IdempotencyStore: %v", err)
	}
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return server, impl, db
}

// call sends a request with the key, when it is set, and decodes its JSON.
func call(t *testing.T, server *httptest.Server, method, path, body, key string) (*http.Response, map[string]any, string) {
	t.Helper()
	request, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		request.Header.Set(idempotency.KeyHeader, key)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("response is not JSON: %s", raw)
	}
	return response, decoded, string(raw)
}

func TestAKeyRunsTheOperationOnce(t *testing.T) {
	server, impl, db := serve(t)
	tea := `{"sku":"tea","quantity":2}`

	first, _, firstBody := call(t, server, "POST", "/api/orders", tea, "order-1")
	if first.StatusCode != http.StatusOK || first.Header.Get(idempotency.ReplayHeader) != "" || impl.runs != 1 {
		t.Fatalf("the first order: %d %s, %d runs", first.StatusCode, firstBody, impl.runs)
	}
	again, _, againBody := call(t, server, "POST", "/api/orders", tea, "order-1")
	if again.StatusCode != http.StatusOK || again.Header.Get(idempotency.ReplayHeader) != "true" || againBody != firstBody || impl.runs != 1 || len(impl.placed) != 1 {
		t.Fatalf("the repeat: %d %v %s, %d runs", again.StatusCode, again.Header, againBody, impl.runs)
	}

	// The key with another order is refused, and nothing runs.
	other, problem, _ := call(t, server, "POST", "/api/orders", `{"sku":"coffee","quantity":1}`, "order-1")
	if other.StatusCode != http.StatusUnprocessableEntity || problem["code"] != idempotency.CodeReused || impl.runs != 1 {
		t.Errorf("another order under the key: %d %v, %d runs", other.StatusCode, problem, impl.runs)
	}
	// Another key runs again, and so does a request with none.
	if second, _, _ := call(t, server, "POST", "/api/orders", tea, "order-2"); second.StatusCode != http.StatusOK || second.Header.Get(idempotency.ReplayHeader) != "" {
		t.Errorf("another key: %d", second.StatusCode)
	}
	call(t, server, "POST", "/api/orders", tea, "")
	call(t, server, "POST", "/api/orders", tea, "")
	if impl.runs != 4 || len(impl.placed) != 4 {
		t.Errorf("%d runs and %d orders, want 4 of each", impl.runs, len(impl.placed))
	}

	// A 500 keeps no answer: the retry runs again.
	for range 2 {
		if failed, _, _ := call(t, server, "POST", "/api/orders", `{"sku":"broken","quantity":1}`, "order-3"); failed.StatusCode != http.StatusInternalServerError || failed.Header.Get(idempotency.ReplayHeader) != "" {
			t.Errorf("a failing order: %d %v", failed.StatusCode, failed.Header)
		}
	}
	if impl.runs != 6 {
		t.Errorf("%d runs, want 6: a 500 is run again", impl.runs)
	}
	// A refusal under 500 is kept as any answer.
	for i := range 2 {
		refused, problem, _ := call(t, server, "POST", "/api/orders", `{"sku":"gone","quantity":1}`, "order-4")
		if refused.StatusCode != http.StatusConflict || problem["detail"] != "Out of stock" || (refused.Header.Get(idempotency.ReplayHeader) == "true") != (i == 1) ||
			refused.Header.Get("Content-Type") != "application/problem+json" {
			t.Errorf("a refused order, request %d: %d %v %v", i, refused.StatusCode, problem, refused.Header)
		}
	}
	if impl.runs != 7 {
		t.Errorf("%d runs, want 7: a 409 is not run again", impl.runs)
	}

	// cancelOrder requires a key, and a DELETE has no body to hash.
	if none, problem, _ := call(t, server, "DELETE", "/api/orders/1", "", ""); none.StatusCode != http.StatusBadRequest || problem["code"] != idempotency.CodeRequired || impl.runs != 7 {
		t.Errorf("a cancel without a key: %d %v", none.StatusCode, problem)
	}
	for i := range 2 {
		cancelled, body, _ := call(t, server, "DELETE", "/api/orders/1", "", "cancel-1")
		data, _ := body["data"].(map[string]any)
		if cancelled.StatusCode != http.StatusOK || data["cancelled"] != true || (cancelled.Header.Get(idempotency.ReplayHeader) == "true") != (i == 1) {
			t.Errorf("cancel %d: %d %v", i, cancelled.StatusCode, body)
		}
	}
	// The same key on another order is another request.
	if other, problem, _ := call(t, server, "DELETE", "/api/orders/2", "", "cancel-1"); other.StatusCode != http.StatusUnprocessableEntity || problem["code"] != idempotency.CodeReused {
		t.Errorf("the cancel key on another order: %d %v", other.StatusCode, problem)
	}
	// placeOrder's key order-1 is cancelOrder's own key too: a key is one
	// operation's.
	if own, _, _ := call(t, server, "DELETE", "/api/orders/2", "", "order-1"); own.StatusCode != http.StatusOK || own.Header.Get(idempotency.ReplayHeader) != "" {
		t.Errorf("placeOrder's key on cancelOrder: %d %v", own.StatusCode, own.Header)
	}
	if impl.runs != 9 {
		t.Errorf("%d runs, want 9", impl.runs)
	}

	// The rows are in the generated table, each done with its answer.
	rows, err := db.Query(`SELECT "` + api.IdempotencyTable.Columns.Operation + `", "` + api.IdempotencyTable.Columns.State + `", "` + api.IdempotencyTable.Columns.Status + `" FROM "` + api.IdempotencyTable.Table + `"`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var operation, state string
		var status float64
		if err := rows.Scan(&operation, &state, &status); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s %s %v", operation, state, status))
	}
	sort.Strings(got)
	want := "orders.cancelOrder done 200, orders.cancelOrder done 200, orders.placeOrder done 200, orders.placeOrder done 200, orders.placeOrder done 409"
	if strings.Join(got, ", ") != want {
		t.Errorf("rows = %s, want %s", strings.Join(got, ", "), want)
	}
}
