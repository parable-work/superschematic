package servergen_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
	publicregistry "github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// The commerce trio (D70): shop-checkout-stack serves shop-commerce-db's
// three APIs, shop-checkout-api in Go, shop-checkout-rs-api in Rust and
// shop-checkout-ts-api in TypeScript, each from its default server over the
// one engine database, beside one runner of shop-commerce-db in the Go of
// shop-checkout-api, the first by name. Each API names shop-commerce-db as
// its authDb, so its members sign in and hold the permissions their roles
// grant, which the commerce behaviors check (stock.adjust, payments.record,
// payments.refund, refunds.approve).
const (
	checkoutStack    = "shop-checkout-stack"
	checkoutServer   = "shop-checkout-api"
	checkoutRsServer = "shop-checkout-rs-api"
	checkoutTSServer = "shop-checkout-ts-api"
)

// commerceTrioOrder is the order a build-all builds the trio's fixture in.
var commerceTrioOrder = []string{commerceDB, checkoutServer, checkoutRsServer, checkoutTSServer, checkoutStack}

// commerceLoadEnv, set to 1, runs TestCommerceHoldsLoad, which measures
// how many holds a second three servers take on Postgres; it asserts no
// rate, so CI leaves it off.
const commerceLoadEnv = "SUPERSCHEMATIC_COMMERCE_LOAD"

// loadCommerceTrioFixture loads the trio's fixture with the core registry
// and stacktest's fake target, as loadEngineTrioFixture does.
func loadCommerceTrioFixture(t *testing.T) fixture {
	t.Helper()
	reg, err := publicregistry.Assemble(publicregistry.DefaultNaming(), &stacktest.Extension{})
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{reg: reg, schemas: map[string]*ir.Schema{}, configs: map[string]*schemaconfig.SchemaConfig{}}
	for _, name := range commerceTrioOrder {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(engineRoot, name), loader.WithRegistry(reg))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		f.schemas[name], f.configs[name] = schema, cfg
	}
	return f
}

// TestTheCommerceRunnerOfThreeServersIsTheFirstAPIs: shop-checkout-stack
// resolves one runner of shop-commerce-db, in the Go of shop-checkout-api,
// the first of its three APIs by name, and each of the three servers opens
// the engine with the commerce behaviors (D70, D58).
func TestTheCommerceRunnerOfThreeServersIsTheFirstAPIs(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadCommerceTrioFixture(t)
	f.build(t, repoRoot, commercePaths(repoRoot), commerceTrioOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	data, err := os.ReadFile(filepath.Join(out, "stack", checkoutStack, "Local", "environment.json"))
	if err != nil {
		t.Fatal(err)
	}
	var env ir.ResolvedEnvironment
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatal(err)
	}
	var runners []string
	for _, d := range env.Deployables {
		if d.Kind == ir.DeployableRunner {
			runners = append(runners, d.Name)
		}
	}
	runner := env.Deployable(commerceRunner)
	if len(runners) != 1 || runner == nil || runner.Language != "GO" || runner.Runner == nil || runner.Runner.API != checkoutServer {
		t.Fatalf("the runners are %v, %s's %+v", runners, commerceRunner, runner)
	}
	main, err := os.ReadFile(filepath.Join(servergen.ServerDir(out, checkoutStack, checkoutTSServer), servergen.TypeScriptMainFile))
	if err != nil {
		t.Fatal(err)
	}
	if want := `{ behaviors: ['Calendar', 'Holds', 'Payments', 'Stock']`; !strings.Contains(string(main), want) {
		t.Errorf("%s opens the engine without naming the commerce behaviors (%s)", checkoutTSServer, want)
	}
}

// commerceTrio is the trio's three servers, built, and the runner beside
// them.
type commerceTrio struct {
	out string
	// goServer, rustServer and runner are binaries; tsServer is the
	// directory of the TypeScript server's package, which Bun runs.
	goServer, rustServer, tsServer, runner string
	bun                                    string
	// goDir is the Go server's module, which requires pgx: the database
	// tool runs in it.
	goDir string
}

// buildCommerceTrio builds shop-checkout-stack's three servers and its
// runner, or skips when a toolchain or a built runtime one of them needs is
// missing. The Go server and runner link the project's engine archive,
// which holds the commerce crate, in the core's place; the Rust server
// depends on the project's engine crate; the TypeScript server names the
// commerce behaviors on the core's addon.
func buildCommerceTrio(t *testing.T) commerceTrio {
	t.Helper()
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		skipEngineServers(t, "cargo not available: the Rust server and the engine archive cannot be built")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		skipEngineServers(t, "bun not available: the TypeScript server cannot run: %v", err)
	}
	paths := testpaths.Local(t)
	requireEngineArchive(t, paths)
	requireEngineTypeScript(t, paths.EngineTypeScript)
	installTypeScriptRuntime(t, bun, paths)

	repoRoot := testpaths.TempDir(t)
	f := loadCommerceTrioFixture(t)
	f.build(t, repoRoot, paths, commerceTrioOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	trio := commerceTrio{out: out, bun: bun}

	archive := buildProjectArchive(t, generator.EngineDir(out, generator.LangRust, commerceDB))
	trio.goDir = servergen.ServerDir(out, checkoutStack, checkoutServer)
	goCommand(t, trio.goDir, "mod", "tidy")
	trio.goServer = filepath.Join(t.TempDir(), checkoutServer)
	goBuildWith(t, trio.goDir, trio.goServer, "-L"+archive)
	runnerDir := servergen.ServerDir(out, checkoutStack, commerceRunner)
	goCommand(t, runnerDir, "mod", "tidy")
	trio.runner = filepath.Join(t.TempDir(), commerceRunner)
	goBuildWith(t, runnerDir, trio.runner, "-L"+archive)

	target := rustTargetDir(t)
	cargoCommand(t, cargo, out, target, "build", "--locked", "--workspace", "--quiet")
	trio.rustServer = filepath.Join(target, "debug", checkoutRsServer)

	install := exec.Command(bun, "install")
	install.Dir = out
	if output, err := install.CombinedOutput(); err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun install failed at the output root (likely offline): %v\n%s", err, output))
	}
	trio.tsServer = servergen.ServerDir(out, checkoutStack, checkoutTSServer)
	return trio
}

// startCommerceTrio starts the three servers on the database at
// databaseURL, each with its identity config and its callers field, as
// startEngineTrio does.
func startCommerceTrio(t *testing.T, trio commerceTrio, databaseURL string, public ed25519.PublicKey) []*scriptServer {
	t.Helper()
	identityConfig := `{"trustedOrigins": ["https://app.example.com"], "cookie": {"secure": false}, "password": {"argon2": {"memoryKiB": 64, "iterations": 1, "parallelism": 1}}}`
	env := func(api string) []string {
		return append([]string{"SHOP_COMMERCE_DB_DATABASE_URL=" + databaseURL, ir.IdentityConfigField(api) + "=" + identityConfig}, opsCallersVariables(t, api, public, "ops-key")...)
	}
	servers := []*scriptServer{
		{api: checkoutServer, language: "Go", tag: "run1", s: start(t, trio.goServer, env(checkoutServer)...)},
		{api: checkoutRsServer, language: "Rust", tag: "run2", s: start(t, trio.rustServer, env(checkoutRsServer)...)},
		{api: checkoutTSServer, language: "TypeScript", tag: "run3", s: bunServer(t, trio.bun, trio.tsServer, env(checkoutTSServer)...)},
	}
	for _, server := range servers {
		server.s.waitReady(t)
	}
	return servers
}

// commerceDatabase builds the trio and makes it a database of its own on
// the Postgres engineDatabaseEnv names, or skips (fails under
// requireEngineServersEnv) without one.
func commerceDatabase(t *testing.T) (commerceTrio, engineDatabaseTools, string) {
	t.Helper()
	postgres := engineDatabase(t)
	if postgres == "" {
		t.Skipf("%s is unset: the commerce servers run on Postgres alone", engineDatabaseEnv)
	}
	trio := buildCommerceTrio(t)
	tool := engineDatabaseTool(t, trio.goDir)
	return trio, tool, tool.create(t, postgres, filepath.Join(generator.SQLDir(trio.out, commerceDB), "create.sql"))
}

// pickupSlot is a start the script's slots are held at: 10:00 in London
// on a Tuesday to Friday two days ahead or more, past the CollectionPoint's
// and the Bay's lead times, inside their horizons and on their grids; and
// the day it falls on.
func pickupSlot(t *testing.T) (time.Time, time.Time) {
	t.Helper()
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	day := time.Now().In(london).AddDate(0, 0, 2)
	for day.Weekday() < time.Tuesday || day.Weekday() > time.Friday {
		day = day.AddDate(0, 0, 1)
	}
	return time.Date(day.Year(), day.Month(), day.Day(), 10, 0, 0, 0, london), day
}

// TestCommerceServersAnswerOneScriptAlike is D70's proof across languages:
// a Go, a Rust and a TypeScript server, generated from shop-commerce-db's
// three APIs in one stack, each mounting the engine with the commerce
// behaviors, answer one script alike on one Postgres. The script runs
// against each in turn, its ids, logins, provider name and event ids
// tagged with the run's tag (normaliseScript writes {{run}}), so the three
// runs share the namespace without meeting:
//
//   - members register and sign in, and an administrator grants staff
//     stock.adjust, payments.record, payments.refund, calendar.manage and
//     refunds.approve, and two approvers refunds.approve;
//   - MCP's tools/list carries the deployment's ask policies and leaves
//     out Payments' service operations;
//   - staff receive three units, which a shopper may not;
//   - a shopper's cart holds two units and a pickup slot: while its items
//     are pending what it owes is unknown (total_unknown), held it owes
//     their price, and a second cart's two units do not fit
//     (unavailable); findAvailability counts the held slot;
//   - the shopper opens a payment, again under the same key, and the
//     adapter (staff, with payments.record) lists the action and reports
//     the provider's events: processing, captured, the capture again (a
//     repeat that changes nothing), a late requiresAction (out of order,
//     ignored) and one in another currency (currency_mismatch); a shopper
//     may not report one;
//   - placing commits the units and the slot: the Inventory's ledger
//     shows the sale, the CollectionPoint the booking;
//   - a refund past what is refundable is refused, and a Refund instance
//     is issued through Approvals: its creator may not sign, one sign-off
//     is not enough, two are, and the adapter's refund event settles it;
//   - two more checkouts hold a unit each, one with a payment processing;
//     the runner fires both expiries, letting the unpaid one go, its unit
//     back on the shelf, and leaving the paid one held (its expiry vetoed,
//     payment_in_progress), and moves the Refund to issued; the late
//     capture then places the held checkout through Reactions, which the
//     runner runs.
//
// After every run the Inventory's ledger holds: onHand is the sum of its
// movements. Each server's transcript, normalised as normaliseScript says
// (a Numbering seq and label are symbols too, since the runs share the
// namespace's count), must be the others'. Then each server reads what
// each other wrote, with its own staff's session and the writer's, and
// answers it alike.
func TestCommerceServersAnswerOneScriptAlike(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the three servers' builds in -short mode")
	}
	since := time.Now()
	trio, tool, databaseURL := commerceDatabase(t)
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	servers := startCommerceTrio(t, trio, databaseURL, public)
	slot, day := pickupSlot(t)
	transcripts := map[string][]scriptExchange{}
	staff := map[*scriptServer]string{}
	for _, server := range servers {
		transcripts[server.language], staff[server] = runCommerceScript(t, server, slot, day, func(login, role string, permissions ...string) {
			tool.grant(t, databaseURL, login, role+"-"+server.tag, permissions...)
		}, func() *started {
			return startBackground(t, trio.runner, "SHOP_COMMERCE_DB_DATABASE_URL="+databaseURL)
		})
	}
	compareTranscripts(t, "commerce", servers, transcripts, since)
	checkCommerceInterop(t, servers, staff)
	for _, server := range servers {
		server.s.stop(t)
	}
}

// runCommerceScript runs the commerce script against server, on a pickup
// slot at slot on day: grant gives the member whose login it is a role of
// the run's own holding permissions, out of band, as an administrator
// would, and startRunner starts the engine's runner. It returns the
// transcript and the staff member's session.
func runCommerceScript(t *testing.T, server *scriptServer, slot, day time.Time, grant func(login, role string, permissions ...string), startRunner func() *started) ([]scriptExchange, string) {
	t.Helper()
	sc := &scriptClient{t: t, server: server}
	tag := server.tag
	inventory := commerceMount + "/schemas/Inventory/instances"
	points := commerceMount + "/schemas/CollectionPoint/instances"
	checkouts := commerceMount + "/schemas/Checkout/instances"
	refunds := commerceMount + "/schemas/Refund/instances"
	tea, front := "tea-"+tag, "front-"+tag
	a, b, e, l, r := "a-"+tag, "b-"+tag, "e-"+tag, "l-"+tag, "r-"+tag
	provider := "acmepay-" + tag
	slotMs := slot.UnixMilli()

	// Members: staff, two approvers and a shopper, who holds no
	// permission.
	creds := credentials{}
	for _, who := range []string{"staff", "approver1", "approver2", "shopper"} {
		login := who + "." + tag + "@example.com"
		sc.do(call{step: "register " + who, method: http.MethodPost, path: "/api/auth/register",
			body: fmt.Sprintf(`{"login": %q, "name": %q, "password": "a long password"}`, login, who), status: http.StatusOK})
		signedIn := sc.do(call{step: "sign in " + who, method: http.MethodPost, path: "/api/auth/login",
			body: fmt.Sprintf(`{"login": %q, "password": "a long password"}`, login), status: http.StatusOK})
		token, _ := member(signedIn, "data", "token").(string)
		if token == "" {
			t.Fatalf("signing %s in on the %s server gave no token: %v", who, server.language, signedIn)
		}
		creds[who] = map[string]string{"Authorization": "Bearer " + token}
		switch who {
		case "staff":
			grant(login, "staff", "stock.adjust", "payments.record", "payments.refund", "calendar.manage", "refunds.approve")
		case "approver1", "approver2":
			grant(login, who, "refunds.approve")
		}
	}
	as := func(who string, c call) any {
		t.Helper()
		c.as = who
		headers := map[string]string{}
		for name, value := range creds[who] {
			headers[name] = value
		}
		for name, value := range c.headers {
			headers[name] = value
		}
		c.headers = headers
		return sc.do(c)
	}
	op := func(path, operation string) string { return path + "/operations/" + operation }
	report := func(who, step, checkout string, status int, event string) any {
		t.Helper()
		return as(who, call{step: step, method: http.MethodPost, path: op(checkouts+"/"+checkout, "recordPaymentEvent"), body: event, status: status})
	}
	at := time.Now().UnixMilli()
	event := func(id, kind string, amount int, currency, more string) string {
		return fmt.Sprintf(`{"provider": %q, "eventId": "evt-%s-%s", "kind": %q, "amount": %d, "currency": %q, "at": %d%s}`, provider, tag, id, kind, amount, currency, at, more)
	}

	// What the engine serves, and what its MCP tools ask before they move
	// money or stock.
	as("shopper", call{step: "the schemas the server deployed", method: http.MethodGet, path: commerceMount + "/schemas", status: http.StatusOK})
	listed := as("shopper", call{step: "MCP tools/list", method: http.MethodPost, path: commerceMount + "/mcp",
		headers: map[string]string{"Accept": "application/json, text/event-stream"},
		body:    `{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}`, status: http.StatusOK})
	checkToolPolicies(t, server, listed)

	// Stock: staff receive three units, which a shopper may not.
	as("staff", call{step: "create an inventory", method: http.MethodPost, path: inventory,
		body: fmt.Sprintf(`{"id": %q, "data": {"sku": "TEA-%s", "priceCents": 450, "reorderAt": 1}}`, tea, tag), status: http.StatusCreated})
	as("shopper", call{step: "a receipt by a shopper", method: http.MethodPost, path: op(inventory+"/"+tea, "receiveStock"),
		body: `{"quantity": 3, "reason": "delivery"}`, status: http.StatusForbidden})
	as("staff", call{step: "receive three units", method: http.MethodPost, path: op(inventory+"/"+tea, "receiveStock"),
		body: fmt.Sprintf(`{"quantity": 3, "reason": "delivery", "reference": "DN-%s"}`, tag), status: http.StatusOK})
	as("staff", call{step: "the inventory, received", method: http.MethodGet, path: inventory + "/" + tea, status: http.StatusOK})
	as("staff", call{step: "create a collection point", method: http.MethodPost, path: points,
		body: fmt.Sprintf(`{"id": %q, "data": {"name": "Front counter"}}`, front), status: http.StatusCreated})

	// A cart holds two units and a pickup slot.
	as("shopper", call{step: "a cart with two units and a pickup slot", method: http.MethodPost, path: checkouts,
		body:   fmt.Sprintf(`{"id": %q, "data": {"lines": [{"inventoryId": %q, "quantity": 2}], "pickupAt": %q}, "behaviors": {"Links": {"counter": %q}}}`, a, tea, slot.UTC().Format(time.RFC3339), front),
		status: http.StatusCreated})
	as("shopper", call{step: "a payment while the cart's items are pending", method: http.MethodPost, path: op(checkouts+"/"+a, "openPayment"),
		body: fmt.Sprintf(`{"provider": %q, "idempotencyKey": "%s-pay"}`, provider, a), status: http.StatusConflict})
	as("shopper", call{step: "hold the cart", method: http.MethodPost, path: op(checkouts+"/"+a, "transition"), body: `{"to": "held"}`, status: http.StatusOK})
	as("shopper", call{step: "the held checkout", method: http.MethodGet, path: checkouts + "/" + a, status: http.StatusOK})
	as("staff", call{step: "the inventory, two units held", method: http.MethodGet, path: inventory + "/" + tea, status: http.StatusOK})
	as("shopper", call{step: "a second cart with two units", method: http.MethodPost, path: checkouts,
		body: fmt.Sprintf(`{"id": %q, "data": {"lines": [{"inventoryId": %q, "quantity": 2}]}}`, b, tea), status: http.StatusCreated})
	as("shopper", call{step: "two units that do not fit", method: http.MethodPost, path: op(checkouts+"/"+b, "transition"), body: `{"to": "held"}`, status: http.StatusConflict})
	as("shopper", call{step: "cancel the second cart", method: http.MethodPost, path: op(checkouts+"/"+b, "transition"), body: `{"to": "cancelled"}`, status: http.StatusOK})
	as("shopper", call{step: "the slots around the held one", method: http.MethodPost, path: commerceMount + "/schemas/CollectionPoint/operations/findAvailability",
		body:   fmt.Sprintf(`{"startMs": %d, "endMs": %d, "durationMs": 900000, "ids": [%q], "limit": 10}`, slotMs-30*60*1000, slotMs+60*60*1000, front),
		status: http.StatusOK})

	// Payment: the shopper opens one, the adapter reports the provider's
	// events, a repeat and a late one among them.
	opened := as("shopper", call{step: "open a payment", method: http.MethodPost, path: op(checkouts+"/"+a, "openPayment"),
		body: fmt.Sprintf(`{"provider": %q, "idempotencyKey": "%s-pay"}`, provider, a), status: http.StatusOK})
	attempt, _ := member(opened, "data", "attempt").(string)
	if attempt == "" {
		t.Fatalf("openPayment on the %s server gave no attempt: %v", server.language, opened)
	}
	as("shopper", call{step: "open it again under the same key", method: http.MethodPost, path: op(checkouts+"/"+a, "openPayment"),
		body: fmt.Sprintf(`{"provider": %q, "idempotencyKey": "%s-pay"}`, provider, a), status: http.StatusOK})
	actions := commerceMount + "/schemas/Checkout/operations/listPaymentActions"
	as("staff", call{step: "the adapter lists what waits for the provider", method: http.MethodPost, path: actions,
		body: fmt.Sprintf(`{"provider": %q}`, provider), status: http.StatusOK})
	with := fmt.Sprintf(`, "attempt": %q`, attempt)
	report("shopper", "a shopper reports a provider event", a, http.StatusForbidden, event("0", "captured", 900, "GBP", with))
	report("staff", "the provider is processing", a, http.StatusOK, event("1", "processing", 900, "GBP", with))
	report("staff", "the provider captured", a, http.StatusOK, event("2", "captured", 900, "GBP", with+fmt.Sprintf(`, "providerRef": "pi_%s", "brand": "visa", "last4": "4242"`, tag)))
	report("staff", "the capture delivered again", a, http.StatusOK, event("2", "captured", 900, "GBP", with+fmt.Sprintf(`, "providerRef": "pi_%s", "brand": "visa", "last4": "4242"`, tag)))
	report("staff", "a late requiresAction", a, http.StatusOK, event("3", "requiresAction", 900, "GBP", with))
	report("staff", "an event in another currency", a, http.StatusConflict, event("4", "captured", 900, "EUR", with))
	as("staff", call{step: "nothing waits once the attempt is captured", method: http.MethodPost, path: actions,
		body: fmt.Sprintf(`{"provider": %q}`, provider), status: http.StatusOK})

	// Placing commits what the checkout holds.
	as("shopper", call{step: "place the checkout", method: http.MethodPost, path: op(checkouts+"/"+a, "transition"), body: `{"to": "placed"}`, status: http.StatusOK})
	as("shopper", call{step: "the placed checkout", method: http.MethodGet, path: checkouts + "/" + a, status: http.StatusOK})
	as("staff", call{step: "the inventory, two units sold", method: http.MethodGet, path: inventory + "/" + tea, status: http.StatusOK})
	as("staff", call{step: "the collection point's bookings", method: http.MethodPost, path: op(points+"/"+front, "listBookings"),
		body: fmt.Sprintf(`{"startMs": %d, "endMs": %d}`, day.Add(-24*time.Hour).UnixMilli(), day.Add(48*time.Hour).UnixMilli()), status: http.StatusOK})

	// A refund: past what is refundable it is refused; a Refund two
	// approvers sign off is issued, and the adapter's event settles it.
	as("staff", call{step: "a refund past what is refundable", method: http.MethodPost, path: op(checkouts+"/"+a, "refundPayment"),
		body: fmt.Sprintf(`{"amount": 1000, "reason": "too much", "idempotencyKey": "%s-over"}`, a), status: http.StatusConflict})
	as("staff", call{step: "ask a refund", method: http.MethodPost, path: refunds,
		body: fmt.Sprintf(`{"id": %q, "data": {"amountCents": 300, "reason": "A dented tin"}, "behaviors": {"Links": {"order": %q}}}`, r, a), status: http.StatusCreated})
	as("staff", call{step: "its creator signs it off", method: http.MethodPost, path: op(refunds+"/"+r, "signOff"), body: `{"state": "approved"}`, status: http.StatusConflict})
	as("approver1", call{step: "the first approver signs it off", method: http.MethodPost, path: op(refunds+"/"+r, "signOff"), body: `{"state": "approved", "note": "Photo seen"}`, status: http.StatusOK})
	as("staff", call{step: "approve it after one sign-off", method: http.MethodPost, path: op(refunds+"/"+r, "transition"), body: `{"to": "approved"}`, status: http.StatusConflict})
	as("approver2", call{step: "the second approver signs it off", method: http.MethodPost, path: op(refunds+"/"+r, "signOff"), body: `{"state": "approved"}`, status: http.StatusOK})
	as("staff", call{step: "approve it", method: http.MethodPost, path: op(refunds+"/"+r, "transition"), body: `{"to": "approved"}`, status: http.StatusOK})
	waiting := as("staff", call{step: "the adapter lists the refund", method: http.MethodPost, path: actions,
		body: fmt.Sprintf(`{"provider": %q}`, provider), status: http.StatusOK})
	var refund string
	if items, _ := member(waiting, "data", "items").([]any); len(items) == 1 && member(items[0], "kind") == "refund" {
		refund, _ = member(items[0], "refund").(string)
	}
	if refund == "" {
		t.Fatalf("the %s server's adapter lists no one refund after the approval: %v", server.language, waiting)
	}
	report("staff", "the provider refunded", a, http.StatusOK, event("5", "refunded", 300, "GBP", fmt.Sprintf(`, "refund": %q`, refund)))
	as("staff", call{step: "the checkout, partly refunded", method: http.MethodGet, path: checkouts + "/" + a, status: http.StatusOK})
	as("staff", call{step: "the refund, refunded", method: http.MethodGet, path: refunds + "/" + r, status: http.StatusOK})

	// Expiry: two checkouts hold a unit each, one with a payment
	// processing. The runner lets the unpaid one go, keeps the paid one
	// held, and moves the Refund to issued.
	as("staff", call{step: "receive two more units", method: http.MethodPost, path: op(inventory+"/"+tea, "receiveStock"),
		body: `{"quantity": 2, "reason": "delivery"}`, status: http.StatusOK})
	for _, id := range []string{e, l} {
		as("shopper", call{step: "a cart with one unit", method: http.MethodPost, path: checkouts,
			body: fmt.Sprintf(`{"id": %q, "data": {"lines": [{"inventoryId": %q, "quantity": 1}]}}`, id, tea), status: http.StatusCreated})
		as("shopper", call{step: "hold it", method: http.MethodPost, path: op(checkouts+"/"+id, "transition"), body: `{"to": "held"}`, status: http.StatusOK})
	}
	late := as("shopper", call{step: "pay for the second", method: http.MethodPost, path: op(checkouts+"/"+l, "openPayment"),
		body: fmt.Sprintf(`{"provider": %q, "idempotencyKey": "%s-pay"}`, provider, l), status: http.StatusOK})
	lateAttempt, _ := member(late, "data", "attempt").(string)
	report("staff", "its payment is processing", l, http.StatusOK, event("6", "processing", 450, "GBP", fmt.Sprintf(`, "attempt": %q`, lateAttempt)))
	as("staff", call{step: "the inventory, two units held", method: http.MethodGet, path: inventory + "/" + tea, status: http.StatusOK})
	untilRunner(t, sc, startRunner, creds["staff"], "the expiries and the refund's move", func(read func(string) any) bool {
		return member(read(checkouts+"/"+e), "data", "behaviors", "Workflow", "status") == "expired" &&
			member(read(checkouts+"/"+l), "data", "behaviors", "Timers", "deadlines", "holdExpiry", "fired") == true &&
			member(read(refunds+"/"+r), "data", "behaviors", "Workflow", "status") == "issued"
	})
	as("shopper", call{step: "the unpaid checkout, expired", method: http.MethodGet, path: checkouts + "/" + e, status: http.StatusOK})
	as("shopper", call{step: "the paying checkout, still held", method: http.MethodGet, path: checkouts + "/" + l, status: http.StatusOK})
	as("staff", call{step: "the refund, issued", method: http.MethodGet, path: refunds + "/" + r, status: http.StatusOK})
	as("staff", call{step: "the inventory, the expired unit back", method: http.MethodGet, path: inventory + "/" + tea, status: http.StatusOK})
	report("staff", "the late payment is captured", l, http.StatusOK, event("7", "captured", 450, "GBP", fmt.Sprintf(`, "attempt": %q`, lateAttempt)))
	untilRunner(t, sc, startRunner, creds["staff"], "the paid checkout's placing", func(read func(string) any) bool {
		return member(read(checkouts+"/"+l), "data", "behaviors", "Workflow", "status") == "placed"
	})
	as("shopper", call{step: "the paid checkout, placed", method: http.MethodGet, path: checkouts + "/" + l, status: http.StatusOK})
	stock := as("staff", call{step: "the inventory at the end", method: http.MethodGet, path: inventory + "/" + tea, status: http.StatusOK})
	movements := as("staff", call{step: "the inventory's ledger", method: http.MethodPost, path: op(inventory+"/"+tea, "listStockMovements"), body: `{"limit": 50}`, status: http.StatusOK})
	checkLedger(t, server.language, stock, movements)
	return sc.transcript, creds["staff"]["Authorization"]
}

// untilRunner starts the engine's runner, polls with headers, as the
// script's polls are not recorded, until done is true of what the reads
// answer, and stops it.
func untilRunner(t *testing.T, sc *scriptClient, startRunner func() *started, headers map[string]string, what string, done func(read func(string) any) bool) {
	t.Helper()
	runner := startRunner()
	defer runner.stop(t)
	deadline := time.Now().Add(45 * time.Second)
	for !done(func(path string) any { return sc.peek(headers, path) }) {
		if time.Now().After(deadline) {
			t.Fatalf("the runner did not do %s on the %s server:\n%s", what, sc.server.language, runner.out.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// checkToolPolicies holds tools/list's answer to the deployment's tool
// policy (D70, amended): the operations that move money or stock ask, the
// rest take the default, and Payments' service operations and Stock's
// movement list are hidden.
func checkToolPolicies(t *testing.T, server *scriptServer, listed any) {
	t.Helper()
	// The answer comes as an event stream whose one message is the
	// JSON-RPC response.
	if frames, ok := listed.([]sseFrame); ok && len(frames) > 0 {
		listed = frames[0].Data
	}
	policies := map[string]any{}
	tools, _ := member(listed, "result", "tools").([]any)
	for _, tool := range tools {
		name, _ := member(tool, "name").(string)
		policies[name] = member(tool, "_meta", "invocationPolicy")
	}
	for tool, want := range map[string]string{
		"checkout_open_payment": "ask", "checkout_refund_payment": "ask", "checkout_capture_payment": "ask", "checkout_void_payment": "ask",
		"inventory_receive_stock": "ask", "inventory_adjust_stock": "ask", "inventory_count_stock": "ask",
		"checkout_transition": "auto", "refund_sign_off": "auto", "collection_point_find_availability": "auto",
	} {
		if policies[tool] != want {
			t.Errorf("on the %s server, tools/list gives %s the policy %v, want %s", server.language, tool, policies[tool], want)
		}
	}
	for _, tool := range []string{"checkout_record_payment_event", "checkout_list_payment_actions", "checkout_settle_refund_for", "refund_settle_refund_for", "inventory_list_stock_movements"} {
		if _, ok := policies[tool]; ok {
			t.Errorf("on the %s server, tools/list offers %s, which the engine document hides", server.language, tool)
		}
	}
}

// checkLedger holds an Inventory to Stock's ledger invariant: onHand is
// the sum of its movements' deltas, and available is onHand less held.
func checkLedger(t *testing.T, language string, stock, movements any) {
	t.Helper()
	onHand, _ := member(stock, "data", "behaviors", "Stock", "onHand").(float64)
	held, _ := member(stock, "data", "behaviors", "Stock", "held").(float64)
	available, _ := member(stock, "data", "behaviors", "Stock", "available").(float64)
	items, _ := member(movements, "data", "items").([]any)
	sum := 0.0
	for _, item := range items {
		delta, _ := member(item, "delta").(float64)
		sum += delta
	}
	if len(items) == 0 || sum != onHand || available != onHand-held {
		t.Errorf("on the %s server, the inventory's onHand is %v, held %v and available %v, and its movements sum to %v: %v", language, onHand, held, available, sum, items)
	}
}

// checkCommerceInterop holds the three servers to one database: what one
// run wrote, each other server reads as the writer does, with its own
// staff's session and the writer's.
func checkCommerceInterop(t *testing.T, servers []*scriptServer, staff map[*scriptServer]string) {
	t.Helper()
	read := func(server *scriptServer, authorization, path string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, server.s.base+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", authorization)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var body any
		data, _ := io.ReadAll(resp.Body)
		if err := json.Unmarshal(data, &body); err != nil {
			t.Fatalf("GET %s on the %s server answered %s", path, server.language, data)
		}
		if meta, ok := member(body, "meta").(map[string]any); ok {
			delete(meta, "requestId")
		}
		normalised, _ := json.Marshal(body)
		return resp.StatusCode, string(normalised)
	}
	for _, writer := range servers {
		tag := writer.tag
		for _, path := range []string{
			commerceMount + "/schemas/Inventory/instances/tea-" + tag,
			commerceMount + "/schemas/CollectionPoint/instances/front-" + tag,
			commerceMount + "/schemas/Checkout/instances/a-" + tag,
			commerceMount + "/schemas/Checkout/instances/e-" + tag,
			commerceMount + "/schemas/Checkout/instances/l-" + tag,
			commerceMount + "/schemas/Refund/instances/r-" + tag,
		} {
			status, want := read(writer, staff[writer], path)
			if status != http.StatusOK {
				t.Fatalf("the %s server reads %s it wrote: %d %s", writer.language, path, status, want)
			}
			for _, reader := range servers {
				if reader == writer {
					continue
				}
				for _, authorization := range []string{staff[reader], staff[writer]} {
					if status, got := read(reader, authorization, path); status != http.StatusOK || got != want {
						t.Errorf("the %s server reads %s, which the %s server wrote, as %d %s, not %s", reader.language, path, writer.language, status, got, want)
					}
				}
			}
		}
	}
}

// commerceClient sends the oversell and load tests' requests, many at once:
// at most 32 in flight to each server. Every move waits on the namespace's
// one writer, a checkout's for some 75 ms, and a server answers an engine
// route 504 gateway_timeout after 30 s: with 128 in flight to each server
// the last of the queue waited past it.
var commerceClient = &http.Client{Transport: &http.Transport{MaxConnsPerHost: 32, MaxIdleConnsPerHost: 32}, Timeout: 2 * time.Minute}

// commerceSend sends method to path on server as authorization, with body
// unless empty, and returns the status and the answer parsed. A busy
// engine (503) is asked again after a short wait, as a client that reads
// Retry-After would.
func commerceSend(t *testing.T, server *scriptServer, authorization, method, path, body string) (int, any) {
	for try := 0; ; try++ {
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req, err := http.NewRequest(method, server.s.base+path, reader)
		if err != nil {
			t.Error(err)
			return 0, nil
		}
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := commerceClient.Do(req)
		if err != nil {
			t.Errorf("%s %s on the %s server: %v", method, path, server.language, err)
			return 0, nil
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var answer any
		_ = json.Unmarshal(data, &answer)
		if resp.StatusCode == http.StatusServiceUnavailable && try < 20 {
			time.Sleep(time.Duration(50*(try+1)) * time.Millisecond)
			continue
		}
		return resp.StatusCode, answer
	}
}

// commerceMember registers a member whose login is login on server, signs
// them in, grants them a role holding permissions, and returns their
// Authorization header.
func commerceMember(t *testing.T, server *scriptServer, tool engineDatabaseTools, databaseURL, login, role string, permissions ...string) string {
	t.Helper()
	if status, answer := commerceSend(t, server, "", http.MethodPost, "/api/auth/register", fmt.Sprintf(`{"login": %q, "name": "Staff", "password": "a long password"}`, login)); status != http.StatusOK {
		t.Fatalf("register %s: %d %v", login, status, answer)
	}
	status, answer := commerceSend(t, server, "", http.MethodPost, "/api/auth/login", fmt.Sprintf(`{"login": %q, "password": "a long password"}`, login))
	token, _ := member(answer, "data", "token").(string)
	if status != http.StatusOK || token == "" {
		t.Fatalf("sign %s in: %d %v", login, status, answer)
	}
	if len(permissions) > 0 {
		tool.grant(t, databaseURL, login, role, permissions...)
	}
	return "Bearer " + token
}

// createAll creates n instances on the servers in turn, at most 32 at a
// time, the body of each body(i), and fails the test unless each is
// created.
func createAll(t *testing.T, servers []*scriptServer, authorization string, n int, path string, body func(i int) string) {
	t.Helper()
	var wg sync.WaitGroup
	gate := make(chan struct{}, 32)
	for i := range n {
		wg.Add(1)
		gate <- struct{}{}
		go func() {
			defer func() { <-gate; wg.Done() }()
			if status, answer := commerceSend(t, servers[i%len(servers)], authorization, http.MethodPost, path, body(i)); status != http.StatusCreated {
				t.Errorf("create %s #%d: %d %v", path, i, status, answer)
			}
		}()
	}
	wg.Wait()
	if t.Failed() {
		t.FailNow()
	}
}

// holdAll moves n claims, ids id(i), to held at once, each through the
// servers in turn, and counts the answers by status and veto code.
func holdAll(t *testing.T, servers []*scriptServer, authorization, instances string, n int, id func(i int) string) map[string]int {
	t.Helper()
	var mu sync.Mutex
	answers := map[string]int{}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			status, answer := commerceSend(t, servers[i%len(servers)], authorization, http.MethodPost, instances+"/"+id(i)+"/operations/transition", `{"to": "held"}`)
			// A veto's code is in its details: the problem's own is vetoed.
			key := fmt.Sprint(status)
			if code, ok := member(answer, "details", "code").(string); ok && status != http.StatusOK {
				key += " " + code
			} else if code, ok := member(answer, "code").(string); ok && status != http.StatusOK {
				key += " " + code
			}
			mu.Lock()
			answers[key]++
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()
	return answers
}

// listAll reads every instance of the list at path, a where query's, in
// pages of 100: a page of 500 checkouts in their carts, each reading what
// its pending items ask of their supply, took past a server's 30-second
// engine route timeout.
func listAll(t *testing.T, server *scriptServer, authorization, path string) []any {
	t.Helper()
	var all []any
	cursor := ""
	for {
		page := path + "&limit=100"
		if cursor != "" {
			page += "&cursor=" + url.QueryEscape(cursor)
		}
		status, answer := commerceSend(t, server, authorization, http.MethodGet, page, "")
		if status != http.StatusOK {
			t.Fatalf("GET %s: %d %v", page, status, answer)
		}
		items, _ := member(answer, "data", "items").([]any)
		all = append(all, items...)
		next, _ := member(answer, "data", "next").(string)
		if next == "" {
			return all
		}
		cursor = next
	}
}

// commerceListCarts is how many one-item carts TestCommerceHoldsLoad
// lists through each server.
const commerceListCarts = 1000

// commerceOversellMoves is how many claims race for the oversell test's
// last units and its one slot.
const commerceOversellMoves = 1000

// TestCommerceServersOversellNothing: three servers, Go, Rust and
// TypeScript, over one Postgres, and 1,000 checkouts, each asking for one
// of an Inventory's three units, moved cart→held at once, a third through
// each server (at most 32 requests in flight to each). The namespace's
// one writer (D58) lets exactly three hold: the rest are vetoed
// unavailable and stay in their carts. Stock's ledger holds afterwards:
// held is the sum of what the held checkouts hold, onHand the sum of the
// movements, and nothing is available. Then the same for time: 1,000
// bookings of one fifteen-minute slot at a Bay of capacity 1, of which
// exactly one holds it, which the Bay's bookings show.
func TestCommerceServersOversellNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the three servers' builds in -short mode")
	}
	trio, tool, databaseURL := commerceDatabase(t)
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	servers := startCommerceTrio(t, trio, databaseURL, public)
	staff := commerceMember(t, servers[0], tool, databaseURL, "staff@example.com", "staff", "stock.adjust", "calendar.manage")

	t.Run("stock", func(t *testing.T) {
		inventory := commerceMount + "/schemas/Inventory/instances"
		checkouts := commerceMount + "/schemas/Checkout/instances"
		for _, step := range []struct{ path, body string }{
			{inventory, `{"id": "last", "data": {"sku": "LAST", "priceCents": 999}}`},
			{inventory + "/last/operations/receiveStock", `{"quantity": 3, "reason": "the last three"}`},
		} {
			if status, answer := commerceSend(t, servers[0], staff, http.MethodPost, step.path, step.body); status != http.StatusCreated && status != http.StatusOK {
				t.Fatalf("POST %s: %d %v", step.path, status, answer)
			}
		}
		id := func(i int) string { return fmt.Sprintf("race-%04d", i) }
		began := time.Now()
		createAll(t, servers, staff, commerceOversellMoves, checkouts, func(i int) string {
			return fmt.Sprintf(`{"id": %q, "data": {"lines": [{"inventoryId": "last", "quantity": 1}]}}`, id(i))
		})
		t.Logf("%d checkouts created in %s", commerceOversellMoves, time.Since(began).Round(time.Millisecond))
		began = time.Now()
		answers := holdAll(t, servers, staff, checkouts, commerceOversellMoves, id)
		t.Logf("%d moves cart→held over three servers in %s: %v", commerceOversellMoves, time.Since(began).Round(time.Millisecond), answers)
		if want := map[string]int{"200": 3, "409 unavailable": commerceOversellMoves - 3}; !mapsEqual(answers, want) {
			t.Errorf("the moves answered %v, want %v", answers, want)
		}

		_, stock := commerceSend(t, servers[1], staff, http.MethodGet, inventory+"/last", "")
		_, movements := commerceSend(t, servers[2], staff, http.MethodPost, inventory+"/last/operations/listStockMovements", `{"limit": 500}`)
		checkLedger(t, "oversell", stock, movements)
		if got := member(stock, "data", "behaviors", "Stock"); member(got, "onHand") != 3.0 || member(got, "held") != 3.0 || member(got, "available") != 0.0 {
			t.Errorf("the inventory is %v, want 3 on hand, 3 held and none available", got)
		}
		held := listAll(t, servers[2], staff, checkouts+"?where="+url.QueryEscape(`{"Workflow.status":"held"}`))
		heldUnits := 0.0
		for _, checkout := range held {
			items, _ := member(checkout, "behaviors", "Holds", "items").([]any)
			for _, item := range items {
				if member(item, "state") == "held" {
					quantity, _ := member(item, "quantity").(float64)
					heldUnits += quantity
				}
			}
		}
		if len(held) != 3 || heldUnits != 3 {
			t.Errorf("%d checkouts are held, holding %v units, want 3 holding 3", len(held), heldUnits)
		}
		listed := time.Now()
		if carts := listAll(t, servers[0], staff, checkouts+"?where="+url.QueryEscape(`{"Workflow.status":"cart"}`)); len(carts) != commerceOversellMoves-3 {
			t.Errorf("%d checkouts are left in their carts, want %d", len(carts), commerceOversellMoves-3)
		}
		t.Logf("the checkouts left in their carts listed in %s", time.Since(listed).Round(time.Millisecond))
	})

	t.Run("time", func(t *testing.T) {
		bays := commerceMount + "/schemas/Bay/instances"
		bookings := commerceMount + "/schemas/Booking/instances"
		if status, answer := commerceSend(t, servers[0], staff, http.MethodPost, bays, `{"id": "bay-1", "data": {"name": "Bay 1"}}`); status != http.StatusCreated {
			t.Fatalf("create the bay: %d %v", status, answer)
		}
		slot, day := pickupSlot(t)
		id := func(i int) string { return fmt.Sprintf("slot-%04d", i) }
		createAll(t, servers, staff, commerceOversellMoves, bookings, func(i int) string {
			return fmt.Sprintf(`{"id": %q, "data": {"startsAt": %q}, "behaviors": {"Links": {"bay": "bay-1"}}}`, id(i), slot.UTC().Format(time.RFC3339))
		})
		began := time.Now()
		answers := holdAll(t, servers, staff, bookings, commerceOversellMoves, id)
		t.Logf("%d bookings requested→held of one slot over three servers in %s: %v", commerceOversellMoves, time.Since(began).Round(time.Millisecond), answers)
		if want := map[string]int{"200": 1, "409 unavailable": commerceOversellMoves - 1}; !mapsEqual(answers, want) {
			t.Errorf("the moves answered %v, want %v", answers, want)
		}
		_, booked := commerceSend(t, servers[1], staff, http.MethodPost, bays+"/bay-1/operations/listBookings",
			fmt.Sprintf(`{"startMs": %d, "endMs": %d}`, day.Add(-24*time.Hour).UnixMilli(), day.Add(48*time.Hour).UnixMilli()))
		if items, _ := member(booked, "data", "items").([]any); len(items) != 1 {
			t.Errorf("the bay lists %d bookings, want 1: %v", len(items), booked)
		}
		if held := listAll(t, servers[2], staff, bookings+"?where="+url.QueryEscape(`{"Workflow.status":"held"}`)); len(held) != 1 {
			t.Errorf("%d bookings are held, want 1", len(held))
		}
	})
}

// mapsEqual reports whether two counts are the same.
func mapsEqual(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// TestCommerceHoldsLoad measures, and asserts nothing of, how many holds a
// second three servers, Go, Rust and TypeScript, over one Postgres take:
// for claims of 1, 10 and 50 items, it moves checkouts cart→held, each
// item a unit of its own Inventory, from 24 clients at once across the
// three servers, and logs the claims and the items held a second; then it
// lists 1,000 carts, each with one pending item, through each server. It runs
// only when SUPERSCHEMATIC_COMMERCE_LOAD=1, beside the Postgres
// engineDatabaseEnv names:
//
//	SUPERSCHEMATIC_COMMERCE_LOAD=1 SUPERSCHEMATIC_IDENTITY_TEST_DATABASE_URL=postgres://… \
//	  go test -run '^TestCommerceHoldsLoad$' -v ./internal/generator/servergen/
//
// D70's Status records what it measured.
func TestCommerceHoldsLoad(t *testing.T) {
	if os.Getenv(commerceLoadEnv) != "1" {
		t.Skipf("%s is not 1: the load measurement runs on demand", commerceLoadEnv)
	}
	if testing.Short() {
		t.Skip("skipping the three servers' builds in -short mode")
	}
	trio, tool, databaseURL := commerceDatabase(t)
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	servers := startCommerceTrio(t, trio, databaseURL, public)
	staff := commerceMember(t, servers[0], tool, databaseURL, "staff@example.com", "staff", "stock.adjust")
	inventory := commerceMount + "/schemas/Inventory/instances"
	checkouts := commerceMount + "/schemas/Checkout/instances"
	const products = 50
	createAll(t, servers, staff, products, inventory, func(i int) string {
		return fmt.Sprintf(`{"id": "p-%02d", "data": {"sku": "P-%02d", "priceCents": 100}}`, i, i)
	})
	for i := range products {
		if status, answer := commerceSend(t, servers[0], staff, http.MethodPost, fmt.Sprintf("%s/p-%02d/operations/receiveStock", inventory, i), `{"quantity": 1000000, "reason": "load"}`); status != http.StatusOK {
			t.Fatalf("receive p-%02d: %d %v", i, status, answer)
		}
	}
	var report []string
	for _, size := range []int{1, 10, 50} {
		claims := map[int]int{1: 600, 10: 200, 50: 60}[size]
		id := func(i int) string { return fmt.Sprintf("load-%d-%04d", size, i) }
		created := time.Now()
		createAll(t, servers, staff, claims, checkouts, func(i int) string {
			lines := make([]string, size)
			for j := range size {
				lines[j] = fmt.Sprintf(`{"inventoryId": "p-%02d", "quantity": 1}`, (i+j)%products)
			}
			return fmt.Sprintf(`{"id": %q, "data": {"lines": [%s]}}`, id(i), strings.Join(lines, ", "))
		})
		creating := time.Since(created)
		var wg sync.WaitGroup
		var mu sync.Mutex
		failed := map[string]int{}
		next := make(chan int)
		began := time.Now()
		for worker := range 24 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range next {
					status, answer := commerceSend(t, servers[(i+worker)%len(servers)], staff, http.MethodPost, checkouts+"/"+id(i)+"/operations/transition", `{"to": "held"}`)
					if status != http.StatusOK {
						mu.Lock()
						failed[fmt.Sprint(status, " ", member(answer, "code"))]++
						mu.Unlock()
					}
				}
			}()
		}
		for i := range claims {
			next <- i
		}
		close(next)
		wg.Wait()
		elapsed := time.Since(began)
		if len(failed) > 0 {
			t.Errorf("%d-item claims: some moves failed: %v", size, failed)
		}
		line := fmt.Sprintf("%2d items a claim: %d claims held in %s, %.1f claims/s, %.0f holds/s (created in %s, %.1f a second)", size, claims, elapsed.Round(time.Millisecond),
			float64(claims)/elapsed.Seconds(), float64(claims*size)/elapsed.Seconds(), creating.Round(time.Millisecond), float64(claims)/creating.Seconds())
		t.Log(line)
		report = append(report, line)
	}
	// The read path: carts whose one pending item each reads its supply,
	// listed by status in pages of 100 through each server in turn.
	createAll(t, servers, staff, commerceListCarts, checkouts, func(i int) string {
		return fmt.Sprintf(`{"id": "list-%04d", "data": {"lines": [{"inventoryId": "p-%02d", "quantity": 1}]}}`, i, i%products)
	})
	carts := checkouts + "?where=" + url.QueryEscape(`{"Workflow.status":"cart"}`)
	for _, server := range servers {
		listed := time.Now()
		items := listAll(t, server, staff, carts)
		line := fmt.Sprintf("%d carts listed through %s in %s", len(items), server.language, time.Since(listed).Round(time.Millisecond))
		t.Log(line)
		report = append(report, line)
	}
	t.Logf("holds a second on Postgres, three servers, 24 clients:\n%s", strings.Join(report, "\n"))
}
