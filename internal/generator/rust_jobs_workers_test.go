package generator

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/rustrestgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// The Rust jobs and workers fixtures (D65): deps-rust-stock, a Rust API
// with a job on a schedule, a job on demand and two workers, over
// deps-stock-db, its one DB-kind dependency, whose Rust types declare the
// workers' queues; and deps-rust-stock-stack, which deploys it.
const rustJobsGoldenDir = "testdata/rust-jobs-workers/golden"

// rustJobsFixtures loads the fixtures' schemas and configs.
func rustJobsFixtures(t *testing.T) (map[string]*ir.Schema, map[string]*schemaconfig.SchemaConfig) {
	t.Helper()
	schemas := map[string]*ir.Schema{}
	configs := map[string]*schemaconfig.SchemaConfig{}
	for _, name := range []string{"deps-stock-db", "deps-rust-stock", "deps-rust-stock-stack"} {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(depsFixtures, name))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		schemas[name], configs[name] = schema, cfg
	}
	return schemas, configs
}

// runRustJobs runs the generators of service, one of the fixtures, into
// outputRoot under the repository root repoRoot, which the implementation
// is scaffolded under.
func runRustJobs(schemas map[string]*ir.Schema, configs map[string]*schemaconfig.SchemaConfig, service, outputRoot, repoRoot string, paths naming.LocalPaths) error {
	_, err := Run(schemas[service], configs[service], Options{
		OutputRoot: outputRoot,
		Paths:      paths,
		Naming:     naming.Default(),
		Clock:      codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
		LoadDependency: func(name string) (*ir.Schema, error) {
			if schema, ok := schemas[name]; ok {
				return schema, nil
			}
			return nil, fmt.Errorf("no fixture %s", name)
		},
		DependencyConfig: func(name string) (*schemaconfig.SchemaConfig, bool) {
			cfg, ok := configs[name]
			return cfg, ok
		},
		LoadDependencyConfig: func(name string) (*schemaconfig.SchemaConfig, error) {
			if cfg, ok := configs[name]; ok {
				return cfg, nil
			}
			return nil, fmt.Errorf("no fixture %s", name)
		},
		Stage:              registry.StageAll,
		ImplementationRoot: repoRoot,
		RepositoryRoot:     repoRoot,
	})
	return err
}

// buildRustJobs builds the database, then the API, as the build plan
// orders them.
func buildRustJobs(t *testing.T, outputRoot, repoRoot string, paths naming.LocalPaths) {
	t.Helper()
	schemas, configs := rustJobsFixtures(t)
	for _, service := range []string{"deps-stock-db", "deps-rust-stock"} {
		if err := runRustJobs(schemas, configs, service, outputRoot, repoRoot, paths); err != nil {
			t.Fatalf("run %s: %v", service, err)
		}
	}
}

// TestRustJobsAndWorkersGolden: deps-stock-db's Rust types hold each
// queue's descriptor in src/queues.rs; deps-rust-stock's crate declares
// Jobs, a method per job taking the runtime's Shutdown, Workers, a method
// per worker taking its queue's message from the database's Rust types,
// which Cargo.toml depends on and lib.rs re-exports as database_types, and
// BoxError; and the scaffold writes create_jobs and create_workers beside
// create, each method failing until it is implemented (D65). Regenerate
// with:
//
//	go test ./internal/generator -run TestRustJobsAndWorkersGolden -update
func TestRustJobsAndWorkersGolden(t *testing.T) {
	repoRoot := t.TempDir()
	outputRoot := filepath.Join(repoRoot, "schemas", "dist")
	buildRustJobs(t, outputRoot, repoRoot, rustGoldenPaths(repoRoot))

	names := naming.Default()
	files := map[string]string{}
	for _, file := range []string{"Cargo.toml", "src/lib.rs", "src/queues.rs"} {
		files[filepath.Join("types", "deps-stock-db", file)] = filepath.Join(TypesDir(outputRoot, LangRust, "deps-stock-db"), filepath.FromSlash(file))
	}
	for _, file := range []string{"Cargo.toml", "src/lib.rs", "src/deps.rs"} {
		files[filepath.Join("api", "deps-rust-stock", file)] = filepath.Join(APIDir(outputRoot, "deps-rust-stock"), filepath.FromSlash(file))
	}
	for _, file := range []string{rustrestgen.ImplementationManifest, rustrestgen.ImplementationLib} {
		files[filepath.Join("rust", "deps-rust-stock", file)] = filepath.Join(names.RustImplementationDir(repoRoot, "deps-rust-stock"), file)
	}
	rels := make([]string, 0, len(files))
	for rel := range files {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		got, err := os.ReadFile(files[rel])
		if err != nil {
			t.Fatal(err)
		}
		golden := filepath.Join(rustJobsGoldenDir, rel)
		if *updateNamingGolden {
			if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(golden, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatalf("%v (run with -update to write it)", err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from golden (run with -update to accept):\n--- got ---\n%s", rel, got)
		}
	}
}

// TestRustJobsAndWorkersInfos: the API's output holds what a job's and a
// worker's entrypoint run: each job's method, schedule, time zone,
// timeout and retries, and each worker's method, queue, the constant
// holding its descriptor, concurrency and grace, with the database's Rust
// types crate in its Deps.
func TestRustJobsAndWorkersInfos(t *testing.T) {
	schemas, configs := rustJobsFixtures(t)
	outputs, err := ParseOutputs(configs["deps-rust-stock"].Outputs)
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{
		OutputRoot:     t.TempDir(),
		Paths:          rustGoldenPaths(t.TempDir()),
		Naming:         naming.Default(),
		LoadDependency: func(name string) (*ir.Schema, error) { return schemas[name], nil },
		DependencyConfig: func(name string) (*schemaconfig.SchemaConfig, bool) {
			c, ok := configs[name]
			return c, ok
		},
	}
	r, _ := newRun(schemas["deps-rust-stock"], configs["deps-rust-stock"], outputs, opts, CoreRegistry(opts.Naming))
	output, err := RustAPIOf(r.GenerateContext)
	if err != nil {
		t.Fatal(err)
	}
	wantJobs := []rustrestgen.JobInfo{
		{Name: "PlaceRestockOrders", Method: "place_restock_orders", Schedule: "0 * * * *", TimeoutSeconds: 300, Retries: 2},
		{Name: "ReconcileStock", Method: "reconcile_stock", TimeoutSeconds: ir.DefaultJobTimeoutSeconds},
	}
	if fmt.Sprint(output.Jobs) != fmt.Sprint(wantJobs) {
		t.Errorf("Jobs = %+v, want %+v", output.Jobs, wantJobs)
	}
	wantWorkers := []rustrestgen.WorkerInfo{
		{Name: "RecountStock", Method: "recount_stock", Queue: "Recount", QueueConst: "RECOUNT_QUEUE", Concurrency: 1, GraceSeconds: ir.DefaultWorkerGraceSeconds},
		{Name: "ReorderStock", Method: "reorder_stock", Queue: "StockLow", QueueConst: "STOCK_LOW_QUEUE", Concurrency: 2, GraceSeconds: 5},
	}
	if fmt.Sprint(output.Workers) != fmt.Sprint(wantWorkers) {
		t.Errorf("Workers = %+v, want %+v", output.Workers, wantWorkers)
	}
	if output.Deps.Database != "deps-stock-db" || output.Deps.DatabaseTypesCrate != "schemas-deps-stock-db-types" || output.Deps.DatabaseTypesPath != "../../types/rust/deps-stock-db" {
		t.Errorf("Deps = %+v, want the database's Rust types crate", *output.Deps)
	}
}

// TestRustJobsAndWorkersRefusals: a Rust API's workers take their messages
// from its database's Rust types, so the build refuses a database that
// generates none, naming the switch to turn on; a worker of a queue the
// database does not declare; and workers of an API that connects to no
// database (D53, D65).
func TestRustJobsAndWorkersRefusals(t *testing.T) {
	run := func(schemas map[string]*ir.Schema, configs map[string]*schemaconfig.SchemaConfig) error {
		return runRustJobs(schemas, configs, "deps-rust-stock", t.TempDir(), "", rustGoldenPaths(t.TempDir()))
	}

	// The API's own Rust types alias the messages it imports from the
	// database, and so refuse a database without Rust types first: an API
	// that imports none meets the workers' refusal.
	schemas, configs := rustJobsFixtures(t)
	schemas["deps-rust-stock"].Imports = nil
	noTypes := *configs["deps-stock-db"]
	noTypes.Outputs = map[string]any{"types": map[string]any{"go": map[string]any{"enabled": true}}}
	configs["deps-stock-db"] = &noTypes
	if err := run(schemas, configs); err == nil || !strings.Contains(err.Error(), "the Rust workers of deps-rust-stock handle the queues of deps-stock-db, whose Rust types declare the messages and hold the queues' descriptors, and deps-stock-db generates none; enable outputs.types.rust in deps-stock-db's config (D65)") {
		t.Errorf("Run = %v, want the refusal of a database without Rust types", err)
	}

	schemas, configs = rustJobsFixtures(t)
	schemas["deps-rust-stock"].Workers[0].Queue = "StockHigh"
	if err := run(schemas, configs); err == nil || !strings.Contains(err.Error(), "handles StockHigh, which is no @queue class of deps-stock-db") {
		t.Errorf("Run = %v, want the refusal of a worker of an undeclared queue", err)
	}

	schemas, configs = rustJobsFixtures(t)
	schemas["deps-rust-stock"].Dependencies, schemas["deps-rust-stock"].Imports = nil, nil
	noDB := *configs["deps-rust-stock"]
	noDB.Dependencies = nil
	configs["deps-rust-stock"] = &noDB
	if err := run(schemas, configs); err == nil || !strings.Contains(err.Error(), "deps-rust-stock declares workers, and connects to no database") {
		t.Errorf("Run = %v, want the refusal of workers without a database", err)
	}
}

// rustJobsTest is tests/background.rs of deps-rust-stock's implementation
// crate: create_jobs and create_workers build from the Deps a job's and a
// worker's entrypoint build; each worker's method fails its message until
// it is implemented; and each queue's descriptor, through the API crate's
// re-export of the database's types, names its message's members as the
// message serializes them, which the queue client writes and reads columns
// by.
const rustJobsTest = `use std::sync::Arc;

use schemas_deps_rust_stock_api::config::load_env_config;
use schemas_deps_rust_stock_api::database_types::{self, Recount, StockLow};
use schemas_deps_rust_stock_api::runtime::postgres;
use schemas_deps_rust_stock_api::{Deps, Jobs, Workers};
use schemas_deps_rust_stock_implementation::{create_jobs, create_workers};
use serde_json::{json, Value};

fn deps() -> Deps {
    std::env::set_var("DEPS_STOCK_DB_DATABASE_URL", "postgres://stock@localhost:5432/stock");
    let config = load_env_config().expect("the configuration loads");
    let db = postgres::connect(&config.deps_stock_db_database).expect("the pool opens lazily");
    Deps {
        config,
        db,
        span: tracing::Span::none(),
    }
}

fn members(descriptor: &str) -> (Value, Vec<String>) {
    let descriptor: Value = serde_json::from_str(descriptor).expect("the descriptor is JSON");
    let names = descriptor["fields"]
        .as_array()
        .expect("fields")
        .iter()
        .map(|field| field["name"].as_str().expect("a name").to_string())
        .collect();
    (descriptor, names)
}

fn keys(message: Value) -> Vec<String> {
    let mut keys: Vec<String> = message.as_object().expect("an object").keys().cloned().collect();
    keys.sort();
    keys
}

#[tokio::test]
async fn the_jobs_and_workers_build_from_deps() {
    let _jobs: Arc<dyn Jobs> = create_jobs(deps()).await.expect("create_jobs builds the jobs");
    let workers: Arc<dyn Workers> = create_workers(deps()).await.expect("create_workers builds the workers");
    let message: StockLow = serde_json::from_value(json!({
        "productId": "0b7e6d2c-4f1a-4c3b-9d5e-8a6f2e1c7b90",
        "level": 2.0,
        "urgency": "high",
        "warehouses": ["north"],
        "note": "running low",
    }))
    .expect("a message");
    let refusal = workers.reorder_stock(message).await.expect_err("not implemented");
    assert_eq!(refusal.to_string(), "worker ReorderStock is not implemented");
    let message = Recount { sku: "s-1".to_string() };
    let refusal = workers.recount_stock(message).await.expect_err("not implemented");
    assert_eq!(refusal.to_string(), "worker RecountStock is not implemented");
}

#[test]
fn each_descriptor_names_its_messages_members() {
    let (descriptor, mut names) = members(database_types::STOCK_LOW_QUEUE);
    assert_eq!(descriptor["name"], "StockLow");
    assert_eq!(descriptor["table"], "stock_low_queue");
    assert_eq!(descriptor["retries"], 3);
    assert_eq!(descriptor["backoffSeconds"], 60);
    assert_eq!(descriptor["leaseSeconds"], 120);
    assert!(descriptor["statements"]["claim"].as_str().unwrap().contains("FOR UPDATE SKIP LOCKED"));
    let message: StockLow = serde_json::from_value(json!({
        "productId": "0b7e6d2c-4f1a-4c3b-9d5e-8a6f2e1c7b90",
        "level": 2.0,
        "urgency": "high",
        "warehouses": ["north"],
        "note": "running low",
    }))
    .expect("a message");
    names.sort();
    assert_eq!(names, keys(serde_json::to_value(message).unwrap()));

    let (descriptor, names) = members(database_types::RECOUNT_QUEUE);
    assert_eq!(descriptor["retries"], 5);
    assert_eq!(names, keys(serde_json::to_value(Recount { sku: "s-1".to_string() }).unwrap()));
}
`

// TestRustJobsAndWorkersCompile: the database's Rust types, the API crate
// and the scaffolded implementation pass cargo clippy with warnings
// denied, and the implementation's create_jobs and create_workers build
// from Deps, its workers fail each message until implemented, and each
// queue's descriptor names its message's members (rustJobsTest). The API
// crate and the scaffold of an API with jobs alone, and of one with
// workers alone, pass clippy too. Until
// the HTTP runtime declares Shutdown, which a job's method takes, and
// BoxError, the test builds against a copy of the runtime with stubs.
func TestRustJobsAndWorkersCompile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo build in -short mode")
	}
	cargoPath, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust build")
	}
	paths := testpaths.Local(t)
	paths.HTTPRuntimeRust = runtimeWithShutdown(t, paths.HTTPRuntimeRust)
	repoRoot := testpaths.TempDir(t)
	outputRoot := filepath.Join(repoRoot, "schemas", "dist")
	buildRustJobs(t, outputRoot, repoRoot, paths)

	dir := naming.Default().RustImplementationDir(repoRoot, "deps-rust-stock")
	manifest := filepath.Join(dir, rustrestgen.ImplementationManifest)
	cargoToml, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	dev := `
[dev-dependencies]
serde_json = "1.0.149"
tokio = { version = "1.52.2", features = ["macros", "rt-multi-thread"] }
tracing = { version = "` + rustrestgen.TracingVersion + `", default-features = false, features = ["std"] }
`
	if err := os.WriteFile(manifest, append(cargoToml, dev...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tests", "background.rs"), []byte(rustJobsTest), 0o644); err != nil {
		t.Fatal(err)
	}

	targetDir := os.Getenv("CARGO_TARGET_DIR")
	if targetDir == "" {
		targetDir = filepath.Join(t.TempDir(), "target")
	}
	cargo := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command(cargoPath, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+targetDir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("cargo %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
		}
	}
	clippy := []string{"clippy", "--quiet", "--all-targets", "--", "-D", "warnings"}
	cargo(TypesDir(outputRoot, LangRust, "deps-stock-db"), clippy...)
	cargo(APIDir(outputRoot, "deps-rust-stock"), clippy...)
	cargo(dir, clippy...)
	cargo(dir, "test", "--quiet")

	// An API with jobs alone, and one with workers alone, each name only
	// what they use.
	for _, variant := range []string{"jobs", "workers"} {
		schemas, configs := rustJobsFixtures(t)
		if variant == "jobs" {
			schemas["deps-rust-stock"].Workers = nil
		} else {
			schemas["deps-rust-stock"].Jobs = nil
		}
		repoRoot := testpaths.TempDir(t)
		outputRoot := filepath.Join(repoRoot, "schemas", "dist")
		for _, service := range []string{"deps-stock-db", "deps-rust-stock"} {
			if err := runRustJobs(schemas, configs, service, outputRoot, repoRoot, paths); err != nil {
				t.Fatalf("run %s with %s alone: %v", service, variant, err)
			}
		}
		cargo(APIDir(outputRoot, "deps-rust-stock"), clippy...)
		cargo(naming.Default().RustImplementationDir(repoRoot, "deps-rust-stock"), clippy...)
	}
}

// runtimeWithShutdown is the HTTP runtime crate at dir when it declares
// Shutdown and BoxError, and otherwise a copy of it that depends on the
// schema runtime at the path the types crates name, with a stub of each it
// lacks: the cancellation token a job's method takes and the error a job's
// or a worker's returns (D65), which the runtime adds.
func runtimeWithShutdown(t *testing.T, dir string) string {
	t.Helper()
	lib, err := os.ReadFile(filepath.Join(dir, "src", "lib.rs"))
	if err != nil {
		t.Fatal(err)
	}
	var stub string
	if !strings.Contains(string(lib), "Shutdown") {
		stub += "\n/// A stub of the runtime's cancellation token (D65), until it declares one.\n#[derive(Clone, Debug, Default)]\npub struct Shutdown;\n"
	}
	if !strings.Contains(string(lib), "BoxError") {
		stub += "\n/// A stub of the runtime's error of a job or a worker (D65), until it declares one.\npub type BoxError = Box<dyn std::error::Error + Send + Sync>;\n"
	}
	if stub == "" {
		return dir
	}
	root := testpaths.TempDir(t)
	copied := filepath.Join(root, "http", "rust")
	if err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		switch {
		case entry.IsDir() && entry.Name() == "target":
			return filepath.SkipDir
		case entry.IsDir():
			return os.MkdirAll(filepath.Join(copied, rel), 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(copied, rel), data, 0o644)
	}); err != nil {
		t.Fatal(err)
	}
	// The runtime names the schema runtime at ../../schema/rust, which the
	// copy names by its path, the one the types crates name.
	manifest := filepath.Join(copied, "Cargo.toml")
	cargoToml, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	schemaRuntime := `path = "` + filepath.ToSlash(filepath.Join(dir, "..", "..", "schema", "rust")) + `"`
	if !strings.Contains(string(cargoToml), `path = "../../schema/rust"`) {
		t.Fatalf("%s names the schema runtime at no path the copy knows:\n%s", manifest, cargoToml)
	}
	if err := os.WriteFile(manifest, []byte(strings.Replace(string(cargoToml), `path = "../../schema/rust"`, schemaRuntime, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(copied, "src", "lib.rs"), append(lib, stub...), 0o644); err != nil {
		t.Fatal(err)
	}
	return copied
}

// TestARustImplementationThatPredatesItsJobsFails: deps-rust-stock declares
// jobs and workers, and its implementation, which exists and so is the
// engineer's, declares neither create_jobs nor create_workers. The stack's
// build writes nothing into it and fails, saying what to add; with
// create_jobs re-exported under its name, it fails for create_workers, each
// message's queue named; with both, a glob re-export standing for one, it
// builds (D65).
func TestARustImplementationThatPredatesItsJobsFails(t *testing.T) {
	repoRoot := t.TempDir()
	outputRoot := filepath.Join(repoRoot, "schemas", "dist")
	paths := rustGoldenPaths(repoRoot)
	buildRustJobs(t, outputRoot, repoRoot, paths)
	schemas, configs := rustJobsFixtures(t)
	dir := naming.Default().RustImplementationDir(repoRoot, "deps-rust-stock")
	lib := filepath.Join(dir, rustrestgen.ImplementationLib)
	build := func(code string) error {
		t.Helper()
		if err := os.WriteFile(lib, []byte(code), 0o644); err != nil {
			t.Fatal(err)
		}
		err := runRustJobs(schemas, configs, "deps-rust-stock-stack", outputRoot, repoRoot, paths)
		if got, rerr := os.ReadFile(lib); rerr != nil || string(got) != code {
			t.Errorf("a build wrote %s: %q, %v", lib, got, rerr)
		}
		return err
	}
	before := "//! The engineer's code, from before the jobs.\n\n/// pub async fn create_jobs, one day.\npub async fn create() {}\n"
	err := build(before)
	for _, want := range []string{
		"stack deps-rust-stock-stack: deps-rust-stock declares jobs, and its implementation at " + dir + ", which the build no longer writes into, declares no create_jobs in src/lib.rs; add",
		"  pub async fn create_jobs(deps: Deps) -> Result<Arc<dyn Jobs>, BoxError>",
		"  impl Jobs for ... {\n      async fn place_restock_orders(&self, shutdown: Shutdown) -> Result<(), BoxError> {\n          // ...\n      }\n      async fn reconcile_stock(",
		"where Deps, Jobs and BoxError are schemas_deps_rust_stock_api's, and Shutdown the HTTP runtime's (docs/stack-model.md, section 8.7)",
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("build = %v, want it to say %q", err, want)
		}
	}
	err = build(before + "\nmod jobs;\npub use jobs::{build as create_jobs, Stock};\n")
	for _, want := range []string{
		"stack deps-rust-stock-stack: deps-rust-stock declares workers, and its implementation at " + dir + ", which the build no longer writes into, declares no create_workers in src/lib.rs; add",
		"  pub async fn create_workers(deps: Deps) -> Result<Arc<dyn Workers>, BoxError>",
		"      async fn reorder_stock(&self, message: database_types::StockLow) -> Result<(), BoxError> {\n          // handle a message of StockLow\n      }",
		"where Deps, Workers, BoxError and database_types, the Rust types of the database that declares each queue, are schemas_deps_rust_stock_api's (docs/stack-model.md, section 8.8)",
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("build = %v, want it to say %q", err, want)
		}
	}
	if err := build(before + "pub async fn create_jobs() {}\npub use workers::*;\n"); err != nil {
		t.Fatalf("a build over an implementation that declares both = %v", err)
	}
}

// TestARustWorkerOfAQueueTheClientCannotHoldFails: a Rust worker whose
// queue's message holds a value the Rust queue client cannot write and read
// back yet, a Temporal.Duration, which Postgres holds as an INTERVAL, is
// refused, naming the field; the database's Rust types leave the queue out
// of src/queues.rs, and the other worker's queue in (D65).
func TestARustWorkerOfAQueueTheClientCannotHoldFails(t *testing.T) {
	repoRoot := t.TempDir()
	outputRoot := filepath.Join(repoRoot, "schemas", "dist")
	paths := rustGoldenPaths(repoRoot)
	schemas, configs := rustJobsFixtures(t)
	db := schemas["deps-stock-db"]
	db.Scalars["Temporal.Duration"] = &ir.ScalarDef{Name: "Temporal.Duration", LanguagePrimitive: ir.LanguageString, TypeMappings: map[string]string{"sql": "INTERVAL"}}
	queue := db.Types["StockLow"]
	queue.Fields = append(queue.Fields, &ir.FieldDef{Name: "lowFor", TypeRef: ir.TypeRef{Name: "Temporal.Duration"}, Required: true})
	for _, service := range []string{"deps-stock-db", "deps-rust-stock"} {
		if err := runRustJobs(schemas, configs, service, outputRoot, repoRoot, paths); err != nil {
			t.Fatalf("run %s: %v", service, err)
		}
	}
	queues, err := os.ReadFile(filepath.Join(TypesDir(outputRoot, LangRust, "deps-stock-db"), "src", "queues.rs"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(queues), "//! Queue StockLow is left out: field lowFor is a Temporal.Duration, an INTERVAL column") || strings.Contains(string(queues), "STOCK_LOW_QUEUE") || !strings.Contains(string(queues), "RECOUNT_QUEUE") {
		t.Errorf("queues.rs should leave StockLow out and write Recount:\n%s", queues)
	}
	err = runRustJobs(schemas, configs, "deps-rust-stock-stack", outputRoot, repoRoot, paths)
	want := "stack deps-rust-stock-stack: Rust worker deps-rust-stock-reorder-stock claims queue StockLow of deps-stock-db, which the Rust queue client cannot hold yet: its field lowFor is a Temporal.Duration, an INTERVAL column, which the Rust queue client does not read yet; hold the value in a @jsonField type, or write the worker in Go (D65)"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("build = %v, want it to say %q", err, want)
	}
}
