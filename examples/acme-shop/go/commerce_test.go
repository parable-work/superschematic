package shop_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	shoporders "example.com/acme/api/shop-orders"
	orm "example.com/acme/orm/shop-db"
	sdk "example.com/acme/sdk/go/shop-orders"
	stocksdk "example.com/acme/sdk/go/shop-stock"
	"example.com/acme/shop/acmepay"
	shopordersimpl "example.com/acme/shop/shop-orders"
	db "example.com/acme/types/go/shop-db"
	types "example.com/acme/types/go/shop-orders"
	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgconn"
	scalars "github.com/parable-work/superscalar/go"
	engine "github.com/parable-work/superschematic/runtime/engine/go"
	"go.uber.org/zap"
)

// acmepay's keys in the tests: anything will do, as they are the fake's.
const (
	acmepayKey    = "ak_test_shop"
	acmepaySecret = "whsec_test_shop"
	// shopNamespace is the engine namespace shop-db's types live in.
	shopNamespace = "shop-db"
)

// commerceShop is shop-orders served in-process over shop-db's engine,
// open on a SQLite file with the project's engine archive, which runs the
// commerce behaviors (D70), and with the policy shop-stack's generated
// servers open it with; acmepay, the fake provider, posting its webhooks
// to it; a stand-in for shop-stock, which records the sales it is told
// of; and the shop's users.
type commerceShop struct {
	engine  *engine.Engine
	orders  *httptest.Server
	pay     *acmepay.Provider
	payAPI  *acmepay.Client
	rows    *commerceRows
	sold    *soldLines
	deps    shoporders.Deps
	users   *users
	shopper string
	token   string
}

// shopPolicy is the access policy shop-stack's generated server for
// shop-orders opens shop-db's engine with: shop-db's outputs.engine.policy.
func shopPolicy(t *testing.T) engine.Policy {
	t.Helper()
	main, err := os.ReadFile(filepath.Join("..", "schemas", "dist", "server", "shop-stack", "shop-orders", "main.go"))
	if err != nil {
		t.Fatalf("shop-stack's build wrote no server for shop-orders: %v", err)
	}
	policy := regexp.MustCompile("EngineDocument, `(\\{\"rules\".*?)`").FindSubmatch(main)
	if policy == nil {
		t.Fatalf("shop-orders' server opens the engine with no rules policy:\n%s", main)
	}
	return engine.Policy(policy[1])
}

// openShopEngine opens shop-db's engine on a SQLite file of the test's
// own and deploys shop-db's engine types, as a server does when it starts;
// its runner acts as shop-db's engine runner does, holding each engine
// type's name. It skips when the linked archive is the core's, which runs
// no commerce behavior, unless ACME_SHOP_ENGINE is set, as
// scripts/check.sh sets it with the project's archive linked.
func openShopEngine(t *testing.T) (*engine.Background, *engine.Engine) {
	t.Helper()
	var doc struct {
		Namespace  string            `json:"namespace"`
		Tools      json.RawMessage   `json:"tools"`
		Schemas    []json.RawMessage `json:"schemas"`
		MetaSchema json.RawMessage   `json:"metaSchema"`
	}
	if err := json.Unmarshal([]byte(db.EngineDocument), &doc); err != nil {
		t.Fatal(err)
	}
	cfg := engine.Config{
		Path:       filepath.Join(t.TempDir(), "shop.engine.db"),
		Policy:     shopPolicy(t),
		Namespaces: &engine.Namespaces{Names: []string{doc.Namespace}},
		Tools:      doc.Tools,
		MetaSchema: doc.MetaSchema,
		Runner: &engine.Runner{Principal: engine.Principal{
			Subject:     "shop-db-engine-runner",
			Permissions: []string{"Checkout", "CollectionPoint", "Inventory", "Refund"},
		}},
	}
	probe, err := engine.Open(cfg)
	if err != nil {
		t.Fatalf("open shop-db's engine: %v", err)
	}
	has, err := probe.Call(context.Background(), "behaviors.has", nil, "Stock")
	_ = probe.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(has) != "true" {
		if os.Getenv("ACME_SHOP_ENGINE") != "" {
			t.Fatal("the linked engine archive runs no commerce behavior: link shop-db's project archive (scripts/engine-archive.sh --crate schemas/dist/engine/rust/shop-db/ffi)")
		}
		t.Skip("the linked engine archive is the core's, which runs no commerce behavior; scripts/check.sh links shop-db's")
	}
	background := engine.OpenInBackground(cfg, engine.BackgroundOptions{
		Setup: func(ctx context.Context, e *engine.Engine) error {
			_, err := e.Deploy(ctx, doc.Schemas, engine.DeployOptions{Namespace: doc.Namespace})
			return err
		},
	})
	t.Cleanup(func() { _ = background.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	e, err := background.Wait(ctx)
	if err != nil {
		t.Fatalf("shop-db's engine did not open: %v", err)
	}
	return background, e
}

// principal is a caller of the engine: a member of staff through
// /engine/shop-db with their permissions, or one of the shop's services
// in-process, as their implementations call it.
func staffPrincipal(subject string, permissions ...string) *engine.Principal {
	return &engine.Principal{Subject: subject, Permissions: permissions}
}

func servicePrincipal(service, subject string, permissions ...string) *engine.Principal {
	return &engine.Principal{
		Subject: subject, Permissions: permissions,
		Service: &engine.PrincipalService{Deployable: service, Serves: []string{service}, Subject: service},
	}
}

// engineCall makes an engine call and decodes its result.
func engineCall(t *testing.T, e *engine.Engine, out any, op string, principal *engine.Principal, args ...any) {
	t.Helper()
	result, err := e.Call(context.Background(), op, principal, args...)
	if err != nil {
		t.Fatalf("%s: %v", op, err)
	}
	if out != nil {
		if err := json.Unmarshal(result, out); err != nil {
			t.Fatalf("%s answered %s: %v", op, result, err)
		}
	}
}

var inShop = map[string]any{"namespace": shopNamespace}

// newCommerceShop stands the shop up: the engine, acmepay, shop-stock's
// stand-in, shop-orders over the engine and an ORM of orders in memory,
// and a shopper signed in through shop-api's login.
func newCommerceShop(t *testing.T) *commerceShop {
	t.Helper()
	background, e := openShopEngine(t)
	s := &commerceShop{engine: e, rows: &commerceRows{NoOpDatabase: orm.NewNoOpDatabase()}, sold: &soldLines{}}

	stock := httptest.NewServer(s.sold)
	t.Cleanup(stock.Close)
	stockClient, err := stocksdk.New(stocksdk.SDKConfig{BaseURL: stock.URL})
	if err != nil {
		t.Fatal(err)
	}

	u := newUsers(t)
	s.users = u
	shopper := u.add(t, shopperLogin, "Grace Hopper", shopperPassword, shopperPermissions...)
	s.shopper = shopper.ID
	s.token = signIn(t, productsClient(t, productsServer(t, u)), shopperLogin, shopperPassword)

	s.pay = acmepay.New(acmepay.Config{APIKey: acmepayKey, SigningSecret: acmepaySecret})
	payServer := httptest.NewServer(s.pay)
	t.Cleanup(payServer.Close)
	s.payAPI = &acmepay.Client{BaseURL: payServer.URL, APIKey: acmepayKey}

	s.deps = shoporders.Deps{DB: s.rows, Engine: background, ShopStock: stockClient, Logger: zap.NewNop()}
	s.deps.Config.AcmepayUrl = payServer.URL
	s.deps.Config.AcmepayApiKey = acmepayKey
	s.deps.Config.AcmepayWebhookSecret = acmepaySecret
	implementations, err := shopordersimpl.New(s.deps)
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Use(chimiddleware.RequestID)
	if err := shoporders.RegisterRoutes(router, shoporders.Config{DB: s.deps.DB, Logger: s.deps.Logger, Identity: u.shopOrders(t), Implementations: implementations}); err != nil {
		t.Fatal(err)
	}
	s.orders = httptest.NewServer(router)
	t.Cleanup(s.orders.Close)
	s.pay.SetWebhookURL(s.orders.URL + "/api/payments/acmepay/events")
	return s
}

// stock gives a product an Inventory as shop-stock's setStock does, at a
// price, with units on the shelf and the level it is low at.
func (s *commerceShop) stock(t *testing.T, product string, price, units, reorderAt int64) {
	t.Helper()
	keeper := servicePrincipal("shop-stock", "ada", "stock.write")
	engineCall(t, s.engine, nil, "instances.create", keeper, "Inventory", map[string]any{"sku": "anvil", "priceCents": price, "reorderAt": reorderAt}, map[string]any{"namespace": shopNamespace, "id": product})
	engineCall(t, s.engine, nil, "instances.invoke", keeper, "Inventory", product, "countStock", map[string]any{"onHand": units, "reason": "counted"}, inShop)
}

// cart fills a checkout in cart as shop-cart does for the shopper: lines
// of the product and, with a counter, a pickup slot there.
func (s *commerceShop) cart(t *testing.T, product string, quantity int, counter string, pickup time.Time) string {
	t.Helper()
	id := scalars.NewUUID().String()
	data := map[string]any{"customerId": s.shopper, "lines": []map[string]any{{"inventoryId": product, "quantity": quantity}}}
	options := map[string]any{"namespace": shopNamespace, "id": id}
	if counter != "" {
		data["pickupAt"] = pickup.UTC().Format(time.RFC3339)
		options["behaviors"] = map[string]any{"Links": map[string]any{"counter": counter}}
	}
	engineCall(t, s.engine, nil, "instances.create", servicePrincipal("shop-cart", s.shopper), "Checkout", data, options)
	return id
}

type inventoryRecord struct {
	Behaviors struct {
		Stock struct {
			OnHand    int64 `json:"onHand"`
			Held      int64 `json:"held"`
			Available int64 `json:"available"`
			Low       bool  `json:"low"`
		} `json:"Stock"`
	} `json:"behaviors"`
}

func (s *commerceShop) inventory(t *testing.T, product string) inventoryRecord {
	t.Helper()
	var record inventoryRecord
	engineCall(t, s.engine, &record, "instances.get", servicePrincipal("shop-stock", "shop-stock"), "Inventory", product, inShop)
	return record
}

// runDue runs what the runner has due: the Reactions rules a payment or a
// refund's settlement set off.
func (s *commerceShop) runDue(t *testing.T) {
	t.Helper()
	ran, err := s.engine.Runner().RunDue(context.Background())
	if err != nil {
		t.Fatalf("the runner: %v", err)
	}
	if strings.Contains(string(ran), `"failed":`) && !strings.Contains(string(ran), `"failed":0`) {
		status, _ := s.engine.Runner().Status(context.Background())
		t.Fatalf("the runner failed: %s; %s", ran, status)
	}
}

// A shopper's checkout holds the units it asks and a pickup slot, all or
// none, and another checkout cannot take what it holds; acmepay's payment,
// reported by its signed webhook, places it through the Reactions rule,
// which commits the units and the slot, and shop-orders writes its order
// once and tells shop-stock what it sold; a webhook whose signature does
// not verify is refused before anything reads it, and one acmepay sends
// again changes nothing; a refund two members of staff sign off is made
// at acmepay by SettlePayments and issued (D70).
func TestACheckoutHoldsPaysPlacesAndRefunds(t *testing.T) {
	s := newCommerceShop(t)
	ctx := context.Background()
	product := mustUUID(t, "00000000-0000-4000-8000-0000000000a1").String()
	s.stock(t, product, 4500, 3, 1)

	// A counter, which staff who hold calendar.manage open, and a slot at
	// 10:00 London time two weekdays or more ahead.
	counter := scalars.NewUUID().String()
	engineCall(t, s.engine, nil, "instances.create", staffPrincipal("ada", "calendar.manage"), "CollectionPoint", map[string]any{"name": "Front counter"}, map[string]any{"namespace": shopNamespace, "id": counter})
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	day := time.Now().In(london).AddDate(0, 0, 2)
	for day.Weekday() < time.Tuesday || day.Weekday() > time.Friday {
		day = day.AddDate(0, 0, 1)
	}
	slot := time.Date(day.Year(), day.Month(), day.Day(), 10, 0, 0, 0, london)

	orders := sdkFor(t, s.orders.URL, s.token)
	checkout := s.cart(t, product, 2, counter, slot)
	id := checkout
	started, err := orders.CheckoutsNamespace.StartCheckout(ctx, id, types.StartCheckoutInput{ShippingAddress: address})
	if err != nil {
		t.Fatalf("StartCheckout: %v", err)
	}
	if started.Status != types.CheckoutStatus_Held || started.DueCents != 9000 || started.Currency != "GBP" || len(started.Lines) != 1 || started.Lines[0].State != "held" ||
		started.Lines[0].UnitPriceCents == nil || *started.Lines[0].UnitPriceCents != 4500 || started.PickupAt == nil {
		t.Fatalf("StartCheckout answered %+v", started)
	}
	if level := s.inventory(t, product).Behaviors.Stock; level.Held != 2 || level.Available != 1 {
		t.Fatalf("the checkout holds %+v of the anvils", level)
	}

	// Another shopper's checkout asks for two anvils, and one is free: none
	// of it is held, and the refusal names what is free.
	other := s.cart(t, product, 2, "", time.Time{})
	_, err = orders.CheckoutsNamespace.StartCheckout(ctx, other, types.StartCheckoutInput{ShippingAddress: address})
	var refused *sdk.APIError
	if !errors.As(err, &refused) || refused.StatusCode != http.StatusConflict || !strings.Contains(refused.Message, "unavailable") && !strings.Contains(fmt.Sprint(refused.Details), "unavailable") {
		t.Fatalf("a checkout of more than is free answered %v, want 409 unavailable", err)
	}

	// The shopper pays on acmepay's page, with a test token: acmepay posts
	// the capture, signed, and shop-orders records it.
	payment, err := orders.CheckoutsNamespace.PayCheckout(ctx, id)
	if err != nil {
		t.Fatalf("PayCheckout: %v", err)
	}
	if payment.AmountCents != 9000 || payment.Currency != "GBP" || payment.ClientSecret == "" {
		t.Fatalf("PayCheckout answered %+v", payment)
	}
	if _, err := s.payAPI.ConfirmIntent(ctx, payment.IntentId, payment.ClientSecret, "4242424242424242"); err == nil {
		t.Fatal("acmepay took a card number")
	}
	if _, err := s.payAPI.ConfirmIntent(ctx, payment.IntentId, payment.ClientSecret, acmepay.TokenSucceeds); err != nil {
		t.Fatalf("pay at acmepay: %v", err)
	}
	events := s.pay.Events()
	captured := events[len(events)-1]
	if captured.Type != acmepay.EventIntentSucceeded || !s.pay.Delivered(captured.ID) {
		t.Fatalf("acmepay's capture %+v was not delivered", captured)
	}

	// A webhook signed with another secret, or not at all, is refused.
	body, _ := json.Marshal(captured)
	for name, signature := range map[string]string{
		"another secret": acmepay.Sign("whsec_someone_else", time.Now(), body),
		"no signature":   "",
		"an old one":     acmepay.Sign(acmepaySecret, time.Now().Add(-time.Hour), body),
	} {
		if status := postWebhook(t, s.orders.URL, signature, body); status != http.StatusUnauthorized {
			t.Errorf("a webhook with %s answered %d, want 401", name, status)
		}
	}
	// acmepay sends the capture again: it is acknowledged, and changes
	// nothing.
	if status, err := s.pay.Resend(ctx, captured.ID); err != nil || status != http.StatusOK {
		t.Fatalf("the capture sent again answered %d, %v", status, err)
	}

	// The runner's Reactions rule places the paid checkout, and reading it
	// writes its order, once.
	s.runDue(t)
	placed, err := orders.CheckoutsNamespace.GetCheckout(ctx, id)
	if err != nil {
		t.Fatalf("GetCheckout: %v", err)
	}
	if placed.Status != types.CheckoutStatus_Placed || placed.Payment != "paid" || placed.OrderId == nil || placed.Lines[0].State != "committed" {
		t.Fatalf("the paid checkout is %+v", placed)
	}
	again, err := orders.CheckoutsNamespace.GetCheckout(ctx, id)
	if err != nil || again.OrderId == nil || *again.OrderId != *placed.OrderId {
		t.Fatalf("a second read answered %+v, %v", again, err)
	}
	order := s.rows.only(t)
	if order.CheckoutId == nil || order.CheckoutId.String() != checkout || order.TotalCents != 9000 || len(order.Lines) != 1 ||
		order.Lines[0].UnitPriceCents != 4500 || order.Lines[0].Quantity != 2 || order.Customer.Id.String() != s.shopper || s.rows.enqueued() != 1 {
		t.Fatalf("the checkout wrote %+v, enqueuing %d", order, s.rows.enqueued())
	}
	// The anvils are sold: off the shelf, held by no one, and the one left
	// is the level the product is low at, which shop-stock is told of.
	if level := s.inventory(t, product).Behaviors.Stock; level.OnHand != 1 || level.Held != 0 || !level.Low {
		t.Fatalf("after the sale the anvils are %+v", level)
	}
	if got := s.sold.lines(); len(got) != 1 || got[0] != product+"x2" {
		t.Fatalf("shop-stock was told of %v", got)
	}
	var bookings json.RawMessage
	engineCall(t, s.engine, &bookings, "instances.invoke", staffPrincipal("ada", "calendar.manage"), "CollectionPoint", counter, "listBookings",
		map[string]any{"startMs": slot.Add(-time.Hour).UnixMilli(), "endMs": slot.Add(time.Hour).UnixMilli()}, inShop)
	if !strings.Contains(string(bookings), `"`+checkout+`"`) || !strings.Contains(string(bookings), `"committed"`) {
		t.Fatalf("the counter's bookings are %s, want the checkout's slot committed", bookings)
	}

	// A refund past what was paid is refused; one within it waits for two
	// members of staff.
	if _, err := orders.CheckoutsNamespace.RequestRefund(ctx, id, types.RequestRefundInput{AmountCents: 9001, Reason: "too much"}); !errors.As(err, &refused) || refused.StatusCode != http.StatusConflict {
		t.Fatalf("a refund past what was paid answered %v", err)
	}
	refund, err := orders.CheckoutsNamespace.RequestRefund(ctx, id, types.RequestRefundInput{AmountCents: 4500, Reason: "one anvil arrived dented"})
	if err != nil || refund.Status != "requested" || !strings.HasPrefix(refund.Number, "RF-") {
		t.Fatalf("RequestRefund answered %+v, %v", refund, err)
	}
	refundID := refund.Id.String()
	approve := map[string]any{"state": "approved"}
	engineCall(t, s.engine, nil, "instances.invoke", staffPrincipal("bob", "refunds.approve"), "Refund", refundID, "signOff", approve, inShop)
	_, err = s.engine.Call(ctx, "instances.invoke", staffPrincipal("bob", "refunds.approve"), "Refund", refundID, "transition", map[string]any{"to": "approved"}, inShop)
	if !engine.IsCode(err, engine.CodeVetoed) {
		t.Fatalf("a refund one member of staff signed off moved on: %v", err)
	}
	engineCall(t, s.engine, nil, "instances.invoke", staffPrincipal("carol", "refunds.approve"), "Refund", refundID, "signOff", approve, inShop)
	engineCall(t, s.engine, nil, "instances.invoke", staffPrincipal("carol", "refunds.approve"), "Refund", refundID, "transition", map[string]any{"to": "approved"}, inShop)

	// SettlePayments makes the refund at acmepay and records it; the
	// runner issues the Refund.
	jobs, err := shopordersimpl.NewJobs(s.deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := jobs.SettlePayments(ctx); err != nil {
		t.Fatalf("SettlePayments: %v", err)
	}
	s.runDue(t)
	var issued struct {
		Behaviors struct {
			Workflow struct {
				Status string `json:"status"`
			} `json:"Workflow"`
		} `json:"behaviors"`
	}
	engineCall(t, s.engine, &issued, "instances.get", servicePrincipal("shop-orders", "shop-orders"), "Refund", refundID, inShop)
	refunded, err := orders.CheckoutsNamespace.GetCheckout(ctx, id)
	if err != nil || issued.Behaviors.Workflow.Status != "issued" || refunded.Payment != "partiallyRefunded" {
		t.Fatalf("after SettlePayments the refund is %s and the checkout %+v (%v)", issued.Behaviors.Workflow.Status, refunded, err)
	}
	if intent, _ := s.pay.Intent(payment.IntentId); intent.Refunded != 4500 {
		t.Fatalf("acmepay refunded %d", intent.Refunded)
	}
	// A second run finds nothing to do.
	if err := jobs.SettlePayments(ctx); err != nil {
		t.Fatalf("SettlePayments again: %v", err)
	}
	if intent, _ := s.pay.Intent(payment.IntentId); intent.Refunded != 4500 {
		t.Fatalf("a second run refunded again: %d", intent.Refunded)
	}

	// shop-db's policy refuses a service it does not name, such as shop-api.
	_, err = s.engine.Call(ctx, "instances.get", servicePrincipal("shop-api", "ada"), "Checkout", checkout, inShop)
	if !engine.IsCode(err, engine.CodeForbidden) {
		t.Fatalf("shop-api read a checkout: %v", err)
	}
}

// A shopper's checkout is theirs: another shopper, or no one, finds none.
func TestACheckoutIsItsShoppersOwn(t *testing.T) {
	s := newCommerceShop(t)
	ctx := context.Background()
	product := mustUUID(t, "00000000-0000-4000-8000-0000000000a2").String()
	s.stock(t, product, 100, 5, 0)
	checkout := s.cart(t, product, 1, "", time.Time{})

	if _, err := sdkFor(t, s.orders.URL, "").CheckoutsNamespace.GetCheckout(ctx, checkout); err == nil {
		t.Fatal("an anonymous caller read a checkout")
	}
	s.users.add(t, "alan@example.com", "Alan Turing", "alan's password", shopperPermissions...)
	alan := signIn(t, productsClient(t, productsServer(t, s.users)), "alan@example.com", "alan's password")
	_, err := sdkFor(t, s.orders.URL, alan).CheckoutsNamespace.GetCheckout(ctx, checkout)
	var refused *sdk.APIError
	if !errors.As(err, &refused) || refused.StatusCode != http.StatusNotFound {
		t.Fatalf("another shopper's read answered %v, want 404", err)
	}
	if _, err := sdkFor(t, s.orders.URL, s.token).CheckoutsNamespace.GetCheckout(ctx, checkout); err != nil {
		t.Fatalf("the shopper's own read: %v", err)
	}
}

// postWebhook posts a body to acmepay's webhook with a signature header.
func postWebhook(t *testing.T, base, signature string, body []byte) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, base+"/api/payments/acmepay/events", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if signature != "" {
		req.Header.Set(acmepay.SignatureHeader, signature)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode
}

func sdkFor(t *testing.T, base, token string) *sdk.ShopOrdersSDK {
	t.Helper()
	config := sdk.SDKConfig{BaseURL: base}
	if token != "" {
		config.Auth = &sdk.AuthConfig{Token: token}
	}
	client, err := sdk.New(config)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// soldLines stands in for shop-stock's stockSold: it records each line it
// is told of, as <product>x<quantity>, and answers as shop-stock does.
type soldLines struct {
	mu   sync.Mutex
	seen []string
}

func (s *soldLines) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Lines []struct {
			ProductID string `json:"productId"`
			Quantity  int64  `json:"quantity"`
		} `json:"lines"`
	}
	if r.Method != http.MethodPost || r.URL.Path != "/api/stock/sold" || json.NewDecoder(r.Body).Decode(&in) != nil {
		http.Error(w, "not shop-stock's stockSold", http.StatusNotFound)
		return
	}
	s.mu.Lock()
	for _, line := range in.Lines {
		s.seen = append(s.seen, fmt.Sprintf("%sx%d", line.ProductID, line.Quantity))
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`[]`))
}

func (s *soldLines) lines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

// commerceRows is shop-db's ORM with its orders in memory: a checkout's
// order and its lines, written in a transaction, the unique checkoutId
// refusing a second, and the OrderPlaced messages it enqueues counted.
type commerceRows struct {
	*orm.NoOpDatabase
	mu       sync.Mutex
	orders   []db.Order
	messages int
}

func (r *commerceRows) GetOrderRepository() orm.OrderRepositoryInterface {
	return &commerceOrders{rows: r}
}

func (r *commerceRows) Transaction(_ context.Context, fn func(orm.TxInterface) error) error {
	tx := &commerceTx{rows: r}
	if err := fn(tx); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, order := range r.orders {
		if order.CheckoutId != nil && tx.order.CheckoutId != nil && *order.CheckoutId == *tx.order.CheckoutId {
			return &pgconn.PgError{Code: "23505", Message: "duplicate key value violates unique constraint"}
		}
	}
	r.orders = append(r.orders, tx.order)
	r.messages += tx.messages
	return nil
}

func (r *commerceRows) only(t *testing.T) db.Order {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.orders) != 1 {
		t.Fatalf("the shop holds %d orders, want 1", len(r.orders))
	}
	return r.orders[0]
}

func (r *commerceRows) enqueued() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.messages
}

type commerceOrders struct {
	orm.NoOpOrderRepository
	rows *commerceRows
}

func (o *commerceOrders) FindOne(_ context.Context, filter *orm.OrderFilter, _ *orm.OrderFindOptions) (*db.Order, error) {
	o.rows.mu.Lock()
	defer o.rows.mu.Unlock()
	for _, order := range o.rows.orders {
		if filter.CheckoutId != nil && filter.CheckoutId.Eq != nil && order.CheckoutId != nil && *order.CheckoutId == *filter.CheckoutId.Eq {
			found := order
			return &found, nil
		}
	}
	return nil, orm.ErrNotFound
}

// commerceTx is a transaction: the order it creates, its lines, and the
// messages it enqueues, which commit together.
type commerceTx struct {
	orm.NoOpTx
	rows     *commerceRows
	order    db.Order
	messages int
}

func (tx *commerceTx) GetOrderRepository() orm.OrderRepositoryInterface {
	return &txOrders{tx: tx}
}

func (tx *commerceTx) GetOrderLineRepository() orm.OrderLineRepositoryInterface {
	return &txLines{tx: tx}
}

func (tx *commerceTx) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	if strings.Contains(sql, "order_placed") {
		tx.messages++
	}
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

type txOrders struct {
	orm.NoOpOrderRepository
	tx *commerceTx
}

func (o *txOrders) CreateOne(_ context.Context, input *db.Order) (*db.Order, error) {
	id := scalars.NewUUID()
	order := *input
	order.Id = &id
	o.tx.order = order
	return &order, nil
}

type txLines struct {
	orm.NoOpOrderLineRepository
	tx *commerceTx
}

func (l *txLines) CreateOne(_ context.Context, input *db.OrderLine) (*db.OrderLine, error) {
	id := scalars.NewUUID()
	line := *input
	line.Id = &id
	l.tx.order.Lines = append(l.tx.order.Lines, line)
	return &line, nil
}
