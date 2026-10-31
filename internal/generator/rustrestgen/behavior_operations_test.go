package rustrestgen

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/behaviorlayout"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/rustgen"
	"github.com/parable-work/superschematic/internal/generator/rustsdkgen"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// The behavior operations fixture (D103): fixture-workflow-api serves
// OrderActions, @behaviorOperations(Order) behind orders.write, and
// ShipmentActions, @behaviorOperations(Shipment) for any signed-in caller,
// over fixture-workflow-db, its one DB dependency, whose Order's status and
// Shipment's status and paymentStatus are Workflows; and OrderQueries,
// whose getOrder returns an Order as its table holds it.
const (
	behaviorAPI = "fixture-workflow-api"
	behaviorDB  = "fixture-workflow-db"
)

// loadBehaviorFixture loads fixture-workflow-api, its @behaviorOperations
// sets filled by the loader, and fixture-workflow-db, with the core's
// registry, which declares Workflow.
func loadBehaviorFixture(t *testing.T) (api, db *ir.Schema, reg *registry.Registry) {
	t.Helper()
	reg = registry.New(naming.Default())
	db, err := loader.LoadService(filepath.Join(fixturesDir, behaviorDB), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", behaviorDB, err)
	}
	api, err = loader.LoadService(filepath.Join(fixturesDir, behaviorAPI), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", behaviorAPI, err)
	}
	return api, db, reg
}

// generateBehaviorAPI generates fixture-workflow-api's crate as the
// dispatch layer does, with fixture-workflow-db among its Dependencies, so
// the OpenAPI document declares the Order that getOrder returns.
func generateBehaviorAPI(t *testing.T, db *ir.Schema) *APIOutput {
	t.Helper()
	api, err := loader.LoadService(filepath.Join(fixturesDir, behaviorAPI))
	if err != nil {
		t.Fatalf("load %s: %v", behaviorAPI, err)
	}
	outDir := t.TempDir()
	output, err := generateFrom(api, apiSource{upstream: behaviorDB, upstreamIR: db, authDB: authDBOf(t, api)}, Options{
		SchemaName:   behaviorAPI,
		Dependencies: map[string]*ir.Schema{behaviorDB: db},
		TypesCrate:   "schemas-" + behaviorAPI + "-types",
		TypesDir:     filepath.Join(outDir, "types", "rust", behaviorAPI),
		OutputDir:    filepath.Join(outDir, "api", behaviorAPI),
		Clock:        codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil || output == nil {
		t.Fatalf("generate %s: %v, %v", behaviorAPI, output, err)
	}
	return output
}

// behaviorLayout is the layout document of db's tables whose fields are
// behaviors, indented as the dispatch layer writes it.
func behaviorLayout(t *testing.T, db *ir.Schema, reg *registry.Registry) string {
	t.Helper()
	layout, err := behaviorlayout.Of(db, reg)
	if err != nil || layout == nil {
		t.Fatalf("the layout of %s: %v, %v", db.Name, layout, err)
	}
	encoded, err := json.MarshalIndent(layout, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// behaviorDeps are the Deps of fixture-workflow-api's crate: a pool of its
// database, the database's layout, and the adapter's and the behaviors'
// crates at a checkout's places from an API crate under
// /repo/schemas/dist/api/<service>, as writeGoldenAPI places the crate.
func behaviorDeps(layout string) DepsInfo {
	return DepsInfo{
		Database: behaviorDB,
		Behaviors: &DepsBehaviors{
			Layout:        layout,
			AdapterPath:   "../../../../runtime/engine/rust/rules-adapter",
			BehaviorsPath: "../../../../runtime/engine/rust/rules-behaviors",
		},
	}
}

// TestWriteRustAPIGoldenBehaviorOperations pins the crate of
// fixture-workflow-api (D103): the router mounts each behavior operation
// behind its set's controls and decodes its key and input into its Args
// struct, and its handler hands it to behaviors::run_operation, which runs
// it through superschematic-rules-adapter with the core of
// superschematic-rules-behaviors on a transaction of fixture-workflow-db's
// pool; the namespace traits have no method for it, so shipment, whose
// every operation is a behavior's, has no trait; Implementations holds the
// Behaviors that Implementations::new builds from the pool; src/behaviors.rs
// embeds the database's layout; and the crate depends on both crates by
// path. Regenerate with:
// go test ./internal/generator/rustrestgen -run TestWriteRustAPIGoldenBehaviorOperations -update
func TestWriteRustAPIGoldenBehaviorOperations(t *testing.T) {
	_, db, reg := loadBehaviorFixture(t)
	output := generateBehaviorAPI(t, db)
	if err := output.SetDeps(behaviorDeps(behaviorLayout(t, db, reg))); err != nil {
		t.Fatalf("set deps: %v", err)
	}
	if !slices.Equal(output.Namespaces, []string{"order"}) {
		t.Errorf("namespaces %v, want order alone: shipment's every operation is a behavior's", output.Namespaces)
	}
	var behaviors []string
	for _, endpoint := range output.Endpoints {
		if b := endpoint.Behavior; b != nil {
			behaviors = append(behaviors, endpoint.Namespace+"."+endpoint.Name+" "+b.Type+"."+b.Use+"."+b.Operation+" "+b.KeyField)
		}
	}
	if want := []string{
		"order.statusTransition Order.status.transition id",
		"shipment.paymentStatusTransition Shipment.paymentStatus.transition id",
		"shipment.statusTransition Shipment.status.transition id",
	}; !slices.Equal(behaviors, want) {
		t.Errorf("behavior operations %q, want %q", behaviors, want)
	}
	if got := len(output.ImplementedEndpoints()); got != 1 {
		t.Errorf("%d implemented endpoints, want getOrder alone", got)
	}

	generated := writeGoldenAPI(t, behaviorAPI, output)
	for file, want := range map[string][]string{
		"Cargo.toml": {
			`superschematic-http-runtime = { path = "../../../../runtime/http/rust", features = ["postgres"] }`,
			`superschematic-rules-adapter = { path = "../../../../runtime/engine/rust/rules-adapter", features = ["postgres"] }`,
			`superschematic-rules-behaviors = { path = "../../../../runtime/engine/rust/rules-behaviors" }`,
			`serde = "1.0.228"`,
		},
		"src/lib.rs":        {"pub mod behaviors;"},
		"src/interfaces.rs": {"pub trait OrderImplementation", "async fn get_order(", "pub behaviors: crate::behaviors::Behaviors,", "pub struct ShipmentStatusTransitionArgs"},
		"src/router.rs": {
			"use crate::behaviors;",
			`.authorize(Arc::clone(&state.implementations.authenticator), &["orders.write"])`,
			"let result: Result<types::OrderStatusTransitionResult, ApiError> = behaviors::run_operation(\n        &state.implementations.behaviors,\n        &ctx,\n        \"Order\",\n        &args.id,\n        \"status\",\n        \"transition\",\n        &args.input,\n    )\n    .await;",
			`"Shipment",` + "\n        &args.id,\n        \"paymentStatus\",",
		},
		"src/deps.rs":       {"db: Pool) -> Self {", "behaviors: crate::behaviors::Behaviors::new(db),"},
		"src/operations.rs": {"core runs: Order's status transition, a Workflow (D103)."},
		"src/behaviors.rs": {
			`pub const RULES_LAYOUT: &str = r#"{`,
			`"Order": {`,
			"superschematic_rules_behaviors::core",
			"postgres::run_operation(",
		},
	} {
		for _, text := range want {
			if !strings.Contains(generated[file], text) {
				t.Errorf("%s does not contain %q", file, text)
			}
		}
	}
	for _, absent := range []string{"ShipmentImplementation", "fn status_transition", "fn payment_status_transition"} {
		if strings.Contains(generated["src/interfaces.rs"], absent) {
			t.Errorf("src/interfaces.rs holds %s, which the behaviors' core serves", absent)
		}
	}
	if strings.Contains(generated["src/router.rs"], ".shipment\n") || strings.Contains(generated["src/router.rs"], ".status_transition(") {
		t.Error("src/router.rs calls an implementation of a behavior operation")
	}
	for _, permission := range []string{"orders.write", "orders.ship", "payments.refund"} {
		if !strings.Contains(output.PermissionCatalogJSON, `"`+permission+`"`) {
			t.Errorf("the crate's permissions.json does not list %s", permission)
		}
	}
}

// TestBehaviorOperationsNeedTheirDeps: a crate with behavior operations
// runs them on its Deps' pool, with the layout and the crates its Deps
// name, so Deps without them, Deps of another database, and no Deps at all
// are refused rather than written into a crate that does not build.
func TestBehaviorOperationsNeedTheirDeps(t *testing.T) {
	_, db, reg := loadBehaviorFixture(t)
	layout := behaviorLayout(t, db, reg)
	for name, tc := range map[string]struct {
		deps DepsInfo
		want string
	}{
		"no behaviors":      {DepsInfo{Database: behaviorDB}, "and its Deps names neither"},
		"another database":  {DepsInfo{Database: "shop-db", Behaviors: behaviorDeps(layout).Behaviors}, `its Deps holds a pool of "shop-db"`},
		"no adapter's path": {DepsInfo{Database: behaviorDB, Behaviors: &DepsBehaviors{Layout: layout, BehaviorsPath: "b"}}, "lacks the layout of fixture-workflow-db's tables or the paths"},
	} {
		t.Run(name, func(t *testing.T) {
			output := generateBehaviorAPI(t, db)
			err := output.SetDeps(tc.deps)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("SetDeps: %v, want an error containing %q", err, tc.want)
			}
		})
	}
	output := generateBehaviorAPI(t, db)
	if err := SetReplacePaths(output, naming.LocalPaths{HTTPRuntimeRust: "/repo/runtime/http/rust"}, "/repo/schemas/dist/api/"+behaviorAPI); err != nil {
		t.Fatal(err)
	}
	err := WriteAPI(output, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "serves the behavior operations of Order's status, a Workflow, among others (D103)") {
		t.Fatalf("WriteAPI without Deps: %v, want the behavior operations refused", err)
	}
}

// behaviorCrate is what buildBehaviorCrate writes: the API crate's
// directory and its output.
type behaviorCrate struct {
	dir    string
	output *APIOutput
}

// buildBehaviorCrate writes fixture-workflow-api's API crate as a build
// lays it out under a temporary root: fixture-workflow-db's Rust types, the
// API's Rust types, and the API crate with its Deps, which name the
// layout and the checkout's adapter and behaviors crates by path. With
// sdk, it writes the API's Rust SDK beside it and makes it a dependency of
// the crate, and the database's Postgres DDL into its tests directory as
// behaviors_postgres.sql. The crate's manifest takes the dev-dependencies
// and the patch of the runtime crates a test of it needs.
func buildBehaviorCrate(t *testing.T, sdk bool) behaviorCrate {
	t.Helper()
	paths := testpaths.Local(t)
	api, db, reg := loadBehaviorFixture(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	names := naming.Default()
	typesRoot := filepath.Join(root, "types", "rust")
	for _, service := range []struct {
		schema *ir.Schema
		deps   map[string]*ir.Schema
	}{{db, nil}, {api, map[string]*ir.Schema{behaviorDB: db}}} {
		crates := map[string]string{}
		for name := range service.deps {
			crates[name] = names.RustTypesCrate(name)
		}
		types, err := rustgen.Generate(service.schema, rustgen.Options{
			SchemaName:       service.schema.Name,
			Dependencies:     service.deps,
			DependencyCrates: crates,
			Registry:         reg,
			Clock:            nestedArraysClock,
		})
		if err != nil {
			t.Fatalf("rustgen.Generate %s: %v", service.schema.Name, err)
		}
		if err := rustgen.WriteTypes(types, filepath.Join(typesRoot, service.schema.Name)); err != nil {
			t.Fatalf("rustgen.WriteTypes %s: %v", service.schema.Name, err)
		}
	}

	apiDir := filepath.Join(root, "api", behaviorAPI)
	src := apiSource{upstream: behaviorDB, upstreamIR: db}
	output, err := generateFrom(api, src, Options{
		SchemaName: behaviorAPI,
		TypesCrate: names.RustTypesCrate(behaviorAPI),
		TypesDir:   filepath.Join(typesRoot, behaviorAPI),
		OutputDir:  apiDir,
		Clock:      nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	deps := behaviorDeps(behaviorLayout(t, db, reg))
	if deps.Behaviors.AdapterPath, err = naming.RelPath(apiDir, paths.RulesAdapterRust); err != nil {
		t.Fatal(err)
	}
	if deps.Behaviors.BehaviorsPath, err = naming.RelPath(apiDir, paths.RulesBehaviorsRust); err != nil {
		t.Fatal(err)
	}
	if err := output.SetDeps(deps); err != nil {
		t.Fatalf("set deps: %v", err)
	}
	if err := SetReplacePaths(output, paths, apiDir); err != nil {
		t.Fatalf("set replace paths: %v", err)
	}
	if err := WriteAPI(output, apiDir); err != nil {
		t.Fatalf("write api: %v", err)
	}

	var sdkDependency string
	if sdk {
		apiOutput, err := apiOutputOf(api, src, Options{SchemaName: behaviorAPI, Clock: nestedArraysClock})
		if err != nil {
			t.Fatal(err)
		}
		sdkDir := filepath.Join(root, "sdk", "rust", behaviorAPI)
		sdkOutput, err := rustsdkgen.Generate(apiOutput, names.RustSDKCrate(behaviorAPI), output.TypesCrate, nestedArraysClock)
		if err != nil {
			t.Fatalf("rustsdkgen.Generate: %v", err)
		}
		if err := rustsdkgen.WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, output.TypesDir, nestedArraysClock); err != nil {
			t.Fatalf("write the Rust SDK: %v", err)
		}
		sdkDependency = fmt.Sprintf("%s = { path = %q }\n", sdkOutput.CrateName, "../../sdk/rust/"+behaviorAPI)
		ddl, err := sqlgen.Generate(db, sqlgen.Options{SchemaName: behaviorDB})
		if err != nil {
			t.Fatalf("generate the DDL of %s: %v", behaviorDB, err)
		}
		ddlDir := t.TempDir()
		if err := sqlgen.WriteDDL(ddl, ddlDir); err != nil {
			t.Fatal(err)
		}
		create, err := os.ReadFile(filepath.Join(ddlDir, "create.sql"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(apiDir, "tests"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(apiDir, "tests", "behaviors_postgres.sql"), create, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	manifest, err := os.ReadFile(filepath.Join(apiDir, "Cargo.toml"))
	if err != nil {
		t.Fatal(err)
	}
	// The generated manifest ends in its [dependencies] table.
	manifest = append(manifest, []byte(sdkDependency+`
[dev-dependencies]
tokio = { version = "1.52.2", features = ["test-util"] }

`+testpaths.RustPatch(paths, names))...)
	if err := os.WriteFile(filepath.Join(apiDir, "Cargo.toml"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	return behaviorCrate{dir: apiDir, output: output}
}

// cargoInBehaviorCrate runs cargo with args in the crate, in
// CARGO_TARGET_DIR when it is set, and fails the test with its output when
// it fails.
func cargoInBehaviorCrate(t *testing.T, cargo string, crate behaviorCrate, args ...string) {
	t.Helper()
	targetDir := os.Getenv("CARGO_TARGET_DIR")
	if targetDir == "" {
		targetDir = filepath.Join(t.TempDir(), "target")
	}
	cmd := exec.Command(cargo, args...)
	cmd.Dir = crate.dir
	cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+targetDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cargo %s on the generated API crate: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// TestBehaviorOperationsAPICrateBuilds lints fixture-workflow-api's crate
// with clippy, warnings denied, as a service's CI would: its behaviors.rs,
// handlers and Implementations::new against the checkout's
// superschematic-rules-adapter, with its postgres feature, and
// superschematic-rules-behaviors, by path.
func TestBehaviorOperationsAPICrateBuilds(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo build in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust API build")
	}
	crate := buildBehaviorCrate(t, false)
	cargoInBehaviorCrate(t, cargo, crate, "clippy", "--quiet", "--all-targets", "--", "-D", "warnings")
}

// TestRustSDKCallsTheBehaviorOperations serves fixture-workflow-api's
// build_router on a local socket, over a schema of its own in the Postgres
// at SUPERSCHEMATIC_RULES_TEST_DATABASE_URL that holds fixture-workflow-db's
// tables, and calls its behavior operations through the generated Rust SDK
// (behaviorOperationsRoundtripTest), as the Go and TypeScript servers' tests
// call theirs: a transition moves the field; a move the config has no
// transition for is the core's 409 veto; the route's permission is the
// API's to refuse and the config's the core's; a key no row holds is the
// core's 404; and a table's two Workflows move apart. Its authenticator
// reads Bearer <subject>:<permission>,... . It skips without the database,
// without cargo, and in -short mode.
func TestRustSDKCallsTheBehaviorOperations(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the Rust server's behavior operations against Postgres in -short mode")
	}
	if os.Getenv(rulesTestDatabaseEnv) == "" {
		t.Skip("set SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to run the generated Rust server's behavior operations against Postgres")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust server's behavior operations")
	}
	crate := buildBehaviorCrate(t, true)
	test := strings.NewReplacer(
		"API_CRATE", strings.ReplaceAll(crate.output.CrateName, "-", "_"),
		"SDK_CRATE", strings.ReplaceAll(naming.Default().RustSDKCrate(behaviorAPI), "-", "_"),
	).Replace(behaviorOperationsRoundtripTest)
	if err := os.WriteFile(filepath.Join(crate.dir, "tests", "behavior_operations.rs"), []byte(test), 0o644); err != nil {
		t.Fatal(err)
	}
	cargoInBehaviorCrate(t, cargo, crate, "clippy", "--quiet", "--all-targets", "--", "-D", "warnings")
	cargoInBehaviorCrate(t, cargo, crate, "test", "--quiet", "--test", "behavior_operations", "--", "--nocapture")
}

// rulesTestDatabaseEnv names the Postgres the end-to-end test runs on, as
// the rules adapter's tests and the Go and TypeScript servers' read it.
const rulesTestDatabaseEnv = "SUPERSCHEMATIC_RULES_TEST_DATABASE_URL"

// behaviorOperationsRoundtripTest is tests/behavior_operations.rs of
// fixture-workflow-api's API crate, beside fixture-workflow-db's DDL. Its
// cases are the ones every server's end-to-end test runs, by the same
// names, with the core's words as ormgen's TestWorkflowTablesOnPostgres
// holds them.
const behaviorOperationsRoundtripTest = `//! The behavior operations of fixture-workflow-api end to end (D103): the
//! crate's router over a schema of its own in the Postgres at
//! SUPERSCHEMATIC_RULES_TEST_DATABASE_URL, which holds fixture-workflow-db's
//! tables, called through the generated Rust SDK.

use std::sync::Arc;
use std::time::{SystemTime, UNIX_EPOCH};

use API_CRATE::runtime::postgres::{deadpool_postgres, tokio_postgres, Pool};
use API_CRATE::runtime::{bearer_token, ApiError, Authenticator, Principal, RequestContext};
use API_CRATE::{build_router, types, Handlers, Implementations, OrderGetOrderArgs, OrderImplementation};
use SDK_CRATE::{ClientConfig, FixtureWorkflowApiSdk, SDKError};
use async_trait::async_trait;
use axum::http::request::Parts;
use serde_json::{json, Value};

/// Reads Bearer <subject>:<permission>,... : that subject, holding those
/// permissions; no header, no caller.
struct Tokens;

#[async_trait]
impl Authenticator for Tokens {
    async fn authenticate(&self, request: &Parts) -> Result<Option<Principal>, ApiError> {
        Ok(bearer_token(&request.headers).map(|token| {
            let (subject, permissions) = token.split_once(':').unwrap_or((token, ""));
            Principal::new(subject, permissions.split(',').filter(|p| !p.is_empty()))
        }))
    }
}

/// getOrder, which the cases do not call: the Go and TypeScript servers'
/// tests read the owned field through it.
struct Orders;

#[async_trait]
impl OrderImplementation for Orders {
    async fn get_order(&self, _ctx: RequestContext, _args: OrderGetOrderArgs) -> Result<types::Order, ApiError> {
        Err(ApiError::not_implemented("order.get_order is not implemented"))
    }
}

/// A schema of its own holding fixture-workflow-db's tables, a pool whose
/// connections search it, and the connection that drops it.
struct Database {
    pool: Pool,
    client: tokio_postgres::Client,
    admin: tokio_postgres::Client,
    schema: String,
}

async fn connect(config: &tokio_postgres::Config) -> tokio_postgres::Client {
    let (client, connection) = config.connect(tokio_postgres::NoTls).await.unwrap();
    tokio::spawn(connection);
    client
}

async fn database() -> Database {
    let url = std::env::var("SUPERSCHEMATIC_RULES_TEST_DATABASE_URL").expect("SUPERSCHEMATIC_RULES_TEST_DATABASE_URL names the database");
    let config: tokio_postgres::Config = url.parse().unwrap();
    let admin = connect(&config).await;
    if let Err(err) = admin.batch_execute("CREATE EXTENSION IF NOT EXISTS pgcrypto SCHEMA public").await {
        // Two tests creating the extension at once: the other won.
        assert_eq!(err.code().map(|code| code.code()), Some("23505"), "create pgcrypto: {err}");
    }
    let nanos = SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_nanos();
    let schema = format!("behavior_ops_rust_{nanos}");
    admin.batch_execute(&format!("CREATE SCHEMA {schema}")).await.unwrap();
    let mut scoped = config;
    scoped.options(format!("-c search_path={schema},public"));
    let client = connect(&scoped).await;
    client.batch_execute(include_str!("behaviors_postgres.sql")).await.unwrap();
    let manager = deadpool_postgres::Manager::new(scoped, tokio_postgres::NoTls);
    let pool = Pool::builder(manager).max_size(4).build().unwrap();
    Database { pool, client, admin, schema }
}

impl Database {
    async fn drop_schema(&self) {
        self.admin.batch_execute(&format!("DROP SCHEMA {} CASCADE", self.schema)).await.unwrap();
    }

    /// Inserts a row as the core's create leaves it, and answers its key.
    async fn insert(&self, statement: &str) -> (types::IdentityUUID, String) {
        let row = self.client.query_one(statement, &[]).await.unwrap();
        let text: String = row.get(0);
        (text.parse().unwrap(), text)
    }

    /// The columns of the row of table whose key is id, as text.
    async fn row(&self, table: &str, columns: &str, id: &str) -> Vec<String> {
        let row = self
            .client
            .query_one(&format!("SELECT {columns} FROM {table} WHERE id::text = $1"), &[&id])
            .await
            .unwrap();
        (0..row.len()).map(|i| row.get::<_, String>(i)).collect()
    }
}

async fn serve(pool: Pool) -> String {
    let implementations = Implementations::new(
        Handlers {
            order: Arc::new(Orders),
            authenticator: Arc::new(Tokens),
        },
        pool,
    );
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let base_url = format!("http://{}", listener.local_addr().unwrap());
    tokio::spawn(async move { axum::serve(listener, build_router(implementations)).await.unwrap() });
    base_url
}

fn sdk(base_url: &str, token: Option<&str>) -> FixtureWorkflowApiSdk {
    FixtureWorkflowApiSdk::new(ClientConfig::with_base_url(base_url.to_string(), token.map(str::to_string), None)).unwrap()
}

/// The key's wire form, as the core names a row.
fn wire(id: &types::IdentityUUID) -> String {
    serde_json::to_value(id).unwrap().as_str().unwrap().to_string()
}

/// The status, code, message and details of a refused call.
fn refusal(err: SDKError) -> (u16, String, String, Option<Value>) {
    let details = err.problem().and_then(|problem| problem.details.clone());
    match err {
        SDKError::Api { status_code, message, code, .. } => (status_code, code.unwrap_or_default(), message, details),
        other => panic!("not a refusal of the API: {other}"),
    }
}

fn order_to(to: types::OrderStatusState) -> types::OrderStatusTransitionInput {
    types::OrderStatusTransitionInput { to }
}

/// The cases run in a task of their own, so the schema is dropped whether
/// they pass or fail.
#[tokio::test]
async fn behavior_operations_run_through_the_core() {
    let db = Arc::new(database().await);
    let outcome = tokio::spawn(cases(Arc::clone(&db))).await;
    db.drop_schema().await;
    if let Err(err) = outcome {
        match err.try_into_panic() {
            Ok(panic) => std::panic::resume_unwind(panic),
            Err(err) => panic!("the cases did not finish: {err}"),
        }
    }
}

async fn cases(db: Arc<Database>) {
    let base_url = serve(db.pool.clone()).await;
    let sam = sdk(&base_url, Some("sam:orders.write"));
    let kim = sdk(&base_url, Some("kim:orders.write,orders.ship"));
    let lee = sdk(&base_url, Some("lee"));
    let nobody = sdk(&base_url, None);
    let (order, order_text) = db.insert(r#"INSERT INTO "order" (title, status, status_entered_at) VALUES ('o', 'draft', now()) RETURNING id::text"#).await;
    let (shipment, shipment_text) = db
        .insert("INSERT INTO shipment (reference, status, status_entered_at, payment_status, payment_status_entered_at) VALUES ('S-1', 'pending', now(), 'unpaid', now()) RETURNING id::text")
        .await;
    let o = wire(&order);
    let s = wire(&shipment);

    println!("a transition moves the field and returns the result");
    let moved = sam.order.status.transition(order, order_to(types::OrderStatusState::Paid), None).await.unwrap();
    assert_eq!(serde_json::to_value(&moved).unwrap(), json!({"from": "draft", "to": "paid"}));
    assert_eq!(db.row(r#""order""#, "status, _version::text", &order_text).await, ["paid", "2"]);

    println!("a move the config has no transition for is refused with the core's status, code and message");
    let err = sam.order.status.transition(order, order_to(types::OrderStatusState::Draft), None).await.unwrap_err();
    let (status, code, message, details) = refusal(err);
    assert_eq!((status, code.as_str()), (409, "vetoed"));
    assert_eq!(message, format!("behavior Workflow vetoes transition of Order {o}: no transition leads from paid to draft; from paid it can move to shipped"));
    assert_eq!(details.as_ref().and_then(|details| details.get("vetoCode")), Some(&json!("transition_not_allowed")), "{details:?}");
    assert_eq!(db.row(r#""order""#, "status, _version::text", &order_text).await, ["paid", "2"]);

    println!("a caller without the route's permission is refused by the API's auth");
    let err = lee.order.status.transition(order, order_to(types::OrderStatusState::Shipped), None).await.unwrap_err();
    let (status, _, message, _) = refusal(err);
    assert_eq!(status, 403);
    assert!(!message.contains("may not move"), "{message}");
    let err = nobody.order.status.transition(order, order_to(types::OrderStatusState::Shipped), None).await.unwrap_err();
    assert_eq!(refusal(err).0, 401);
    assert_eq!(db.row(r#""order""#, "status, _version::text", &order_text).await, ["paid", "2"]);

    println!("a caller without the permission the config names is refused by the core, and one who holds it moves the row");
    let err = sam.order.status.transition(order, order_to(types::OrderStatusState::Shipped), None).await.unwrap_err();
    let (status, code, message, _) = refusal(err);
    assert_eq!((status, code.as_str()), (403, "forbidden"));
    assert_eq!(message, format!("sam may not move Order {o}'s status from paid to shipped: the transition needs permission orders.ship"));
    let moved = kim.order.status.transition(order, order_to(types::OrderStatusState::Shipped), None).await.unwrap();
    assert_eq!(serde_json::to_value(&moved).unwrap(), json!({"from": "paid", "to": "shipped"}));
    assert_eq!(db.row(r#""order""#, "status", &order_text).await, ["shipped"]);

    println!("a key no row holds is not found");
    let (missing, _) = db.insert("SELECT gen_random_uuid()::text").await;
    let err = sam.order.status.transition(missing, order_to(types::OrderStatusState::Paid), None).await.unwrap_err();
    let (status, code, message, _) = refusal(err);
    assert_eq!((status, code.as_str()), (404, "not_found"));
    assert_eq!(message, format!("Order {} does not exist", wire(&missing)));

    println!("a table's two Workflows move apart");
    lee.shipment
        .status
        .transition(shipment, types::ShipmentStatusTransitionInput { to: types::ShipmentStatusState::Packed }, None)
        .await
        .unwrap();
    lee.shipment
        .payment_status
        .transition(shipment, types::ShipmentPaymentStatusTransitionInput { to: types::ShipmentPaymentStatusState::Paid }, None)
        .await
        .unwrap();
    assert_eq!(db.row("shipment", "status, payment_status", &shipment_text).await, ["packed", "paid"]);
    let err = lee
        .shipment
        .payment_status
        .transition(shipment, types::ShipmentPaymentStatusTransitionInput { to: types::ShipmentPaymentStatusState::Refunded }, None)
        .await
        .unwrap_err();
    let (status, code, message, _) = refusal(err);
    assert_eq!((status, code.as_str()), (403, "forbidden"));
    assert_eq!(message, format!("lee may not move Shipment {s}'s paymentStatus from paid to refunded: the transition needs permission payments.refund"));
}
`

// TestBehaviorOperationsHaveNoScaffold: the route scaffolds and the
// implementation's scaffold hold the operations an implementation serves,
// getOrder alone: the behaviors' core runs the behavior operations, so no
// file, method or module of shipment, whose every operation is a
// behavior's, is written (D103).
func TestBehaviorOperationsHaveNoScaffold(t *testing.T) {
	_, db, reg := loadBehaviorFixture(t)
	output := generateBehaviorAPI(t, db)
	if err := output.SetDeps(behaviorDeps(behaviorLayout(t, db, reg))); err != nil {
		t.Fatal(err)
	}
	scaffolds := t.TempDir()
	if _, err := WriteScaffolds(output, scaffolds); err != nil {
		t.Fatal(err)
	}
	var files []string
	if err := filepath.WalkDir(scaffolds, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(scaffolds, path)
			files = append(files, filepath.ToSlash(rel))
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	slices.Sort(files)
	if want := []string{"README.md", "mod.rs", "order/get_order.rs", "order/implementation.rs", "order/mod.rs"}; !slices.Equal(files, want) {
		t.Errorf("route scaffolds %v, want %v", files, want)
	}
	readme, err := os.ReadFile(filepath.Join(scaffolds, "README.md"))
	if err != nil || !strings.Contains(string(readme), "An operation of a `@behaviorOperations` set has no file either") {
		t.Errorf("the scaffolds' README does not say the behavior operations have none: %v\n%s", err, readme)
	}

	dir := filepath.Join(t.TempDir(), "impl")
	paths := naming.LocalPaths{HTTPRuntimeRust: "/repo/runtime/http/rust"}
	if _, err := WriteImplementationScaffold(output, dir, "/repo/schemas/dist/api/"+behaviorAPI, paths, naming.Default()); err != nil {
		t.Fatal(err)
	}
	lib, err := os.ReadFile(filepath.Join(dir, ImplementationLib))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"impl OrderImplementation for Order", "async fn get_order("} {
		if !strings.Contains(string(lib), want) {
			t.Errorf("the implementation's scaffold does not contain %q:\n%s", want, lib)
		}
	}
	for _, absent := range []string{"Shipment", "status_transition", "OrderStatusTransitionArgs"} {
		if strings.Contains(string(lib), absent) {
			t.Errorf("the implementation's scaffold holds %s, which the behaviors' core serves:\n%s", absent, lib)
		}
	}
}
