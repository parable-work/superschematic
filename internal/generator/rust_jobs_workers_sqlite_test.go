package generator

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/rustrestgen"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// The Rust jobs and workers fixtures on a SQLite file (D116):
// deps-stock-db with the sqlite dialect listed first, so deps-rust-stock's
// Deps.db is the database's SQLite file, and its jobs and workers open it.
const rustJobsSQLiteGoldenDir = "testdata/rust-jobs-workers/golden-sqlite"

// The deployables of deps-rust-stock-stack the tests read.
const (
	rustJobsStack        = "deps-rust-stock-stack"
	rustReorderWorker    = "deps-rust-stock-reorder-stock"
	rustReconcileJob     = "deps-rust-stock-reconcile-stock"
	rustStockDatabaseURL = "DEPS_STOCK_DB_DATABASE_URL"
)

// rustJobsSQLiteFixtures loads the fixtures with deps-stock-db listing
// sqlite before postgres.
func rustJobsSQLiteFixtures(t *testing.T) (map[string]*ir.Schema, map[string]*schemaconfig.SchemaConfig) {
	t.Helper()
	schemas, configs := rustJobsFixtures(t)
	enabled := map[string]any{"enabled": true}
	db := *configs["deps-stock-db"]
	db.Outputs = map[string]any{
		"types": map[string]any{"go": enabled, "rust": enabled},
		"sql":   map[string]any{"dialects": []any{"sqlite", "postgres"}},
	}
	configs["deps-stock-db"] = &db
	return schemas, configs
}

// buildRustJobsOnSQLite builds the database, the API and the stack, as the
// build plan orders them.
func buildRustJobsOnSQLite(t *testing.T, outputRoot, repoRoot string, paths naming.LocalPaths) {
	t.Helper()
	schemas, configs := rustJobsSQLiteFixtures(t)
	for _, service := range []string{"deps-stock-db", "deps-rust-stock", rustJobsStack} {
		if err := runRustJobs(schemas, configs, service, outputRoot, repoRoot, paths); err != nil {
			t.Fatalf("run %s: %v", service, err)
		}
	}
}

// TestRustJobsAndWorkersOnSQLiteGolden: over a database whose DB service
// lists sqlite first (D116), deps-rust-stock's Deps.db is the database's
// SQLite file; its worker's entrypoint opens the file, builds the worker
// from its queue's descriptor and its method, and runs the HTTP runtime's
// loop over the worker's drain on connections of its own, a connection per
// message it handles at a time; its job's entrypoint opens the file and
// runs the job. Each main.rs is laid out as rustfmt lays it out.
// Regenerate with:
//
//	go test ./internal/generator -run TestRustJobsAndWorkersOnSQLiteGolden -update
func TestRustJobsAndWorkersOnSQLiteGolden(t *testing.T) {
	repoRoot := t.TempDir()
	outputRoot := filepath.Join(repoRoot, "schemas", "dist")
	buildRustJobsOnSQLite(t, outputRoot, repoRoot, rustGoldenPaths(repoRoot))

	files := map[string]string{
		filepath.Join("api", "deps-rust-stock", "src", "deps.rs"): filepath.Join(APIDir(outputRoot, "deps-rust-stock"), "src", "deps.rs"),
	}
	for _, deployable := range []string{rustReorderWorker, rustReconcileJob} {
		dir := servergen.ServerDir(outputRoot, rustJobsStack, deployable)
		for _, file := range []string{servergen.RustManifestFile, servergen.RustMainFile} {
			files[filepath.Join("server", deployable, filepath.FromSlash(file))] = filepath.Join(dir, filepath.FromSlash(file))
		}
	}
	rels := make([]string, 0, len(files))
	for rel := range files {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	generated := map[string]string{}
	for _, rel := range rels {
		got, err := os.ReadFile(files[rel])
		if err != nil {
			t.Fatal(err)
		}
		generated[filepath.ToSlash(rel)] = string(got)
		golden := filepath.Join(rustJobsSQLiteGoldenDir, rel)
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
	for file, wants := range map[string][]string{
		"api/deps-rust-stock/src/deps.rs": {"pub db: Arc<Database>,"},
		"server/" + rustReorderWorker + "/Cargo.toml": {
			`features = ["logging", "otel", "sqlite"]`,
		},
		"server/" + rustReorderWorker + "/src/main.rs": {
			"let deps_stock_db_pool = open_file(\n        \"deps-stock-db\",\n        \"DEPS_STOCK_DB_DATABASE\",",
			"use superschematic_http_runtime::worker::{self, DrainOptions, SqliteWorker};",
			"use superschematic_http_runtime::{block_on, Shutdown};",
			"let handle = move |message: Message| block_on(workers.reorder_stock(message));",
			"let queue = SqliteWorker::new().queue(definition, handle)?;",
			".map(|_| worker::drain::open(deps_stock_db_pool.path()))",
			"let wake = Arc::clone(deps_stock_db_pool.wake());",
			"worker::run_drain(Arc::new(queue), connections, Some(wake), options, shutdown).await?;",
		},
		"server/" + rustReconcileJob + "/src/main.rs": {
			"let deps_stock_db_pool = open_file(\n        \"deps-stock-db\",\n        \"DEPS_STOCK_DB_DATABASE\",",
			"use superschematic_http_runtime::orm::sqlite::{self, Database as SqliteFile};",
			"db: deps_stock_db_pool,",
			"jobs.reconcile_stock(shutdown)",
		},
	} {
		for _, want := range wants {
			if !strings.Contains(generated[file], want) {
				t.Errorf("%s does not contain %q:\n%s", file, want, generated[file])
			}
		}
	}
	for _, absent := range []string{"PostgresQueue", "postgres::", "run_worker"} {
		if strings.Contains(generated["server/"+rustReorderWorker+"/src/main.rs"], absent) {
			t.Errorf("the worker's main.rs holds %s, which a worker on a SQLite file has none of", absent)
		}
	}
	if rustfmt, err := exec.LookPath("rustfmt"); err == nil {
		for _, deployable := range []string{rustReorderWorker, rustReconcileJob} {
			source := filepath.Join(servergen.ServerDir(outputRoot, rustJobsStack, deployable), filepath.FromSlash(servergen.RustMainFile))
			if diff, err := exec.Command(rustfmt, "--edition", "2021", "--check", source).CombinedOutput(); err != nil {
				t.Errorf("rustfmt lays out %s otherwise: %v\n%s", source, err, diff)
			}
		}
	}
}

// TestARustWorkerOfAQueueSQLiteCannotHoldFails: a Rust worker of a
// sqlite-first database claims on its SQLite file, by the statements on
// SQLite its queue's descriptor carries, so one whose queue's message holds
// a value SQLite has no storage for is refused, naming the field (D53,
// amended; D116).
func TestARustWorkerOfAQueueSQLiteCannotHoldFails(t *testing.T) {
	repoRoot := t.TempDir()
	outputRoot := filepath.Join(repoRoot, "schemas", "dist")
	paths := rustGoldenPaths(repoRoot)
	schemas, configs := rustJobsSQLiteFixtures(t)
	db := schemas["deps-stock-db"]
	db.Scalars["Test.Photo"] = &ir.ScalarDef{Name: "Test.Photo", LanguagePrimitive: ir.LanguageString, TypeMappings: map[string]string{"sql": "BYTEA"}}
	queue := db.Types["StockLow"]
	queue.Fields = append(queue.Fields, &ir.FieldDef{Name: "photo", TypeRef: ir.TypeRef{Name: "Test.Photo"}})
	for _, service := range []string{"deps-stock-db", "deps-rust-stock"} {
		if err := runRustJobs(schemas, configs, service, outputRoot, repoRoot, paths); err != nil {
			t.Fatalf("run %s: %v", service, err)
		}
	}
	err := runRustJobs(schemas, configs, rustJobsStack, outputRoot, repoRoot, paths)
	want := "stack " + rustJobsStack + ": Rust worker " + rustReorderWorker + " claims queue StockLow of deps-stock-db, whose DB service lists sqlite first, and the SQLite queue client cannot hold its field photo"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("build = %v, want it to say %q", err, want)
	}
}

// rustStockSQLiteImplementation is deps-rust-stock's implementation over a
// SQLite file: its job ReconcileStock enqueues a StockLow message per SKU
// of RESTOCK_SKUS in a transaction of the file's writer; and its worker
// ReorderStock files a restock request for each, through the same writer,
// but for one that fails every time and one that fails once.
const rustStockSQLiteImplementation = `//! deps-rust-stock over a SQLite file, for the test of its job and worker.

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;

use async_trait::async_trait;
use schemas_deps_rust_stock_api::database_types::{self, Recount, StockLow};
use schemas_deps_rust_stock_api::runtime::orm;
use schemas_deps_rust_stock_api::runtime::queue::{enqueue_sqlite, QueueDefinition};
use schemas_deps_rust_stock_api::runtime::{ApiError, RequestContext, Shutdown};
use schemas_deps_rust_stock_api::types::StockView;
use schemas_deps_rust_stock_api::{
    BoxError, Deps, Handlers, Jobs, StockGetStockArgs, StockImplementation, Workers,
};

/// The warehouse a message's note names, which stands for its SKU.
fn note(message: &StockLow) -> String {
    message.note.clone().unwrap_or_default()
}

struct Stock {
    deps: Deps,
    flaked: AtomicBool,
}

#[async_trait]
impl StockImplementation for Stock {
    async fn get_stock(
        &self,
        _ctx: RequestContext,
        _args: StockGetStockArgs,
    ) -> Result<StockView, ApiError> {
        Err(ApiError::not_implemented(
            "stock.get_stock is not implemented",
        ))
    }
}

#[async_trait]
impl Jobs for Stock {
    async fn place_restock_orders(&self, _shutdown: Shutdown) -> Result<(), BoxError> {
        Ok(())
    }

    async fn reconcile_stock(&self, _shutdown: Shutdown) -> Result<(), BoxError> {
        let definition = QueueDefinition::parse(database_types::STOCK_LOW_QUEUE)?;
        let skus = std::env::var("RESTOCK_SKUS").unwrap_or_default();
        self.deps
            .db
            .write_async(move |connection| -> Result<(), orm::Error> {
                for sku in skus.split(',').filter(|sku| !sku.is_empty()) {
                    let message = serde_json::json!({
                        "productId": "0b7e6d2c-4f1a-4c3b-9d5e-8a6f2e1c7b90",
                        "level": 2.0,
                        "urgency": "high",
                        "warehouses": ["north"],
                        "note": sku,
                    });
                    enqueue_sqlite(connection, &definition, &message)
                        .map_err(|err| orm::Error::Database(err.to_string()))?;
                }
                Ok(())
            })
            .await?;
        Ok(())
    }
}

#[async_trait]
impl Workers for Stock {
    async fn reorder_stock(&self, message: StockLow) -> Result<(), BoxError> {
        // A method awaits as any does: its future is driven where the
        // worker's drain runs.
        tokio::time::sleep(std::time::Duration::from_millis(10)).await;
        let sku = note(&message);
        match sku.as_str() {
            "poison" => return Err("poison every time".into()),
            "flaky" if !self.flaked.swap(true, Ordering::SeqCst) => {
                return Err("flaky the first time".into())
            }
            _ => {}
        }
        self.deps
            .db
            .write_async(move |connection| -> Result<(), orm::Error> {
                connection
                    .execute(
                        "INSERT INTO restock_request (product_id, status) VALUES (?1, ?2)",
                        ["0b7e6d2c-4f1a-4c3b-9d5e-8a6f2e1c7b90", sku.as_str()],
                    )
                    .map_err(|err| orm::Error::Database(err.to_string()))?;
                Ok(())
            })
            .await?;
        Ok(())
    }

    async fn recount_stock(&self, _message: Recount) -> Result<(), BoxError> {
        Ok(())
    }
}

fn stock(deps: Deps) -> Arc<Stock> {
    Arc::new(Stock {
        deps,
        flaked: AtomicBool::new(false),
    })
}

pub async fn create(deps: Deps) -> Result<Handlers, BoxError> {
    Ok(Handlers { stock: stock(deps) })
}

pub async fn create_jobs(deps: Deps) -> Result<Arc<dyn Jobs>, BoxError> {
    Ok(stock(deps))
}

pub async fn create_workers(deps: Deps) -> Result<Arc<dyn Workers>, BoxError> {
    Ok(stock(deps))
}
`

// syncBuffer is a buffer a running process writes and the test reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestRustJobAndWorkerRunOnSQLite builds deps-rust-stock's job and worker
// in the output root's workspace over a database whose DB service lists
// sqlite first, and runs each as its process on a SQLite file the build's
// SQLite DDL creates (D116), on the variable the stack derives for the
// database, a sqlite: URL.
//
//   - The job, ReconcileStock, opens the file, enqueues a StockLow message
//     per SKU in a transaction of the file's writer, logs job done and
//     exits 0.
//   - The worker, ReorderStock, opens the file and drains its queue on two
//     connections of its own, as WORKER_CONCURRENCY says: a message its
//     method handles is done, and its restock request is written through
//     the worker's Deps; one that fails once is due again after the backoff
//     and done on its second attempt; one that fails every time is dead
//     after its one retry, with its error. A message another process
//     enqueues while it sleeps is heard at its next poll. On SIGTERM it
//     claims no more and exits 0.
//
// The test needs cargo and the sqlite3 command, which it skips without,
// and runs outside -short.
func TestRustJobAndWorkerRunOnSQLite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated Rust job and worker on SQLite in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not found, skipping the generated Rust job and worker on SQLite")
	}
	sqlite3, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("sqlite3 not found, skipping the generated Rust job and worker on SQLite")
	}
	paths := testpaths.Local(t)
	repoRoot := testpaths.TempDir(t)
	outputRoot := filepath.Join(repoRoot, "schemas", "dist")
	schemas, configs := rustJobsSQLiteFixtures(t)
	// A message of the test fails twice at most, and is due again soon.
	once := 1
	schemas["deps-stock-db"].Types["StockLow"].Queue = &ir.QueueDef{Retries: &once, Backoff: "1s", Lease: "30s"}
	build := func(services ...string) {
		t.Helper()
		for _, service := range services {
			if err := runRustJobs(schemas, configs, service, outputRoot, repoRoot, paths); err != nil {
				t.Fatalf("run %s: %v", service, err)
			}
		}
	}
	build("deps-stock-db", "deps-rust-stock", rustJobsStack)
	impl := naming.Default().RustImplementationDir(repoRoot, "deps-rust-stock")
	if err := os.WriteFile(filepath.Join(impl, rustrestgen.ImplementationLib), []byte(rustStockSQLiteImplementation), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(impl, rustrestgen.ImplementationManifest)
	cargoToml, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	const first = "async-trait = \"0.1.89\"\n"
	if !strings.Contains(string(cargoToml), first) {
		t.Fatalf("%s names no async-trait to add the implementation's dependencies after:\n%s", manifest, cargoToml)
	}
	added := strings.Replace(string(cargoToml), first, first+"serde_json = \"1.0.149\"\ntokio = { version = \"1.52.2\", features = [\"time\"] }\n", 1)
	if err := os.WriteFile(manifest, []byte(added), 0o644); err != nil {
		t.Fatal(err)
	}
	// The stack's build mirrors the implementation the entrypoints build
	// against.
	build(rustJobsStack)

	target := os.Getenv("CARGO_TARGET_DIR")
	if target == "" {
		target = filepath.Join(testpaths.TempDir(t), "target")
	}
	for _, args := range [][]string{
		{"clippy", "--workspace", "--quiet", "--", "-D", "warnings"},
		{"build", "--quiet", "-p", naming.Default().RustWorkerCrate(rustJobsStack, rustReorderWorker), "-p", naming.Default().RustJobCrate(rustJobsStack, rustReconcileJob)},
	} {
		cmd := exec.Command(cargo, args...)
		cmd.Dir = outputRoot
		cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+target)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("cargo %s in %s: %v\n%s", strings.Join(args, " "), outputRoot, err, out)
		}
	}
	bin := filepath.Join(target, "debug")

	// The file, from the SQLite DDL the build wrote.
	ddl := filepath.Join(SQLDir(outputRoot, "deps-stock-db"), SQLiteSubdir, "create.sql")
	file := filepath.Join(testpaths.TempDir(t), "stock.sqlite")
	sql := func(statement string) string {
		t.Helper()
		out, err := exec.Command(sqlite3, "-json", "-cmd", ".timeout 5000", file, statement).CombinedOutput()
		if err != nil {
			t.Fatalf("sqlite3 %s: %v\n%s", statement, err, out)
		}
		return string(out)
	}
	sql(".read " + ddl)
	type message struct {
		Note      string  `json:"note"`
		State     string  `json:"state"`
		Attempts  int     `json:"attempts"`
		LastError *string `json:"last_error"`
	}
	messages := func() map[string]message {
		t.Helper()
		out := sql("SELECT note, state, attempts, last_error FROM stock_low_queue ORDER BY note")
		var list []message
		if strings.TrimSpace(out) != "" {
			if err := json.Unmarshal([]byte(out), &list); err != nil {
				t.Fatalf("read stock_low_queue: %v\n%s", err, out)
			}
		}
		byNote := map[string]message{}
		for _, m := range list {
			byNote[m.Note] = m
		}
		return byNote
	}
	env := append(os.Environ(), rustStockDatabaseURL+"=sqlite:"+file)

	job := exec.Command(filepath.Join(bin, rustReconcileJob))
	job.Env = append(env, "RESTOCK_SKUS=tea,flaky,poison")
	output, err := job.CombinedOutput()
	if err != nil {
		t.Fatalf("the job failed: %v\n%s", err, output)
	}
	for _, want := range []string{`"msg":"job started"`, `"api":"deps-rust-stock","class":"ReconcileStock"`, `"msg":"job done"`} {
		if !strings.Contains(string(output), want) {
			t.Errorf("the job's output lacks %s:\n%s", want, output)
		}
	}
	if enqueued := messages(); len(enqueued) != 3 || enqueued["tea"].State != ir.QueueStateReady {
		t.Fatalf("the job enqueued %v, want tea, flaky and poison ready", enqueued)
	}

	worker := exec.Command(filepath.Join(bin, rustReorderWorker))
	worker.Env = append(env, "WORKER_CONCURRENCY=2")
	var logs syncBuffer
	worker.Stdout, worker.Stderr = &logs, &logs
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- worker.Wait() }()
	t.Cleanup(func() { _ = worker.Process.Kill() })
	settle := func(what string, settled func(map[string]message) bool) map[string]message {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for {
			m := messages()
			if settled(m) {
				return m
			}
			select {
			case err := <-exited:
				t.Fatalf("the worker exited: %v\n%s", err, logs.String())
			default:
			}
			if time.Now().After(deadline) {
				t.Fatalf("the worker did not settle %s: %v\n%s", what, m, logs.String())
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	settled := settle("the job's messages", func(m map[string]message) bool {
		return m["tea"].State == ir.QueueStateDone && m["flaky"].State == ir.QueueStateDone && m["poison"].State == ir.QueueStateDead
	})
	if settled["tea"].Attempts != 1 || settled["flaky"].Attempts != 2 || settled["poison"].Attempts != 2 || settled["poison"].LastError == nil || *settled["poison"].LastError != "poison every time" {
		t.Errorf("tea took %d attempts, flaky %d and poison %d with %v; want one, two and two, and poison's error kept", settled["tea"].Attempts, settled["flaky"].Attempts, settled["poison"].Attempts, settled["poison"].LastError)
	}
	if requests := sql("SELECT status FROM restock_request ORDER BY status"); !strings.Contains(requests, `"flaky"`) || !strings.Contains(requests, `"tea"`) || strings.Contains(requests, `"poison"`) {
		t.Errorf("the worker's Deps wrote the restock requests %s, want flaky's and tea's alone", requests)
	}

	// A write another process makes is heard at the worker's next poll.
	again := exec.Command(filepath.Join(bin, rustReconcileJob))
	again.Env = append(env, "RESTOCK_SKUS=late")
	if output, err := again.CombinedOutput(); err != nil {
		t.Fatalf("the job failed: %v\n%s", err, output)
	}
	settle("a message another process enqueued", func(m map[string]message) bool { return m["late"].State == ir.QueueStateDone })

	if err := worker.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		if err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				t.Fatalf("the worker exited %d on SIGTERM, want 0:\n%s", exit.ExitCode(), logs.String())
			}
			t.Fatalf("the worker on SIGTERM: %v\n%s", err, logs.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("the worker did not stop on SIGTERM:\n%s", logs.String())
	}
	for _, want := range []string{
		`"msg":"worker started"`,
		`"api":"deps-rust-stock","class":"ReorderStock","queue":"StockLow","concurrency":2`,
		`"msg":"message handled"`,
		`"msg":"message failed and will be retried"`,
		`"msg":"message failed and is dead: its retries are spent"`,
		`"msg":"worker stopped"`,
		`"worker":"` + rustReorderWorker + `"`,
	} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("the worker's output lacks %s:\n%s", want, logs.String())
		}
	}
}
