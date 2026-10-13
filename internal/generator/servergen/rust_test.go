package servergen_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/rustapigen"
	"github.com/parable-work/superschematic/internal/generator/rustrestgen"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/stackdeploy"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
	publicregistry "github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

const (
	// rustServicesRoot holds the Rust fixture (D57): rs-stack, whose Rust
	// server rs-stock, on rs-db, its authDb, calls rs-pricing's Rust
	// server, which admits only calling services; rs-orders' Go server
	// calls rs-stock, whose reservations only it may make; and the site
	// rs-web calls rs-stock. Its one environment is local. Beside it,
	// rs-merged-stack's declared Rust server Warehouse serves rs-catalog
	// and rs-pricing.
	rustServicesRoot = "testdata/rust/services"

	// rustStack is the fixture's stack, and rustMergedStack the second,
	// whose server rustMergedServer serves two APIs.
	rustStack        = "rs-stack"
	rustMergedStack  = "rs-merged-stack"
	rustMergedServer = "Warehouse"

	// rustGoldenRoot holds the Rust entrypoints as the output root lays
	// them out, and the workspace's root.
	rustGoldenRoot = "testdata/golden/rust"
)

// rustOrder is the order a build-all builds the Rust fixture in: rs-stock
// calls rs-pricing, so it builds after rs-pricing's SDK, and rs-orders
// calls rs-stock. rs-web, the site, is loaded, not built.
var rustOrder = []string{"rs-db", "rs-pricing", "rs-catalog", "rs-stock", "rs-orders", rustStack, rustMergedStack}

// loadRustFixture loads every Rust fixture service, as loadFixture loads
// the Go one.
func loadRustFixture(t *testing.T) fixture {
	t.Helper()
	reg, err := publicregistry.Assemble(publicregistry.DefaultNaming(), &stacktest.Extension{})
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{reg: reg, schemas: map[string]*ir.Schema{}, configs: map[string]*schemaconfig.SchemaConfig{}}
	for _, name := range append(slices.Clone(rustOrder), "rs-web") {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(rustServicesRoot, name), loader.WithRegistry(reg))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		f.schemas[name], f.configs[name] = schema, cfg
	}
	return f
}

// rustFakePaths places the checkouts the Rust crates depend on in the
// repository at repoRoot, as this repository's naming file does, beside
// the Go server's.
func rustFakePaths(repoRoot string) naming.LocalPaths {
	paths := fakePaths(repoRoot)
	paths.HTTPRuntimeRust = filepath.Join(repoRoot, "runtime", "http", "rust")
	paths.SchemaRuntimeRust = filepath.Join(repoRoot, "runtime", "schema", "rust")
	paths.ScalarRust = filepath.Join(repoRoot, "third_party", "superscalar", "crates", "core")
	return paths
}

// rustGenerated lists the files of each Rust entrypoint the stack's build
// writes, and the workspace's root, by their path under rustGoldenRoot.
func rustGenerated(repoRoot string) map[string]string {
	out := filepath.Join(repoRoot, "schemas", "dist")
	files := map[string]string{servergen.RustWorkspaceFile: filepath.Join(out, servergen.RustWorkspaceFile)}
	for _, server := range [][2]string{{rustStack, "rs-pricing"}, {rustStack, "rs-stock"}, {rustMergedStack, rustMergedServer}} {
		for _, file := range []string{servergen.RustManifestFile, servergen.RustMainFile, servergen.DockerFile, servergen.DockerIgnoreFile} {
			files[filepath.Join("server", server[0], server[1], filepath.FromSlash(file))] = filepath.Join(servergen.ServerDir(out, server[0], server[1]), filepath.FromSlash(file))
		}
	}
	return files
}

// TestRustEntrypointGolden: the stack's build writes a crate per Rust
// server, a member of the output root's Cargo workspace, whose root it
// writes too: rs-stock's opens a pool on rs-db, builds the identity store
// over it and its identity service from its identity config field, a
// client of rs-pricing that sends the edge's credential, its service
// authenticator from its callers field, and answers the site's CORS;
// rs-pricing's verifies its callers. rs-merged-stack's Warehouse, built
// into the same output root, joins the workspace, and hands each request
// to the router of the API whose operations take it. Each gets a
// Dockerfile that builds it frozen to the workspace's lockfile. rs-orders'
// Go server is a Go module, outside the workspace. Regenerate with:
//
//	go test ./internal/generator/servergen -run TestRustEntrypointGolden -update
func TestRustEntrypointGolden(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadRustFixture(t)
	result := f.build(t, repoRoot, rustFakePaths(repoRoot), rustOrder[:len(rustOrder)-1]...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	if got, want := result.Outputs["server"], servergen.StackDir(out, rustStack); got != want {
		t.Errorf("server output = %q, want %q", got, want)
	}
	f.build(t, repoRoot, rustFakePaths(repoRoot), rustMergedStack)
	compareGoldens(t, rustGoldenRoot, rustGenerated(repoRoot))
	if _, err := os.Stat(filepath.Join(servergen.ServerDir(out, rustStack, "rs-orders"), servergen.MainFile)); err != nil {
		t.Errorf("rs-orders has no Go entrypoint: %v", err)
	}
	for _, service := range []string{"rs-pricing", "rs-stock"} {
		if _, err := os.Stat(filepath.Join(naming.Default().RustImplementationDir(repoRoot, service), "Cargo.toml")); err != nil {
			t.Errorf("the build did not scaffold %s: %v", service, err)
		}
	}
	root, err := os.ReadFile(filepath.Join(out, servergen.RustWorkspaceFile))
	if err != nil || strings.Contains(string(root), "rs-orders") {
		t.Errorf("the workspace names rs-orders' Go server: %v\n%s", err, root)
	}
}

// rustPricingImplementation is rs-pricing's implementation: a quote of 250
// cents for any product, which only a calling service gets.
const rustPricingImplementation = `use std::sync::Arc;

use async_trait::async_trait;
use schemas_rs_pricing_api::types;
use schemas_rs_pricing_api::{Deps, Handlers, QuotesGetQuoteArgs, QuotesImplementation};
use superschematic_http_runtime::{ApiError, RequestContext};

pub async fn create(deps: Deps) -> Result<Handlers, Box<dyn std::error::Error + Send + Sync>> {
    Ok(Handlers {
        quotes: Arc::new(Quotes { _deps: Arc::new(deps) }),
    })
}

pub struct Quotes {
    _deps: Arc<Deps>,
}

#[async_trait]
impl QuotesImplementation for Quotes {
    async fn get_quote(&self, ctx: RequestContext, args: QuotesGetQuoteArgs) -> Result<types::Quote, ApiError> {
        let caller = ctx.service_caller.map(|caller| caller.subject).unwrap_or_default();
        Ok(types::Quote {
            sku: format!("{}:{caller}", args.sku),
            cents: 250.0,
        })
    }
}
`

// rustStockImplementation is rs-stock's implementation: a level in the
// warehouse its setting names, priced by rs-pricing, which it calls with
// its edge's credential; a slow level, which answers after a second; and a
// reservation for the service that asks for it.
const rustStockImplementation = `use std::sync::Arc;
use std::time::Duration;

use async_trait::async_trait;
use schemas_rs_stock_api::types;
use schemas_rs_stock_api::{
    Deps, Handlers, LevelsGetLevelArgs, LevelsImplementation, LevelsSetLevelArgs,
    ReservationsImplementation, ReservationsReserveStockArgs,
};
use superschematic_http_runtime::{ApiError, RequestContext};

pub async fn create(deps: Deps) -> Result<Handlers, Box<dyn std::error::Error + Send + Sync>> {
    let deps = Arc::new(deps);
    Ok(Handlers {
        levels: Arc::new(Levels { deps: Arc::clone(&deps) }),
        reservations: Arc::new(Reservations { deps }),
    })
}

fn level(deps: &Deps, sku: String, cents: f64) -> types::LevelView {
    types::LevelView {
        sku,
        quantity: 7.0,
        cents,
        warehouse: deps.config.warehouse.clone(),
    }
}

pub struct Levels {
    deps: Arc<Deps>,
}

#[async_trait]
impl LevelsImplementation for Levels {
    async fn list_levels(&self, _ctx: RequestContext) -> Result<Vec<types::LevelView>, ApiError> {
        Ok(vec![level(&self.deps, "tea".to_string(), 0.0)])
    }

    async fn get_level(&self, _ctx: RequestContext, args: LevelsGetLevelArgs) -> Result<types::LevelView, ApiError> {
        if args.sku == "slow" {
            std::thread::sleep(Duration::from_secs(1));
            return Ok(level(&self.deps, args.sku, 0.0));
        }
        let quote = self
            .deps
            .rs_pricing
            .quotes
            .get_quote(args.sku.clone(), None)
            .await
            .map_err(|err| ApiError::internal(format!("rs-pricing: {err}")))?;
        Ok(level(&self.deps, quote.sku, quote.cents))
    }

    async fn set_level(&self, _ctx: RequestContext, args: LevelsSetLevelArgs) -> Result<types::LevelView, ApiError> {
        Ok(level(&self.deps, args.sku, 0.0))
    }
}

pub struct Reservations {
    deps: Arc<Deps>,
}

#[async_trait]
impl ReservationsImplementation for Reservations {
    async fn reserve_stock(&self, ctx: RequestContext, args: ReservationsReserveStockArgs) -> Result<types::LevelView, ApiError> {
        let caller = ctx.service_caller.map(|caller| caller.deployable).unwrap_or_default();
        Ok(level(&self.deps, format!("{}:{caller}", args.sku), 0.0))
    }
}
`

// rustOrdersImplementation is rs-orders' Go implementation: an order
// reserves its product's stock on rs-stock, a Rust server, through
// rs-stock's Go SDK, which sends the credential rs-orders' edge names.
const rustOrdersImplementation = `package rsorders

import (
	"context"

	api "example.com/schemas/api/rs-orders"
	types "example.com/schemas/types/go/rs-orders"
)

func New(deps api.Deps) (api.Implementations, error) {
	return api.Implementations{Orders: &Orders{deps: deps}}, nil
}

var _ api.Constructor = New

type Orders struct {
	deps api.Deps
}

func (impl *Orders) PlaceOrder(ctx context.Context, sku string) (*types.Order, error) {
	level, err := impl.deps.RsStock.ReservationsNamespace.ReserveStock(ctx, sku)
	if err != nil {
		return nil, err
	}
	return &types.Order{Sku: level.Sku, Status: "reserved"}, nil
}
`

// rustIdentityConfig is rs-stock's identity config in the serve test: a
// trusted origin, a cookie without Secure, and a cheap password hash.
const rustIdentityConfig = `{"trustedOrigins": ["https://app.example.com"], "cookie": {"secure": false}, "password": {"argon2": {"memoryKiB": 64, "iterations": 1, "parallelism": 1}}}`

// cargoCommand runs cargo with args in dir, its target directory
// CARGO_TARGET_DIR or target, and returns its output.
func cargoCommand(t *testing.T, cargo, dir, target string, args ...string) string {
	t.Helper()
	cmd := exec.Command(cargo, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+target)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cargo %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

// rustTargetDir is the Cargo target directory of a test that builds the
// fixture: CARGO_TARGET_DIR when set, so runs share their builds, or one
// of the test's own.
func rustTargetDir(t *testing.T) string {
	if dir := os.Getenv("CARGO_TARGET_DIR"); dir != "" {
		return dir
	}
	return filepath.Join(t.TempDir(), "target")
}

// buildRustFixture builds the fixture into a repository of its own with
// this checkout's runtimes, writes the implementations, and builds the
// workspace's servers with cargo, frozen to the lockfile the stack's build
// wrote. It returns the repository root and the directory of the binaries.
func buildRustFixture(t *testing.T, cargo string) (string, string) {
	t.Helper()
	paths := testpaths.Local(t)
	repoRoot := testpaths.TempDir(t)
	f := loadRustFixture(t)
	f.build(t, repoRoot, paths, rustOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	if _, err := os.Stat(filepath.Join(out, servergen.RustLockfileName)); err != nil {
		t.Fatalf("the stack's build wrote no lockfile: %v", err)
	}
	names := naming.Default()
	for service, code := range map[string]string{"rs-pricing": rustPricingImplementation, "rs-stock": rustStockImplementation} {
		if err := os.WriteFile(filepath.Join(names.RustImplementationDir(repoRoot, service), "src", "lib.rs"), []byte(code), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	target := rustTargetDir(t)
	cargoCommand(t, cargo, out, target, "clippy", "--locked", "--workspace", "--quiet", "--", "-D", "warnings")
	cargoCommand(t, cargo, out, target, "build", "--locked", "--workspace", "--quiet")
	return repoRoot, filepath.Join(target, "debug")
}

// TestRustEntrypointCompilesAndServes: each Rust server's crate builds in
// the output root's workspace, frozen to the lockfile the stack's build
// wrote, with its implementation, and passes clippy; its main.rs is laid
// out as rustfmt lays it out. Warehouse hands each request to the API
// whose operations take it. rs-orders' Go server reserves stock on
// rs-stock through rs-stock's Go SDK with its edge's credential.
// rs-pricing verifies a calling service's
// credential against its callers field. rs-stock starts with no database
// up, answers /healthz and reports its database in /readyz, prices a level
// through rs-pricing, which it calls with a token signed with its edge's
// key, reserves stock for rs-orders, whose token it verifies, and no other
// caller, refuses its staff route without an end user, answers the site's
// CORS and the identity config's trusted origins, and on SIGTERM lets a
// request in flight finish, then stops. Without its configuration it stops
// at once, naming every variable missing. With Postgres
// (SUPERSCHEMATIC_IDENTITY_TEST_DATABASE_URL), an end user registers, signs
// in and is refused the staff route for want of stock.write.
func TestRustEntrypointCompilesAndServes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo build in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust build")
	}
	repoRoot, bin := buildRustFixture(t, cargo)
	out := filepath.Join(repoRoot, "schemas", "dist")
	if err := os.WriteFile(filepath.Join(naming.Default().GoImplementationDir(repoRoot, "rs-orders"), "implementation.go"), []byte(rustOrdersImplementation), 0o644); err != nil {
		t.Fatal(err)
	}
	ordersBinary := filepath.Join(t.TempDir(), "rs-orders")
	goCommand(t, servergen.ServerDir(out, rustStack, "rs-orders"), "build", "-mod=mod", "-o", ordersBinary, ".")
	if rustfmt, err := exec.LookPath("rustfmt"); err == nil {
		for _, server := range [][2]string{{rustStack, "rs-pricing"}, {rustStack, "rs-stock"}, {rustMergedStack, rustMergedServer}} {
			main := filepath.Join(servergen.ServerDir(out, server[0], server[1]), filepath.FromSlash(servergen.RustMainFile))
			if diff, err := exec.Command(rustfmt, "--edition", "2021", "--check", main).CombinedOutput(); err != nil {
				t.Errorf("rustfmt lays out %s's main.rs otherwise: %v\n%s", server[1], err, diff)
			}
		}
	}

	// Warehouse serves rs-catalog and rs-pricing on one port, and hands
	// each request to the router of the API whose operations take it, and
	// the routes both register, the OpenAPI document's, to the first.
	warehouse := start(t, filepath.Join(bin, rustMergedServer), ir.CallersField("rs-pricing")+"_ISSUERS=0")
	warehouse.expect(t, http.MethodGet, "/readyz", http.StatusOK, `"ready"`)
	warehouse.expect(t, http.MethodGet, "/api/catalog/tea", http.StatusNotImplemented, "catalog.get_entry is not implemented")
	warehouse.expect(t, http.MethodGet, "/api/quotes/tea", http.StatusUnauthorized, "service_unauthorized")
	warehouse.expect(t, http.MethodGet, "/api/openapi.json", http.StatusOK, "/api/catalog/{sku}")
	warehouse.expect(t, http.MethodGet, "/api/nowhere", http.StatusNotFound)
	warehouse.stop(t)

	// The edge from rs-stock to rs-pricing, as the local target's connector
	// derives it: rs-stock signs with the private key as its own issuer,
	// and rs-pricing's callers field holds the public key.
	private, public := edgeKey(t)
	pricing := start(t, filepath.Join(bin, "rs-pricing"), variables(t, ir.CallersField("rs-pricing"), ir.ServiceAuth{Issuers: []*ir.ServiceAuthIssuer{{
		Issuer:             "rs-stock",
		Audience:           "rs-pricing",
		Algorithms:         []string{ir.AlgorithmEdDSA},
		Keys:               []ir.ServiceAuthKey{{JWK: public}},
		MaxLifetimeSeconds: 300,
		Callers:            []ir.ServiceAuthCaller{{Subject: "rs-stock", Deployable: "rs-stock", Serves: []string{"rs-stock"}}},
	}}})...)
	pricing.expect(t, http.MethodGet, "/readyz", http.StatusOK, `"ready"`)
	pricing.expect(t, http.MethodGet, "/api/quotes/tea", http.StatusUnauthorized, "service_unauthorized")

	// rs-orders' edge to rs-stock: rs-stock's callers field holds its
	// public key, and rs-orders alone may reserve stock.
	ordersPublic, ordersPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ordersJWK, err := json.Marshal(map[string]string{"kty": "OKP", "crv": "Ed25519", "kid": "orders-1", "x": base64.RawURLEncoding.EncodeToString(ordersPublic)})
	if err != nil {
		t.Fatal(err)
	}
	ordersPrivateJWK, err := json.Marshal(map[string]string{"kty": "OKP", "crv": "Ed25519", "kid": "orders-1", "x": base64.RawURLEncoding.EncodeToString(ordersPublic), "d": base64.RawURLEncoding.EncodeToString(ordersPrivate.Seed())})
	if err != nil {
		t.Fatal(err)
	}
	callers := variables(t, ir.CallersField("rs-stock"), ir.ServiceAuth{Issuers: []*ir.ServiceAuthIssuer{{
		Issuer:             "rs-orders",
		Audience:           "rs-stock",
		Algorithms:         []string{ir.AlgorithmEdDSA},
		Keys:               []ir.ServiceAuthKey{{JWK: string(ordersJWK)}},
		MaxLifetimeSeconds: 300,
		Callers:            []ir.ServiceAuthCaller{{Subject: "rs-orders", Deployable: "rs-orders", Serves: []string{"rs-orders"}}},
	}}})
	edge := variables(t, "RS_PRICING_SERVICE", ir.ServiceEndpoint{URL: pricing.base, Credential: &ir.ServiceCredential{
		Source: ir.CredentialSignedToken, Audience: "rs-pricing", Issuer: "rs-stock", Key: private,
	}})
	const siteOrigin = "https://rs-web.acme.dev"
	env := append(append([]string{"RS_STOCK_CORS_ORIGINS=" + siteOrigin, "RS_STOCK_IDENTITY=" + rustIdentityConfig, "WAREHOUSE=north"}, edge...), callers...)

	// Port 9 discards; nothing answers a database there.
	unreachable := "RS_DB_DATABASE_URL=postgres://stock@127.0.0.1:9/rs_db?connect_timeout=1&sslmode=disable"
	stock := start(t, filepath.Join(bin, "rs-stock"), append([]string{unreachable}, env...)...)
	stock.expect(t, http.MethodGet, "/healthz", http.StatusOK, `"ok"`)
	stock.expect(t, http.MethodGet, "/readyz", http.StatusServiceUnavailable, `"unavailable":["rs-db"]`)
	stock.expect(t, http.MethodGet, "/api/levels", http.StatusOK, `"warehouse":"north"`)
	stock.expect(t, http.MethodGet, "/api/levels/tea", http.StatusOK, `"sku":"tea:rs-stock"`, `"cents":250`)
	stock.expect(t, http.MethodPut, "/api/levels/tea", http.StatusUnauthorized)
	reserve := "/api/reservations/tea"
	stock.sendWith(t, http.MethodPost, reserve, "{}", map[string]string{"Service-Authorization": "Bearer " + signEdgeToken(t, ordersPrivate, "orders-1", "rs-orders", "rs-stock")}, http.StatusOK, `"sku":"tea:rs-orders"`)
	stock.sendWith(t, http.MethodPost, reserve, "{}", map[string]string{"Service-Authorization": "Bearer " + signEdgeToken(t, ordersPrivate, "orders-1", "rs-orders", "rs-pricing")}, http.StatusUnauthorized, "service_unauthorized")
	stock.sendWith(t, http.MethodPost, reserve, "{}", nil, http.StatusUnauthorized, "service_unauthorized")
	stock.expect(t, http.MethodGet, "/api/nowhere", http.StatusNotFound)

	// rs-orders' Go server places an order, which reserves the stock on
	// rs-stock through its Go SDK with the token its edge signs.
	orders := start(t, ordersBinary, variables(t, "RS_STOCK_SERVICE", ir.ServiceEndpoint{URL: stock.base, Credential: &ir.ServiceCredential{
		Source: ir.CredentialSignedToken, Audience: "rs-stock", Issuer: "rs-orders", Key: string(ordersPrivateJWK),
	}})...)
	orders.expect(t, http.MethodPost, "/api/orders/tea", http.StatusOK, `"sku":"tea:rs-orders"`, `"status":"reserved"`)
	orders.stop(t)
	checkCORS(t, stock.base, http.MethodOptions, "/api/levels", siteOrigin, http.MethodGet, http.StatusNoContent, siteOrigin)
	checkCORS(t, stock.base, http.MethodOptions, "/api/levels", "https://evil.example", http.MethodGet, 0, "")
	checkCORS(t, stock.base, http.MethodGet, "/api/levels", siteOrigin, "", http.StatusOK, siteOrigin)
	stock.expectPreflight(t, "/api/auth/login", http.MethodPost)
	if !strings.Contains(stock.out.String(), `"msg":"listening"`) || !strings.Contains(stock.out.String(), `"server":"rs-stock"`) {
		t.Errorf("rs-stock did not log JSON lines:\n%s", stock.out.String())
	}

	// SIGTERM with a request in flight: the request finishes, then the
	// server stops.
	slow := make(chan error, 1)
	go func() {
		resp, err := http.Get(stock.base + "/api/levels/slow")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				err = fmt.Errorf("status %d", resp.StatusCode)
			}
		}
		slow <- err
	}()
	time.Sleep(300 * time.Millisecond)
	stock.stop(t)
	if err := <-slow; err != nil {
		t.Errorf("the request in flight at SIGTERM: %v\n%s", err, stock.out.String())
	}
	if !strings.Contains(stock.out.String(), `"msg":"shutting down"`) {
		t.Errorf("rs-stock did not log its shutdown:\n%s", stock.out.String())
	}

	// Without its configuration, rs-stock stops at once, naming each
	// variable missing.
	cmd := exec.Command(filepath.Join(bin, "rs-stock"))
	cmd.Env = append(os.Environ(), "PORT="+freePort(t))
	refused, err := cmd.CombinedOutput()
	if err == nil {
		t.Errorf("rs-stock started without its configuration:\n%s", refused)
	}
	for _, want := range []string{`"msg":"server stopped"`, "configuration of rs-stock: ", "RS_DB_DATABASE_URL", "RS_PRICING_SERVICE_URL", "RS_STOCK_CALLERS_ISSUERS"} {
		if !strings.Contains(string(refused), want) {
			t.Errorf("rs-stock stopped without saying %q:\n%s", want, refused)
		}
	}
	// An identity config the runtime refuses stops it too, naming the
	// field.
	cmd = exec.Command(filepath.Join(bin, "rs-stock"))
	cmd.Env = append(append(os.Environ(), "PORT="+freePort(t), unreachable), append(edge, append(callers, `RS_STOCK_IDENTITY={"cookie": {"sameSite": "Loose"}}`)...)...)
	if refused, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(refused), "RS_STOCK_IDENTITY") {
		t.Errorf("rs-stock on a refused identity config = %v:\n%s", err, refused)
	}
	pricing.stop(t)

	postgres := os.Getenv(identityDatabaseEnv)
	if postgres == "" {
		t.Logf("%s is unset: the entrypoint serves no database", identityDatabaseEnv)
		return
	}
	databaseURL := libpqSearchPath(t, identitySchema(t, pgxModule(t), postgres, filepath.Join(generator.SQLDir(out, "rs-db"), "create.sql")))
	pricing = start(t, filepath.Join(bin, "rs-pricing"), ir.CallersField("rs-pricing")+"_ISSUERS=0")
	online := start(t, filepath.Join(bin, "rs-stock"), append([]string{"RS_DB_DATABASE_URL=" + databaseURL}, append(env[:len(env):len(env)], variables(t, "RS_PRICING_SERVICE", ir.ServiceEndpoint{URL: pricing.base})...)...)...)
	online.expect(t, http.MethodGet, "/readyz", http.StatusOK, `"ready"`)
	online.send(t, http.MethodPost, "/api/auth/register", `{"login": "ada@example.com", "name": "Ada", "password": "ada's password"}`, http.StatusOK, `"token"`)
	token := online.login(t, "ada@example.com", "ada's password")
	online.authorized(t, token, http.MethodGet, "/api/auth/me", http.StatusOK, `"login":"ada@example.com"`)
	online.authorized(t, token, http.MethodPut, "/api/levels/tea", http.StatusForbidden)
	online.authorized(t, token, http.MethodPost, "/api/auth/logout", http.StatusOK, `"data":true`)
	online.authorized(t, token, http.MethodGet, "/api/auth/me", http.StatusUnauthorized)
	online.stop(t)
	pricing.stop(t)
}

// pgxModule is a Go module that requires pgx, in which identitySchema runs
// its tool: the Rust fixture's servers are no Go module to run it in.
func pgxModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	tool := filepath.Join(dir, "internal", "identityschema")
	if err := os.MkdirAll(tool, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tool, "main.go"), []byte(identitySchemaTool), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module identityschema\n\ngo "+servergen.GoVersion+"\n\nrequire github.com/jackc/pgx/v5 v5.11.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	goCommand(t, dir, "mod", "tidy")
	return dir
}

// libpqSearchPath is databaseURL with its search_path parameter, which pgx
// sends as a runtime parameter, as libpq's options parameter, which
// tokio-postgres reads: tokio-postgres refuses a parameter it does not
// know.
func libpqSearchPath(t *testing.T, databaseURL string) string {
	t.Helper()
	u, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	path := q.Get("search_path")
	q.Del("search_path")
	u.RawQuery = q.Encode() + "&options=" + strings.ReplaceAll(url.QueryEscape("-c search_path="+path), "+", "%20")
	return u.String()
}

// TestTheRustDockerfileBuildsAnImageThatServes: rs-stock's Dockerfile
// builds from the context a deploy writes (stackdeploy.WriteContext) out of
// a repository root holding the generated crates, the scaffolds, the
// workspace's lockfile the stack's build wrote, and copies of the HTTP
// runtime, the schema runtime and superscalar's crate, frozen to the
// lockfile, and the image serves as a non-root user: /healthz, an API's
// route, /readyz reporting its database down, and a clean stop on docker
// stop. It needs Docker and the network, and runs only outside -short.
func TestTheRustDockerfileBuildsAnImageThatServes(t *testing.T) {
	needDocker(t)
	if _, err := exec.LookPath("cargo"); err != nil {
		t.Skip("cargo not available: the stack's build writes the lockfile with it")
	}
	local := testpaths.Local(t)
	repoRoot := testpaths.TempDir(t)
	paths := rustFakePaths(repoRoot)
	copyTree(t, local.HTTPRuntimeRust, paths.HTTPRuntimeRust)
	copyTree(t, local.SchemaRuntimeRust, paths.SchemaRuntimeRust)
	copyTree(t, local.ScalarRust, paths.ScalarRust)
	workspace, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(local.ScalarRust)), "Cargo.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(filepath.Dir(paths.ScalarRust)), "Cargo.toml"), workspace, 0o644); err != nil {
		t.Fatal(err)
	}
	f := loadRustFixture(t)
	f.build(t, repoRoot, paths, rustOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	if _, err := os.Stat(filepath.Join(out, servergen.RustLockfileName)); err != nil {
		t.Fatalf("the stack's build wrote no lockfile: %v", err)
	}

	dockerfile := filepath.Join(servergen.ServerDir(out, rustStack, "rs-stock"), servergen.DockerFile)
	archive := filepath.Join(t.TempDir(), "context.tar.gz")
	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	built, err := stackdeploy.WriteContext(file, repoRoot, dockerfile)
	if cerr := file.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatalf("write the build context: %v", err)
	}
	context, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer context.Close()
	image := fmt.Sprintf("superschematic-servergen-rust-test:%d", time.Now().UnixNano())
	build := exec.Command("docker", "build", "-q", "-t", image, "-f", built.Dockerfile, "-")
	build.Stdin = context
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("docker build: %v\n%s", err, output)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", image).Run() })
	if user := docker(t, "image", "inspect", "-f", "{{.Config.User}}", image); user == "" || user == "0" || user == "root" {
		t.Errorf("the image runs as %q, want a non-root user", user)
	}

	args := []string{"run", "-d", "-p", "127.0.0.1::8080",
		"-e", "RS_DB_DATABASE_URL=postgres://stock@127.0.0.1:9/rs_db?connect_timeout=1",
		"-e", ir.CallersField("rs-stock") + "_ISSUERS=0"}
	for _, v := range variables(t, "RS_PRICING_SERVICE", ir.ServiceEndpoint{URL: "http://127.0.0.1:9"}) {
		args = append(args, "-e", v)
	}
	container := docker(t, append(args, image)...)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", container).Run() })
	s := &started{base: "http://" + firstLine(docker(t, "port", container, "8080/tcp")), out: &strings.Builder{}}
	deadline := time.Now().Add(30 * time.Second)
	for {
		resp, err := http.Get(s.base + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the container did not answer /healthz: %v\n%s", err, docker(t, "logs", container))
		}
		time.Sleep(200 * time.Millisecond)
	}
	s.expect(t, http.MethodGet, "/api/levels", http.StatusNotImplemented, "levels.list_levels is not implemented")
	s.expect(t, http.MethodGet, "/readyz", http.StatusServiceUnavailable, `"unavailable":["rs-db"]`)
	docker(t, "stop", container)
	if code := docker(t, "inspect", "-f", "{{.State.ExitCode}}", container); code != "0" {
		t.Errorf("the container exited %s on docker stop:\n%s", code, docker(t, "logs", container))
	}
	if logs := docker(t, "logs", container); !strings.Contains(logs, `"msg":"stopped"`) {
		t.Errorf("the container did not log its graceful stop:\n%s", logs)
	}
}

// TestRustBuildKeepsTheLockfile: the Cargo workspace's lockfile, which the
// project commits (D57), outlasts every build of a stack, which empties
// server/<stack>, and the root it pairs with is the same bytes on each. A
// build that cannot bring the lockfile up to date, here over checkouts
// that are not on disk, or with no cargo on PATH, leaves it as it was and
// says so in one line. In a repository whose rules ignore the output root
// itself, the build says in one line how to commit the lockfile; once the
// rules take it back, it says nothing.
func TestRustBuildKeepsTheLockfile(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	repoRoot := testpaths.TempDir(t)
	if out, err := exec.Command(git, "-C", repoRoot, "init", "--quiet").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	ignore := filepath.Join(repoRoot, ".gitignore")
	if err := os.WriteFile(ignore, []byte("schemas/dist/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := loadRustFixture(t)
	paths := rustFakePaths(repoRoot)
	f.build(t, repoRoot, paths, rustOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	lock := filepath.Join(out, servergen.RustLockfileName)
	committed := []byte("# The lockfile the project committed.\nversion = 4\n")
	if err := os.WriteFile(lock, committed, 0o644); err != nil {
		t.Fatal(err)
	}
	root, err := os.ReadFile(filepath.Join(out, servergen.RustWorkspaceFile))
	if err != nil {
		t.Fatal(err)
	}
	rebuild := func() string {
		t.Helper()
		var log strings.Builder
		opts := f.options(repoRoot, paths)
		opts.Log = &log
		if _, err := generator.Run(f.schemas[rustStack], f.configs[rustStack], opts); err != nil {
			t.Fatalf("build %s: %v", rustStack, err)
		}
		if got, err := os.ReadFile(lock); err != nil || string(got) != string(committed) {
			t.Errorf("the build changed the lockfile it could not bring up to date: %v\n%s", err, got)
		}
		if got, err := os.ReadFile(filepath.Join(out, servergen.RustWorkspaceFile)); err != nil || string(got) != string(root) {
			t.Errorf("the build wrote another workspace root: %v\n%s", err, got)
		}
		return log.String()
	}

	log := rebuild()
	if _, err := exec.LookPath("cargo"); err == nil {
		if want := lock + ": not brought up to date: "; !strings.Contains(log, want) {
			t.Errorf("the build did not say %q:\n%s", want, log)
		}
	}
	if want := lock + " is ignored by .gitignore:1:schemas/dist/; commit it"; !strings.Contains(log, want) {
		t.Errorf("the build did not say %q:\n%s", want, log)
	}
	if err := os.WriteFile(ignore, []byte("schemas/dist/*\n!schemas/dist/Cargo.lock\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if log := rebuild(); strings.Contains(log, "is ignored by") {
		t.Errorf("the build named an ignore rule for a lockfile git takes back:\n%s", log)
	}
	t.Setenv("PATH", "")
	if log := rebuild(); !strings.Contains(log, lock+": no cargo on PATH to bring it up to date") {
		t.Errorf("the build did not say there is no cargo:\n%s", log)
	}
}

// TestTheRustWorkspaceNamesEveryStacksServers: the workspace's root names
// the Rust server crates of every stack under the output root, and only
// theirs: a server directory without a Cargo.toml, a Go or TypeScript
// server's, is no member. Once no Rust server is left, the root goes and
// the lockfile stays; a Cargo.toml at the output root that no build wrote
// is refused.
func TestTheRustWorkspaceNamesEveryStacksServers(t *testing.T) {
	out := t.TempDir()
	crate := func(stack, server string) {
		t.Helper()
		dir := servergen.ServerDir(out, stack, server)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, servergen.RustManifestFile), []byte("[package]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	crate("a-stack", "stock")
	crate("b-stack", "Warehouse")
	if err := os.MkdirAll(servergen.ServerDir(out, "a-stack", "orders"), 0o755); err != nil {
		t.Fatal(err)
	}
	if members, err := servergen.WriteRustWorkspace(out); err != nil || !members {
		t.Fatalf("WriteRustWorkspace = %v, %v", members, err)
	}
	root := filepath.Join(out, servergen.RustWorkspaceFile)
	if got, err := os.ReadFile(root); err != nil || string(got) != servergen.RustWorkspaceManifest([]string{"server/a-stack/stock", "server/b-stack/Warehouse"}) {
		t.Errorf("the root names other members: %v\n%s", err, got)
	}
	if err := os.RemoveAll(servergen.StackDir(out, "b-stack")); err != nil {
		t.Fatal(err)
	}
	if _, err := servergen.WriteRustWorkspace(out); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(root); err != nil || string(got) != servergen.RustWorkspaceManifest([]string{"server/a-stack/stock"}) {
		t.Errorf("the root names a stack that is gone: %v\n%s", err, got)
	}
	lock := filepath.Join(out, servergen.RustLockfileName)
	if err := os.WriteFile(lock, []byte("version = 4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(servergen.StackDir(out, "a-stack")); err != nil {
		t.Fatal(err)
	}
	if members, err := servergen.WriteRustWorkspace(out); err != nil || members {
		t.Fatalf("WriteRustWorkspace with no Rust server = %v, %v", members, err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the root outlived the last Rust server: %v", err)
	}
	if _, err := os.Stat(lock); err != nil {
		t.Errorf("the lockfile went with the root: %v", err)
	}
	if err := os.WriteFile(root, []byte("[workspace]\nmembers = [\"tools\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	crate("a-stack", "stock")
	if _, err := servergen.WriteRustWorkspace(out); err == nil || !strings.Contains(err.Error(), "is not the Cargo workspace a build writes") {
		t.Errorf("WriteRustWorkspace over a project's Cargo.toml = %v", err)
	}
}

// TestRustRoutesAcrossAPIs: two APIs a Rust server serves cannot register
// one method and path, path parameters matched whatever their names, and
// a server whose name no binary of cargo's takes is refused.
func TestRustRoutesAcrossAPIs(t *testing.T) {
	api := func(name, path string) servergen.RustAPIInput {
		return servergen.RustAPIInput{
			Output: &rustrestgen.APIOutput{APIOutputBase: rustapigen.APIOutputBase{SchemaName: name, CrateName: "schemas-" + name + "-api"}, Deps: &rustrestgen.DepsInfo{}},
			Routes: &apigen.APIOutput{SchemaName: name, Endpoints: []apigen.EndpointInfo{{Method: http.MethodGet, Path: path}}},
		}
	}
	in := servergen.RustInput{
		Stack:  "stock-stack",
		Server: "Warehouse",
		Dir:    t.TempDir(),
		APIs:   []servergen.RustAPIInput{api("catalog", "/api/items/{sku}"), api("pricing", "/api/items/{id}")},
		Paths:  naming.LocalPaths{HTTPRuntimeRust: t.TempDir()},
	}
	if _, err := servergen.PlanRust(in); err == nil || !strings.Contains(err.Error(), "catalog registers GET /api/items/{sku} and pricing registers GET /api/items/{id}") {
		t.Errorf("PlanRust = %v, want the routes refused", err)
	}
	in.APIs = in.APIs[:1]
	in.Server = "Ware.house"
	if _, err := servergen.PlanRust(in); err == nil || !strings.Contains(err.Error(), "cargo names a binary with letters, digits") {
		t.Errorf("PlanRust = %v, want the server's name refused", err)
	}
	in.Server = "Warehouse"
	if s, err := servergen.PlanRust(in); err != nil || s.Binary != "Warehouse" || s.Package != "schemas-stock-stack-warehouse-server" {
		t.Errorf("PlanRust = %+v, %v", s, err)
	}
}
