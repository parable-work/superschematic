package shop_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	shoporders "example.com/acme/api/shop-orders"
	orm "example.com/acme/orm/shop-db"
	sdk "example.com/acme/sdk/go/shop-orders"
	"example.com/acme/sdk/go/shop-orders/namespaces"
	"example.com/acme/sdk/go/shop-orders/runtime"
	shopordersimpl "example.com/acme/shop/shop-orders"
	db "example.com/acme/types/go/shop-db"
	types "example.com/acme/types/go/shop-orders"
	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	scalars "github.com/parable-work/superscalar/go"
	"github.com/parable-work/superschematic/runtime/http/go/identity"
	"go.uber.org/zap"
)

// shopper is a user of the shop who places orders and writes reviews.
const (
	shopperLogin    = "grace@example.com"
	shopperPassword = "grace's password"
)

// shopperPermissions are a shopper's: to place orders, and to read and
// cancel their own. staffPermissions add orders.any, every customer's.
var (
	shopperPermissions = []string{"orders.read", "orders.write"}
	staffPermissions   = []string{"orders.read", "orders.write", "orders.any"}
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
	return serveOrders(t, u, orm.NewNoOpDatabase()), token
}

// serveOrders serves shop-orders in-process over database, its callers
// u's users.
func serveOrders(t *testing.T, u *users, database orm.DatabaseInterface) *httptest.Server {
	t.Helper()
	deps := shoporders.Deps{DB: database, Logger: zap.NewNop()}
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
// does not. A session shop-api's sign-in started authenticates the
// shopper on shop-orders, which reads the same users.
func TestPublicAndAuthRoutes(t *testing.T) {
	server, token := ordersServer(t)
	ctx := context.Background()

	reviews, err := ordersClient(t, server, "").ProductReviewsNamespace.ListReviews(ctx, productID, nil, runtime.PageRequest{})
	if err != nil || len(reviews.Items) != 0 || reviews.Next != nil {
		t.Fatalf("ListReviews without a token: %+v, %v", reviews, err)
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
	if _, err := reader.OrderNamespace.ListOrders(ctx, nil, runtime.PageRequest{}); err != nil {
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

// A shopper gets and cancels their own orders alone: another customer's
// order is the 404 of one no one placed, to get and to cancel, and stays as
// it was. Staff, who hold orders.any, get and cancel any. The orders are
// rows of the test's own database; the stack's test lists and pages them
// as each caller sees them, over Postgres (TestStackDevRunsTheShop).
func TestAShopperReadsAndCancelsOnlyTheirOwnOrders(t *testing.T) {
	ctx := context.Background()
	u := newUsers(t)
	grace := u.add(t, shopperLogin, "Grace Hopper", shopperPassword, shopperPermissions...)
	alan := u.add(t, "alan@example.com", "Alan Turing", "alan's password", shopperPermissions...)
	u.add(t, staffLogin, "Ada Lovelace", staffPassword, staffPermissions...)
	orders := newOrderRows(t, grace, alan)
	server := serveOrders(t, u, orders)
	accounts := productsServer(t, u)
	as := func(login, password string) *sdk.ShopOrdersSDK {
		return ordersClient(t, server, signIn(t, productsClient(t, accounts), login, password))
	}
	shoppers := map[string]*sdk.ShopOrdersSDK{
		"grace": as(shopperLogin, shopperPassword),
		"alan":  as("alan@example.com", "alan's password"),
	}
	staff := as(staffLogin, staffPassword)
	notMine, outOfStock := "not mine", "out of stock"
	notFound := func(what string, err error) {
		t.Helper()
		var apiErr *sdk.APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
			t.Fatalf("%s = %v, want 404", what, err)
		}
	}

	for who, client := range shoppers {
		own, other := orders.of["grace"], orders.of["alan"]
		if who == "alan" {
			own, other = other, own
		}
		if got, err := client.OrderNamespace.GetOrder(ctx, own.String()); err != nil || got.Id != own {
			t.Fatalf("%s's GetOrder of their order = %+v, %v", who, got, err)
		}
		_, err := client.OrderNamespace.GetOrder(ctx, other.String())
		notFound(who+"'s GetOrder of the other's order", err)
		_, err = client.OrderNamespace.CancelOrder(ctx, other.String(), namespaces.OrderCancelOrderInput{Reason: &notMine})
		notFound(who+"'s CancelOrder of the other's order", err)
	}
	for _, id := range orders.of {
		if status := orders.status(id); status != db.OrderStatus_Placed {
			t.Fatalf("after the shoppers' refused cancels, order %s is %s, want placed", id, status)
		}
	}

	for who, id := range orders.of {
		if got, err := staff.OrderNamespace.GetOrder(ctx, id.String()); err != nil || got.Id != id {
			t.Fatalf("staff's GetOrder of %s's order = %+v, %v", who, got, err)
		}
		cancelled, err := staff.OrderNamespace.CancelOrder(ctx, id.String(), namespaces.OrderCancelOrderInput{Reason: &outOfStock})
		if err != nil || cancelled.Status != types.OrderStatus_Cancelled || cancelled.CancelReason != outOfStock {
			t.Fatalf("staff's CancelOrder of %s's order = %+v, %v", who, cancelled, err)
		}
	}
}

// orderRows is the no-op database with an order of each customer's: their
// ids are of, by the customer's first name, lowercase. GetOne reads one and
// UpdateOne writes its status and its reason; every other call is the
// no-op database's.
type orderRows struct {
	*orm.NoOpDatabase
	repository *orderRepository
	of         map[string]db.IdentityUUID
}

func newOrderRows(t *testing.T, customers ...identity.User) *orderRows {
	t.Helper()
	rows := &orderRows{
		NoOpDatabase: orm.NewNoOpDatabase(),
		repository:   &orderRepository{rows: map[db.IdentityUUID]db.Order{}},
		of:           map[string]db.IdentityUUID{},
	}
	for _, customer := range customers {
		customerID := mustUUID(t, customer.ID)
		id := scalars.NewUUID()
		rows.repository.rows[id] = db.Order{
			Id:              &id,
			Customer:        db.User{Id: &customerID},
			Status:          db.OrderStatus_Placed,
			PlacedAt:        db.TemporalDateTime(time.Now()),
			ShippingAddress: address,
		}
		rows.of[strings.ToLower(strings.Fields(customer.Name)[0])] = id
	}
	return rows
}

func (rows *orderRows) GetOrderRepository() orm.OrderRepositoryInterface { return rows.repository }

func (rows *orderRows) status(id db.IdentityUUID) db.OrderStatus {
	order, _ := rows.repository.GetOne(context.Background(), id, nil)
	return order.Status
}

type orderRepository struct {
	orm.NoOpOrderRepository
	mu   sync.Mutex
	rows map[db.IdentityUUID]db.Order
}

func (r *orderRepository) GetOne(_ context.Context, id db.IdentityUUID, _ *orm.OrderGetOptions) (*db.Order, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	order, ok := r.rows[id]
	if !ok {
		return nil, orm.ErrNotFound
	}
	return &order, nil
}

func (r *orderRepository) UpdateOne(_ context.Context, id db.IdentityUUID, update *orm.OrderUpdate) (*db.Order, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	order, ok := r.rows[id]
	if !ok {
		return nil, orm.ErrNotFound
	}
	if update.Status != nil {
		order.Status = *update.Status
	}
	if update.CancelReason != nil {
		order.CancelReason = *update.CancelReason
	}
	r.rows[id] = order
	return &order, nil
}

// Both sides check an input against the schema: the SDK before it sends,
// the server when it receives.
func TestValidation(t *testing.T) {
	server, token := ordersServer(t, shopperPermissions...)
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

// A paged list answers its page as data, {items, next}, next null after
// the last page, and the router refuses a limit past the list's most, or
// a cursor outside its alphabet, before the implementation runs. The
// implementation refuses a cursor that decodes but names no order, and a
// search longer than its rules allow is refused like any query parameter.
// The no-op database holds no rows, so every page is empty and the last;
// the stack's test pages through Postgres (TestStackDevRunsTheShop).
func TestPagesAndSearch(t *testing.T) {
	server, token := ordersServer(t, shopperPermissions...)
	page := func(path string) (int, string) {
		t.Helper()
		status, body := call(t, http.MethodGet, server.URL+path, token, "")
		var answer struct {
			Data   json.RawMessage `json:"data"`
			Detail string          `json:"detail"`
		}
		if err := json.Unmarshal(body, &answer); err != nil {
			t.Fatalf("GET %s: %d %s", path, status, body)
		}
		if status == http.StatusOK {
			return status, string(answer.Data)
		}
		return status, answer.Detail
	}
	for _, path := range []string{"/api/orders", "/api/orders?statuses=placed&limit=100", "/api/products", "/api/products/" + productID + "/reviews?q=smoky+SMOOTH&minRating=4"} {
		if status, data := page(path); status != http.StatusOK || data != `{"items":[],"next":null}` {
			t.Errorf("GET %s answered %d %s, want an empty last page", path, status, data)
		}
	}
	for path, detail := range map[string]string{
		"/api/orders?limit=101":                            "Invalid query parameter limit: must be at most 100",
		"/api/orders?limit=0":                              "Invalid query parameter limit: must be at least 1",
		"/api/orders?cursor=a%3D":                          "Invalid query parameter cursor: does not match the required pattern",
		"/api/orders?cursor=e30":                           "cursor is not one this operation returned",
		"/api/products?cursor=bnVsbA":                      "cursor is not one this operation returned",
		"/api/products/" + productID + "/reviews?limit=51": "Invalid query parameter limit: must be at most 50",
	} {
		if status, got := page(path); status != http.StatusBadRequest || got != detail {
			t.Errorf("GET %s answered %d %q, want 400 %q", path, status, got, detail)
		}
	}
	if status, _ := page("/api/products/" + productID + "/reviews?q=" + strings.Repeat("a", 101)); status != http.StatusBadRequest {
		t.Errorf("a search of 101 characters answered %d, want 400", status)
	}

	// The SDK sends the lookup's ids as one list; the catalog is public.
	products, err := ordersClient(t, server, "").ProductCatalogNamespace.ListProducts(context.Background(),
		&namespaces.ProductCatalogListProductsQueryParams{Ids: []types.IdentityUUID{mustUUID(t, productID)}}, runtime.PageRequest{Limit: 100})
	if err != nil || len(products.Items) != 0 || products.Next != nil {
		t.Fatalf("ListProducts by id: %+v, %v", products, err)
	}
}

// @bodyLimit refuses a body over its size before it is read, and
// @rateLimit refuses a caller's requests past the limit for the minute.
func TestTrafficControls(t *testing.T) {
	server, token := ordersServer(t, shopperPermissions...)

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
