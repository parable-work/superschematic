package servergen_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	// The pickup slot is London's wall time wherever the test runs.
	_ "time/tzdata"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/projectengine"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
	publicregistry "github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// The commerce fixture (D70): shop-commerce-db, an engine database whose
// Inventory, CollectionPoint, Checkout and Refund compose Stock, Calendar,
// Holds and Payments beside the core's behaviors, under the allowAll
// policy, with outputs.engine.tools.invocation marking the operations that
// move money or stock ask and tools.hidden hiding Stock's movement list;
// and an API over it in each language whose one
// operation only a calling service may call, each served on the local
// target by a stack of its own beside shop-commerce-db's runner.
const (
	commerceDB       = "shop-commerce-db"
	commerceRunner   = "shop-commerce-db-engine-runner"
	commerceStack    = "shop-commerce-stack"
	commerceServer   = "shop-commerce-api"
	commerceRsStack  = "shop-commerce-rs-stack"
	commerceRsServer = "shop-commerce-rs-api"
	commerceTSStack  = "shop-commerce-ts-stack"
	commerceTSServer = "shop-commerce-ts-api"
	// commerceMount is where each server mounts shop-commerce-db's engine,
	// in its namespace.
	commerceMount = "/engine/shop-commerce-db/namespaces/shop-commerce-db"
)

// commerceBehaviors are the commerce behaviors, as the TypeScript servers
// name them, sorted.
var commerceBehaviors = []string{"Calendar", "Holds", "Payments", "Stock"}

// commerceOrder is the order a build-all builds the fixture's stack whose
// API is api in.
func commerceOrder(api, stack string) []string {
	return []string{commerceDB, api, stack}
}

// loadCommerceFixture loads the commerce fixture with the core registry
// and stacktest's fake target, as loadEngineFixture does.
func loadCommerceFixture(t *testing.T) fixture {
	t.Helper()
	reg, err := publicregistry.Assemble(publicregistry.DefaultNaming(), &stacktest.Extension{})
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{reg: reg, schemas: map[string]*ir.Schema{}, configs: map[string]*schemaconfig.SchemaConfig{}}
	for _, name := range []string{commerceDB, commerceServer, commerceStack, commerceRsServer, commerceRsStack, commerceTSServer, commerceTSStack} {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(engineRoot, name), loader.WithRegistry(reg))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		f.schemas[name], f.configs[name] = schema, cfg
	}
	return f
}

// commercePaths are the [paths] a commerce build of every language takes:
// rustFakePaths with the engine's npm package and the TypeScript runtimes.
func commercePaths(repoRoot string) naming.LocalPaths {
	paths := rustFakePaths(repoRoot)
	ts := tsEngineFakePaths(repoRoot)
	paths.ScalarTypeScript, paths.HTTPRuntimeTypeScript, paths.EngineTypeScript = ts.ScalarTypeScript, ts.HTTPRuntimeTypeScript, ts.EngineTypeScript
	return paths
}

// TestCommerceEngineFixtureBuilds: shop-commerce-db's build writes the
// project's engine crate with the commerce crate and no work queue's, and
// an engine document whose behaviors name @superschematic/engine-commerce
// and whose tools carry the operations the deployment marks ask (D70). The
// Go server and runner link the crate's ffi member, whose archive the
// image's engine stage builds; the Rust server depends on the crate; the
// TypeScript server and runner name the four commerce behaviors on the
// core's addon and take no project addon.
func TestCommerceEngineFixtureBuilds(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadCommerceFixture(t)
	f.build(t, repoRoot, commercePaths(repoRoot), commerceDB, commerceServer, commerceStack, commerceRsServer, commerceRsStack, commerceTSServer, commerceTSStack)
	out := filepath.Join(repoRoot, "schemas", "dist")

	crate := generator.EngineDir(out, generator.LangRust, commerceDB)
	manifest, err := os.ReadFile(filepath.Join(crate, projectengine.ManifestFile))
	if err != nil {
		t.Fatalf("%s's build wrote no engine crate: %v", commerceDB, err)
	}
	if !strings.Contains(string(manifest), "\nsuperschematic-engine-commerce = { path = ") || strings.Contains(string(manifest), "superschematic-engine-workqueue") {
		t.Errorf("the crate does not depend on the commerce crate alone:\n%s", manifest)
	}
	lib, err := os.ReadFile(filepath.Join(crate, filepath.FromSlash(projectengine.LibFile)))
	if err != nil || !strings.Contains(string(lib), ".extend(superschematic_engine_commerce::commerce())") {
		t.Errorf("the crate's behaviors() registers no commerce behaviors (%v):\n%s", err, lib)
	}

	data, err := os.ReadFile(generator.EngineDocumentPath(out, commerceDB))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Tools struct {
			Invocation map[string]string `json:"invocation"`
			Hidden     []string          `json:"hidden"`
		} `json:"tools"`
		Behaviors []struct {
			Name    string `json:"name"`
			Package string `json:"package"`
		} `json:"behaviors"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"Payments.openPayment": "ask", "Payments.capturePayment": "ask", "Payments.voidPayment": "ask", "Payments.refundPayment": "ask", "Payments.refundFor": "ask",
		"Stock.receiveStock": "ask", "Stock.adjustStock": "ask", "Stock.countStock": "ask",
	}
	if !maps.Equal(doc.Tools.Invocation, want) {
		t.Errorf("the document's tools.invocation = %v, want %v", doc.Tools.Invocation, want)
	}
	// Payments' service operations are hidden wherever it is composed,
	// beside what the deployment hides.
	if hidden := []string{"Payments.listPaymentActions", "Payments.recordPaymentEvent", "Payments.settleRefundFor", "Stock.listStockMovements"}; !slices.Equal(doc.Tools.Hidden, hidden) {
		t.Errorf("the document's tools.hidden = %v, want %v", doc.Tools.Hidden, hidden)
	}
	var commerce []string
	for _, b := range doc.Behaviors {
		if b.Package == "@superschematic/engine-commerce" {
			commerce = append(commerce, b.Name)
		}
	}
	if !slices.Equal(commerce, commerceBehaviors) {
		t.Errorf("the document's commerce behaviors = %v, want %v", commerce, commerceBehaviors)
	}

	for _, d := range []string{commerceServer, commerceRunner} {
		dir := servergen.ServerDir(out, commerceStack, d)
		link, err := projectengine.ReadLink(dir)
		if err != nil || link == nil || link.Member != projectengine.FFIDir || link.Database != commerceDB {
			t.Errorf("%s's link file is %+v (%v), want %s's ffi member", d, link, err, commerceDB)
		}
		dockerfile, err := os.ReadFile(filepath.Join(dir, servergen.DockerFile))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			"COPY schemas/dist/engine/rust/shop-commerce-db schemas/dist/engine/rust/shop-commerce-db\n",
			"cargo build --release --manifest-path schemas/dist/engine/rust/shop-commerce-db/ffi/Cargo.toml",
		} {
			if !strings.Contains(string(dockerfile), want) {
				t.Errorf("%s's Dockerfile lacks %q:\n%s", d, want, dockerfile)
			}
		}
	}

	cargo, err := os.ReadFile(filepath.Join(servergen.ServerDir(out, commerceRsStack, commerceRsServer), "Cargo.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cargo), naming.Default().RustEngineCrate(commerceDB)+" = { path = ") {
		t.Errorf("%s does not depend on the project's engine crate:\n%s", commerceRsServer, cargo)
	}

	for _, d := range []string{commerceTSServer, commerceRunner} {
		dir := servergen.ServerDir(out, commerceTSStack, d)
		main, err := os.ReadFile(filepath.Join(dir, servergen.TypeScriptMainFile))
		if err != nil {
			t.Fatal(err)
		}
		if want := `shopCommerceDbEngineDocument, 'allowAll', { behaviors: ['Calendar', 'Holds', 'Payments', 'Stock']`; !strings.Contains(string(main), want) {
			t.Errorf("%s opens the engine without naming the commerce behaviors (%s):\n%s", d, want, main)
		}
		if link, err := projectengine.ReadLink(dir); err != nil || link != nil {
			t.Errorf("%s loads a project addon (%+v, %v), where the core's carries the commerce behaviors", d, link, err)
		}
	}
}

// TestCommerceEngineInvocationIsChecked: the build of shop-commerce-db
// refuses outputs.engine.tools.invocation naming an operation its engine
// types' behaviors do not declare, or a value the binary's policy does not
// hold (D70).
func TestCommerceEngineInvocationIsChecked(t *testing.T) {
	f := loadCommerceFixture(t)
	cfg := *f.configs[commerceDB]
	cfg.Outputs = maps.Clone(cfg.Outputs)
	cfg.Outputs["engine"] = map[string]any{"policy": "allowAll", "tools": map[string]any{"invocation": map[string]any{"Payments.capturePay": "ask", "Stock.adjustStock": "always"}}}
	repoRoot := t.TempDir()
	_, err := generator.Run(f.schemas[commerceDB], &cfg, f.options(repoRoot, commercePaths(repoRoot)))
	for _, want := range []string{"Payments.capturePay names no operation of Payments", `the policy of Stock.adjustStock, "always", is not one of invocationPolicy's values (auto, ask)`} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("the build of %s = %v, want it refused saying %q", commerceDB, err, want)
		}
	}

	// Every operation that moves money or stock must be named: here none
	// of Payments' and only Stock's adjust are.
	cfg.Outputs["engine"] = map[string]any{"policy": "allowAll", "tools": map[string]any{"invocation": map[string]any{"Stock.adjustStock": "auto"}}}
	_, err = generator.Run(f.schemas[commerceDB], &cfg, f.options(repoRoot, commercePaths(repoRoot)))
	if want := "outputs.engine.tools.invocation names no invocationPolicy for Payments.openPayment, Payments.capturePayment, Payments.voidPayment, Payments.refundPayment, Payments.refundFor, Stock.receiveStock, Stock.countStock"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("the build of %s with no money operations named = %v, want it refused saying %q", commerceDB, err, want)
	}
	// And a hidden name no composed behavior declares is refused.
	invocation := maps.Clone(f.configs[commerceDB].Outputs["engine"].(map[string]any)["tools"].(map[string]any)["invocation"].(map[string]any))
	cfg.Outputs["engine"] = map[string]any{"policy": "allowAll", "tools": map[string]any{"invocation": invocation, "hidden": []any{"Stock.sellStock", "Queue.claim"}}}
	_, err = generator.Run(f.schemas[commerceDB], &cfg, f.options(repoRoot, commercePaths(repoRoot)))
	for _, want := range []string{"outputs.engine.tools.hidden: Stock.sellStock names no operation of Stock", "Queue.claim names behavior Queue, which no engine type composes"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("the build of %s = %v, want it refused saying %q", commerceDB, err, want)
		}
	}
}

// commerceScript runs what each language's server must answer alike on
// shop-commerce-db, with a service credential standing in for an end user
// under the allowAll policy (D70). The engine lists the four schemas. Its
// MCP tools carry the invocation policy the deployment names, ask for each
// operation that moves money or stock and the default for the rest, and
// leave out Payments' service operations and what the deployment hides.
// An Inventory is created and read back with Stock's entry. A Checkout
// whose pickup slot at a CollectionPoint is still pending owes what is not
// known (total_unknown); it holds the slot through Calendar as it enters
// held, owes nothing for a slot with no price (nothing_due), and is placed
// with no payment, committing the slot, which the CollectionPoint's
// bookings show. A call whose parameters the declaration refuses is 400
// before the behavior runs, and an operation no behavior declares is 404.
func commerceScript(t *testing.T, s *started, credential string) {
	t.Helper()
	s.serviceSend(t, credential, http.MethodGet, commerceMount+"/schemas", "", http.StatusOK, `"Checkout"`, `"CollectionPoint"`, `"Inventory"`, `"Refund"`)

	listed := s.serviceMCP(t, credential, commerceMount+"/mcp", `{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}`)
	var tools struct {
		Result struct {
			Tools []struct {
				Name string         `json:"name"`
				Meta map[string]any `json:"_meta"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(listed), &tools); err != nil {
		t.Fatalf("tools/list answered %s: %v", listed, err)
	}
	policies := map[string]string{}
	for _, tool := range tools.Result.Tools {
		policies[tool.Name], _ = tool.Meta["invocationPolicy"].(string)
	}
	for tool, want := range map[string]string{
		"checkout_open_payment":          "ask",
		"checkout_capture_payment":       "ask",
		"checkout_void_payment":          "ask",
		"checkout_refund_payment":        "ask",
		"checkout_refund_for":            "ask",
		"checkout_retake_holds":          "auto",
		"refund_refund_payment":          "ask",
		"inventory_adjust_stock":         "ask",
		"inventory_receive_stock":        "ask",
		"inventory_count_stock":          "ask",
		"collection_point_add_closure":   "auto",
		"collection_point_list_bookings": "auto",
	} {
		if got, ok := policies[tool]; !ok || got != want {
			t.Errorf("tools/list: %s's invocationPolicy = %q (listed %t), want %q; tools: %v", tool, got, ok, want, policies)
		}
	}
	for _, tool := range []string{"checkout_record_payment_event", "checkout_settle_refund_for", "checkout_list_payment_actions", "refund_record_payment_event", "refund_settle_refund_for", "inventory_list_stock_movements"} {
		if _, ok := policies[tool]; ok {
			t.Errorf("tools/list offers %s, which the engine document hides", tool)
		}
	}

	const inventory = commerceMount + "/schemas/Inventory/instances"
	s.serviceSend(t, credential, http.MethodPost, inventory, `{"id": "tea", "data": {"sku": "TEA-1", "priceCents": 450}}`, http.StatusCreated, `"id":"tea"`)
	s.serviceSend(t, credential, http.MethodGet, inventory+"/tea", "", http.StatusOK, `"sku":"TEA-1"`, `"Stock":{`)

	// A pickup slot: a day ahead or more, past the CollectionPoint's lead
	// time and inside its horizon, at 10:00 on a weekday it opens 09:00 to
	// 17:30, which its fifteen-minute grid from 09:00 holds.
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	day := time.Now().In(london).AddDate(0, 0, 2)
	for day.Weekday() < time.Tuesday || day.Weekday() > time.Friday {
		day = day.AddDate(0, 0, 1)
	}
	slot := time.Date(day.Year(), day.Month(), day.Day(), 10, 0, 0, 0, london).UTC().Format(time.RFC3339)
	const points = commerceMount + "/schemas/CollectionPoint/instances"
	const checkouts = commerceMount + "/schemas/Checkout/instances"
	s.serviceSend(t, credential, http.MethodPost, points, `{"id": "front", "data": {"name": "Front counter"}}`, http.StatusCreated, `"id":"front"`)
	s.serviceSend(t, credential, http.MethodPost, checkouts,
		fmt.Sprintf(`{"id": "c1", "data": {"lines": [], "pickupAt": %q}, "behaviors": {"Links": {"counter": "front"}}}`, slot),
		http.StatusCreated, `"id":"c1"`, `"state":"pending"`)
	// The slot is pending, its price known only once it is held.
	s.serviceSend(t, credential, http.MethodPost, checkouts+"/c1/operations/openPayment", `{"provider": "acmepay", "idempotencyKey": "c1-pay"}`, http.StatusConflict, `"total_unknown"`)
	s.serviceSend(t, credential, http.MethodPost, checkouts+"/c1/operations/transition", `{"to": "held"}`, http.StatusOK)
	s.serviceSend(t, credential, http.MethodGet, checkouts+"/c1", "", http.StatusOK, `"status":"held"`, `"state":"held"`, `"due":0`)
	// A slot with no price owes nothing: no payment, and placed needs none.
	s.serviceSend(t, credential, http.MethodPost, checkouts+"/c1/operations/openPayment", `{"provider": "acmepay", "idempotencyKey": "c1-pay"}`, http.StatusConflict, `"nothing_due"`)
	s.serviceSend(t, credential, http.MethodPost, checkouts+"/c1/operations/transition", `{"to": "placed"}`, http.StatusOK)
	s.serviceSend(t, credential, http.MethodGet, checkouts+"/c1", "", http.StatusOK, `"status":"placed"`, `"state":"committed"`)
	s.serviceSend(t, credential, http.MethodPost, points+"/front/operations/listBookings",
		fmt.Sprintf(`{"startMs": %d, "endMs": %d}`, day.Add(-48*time.Hour).UnixMilli(), day.Add(48*time.Hour).UnixMilli()),
		http.StatusOK, `"id":"c1"`)

	for _, call := range []struct{ path, body string }{
		{inventory + "/tea/operations/receiveStock", `{"quantity": "ten"}`},
		{inventory + "/tea/operations/adjustStock", `{"delta": 1, "reason": "count", "bogus": true}`},
		{commerceMount + "/schemas/Checkout/instances/c1/operations/capturePayment", `{"amount": "all"}`},
		{commerceMount + "/schemas/CollectionPoint/operations/findAvailability", `{}`},
	} {
		s.serviceSend(t, credential, http.MethodPost, call.path, call.body, http.StatusBadRequest)
	}
	s.serviceSend(t, credential, http.MethodPost, inventory+"/tea/operations/sellStock", `{}`, http.StatusNotFound)
}

// TestCommerceEngineEntrypointServes: shop-commerce-db's engine crate,
// written by its build, builds its archive with the checkout's script,
// which holds the commerce crate, and shop-commerce-api's Go server and
// shop-commerce-db's runner link it in the core's place (D70, D58). The
// server starts with no database up, names the engine in /readyz and
// answers its routes 503. With the Postgres engineDatabaseEnv names it
// deploys shop-commerce-db's document, which only an engine that runs the
// four commerce behaviors accepts, and answers commerceScript; the runner
// starts beside it. Without one it skips (fails under
// SUPERSCHEMATIC_REQUIRE_ENGINE_SERVERS=1) after the offline half.
func TestCommerceEngineEntrypointServes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the archive's build in -short mode")
	}
	if _, err := exec.LookPath("cargo"); err != nil {
		skipEngineServers(t, "cargo not available; skipping the engine archive's build")
	}
	paths := testpaths.Local(t)
	repoRoot := t.TempDir()
	f := loadCommerceFixture(t)
	f.build(t, repoRoot, paths, commerceOrder(commerceServer, commerceStack)...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	archive := buildProjectArchive(t, generator.EngineDir(out, generator.LangRust, commerceDB))
	ldflags := "-L" + archive

	dir := servergen.ServerDir(out, commerceStack, commerceServer)
	goCommand(t, dir, "mod", "tidy")
	binary := filepath.Join(t.TempDir(), commerceServer)
	goBuildWith(t, dir, binary, ldflags)
	runnerDir := servergen.ServerDir(out, commerceStack, commerceRunner)
	goCommand(t, runnerDir, "mod", "tidy")
	runnerBinary := filepath.Join(t.TempDir(), commerceRunner)
	goBuildWith(t, runnerDir, runnerBinary, ldflags)

	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	env := opsCallersVariables(t, commerceServer, public, "ops-key")
	offline := start(t, binary, append([]string{"SHOP_COMMERCE_DB_DATABASE_URL=postgres://shop@127.0.0.1:9/shop_commerce_db?connect_timeout=1&sslmode=disable"}, env...)...)
	offline.expect(t, http.MethodGet, "/readyz", http.StatusServiceUnavailable, `"shop-commerce-db engine"`)
	offline.expect(t, http.MethodGet, commerceMount+"/schemas", http.StatusServiceUnavailable, `"code":"unavailable"`)
	offline.stop(t)

	postgres := engineDatabase(t)
	if postgres == "" {
		skipEngineServers(t, "%s is unset: the entrypoint served offline only, and deploys and answers commerceScript on Postgres alone", engineDatabaseEnv)
	}
	tool := engineDatabaseTool(t, dir)
	databaseURL := tool.create(t, postgres, filepath.Join(generator.SQLDir(out, commerceDB), "create.sql"))
	online := start(t, binary, append([]string{"SHOP_COMMERCE_DB_DATABASE_URL=" + databaseURL}, env...)...)
	online.waitReady(t)
	if !strings.Contains(online.out.String(), `"archive":"schemas-shop-commerce-db-engine-ffi"`) {
		t.Errorf("the server did not open the project's archive:\n%s", online.out.String())
	}
	commerceScript(t, online, signEdgeToken(t, key, "ops-key", "ops", commerceServer))
	runner := startBackground(t, runnerBinary, "SHOP_COMMERCE_DB_DATABASE_URL="+databaseURL)
	runner.stop(t)
	online.stop(t)
}

// TestCommerceEngineEntrypointServesRust: shop-commerce-rs-api's Rust
// server depends on shop-commerce-db's engine crate, whose behaviors() it
// opens the engine with, the commerce ones among them (D70, D58). It
// passes clippy, starts with no database up, names the engine in /readyz
// and answers its routes 503; with the Postgres engineDatabaseEnv names it
// deploys shop-commerce-db's document and answers commerceScript, and
// without one it skips after the offline half.
func TestCommerceEngineEntrypointServesRust(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo build in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		skipEngineServers(t, "cargo not available; skipping the Rust build")
	}
	paths := testpaths.Local(t)
	repoRoot := testpaths.TempDir(t)
	f := loadCommerceFixture(t)
	f.build(t, repoRoot, paths, commerceOrder(commerceRsServer, commerceRsStack)...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	target := rustTargetDir(t)
	cargoCommand(t, cargo, out, target, "clippy", "--locked", "--workspace", "--quiet", "--", "-D", "warnings")
	cargoCommand(t, cargo, out, target, "build", "--locked", "--workspace", "--quiet")
	server := filepath.Join(target, "debug", commerceRsServer)

	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	env := opsCallersVariables(t, commerceRsServer, public, "ops-key")
	offline := start(t, server, append([]string{"SHOP_COMMERCE_DB_DATABASE_URL=postgres://shop@127.0.0.1:9/shop_commerce_db?connect_timeout=1&sslmode=disable"}, env...)...)
	offline.expect(t, http.MethodGet, "/readyz", http.StatusServiceUnavailable, `"shop-commerce-db engine"`)
	offline.expect(t, http.MethodGet, commerceMount+"/schemas", http.StatusServiceUnavailable, `"code":"unavailable"`)
	offline.stop(t)

	postgres := engineDatabase(t)
	if postgres == "" {
		skipEngineServers(t, "%s is unset: the entrypoint served offline only, and deploys and answers commerceScript on Postgres alone", engineDatabaseEnv)
	}
	tool := engineDatabaseTool(t, pgxModule(t))
	databaseURL := tool.create(t, postgres, filepath.Join(generator.SQLDir(out, commerceDB), "create.sql"))
	online := start(t, server, append([]string{"SHOP_COMMERCE_DB_DATABASE_URL=" + databaseURL}, env...)...)
	online.waitReady(t)
	commerceScript(t, online, signEdgeToken(t, key, "ops-key", "ops", commerceRsServer))
	runner := startBackground(t, filepath.Join(target, "debug", commerceRunner), "SHOP_COMMERCE_DB_DATABASE_URL="+databaseURL)
	runner.stop(t)
	online.stop(t)
}

// TestCommerceEngineEntrypointServesTypeScript: shop-commerce-ts-api's
// TypeScript server and shop-commerce-db's TypeScript runner install in
// the output root's workspace with the engine's package and type-check;
// they open the engine on the core's Node-API addon, which carries the
// commerce behaviors, naming the four (D70, D58). The server starts with
// no database up, names the engine in /readyz and answers its routes 503;
// with the Postgres engineDatabaseEnv names it deploys shop-commerce-db's
// document and answers commerceScript, and without one it skips after the
// offline half.
func TestCommerceEngineEntrypointServesTypeScript(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun not available: %v", err))
	}
	paths := testpaths.Local(t)
	requireEngineTypeScript(t, paths.EngineTypeScript)
	installTypeScriptRuntime(t, bun, paths)
	repoRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := loadCommerceFixture(t)
	f.build(t, repoRoot, paths, commerceOrder(commerceTSServer, commerceTSStack)...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	install := exec.Command(bun, "install")
	install.Dir = out
	if output, err := install.CombinedOutput(); err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun install failed at the output root (likely offline): %v\n%s", err, output))
	}
	dir := servergen.ServerDir(out, commerceTSStack, commerceTSServer)
	runnerDir := servergen.ServerDir(out, commerceTSStack, commerceRunner)
	tsc := filepath.Join(paths.HTTPRuntimeTypeScript, "node_modules", ".bin", "tsc")
	for _, pkg := range []string{dir, runnerDir, naming.Default().TypeScriptImplementationDir(repoRoot, commerceTSServer)} {
		cmd := exec.Command(tsc, "--noEmit", "-p", "tsconfig.json")
		cmd.Dir = pkg
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("tsc in %s: %v\n%s", pkg, err, output)
		}
	}

	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	env := opsCallersVariables(t, commerceTSServer, public, "ops-key")
	offline := bunServer(t, bun, dir, append([]string{"SHOP_COMMERCE_DB_DATABASE_URL=postgres://shop@127.0.0.1:9/shop_commerce_db?connect_timeout=1&sslmode=disable"}, env...)...)
	offline.expect(t, http.MethodGet, "/readyz", http.StatusServiceUnavailable, `"shop-commerce-db engine"`)
	offline.expect(t, http.MethodGet, commerceMount+"/schemas", http.StatusServiceUnavailable, `"code":"unavailable"`)
	offline.stop(t)

	postgres := engineDatabase(t)
	if postgres == "" {
		skipEngineServers(t, "%s is unset: the entrypoint served offline only, and deploys and answers commerceScript on Postgres alone", engineDatabaseEnv)
	}
	tool := engineDatabaseToolTS(t, bun, dir)
	databaseURL := tool.create(t, postgres, filepath.Join(generator.SQLDir(out, commerceDB), "create.sql"))
	online := bunServer(t, bun, dir, append([]string{"SHOP_COMMERCE_DB_DATABASE_URL=" + databaseURL}, env...)...)
	online.waitReady(t)
	commerceScript(t, online, signEdgeToken(t, key, "ops-key", "ops", commerceTSServer))
	runner := bunBackground(t, bun, runnerDir, "SHOP_COMMERCE_DB_DATABASE_URL="+databaseURL)
	runner.stop(t)
	online.stop(t)
}

// serviceMCP posts a JSON-RPC request to the MCP endpoint at path with a
// service credential and no end user's, checks it is answered 200, and
// returns the answer.
func (s *started) serviceMCP(t *testing.T, credential, path, body string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, s.base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Service-Authorization", "Bearer "+credential)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("POST %s with a service credential = %d: %s", path, resp.StatusCode, answer)
	}
	// An answer as an event stream carries the JSON-RPC response as its
	// one message's data.
	for _, line := range strings.Split(string(answer), "\n") {
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			return data
		}
	}
	return string(answer)
}
