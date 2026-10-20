package servergen_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	// server rs-stock, on rs-db, its authDb, with the bucket rs-media
	// (D54), calls rs-pricing's Rust server, which admits only calling
	// services; rs-orders' Go server calls rs-stock, whose reservations
	// only it may make; and the site rs-web calls rs-stock. Its one
	// environment is local. Beside it, rs-merged-stack's declared Rust
	// server Warehouse serves rs-catalog and rs-pricing, and rs-catalog's
	// calls of rs-pricing stay within it (docs/stack-model.md, section
	// 9.9).
	rustServicesRoot = "testdata/rust/services"

	// rustStack is the fixture's stack, and rustMergedStack the second,
	// whose server rustMergedServer serves two APIs.
	rustStack        = "rs-stack"
	rustMergedStack  = "rs-merged-stack"
	rustMergedServer = "Warehouse"

	// rustStockJob and rustStockWorker are rs-stock's job and worker
	// (D65): the job enqueues a StockLow message of rs-db per product it
	// is given, and the worker handles them.
	rustStockJob    = "rs-stock-place-restock-orders"
	rustStockWorker = "rs-stock-reorder-stock"

	// rustGoldenRoot holds the Rust entrypoints as the output root lays
	// them out, and the workspace's root.
	rustGoldenRoot = "testdata/golden/rust"
)

// rustOrder is the order a build-all builds the Rust fixture in: rs-stock
// calls rs-pricing, so it builds after rs-pricing's SDK, and rs-orders
// calls rs-stock. rs-media, the bucket rs-stock lists, builds nothing
// (D54). rs-web, the site, is loaded, not built.
var rustOrder = []string{"rs-db", "rs-media", "rs-pricing", "rs-catalog", "rs-stock", "rs-orders", rustStack, rustMergedStack}

// rustMediaVariables are the variables of rs-stock's bucket field holding
// rs-media on an emulator at port 9, which discards: opening the bucket
// makes no request, and signing a URL for it none either (D54).
func rustMediaVariables(t *testing.T) []string {
	t.Helper()
	return variables(t, "RS_MEDIA_BUCKET", ir.BucketConnection{Name: "rs-media", Endpoint: "http://127.0.0.1:9"})
}

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
// writes, the workspace's root, and the manifest of each implementation's
// mirror (D65), by their path under rustGoldenRoot.
func rustGenerated(repoRoot string) map[string]string {
	out := filepath.Join(repoRoot, "schemas", "dist")
	files := map[string]string{servergen.RustWorkspaceFile: filepath.Join(out, servergen.RustWorkspaceFile)}
	for _, server := range [][2]string{{rustStack, "rs-pricing"}, {rustStack, "rs-stock"}, {rustStack, rustStockJob}, {rustStack, rustStockWorker}, {rustMergedStack, rustMergedServer}} {
		for _, file := range []string{servergen.RustManifestFile, servergen.RustMainFile, servergen.DockerFile, servergen.DockerIgnoreFile} {
			files[filepath.Join("server", server[0], server[1], filepath.FromSlash(file))] = filepath.Join(servergen.ServerDir(out, server[0], server[1]), filepath.FromSlash(file))
		}
	}
	for _, service := range []string{"rs-catalog", "rs-pricing", "rs-stock"} {
		files[filepath.Join(servergen.RustMirrorRoot, service, servergen.RustManifestFile)] = filepath.Join(servergen.RustMirrorDir(out, service), servergen.RustManifestFile)
	}
	return files
}

// TestRustEntrypointGolden: the stack's build writes a crate per Rust
// server, a member of the output root's Cargo workspace, whose root it
// writes too: rs-stock's opens a pool on rs-db, builds the identity store
// over it and its identity service from its identity config field, a
// client of rs-pricing that sends the edge's credential, rs-media on GCS
// or its emulator with the runtime's gcs feature, as its job's and its
// worker's do too (D54), its service
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

// rustCatalogImplementation is rs-catalog's implementation: an entry
// named by rs-pricing's quote, which it asks for with the client in its
// Deps. On Warehouse, which serves both, the quote names Warehouse, the
// caller rs-pricing saw.
const rustCatalogImplementation = `use std::sync::Arc;

use async_trait::async_trait;
use schemas_rs_catalog_api::types;
use schemas_rs_catalog_api::{CatalogGetEntryArgs, CatalogImplementation, Deps, Handlers};
use superschematic_http_runtime::{ApiError, RequestContext};

pub async fn create(deps: Deps) -> Result<Handlers, Box<dyn std::error::Error + Send + Sync>> {
    Ok(Handlers {
        catalog: Arc::new(Catalog { deps: Arc::new(deps) }),
    })
}

pub struct Catalog {
    deps: Arc<Deps>,
}

#[async_trait]
impl CatalogImplementation for Catalog {
    async fn get_entry(&self, _ctx: RequestContext, args: CatalogGetEntryArgs) -> Result<types::Entry, ApiError> {
        let quote = self
            .deps
            .rs_pricing
            .quotes
            .get_quote(args.sku.clone(), None)
            .await
            .map_err(|err| ApiError::internal(format!("rs-pricing: {err}")))?;
        Ok(types::Entry {
            sku: args.sku,
            name: quote.sku,
        })
    }
}
`

// rustStockImplementation is rs-stock's implementation: a level in the
// warehouse its setting names, priced by rs-pricing, which it calls with
// its edge's credential; a slow level, which answers after a second; a
// level whose sku is a URL to download its sheet from rs-media, which
// signing makes no request for (D54); and a reservation for the service
// that asks for it. Its job, PlaceRestockOrders,
// enqueues a StockLow message per product RESTOCK_SKUS names, in a
// transaction that commits, after one in a transaction that rolls back,
// which leaves nothing; with no products it does nothing, and with
// RESTOCK_FAIL set it fails, naming its warehouse and its bucket. Its
// worker, ReorderStock, handles each message: poison fails every time,
// flaky fails its first time, and slow never finishes. Its manual route,
// exportLevel, answers the level as CSV, with the warehouse its Deps'
// config holds and the request's id, which the router hands the handler
// in its RequestContext (D51).
const rustStockImplementation = `use std::collections::HashSet;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use async_trait::async_trait;
use axum::body::Body;
use axum::extract::Request;
use axum::response::Response;
use schemas_rs_stock_api::database_types::{self, StockLow};
use schemas_rs_stock_api::types;
use schemas_rs_stock_api::{
    BoxError, Deps, Handlers, Jobs, LevelsGetLevelArgs, LevelsImplementation, LevelsSetLevelArgs,
    ManualRoutes, ReservationsImplementation, ReservationsReserveStockArgs, Workers,
};
use superschematic_http_runtime::bucket::SignedUrlOptions;
use superschematic_http_runtime::queue::{enqueue, QueueDefinition};
use superschematic_http_runtime::{ApiError, RequestContext, Shutdown};

pub async fn create_manual_routes(deps: Deps) -> Result<Arc<dyn ManualRoutes>, Box<dyn std::error::Error + Send + Sync>> {
    Ok(Arc::new(StockExports { deps }))
}

pub struct StockExports {
    deps: Deps,
}

#[async_trait]
impl ManualRoutes for StockExports {
    async fn levels_export_level(&self, ctx: RequestContext, _request: Request) -> Result<Response, ApiError> {
        let sku = ctx.path_params.get("sku").cloned().unwrap_or_default();
        Response::builder()
            .header("content-type", "text/csv")
            .header("x-export-request", ctx.header("x-request-id").unwrap_or_default())
            .body(Body::from(format!("sku,warehouse\n{sku},{}\n", self.deps.config.warehouse)))
            .map_err(|err| ApiError::internal(err.to_string()))
    }
}

pub async fn create_jobs(deps: Deps) -> Result<Arc<dyn Jobs>, BoxError> {
    Ok(Arc::new(StockJobs { deps }))
}

pub struct StockJobs {
    deps: Deps,
}

#[async_trait]
impl Jobs for StockJobs {
    async fn place_restock_orders(&self, _shutdown: Shutdown) -> Result<(), BoxError> {
        if std::env::var_os("RESTOCK_FAIL").is_some() {
            return Err(format!("the restock of {} into {} failed", self.deps.config.warehouse, self.deps.rs_media.name()).into());
        }
        let skus = std::env::var("RESTOCK_SKUS").unwrap_or_default();
        if skus.is_empty() {
            return Ok(());
        }
        let queue = QueueDefinition::parse(database_types::STOCK_LOW_QUEUE)?;
        let mut client = self.deps.db.get().await?;
        let rolled_back = client.transaction().await?;
        enqueue(&rolled_back, &queue, &StockLow { sku: "ghost".to_string(), quantity: 1.0 }).await?;
        rolled_back.rollback().await?;
        let tx = client.transaction().await?;
        for sku in skus.split(',') {
            enqueue(&tx, &queue, &StockLow { sku: sku.to_string(), quantity: 1.0 }).await?;
        }
        tx.commit().await?;
        Ok(())
    }
}

pub async fn create_workers(deps: Deps) -> Result<Arc<dyn Workers>, BoxError> {
    Ok(Arc::new(StockWorkers { _deps: deps, failed: Mutex::new(HashSet::new()) }))
}

pub struct StockWorkers {
    _deps: Deps,
    failed: Mutex<HashSet<String>>,
}

#[async_trait]
impl Workers for StockWorkers {
    async fn reorder_stock(&self, message: StockLow) -> Result<(), BoxError> {
        let first = self.failed.lock().map(|mut failed| failed.insert(message.sku.clone())).unwrap_or(false);
        match message.sku.as_str() {
            "poison" => Err("poison every time".into()),
            "flaky" if first => Err("flaky the first time".into()),
            "slow" => {
                std::future::pending::<()>().await;
                Ok(())
            }
            _ => Ok(()),
        }
    }
}

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
        if args.sku == "sheet" {
            let url = self
                .deps
                .rs_media
                .signed_url("sheets/sheet.pdf", SignedUrlOptions::get(Duration::from_secs(60)))
                .await
                .map_err(|err| ApiError::internal(err.to_string()))?;
            return Ok(level(&self.deps, url, 0.0));
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
// this checkout's runtimes, checks that rs-stock's scaffold, its manual
// routes' handlers, jobs and workers among them, passes clippy, writes the
// implementations, builds the stacks again, which copies them into the
// output root's mirrors (D65), and builds the workspace's servers with
// cargo, frozen to the lockfile the stack's build wrote. It returns the
// repository root and the directory of the binaries.
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
	target := rustTargetDir(t)
	cargoCommand(t, cargo, naming.Default().RustImplementationDir(repoRoot, "rs-stock"), target, "clippy", "--quiet", "--", "-D", "warnings")
	writeRustImplementations(t, repoRoot)
	f.build(t, repoRoot, paths, rustStack, rustMergedStack)
	cargoCommand(t, cargo, out, target, "clippy", "--locked", "--workspace", "--quiet", "--", "-D", "warnings")
	cargoCommand(t, cargo, out, target, "build", "--locked", "--workspace", "--quiet")
	return repoRoot, filepath.Join(target, "debug")
}

// writeRustImplementations writes rs-catalog's, rs-pricing's and
// rs-stock's implementations over their scaffolds in the repository at
// repoRoot.
func writeRustImplementations(t *testing.T, repoRoot string) {
	t.Helper()
	names := naming.Default()
	for service, code := range map[string]string{"rs-catalog": rustCatalogImplementation, "rs-pricing": rustPricingImplementation, "rs-stock": rustStockImplementation} {
		if err := os.WriteFile(filepath.Join(names.RustImplementationDir(repoRoot, service), "src", "lib.rs"), []byte(code), 0o644); err != nil {
			t.Fatal(err)
		}
	}
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
// caller, answers its manual route with the handler the implementation's
// create_manual_routes built from its Deps, behind the route's rate limit
// (D51), refuses its staff route without an end user, answers the site's
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
		for _, server := range [][2]string{{rustStack, "rs-pricing"}, {rustStack, "rs-stock"}, {rustStack, rustStockJob}, {rustStack, rustStockWorker}, {rustMergedStack, rustMergedServer}} {
			main := filepath.Join(servergen.ServerDir(out, server[0], server[1]), filepath.FromSlash(servergen.RustMainFile))
			if diff, err := exec.Command(rustfmt, "--edition", "2021", "--check", main).CombinedOutput(); err != nil {
				t.Errorf("rustfmt lays out %s's main.rs otherwise: %v\n%s", server[1], err, diff)
			}
		}
	}
	checkRustJobRuns(t, filepath.Join(bin, rustStockJob))

	// Warehouse serves rs-catalog and rs-pricing on one port, and hands
	// each request to the router of the API whose operations take it, and
	// the routes both register, the OpenAPI document's, to the first. An
	// entry asks rs-pricing for its quote at the loopback URL the local
	// connector derives for a call within one server, with no credential,
	// here through a proxy that keeps the token the call carries.
	// rs-pricing admits it as Warehouse (docs/stack-model.md, section 9.9),
	// and a second Warehouse process, whose loopback key is its own,
	// refuses it.
	warehousePort := freePort(t)
	capture := captureLoopback(t, "http://127.0.0.1:"+warehousePort)
	warehouse := startOn(t, warehousePort, filepath.Join(bin, rustMergedServer), ir.CallersField("rs-pricing")+"_ISSUERS=0", "RS_PRICING_SERVICE_URL="+capture.url)
	warehouse.expect(t, http.MethodGet, "/readyz", http.StatusOK, `"ready"`)
	loopbackToken := checkLoopbackCalls(t, warehouse, capture, rustMergedServer, "/api/catalog/tea", "/api/quotes/tea", `"name":"tea:Warehouse"`)
	warehouse.expect(t, http.MethodGet, "/api/openapi.json", http.StatusOK, "/api/catalog/{sku}")
	warehouse.expect(t, http.MethodGet, "/api/nowhere", http.StatusNotFound)
	warehouse.stop(t)
	again := start(t, filepath.Join(bin, rustMergedServer), ir.CallersField("rs-pricing")+"_ISSUERS=0", "RS_PRICING_SERVICE_URL=http://127.0.0.1:9")
	again.sendWith(t, http.MethodGet, "/api/quotes/tea", "", map[string]string{"Service-Authorization": "Bearer " + loopbackToken}, http.StatusUnauthorized, "service_unauthorized")
	again.stop(t)

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
	env := append(append(append([]string{"RS_STOCK_CORS_ORIGINS=" + siteOrigin, "RS_STOCK_IDENTITY=" + rustIdentityConfig, "WAREHOUSE=north"}, edge...), callers...), rustMediaVariables(t)...)

	// Port 9 discards; nothing answers a database there.
	unreachable := "RS_DB_DATABASE_URL=postgres://stock@127.0.0.1:9/rs_db?connect_timeout=1&sslmode=disable"
	stock := start(t, filepath.Join(bin, "rs-stock"), append([]string{unreachable}, env...)...)
	stock.expect(t, http.MethodGet, "/healthz", http.StatusOK, `"ok"`)
	stock.expect(t, http.MethodGet, "/readyz", http.StatusServiceUnavailable, `"unavailable":["rs-db"]`)
	stock.expect(t, http.MethodGet, "/api/levels", http.StatusOK, `"warehouse":"north"`)
	stock.expect(t, http.MethodGet, "/api/levels/tea", http.StatusOK, `"sku":"tea:rs-stock"`, `"cents":250`)
	// rs-stock's Deps hold rs-media, opened on the emulator its bucket
	// field names, for which it signs a V4 URL as no account (D54).
	stock.expect(t, http.MethodGet, "/api/levels/sheet", http.StatusOK, `"sku":"http://127.0.0.1:9/rs-media/sheets/sheet.pdf?X-Goog-Algorithm=GOOG4-RSA-SHA256`, "X-Goog-Credential=emulator%40superschematic.local")
	stock.expect(t, http.MethodPut, "/api/levels/tea", http.StatusUnauthorized)
	// Its manual route answers with the handler create_manual_routes built
	// from its Deps, behind the route's rate limit of one a minute (D51).
	exportLevel(t, stock.base+"/api/levels/tea/export")
	stock.expect(t, http.MethodGet, "/api/levels/tea/export", http.StatusTooManyRequests, "too_many_requests")
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
	for _, want := range []string{`"msg":"server stopped"`, "configuration of rs-stock: ", "RS_DB_DATABASE_URL", "RS_PRICING_SERVICE_URL", "RS_MEDIA_BUCKET_NAME", "RS_STOCK_CALLERS_ISSUERS"} {
		if !strings.Contains(string(refused), want) {
			t.Errorf("rs-stock stopped without saying %q:\n%s", want, refused)
		}
	}
	// An identity config the runtime refuses stops it too, naming the
	// field.
	cmd = exec.Command(filepath.Join(bin, "rs-stock"))
	cmd.Env = append(append(append(os.Environ(), "PORT="+freePort(t), unreachable), append(edge, append(callers, `RS_STOCK_IDENTITY={"cookie": {"sameSite": "Loose"}}`)...)...), rustMediaVariables(t)...)
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

// exportLevel calls rs-stock's manual route at url: the router hands the
// request to the implementation's handler, whose response it sends as it
// is, the CSV with the warehouse rs-stock's config holds, not the JSON
// envelope, and with the request's id, which the router's request_ids
// layer settled and the RequestContext it handed the handler holds.
func exportLevel(t *testing.T, url string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Request-Id", "export-1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || string(body) != "sku,warehouse\ntea,north\n" || resp.Header.Get("Content-Type") != "text/csv" {
		t.Errorf("GET %s = %d %s: %q, want 200 text/csv from the manual route's handler", url, resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	if id := resp.Header.Get("X-Export-Request"); id != "export-1" {
		t.Errorf("GET %s: the handler's RequestContext held request id %q, want the request's export-1", url, id)
	}
	if id := resp.Header.Get("X-Request-Id"); id != "export-1" {
		t.Errorf("GET %s answered x-request-id %q, want export-1: the router's request_ids layer did not wrap the manual route", url, id)
	}
}

// TestARustImplementationThatPredatesItsManualRoutesFails: rs-stock
// declares a @manualRouteRegistration operation, exportLevel, and its
// implementation, which exists and so is the engineer's, declares no
// create_manual_routes, whose handlers rs-stock's main.rs mounts with
// build_router_with_manual_routes (D51). The build writes nothing into it
// and fails, saying what to add; with it declared, it builds.
func TestARustImplementationThatPredatesItsManualRoutesFails(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadRustFixture(t)
	f.build(t, repoRoot, rustFakePaths(repoRoot), rustOrder[:len(rustOrder)-1]...)
	dir := naming.Default().RustImplementationDir(repoRoot, "rs-stock")
	lib := filepath.Join(dir, rustrestgen.ImplementationLib)
	before := "// The engineer's code, from before the manual route.\npub async fn create() {}\n\npub async fn create_jobs() {}\n\npub async fn create_workers() {}\n"
	if err := os.WriteFile(lib, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := generator.Run(f.schemas[rustStack], f.configs[rustStack], f.options(repoRoot, rustFakePaths(repoRoot)))
	for _, want := range []string{
		"stack rs-stack: rs-stock declares @manualRouteRegistration operations, and its implementation at " + dir + ", which the build no longer writes into, declares no create_manual_routes in " + rustrestgen.ImplementationLib,
		"pub async fn create_manual_routes(deps: Deps) -> Result<Arc<dyn ManualRoutes>, Box<dyn std::error::Error + Send + Sync>>",
		"impl ManualRoutes for ... {\n      async fn levels_export_level(&self, ctx: RequestContext, request: Request) -> Result<Response, ApiError> {\n          // answer GET /api/levels/{sku}/export",
		"where Deps and ManualRoutes are schemas_rs_stock_api's",
		"(docs/stack-model.md, section 8.6)",
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("build = %v, want it to say %q", err, want)
		}
	}
	if got, err := os.ReadFile(lib); err != nil || string(got) != before {
		t.Errorf("a refused build wrote %s: %q, %v", lib, got, err)
	}
	if err := os.WriteFile(lib, []byte(before+"\npub async fn create_manual_routes() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := generator.Run(f.schemas[rustStack], f.configs[rustStack], f.options(repoRoot, rustFakePaths(repoRoot))); err != nil {
		t.Fatalf("a build over an implementation that declares create_manual_routes = %v", err)
	}
}

// checkRustJobRuns runs rs-stock's job, the binary at job, without a
// database, a callee or a bucket that answers (D65). It reads rs-stock's
// configuration without the callers field, which no job is given, builds
// its Deps, rs-media among them (D54), and runs its method once: with no
// products to restock, the method touches nothing, and the job logs job
// started and job done, its lines naming the job, and exits 0. Told to
// fail, it logs job failed with the error, which names its bucket, and
// exits 1, and without its configuration it fails at once, naming each
// variable missing.
func checkRustJobRuns(t *testing.T, job string) {
	t.Helper()
	env := append(append([]string{"RS_DB_DATABASE_URL=postgres://stock@127.0.0.1:9/rs_db?connect_timeout=1&sslmode=disable", "WAREHOUSE=north"},
		variables(t, "RS_PRICING_SERVICE", ir.ServiceEndpoint{URL: "http://127.0.0.1:9"})...), rustMediaVariables(t)...)
	run := func(extra ...string) (string, int) {
		t.Helper()
		cmd := exec.Command(job)
		cmd.Env = append(os.Environ(), append(slices.Clone(env), extra...)...)
		output, err := cmd.CombinedOutput()
		var exit *exec.ExitError
		switch {
		case err == nil:
			return string(output), 0
		case errors.As(err, &exit):
			return string(output), exit.ExitCode()
		}
		t.Fatalf("run %s: %v\n%s", job, err, output)
		return "", 0
	}
	output, code := run()
	if code != 0 {
		t.Fatalf("the job exited %d, want 0:\n%s", code, output)
	}
	for _, want := range []string{`"msg":"job started"`, `"api":"rs-stock","class":"PlaceRestockOrders"`, `"msg":"job done"`, `"job":"` + rustStockJob + `"`, `"took":`} {
		if !strings.Contains(output, want) {
			t.Errorf("the job's output lacks %s:\n%s", want, output)
		}
	}
	if strings.Contains(output, `"server":`) {
		t.Errorf("the job's lines name a server:\n%s", output)
	}
	output, code = run("RESTOCK_FAIL=1")
	if code != 1 || !strings.Contains(output, `"msg":"job failed"`) || !strings.Contains(output, `"error":"the restock of north into rs-media failed"`) {
		t.Errorf("a failing job exited %d, want 1 with job failed and its error:\n%s", code, output)
	}
	cmd := exec.Command(job)
	cmd.Env = os.Environ()
	refused, err := cmd.CombinedOutput()
	if err == nil {
		t.Errorf("the job ran without its configuration:\n%s", refused)
	}
	for _, want := range []string{`"msg":"job failed"`, "configuration of rs-stock: ", "RS_DB_DATABASE_URL", "RS_PRICING_SERVICE_URL", "RS_MEDIA_BUCKET_NAME"} {
		if !strings.Contains(string(refused), want) {
			t.Errorf("the job failed without saying %q:\n%s", want, refused)
		}
	}
	if strings.Contains(string(refused), "RS_STOCK_CALLERS") {
		t.Errorf("the job asks for the callers field, which no job is given:\n%s", refused)
	}
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

// rustDockerFixture builds the Rust fixture into a repository root of its
// own that holds copies of the HTTP runtime, the schema runtime and
// superscalar's crate, which an image's context takes, with the scaffolds
// and the workspace's lockfile the stack's build wrote, and returns the
// root and its output root.
func rustDockerFixture(t *testing.T) (string, string) {
	t.Helper()
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
	return repoRoot, out
}

// buildRustImage builds the image of the Dockerfile of the crate at dir
// from the context a deploy writes (stackdeploy.WriteContext) out of
// repoRoot, checks it runs as a non-root user, and removes it when the test
// ends.
func buildRustImage(t *testing.T, repoRoot, dir string) string {
	t.Helper()
	archive := filepath.Join(t.TempDir(), "context.tar.gz")
	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	built, err := stackdeploy.WriteContext(file, repoRoot, filepath.Join(dir, servergen.DockerFile))
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
	build := exec.Command("docker", "build", "--progress=plain", "-t", image, "-f", built.Dockerfile, "-")
	build.Stdin = context
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("docker build: %v\n%s", err, output)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", image).Run() })
	if user := docker(t, "image", "inspect", "-f", "{{.Config.User}}", image); user == "" || user == "0" || user == "root" {
		t.Errorf("the image runs as %q, want a non-root user", user)
	}
	return image
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
	repoRoot, out := rustDockerFixture(t)
	image := buildRustImage(t, repoRoot, servergen.ServerDir(out, rustStack, "rs-stock"))

	args := []string{"run", "-d", "-p", "127.0.0.1::8080",
		"-e", "RS_DB_DATABASE_URL=postgres://stock@127.0.0.1:9/rs_db?connect_timeout=1",
		"-e", ir.CallersField("rs-stock") + "_ISSUERS=0"}
	for _, v := range append(variables(t, "RS_PRICING_SERVICE", ir.ServiceEndpoint{URL: "http://127.0.0.1:9"}), rustMediaVariables(t)...) {
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

// TestTheRustJobAndWorkerDockerfilesBuildImagesThatRun: rs-stock's job's
// and worker's Dockerfiles build from a deploy's context as a server's do
// (D65), each image running its binary as a non-root user, with no port.
// Given only the variables of rs-stock's edges, its database's, its
// callee's and its bucket's, and none of the callers, identity or CORS fields only its
// server is given, the job runs its scaffold's method, which fails, and
// exits 1, logging job failed with the error; the worker starts, claims
// from a database that does not answer, and on docker stop exits 0. It
// needs Docker and the network, and runs only outside -short.
func TestTheRustJobAndWorkerDockerfilesBuildImagesThatRun(t *testing.T) {
	needDocker(t)
	if _, err := exec.LookPath("cargo"); err != nil {
		t.Skip("cargo not available: the stack's build writes the lockfile with it")
	}
	repoRoot, out := rustDockerFixture(t)
	args := []string{"-e", "RS_DB_DATABASE_URL=postgres://stock@127.0.0.1:9/rs_db?connect_timeout=1"}
	for _, v := range append(variables(t, "RS_PRICING_SERVICE", ir.ServiceEndpoint{URL: "http://127.0.0.1:9"}), rustMediaVariables(t)...) {
		args = append(args, "-e", v)
	}
	noPort := func(image string) {
		t.Helper()
		if ports := docker(t, "image", "inspect", "-f", "{{json .Config.ExposedPorts}}", image); ports != "null" && ports != "{}" {
			t.Errorf("the image %s exposes %s, want no port", image, ports)
		}
		if env := docker(t, "image", "inspect", "-f", "{{json .Config.Env}}", image); strings.Contains(env, "PORT=") {
			t.Errorf("the image %s sets %s, want no PORT", image, env)
		}
	}

	job := buildRustImage(t, repoRoot, servergen.ServerDir(out, rustStack, rustStockJob))
	noPort(job)
	run := exec.Command("docker", append(append([]string{"run", "--rm"}, args...), job)...)
	logs, err := run.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Errorf("the job's container = %v, want exit status 1:\n%s", err, logs)
	}
	for _, want := range []string{`"msg":"job started"`, `"msg":"job failed"`, `"error":"job PlaceRestockOrders is not implemented"`, `"job":"` + rustStockJob + `"`} {
		if !strings.Contains(string(logs), want) {
			t.Errorf("the job's container did not log %s:\n%s", want, logs)
		}
	}

	worker := buildRustImage(t, repoRoot, servergen.ServerDir(out, rustStack, rustStockWorker))
	noPort(worker)
	container := docker(t, append(append([]string{"run", "-d"}, args...), worker)...)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", container).Run() })
	deadline := time.Now().Add(30 * time.Second)
	for !strings.Contains(docker(t, "logs", container), `"msg":"worker started"`) {
		if time.Now().After(deadline) {
			t.Fatalf("the worker's container did not start:\n%s", docker(t, "logs", container))
		}
		time.Sleep(200 * time.Millisecond)
	}
	docker(t, "stop", container)
	if code := docker(t, "inspect", "-f", "{{.State.ExitCode}}", container); code != "0" {
		t.Errorf("the worker's container exited %s on docker stop:\n%s", code, docker(t, "logs", container))
	}
	if logs := docker(t, "logs", container); !strings.Contains(logs, `"msg":"worker stopped"`) || !strings.Contains(logs, `"worker":"`+rustStockWorker+`"`) {
		t.Errorf("the worker's container did not log its stop:\n%s", logs)
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

// TestRustServersBuildInAnyOutputRoot (D65): the implementations, whose
// Cargo.toml files name the API crates under schemas/dist, where their
// scaffolds were written, build into two other output roots, one after the
// other and both at once. Each root's build mirrors them into its impl/,
// and cargo builds every server of the root's workspace frozen to the
// root's lockfile, against the root's own API crates and mirrors alone.
// The lockfile the first root's build wrote holds in the second as it is,
// as a project's committed one would, since it names no path crate's
// directory, and outlasts another build of either; a change to an
// implementation reaches each root's mirror with its next build.
func TestRustServersBuildInAnyOutputRoot(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo build in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust build")
	}
	paths := testpaths.Local(t)
	repoRoot := testpaths.TempDir(t)
	f := loadRustFixture(t)
	f.build(t, repoRoot, paths, rustOrder...)
	writeRustImplementations(t, repoRoot)
	roots := []string{filepath.Join(testpaths.TempDir(t), "a"), filepath.Join(testpaths.TempDir(t), "b")}
	buildInto := func(out string, services ...string) error {
		opts := f.options(repoRoot, paths)
		opts.OutputRoot = out
		for _, name := range services {
			if _, err := generator.Run(f.schemas[name], f.configs[name], opts); err != nil {
				return fmt.Errorf("build %s into %s: %w", name, out, err)
			}
		}
		return nil
	}
	target := rustTargetDir(t)
	cargoBuild := func(out string) error {
		cmd := exec.Command(cargo, "build", "--locked", "--workspace", "--quiet")
		cmd.Dir = out
		cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+target)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("cargo build --locked in %s: %w\n%s", out, err, output)
		}
		return nil
	}
	lockfile := func(out string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(out, servergen.RustLockfileName))
		if err != nil {
			t.Fatalf("the build of %s wrote no lockfile: %v", out, err)
		}
		return string(data)
	}

	// One root after the other, the second from the first's lockfile.
	var lock string
	for i, out := range roots {
		if i > 0 {
			if err := os.MkdirAll(out, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(out, servergen.RustLockfileName), []byte(lock), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := buildInto(out, rustOrder...); err != nil {
			t.Fatal(err)
		}
		if err := cargoBuild(out); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			lock = lockfile(out)
		} else if got := lockfile(out); got != lock {
			t.Errorf("the build of %s changed the lockfile of %s:\n%s\n---\n%s", out, roots[0], lock, got)
		}
	}

	// Both at once, after a change to rs-pricing's implementation.
	pricing := filepath.Join(naming.Default().RustImplementationDir(repoRoot, "rs-pricing"), "src", "lib.rs")
	const changed = "\n// The mirror follows the crate (D65).\n"
	if err := os.WriteFile(pricing, []byte(rustPricingImplementation+changed), 0o644); err != nil {
		t.Fatal(err)
	}
	together := func(run func(out string) error) {
		t.Helper()
		errs := make(chan error, len(roots))
		for _, out := range roots {
			go func() { errs <- run(out) }()
		}
		for range roots {
			if err := <-errs; err != nil {
				t.Error(err)
			}
		}
		if t.Failed() {
			t.FailNow()
		}
	}
	together(func(out string) error { return buildInto(out, rustStack, rustMergedStack) })
	together(cargoBuild)
	for _, out := range roots {
		if got := lockfile(out); got != lock {
			t.Errorf("another build changed %s's lockfile:\n%s", out, got)
		}
		mirrored, err := os.ReadFile(filepath.Join(servergen.RustMirrorDir(out, "rs-pricing"), "src", "lib.rs"))
		if err != nil || !strings.HasSuffix(string(mirrored), changed) {
			t.Errorf("the change to rs-pricing did not reach %s's mirror: %v", out, err)
		}
		// cargo tree prints a path crate with its directory, and resolves
		// the host's dependencies alone, which the build downloaded.
		generated := 0
		for _, line := range strings.Split(cargoCommand(t, cargo, out, target, "tree", "--locked", "--offline", "--workspace", "--prefix", "none", "--format", "{p}"), "\n") {
			crate := cargoTreePath.FindStringSubmatch(line)
			if crate == nil || !strings.HasPrefix(crate[1], naming.Default().RustCratePrefix) {
				continue
			}
			generated++
			under := out
			if strings.HasSuffix(crate[1], "-implementation") {
				under = filepath.Join(out, servergen.RustMirrorRoot)
			}
			if rel, err := filepath.Rel(under, crate[2]); err != nil || strings.HasPrefix(rel, "..") {
				t.Errorf("%s's workspace builds %s from %s, outside %s", out, crate[1], crate[2], under)
			}
		}
		if generated == 0 {
			t.Errorf("%s's workspace builds no generated crate", out)
		}
	}
}

// cargoTreePath matches a path crate in cargo tree's lines: its name and
// its directory.
var cargoTreePath = regexp.MustCompile(`^(\S+) v\S+ \(([^)]+)\)`)

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
