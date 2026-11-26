package gosdkgen

import (
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/sdkgen/sdktest"
)

// TestIdempotentOperationsSendOneKeyACall runs idempotencySDKTest in the
// generated SDK of fixture-idempotent-api (D140): a method of an
// @idempotent operation sends an Idempotency-Key, a UUID it makes for each
// call, the same on each send of the call, or the key the context names
// (runtime.WithIdempotencyKey); a method of any other operation sends none.
func TestIdempotentOperationsSendOneKeyACall(t *testing.T) {
	schema, table, err := sdktest.LoadIdempotentService()
	if err != nil {
		t.Fatalf("load %s: %v", sdktest.IdempotentService, err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:         sessionauth.Provider{},
		SchemaName:       sdktest.IdempotentService,
		ModulePath:       "example.com/schemas/api/" + sdktest.IdempotentService,
		TypesModule:      "example.com/schemas/types/go/" + sdktest.IdempotentService,
		Clock:            nestedArraysClock,
		IdempotencyTable: table,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	runInSDK(t, schema, apiOutput, sdktest.IdempotentService, "idempotency_test.go", idempotencySDKTest)
}

// idempotencySDKTest runs in the generated SDK module against an httptest
// server that records each request's Idempotency-Key, and answers the
// first send of an order of "busy" 429, so the client sends it again.
const idempotencySDKTest = `package sdk_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	sdk "example.com/schemas/sdk/go/fixture-idempotent-api"
	"example.com/schemas/sdk/go/fixture-idempotent-api/runtime"
	types "example.com/schemas/types/go/fixture-idempotent-api"
)

var uuidV4 = regexp.MustCompile("^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$")

func TestIdempotencyKey(t *testing.T) {
	type sent struct {
		method string
		keys   []string
	}
	var seen []sent
	busy := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, sent{r.Method, r.Header.Values("Idempotency-Key")})
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), "busy") && busy == 0 {
			busy++
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, "{\"status\":429,\"detail\":\"slow down\"}")
			return
		}
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, "{\"data\":[],\"meta\":{\"requestId\":\"req-1\"}}")
			return
		}
		_, _ = io.WriteString(w, "{\"data\":{\"number\":1,\"sku\":\"tea\",\"quantity\":2,\"cancelled\":false},\"meta\":{\"requestId\":\"req-1\"}}")
	}))
	t.Cleanup(server.Close)

	client, err := sdk.New(sdk.SDKConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tea := types.PlaceOrderInput{Sku: "tea", Quantity: 2}

	// Each call makes its own UUID.
	for range 2 {
		if _, err := client.OrdersNamespace.PlaceOrder(ctx, tea); err != nil {
			t.Fatal(err)
		}
	}
	// The key the context names, on a POST and on a DELETE.
	named := runtime.WithIdempotencyKey(ctx, "order-7")
	if _, err := client.OrdersNamespace.PlaceOrder(named, tea); err != nil {
		t.Fatal(err)
	}
	if _, err := client.OrdersNamespace.CancelOrder(named, 1); err != nil {
		t.Fatal(err)
	}
	// An operation that is not @idempotent sends none, with or without one.
	if _, err := client.OrdersNamespace.ListOrders(named); err != nil {
		t.Fatal(err)
	}
	// The send after a 429 carries the key of the first.
	if _, err := client.OrdersNamespace.PlaceOrder(ctx, types.PlaceOrderInput{Sku: "busy", Quantity: 1}); err != nil {
		t.Fatal(err)
	}

	if len(seen) != 7 {
		t.Fatalf("%d requests, want 7: %+v", len(seen), seen)
	}
	for i, request := range seen {
		if want := 1; i == 4 {
			if len(request.keys) != 0 {
				t.Errorf("request %d, a %s of an operation that is not @idempotent, sent %v", i, request.method, request.keys)
			}
		} else if len(request.keys) != want {
			t.Errorf("request %d sent %d keys: %v", i, len(request.keys), request.keys)
		}
	}
	if t.Failed() {
		t.FailNow()
	}
	first, second := seen[0].keys[0], seen[1].keys[0]
	if !uuidV4.MatchString(first) || !uuidV4.MatchString(second) || first == second {
		t.Errorf("two calls sent %q and %q, want a UUID each", first, second)
	}
	if seen[2].keys[0] != "order-7" || seen[3].keys[0] != "order-7" || seen[3].method != http.MethodDelete {
		t.Errorf("the named key: %+v, %+v", seen[2], seen[3])
	}
	if retried := seen[6].keys[0]; !uuidV4.MatchString(retried) || retried != seen[5].keys[0] || retried == first {
		t.Errorf("the send after a 429 carried %q, the first %q", retried, seen[5].keys[0])
	}
}
`
