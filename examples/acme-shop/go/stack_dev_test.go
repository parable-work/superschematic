package shop_test

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	apisdk "example.com/acme/sdk/go/shop-api"
	"example.com/acme/sdk/go/shop-api/namespaces"
	orderssdk "example.com/acme/sdk/go/shop-orders"
	ordersns "example.com/acme/sdk/go/shop-orders/namespaces"
	orderspages "example.com/acme/sdk/go/shop-orders/runtime"
	stocksdk "example.com/acme/sdk/go/shop-stock"
	"example.com/acme/shop/acmepay"
	apitypes "example.com/acme/types/go/shop-api"
	db "example.com/acme/types/go/shop-db"
	orderstypes "example.com/acme/types/go/shop-orders"
	stocktypes "example.com/acme/types/go/shop-stock"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/parable-work/superschematic/runtime/http/go/identity"
	"github.com/parable-work/superschematic/runtime/http/go/telemetry/otlptest"
)

// TestStackDevRunsTheShop is milestones 1 and 7 of docs/stack-model.md
// (section 14) on this example. `superschematic stack dev` runs
// shop-stack's Dev environment from examples/acme-shop, as the tutorial
// does: Postgres in a container with shop-db migrated; fake-gcs-server in a
// container with shop-media, the bucket shop-api lists (D54); shop-api and
// shop-orders each on its generated Go entrypoint, built with the
// implementations in go/shop-api and go/shop-orders; shop-stock on its
// generated Rust entrypoint, which cargo builds with the implementation in
// rust-impl/shop-stock (D57); shop-cart and shop-storefront on their
// generated TypeScript entrypoints, which Bun runs with the implementations
// in typescript/shop-cart and typescript/shop-storefront (D51); and
// shop-db's engine runner, which runs its engine types' reactions and
// deadlines (D58, D70). Every connection string, URL and port
// the test uses comes from the environment the build resolved. The test
// waits for each server's /readyz, creates a user in shop-db with the
// identity runtime's store, as staff would through the administration
// routes, signs them in through shop-api's generated login, calls each Go
// and Rust API through its generated Go SDK with the session and the
// storefront over HTTP. shop-api signs an upload of a product's image,
// which the test PUTs to the emulator through the signed URL, as a browser
// would, and reads back from shop-media. Anyone lists shop-stock's stock,
// and staff count the product's shelf, which gives it an Inventory in
// shop-db's engine (D70). Staff open a collection point through the
// engine's routes; a cart on shop-cart takes two units and a pickup slot
// from the slots it lists; shop-orders' startCheckout holds them, and a
// second checkout beyond the stock is refused 409 unavailable. acmepay,
// the fake provider, which the test runs where ACMEPAY_URL points with the
// keys of Dev's secrets, takes the payment and posts the capture to
// shop-orders' webhook, signed; the runner places the checkout, whose
// order shop-orders writes, enqueuing OrderPlaced, and tells shop-stock
// what it sold through its Go SDK with shop-orders' service credential; a
// sale reported with the shopper's session and no service credential is
// refused 401. A refund two members of staff sign off through the engine's
// routes is made at acmepay by SettlePayments, run with stack run.
// shop-orders' worker FulfilOrders, a process of its own on its generated
// entrypoint with the implementation's NewWorkers, claims the OrderPlaced
// message and fulfils the order (section 8.8, D53). It reads the static site shop-web, which
// stack dev built and serves with its config, and asks shop-api, as a
// browser would, whether the site's origin may call it, and another origin
// (D55). Then shop-orders' job ShipOrders, on its generated entrypoint with
// the implementation's NewJobs, ships the order fulfilled: once on demand
// with `superschematic stack run`, and again on the every-minute schedule
// Dev's settings give it, which stack dev runs
// (section 8.7, D52). shop-orders lists its catalog, by the lookup of the
// products' ids, and its orders a page at a time over Postgres, each
// shopper's own and staff every customer's, and a product's reviews
// searched by their words and paged (D60, D61, D62); a shopper gets and
// cancels no other customer's order.
// The sale left the product low, at the level staff set, so shop-stock
// enqueued StockLow, and
// shop-stock's worker ReorderStock, a Rust process on its generated
// entrypoint with the implementation's create_workers, files the product's
// restock request in shop-db; shop-stock's job OrderRestocks, a Rust job
// Dev runs only on demand, orders it when stack run runs it (D65). Every
// server, job and worker exports its traces to an OTLP receiver of the
// test's own, which the test's environment names and stack dev hands on
// (D67): a sale is one trace from shop-orders' Go call to shop-stock's
// Rust server, the storefront's requests are spans named by
// their routes, each worker makes a span of the message it handles, and
// each job's process sends the span of its run before it exits. The
// test stops the stack as Ctrl-C does, which with --remove-data removes
// the containers. Last, it runs stack dev once more, into an output root
// of its own (--out), where shop-stock's server, worker and job build
// against that root's mirror of rust-impl/shop-stock and its own API
// crates, and shop-cart and shop-storefront against their mirrors of
// typescript/shop-cart and typescript/shop-storefront, and runs the job there with stack run. The
// project's own TypeScript packages keep the links the default output
// root's install made.
//
// scripts/check.sh sets ACME_SHOP_SUPERSCHEMATIC to the core binary and
// SUPERSCHEMATIC_MIGRATE to the migration runner. Without the binary,
// without Docker, or without cargo, which builds shop-stock's server, the
// test skips.
func TestStackDevRunsTheShop(t *testing.T) {
	binary := os.Getenv("ACME_SHOP_SUPERSCHEMATIC")
	if binary == "" {
		t.Skip("set ACME_SHOP_SUPERSCHEMATIC to the superschematic binary, as scripts/check.sh does, to run the stack")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("Docker is not available: %v", err)
	}
	if _, err := exec.LookPath("cargo"); err != nil {
		t.Skip("cargo is not available to build shop-stock's Rust server")
	}

	// stack dev builds into the example's output root, schemas/dist, as the
	// tutorial's does, which build-all has just written: its build writes
	// the same files again, and its install of the Bun workspace there
	// leaves the committed bun.lock as it is. shop-stock's server builds
	// frozen to the committed Cargo.lock, against the mirror of its
	// implementation the build writes under schemas/dist/impl (D65), and
	// reuses the crates an earlier run compiled there.
	outputRoot := filepath.Join("..", "schemas", "dist")
	// Every server, job and worker stack dev runs exports its traces to a
	// receiver of the test's own, as a developer's shell points them at a
	// collector: stack dev hands its OpenTelemetry variables on (D67).
	traces := otlptest.Start(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", traces.URL)
	t.Setenv("OTEL_BSP_SCHEDULE_DELAY", "500")
	// acmepay, where shop-orders' ACMEPAY_URL defaults to, with the keys of
	// the environment's secrets file, which stack dev hands shop-orders.
	pay := startAcmepay(t)
	dev := startStackDev(t, binary, shopStack)
	output := dev.output
	env := dev.waitUntilRunning(t, outputRoot, "shop-stack", "shop-api", "shop-orders", "shop-stock", "shop-cart", "shop-storefront")
	pay.provider.SetWebhookURL(env.servers["shop-orders"] + "/api/payments/acmepay/events")

	// Staff, who hold products, orders and stock, add and stock the
	// products, and order them too.
	ctx := context.Background()
	token := signInOnTheStack(ctx, t, env.database, env.servers["shop-api"], "Ada Lovelace", "products", "orders", "stock", "calendar.manage")
	n := time.Now().UnixNano()

	// shop-api: staff add a product, and it is listed.
	products, err := apisdk.New(apisdk.SDKConfig{BaseURL: env.servers["shop-api"], Auth: &apisdk.AuthConfig{Token: token}})
	if err != nil {
		t.Fatal(err)
	}
	product, err := products.ProductNamespace.CreateProduct(ctx, apitypes.CreateProductInput{
		Sku:        apitypes.IdentitySlug(fmt.Sprintf("green-tea-%d", n)),
		Name:       "Green tea",
		PriceCents: 450,
	})
	if err != nil {
		t.Fatalf("CreateProduct: %v", err)
	}
	listed, err := products.ProductNamespace.ListProducts(ctx, &namespaces.ProductListProductsQueryParams{InStock: true})
	if err != nil {
		t.Fatalf("ListProducts: %v", err)
	}
	if !containsProduct(listed, product.Id) {
		t.Fatalf("ListProducts returned %+v, without %s", listed, product.Id)
	}

	// shop-media (D54): shop-api names an object for the product's image
	// and signs its upload; the image goes straight to the bucket through
	// the signed URL, as a browser would send it, never through shop-api.
	upload, err := products.ProductNamespace.CreateProductImageUpload(ctx, product.Id.String(), apitypes.ProductImageUploadInput{ContentType: "image/png"})
	if err != nil {
		t.Fatalf("CreateProductImageUpload: %v", err)
	}
	if !strings.HasPrefix(upload.UploadUrl, env.bucket+"/") || !strings.Contains(upload.UploadUrl, "X-Goog-Signature=") {
		t.Fatalf("the upload URL %s is no signed URL of the bucket at %s", upload.UploadUrl, env.bucket)
	}
	image := []byte("\x89PNG\r\n\x1a\n green tea")
	put, err := http.NewRequest(http.MethodPut, upload.UploadUrl, bytes.NewReader(image))
	if err != nil {
		t.Fatal(err)
	}
	put.Header.Set("Content-Type", "image/png")
	uploaded, err := http.DefaultClient.Do(put)
	if err != nil {
		t.Fatal(err)
	}
	uploaded.Body.Close()
	if uploaded.StatusCode != http.StatusOK {
		t.Fatalf("PUT the image through the signed URL: %s", uploaded.Status)
	}
	got, err := products.ProductNamespace.GetProduct(ctx, product.Id.String())
	if err != nil || got.ImageObject != upload.ObjectName {
		t.Fatalf("GetProduct = %+v, %v; want the image object %s recorded", got, err, upload.ObjectName)
	}
	back, err := http.Get(env.bucket + "/" + upload.ObjectName)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := io.ReadAll(back.Body)
	back.Body.Close()
	if err != nil || back.StatusCode != http.StatusOK || !bytes.Equal(stored, image) || back.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("read the image back from shop-media: %s %s %q, %v", back.Status, back.Header.Get("Content-Type"), stored, err)
	}

	// shop-stock, on its Rust server (D57), over shop-db's Inventory, which
	// the engine keeps (D70): anyone reads the stock, which holds none of
	// the new product, and is refused counting it; staff, who hold
	// stock.write, count three on the shelf, low at two, and anyone sees
	// it.
	anyone, err := stocksdk.New(stocksdk.SDKConfig{BaseURL: env.servers["shop-stock"]})
	if err != nil {
		t.Fatal(err)
	}
	if level, err := anyone.StockNamespace.GetStock(ctx, product.Id.String()); err != nil || level.OnHand != 0 {
		t.Fatalf("GetStock of a product never stocked = %+v, %v; want none", level, err)
	}
	_, err = anyone.StockNamespace.SetStock(ctx, product.Id.String(), stocktypes.SetStockInput{Quantity: 3})
	var unauthenticated *stocksdk.AuthenticationError
	if !errors.As(err, &unauthenticated) {
		t.Fatalf("SetStock without a session: %v", err)
	}
	staff, err := stocksdk.New(stocksdk.SDKConfig{BaseURL: env.servers["shop-stock"], Auth: &stocksdk.AuthConfig{Token: token}})
	if err != nil {
		t.Fatal(err)
	}
	reorderAt := stocktypes.InputField[stocktypes.GenericInt64]{Set: true, Value: 2}
	if level, err := staff.StockNamespace.SetStock(ctx, product.Id.String(), stocktypes.SetStockInput{Quantity: 3, ReorderAt: reorderAt}); err != nil ||
		level.OnHand != 3 || level.Available != 3 || level.ReorderAt == nil || *level.ReorderAt != 2 || level.Low {
		t.Fatalf("SetStock = %+v, %v", level, err)
	}
	stock, err := anyone.StockNamespace.ListStock(ctx)
	if err != nil || stockOf(stock, product.Id) != 3 {
		t.Fatalf("ListStock = %+v, %v; want three of %s", stock, err, product.Id)
	}

	// A collection point, which staff who hold calendar.manage open through
	// shop-db's engine routes, which shop-orders' server mounts.
	engineRoutes := env.servers["shop-orders"] + "/engine/shop-db/namespaces/shop-db/schemas"
	counter := mustUUID(t, fmt.Sprintf("00000000-0000-4000-9000-%012d", n%1_000_000_000_000)).String()
	if status, body := call(t, http.MethodPost, engineRoutes+"/CollectionPoint/instances", token,
		fmt.Sprintf(`{"id": %q, "data": {"name": "Front counter"}}`, counter)); status != http.StatusCreated {
		t.Fatalf("open a collection point: %d %s", status, body)
	}

	// shop-cart, on Bun over the engine: staff, as a shopper would, open a
	// cart, put two green teas in it, and choose the first pickup slot the
	// counter has two days on.
	carts := env.servers["shop-cart"]
	status, body := call(t, http.MethodPost, carts+"/api/carts", token, "")
	var opened struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if status != http.StatusCreated || json.Unmarshal(body, &opened) != nil || opened.Data.ID == "" {
		t.Fatalf("open a cart: %d %s", status, body)
	}
	cart := carts + "/api/carts/" + opened.Data.ID
	if status, body := call(t, http.MethodPost, cart+"/lines", token, fmt.Sprintf(`{"productId": %q, "quantity": 2}`, product.Id)); status != http.StatusOK {
		t.Fatalf("add a cart line: %d %s", status, body)
	}
	from := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	status, body = call(t, http.MethodGet, carts+"/api/pickup-slots?days=7&from="+from, "", "")
	var slots struct {
		Data []struct {
			CollectionPointID string `json:"collectionPointId"`
			StartsAt          string `json:"startsAt"`
		} `json:"data"`
	}
	if status != http.StatusOK || json.Unmarshal(body, &slots) != nil || len(slots.Data) == 0 || slots.Data[0].CollectionPointID != counter {
		t.Fatalf("the pickup slots: %d %s", status, body)
	}
	if status, body := call(t, http.MethodPut, cart+"/pickup", token,
		fmt.Sprintf(`{"collectionPointId": %q, "startsAt": %q}`, counter, slots.Data[0].StartsAt)); status != http.StatusOK {
		t.Fatalf("choose a pickup slot: %d %s", status, body)
	}

	// shop-orders: staff start the checkout, which holds two teas and the
	// slot, all or none; another cart of two finds one free and holds
	// nothing.
	orders, err := orderssdk.New(orderssdk.SDKConfig{BaseURL: env.servers["shop-orders"], Auth: &orderssdk.AuthConfig{Token: token}})
	if err != nil {
		t.Fatal(err)
	}
	checkoutID := opened.Data.ID
	started, err := orders.CheckoutsNamespace.StartCheckout(ctx, checkoutID, orderstypes.StartCheckoutInput{ShippingAddress: address})
	if err != nil {
		t.Fatalf("StartCheckout: %v", err)
	}
	if started.Status != orderstypes.CheckoutStatus_Held || started.DueCents != 900 || started.Currency != "GBP" {
		t.Fatalf("StartCheckout returned %+v", started)
	}
	if level, err := anyone.StockNamespace.GetStock(ctx, product.Id.String()); err != nil || level.Held != 2 || level.Available != 1 {
		t.Fatalf("after the checkout, GetStock = %+v, %v; want two held", level, err)
	}
	status, body = call(t, http.MethodPost, carts+"/api/carts", token, "")
	var second struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if status != http.StatusCreated || json.Unmarshal(body, &second) != nil {
		t.Fatalf("open a second cart: %d %s", status, body)
	}
	if status, body := call(t, http.MethodPost, carts+"/api/carts/"+second.Data.ID+"/lines", token, fmt.Sprintf(`{"productId": %q, "quantity": 2}`, product.Id)); status != http.StatusOK {
		t.Fatalf("add a line to the second cart: %d %s", status, body)
	}
	_, err = orders.CheckoutsNamespace.StartCheckout(ctx, second.Data.ID, orderstypes.StartCheckoutInput{ShippingAddress: address})
	var refused *orderssdk.APIError
	if !errors.As(err, &refused) || refused.StatusCode != http.StatusConflict || !strings.Contains(string(refused.Body), "unavailable") {
		t.Fatalf("StartCheckout beyond the stock: %v, want 409 unavailable", err)
	}

	// acmepay, the fake provider, at the address shop-orders' ACMEPAY_URL
	// defaults to, with the environment's keys: staff pay on its page with
	// a test token, it posts the capture to shop-orders' webhook, signed,
	// and the runner's Reactions rule places the checkout, whose order
	// shop-orders writes and FulfilOrders fulfils.
	payment, err := orders.CheckoutsNamespace.PayCheckout(ctx, checkoutID)
	if err != nil {
		t.Fatalf("PayCheckout: %v", err)
	}
	if payment.AmountCents != 900 || payment.Currency != "GBP" {
		t.Fatalf("PayCheckout returned %+v", payment)
	}
	if _, err := pay.client.ConfirmIntent(ctx, payment.IntentId, payment.ClientSecret, acmepay.TokenSucceeds); err != nil {
		t.Fatalf("pay at acmepay: %v", err)
	}
	if events := pay.provider.Events(); !pay.provider.Delivered(events[len(events)-1].ID) {
		t.Fatalf("acmepay could not deliver %+v to shop-orders", events[len(events)-1])
	}
	placedCheckout := waitForCheckout(ctx, t, orders, checkoutID, time.Minute)
	if placedCheckout.Payment != "paid" || placedCheckout.OrderId == nil {
		t.Fatalf("the paid checkout is %+v", placedCheckout)
	}
	order, err := orders.OrderNamespace.GetOrder(ctx, placedCheckout.OrderId.String())
	if err != nil || order.TotalCents != 900 || len(order.Lines) != 1 || order.Lines[0].UnitPriceCents != 450 {
		t.Fatalf("the checkout's order is %+v, %v", order, err)
	}
	if level, err := anyone.StockNamespace.GetStock(ctx, product.Id.String()); err != nil || level.OnHand != 1 || level.Held != 0 || !level.Low {
		t.Fatalf("after the sale, GetStock = %+v, %v; want one left, low", level, err)
	}
	// shop-orders told shop-stock what the checkout sold, through its Go
	// SDK with its service credential (D37, D57), and shop-stock enqueued
	// StockLow for the product the sale left low.
	for _, want := range []string{"[shop-stock] ", `"msg":"took a sale"`, `"caller":"shop-orders"`, `"api":"shop-stock"`} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("stack dev printed no %q", want)
		}
	}
	// The sale is one trace across two languages' runtimes (D67): a span of
	// shop-orders' call to shop-stock, in Go, whose child is shop-stock's
	// Rust span of stockSold, named by its route.
	sold := traces.Wait(t, 30*time.Second, "of shop-stock's stockSold for shop-orders", func(s otlptest.Span) bool {
		return s.Service() == "shop-stock" && s.Kind == otlptest.KindServer && s.Name == "POST /api/stock/sold" && s.ParentSpanID != ""
	})
	telling := traces.Wait(t, 30*time.Second, "of shop-orders' call to shop-stock", func(s otlptest.Span) bool {
		return s.SpanID == sold.ParentSpanID
	})
	if telling.Service() != "shop-orders" || telling.Kind != otlptest.KindClient || telling.TraceID != sold.TraceID {
		t.Fatalf("the sale's spans are %v and %v; want shop-orders' call and shop-stock's stockSold, in one trace", telling, sold)
	}
	// Only shop-orders tells shop-stock of a sale: the session alone, with
	// no service credential, is refused.
	if status, body := call(t, http.MethodPost, env.servers["shop-stock"]+"/api/stock/sold", token,
		fmt.Sprintf(`{"lines": [{"productId": %q, "quantity": 1}]}`, product.Id)); status != http.StatusUnauthorized || !strings.Contains(string(body), "service_unauthorized") {
		t.Fatalf("a sale without a service credential: %d %s, want 401", status, body)
	}

	// A refund of one tea, which two members of staff who hold
	// refunds.approve sign off through the engine's routes; SettlePayments,
	// run with stack run, makes it at acmepay, and the runner issues it.
	refund, err := orders.CheckoutsNamespace.RequestRefund(ctx, checkoutID, orderstypes.RequestRefundInput{AmountCents: 450, Reason: "one arrived crushed"})
	if err != nil || refund.Status != "requested" {
		t.Fatalf("RequestRefund = %+v, %v", refund, err)
	}
	refundRoute := engineRoutes + "/Refund/instances/" + refund.Id.String()
	for _, name := range []string{"Barbara Liskov", "Katherine Johnson"} {
		approver := signInOnTheStack(ctx, t, env.database, env.servers["shop-api"], name, "refunds.approve")
		if status, body := call(t, http.MethodPost, refundRoute+"/operations/signOff", approver, `{"state": "approved"}`); status != http.StatusOK {
			t.Fatalf("%s's sign-off: %d %s", name, status, body)
		}
		if name == "Katherine Johnson" {
			if status, body := call(t, http.MethodPost, refundRoute+"/operations/transition", approver, `{"to": "approved"}`); status != http.StatusOK {
				t.Fatalf("approve the refund: %d %s", status, body)
			}
		}
	}
	run := runJob(t, binary, settlePayments)
	if !strings.Contains(run, "job "+settlePayments+": the run stack run asked for, try 1 of 2 succeeded") {
		t.Fatalf("stack run printed:\n%s", run)
	}
	waitForRefund(ctx, t, orders, checkoutID, time.Minute)
	if intent, _ := pay.provider.Intent(payment.IntentId); intent.Refunded != 450 {
		t.Fatalf("acmepay refunded %d of the payment, want 450", intent.Refunded)
	}
	if !strings.Contains(output.String(), "job "+settlePayments+" runs on * * * * * (UTC)") {
		t.Fatalf("stack dev did not schedule %s", settlePayments)
	}

	// shop-orders' worker FulfilOrders claims the OrderPlaced message
	// PlaceOrder enqueued with the order, and fulfils the order (D53).
	if !strings.Contains(output.String(), "worker   "+fulfilOrders+" handles OrderPlaced of shop-db, 4 at a time") {
		t.Fatalf("stack dev did not run %s", fulfilOrders)
	}
	waitForStatus(ctx, t, orders, order.Id, orderstypes.OrderStatus_Fulfilled, time.Minute)
	for _, want := range []string{"[" + fulfilOrders + "] ", `"msg":"worker started"`, `"msg":"fulfilled the order"`, `"msg":"message handled"`} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("stack dev printed no %q", want)
		}
	}
	traces.Wait(t, 30*time.Second, "of the message "+fulfilOrders+" handled", func(s otlptest.Span) bool {
		return s.Service() == fulfilOrders && s.Kind == otlptest.KindConsumer && s.Name == "process OrderPlaced"
	})

	// shop-stock's worker ReorderStock, a Rust process (D65), claims the
	// StockLow message the sale enqueued, and files the product's one open
	// restock request, for enough units to bring its level of one back to
	// twice its reorder level of two.
	if !strings.Contains(output.String(), "worker   "+reorderStock+" handles StockLow of shop-db, 2 at a time") {
		t.Fatalf("stack dev did not run %s", reorderStock)
	}
	filed := waitForRestock(ctx, t, env.database, product.Id, "open", time.Minute)
	if filed.quantity != 3 || filed.orderedAt != nil {
		t.Fatalf("the restock request ReorderStock filed is %+v, want three units, not ordered", filed)
	}
	for _, want := range []string{"[" + reorderStock + "] ", `"msg":"filed a restock request"`} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("stack dev printed no %q", want)
		}
	}
	traces.Wait(t, 30*time.Second, "of the message "+reorderStock+" handled", func(s otlptest.Span) bool {
		return s.Service() == reorderStock && s.Kind == otlptest.KindConsumer && s.Name == "process StockLow"
	})

	// shop-storefront, on Bun: a shopper puts two green teas in a cart,
	// and a viewer reads the cart back. Its implementation keeps carts in
	// memory and knows its callers by static tokens.
	storefront := env.servers["shop-storefront"]
	cart = fmt.Sprintf("%s/api/carts/00000000-0000-4000-8000-%012d", storefront, n%1_000_000_000_000)
	if status, body := call(t, http.MethodPost, cart+"/lines", "shopper-token", `{"sku": "green-tea", "quantity": 2}`); status != http.StatusCreated {
		t.Fatalf("add a cart line: %d %s", status, body)
	}
	status, body = call(t, http.MethodGet, cart, "viewer-token", "")
	var read struct {
		Data struct {
			Lines []struct {
				Sku      string `json:"sku"`
				Quantity int    `json:"quantity"`
			} `json:"lines"`
			Total struct {
				AmountCents int    `json:"amountCents"`
				Currency    string `json:"currency"`
			} `json:"total"`
		} `json:"data"`
	}
	if status != http.StatusOK || json.Unmarshal(body, &read) != nil || len(read.Data.Lines) != 1 || read.Data.Lines[0].Quantity != 2 ||
		read.Data.Total.AmountCents != 900 || read.Data.Total.Currency != "EUR" {
		t.Fatalf("read the cart: %d %s", status, body)
	}
	if status, body := call(t, http.MethodGet, cart, "", ""); status != http.StatusUnauthorized {
		t.Fatalf("read the cart without a token: %d %s, want 401", status, body)
	}
	// The storefront's TypeScript server made a span of each request, named
	// by its route (D67).
	traces.Wait(t, 30*time.Second, "of the storefront's new cart line", func(s otlptest.Span) bool {
		return s.Service() == "shop-storefront" && s.Kind == otlptest.KindServer &&
			strings.HasPrefix(s.Name, "POST /api/carts/{") && strings.HasSuffix(s.Name, "}/lines")
	})

	// shop-web, the static site, which stack dev built once and serves
	// from a file server of its own (D55): its page, its config with
	// shop-api's address, and the page again for a route of the
	// application. shop-api answers CORS for the site's origin, which its
	// CORS field lists, and for no other: the site's CORS answers that
	// origin's preflight itself, 204, and hands another origin's on to
	// shop-api's router (D55, amended). There the identity runtime's CORS
	// answers the trusted origins of its config, which in Dev trusts none,
	// and the router, which routes no OPTIONS, answers 405 with no
	// Access-Control-Allow-Origin, so the browser refuses it.
	site := env.site
	if site == "" {
		t.Fatalf("environment Dev has no site shop-web")
	}
	if status, body := call(t, http.MethodGet, site+"/", "", ""); status != http.StatusOK || !strings.Contains(string(body), "<title>acme shop</title>") {
		t.Fatalf("the site's page: %d %s", status, body)
	}
	status, body = call(t, http.MethodGet, site+"/__superschematic/config.json", "", "")
	var config struct {
		APIs map[string]struct {
			URL string `json:"url"`
		} `json:"apis"`
	}
	if status != http.StatusOK || json.Unmarshal(body, &config) != nil || config.APIs["shop-api"].URL != env.servers["shop-api"] {
		t.Fatalf("the site's config: %d %s, want shop-api at %s", status, body, env.servers["shop-api"])
	}
	if status, body := call(t, http.MethodGet, site+"/products/42", "", ""); status != http.StatusOK || !strings.Contains(string(body), "<title>acme shop</title>") {
		t.Fatalf("a route of the site's application: %d %s, want the page", status, body)
	}
	listing := env.servers["shop-api"] + "/api/products?inStock=true"
	if status, allowed := preflight(t, listing, site); status != http.StatusNoContent ||
		allowed.Get("Access-Control-Allow-Origin") != site || allowed.Get("Access-Control-Allow-Credentials") != "true" ||
		!strings.Contains(allowed.Get("Access-Control-Allow-Headers"), "Authorization") || !strings.Contains(allowed.Get("Access-Control-Allow-Methods"), "GET") {
		t.Fatalf("shop-api's answer to the site's preflight: %d %v, want 204 allowing the site", status, allowed)
	}
	if status, refused := preflight(t, listing, "https://evil.example"); status != http.StatusMethodNotAllowed ||
		refused.Get("Access-Control-Allow-Origin") != "" || refused.Get("Access-Control-Allow-Credentials") != "" {
		t.Fatalf("shop-api's answer to another origin's preflight: %d %v, want 405 allowing no origin", status, refused)
	}

	// shop-orders' job ShipOrders ships every fulfilled order (D52).
	// `superschematic stack run` runs it once, from another terminal,
	// against the environment stack dev runs.
	run = runJob(t, binary, shipOrders)
	for _, want := range []string{
		"job " + shipOrders + ": start the run stack run asked for, try 1 of 2",
		"job " + shipOrders + ": the run stack run asked for, try 1 of 2 succeeded",
		"[" + shipOrders + "] ",
	} {
		if !strings.Contains(run, want) {
			t.Fatalf("stack run printed no %q:\n%s", want, run)
		}
	}
	if got := orderStatus(ctx, t, orders, order.Id); got != orderstypes.OrderStatus_Shipped {
		t.Fatalf("after stack run, order %s is %s, want shipped", order.Id, got)
	}
	// The job's process flushed the span of its run before it exited.
	traces.Wait(t, 30*time.Second, "of the run of "+shipOrders, func(s otlptest.Span) bool {
		return s.Service() == shipOrders && s.Kind == otlptest.KindInternal && s.Name == "ShipOrders"
	})

	// shop-stock's job OrderRestocks, a Rust job (D65), runs only on
	// demand in Dev, whose settings turn its schedule off. stack run runs
	// it, and it orders the open restock request.
	if !strings.Contains(output.String(), "job "+orderRestocks+" runs only on demand") {
		t.Fatalf("stack dev scheduled %s", orderRestocks)
	}
	run = runJob(t, binary, orderRestocks)
	for _, want := range []string{
		"job " + orderRestocks + ": start the run stack run asked for, try 1 of 2",
		"job " + orderRestocks + ": the run stack run asked for, try 1 of 2 succeeded",
		"[" + orderRestocks + "] ",
		`"msg":"placed the open restock orders"`,
	} {
		if !strings.Contains(run, want) {
			t.Fatalf("stack run printed no %q:\n%s", want, run)
		}
	}
	if ordered := restockRequestsOf(ctx, t, env.database, product.Id); len(ordered) != 1 || ordered[0].status != "ordered" || ordered[0].open || ordered[0].orderedAt == nil {
		t.Fatalf("after stack run, the product's restock requests are %+v, want the one ordered", ordered)
	}
	traces.Wait(t, 30*time.Second, "of the run of "+orderRestocks, func(s otlptest.Span) bool {
		return s.Service() == orderRestocks && s.Kind == otlptest.KindInternal && s.Name == "OrderRestocks"
	})

	// Dev's settings run the job every minute, so stack dev ships an order
	// placed now within about one, once the worker fulfils it. PlaceOrder
	// takes an order in one step, holding no stock and taking no payment.
	if !strings.Contains(output.String(), "job "+shipOrders+" runs on * * * * * (UTC)") {
		t.Fatalf("stack dev did not schedule %s", shipOrders)
	}
	next, err := orders.OrderNamespace.PlaceOrder(ctx, orderstypes.PlaceOrderInput{
		Lines:           []orderstypes.PlaceOrderLine{{ProductId: product.Id, Quantity: 1}},
		ShippingAddress: address,
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	waitForStatus(ctx, t, orders, next.Id, orderstypes.OrderStatus_Shipped, 3*time.Minute)
	for _, want := range []string{"job " + shipOrders + ": start the run due at ", "[" + shipOrders + "] "} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("stack dev printed no %q", want)
		}
	}

	// shop-orders' lists over Postgres, a page at a time (D60). Its catalog,
	// which anyone reads, holds the product and a second one staff add: by
	// name, one to a page, through the lookup of their ids (D61).
	other, err := products.ProductNamespace.CreateProduct(ctx, apitypes.CreateProductInput{
		Sku:        apitypes.IdentitySlug(fmt.Sprintf("black-tea-%d", n)),
		Name:       "Black tea",
		PriceCents: 350,
	})
	if err != nil {
		t.Fatalf("CreateProduct: %v", err)
	}
	catalog, err := orderssdk.New(orderssdk.SDKConfig{BaseURL: env.servers["shop-orders"]})
	if err != nil {
		t.Fatal(err)
	}
	ours := &ordersns.ProductCatalogListProductsQueryParams{Ids: []orderstypes.IdentityUUID{product.Id, other.Id}}
	first, err := catalog.ProductCatalogNamespace.ListProducts(ctx, ours, orderspages.PageRequest{Limit: 1})
	if err != nil || len(first.Items) != 1 || first.Items[0].Id != other.Id || first.Next == nil {
		t.Fatalf("ListProducts, the first page of one: %+v, %v; want the black tea and a next page", first, err)
	}
	last, err := catalog.ProductCatalogNamespace.ListProducts(ctx, ours, orderspages.PageRequest{Cursor: *first.Next, Limit: 1})
	if err != nil || len(last.Items) != 1 || last.Items[0].Id != product.Id || last.Next != nil {
		t.Fatalf("ListProducts, the page after: %+v, %v; want the green tea and no next page", last, err)
	}
	if got, err := catalog.ProductCatalogNamespace.GetProduct(ctx, product.Id.String()); err != nil || got.Sku != product.Sku || got.PriceCents != 450 {
		t.Fatalf("GetProduct = %+v, %v", got, err)
	}

	// The catalog by price, which compares as a number, either way, across
	// pages of one (D60, amended), and searched by name, the search a
	// product picker finds them by. A cursor of the pages by name is
	// refused by a page by price: the router binds each cursor to its sort.
	pricesOf := func(sort string) []orderstypes.GenericInt64 {
		t.Helper()
		query := &ordersns.ProductCatalogListProductsQueryParams{Ids: ours.Ids, Sort: &sort}
		var prices []orderstypes.GenericInt64
		for listed, err := range catalog.ProductCatalogNamespace.ListProductsAll(ctx, query, orderspages.PageRequest{Limit: 1}) {
			if err != nil {
				t.Fatalf("ListProductsAll by %s: %v", sort, err)
			}
			prices = append(prices, listed.PriceCents)
		}
		return prices
	}
	if got := pricesOf("priceCents"); !slices.Equal(got, []orderstypes.GenericInt64{350, 450}) {
		t.Fatalf("the products by price = %v, want 350 then 450", got)
	}
	if got := pricesOf("-priceCents"); !slices.Equal(got, []orderstypes.GenericInt64{450, 350}) {
		t.Fatalf("the products by price, the dearest first = %v, want 450 then 350", got)
	}
	black := "BLACK"
	found, err := catalog.ProductCatalogNamespace.ListProducts(ctx, &ordersns.ProductCatalogListProductsQueryParams{Ids: ours.Ids, Q: &black}, orderspages.PageRequest{})
	if err != nil || len(found.Items) != 1 || found.Items[0].Id != other.Id {
		t.Fatalf("ListProducts q=BLACK = %+v, %v; want the black tea", found, err)
	}
	byPrice := "priceCents"
	if _, err := catalog.ProductCatalogNamespace.ListProducts(ctx, &ordersns.ProductCatalogListProductsQueryParams{Ids: ours.Ids, Sort: &byPrice},
		orderspages.PageRequest{Cursor: *first.Next, Limit: 1}); !errors.As(err, &refused) || refused.StatusCode != http.StatusBadRequest ||
		!strings.Contains(string(refused.Body), "cursor is not one this sort returned") {
		t.Fatalf("ListProducts by price after a cursor of the products by name: %v, want 400", err)
	}

	// The orders, oldest first: a page of one order at a time holds each
	// order once, in the order of one page of them all, the two staff
	// placed last, and a status filter holds on every page.
	var paged []orderstypes.IdentityUUID
	for listed, err := range orders.OrderNamespace.ListOrdersAll(ctx, nil, orderspages.PageRequest{Limit: 1}) {
		if err != nil {
			t.Fatalf("ListOrdersAll: %v", err)
		}
		paged = append(paged, listed.Id)
	}
	whole, err := orders.OrderNamespace.ListOrders(ctx, nil, orderspages.PageRequest{Limit: 100})
	if err != nil || whole.Next != nil || len(whole.Items) != len(paged) {
		t.Fatalf("ListOrders of up to 100 = %d orders, next %v, %v; the pages of one held %d", len(whole.Items), whole.Next, err, len(paged))
	}
	for i, listed := range whole.Items {
		if listed.Id != paged[i] {
			t.Fatalf("order %d is %s on a page of 100 and %s on the pages of one", i, listed.Id, paged[i])
		}
	}
	if len(paged) < 2 || paged[len(paged)-2] != order.Id || paged[len(paged)-1] != next.Id {
		t.Fatalf("ListOrdersAll = %v, want %s and %s last", paged, order.Id, next.Id)
	}
	placedOnly := &ordersns.OrderListOrdersQueryParams{Statuses: []orderstypes.OrderStatus{orderstypes.OrderStatus_Placed}}
	for listed, err := range orders.OrderNamespace.ListOrdersAll(ctx, placedOnly, orderspages.PageRequest{Limit: 1}) {
		if err != nil || listed.Status != orderstypes.OrderStatus_Placed {
			t.Fatalf("ListOrdersAll of placed orders yielded %+v, %v", listed, err)
		}
	}
	if _, err := orders.OrderNamespace.ListOrders(ctx, nil, orderspages.PageRequest{Cursor: "e30"}); !errors.As(err, &refused) || refused.StatusCode != http.StatusBadRequest {
		t.Fatalf("ListOrders after a cursor it never returned: %v, want 400", err)
	}

	// Two shoppers, who hold orders.read and orders.write, order the black
	// tea, which staff stock, twice each. A shopper lists their own orders
	// alone, a page of one at a time, and another customer's order is the
	// 404 of one no one placed, to get and to cancel. Staff, whose orders
	// covers orders.any, list everyone's, theirs first, as placed, and get
	// and cancel any: the cancel answers the order cancelled, or 409 once
	// the worker has fulfilled it, never 404.
	if _, err := staff.StockNamespace.SetStock(ctx, other.Id.String(), stocktypes.SetStockInput{Quantity: 10}); err != nil {
		t.Fatalf("SetStock of the black tea: %v", err)
	}
	shoppers := []string{"Grace Hopper", "Alan Turing"}
	tokens := map[string]string{}
	placedBy := map[string][]orderstypes.IdentityUUID{}
	ordersOf := func(token string) *orderssdk.ShopOrdersSDK {
		t.Helper()
		client, err := orderssdk.New(orderssdk.SDKConfig{BaseURL: env.servers["shop-orders"], Auth: &orderssdk.AuthConfig{Token: token}})
		if err != nil {
			t.Fatal(err)
		}
		return client
	}
	listAll := func(client *orderssdk.ShopOrdersSDK) []orderstypes.IdentityUUID {
		t.Helper()
		var ids []orderstypes.IdentityUUID
		for listed, err := range client.OrderNamespace.ListOrdersAll(ctx, nil, orderspages.PageRequest{Limit: 1}) {
			if err != nil {
				t.Fatalf("ListOrdersAll: %v", err)
			}
			ids = append(ids, listed.Id)
		}
		return ids
	}
	for _, name := range shoppers {
		tokens[name] = signInOnTheStack(ctx, t, env.database, env.servers["shop-api"], name, "orders.read", "orders.write")
		for range 2 {
			placed, err := ordersOf(tokens[name]).OrderNamespace.PlaceOrder(ctx, orderstypes.PlaceOrderInput{
				Lines:           []orderstypes.PlaceOrderLine{{ProductId: other.Id, Quantity: 1}},
				ShippingAddress: address,
			})
			if err != nil {
				t.Fatalf("%s's PlaceOrder: %v", name, err)
			}
			placedBy[name] = append(placedBy[name], placed.Id)
		}
	}
	for i, name := range shoppers {
		shopper := ordersOf(tokens[name])
		if got := listAll(shopper); !slices.Equal(got, placedBy[name]) {
			t.Fatalf("%s's ListOrdersAll = %v, want their own, %v", name, got, placedBy[name])
		}
		theirs := placedBy[shoppers[1-i]][0]
		if _, err := shopper.OrderNamespace.GetOrder(ctx, theirs.String()); !errors.As(err, &refused) || refused.StatusCode != http.StatusNotFound {
			t.Fatalf("%s's GetOrder of another customer's order: %v, want 404", name, err)
		}
		reason := "not mine"
		if _, err := shopper.OrderNamespace.CancelOrder(ctx, theirs.String(), ordersns.OrderCancelOrderInput{Reason: &reason}); !errors.As(err, &refused) || refused.StatusCode != http.StatusNotFound {
			t.Fatalf("%s's CancelOrder of another customer's order: %v, want 404", name, err)
		}
	}
	everyone := slices.Concat(paged, placedBy[shoppers[0]], placedBy[shoppers[1]])
	if got := listAll(orders); !slices.Equal(got, everyone) {
		t.Fatalf("staff's ListOrdersAll = %v, want %v", got, everyone)
	}
	for _, name := range shoppers {
		for _, id := range placedBy[name] {
			got, err := orders.OrderNamespace.GetOrder(ctx, id.String())
			if err != nil || got.Id != id || got.Status == orderstypes.OrderStatus_Cancelled {
				t.Fatalf("staff's GetOrder of %s's order %s = %+v, %v; want it, not cancelled", name, id, got, err)
			}
		}
	}
	// Every order by its total, the largest first, a page of one at a
	// time: the stored totals, in order, ties broken by their ids in the
	// same direction, as Postgres orders a uuid; and a shopper's own alike.
	byTotal := func(client *orderssdk.ShopOrdersSDK, sort string) []orderstypes.OrderView {
		t.Helper()
		var listed []orderstypes.OrderView
		for order, err := range client.OrderNamespace.ListOrdersAll(ctx, &ordersns.OrderListOrdersQueryParams{Sort: &sort}, orderspages.PageRequest{Limit: 1}) {
			if err != nil {
				t.Fatalf("ListOrdersAll by %s: %v", sort, err)
			}
			listed = append(listed, order)
		}
		return listed
	}
	inTotalOrder := func(listed []orderstypes.OrderView, desc bool) bool {
		return slices.IsSortedFunc(listed, func(a, b orderstypes.OrderView) int {
			ida, idb := a.Id.ToUUID(), b.Id.ToUUID()
			order := cmp.Or(cmp.Compare(a.TotalCents, b.TotalCents), bytes.Compare(ida[:], idb[:]))
			if desc {
				return -order
			}
			return order
		})
	}
	largest := byTotal(orders, "-totalCents")
	if len(largest) != len(everyone) || !inTotalOrder(largest, true) {
		t.Fatalf("staff's orders by total, the largest first = %+v, want every one of %d in that order", largest, len(everyone))
	}
	for _, order := range largest {
		var lines orderstypes.GenericInt64
		for _, line := range order.Lines {
			lines += line.Quantity * line.UnitPriceCents
		}
		if order.TotalCents != lines {
			t.Fatalf("order %s's total is %d, and its lines sum to %d", order.Id, order.TotalCents, lines)
		}
	}
	if own := byTotal(ordersOf(tokens[shoppers[0]]), "totalCents"); len(own) != 2 || !inTotalOrder(own, false) {
		t.Fatalf("%s's orders by total = %+v, want their two in that order", shoppers[0], own)
	}

	reason := "out of stock"
	cancelled, err := orders.OrderNamespace.CancelOrder(ctx, placedBy[shoppers[1]][1].String(), ordersns.OrderCancelOrderInput{Reason: &reason})
	if (err != nil || cancelled.Status != orderstypes.OrderStatus_Cancelled) && (!errors.As(err, &refused) || refused.StatusCode != http.StatusConflict) {
		t.Fatalf("staff's CancelOrder of %s's order = %+v, %v; want it cancelled, or 409 once fulfilled", shoppers[1], cancelled, err)
	}

	// The product's reviews, searched by every word over Postgres's
	// search_text, in any case, a % matching itself (D62), and paged: staff
	// and a shopper review it.
	reviewer := tokens[shoppers[0]]
	for _, review := range []struct {
		token string
		input orderstypes.WriteReviewInput
	}{
		{token, orderstypes.WriteReviewInput{Rating: 5, Title: "Smoky and smooth", Body: "A deep cup."}},
		{reviewer, orderstypes.WriteReviewInput{Rating: 3, Title: "Grassy", Body: "100% green, and SMOOTH enough."}},
	} {
		writer, err := orderssdk.New(orderssdk.SDKConfig{BaseURL: env.servers["shop-orders"], Auth: &orderssdk.AuthConfig{Token: review.token}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.ProductReviewsNamespace.WriteReview(ctx, product.Id.String(), review.input); err != nil {
			t.Fatalf("WriteReview %q: %v", review.input.Title, err)
		}
	}
	search := func(q string, page orderspages.PageRequest) orderspages.Page[orderstypes.ReviewView] {
		t.Helper()
		found, err := catalog.ProductReviewsNamespace.ListReviews(ctx, product.Id.String(), &ordersns.ProductReviewsListReviewsQueryParams{Q: &q}, page)
		if err != nil {
			t.Fatalf("ListReviews %q: %v", q, err)
		}
		return found
	}
	for q, want := range map[string]string{
		"smooth":       "Smoky and smooth | Grassy",
		"SMOKY smooth": "Smoky and smooth",
		"100%":         "Grassy",
		"1%0":          "",
		"  ":           "Smoky and smooth | Grassy",
	} {
		var titles []string
		for _, review := range search(q, orderspages.PageRequest{}).Items {
			titles = append(titles, review.Title)
		}
		if got := strings.Join(titles, " | "); got != want {
			t.Errorf("ListReviews %q = %q, want %q", q, got, want)
		}
	}
	page := search("smooth", orderspages.PageRequest{Limit: 1})
	if len(page.Items) != 1 || page.Items[0].Title != "Smoky and smooth" || page.Next == nil {
		t.Fatalf("ListReviews smooth, the first page of one: %+v", page)
	}
	page = search("smooth", orderspages.PageRequest{Cursor: *page.Next, Limit: 1})
	if len(page.Items) != 1 || page.Items[0].Title != "Grassy" || page.Next != nil {
		t.Fatalf("ListReviews smooth, the page after: %+v", page)
	}
	// By rating, with the search, either way, a page of one at a time.
	for sort, want := range map[string]string{"rating": "Grassy | Smoky and smooth", "-rating": "Smoky and smooth | Grassy"} {
		q := "smooth"
		var titles []string
		for review, err := range catalog.ProductReviewsNamespace.ListReviewsAll(ctx, product.Id.String(),
			&ordersns.ProductReviewsListReviewsQueryParams{Q: &q, Sort: &sort}, orderspages.PageRequest{Limit: 1}) {
			if err != nil {
				t.Fatalf("ListReviewsAll by %s: %v", sort, err)
			}
			titles = append(titles, review.Title)
		}
		if got := strings.Join(titles, " | "); got != want {
			t.Errorf("ListReviewsAll by %s = %q, want %q", sort, got, want)
		}
	}

	// Ctrl-C stops the servers, then removes the containers and their data:
	// Postgres's and the storage emulator's.
	dev.stop(t)

	// Once more, into an output root of its own (D65): stack dev --out
	// builds every service and the stack there, mirrors rust-impl/shop-stock
	// into the root's impl/shop-stock, and builds shop-stock's server,
	// worker and job against the root's own API crates. The root starts
	// with the committed Cargo.lock, so they build the versions the first
	// run built, and its Cargo target directory is the first run's, so
	// cargo compiles the crates the root generates and reuses every other.
	// It mirrors typescript/shop-storefront into impl/shop-storefront too,
	// and the site web/shop-web into impl/shop-web, where the site builds,
	// and its Bun workspace lists the mirrors in place of the project's
	// packages, so its install leaves their node_modules as the first
	// run's install linked them, to outputRoot (D65, amended).
	links := projectLinks(t, outputRoot)
	otherRoot := otherOutputRoot(t, outputRoot)
	again := startStackDev(t, binary, shopStack, "--out", otherRoot)
	againEnv := again.waitUntilRunning(t, otherRoot, "shop-stack", "shop-api", "shop-orders", "shop-stock", "shop-cart", "shop-storefront")
	for mirror, implementation := range map[string]string{
		filepath.Join(otherRoot, "impl", "shop-stock", "src", "lib.rs"):      filepath.Join("..", "rust-impl", "shop-stock", "src", "lib.rs"),
		filepath.Join(otherRoot, "impl", "shop-storefront", "index.ts"):      filepath.Join("..", "typescript", "shop-storefront", "index.ts"),
		filepath.Join(otherRoot, "impl", "shop-storefront", "tsconfig.json"): filepath.Join("..", "typescript", "shop-storefront", "tsconfig.json"),
		filepath.Join(otherRoot, "impl", "shop-web", "index.html"):           filepath.Join("..", "web", "shop-web", "index.html"),
		filepath.Join(otherRoot, "impl", "shop-web", "config.generated.ts"):  filepath.Join("..", "web", "shop-web", "config.generated.ts"),
	} {
		mirrored, err := os.ReadFile(mirror)
		if err != nil {
			t.Fatal(err)
		}
		if source, err := os.ReadFile(implementation); err != nil || !bytes.Equal(mirrored, source) {
			t.Fatalf("the output root %s holds no mirror of %s (%v)", otherRoot, implementation, err)
		}
	}
	if status := get(t, againEnv.servers["shop-storefront"]+"/readyz"); status != http.StatusOK {
		t.Fatalf("shop-storefront built in %s answered GET /readyz %d", otherRoot, status)
	}
	// shop-web built in its mirror, which its file server serves, and not
	// in the project's package.
	if _, err := os.Stat(filepath.Join(otherRoot, "impl", "shop-web", "dist", "index.html")); err != nil {
		t.Fatalf("shop-web's build in %s wrote no page into its mirror: %v", otherRoot, err)
	}
	if status := get(t, againEnv.site+"/"); status != http.StatusOK {
		t.Fatalf("shop-web built in %s answered GET / %d", otherRoot, status)
	}
	if got := projectLinks(t, outputRoot); !maps.Equal(got, links) {
		t.Fatalf("stack dev --out %s relinked the project's TypeScript packages:\n%v\nwas\n%v", otherRoot, got, links)
	}
	if !strings.Contains(again.output.String(), "worker   "+reorderStock+" handles StockLow of shop-db, 2 at a time") {
		t.Fatalf("stack dev --out did not run %s", reorderStock)
	}
	if status := get(t, againEnv.servers["shop-stock"]+"/api/stock"); status != http.StatusOK {
		t.Fatalf("shop-stock built in %s answered GET /api/stock %d", otherRoot, status)
	}
	run = runJob(t, binary, orderRestocks, "--out", otherRoot)
	if !strings.Contains(run, "job "+orderRestocks+": the run stack run asked for, try 1 of 2 succeeded") {
		t.Fatalf("stack run --out printed:\n%s", run)
	}
	again.stop(t)
}

// shipOrders and settlePayments are the deployables of shop-orders' jobs
// ShipOrders and SettlePayments: the API's name, then the job's class in
// kebab case.
const (
	shipOrders     = "shop-orders-ship-orders"
	settlePayments = "shop-orders-settle-payments"
)

// fulfilOrders is the deployable of shop-orders' worker FulfilOrders,
// named as a job's is (D53).
const fulfilOrders = "shop-orders-fulfil-orders"

// reorderStock and orderRestocks are the deployables of shop-stock's
// Rust worker ReorderStock and job OrderRestocks (D65).
const (
	reorderStock  = "shop-stock-reorder-stock"
	orderRestocks = "shop-stock-order-restocks"
)

// shopStack is shop-stack's service directory, relative to the example's.
// The example holds two stacks, shop-stack and storefront-stack, so each
// stack command names its stack.
const shopStack = "schemas/services/shop-stack"

// stackDev is a run of `superschematic stack dev --remove-data` from the
// example's directory, as a person would start it, and what it prints.
type stackDev struct {
	cmd        *exec.Cmd
	output     *lockedBuffer
	exited     chan error
	stopped    bool
	containers []string
}

// startStackDev starts stack dev with flags. On a failure, the test's
// cleanup stops it as Ctrl-C does, removes the containers whatever it
// left, and logs what it printed.
func startStackDev(t *testing.T, binary, stack string, flags ...string) *stackDev {
	t.Helper()
	dev := &stackDev{output: &lockedBuffer{}, exited: make(chan error, 1)}
	dev.cmd = exec.Command(binary, append([]string{"stack", "dev", stack, "--remove-data"}, flags...)...)
	dev.cmd.Dir = ".."
	dev.cmd.Stdout, dev.cmd.Stderr = dev.output, dev.output
	if err := dev.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { dev.exited <- dev.cmd.Wait() }()
	t.Cleanup(func() {
		if !dev.stopped {
			_ = dev.cmd.Process.Signal(os.Interrupt)
			select {
			case <-dev.exited:
			case <-time.After(time.Minute):
				_ = dev.cmd.Process.Kill()
			}
		}
		for _, container := range dev.containers {
			_ = exec.Command("docker", "rm", "--force", "--volumes", container).Run()
		}
		if t.Failed() {
			t.Logf("%s printed:\n%s", strings.Join(dev.cmd.Args[1:], " "), dev.output)
		}
	})
	return dev
}

// waitUntilRunning waits for stack dev's summary, which it prints once
// every server answers /readyz, then reads the environment the build
// resolved into outputRoot and checks each server's /readyz. A first run
// in an output root compiles every crate shop-stock's server, worker and
// job build with; later runs reuse them from the environment's Cargo
// target directory there, until the output root is emptied.
func (dev *stackDev) waitUntilRunning(t *testing.T, outputRoot, stack string, servers ...string) environment {
	t.Helper()
	started := time.Now()
	deadline := started.Add(10 * time.Minute)
	for !strings.Contains(dev.output.String(), "is running:") {
		select {
		case err := <-dev.exited:
			dev.stopped = true
			t.Fatalf("stack dev exited before the stack ran: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the stack did not come up in 10 minutes")
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Logf("%s ran the stack in %s", strings.Join(dev.cmd.Args[1:], " "), time.Since(started).Round(time.Second))
	path := filepath.Join(outputRoot, "stack", stack, "Dev", "environment.json")
	env := readEnvironment(t, path)
	dev.containers = env.containers
	if stack == "shop-stack" && (env.database == "" || env.bucket == "" || len(env.containers) != 2) {
		t.Fatalf("%s names no shop-db database, no shop-media bucket, or not two containers", path)
	}
	for _, server := range servers {
		if env.servers[server] == "" {
			t.Fatalf("environment Dev has no server %s: %v", server, env.servers)
		}
	}
	for server, url := range env.servers {
		if status := get(t, url+"/readyz"); status != http.StatusOK {
			t.Fatalf("%s answered /readyz %d", server, status)
		}
	}
	return env
}

// stop stops stack dev as Ctrl-C does, which stops the servers, then
// removes the containers and their data, Postgres's and the storage
// emulator's.
func (dev *stackDev) stop(t *testing.T) {
	t.Helper()
	dev.stopped = true
	if err := dev.cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-dev.exited:
		if err != nil {
			t.Fatalf("stack dev exited with %v", err)
		}
	case <-time.After(2 * time.Minute):
		_ = dev.cmd.Process.Kill()
		t.Fatal("stack dev did not stop in 2 minutes")
	}
	for _, container := range dev.containers {
		if !strings.Contains(dev.output.String(), "removed container "+container) {
			t.Fatalf("stack dev did not remove container %s", container)
		}
		if exec.Command("docker", "container", "inspect", container).Run() == nil {
			t.Fatalf("container %s outlived --remove-data", container)
		}
	}
}

// otherOutputRoot is an output root of the test's own, outside the
// example, holding the lockfile of outputRoot's Cargo workspace and a link
// to the Cargo target directory of its Dev environment, which every Rust
// deployable of Dev builds into (D57, D65). Its path is the real one, so
// the relative paths the build writes from it to the checkout resolve.
// Its Bun workspace lists its mirrors of the TypeScript implementations,
// not the packages under typescript/ and web/, which are outputRoot's
// members alone (D65, amended).
func otherOutputRoot(t *testing.T, outputRoot string) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lock, err := os.ReadFile(filepath.Join(outputRoot, "Cargo.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Cargo.lock"), lock, 0o644); err != nil {
		t.Fatal(err)
	}
	target, err := filepath.Abs(filepath.Join(outputRoot, "program", "shop-stack", "Dev", "target"))
	if err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(root, "program", "shop-stack", "Dev")
	if err := os.MkdirAll(program, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(program, "target")); err != nil {
		t.Fatal(err)
	}
	return root
}

// projectLinks are the symbolic links under the node_modules of the
// example's own TypeScript packages, the members of outputRoot's Bun
// workspace under typescript/ and web/, by path, each with what it names.
func projectLinks(t *testing.T, outputRoot string) map[string]string {
	t.Helper()
	dirs, err := filepath.Glob(filepath.Join("..", "*", "*", "node_modules"))
	if err != nil {
		t.Fatal(err)
	}
	links := map[string]string{}
	for _, dir := range dirs {
		if group := filepath.Base(filepath.Dir(filepath.Dir(dir))); group != "typescript" && group != "web" {
			continue
		}
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.Type()&fs.ModeSymlink == 0 {
				return err
			}
			target, err := os.Readlink(p)
			links[p] = target
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(links) == 0 {
		t.Fatalf("the install of %s linked nothing into the example's TypeScript packages", outputRoot)
	}
	return links
}

// restockRequest is a row of shop-db's restock_request table, which
// shop-stock's worker ReorderStock files and its job OrderRestocks
// orders.
type restockRequest struct {
	status    string
	quantity  int64
	open      bool
	orderedAt *time.Time
}

// restockRequestsOf reads the product's restock requests from shop-db,
// oldest first; open says whether the request holds the product's one
// open place (open_product_id).
func restockRequestsOf(ctx context.Context, t *testing.T, databaseURL string, product apitypes.IdentityUUID) []restockRequest {
	t.Helper()
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	rows, err := conn.Query(ctx,
		"SELECT status, quantity, open_product_id IS NOT NULL, ordered_at FROM restock_request WHERE product_id = $1 ORDER BY created_at, id",
		product.ToUUID().String())
	if err != nil {
		t.Fatal(err)
	}
	requests, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (restockRequest, error) {
		var r restockRequest
		return r, row.Scan(&r.status, &r.quantity, &r.open, &r.orderedAt)
	})
	if err != nil {
		t.Fatal(err)
	}
	return requests
}

// waitForRestock waits until the product has one restock request, of
// status want, and returns it, or fails the test after within.
func waitForRestock(ctx context.Context, t *testing.T, databaseURL string, product apitypes.IdentityUUID, want string, within time.Duration) restockRequest {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		requests := restockRequestsOf(ctx, t, databaseURL, product)
		if len(requests) == 1 && requests[0].status == want {
			return requests[0]
		}
		if time.Now().After(deadline) {
			t.Fatalf("the restock requests of product %s are %+v after %s, want one %s", product, requests, within, want)
		}
		time.Sleep(time.Second)
	}
}

// waitForStatus waits until order id has status want, or fails the test
// after within.
func waitForStatus(ctx context.Context, t *testing.T, orders *orderssdk.ShopOrdersSDK, id orderstypes.IdentityUUID, want orderstypes.OrderStatus, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		got := orderStatus(ctx, t, orders, id)
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("order %s is %s after %s, want %s", id, got, within, want)
		}
		time.Sleep(time.Second)
	}
}

// runJob runs a job of Dev once with `superschematic stack run` and flags,
// from the example's directory as a person would, and returns what it
// printed. A run from stack dev's schedule may hold the job while it goes
// on; then stack run refuses, and runJob tries again.
func runJob(t *testing.T, binary, job string, flags ...string) string {
	t.Helper()
	for try := 1; ; try++ {
		cmd := exec.Command(binary, append(append([]string{"stack", "run", "--stack", shopStack}, flags...), "Dev", job)...)
		cmd.Dir = ".."
		out, err := cmd.CombinedOutput()
		if err == nil {
			return string(out)
		}
		if try < 5 && strings.Contains(string(out), "goes on, from stack dev's schedule") {
			time.Sleep(time.Second)
			continue
		}
		t.Fatalf("stack run: %v\n%s", err, out)
	}
}

// orderStatus reads an order's status through the generated SDK.
func orderStatus(ctx context.Context, t *testing.T, orders *orderssdk.ShopOrdersSDK, id orderstypes.IdentityUUID) orderstypes.OrderStatus {
	t.Helper()
	order, err := orders.OrderNamespace.GetOrder(ctx, id.String())
	if err != nil {
		t.Fatalf("GetOrder %s: %v", id, err)
	}
	return order.Status
}

// environment is what the test reads from the environment the build
// resolved: each server's URL, the site's origin, shop-db's connection
// string, shop-media's URL on the storage emulator, and the names of the
// containers, Postgres's and the emulator's.
type environment struct {
	servers    map[string]string
	site       string
	database   string
	bucket     string
	containers []string
}

func readEnvironment(t *testing.T, path string) environment {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var resolved struct {
		Deployables []struct {
			Name          string `json:"name"`
			Kind          string `json:"kind"`
			Address       string `json:"address"`
			PublicAddress string `json:"publicAddress"`
		} `json:"deployables"`
		Resources struct {
			Resources []struct {
				Type       string         `json:"type"`
				Properties map[string]any `json:"properties"`
			} `json:"resources"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(data, &resolved); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	env := environment{servers: map[string]string{}}
	for _, d := range resolved.Deployables {
		switch {
		case d.Kind == "server":
			env.servers[d.Name] = d.Address
		case d.Kind == "site" && d.Name == "shop-web":
			env.site = d.PublicAddress
		}
	}
	for _, r := range resolved.Resources.Resources {
		switch {
		case r.Type == "local:docker/container:Container":
			name, _ := r.Properties["name"].(string)
			env.containers = append(env.containers, name)
		case r.Type == "local:postgres/database:Database" && r.Properties["service"] == "shop-db":
			env.database, _ = r.Properties["url"].(string)
		case r.Type == "local:storage/bucket:Bucket" && r.Properties["service"] == "shop-media":
			endpoint, _ := r.Properties["endpoint"].(string)
			name, _ := r.Properties["name"].(string)
			env.bucket = endpoint + "/" + name
		}
	}
	return env
}

// signInOnTheStack creates a user in shop-db named name, with a role of
// their own that holds permissions, through the identity runtime's store
// over the database, as staff would through shop-api's administration
// routes; signs them in through shop-api's generated login with a bearer
// session, and returns its token. It also signs in with a cookie session,
// which the local target's servers, on plain HTTP, write as session
// without Secure.
func signInOnTheStack(ctx context.Context, t *testing.T, databaseURL, shopAPI, name string, permissions ...string) string {
	t.Helper()
	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	database := stdlib.OpenDB(*config)
	defer database.Close()
	store, err := identity.NewSQLStore(database, identity.Postgres, []byte(db.IdentityDescriptor))
	if err != nil {
		t.Fatal(err)
	}
	n := time.Now().UnixNano()
	first := strings.ToLower(strings.Fields(name)[0])
	login, password := fmt.Sprintf("%s.%d@example.com", first, n), first+"'s password"
	hash, err := identity.HashPassword(password, identity.Config{}.Argon2Params())
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.CreateUser(ctx, identity.NewUser{Login: login, Name: name, PasswordHash: hash, At: time.Now()})
	if err != nil {
		t.Fatalf("create the user: %v", err)
	}
	role, err := store.CreateRole(ctx, fmt.Sprintf("%s-%d", first, n), permissions)
	if err != nil {
		t.Fatalf("create the role: %v", err)
	}
	if err := store.GrantRole(ctx, user.ID, role.ID, time.Now()); err != nil {
		t.Fatalf("grant the role: %v", err)
	}

	client, err := apisdk.New(apisdk.SDKConfig{BaseURL: shopAPI})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.AccountNamespace.Login(ctx, apitypes.LoginInput{Login: apitypes.ContactEmail(login), Password: apitypes.AuthPassword(password)})
	if err != nil || result.Token == "" {
		t.Fatalf("login: %+v, %v", result, err)
	}

	request, err := http.NewRequest(http.MethodPost, shopAPI+"/api/auth/login",
		strings.NewReader(fmt.Sprintf(`{"login": %q, "password": %q, "session": "cookie"}`, login, password)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if cookies := response.Cookies(); response.StatusCode != http.StatusOK || len(cookies) != 1 || cookies[0].Name != identity.PlainCookieName || cookies[0].Secure {
		t.Fatalf("a cookie login on the local target answered %d with %v, want the session cookie without Secure", response.StatusCode, cookies)
	}
	return result.Token
}

// stockOf is how many units of the product stock lists on hand, or -1
// when it lists none.
func stockOf(stock []stocktypes.StockView, id apitypes.IdentityUUID) stocktypes.GenericInt64 {
	for _, level := range stock {
		if level.ProductId == id {
			return level.OnHand
		}
	}
	return -1
}

func containsProduct(products []apitypes.ProductView, id apitypes.IdentityUUID) bool {
	for _, p := range products {
		if p.Id == id {
			return true
		}
	}
	return false
}

// call sends a request with a bearer token, unless token is empty, and a
// JSON body, unless body is empty, and returns the status and the body of
// the answer.
func call(t *testing.T, method, url, token, body string) (int, []byte) {
	t.Helper()
	request, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	answer, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, answer
}

// preflight asks, from origin, as a browser does before a GET with the
// shopper's token, whether url answers it, and returns the answer's
// status and headers. A site's CORS answers the preflight of an origin
// it lists, 204; any other reaches the API, which answers it as it would
// with no site (D55, amended).
func preflight(t *testing.T, url, origin string) (int, http.Header) {
	t.Helper()
	request, err := http.NewRequest(http.MethodOptions, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", origin)
	request.Header.Set("Access-Control-Request-Method", http.MethodGet)
	request.Header.Set("Access-Control-Request-Headers", "authorization")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	return response.StatusCode, response.Header
}

func get(t *testing.T, url string) int {
	t.Helper()
	response, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	return response.StatusCode
}

// lockedBuffer is stack dev's output, which its process writes while the
// test reads it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// acmepayRun is acmepay, the fake provider, run by the test where
// shop-orders' ACMEPAY_URL defaults to, and a client of it.
type acmepayRun struct {
	provider *acmepay.Provider
	client   *acmepay.Client
}

// acmepayAddress is where shop-orders' ACMEPAY_URL points unless set.
const acmepayAddress = "127.0.0.1:4250"

// startAcmepay starts acmepay with the keys of shop-stack's Dev
// environment, PaymentsSecrets', from its secrets file, which stack dev
// reads (docs/stack-model.md, section 4.2): a developer's own, when they
// set them with `superschematic stack secrets set Dev`, or test ones this
// writes into the file when it holds none.
func startAcmepay(t *testing.T) *acmepayRun {
	t.Helper()
	path := filepath.Join("..", "schemas", ".superschematic", "local", "shop-stack", "Dev", "secrets.env")
	keys := map[string]string{"PaymentsSecrets.ACMEPAY_API_KEY": "", "PaymentsSecrets.ACMEPAY_WEBHOOK_SECRET": ""}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		id, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if _, wanted := keys[id]; ok && wanted {
			keys[id] = strings.Trim(strings.TrimSpace(value), `"`)
		}
	}
	missing := ""
	for id, value := range map[string]string{"PaymentsSecrets.ACMEPAY_API_KEY": "ak_dev_shop", "PaymentsSecrets.ACMEPAY_WEBHOOK_SECRET": "whsec_dev_shop"} {
		if keys[id] == "" {
			keys[id] = value
			missing += id + "=" + value + "\n"
		}
	}
	if missing != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if len(data) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
			missing = "\n" + missing
		}
		if err := os.WriteFile(path, append(data, missing...), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	listener, err := net.Listen("tcp", acmepayAddress)
	if err != nil {
		t.Fatalf("acmepay listens where shop-orders' ACMEPAY_URL points, %s: %v", acmepayAddress, err)
	}
	provider := acmepay.New(acmepay.Config{APIKey: keys["PaymentsSecrets.ACMEPAY_API_KEY"], SigningSecret: keys["PaymentsSecrets.ACMEPAY_WEBHOOK_SECRET"]})
	server := &http.Server{Handler: provider, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return &acmepayRun{provider: provider, client: &acmepay.Client{BaseURL: "http://" + acmepayAddress, APIKey: keys["PaymentsSecrets.ACMEPAY_API_KEY"]}}
}

// waitForCheckout waits until the checkout is placed and its order
// written, as the runner's Reactions rule places it after the capture,
// and returns it, or fails the test after within.
func waitForCheckout(ctx context.Context, t *testing.T, orders *orderssdk.ShopOrdersSDK, id string, within time.Duration) orderstypes.CheckoutView {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		checkout, err := orders.CheckoutsNamespace.GetCheckout(ctx, id)
		if err == nil && checkout.Status == orderstypes.CheckoutStatus_Placed && checkout.OrderId != nil {
			return checkout
		}
		if time.Now().After(deadline) {
			t.Fatalf("checkout %s is %+v, %v after %s, want placed with its order", id, checkout, err, within)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// waitForRefund waits until the checkout's payment is partly refunded, or
// fails the test after within.
func waitForRefund(ctx context.Context, t *testing.T, orders *orderssdk.ShopOrdersSDK, id string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		checkout, err := orders.CheckoutsNamespace.GetCheckout(ctx, id)
		if err == nil && checkout.Payment == "partiallyRefunded" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("checkout %s is %+v, %v after %s, want its payment partly refunded", id, checkout, err, within)
		}
		time.Sleep(500 * time.Millisecond)
	}
}
