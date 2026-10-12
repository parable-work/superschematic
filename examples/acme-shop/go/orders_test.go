package shop_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	shoporders "example.com/acme/api/shop-orders"
	orm "example.com/acme/orm/shop-db"
	sdk "example.com/acme/sdk/go/shop-orders"
	shopordersimpl "example.com/acme/shop/shop-orders"
	types "example.com/acme/types/go/shop-orders"
	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	scalars "github.com/parable-work/superscalar/go"
	"go.uber.org/zap"
)

// shopper is a user of the shop who places orders and writes reviews.
const (
	shopperLogin    = "grace@example.com"
	shopperPassword = "grace's password"
)

// ordersServer serves shop-orders in-process over the no-op database, as
// productsServer serves shop-api, beside a shop-api over the same users,
// whose sign-in starts the sessions shop-orders authenticates. It signs the
// shopper in, holding the given permissions, and returns shop-orders and
// the shopper's bearer token.
func ordersServer(t *testing.T, permissions ...string) (*httptest.Server, string) {
	t.Helper()
	u := newUsers(t)
	u.add(t, shopperLogin, "Grace Hopper", shopperPassword, permissions...)
	token := signIn(t, productsClient(t, productsServer(t, u)), shopperLogin, shopperPassword)

	deps := shoporders.Deps{DB: orm.NewNoOpDatabase(), Logger: zap.NewNop()}
	implementations, err := shopordersimpl.New(deps)
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Use(chimiddleware.RequestID)
	err = shoporders.RegisterRoutes(router, shoporders.Config{
		DB:              deps.DB,
		Logger:          deps.Logger,
		Identity:        u.shopOrders(t),
		Implementations: implementations,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return server, token
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
// does not. A session shop-api's sign-in started authenticates the
// shopper on shop-orders, which reads the same users.
func TestPublicAndAuthRoutes(t *testing.T) {
	server, token := ordersServer(t)
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
	review, err := ordersClient(t, server, token).ProductReviewsNamespace.WriteReview(ctx, productID, input)
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

	readerServer, readerToken := ordersServer(t, "orders.read")
	reader := ordersClient(t, readerServer, readerToken)
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
	writerServer, writerToken := ordersServer(t, "orders")
	writer := ordersClient(t, writerServer, writerToken)
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
	server, token := ordersServer(t, "orders")
	empty := types.PlaceOrderInput{Lines: []types.PlaceOrderLine{}, ShippingAddress: address}

	_, err := ordersClient(t, server, token).OrderNamespace.PlaceOrder(context.Background(), empty)
	var invalid *sdk.ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("the SDK sent an order with no lines: %v", err)
	}

	status := post(t, server.URL+"/api/orders", token, `{"lines": [], "shippingAddress": {"recipient": "Ada", "line1": "1", "city": "London", "postcode": "N1", "country": "gb"}}`)
	if status != http.StatusBadRequest {
		t.Fatalf("POST /api/orders with no lines and a lowercase country answered %d", status)
	}
}

// @bodyLimit refuses a body over its size before it is read, and
// @rateLimit refuses a caller's requests past the limit for the minute.
func TestTrafficControls(t *testing.T) {
	server, token := ordersServer(t, "orders")

	if status := post(t, server.URL+"/api/orders", token, strings.Repeat(" ", 1<<20+1)); status != http.StatusRequestEntityTooLarge {
		t.Fatalf("a body over 1 MB answered %d", status)
	}

	url := server.URL + "/api/orders/00000000-0000-4000-8000-0000000000c1/cancel"
	for i := 1; i <= 60; i++ {
		if status := post(t, url, token, `{}`); status != http.StatusNotFound {
			t.Fatalf("request %d answered %d", i, status)
		}
	}
	if status := post(t, url, token, `{}`); status != http.StatusTooManyRequests {
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

func post(t *testing.T, url, token, body string) int {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	return response.StatusCode
}
