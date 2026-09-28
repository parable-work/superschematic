package shop_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	orm "example.com/acme/orm/shop-db"
	sdk "example.com/acme/sdk/go/shop-orders"
	"example.com/acme/shop"
	types "example.com/acme/types/go/shop-orders"
	scalars "github.com/parable-work/superscalar/go"
	"github.com/parable-work/superschematic/runtime/http/go/session"
	"go.uber.org/zap"
)

// grants gives every principal one role holding these permissions.
type grants []string

func (g grants) ListRolesForPrincipal(context.Context, string) ([]session.Role, error) {
	return []session.Role{{ID: "role", Name: "Role", Permissions: g}}, nil
}

// ordersServer serves shop-orders over the no-op database, with every
// signed-in caller holding the given permissions.
func ordersServer(t *testing.T, permissions ...string) *httptest.Server {
	t.Helper()
	handler, err := shop.NewOrdersHandler(orm.NewNoOpDatabase(), zap.NewNop(), shop.Auth{
		Validate:   verifyJWT,
		Sessions:   fakeSessions{},
		Principals: fakePrincipals{},
		Roles:      grants(permissions),
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func ordersClient(t *testing.T, server *httptest.Server, token string) *sdk.ShopOrdersSDK {
	t.Helper()
	config := sdk.SDKConfig{BaseURL: server.URL}
	if token != "" {
		config.Auth = &sdk.AuthConfig{Token: token}
	}
	client, err := sdk.New(config)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

const productID = "00000000-0000-4000-8000-0000000000b1"

var address = types.ShippingAddress{
	Recipient: "Ada Lovelace",
	Line1:     "12 St James's Square",
	City:      "London",
	Postcode:  "SW1Y 4JH",
	Country:   "GB",
}

// A @publicRoute answers without a token; an @auth route in the same set
// does not.
func TestPublicAndAuthRoutes(t *testing.T) {
	server := ordersServer(t)
	ctx := context.Background()

	reviews, err := ordersClient(t, server, "").ProductReviewsNamespace.ListReviews(ctx, productID, nil)
	if err != nil || len(reviews) != 0 {
		t.Fatalf("ListReviews without a token: %v, %v", reviews, err)
	}

	input := types.WriteReviewInput{Rating: 5, Title: "Lovely", Body: "Smoky and smooth."}
	_, err = ordersClient(t, server, "").ProductReviewsNamespace.WriteReview(ctx, productID, input)
	var unauthenticated *sdk.AuthenticationError
	if !errors.As(err, &unauthenticated) {
		t.Fatalf("WriteReview without a token: %v", err)
	}

	// @auth needs a caller and no permission. The no-op database returns
	// what it was given.
	review, err := ordersClient(t, server, "token-1").ProductReviewsNamespace.WriteReview(ctx, productID, input)
	if err != nil || review.Rating != 5 {
		t.Fatalf("WriteReview with a token: %+v, %v", review, err)
	}
}

// A caller needs a permission that covers the route's: orders.read lets a
// caller read orders and not place them, and orders covers both.
func TestPermissions(t *testing.T) {
	ctx := context.Background()
	order := types.PlaceOrderInput{
		Lines:           []types.PlaceOrderLine{{ProductId: mustUUID(t, productID), Quantity: 2}},
		ShippingAddress: address,
	}

	reader := ordersClient(t, ordersServer(t, "orders.read"), "token-1")
	if _, err := reader.OrderNamespace.ListOrders(ctx, nil); err != nil {
		t.Fatalf("ListOrders with orders.read: %v", err)
	}
	_, err := reader.OrderNamespace.PlaceOrder(ctx, order)
	var forbidden *sdk.AuthorizationError
	if !errors.As(err, &forbidden) {
		t.Fatalf("PlaceOrder with orders.read: %v", err)
	}

	// With orders the request reaches PlaceOrder, which finds no product in
	// the no-op database and answers 400 with its own message.
	writer := ordersClient(t, ordersServer(t, "orders"), "token-1")
	_, err = writer.OrderNamespace.PlaceOrder(ctx, order)
	var apiErr *sdk.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest || !strings.Contains(apiErr.Message, "no product has id") {
		t.Fatalf("PlaceOrder of a missing product: %v", err)
	}

	_, err = writer.OrderNamespace.GetOrder(ctx, "00000000-0000-4000-8000-0000000000c1")
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
		t.Fatalf("GetOrder of a missing order: %v", err)
	}
}

// Both sides check an input against the schema: the SDK before it sends,
// the server when it receives.
func TestValidation(t *testing.T) {
	server := ordersServer(t, "orders")
	empty := types.PlaceOrderInput{Lines: []types.PlaceOrderLine{}, ShippingAddress: address}

	_, err := ordersClient(t, server, "token-1").OrderNamespace.PlaceOrder(context.Background(), empty)
	var invalid *sdk.ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("the SDK sent an order with no lines: %v", err)
	}

	status := post(t, server.URL+"/api/orders", `{"lines": [], "shippingAddress": {"recipient": "Ada", "line1": "1", "city": "London", "postcode": "N1", "country": "gb"}}`)
	if status != http.StatusBadRequest {
		t.Fatalf("POST /api/orders with no lines and a lowercase country answered %d", status)
	}
}

// @bodyLimit refuses a body over its size before it is read, and
// @rateLimit refuses a caller's requests past the limit for the minute.
func TestTrafficControls(t *testing.T) {
	server := ordersServer(t, "orders")

	if status := post(t, server.URL+"/api/orders", strings.Repeat(" ", 1<<20+1)); status != http.StatusRequestEntityTooLarge {
		t.Fatalf("a body over 1 MB answered %d", status)
	}

	url := server.URL + "/api/orders/00000000-0000-4000-8000-0000000000c1/cancel"
	for i := 1; i <= 60; i++ {
		if status := post(t, url, `{}`); status != http.StatusNotFound {
			t.Fatalf("request %d answered %d", i, status)
		}
	}
	if status := post(t, url, `{}`); status != http.StatusTooManyRequests {
		t.Fatalf("request 61 in a minute answered %d", status)
	}
}

func mustUUID(t *testing.T, s string) types.IdentityUUID {
	t.Helper()
	id, err := scalars.ParseUUID(s)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func post(t *testing.T, url, body string) int {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer token-1")
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	return response.StatusCode
}
