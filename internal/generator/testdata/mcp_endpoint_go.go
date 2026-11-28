package fixturemcp_test

// The generated Go API of fixture-mcp and its MCP endpoint (#660), which
// TestGoAPIServesItsMCPEndpoint writes beside the API package and runs: the
// scenario the TypeScript and Rust routers are held to, through
// RegisterRoutes' router with the API's own implementation behind it.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	api "example.com/schemas/api/fixture-mcp"
	types "example.com/schemas/types/go/fixture-mcp"
)

const confirmationKey = "kkkkkkkkkkkkkkkkeeeeeeeeeeeeeeeey"

type orders struct{ updates atomic.Int64 }

func (*orders) ListOrders(_ context.Context, statuses []types.OrderStatus, limit *float64) ([]types.Order, error) {
	out := []types.Order{}
	for _, status := range statuses {
		out = append(out, types.Order{Status: status, TotalCents: *limit})
	}
	return out, nil
}

func (*orders) ExportOrders(context.Context) (*string, error) {
	return nil, errors.New("not a tool")
}

func (*orders) DeleteOrder(context.Context, types.IdentityUUID) (*types.Order, error) {
	return nil, errors.New("not a tool")
}

func (*orders) GetOrder(_ context.Context, id types.IdentityUUID) (*types.Order, error) {
	return &types.Order{Id: id, Status: types.OrderStatus_Open, Revision: 3, TotalCents: 1250}, nil
}

func (o *orders) UpdateOrder(_ context.Context, id types.IdentityUUID, input *types.OrderUpdate) (*types.Order, error) {
	o.updates.Add(1)
	return &types.Order{Id: id, Status: types.OrderStatus_Open, Revision: input.Revision + 1}, nil
}

func (*orders) OpenReturn(_ context.Context, input *types.ReturnRequest) (*types.Order, error) {
	return &types.Order{Id: input.OrderId, Status: types.OrderStatus_Shipped}, nil
}

func serve(t *testing.T, key string) (*chi.Mux, *orders, error) {
	impl := &orders{}
	r := chi.NewRouter()
	err := api.RegisterRoutes(r, api.Config{
		Logger:          zap.NewNop(),
		MCP:             api.MCPOptions{ConfirmationKey: []byte(key)},
		Implementations: api.Implementations{Order: impl},
	})
	return r, impl, err
}

func rpc(t *testing.T, r http.Handler, method, body string) (int, map[string]any) {
	t.Helper()
	request := httptest.NewRequest(method, "/api/mcp", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, request)
	text := strings.TrimSuffix(strings.TrimPrefix(w.Body.String(), "event: message\ndata: "), "\n\n")
	var answer map[string]any
	_ = json.Unmarshal([]byte(text), &answer)
	return w.Code, answer
}

func call(t *testing.T, r http.Handler, tool, arguments string) map[string]any {
	t.Helper()
	_, answer := rpc(t, r, http.MethodPost, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+tool+`","arguments":`+arguments+`}}`)
	result, _ := answer["result"].(map[string]any)
	if result == nil {
		t.Fatalf("tools/call %s answered %v", tool, answer)
	}
	return result
}

func structured(result map[string]any) map[string]any {
	out, _ := result["structuredContent"].(map[string]any)
	return out
}

func marshal(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}

const orderID = "4Fd8x2LqYV9jP0aK3mTn7c"

func TestTheEndpointServesTheVisibleToolsThroughTheirRoutes(t *testing.T) {
	r, impl, err := serve(t, confirmationKey)
	if err != nil {
		t.Fatal(err)
	}
	status, initialized := rpc(t, r, http.MethodPost, `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`)
	if status != 200 || marshal(initialized["result"].(map[string]any)["serverInfo"]) != `{"name":"fixture-mcp","version":"1.0.0"}` {
		t.Fatalf("initialize = %d %v", status, initialized)
	}
	_, listed := rpc(t, r, http.MethodPost, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	var names []string
	for _, tool := range listed["result"].(map[string]any)["tools"].([]any) {
		names = append(names, tool.(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "list_orders,get_order,update_order,open_return" {
		t.Errorf("tools/list = %v, want the visible tools alone", names)
	}
	if got := structured(call(t, r, "get_order", `{"id":"`+orderID+`"}`)); got["id"] != orderID || got["totalCents"] != float64(1250) {
		t.Errorf("get_order answered %v", got)
	}
	listedOrders := call(t, r, "list_orders", `{"statuses":["open","shipped"],"limit":5}`)
	if text := listedOrders["content"].([]any)[0].(map[string]any)["text"]; !strings.Contains(text.(string), `"status":"shipped"`) || listedOrders["structuredContent"] != nil {
		t.Errorf("list_orders answered %v", listedOrders)
	}
	if p := structured(call(t, r, "list_orders", `{"statuses":["lost"]}`)); p["status"] != float64(400) {
		t.Errorf("a status the route refuses answered %v", p)
	}
	if p := structured(call(t, r, "get_order", `{"id":"`+orderID+`","extra":1}`)); p["detail"] != `get_order takes id, not "extra"` {
		t.Errorf("an argument get_order does not take answered %v", p)
	}

	// update_order asks: its first call answers a token and runs nothing.
	asked := call(t, r, "update_order", `{"id":"`+orderID+`","revision":3,"note":"gift"}`)
	problem := structured(asked)
	token, _ := problem["details"].(map[string]any)["confirmationToken"].(string)
	if problem["code"] != "confirmation_required" || token == "" || impl.updates.Load() != 0 {
		t.Fatalf("update_order's first call answered %v", asked)
	}
	// A call its route would refuse answers its 400 at once, and the token
	// sent with it is not spent (D98).
	if p := structured(call(t, r, "update_order", `{"id":"`+orderID+`","revision":"three","confirmationToken":"`+token+`"}`)); p["status"] != float64(400) || impl.updates.Load() != 0 {
		t.Errorf("a call the route refuses answered %v", p)
	}
	ran := structured(call(t, r, "update_order", `{"id":"`+orderID+`","revision":3,"note":"gift","confirmationToken":"`+token+`"}`))
	if ran["revision"] != float64(4) || impl.updates.Load() != 1 {
		t.Errorf("update_order with its token answered %v and ran %d times", ran, impl.updates.Load())
	}
	if p := structured(call(t, r, "update_order", `{"id":"`+orderID+`","revision":3,"note":"gift","confirmationToken":"`+token+`"}`)); p["code"] != "confirmation_required" || p["details"].(map[string]any)["refused"] != "used" || impl.updates.Load() != 1 {
		t.Errorf("a used token answered %v", p)
	}
	_, unknown := rpc(t, r, http.MethodPost, `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"delete_order","arguments":{"id":"`+orderID+`"}}}`)
	if marshal(unknown["error"]) != `{"code":-32602,"message":"fixture-mcp has no tool \"delete_order\""}` {
		t.Errorf("a hidden operation's tool answered %v", unknown)
	}
	if status, _ := rpc(t, r, http.MethodGet, ``); status != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/mcp = %d, want 405", status)
	}
}

func TestTheEndpointNeedsItsConfirmationKey(t *testing.T) {
	if _, _, err := serve(t, ""); err == nil || !strings.Contains(err.Error(), "update_order runs only once the person confirms it") {
		t.Errorf("RegisterRoutes with no key: %v", err)
	}
}
