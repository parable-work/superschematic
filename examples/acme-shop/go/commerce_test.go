package shop_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	shopapi "example.com/acme/api/shop-api"
	shoporders "example.com/acme/api/shop-orders"
	orm "example.com/acme/orm/shop-db"
	apisdk "example.com/acme/sdk/go/shop-api"
	sdk "example.com/acme/sdk/go/shop-orders"
	stocksdk "example.com/acme/sdk/go/shop-stock"
	"example.com/acme/shop/acmepay"
	shopapiimpl "example.com/acme/shop/shop-api"
	shopordersimpl "example.com/acme/shop/shop-orders"
	apitypes "example.com/acme/types/go/shop-api"
	db "example.com/acme/types/go/shop-db"
	types "example.com/acme/types/go/shop-orders"
	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
	scalars "github.com/parable-work/superscalar/go"
	"github.com/parable-work/superschematic/runtime/http/go/identity"
	rules "github.com/parable-work/superschematic/runtime/rules/go"
	rulespostgres "github.com/parable-work/superschematic/runtime/rules/go/postgres"
	rulessqlite "github.com/parable-work/superschematic/runtime/rules/go/sqlite"
	"go.uber.org/zap"
)

// acmepay's keys in the tests: anything will do, as they are the fake's.
const (
	acmepayKey    = "ak_test_shop"
	acmepaySecret = "whsec_test_shop"
)

// commerceShop is shop-orders served in-process over shop-db's tables,
// whose behaviors the generated ORM runs through the behaviors' core (D70,
// D103), in a database of the test's own: shop-db's SQLite schema in a
// file, or its Postgres schema in a database of the server
// ACME_SHOP_DATABASE_URL names. acmepay, the fake provider, posts its
// webhooks to it; a stand-in for shop-stock records the sales it is told
// of; and the shop's users are rows of the same database.
type commerceShop struct {
	database orm.DatabaseInterface
	orders   *httptest.Server
	api      *httptest.Server
	pay      *acmepay.Provider
	payAPI   *acmepay.Client
	sold     *soldLines
	deps     shoporders.Deps
	users    *users
	shopper  string
	token    string
}

// commerceDatabase opens a new shop-db database and its users.
type commerceDatabase func(t *testing.T) (orm.DatabaseInterface, *users)

// commerceDatabases are the databases the commerce tests run on: a SQLite
// file always, and Postgres when ACME_SHOP_DATABASE_URL names a server.
func commerceDatabases(t *testing.T) map[string]commerceDatabase {
	t.Helper()
	databases := map[string]commerceDatabase{"sqlite": sqliteShop}
	if os.Getenv("ACME_SHOP_DATABASE_URL") != "" {
		databases["postgres"] = postgresShop
	}
	return databases
}

// sqliteShop is shop-db in a SQLite file of the test's own, created from
// its SQLite DDL, with the repositories on it (D116) and the shop's users
// in its identity tables.
func sqliteShop(t *testing.T) (orm.DatabaseInterface, *users) {
	t.Helper()
	ddl, err := os.ReadFile(filepath.Join("..", "schemas", "dist", "sql", "shop-db", "sqlite", "create.sql"))
	if err != nil {
		t.Fatalf("the build wrote no SQLite DDL of shop-db: %v", err)
	}
	conn, err := sql.Open("sqlite", orm.SQLiteDSN(filepath.Join(t.TempDir(), "shop.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.Exec(string(ddl)); err != nil {
		t.Fatalf("create shop-db's tables: %v", err)
	}
	database, err := orm.OpenSQLite(context.Background(), conn)
	if err != nil {
		t.Fatal(err)
	}
	return database, usersIn(t, conn, identity.SQLite)
}

// postgresShop is shop-db in a database of its own on the Postgres server
// ACME_SHOP_DATABASE_URL names, created from its Postgres DDL and dropped
// when the test ends.
func postgresShop(t *testing.T) (orm.DatabaseInterface, *users) {
	t.Helper()
	ctx := context.Background()
	server := os.Getenv("ACME_SHOP_DATABASE_URL")
	admin, err := pgx.Connect(ctx, server)
	if err != nil {
		t.Fatalf("connect to ACME_SHOP_DATABASE_URL: %v", err)
	}
	defer admin.Close(ctx)
	name := fmt.Sprintf("acme_commerce_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if admin, err := pgx.Connect(context.Background(), server); err == nil {
			_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
			_ = admin.Close(context.Background())
		}
	})
	at, err := url.Parse(server)
	if err != nil {
		t.Fatal(err)
	}
	at.Path = "/" + name
	ddl, err := os.ReadFile(filepath.Join("..", "schemas", "dist", "sql", "shop-db", "create.sql"))
	if err != nil {
		t.Fatalf("the build wrote no Postgres DDL of shop-db: %v", err)
	}
	conn, err := pgx.Connect(ctx, at.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, string(ddl)); err != nil {
		t.Fatalf("create shop-db's tables: %v", err)
	}
	_ = conn.Close(ctx)
	database, err := orm.Connect(ctx, at.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	identityDB, err := sql.Open("pgx", at.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = identityDB.Close() })
	return database, usersIn(t, identityDB, identity.Postgres)
}

// usersIn are the shop's users in shop-db's identity tables on conn.
func usersIn(t *testing.T, conn *sql.DB, dialect identity.Dialect) *users {
	t.Helper()
	store, err := identity.NewSQLStore(conn, dialect, []byte(db.IdentityDescriptor))
	if err != nil {
		t.Fatal(err)
	}
	u := newUsers(t)
	u.store = store
	return u
}

// asStaff is ctx with the behaviors' commands run as a member of staff
// holding permissions, as a behavior operation's route runs them.
func asStaff(ctx context.Context, subject string, permissions ...string) context.Context {
	return orm.WithBehaviorCaller(ctx, rules.Caller{Subject: subject, Permissions: permissions})
}

// newCommerceShop stands the shop up on a database: acmepay, shop-stock's
// stand-in, shop-orders over the database's repositories, and a shopper
// signed in through shop-api's login.
func newCommerceShop(t *testing.T, open commerceDatabase) *commerceShop {
	t.Helper()
	database, u := open(t)
	s := &commerceShop{database: database, sold: &soldLines{}, users: u}

	stock := httptest.NewServer(s.sold)
	t.Cleanup(stock.Close)
	stockClient, err := stocksdk.New(stocksdk.SDKConfig{BaseURL: stock.URL})
	if err != nil {
		t.Fatal(err)
	}

	shopper := u.add(t, shopperLogin, "Grace Hopper", shopperPassword, shopperPermissions...)
	s.shopper = shopper.ID
	s.token = signIn(t, productsClient(t, productsServer(t, u)), shopperLogin, shopperPassword)

	s.pay = acmepay.New(acmepay.Config{APIKey: acmepayKey, SigningSecret: acmepaySecret})
	payServer := httptest.NewServer(s.pay)
	t.Cleanup(payServer.Close)
	s.payAPI = &acmepay.Client{BaseURL: payServer.URL, APIKey: acmepayKey}

	s.deps = shoporders.Deps{DB: database, ShopStock: stockClient, Logger: zap.NewNop()}
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

	// shop-api over the same database, which serves Refund's operations
	// to staff (RefundActions).
	apiDeps := shopapi.Deps{DB: database, Logger: zap.NewNop()}
	apiImplementations, err := shopapiimpl.New(apiDeps)
	if err != nil {
		t.Fatal(err)
	}
	apiRouter := chi.NewRouter()
	apiRouter.Use(chimiddleware.RequestID)
	if err := shopapi.RegisterRoutes(apiRouter, shopapi.Config{DB: apiDeps.DB, Logger: apiDeps.Logger, Identity: u.shopAPI(t), Implementations: apiImplementations}); err != nil {
		t.Fatal(err)
	}
	s.api = httptest.NewServer(apiRouter)
	t.Cleanup(s.api.Close)
	return s
}

// apiSDK is shop-api's SDK with a session's token.
func (s *commerceShop) apiSDK(t *testing.T, token string) *apisdk.ShopApiSDK {
	t.Helper()
	client, err := apisdk.New(apisdk.SDKConfig{BaseURL: s.api.URL, Auth: &apisdk.AuthConfig{Token: token}})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// stock adds a product and gives it an Inventory row as shop-stock's
// setStock does, at its price, with units on the shelf and the level it is
// low at.
func (s *commerceShop) stock(t *testing.T, sku string, price, units, reorderAt int64) db.IdentityUUID {
	t.Helper()
	ctx := context.Background()
	product, err := s.database.GetProductRepository().CreateOne(ctx, &db.Product{Sku: db.IdentitySlug(sku), Name: "Anvil", PriceCents: db.GenericInt64(price), InStock: true})
	if err != nil {
		t.Fatalf("add product %s: %v", sku, err)
	}
	keeper := asStaff(ctx, "ada", "stock.write")
	level := db.GenericInt64(reorderAt)
	err = s.database.Transaction(keeper, func(tx orm.TxInterface) error {
		if _, err := tx.GetInventoryRepository().CreateOne(keeper, &orm.InventoryCreate{Id: *product.Id, Sku: sku, PriceCents: db.GenericInt64(price), ReorderAt: &level}); err != nil {
			return err
		}
		_, err := tx.GetInventoryRepository().Stock().CountStock(keeper, *product.Id, orm.InventoryStockCountStockParams{OnHand: units, Reason: "counted"})
		return err
	})
	if err != nil {
		t.Fatalf("stock %s: %v", sku, err)
	}
	return *product.Id
}

// cart fills a checkout in cart as shop-cart does for the shopper: lines
// of the product and, with a counter, a pickup slot there.
func (s *commerceShop) cart(t *testing.T, product db.IdentityUUID, quantity int64, counter *db.IdentityUUID, pickup time.Time) db.IdentityUUID {
	t.Helper()
	shopper, err := scalars.ParseUUID(s.shopper)
	if err != nil {
		t.Fatal(err)
	}
	create := &orm.CheckoutCreate{
		Customer: db.User{Id: &shopper},
		Lines:    []db.CheckoutLine{{InventoryId: product, Quantity: db.GenericInt64(quantity)}},
	}
	if counter != nil {
		at := db.TemporalDateTime(pickup.UTC())
		create.Counter = &db.CollectionPoint{Id: counter}
		create.PickupAt = &at
	}
	checkout, err := s.database.GetCheckoutRepository().CreateOne(orm.WithCaller(context.Background(), s.shopper), create)
	if err != nil {
		t.Fatalf("fill a cart: %v", err)
	}
	return *checkout.Id
}

type stockLevel struct {
	OnHand, Held, Available int64
	Low                     bool
}

// inventory is a product's Stock as shop-stock reads it: what is on hand,
// what checkouts hold, what is free, and whether it is low.
func (s *commerceShop) inventory(t *testing.T, product db.IdentityUUID) stockLevel {
	t.Helper()
	row, err := s.database.GetInventoryRepository().GetOne(context.Background(), product, nil)
	if err != nil {
		t.Fatal(err)
	}
	level := stockLevel{}
	if row.StockOnHand != nil {
		level.OnHand = int64(*row.StockOnHand)
	}
	if row.StockHeld != nil {
		level.Held = int64(*row.StockHeld)
	}
	level.Available = level.OnHand - level.Held
	level.Low = row.ReorderAt != nil && level.Available <= int64(*row.ReorderAt)
	return level
}

// counter opens a counter, which staff who hold calendar.manage open, and
// a slot at 10:00 London time two weekdays or more ahead.
func (s *commerceShop) counter(t *testing.T) (db.IdentityUUID, time.Time) {
	t.Helper()
	point, err := s.database.GetCollectionPointRepository().CreateOne(asStaff(context.Background(), "ada", "calendar.manage"), &orm.CollectionPointCreate{Name: "Front counter"})
	if err != nil {
		t.Fatalf("open a counter: %v", err)
	}
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	day := time.Now().In(london).AddDate(0, 0, 2)
	for day.Weekday() < time.Tuesday || day.Weekday() > time.Friday {
		day = day.AddDate(0, 0, 1)
	}
	return *point.Id, time.Date(day.Year(), day.Month(), day.Day(), 10, 0, 0, 0, london)
}

// staffSDK is shop-api's SDK with a member of staff's session, who holds
// permissions.
func (s *commerceShop) staffSDK(t *testing.T, login, name string, permissions ...string) *apisdk.ShopApiSDK {
	t.Helper()
	s.users.add(t, login, name, name+"'s password", permissions...)
	return s.apiSDK(t, signIn(t, productsClient(t, s.api), login, name+"'s password"))
}

// A shopper's checkout holds the units it asks and a pickup slot, all or
// none, and another checkout cannot take what it holds; acmepay's payment,
// reported by its signed webhook, places it, which commits the units and
// the slot, and shop-orders writes its order once and tells shop-stock what
// it sold; a webhook whose signature does not verify is refused before
// anything reads it, and one acmepay sends again changes nothing; a refund
// two members of staff sign off through shop-api's RefundActions is made at
// acmepay by SettlePayments and issued (D70, D103).
func TestACheckoutHoldsPaysPlacesAndRefunds(t *testing.T) {
	for name, open := range commerceDatabases(t) {
		t.Run(name, func(t *testing.T) { aCheckoutHoldsPaysPlacesAndRefunds(t, open) })
	}
}

func aCheckoutHoldsPaysPlacesAndRefunds(t *testing.T, open commerceDatabase) {
	s := newCommerceShop(t, open)
	ctx := context.Background()
	product := s.stock(t, "anvil", 4500, 3, 1)
	counter, slot := s.counter(t)

	orders := sdkFor(t, s.orders.URL, s.token)
	checkout := s.cart(t, product, 2, &counter, slot)
	id := checkout.String()
	started, err := orders.CheckoutsNamespace.StartCheckout(ctx, id, types.StartCheckoutInput{ShippingAddress: address})
	if err != nil {
		t.Fatalf("StartCheckout: %v", err)
	}
	if started.Status != types.CheckoutStatus_Held || started.DueCents != 9000 || started.Currency != "GBP" || len(started.Lines) != 1 || started.Lines[0].State != "held" ||
		started.Lines[0].UnitPriceCents == nil || *started.Lines[0].UnitPriceCents != 4500 || started.PickupAt == nil || !strings.HasPrefix(started.Number, "ORD-") {
		t.Fatalf("StartCheckout answered %+v", started)
	}
	if level := s.inventory(t, product); level.Held != 2 || level.Available != 1 {
		t.Fatalf("the checkout holds %+v of the anvils", level)
	}

	// Another shopper's checkout asks for two anvils, and one is free: none
	// of it is held, and the refusal names what is free.
	other := s.cart(t, product, 2, nil, time.Time{})
	_, err = orders.CheckoutsNamespace.StartCheckout(ctx, other.String(), types.StartCheckoutInput{ShippingAddress: address})
	var refused *sdk.APIError
	if !errors.As(err, &refused) || refused.StatusCode != http.StatusConflict || !strings.Contains(refused.Message, "unavailable") && !strings.Contains(fmt.Sprint(refused.Details), "unavailable") && !strings.Contains(string(refused.Body), "unavailable") {
		t.Fatalf("a checkout of more than is free answered %v, want 409 unavailable", err)
	}
	if level := s.inventory(t, product); level.Held != 2 {
		t.Fatalf("the refused checkout left %+v of the anvils", level)
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

	// The capture placed the paid checkout, and its order is written once.
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
	order, err := s.database.GetOrderRepository().GetOne(ctx, *placed.OrderId, nil)
	if err != nil {
		t.Fatal(err)
	}
	lines, _, err := s.database.GetOrderLineRepository().FindMany(ctx, &orm.OrderLineFilter{OrderID: &orm.UUIDFilter{Eq: order.Id}}, &orm.OrderLineFindOptions{Fields: orm.OrderLineFields{}.All()})
	if err != nil {
		t.Fatal(err)
	}
	if order.CheckoutId == nil || *order.CheckoutId != checkout || order.TotalCents != 9000 || len(lines) != 1 ||
		lines[0].UnitPriceCents != 4500 || lines[0].Quantity != 2 || order.Customer.Id.String() != mustUUID(t, s.shopper).String() {
		t.Fatalf("the checkout wrote %+v", order)
	}
	if _, n, err := s.database.GetOrderRepository().FindMany(ctx, &orm.OrderFilter{CheckoutId: &orm.UUIDFilter{Eq: &checkout}}, &orm.OrderFindOptions{Fields: orm.OrderFields{}.All()}); err != nil || n != 1 {
		t.Fatalf("the checkout wrote %d orders, %v", n, err)
	}
	// The anvils are sold: off the shelf, held by no one, and the one left
	// is the level the product is low at, which shop-stock is told of.
	if level := s.inventory(t, product); level.OnHand != 1 || level.Held != 0 || !level.Low {
		t.Fatalf("after the sale the anvils are %+v", level)
	}
	if got := s.sold.lines(); len(got) != 1 || got[0] != product.String()+"x2" {
		t.Fatalf("shop-stock was told of %v", got)
	}
	bookings, err := s.database.GetCollectionPointRepository().Calendar().ListBookings(asStaff(ctx, "ada", "calendar.manage"), counter,
		orm.CollectionPointCalendarListBookingsParams{StartMs: slot.Add(-time.Hour).UnixMilli(), EndMs: slot.Add(time.Hour).UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(bookings); len(bookings.Items) != 1 || !strings.Contains(string(raw), `"committed"`) {
		t.Fatalf("the counter's bookings are %s, want the checkout's slot committed", raw)
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
	approve := apitypes.RefundApprovalsSignOffInput{State: apitypes.RefundApprovalsGate_Approved}
	toApproved := apitypes.RefundStatusTransitionInput{To: apitypes.RefundStatusState_Approved}
	// The shopper who asked for it holds no refunds.approve, and
	// shop-api's routes refuse them.
	var staffRefused *apisdk.APIError
	if _, err := s.apiSDK(t, s.token).RefundNamespace.Approvals.SignOff(ctx, refundID, approve); !errors.As(err, &staffRefused) || staffRefused.StatusCode != http.StatusForbidden {
		t.Fatalf("the shopper's sign-off answered %v, want 403", err)
	}
	bob := s.staffSDK(t, "bob@example.com", "Bob", "refunds.approve")
	if _, err := bob.RefundNamespace.Approvals.SignOff(ctx, refundID, approve); err != nil {
		t.Fatalf("Bob's sign-off: %v", err)
	}
	if _, err := bob.RefundNamespace.Status.Transition(ctx, refundID, toApproved); !errors.As(err, &staffRefused) || staffRefused.StatusCode != http.StatusConflict {
		t.Fatalf("a refund one member of staff signed off moved on: %v", err)
	}
	carol := s.staffSDK(t, "carol@example.com", "Carol", "refunds.approve")
	if _, err := carol.RefundNamespace.Approvals.SignOff(ctx, refundID, approve); err != nil {
		t.Fatalf("Carol's sign-off: %v", err)
	}
	if _, err := carol.RefundNamespace.Status.Transition(ctx, refundID, toApproved); err != nil {
		t.Fatalf("approve the refund: %v", err)
	}

	// SettlePayments makes the refund at acmepay, records it on the
	// checkout, which settles it on the Refund, and issues the Refund.
	jobs, err := shopordersimpl.NewJobs(s.deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := jobs.SettlePayments(ctx); err != nil {
		t.Fatalf("SettlePayments: %v", err)
	}
	issued, err := s.database.GetRefundRepository().GetOne(ctx, refund.Id, nil)
	if err != nil {
		t.Fatal(err)
	}
	refunded, err := orders.CheckoutsNamespace.GetCheckout(ctx, id)
	if err != nil || issued.Status != "issued" || refunded.Payment != "partiallyRefunded" {
		t.Fatalf("after SettlePayments the refund is %s and the checkout %+v (%v)", issued.Status, refunded, err)
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
}

// A checkout still held fifteen minutes on expires: its hold's deadline,
// which shop-api's behavior worker CheckoutTimers runs when it falls due,
// moves it to expired, which lets go of the units and the pickup slot it
// holds and voids the payment it opened (D70, D103).
func TestAHeldCheckoutExpires(t *testing.T) {
	for name, open := range commerceDatabases(t) {
		t.Run(name, func(t *testing.T) { aHeldCheckoutExpires(t, open) })
	}
}

func aHeldCheckoutExpires(t *testing.T, open commerceDatabase) {
	s := newCommerceShop(t, open)
	ctx := context.Background()
	product := s.stock(t, "kettle", 3000, 2, 0)
	counter, slot := s.counter(t)
	orders := sdkFor(t, s.orders.URL, s.token)
	checkout := s.cart(t, product, 2, &counter, slot)
	if _, err := orders.CheckoutsNamespace.StartCheckout(ctx, checkout.String(), types.StartCheckoutInput{ShippingAddress: address}); err != nil {
		t.Fatalf("StartCheckout: %v", err)
	}
	if _, err := orders.CheckoutsNamespace.PayCheckout(ctx, checkout.String()); err != nil {
		t.Fatalf("PayCheckout: %v", err)
	}
	if level := s.inventory(t, product); level.Held != 2 || level.Available != 0 {
		t.Fatalf("the checkout holds %+v of the kettles", level)
	}

	// The worker runs the hold's deadline when it falls due, as of its due
	// time; the shopper never paid.
	armed := deadlinesOf(t, s.database, checkout)
	var expiry *rules.ArmedDeadline
	for i := range armed {
		if armed[i].Name == "holdExpiry" {
			expiry = &armed[i]
		}
	}
	if expiry == nil {
		t.Fatalf("the held checkout's deadlines are %+v, without holdExpiry", armed)
	}
	if due := time.UnixMilli(expiry.At); due.Before(time.Now().Add(14*time.Minute)) || due.After(time.Now().Add(16*time.Minute)) {
		t.Fatalf("holdExpiry falls due at %s, want fifteen minutes on", due)
	}
	worker := orm.WithBehaviorCaller(ctx, rules.Caller{Subject: "shop-api/CheckoutTimers"})
	run, err := s.database.GetCheckoutRepository().RunDeadline(worker, db.CheckoutDeadline{RowKey: checkout, UseId: expiry.UseID, Name: expiry.Name, At: db.GenericInt64(expiry.At)}, nil)
	if err != nil || !run.Ran {
		t.Fatalf("the hold's deadline ran %+v, %v", run, err)
	}
	expired, err := orders.CheckoutsNamespace.GetCheckout(ctx, checkout.String())
	if err != nil {
		t.Fatal(err)
	}
	if expired.Status != types.CheckoutStatus_Expired || len(expired.Lines) != 1 || expired.Lines[0].State != "released" || expired.OrderId != nil {
		t.Fatalf("the expired checkout is %+v", expired)
	}
	if level := s.inventory(t, product); level.OnHand != 2 || level.Held != 0 || level.Available != 2 {
		t.Fatalf("after the expiry the kettles are %+v, want both free", level)
	}
	bookings, err := s.database.GetCollectionPointRepository().Calendar().ListBookings(asStaff(ctx, "ada", "calendar.manage"), counter,
		orm.CollectionPointCalendarListBookingsParams{StartMs: slot.Add(-time.Hour).UnixMilli(), EndMs: slot.Add(time.Hour).UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(bookings); strings.Contains(string(raw), `"held"`) {
		t.Fatalf("the counter's bookings are %s, want the slot let go", raw)
	}
	// The payment it opened is voided: SettlePayments cancels it at
	// acmepay.
	jobs, err := shopordersimpl.NewJobs(s.deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := jobs.SettlePayments(ctx); err != nil {
		t.Fatalf("SettlePayments: %v", err)
	}
	for _, event := range s.pay.Events() {
		if event.Type == acmepay.EventIntentSucceeded {
			t.Fatalf("acmepay captured %+v of an expired checkout", event)
		}
	}
	if !hasEvent(s.pay.Events(), acmepay.EventIntentCanceled) {
		t.Fatalf("acmepay's events are %+v, want the intent cancelled", s.pay.Events())
	}
}

func hasEvent(events []acmepay.Event, eventType string) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

// deadlinesOf are the deadlines armed on a checkout, as its behavior
// worker's queue holds them.
func deadlinesOf(t *testing.T, database orm.DatabaseInterface, checkout db.IdentityUUID) []rules.ArmedDeadline {
	t.Helper()
	var armed []rules.ArmedDeadline
	err := database.Transaction(context.Background(), func(tx orm.TxInterface) error {
		store := rulespostgres.NewStore(tx)
		if lite, ok := tx.(*orm.SQLiteTx); ok {
			store = rulessqlite.NewStore(lite.SQL())
		}
		var err error
		armed, err = rules.ReadDeadlines(context.Background(), rules.Host{Layout: orm.RulesLayout, Store: store}, "Checkout", checkout.String())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return armed
}

// A shopper's checkout is theirs: another shopper, or no one, finds none.
func TestACheckoutIsItsShoppersOwn(t *testing.T) {
	s := newCommerceShop(t, sqliteShop)
	ctx := context.Background()
	product := s.stock(t, "anvil", 100, 5, 0)
	checkout := s.cart(t, product, 1, nil, time.Time{}).String()

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
