// This file runs inside the generated API module of fixture-webhooks-api
// (TestWebhookRoutesDropUndeclaredKeys copies it there). A @webhook's route
// reads only the keys its input type declares, spelled as declared, at the
// top level and in a nested @allowUnknownFields type, and checks them; a
// @strictJSON input refuses a key it does not declare (D87). The
// implementation records each input it receives.
package fixturewebhooksapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	api "example.com/schemas/api/fixture-webhooks-api"
	types "example.com/schemas/types/go/fixture-webhooks-api"
)

type events struct {
	received []any
}

func (e *events) GetEvent(_ context.Context, id string) (*types.EventReceipt, error) {
	return &types.EventReceipt{Id: id, Received: true}, nil
}

func (e *events) ReceiveStripeEvent(_ context.Context, input *types.PaymentEvent) (*types.EventReceipt, error) {
	e.received = append(e.received, input)
	return &types.EventReceipt{Id: input.Id, Received: true}, nil
}

func (e *events) ReceiveGithubEvent(_ context.Context, id string, _ string) (*types.EventReceipt, error) {
	return &types.EventReceipt{Id: id, Received: true}, nil
}

func (e *events) ReceiveRawGithubEvent(_ context.Context, _ string) (*bool, error) {
	received := true
	return &received, nil
}

func (e *events) ReceivePlaidEvent(_ context.Context, input *types.PlaidItemEvent) (*types.EventReceipt, error) {
	e.received = append(e.received, input)
	return &types.EventReceipt{Id: input.ItemId, Received: true}, nil
}

func (e *events) ReceiveLedgerEvent(_ context.Context, input *types.LedgerEvent) (*types.EventReceipt, error) {
	e.received = append(e.received, input)
	return &types.EventReceipt{Id: input.Id, Received: true}, nil
}

func serve(t *testing.T) (*httptest.Server, *events) {
	t.Helper()
	impl := &events{}
	pass := func(next http.Handler) http.Handler { return next }
	router := chi.NewRouter()
	if err := api.RegisterRoutes(router, api.Config{
		Logger: zap.NewNop(),
		Implementations: api.Implementations{
			Event:            impl,
			Webhook:          impl,
			WebhookVerifiers: map[string]api.WebhookVerifier{"stripe": pass, "github": pass},
		},
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return server, impl
}

// post sends body to path and returns the status and the decoded
// response object.
func post(t *testing.T, server *httptest.Server, path, body string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Post(server.URL+path, "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("%s: response is not a JSON object: %s", body, raw)
	}
	return resp.StatusCode, decoded
}

// jsonOf is v's JSON as a generic value, for comparing what an
// implementation received.
func jsonOf(t *testing.T, v any) any {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAWebhookInputDropsTheKeysItsTypeDoesNotDeclare(t *testing.T) {
	server, impl := serve(t)
	// Item_Id and Error_Code differ from declared keys in case alone, which
	// encoding/json would read into the fields; the route drops them.
	status, response := post(t, server, "/api/webhooks/plaid",
		`{"webhook_type":"TRANSACTIONS","webhook_code":"SYNC_UPDATES_AVAILABLE","item_id":"item-1","environment":"sandbox","Item_Id":"another",`+
			`"error":{"error_code":"ITEM_LOGIN_REQUIRED","error_message":"the login changed","Error_Code":"OTHER"}}`)
	if status != http.StatusOK {
		t.Fatalf("status %d: %v", status, response)
	}
	if len(impl.received) != 1 {
		t.Fatalf("received %d inputs, want 1", len(impl.received))
	}
	want := map[string]any{
		"webhook_type": "TRANSACTIONS", "webhook_code": "SYNC_UPDATES_AVAILABLE", "item_id": "item-1",
		"error": map[string]any{"error_code": "ITEM_LOGIN_REQUIRED"},
	}
	if got := jsonOf(t, impl.received[0]); !reflect.DeepEqual(got, want) {
		t.Fatalf("received %v, want %v", got, want)
	}

	status, response = post(t, server, "/api/webhooks/plaid", `{"webhook_type":"TRANSACTIONS","webhook_code":7,"item_id":"item-1","environment":"sandbox"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("a mistyped declared key: status %d: %v", status, response)
	}
	status, response = post(t, server, "/api/webhooks/plaid", `["item-1"]`)
	details, _ := response["details"].(map[string]any)
	if status != http.StatusBadRequest || details["reason"] != "expected an object" {
		t.Fatalf("a body that is not an object: status %d: %v", status, response)
	}
	if len(impl.received) != 1 {
		t.Fatalf("a refused input reached the implementation: %v", impl.received)
	}

	// A @webhook whose type is neither @strictJSON nor @allowUnknownFields
	// drops them too.
	status, response = post(t, server, "/api/webhooks/stripe", `{"id":"evt_1","type":"invoice.paid","livemode":false}`)
	if status != http.StatusOK {
		t.Fatalf("stripe: status %d: %v", status, response)
	}
	if got, want := jsonOf(t, impl.received[1]), map[string]any{"id": "evt_1", "type": "invoice.paid"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stripe: received %v, want %v", got, want)
	}
}

func TestAStrictJSONWebhookInputRefusesAKeyItsTypeDoesNotDeclare(t *testing.T) {
	server, impl := serve(t)
	status, response := post(t, server, "/api/webhooks/ledger", `{"id":"ledger-1","environment":"sandbox"}`)
	details, _ := response["details"].(map[string]any)
	if status != http.StatusBadRequest || details["reason"] != "unknown fields: environment" {
		t.Fatalf("status %d: %v", status, response)
	}
	if status, response := post(t, server, "/api/webhooks/ledger", `{"id":"ledger-1"}`); status != http.StatusOK {
		t.Fatalf("a declared body: status %d: %v", status, response)
	}
	if len(impl.received) != 1 {
		t.Fatalf("received %d inputs, want 1", len(impl.received))
	}
}

// TestALenientTypeDropsUndeclaredKeysWhereverItIsDecoded holds the type's
// own decoders to the route's reading: PlaidError is @allowUnknownFields,
// so FromJSON, a strict decoder, drops a key it does not declare and one
// that differs in case.
func TestALenientTypeDropsUndeclaredKeysWhereverItIsDecoded(t *testing.T) {
	decoded, err := types.PlaidErrorFromJSON([]byte(`{"error_code":"A","Error_Code":"B","error_message":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := jsonOf(t, decoded), map[string]any{"error_code": "A"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded %v, want %v", got, want)
	}
}
