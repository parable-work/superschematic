package ormgen

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/rustgen"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
	"github.com/parable-work/superschematic/internal/generator/typegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// queueParitySubtests are the subtests of the generated TestQueueParity,
// each of which must pass.
var queueParitySubtests = []string{
	"GoAndTypeScript",
	"RustReadsGoAndTypeScript",
	"GoAndTypeScriptReadRust",
	"EachChangesTheOthersClaims",
	"AttemptsAndStatesAgree",
	"BackoffGrowsForEverySide",
	"EachPrunesTheOthersDoneMessages",
	"LocationsAndDurationsAgree",
}

// TestQueueParityOfGoTypeScriptAndRustOnPostgres runs the Go ORM's queue,
// the TypeScript queue client and the Rust queue client against one
// Postgres, on the queue OrderPlaced of fixture-queue-db, whose message
// holds a field of every kind a column takes (D53, D65), and on
// DeliveryPlanned of fixture-queue-geo-db, whose message holds locations,
// POINT columns, and durations, INTERVAL ones (D53, amended). All three run the
// statements sqlutil.PostgresQueueStatements writes, so each sees the
// others' states, attempts and tokens. The Go ORM runs in the generated
// test; the TypeScript client and the Rust client run a step per process,
// the TypeScript one through parity.ts and the Rust one through a binary
// over the DB's generated Rust types (queueParityRust), and print what
// they claimed as their types serialize it.
//
// The generated TestQueueParity holds a subtest per case, each in a schema
// of its own:
//   - GoAndTypeScript: Go and TypeScript claim, read, fail, release and
//     complete each other's messages, until one is dead;
//   - RustReadsGoAndTypeScript: Rust claims and reads, field for field, what
//     Go and TypeScript enqueue, an unset enum as its default;
//   - GoAndTypeScriptReadRust: Go and TypeScript claim and read what Rust
//     enqueues, its own wire form among it;
//   - EachChangesTheOthersClaims: for each pair of sides, a message the
//     third enqueues is claimed by one and extended, released, failed and
//     completed by the other, and once complete each side refuses the token
//     as a lost claim;
//   - AttemptsAndStatesAgree: one message's attempts pass through every
//     side until it is dead, and a claim one side lets expire another
//     claims again;
//   - BackoffGrowsForEverySide: each side's failure of another's claim
//     makes the message due after the backoff doubled for each attempt
//     before, capped at the maximum backoff, and no side claims it sooner
//     (D68);
//   - EachPrunesTheOthersDoneMessages: each side's prune deletes, a batch
//     at a time, the messages the other two completed past the retention,
//     and leaves one done within it and a dead one;
//   - LocationsAndDurationsAgree: on DeliveryPlanned, each side reads the
//     locations and durations each enqueues, a duration truncated to its
//     microseconds, and an interval written with days and months as their
//     hours.
//
// It needs Bun and cargo, builds the HTTP runtimes, and runs on the
// database at SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL, skipping without
// it. Without Bun or cargo it skips, unless SUPERSCHEMATIC_REQUIRE_TS_CHECKS
// says the gate must run.
func TestQueueParityOfGoTypeScriptAndRustOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the Go, TypeScript and Rust queues against one Postgres")
	}
	runQueueParity(t, "postgres")
}

// TestQueueParityOfGoTypeScriptAndRustOnSQLite runs the same cases on
// SQLite (D53, amended), each in a file of its own, which the Go ORM's
// queues_sqlite.go writes through modernc.org/sqlite, the TypeScript
// client through bun:sqlite and the Rust client through rusqlite, each
// process with a connection of its own: all three run the statements
// sqlutil.SQLiteQueueStatements writes. LocationsAndDurationsAgree skips,
// since SQLite has no storage for a location. It needs no database, and
// skips as the Postgres test does without Bun or cargo.
func TestQueueParityOfGoTypeScriptAndRustOnSQLite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	runQueueParity(t, "sqlite")
}

// runQueueParity generates the fixture's Go ORM, TypeScript types and Rust
// types, each with its queues' statements on SQLite too, and runs the
// generated TestQueueParity on dialect, postgres or sqlite.
func runQueueParity(t *testing.T, dialect string) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun not available: %v", err))
	}
	// The gate is the TypeScript one too, which a skip for want of cargo
	// would hide.
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("cargo not available for the Rust side: %v", err))
	}
	paths := testpaths.Local(t)
	// The HTTP runtime's dist/, which the workspace's root overrides the
	// package with, built as the TypeScript generators' tests build it.
	testpaths.BuildHTTPRuntime(t, bun, paths)
	schema := queueSchema(t)
	clock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// The Go types and ORM modules.
	typesModule := "example.com/schemas/types/go/" + queueFixture
	typesDir := filepath.Join(root, "types", "go", queueFixture)
	ormDir := filepath.Join(root, "orm", queueFixture)
	typesOutput, err := typegen.Generate(schema, typegen.Options{SchemaName: queueFixture, ModulePath: typesModule, Clock: clock})
	if err != nil {
		t.Fatalf("generate types: %v", err)
	}
	if err := typegen.SetReplacePaths(typesOutput, paths, typesDir); err != nil {
		t.Fatal(err)
	}
	if err := typegen.WriteTypes(typesOutput, typesDir); err != nil {
		t.Fatal(err)
	}
	ormOutput, err := Generate(schema, Options{SchemaName: queueFixture, ModulePath: "example.com/schemas/orm/" + queueFixture, TypesModule: typesModule, SQLite: true, Clock: clock})
	if err != nil {
		t.Fatalf("generate orm: %v", err)
	}
	if err := SetReplacePaths(ormOutput, paths, ormDir); err != nil {
		t.Fatal(err)
	}
	if err := WriteORM(ormOutput, ormDir); err != nil {
		t.Fatal(err)
	}

	// The TypeScript types package, in a Bun workspace with a member that
	// runs the queue client over its queues.ts.
	tsTypesDir := filepath.Join(root, "types", "typescript", queueFixture)
	tsOutput, err := tsgen.Generate(schema, tsgen.Options{SchemaName: queueFixture, SQLite: true, Clock: clock})
	if err != nil {
		t.Fatalf("generate typescript types: %v", err)
	}
	if err := tsgen.SetScalarLibSpec(tsOutput, paths, tsTypesDir); err != nil {
		t.Fatal(err)
	}
	if err := tsgen.WriteTypes(tsOutput, tsTypesDir); err != nil {
		t.Fatal(err)
	}
	if err := (tsgen.WorkspaceRoot{OutputRoot: root, Paths: paths}).Write(); err != nil {
		t.Fatal(err)
	}
	scriptDir := filepath.Join(root, "server", "parity", "queue")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := fmt.Sprintf(`{
  "name": "queue-parity",
  "private": true,
  "type": "module",
  "dependencies": {
    %q: "workspace:*",
    %q: "*",
    "pg": "8.23.0"
  }
}
`, naming.Default().NpmTypesPackage(queueFixture), naming.Default().HTTPRuntimeNpmPackage)
	script := strings.ReplaceAll(queueParityScript, "@TYPES@", naming.Default().NpmTypesPackage(queueFixture))
	for name, content := range map[string]string{"package.json": manifest, "parity.ts": script} {
		if err := os.WriteFile(filepath.Join(scriptDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	install := exec.Command(bun, "install")
	install.Dir = root
	if out, err := install.CombinedOutput(); err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun install failed (likely offline): %v\n%s", err, out))
	}

	rust := buildQueueParityRust(t, cargo, paths, schema, clock, root)

	ddl, err := sqlgen.Generate(schema, sqlgen.Options{SchemaName: queueFixture})
	if err != nil {
		t.Fatalf("generate ddl: %v", err)
	}
	ddlDir := t.TempDir()
	if err := sqlgen.WriteDDL(ddl, ddlDir); err != nil {
		t.Fatal(err)
	}
	// The SQLite model's DDL of fixture-queue-db alone: SQLite has no
	// storage for fixture-queue-geo-db's locations.
	sqliteSchema, err := loader.LoadService(filepath.Join(fixturesDir, queueFixture))
	if err != nil {
		t.Fatal(err)
	}
	model, err := sqlmigrate.BuildModel(sqliteSchema, sqlgen.Options{SchemaName: queueFixture}, sqlmigrate.SQLite)
	if err != nil {
		t.Fatalf("the SQLite model: %v", err)
	}
	sqliteDDL, err := sqlmigrate.CreateSQL(model)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(ormDir, "testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ormDir, "testdata", "create_sqlite.sql"), []byte(sqliteDDL), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("QUEUE_PARITY_DIALECT", dialect)
	t.Setenv("QUEUE_PARITY_BUN", bun)
	t.Setenv("QUEUE_PARITY_SCRIPT", filepath.Join(scriptDir, "parity.ts"))
	t.Setenv("QUEUE_PARITY_RUST", rust)
	out := runGeneratedORMModule(t, ormDir, filepath.Join(ddlDir, "create.sql"), "parity_test.go", queueParityORMTest)
	if !strings.Contains(out, "--- PASS: TestQueueParity ") {
		t.Fatal("the generated parity test did not pass")
	}
	for _, subtest := range queueParitySubtests {
		if dialect == "sqlite" && subtest == "LocationsAndDurationsAgree" {
			continue
		}
		if !strings.Contains(out, "--- PASS: TestQueueParity/"+subtest+" ") {
			t.Errorf("the generated parity test's %s did not pass", subtest)
		}
	}
}

// buildQueueParityRust generates fixture-queue-db's Rust types crate under
// root, beside it a binary crate that runs a step of the Rust queue client
// over the crate's ORDER_PLACED_QUEUE (queueParityRust), and builds it
// with cargo, returning the binary's path. The binary crate's lockfile
// starts as the HTTP runtime's, which the repository commits, so cargo
// keeps the versions it pins wherever the types crate's dependencies
// allow, and resolves only what the types crate adds. It builds without
// --locked: the parity crate's graph has no lockfile of its own, and one
// committed here would go stale with each change to the runtime's or the
// scalar crate's dependencies. Nor --offline: CI's Go job caches no
// crates, so the build may fetch what the cargo cache lacks, which
// CARGO_NET_OFFLINE=true forbids. The target directory is CARGO_TARGET_DIR
// when set, which CI shares among the tests that run cargo, and otherwise
// one in root.
func buildQueueParityRust(t *testing.T, cargo string, paths naming.LocalPaths, schema *ir.Schema, clock codegen.Clock, root string) string {
	t.Helper()
	typesDir := filepath.Join(root, "types", "rust", queueFixture)
	output, err := rustgen.Generate(schema, rustgen.Options{SchemaName: queueFixture, SQLite: true, Clock: clock})
	if err != nil {
		t.Fatalf("generate rust types: %v", err)
	}
	if err := rustgen.SetLocalPaths(output, paths, typesDir); err != nil {
		t.Fatal(err)
	}
	if err := rustgen.WriteTypes(output, typesDir); err != nil {
		t.Fatal(err)
	}
	crateDir := filepath.Join(root, "server", "parity", "queue-rust")
	if err := os.MkdirAll(filepath.Join(crateDir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	lock, err := os.ReadFile(filepath.Join(paths.HTTPRuntimeRust, "Cargo.lock"))
	if err != nil {
		t.Fatalf("the HTTP runtime's lockfile: %v", err)
	}
	manifest := fmt.Sprintf(queueParityRustManifest, filepath.ToSlash(paths.HTTPRuntimeRust), output.CrateName, filepath.ToSlash(typesDir))
	for name, content := range map[string]string{
		"Cargo.toml":  manifest,
		"Cargo.lock":  string(lock),
		"src/main.rs": queueParityRust,
	} {
		if err := os.WriteFile(filepath.Join(crateDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	target := os.Getenv("CARGO_TARGET_DIR")
	if target == "" {
		target = filepath.Join(root, "target")
	}
	build := exec.Command(cargo, "build", "--quiet")
	build.Dir = crateDir
	build.Env = append(os.Environ(), "CARGO_TARGET_DIR="+target)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("cargo build in %s: %v\n%s", crateDir, err, out)
	}
	// A copy of the test's own, which a build of another run into a shared
	// target directory cannot replace while the test runs it.
	built, err := os.ReadFile(filepath.Join(target, "debug", queueParityRustBinary))
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin", queueParityRustBinary)
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, built, 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// queueParityRustBinary is the Rust side's binary, named after its crate.
const queueParityRustBinary = "ormgen-queue-parity"

// queueParityRustManifest is the Rust side's Cargo.toml, given the HTTP
// runtime's directory, the types crate's name and its directory.
const queueParityRustManifest = `# The Rust side of ormgen's queue parity test (D65): a step of the Rust
# queue client per run, over the DB's generated Rust types.
[package]
name = "` + queueParityRustBinary + `"
version = "0.0.0"
edition = "2021"
publish = false

[dependencies]
rusqlite = { version = "0.40.2", features = ["bundled"] }
serde = "1.0.228"
serde_json = "1.0.149"
superschematic-http-runtime = { path = %q, features = ["postgres", "sqlite"] }
tokio = { version = "1.52.2", features = ["macros", "rt"] }
types = { package = %q, path = %q }

# Its own workspace, so no manifest above the test's directory claims it.
[workspace]
`

// queueParityRust is the Rust side's src/main.rs: one step of the Rust
// queue client on the database its second argument names, which prints
// its result as JSON, as queueParityScript does for TypeScript.
const queueParityRust = `//! One step of the Rust queue client (D65) on the queue ` + "`OrderPlaced`" + `, or
//! ` + "`DeliveryPlanned`" + ` when ` + "`QUEUE_PARITY_QUEUE`" + ` names it, for ormgen's queue
//! parity test, as its TypeScript
//! script runs one of the TypeScript client: the step, the database's URL,
//! then the step's arguments. It prints the step's result as JSON, a
//! claim's message as the Rust types serialize it, and on a failure its
//! error, a lost claim's as ` + "`ClaimLost: ...`" + `, exiting 1.

use std::process::ExitCode;
use std::time::Duration;

use serde::de::DeserializeOwned;
use serde::Serialize;
use serde_json::{json, Value};
use superschematic_http_runtime::postgres::connect;
use superschematic_http_runtime::queue::{
    enqueue, enqueue_sqlite, PostgresQueue, QueueDefinition, QueueError, SqliteQueue,
};
use superschematic_http_runtime::stackconfig::Database;
use types::{DeliveryPlanned, OrderPlaced, DELIVERY_PLANNED_QUEUE, ORDER_PLACED_QUEUE};

/// The lease of a claim, the TypeScript script's.
const LEASE: Duration = Duration::from_secs(60);

#[tokio::main(flavor = "current_thread")]
async fn main() -> ExitCode {
    let args: Vec<String> = std::env::args().skip(1).collect();
    match run(&args).await {
        Ok(result) => {
            println!("{result}");
            ExitCode::SUCCESS
        }
        Err(message) => {
            eprintln!("{message}");
            ExitCode::FAILURE
        }
    }
}

/// A queue's error as the test reads it, a lost claim's apart.
fn refused(err: QueueError) -> String {
    if err.is_claim_lost() {
        format!("ClaimLost: {err}")
    } else {
        format!("QueueError: {err}")
    }
}

/// Argument i of args.
fn arg(args: &[String], i: usize) -> Result<&str, String> {
    args.get(i)
        .map(String::as_str)
        .ok_or_else(|| format!("argument {i} is missing"))
}

/// The step on the queue QUEUE_PARITY_QUEUE names, OrderPlaced when unset.
async fn run(args: &[String]) -> Result<Value, String> {
    match std::env::var("QUEUE_PARITY_QUEUE").as_deref() {
        Ok("DeliveryPlanned") => run_on::<DeliveryPlanned>(DELIVERY_PLANNED_QUEUE, args).await,
        _ => run_on::<OrderPlaced>(ORDER_PLACED_QUEUE, args).await,
    }
}

async fn run_on<M: Serialize + DeserializeOwned + Send + 'static>(descriptor: &str, args: &[String]) -> Result<Value, String> {
    let step = arg(args, 0)?;
    let url = arg(args, 1)?;
    let rest = &args[2..];
    let definition = QueueDefinition::parse(descriptor).map_err(refused)?;
    if let Some(path) = url.strip_prefix("sqlite:") {
        return run_on_sqlite::<M>(definition, path, step, rest).await;
    }
    let pool = connect(&Database::Url(url.to_string())).map_err(|err| err.to_string())?;
    let queue = PostgresQueue::<M>::new(pool.clone(), definition.clone());
    let result = match step {
        "enqueue" => {
            let message: M = serde_json::from_str(arg(rest, 0)?)
                .map_err(|err| format!("the message: {err}"))?;
            let mut client = pool.get().await.map_err(|err| err.to_string())?;
            let tx = client.transaction().await.map_err(|err| err.to_string())?;
            enqueue(&tx, &definition, &message).await.map_err(refused)?;
            tx.commit().await.map_err(|err| err.to_string())?;
            Value::Null
        }
        "claim" => {
            let n: usize = arg(rest, 0)?.parse().map_err(|err| format!("n: {err}"))?;
            let mut claims = Vec::new();
            for claim in queue.claim(n, LEASE).await.map_err(refused)? {
                let message = serde_json::to_value(&claim.message).map_err(|err| err.to_string())?;
                claims.push(json!({
                    "id": claim.id,
                    "token": claim.token,
                    "attempt": claim.attempt,
                    "message": message,
                }));
            }
            Value::Array(claims)
        }
        "complete" => {
            queue.complete(arg(rest, 0)?, arg(rest, 1)?).await.map_err(refused)?;
            Value::Null
        }
        "fail" => Value::Bool(
            queue
                .fail(arg(rest, 0)?, arg(rest, 1)?, arg(rest, 2)?)
                .await
                .map_err(refused)?,
        ),
        "extend" => {
            let seconds: u64 = arg(rest, 2)?.parse().map_err(|err| format!("seconds: {err}"))?;
            queue
                .extend(arg(rest, 0)?, arg(rest, 1)?, Duration::from_secs(seconds))
                .await
                .map_err(refused)?;
            Value::Null
        }
        "release" => {
            queue.release(arg(rest, 0)?, arg(rest, 1)?).await.map_err(refused)?;
            Value::Null
        }
        "prune" => {
            let limit: usize = arg(rest, 0)?.parse().map_err(|err| format!("limit: {err}"))?;
            Value::from(queue.prune(limit).await.map_err(refused)?)
        }
        other => return Err(format!("no step {other}")),
    };
    pool.close();
    Ok(result)
}

/// The step on the queue on the SQLite file at path, enqueueing in a
/// transaction of its own.
async fn run_on_sqlite<M: Serialize + DeserializeOwned + Send + 'static>(
    definition: QueueDefinition,
    path: &str,
    step: &str,
    rest: &[String],
) -> Result<Value, String> {
    if step == "enqueue" {
        let message: M = serde_json::from_str(arg(rest, 0)?)
            .map_err(|err| format!("the message: {err}"))?;
        let mut connection = rusqlite::Connection::open(path).map_err(|err| err.to_string())?;
        connection
            .busy_timeout(Duration::from_secs(5))
            .map_err(|err| err.to_string())?;
        let tx = connection.transaction().map_err(|err| err.to_string())?;
        enqueue_sqlite(&tx, &definition, &message).map_err(refused)?;
        tx.commit().map_err(|err| err.to_string())?;
        return Ok(Value::Null);
    }
    let queue = SqliteQueue::<M>::open(path, definition).map_err(refused)?;
    Ok(match step {
        "claim" => {
            let n: usize = arg(rest, 0)?.parse().map_err(|err| format!("n: {err}"))?;
            let mut claims = Vec::new();
            for claim in queue.claim(n, LEASE).await.map_err(refused)? {
                let message = serde_json::to_value(&claim.message).map_err(|err| err.to_string())?;
                claims.push(json!({
                    "id": claim.id,
                    "token": claim.token,
                    "attempt": claim.attempt,
                    "message": message,
                }));
            }
            Value::Array(claims)
        }
        "complete" => {
            queue.complete(arg(rest, 0)?, arg(rest, 1)?).await.map_err(refused)?;
            Value::Null
        }
        "fail" => Value::Bool(
            queue
                .fail(arg(rest, 0)?, arg(rest, 1)?, arg(rest, 2)?)
                .await
                .map_err(refused)?,
        ),
        "extend" => {
            let seconds: u64 = arg(rest, 2)?.parse().map_err(|err| format!("seconds: {err}"))?;
            queue
                .extend(arg(rest, 0)?, arg(rest, 1)?, Duration::from_secs(seconds))
                .await
                .map_err(refused)?;
            Value::Null
        }
        "release" => {
            queue.release(arg(rest, 0)?, arg(rest, 1)?).await.map_err(refused)?;
            Value::Null
        }
        "prune" => {
            let limit: usize = arg(rest, 0)?.parse().map_err(|err| format!("limit: {err}"))?;
            Value::from(queue.prune(limit).await.map_err(refused)?)
        }
        other => return Err(format!("no step {other}")),
    })
}
`

// queueParityScript runs one step of the TypeScript queue client on the
// database its second argument names, a Postgres URL or a sqlite: one, on
// the queue QUEUE_PARITY_QUEUE names, and prints its result as JSON. On
// SQLite it enqueues through enqueueStatement in a batch, as a server adds
// the message to the writes that caused it.
const queueParityScript = `import pg from 'pg';
import { enqueue, postgresQueue } from '@superschematic/http-runtime/postgres';
import { enqueueStatement, openSqlite, sqliteFile, sqliteQueue } from '@superschematic/http-runtime/sqlite';
import { deliveryPlannedQueue, orderPlacedQueue } from '@TYPES@/queues';

const [step, url, ...args] = process.argv.slice(2);
// The queue QUEUE_PARITY_QUEUE names, OrderPlaced when unset.
const definition = (process.env.QUEUE_PARITY_QUEUE === 'DeliveryPlanned' ? deliveryPlannedQueue : orderPlacedQueue) as typeof orderPlacedQueue;
const file = url.startsWith('sqlite:') ? openSqlite(sqliteFile(url)) : undefined;
const pool = file === undefined ? new pg.Pool({ connectionString: url, max: 1 }) : undefined;
try {
  const queue = file !== undefined ? sqliteQueue(file, definition) : postgresQueue(pool!, definition);
  let result: unknown = null;
  switch (step) {
    case 'claim':
      result = await queue.claim(Number(args[0]), 60);
      break;
    case 'enqueue': {
      if (file !== undefined) {
        await file.batch([enqueueStatement(file, definition, JSON.parse(args[0]))]);
        break;
      }
      const client = await pool!.connect();
      try {
        await client.query('BEGIN');
        await enqueue(client, definition, JSON.parse(args[0]));
        await client.query('COMMIT');
      } finally {
        client.release();
      }
      break;
    }
    case 'fail':
      result = await queue.fail(args[0], args[1], args[2]);
      break;
    case 'extend':
      await queue.extend(args[0], args[1], Number(args[2]));
      break;
    case 'release':
      await queue.release(args[0], args[1]);
      break;
    case 'complete':
      await queue.complete(args[0], args[1]);
      break;
    case 'prune':
      result = await queue.prune(Number(args[0]));
      break;
    default:
      throw new Error('no step ' + step);
  }
  console.log(JSON.stringify(result));
} catch (error) {
  console.error(error instanceof Error ? error.name + ': ' + error.message : String(error));
  process.exitCode = 1;
} finally {
  file?.close();
  await pool?.end();
}
`

const queueParityORMTest = `package orm

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	scalars "github.com/parable-work/superscalar/go"
	types "example.com/schemas/types/go/fixture-queue-db"
	_ "modernc.org/sqlite"
)

// printedClaim is a claim the TypeScript client or the Rust binary
// printed, its message as that side's types serialize it: TypeScript's
// UUID hyphenated and its timestamp as its ISO text, Rust's UUID in base62
// and its timestamp in RFC 3339, its fraction trimmed, as Go's types write
// them.
type printedClaim struct {
	ID      string         ` + "`json:\"id\"`" + `
	Token   string         ` + "`json:\"token\"`" + `
	Attempt int            ` + "`json:\"attempt\"`" + `
	Message map[string]any ` + "`json:\"message\"`" + `
}

// claim is a claim of any side, its message as Go's types read it.
type claim struct {
	ID      string
	Token   string
	Attempt int
	Message types.OrderPlaced
}

// side is one language's queue client over a parity's schema. A change
// refused as a lost claim returns an error wrapping ErrClaimLost.
type side struct {
	name     string
	enqueue  func(message string)
	claim    func(n int) []claim
	complete func(id, token string) error
	fail     func(id, token, cause string) (bool, error)
	extend   func(id, token string, seconds int) error
	release  func(id, token string) error
	prune    func(limit int) (int, error)
}

// orderQueue is the Go ORM's queue OrderPlaced: OrderPlacedQueue on
// Postgres, OrderPlacedSQLiteQueue on SQLite.
type orderQueue interface {
	Claim(ctx context.Context, n int, lease time.Duration) ([]OrderPlacedClaim, error)
	Complete(ctx context.Context, id, token string) error
	Fail(ctx context.Context, id, token, cause string) (bool, error)
	Extend(ctx context.Context, id, token string, lease time.Duration) error
	Release(ctx context.Context, id, token string) error
	Prune(ctx context.Context, limit int) (int, error)
}

// sqliteDialect is QUEUE_PARITY_DIALECT's value that runs the parity on
// SQLite; any other runs it on Postgres.
const sqliteDialect = "sqlite"

// parity is a database of a subtest's own holding the queue's tables: a
// schema of the Postgres at SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL, or a
// SQLite file when QUEUE_PARITY_DIALECT is sqlite. It holds the Go ORM's
// queue over it, and the URL by which the TypeScript client and the Rust
// binary reach the same database.
type parity struct {
	t     *testing.T
	ctx   context.Context
	pool  *pgxpool.Pool
	db    *Database
	file  *sql.DB
	queue orderQueue
	url   string

	// on is the queue the TypeScript client and the Rust binary run their
	// steps on, which they read from QUEUE_PARITY_QUEUE: OrderPlaced when
	// empty.
	on string
}

// onSQLite reports whether the parity runs on SQLite.
func onSQLite() bool { return os.Getenv("QUEUE_PARITY_DIALECT") == sqliteDialect }

func newParity(t *testing.T) *parity {
	t.Helper()
	if onSQLite() {
		return newSQLiteParity(t)
	}
	ctx := context.Background()
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	createSQL, err := os.ReadFile("testdata/create.sql")
	if err != nil {
		t.Fatal(err)
	}
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(base.Close)
	schema := fmt.Sprintf("queues_parity_%d", time.Now().UnixNano())
	if _, err := base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = base.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, string(createSQL)); err != nil {
		t.Fatalf("apply create.sql: %v", err)
	}
	db, err := ConnectWithPool(pool)
	if err != nil {
		t.Fatal(err)
	}
	// The option holds no space: a URL's query writes one as +, which
	// tokio-postgres reads as a plus.
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("options", "-csearch_path="+schema+",public")
	u.RawQuery = q.Encode()
	return &parity{t: t, ctx: ctx, pool: pool, db: db, queue: db.OrderPlacedQueue(), url: u.String()}
}

// newSQLiteParity is a SQLite file of the subtest's own holding the
// queue's tables, testdata/create_sqlite.sql, opened through
// modernc.org/sqlite with the busy timeout the TypeScript and Rust clients
// use, since their processes write the same file.
func newSQLiteParity(t *testing.T) *parity {
	t.Helper()
	createSQL, err := os.ReadFile("testdata/create_sqlite.sql")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "queue.sqlite")
	file, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	if _, err := file.Exec(string(createSQL)); err != nil {
		t.Fatalf("apply create_sqlite.sql: %v", err)
	}
	return &parity{t: t, ctx: context.Background(), file: file, queue: NewOrderPlacedSQLiteQueue(file), url: "sqlite:" + path}
}

// enqueue writes msg with the Go ORM in a transaction of its own.
func (p *parity) enqueue(msg *types.OrderPlaced) error {
	if p.file == nil {
		return p.db.Transaction(p.ctx, func(tx TxInterface) error { return EnqueueOrderPlaced(p.ctx, tx, msg) })
	}
	tx, err := p.file.BeginTx(p.ctx, nil)
	if err != nil {
		return err
	}
	if err := EnqueueOrderPlacedSQLite(p.ctx, tx, msg); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// dialectSQL is the SQL of the parity's dialect: postgres's on Postgres, sqlite's
// on SQLite.
func (p *parity) dialectSQL(postgres, sqlite string) string {
	if p.file != nil {
		return sqlite
	}
	return postgres
}

// queryRow runs a query of one row, its arguments by number ($1 or ?1).
func (p *parity) queryRow(query string, args []any, dest ...any) {
	p.t.Helper()
	var err error
	if p.file != nil {
		err = p.file.QueryRowContext(p.ctx, query, args...).Scan(dest...)
	} else {
		err = p.pool.QueryRow(p.ctx, query, args...).Scan(dest...)
	}
	if err != nil {
		p.t.Fatalf("%s: %v", query, err)
	}
}

// sqliteAgo is SQLite's clock moved by seconds, in the form a queue's
// times hold.
func sqliteAgo(seconds float64) string {
	return fmt.Sprintf("strftime('%%Y-%%m-%%dT%%H:%%M:%%fZ', 'now', '%+f seconds')", -seconds)
}

// expireClaim makes a message's claim expired a second ago.
func (p *parity) expireClaim(id string) {
	p.t.Helper()
	p.exec(p.dialectSQL(
		"UPDATE order_placed_queue SET claim_expires_at = now() - interval '1 second' WHERE id = $1::uuid",
		"UPDATE order_placed_queue SET claim_expires_at = "+sqliteAgo(1)+" WHERE id = ?1",
	), id)
}

// dueSoon makes a message due in two seconds.
func (p *parity) dueSoon(id string) {
	p.t.Helper()
	p.exec(p.dialectSQL(
		"UPDATE order_placed_queue SET due_at = now() + interval '2 seconds' WHERE id = $1::uuid",
		"UPDATE order_placed_queue SET due_at = "+sqliteAgo(-2)+" WHERE id = ?1",
	), id)
}

// doneAgo makes a message done ago.
func (p *parity) doneAgo(id string, ago time.Duration) {
	p.t.Helper()
	p.exec(p.dialectSQL(
		fmt.Sprintf("UPDATE order_placed_queue SET updated_at = now() - make_interval(secs => %f) WHERE id = $1::uuid", ago.Seconds()),
		"UPDATE order_placed_queue SET updated_at = "+sqliteAgo(ago.Seconds())+" WHERE id = ?1",
	), id)
}

// markDead makes a claimed message dead for a month.
func (p *parity) markDead(id string) {
	p.t.Helper()
	p.exec(p.dialectSQL(
		"UPDATE order_placed_queue SET state = 'dead', claim_token = NULL, claim_expires_at = NULL, updated_at = now() - interval '30 days' WHERE id = $1::uuid",
		"UPDATE order_placed_queue SET state = 'dead', claim_token = NULL, claim_expires_at = NULL, updated_at = "+sqliteAgo(30*86400)+" WHERE id = ?1",
	), id)
}

// deadAndDone are the IDs of the dead messages and of the done ones, each
// joined by commas.
func (p *parity) deadAndDone() (string, string) {
	p.t.Helper()
	var dead, done string
	p.queryRow(p.dialectSQL(
		"SELECT COALESCE(string_agg(id::text, ',') FILTER (WHERE state = 'dead'), ''), COALESCE(string_agg(id::text, ',') FILTER (WHERE state = 'done'), '') FROM order_placed_queue",
		"SELECT COALESCE(group_concat(id, ',') FILTER (WHERE state = 'dead'), ''), COALESCE(group_concat(id, ',') FILTER (WHERE state = 'done'), '') FROM order_placed_queue",
	), nil, &dead, &done)
	return dead, done
}

// step runs one step of the TypeScript client or the Rust binary and
// returns what it printed. A step refused as a lost claim, which
// TypeScript prints as its ClaimLostError and the Rust binary as
// ClaimLost, returns an error wrapping ErrClaimLost.
func (p *parity) step(side, step string, args ...string) (string, error) {
	var cmd *exec.Cmd
	var lost string
	switch side {
	case "TypeScript":
		cmd = exec.Command(os.Getenv("QUEUE_PARITY_BUN"), append([]string{os.Getenv("QUEUE_PARITY_SCRIPT"), step, p.url}, args...)...)
		lost = "ClaimLostError: "
	case "Rust":
		cmd = exec.Command(os.Getenv("QUEUE_PARITY_RUST"), append([]string{step, p.url}, args...)...)
		lost = "ClaimLost: "
	default:
		p.t.Fatalf("no side %s", side)
	}
	cmd.Env = append(os.Environ(), "QUEUE_PARITY_QUEUE="+p.on)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	switch {
	case err == nil:
		return text, nil
	case strings.HasPrefix(text, lost):
		return text, fmt.Errorf("the %s %s printed %q: %w", side, step, text, ErrClaimLost)
	default:
		return text, fmt.Errorf("the %s %s: %v\n%s", side, step, err, text)
	}
}

// must is step, failing the test on an error.
func (p *parity) must(side, step string, args ...string) string {
	p.t.Helper()
	out, err := p.step(side, step, args...)
	if err != nil {
		p.t.Fatal(err)
	}
	return out
}

// printed claims up to n messages with the TypeScript client or the Rust
// binary, as it printed them.
func (p *parity) printed(side string, n int) []printedClaim {
	p.t.Helper()
	out := p.must(side, "claim", fmt.Sprint(n))
	var claims []printedClaim
	if err := json.Unmarshal([]byte(out), &claims); err != nil {
		p.t.Fatalf("the %s claim printed %s: %v", side, out, err)
	}
	return claims
}

// typed reads a message a side printed as Go's types read it.
func (p *parity) typed(side string, message map[string]any) types.OrderPlaced {
	p.t.Helper()
	data, err := json.Marshal(message)
	if err != nil {
		p.t.Fatal(err)
	}
	var m types.OrderPlaced
	if err := json.Unmarshal(data, &m); err != nil {
		p.t.Fatalf("Go's types read the %s message %s: %v", side, data, err)
	}
	return m
}

// sides are the three queue clients over p's schema: the Go ORM in the
// test's process, and the TypeScript client and the Rust binary in a
// process per step. Each enqueues a message from its JSON as its types
// read it.
func (p *parity) sides() map[string]side {
	ctx := p.ctx
	goSide := side{
		name: "Go",
		enqueue: func(message string) {
			p.t.Helper()
			var msg types.OrderPlaced
			if err := json.Unmarshal([]byte(message), &msg); err != nil {
				p.t.Fatal(err)
			}
			if err := p.enqueue(&msg); err != nil {
				p.t.Fatalf("Go's enqueue: %v", err)
			}
		},
		claim: func(n int) []claim {
			p.t.Helper()
			got, err := p.queue.Claim(ctx, n, time.Minute)
			if err != nil {
				p.t.Fatalf("Go's Claim: %v", err)
			}
			claims := make([]claim, len(got))
			for i, c := range got {
				claims[i] = claim{ID: c.ID, Token: c.Token, Attempt: c.Attempt, Message: c.Message}
			}
			return claims
		},
		complete: func(id, token string) error { return p.queue.Complete(ctx, id, token) },
		fail:     func(id, token, cause string) (bool, error) { return p.queue.Fail(ctx, id, token, cause) },
		extend: func(id, token string, seconds int) error {
			return p.queue.Extend(ctx, id, token, time.Duration(seconds)*time.Second)
		},
		release: func(id, token string) error { return p.queue.Release(ctx, id, token) },
		prune:   func(limit int) (int, error) { return p.queue.Prune(ctx, limit) },
	}
	process := func(name string) side {
		return side{
			name:    name,
			enqueue: func(message string) { p.t.Helper(); p.must(name, "enqueue", message) },
			claim: func(n int) []claim {
				p.t.Helper()
				printed := p.printed(name, n)
				claims := make([]claim, len(printed))
				for i, c := range printed {
					claims[i] = claim{ID: c.ID, Token: c.Token, Attempt: c.Attempt, Message: p.typed(name, c.Message)}
				}
				return claims
			},
			complete: func(id, token string) error {
				_, err := p.step(name, "complete", id, token)
				return err
			},
			fail: func(id, token, cause string) (bool, error) {
				out, err := p.step(name, "fail", id, token, cause)
				if err != nil {
					return false, err
				}
				if out != "true" && out != "false" {
					return false, fmt.Errorf("the %s fail printed %q", name, out)
				}
				return out == "true", nil
			},
			extend: func(id, token string, seconds int) error {
				_, err := p.step(name, "extend", id, token, fmt.Sprint(seconds))
				return err
			},
			release: func(id, token string) error {
				_, err := p.step(name, "release", id, token)
				return err
			},
			prune: func(limit int) (int, error) {
				out, err := p.step(name, "prune", fmt.Sprint(limit))
				if err != nil {
					return 0, err
				}
				var n int
				if _, err := fmt.Sscan(out, &n); err != nil {
					return 0, fmt.Errorf("the %s prune printed %q", name, out)
				}
				return n, nil
			},
		}
	}
	return map[string]side{"Go": goSide, "TypeScript": process("TypeScript"), "Rust": process("Rust")}
}

// one is the one claim s makes of a single message, which must be the
// message's attempt.
func (p *parity) one(s side, attempt int) claim {
	p.t.Helper()
	claims := s.claim(1)
	if len(claims) != 1 || claims[0].Attempt != attempt {
		p.t.Fatalf("the %s claim = %+v, want one message's attempt %d", s.name, claims, attempt)
	}
	return claims[0]
}

// state is a message's state, its attempts and its last error.
func (p *parity) state(id string) (string, int, string) {
	p.t.Helper()
	var s, lastError string
	var attempts int
	p.queryRow(p.dialectSQL(
		"SELECT state, attempts, COALESCE(last_error, '') FROM order_placed_queue WHERE id = $1::uuid",
		"SELECT state, attempts, COALESCE(last_error, '') FROM order_placed_queue WHERE id = ?1",
	), []any{id}, &s, &attempts, &lastError)
	return s, attempts, lastError
}

// expect fails the test unless a message is in state after attempts, its
// last error lastError.
func (p *parity) expect(id, state string, attempts int, lastError string) {
	p.t.Helper()
	if s, a, e := p.state(id); s != state || a != attempts || e != lastError {
		p.t.Fatalf("message %s is %s after %d attempts, its last error %q; want %s after %d, %q", id, s, a, e, state, attempts, lastError)
	}
}

// dueIn is the seconds until a message is due.
func (p *parity) dueIn(id string) float64 {
	p.t.Helper()
	var seconds float64
	p.queryRow(p.dialectSQL(
		"SELECT extract(epoch FROM due_at - now())::float8 FROM order_placed_queue WHERE id = $1::uuid",
		// Rounded to the millisecond, SQLite's clock, past julianday's float.
		"SELECT round((julianday(due_at) - julianday('now')) * 86400, 3) FROM order_placed_queue WHERE id = ?1",
	), []any{id}, &seconds)
	return seconds
}

// exec runs a statement on the parity's schema.
func (p *parity) exec(sql string, args ...any) {
	p.t.Helper()
	var err error
	if p.file != nil {
		_, err = p.file.ExecContext(p.ctx, sql, args...)
	} else {
		_, err = p.pool.Exec(p.ctx, sql, args...)
	}
	if err != nil {
		p.t.Fatalf("%s: %v", sql, err)
	}
}

// expiresIn is the seconds until a message's claim expires.
func (p *parity) expiresIn(id string) float64 {
	p.t.Helper()
	var seconds float64
	p.queryRow(p.dialectSQL(
		"SELECT extract(epoch FROM claim_expires_at - now())::float8 FROM order_placed_queue WHERE id = $1::uuid",
		// Rounded to the millisecond, SQLite's clock, past julianday's float.
		"SELECT round((julianday(claim_expires_at) - julianday('now')) * 86400, 3) FROM order_placed_queue WHERE id = ?1",
	), []any{id}, &seconds)
	return seconds
}

// due makes each ready message due now, past its backoff.
func (p *parity) due() {
	p.t.Helper()
	p.exec(p.dialectSQL(
		"UPDATE order_placed_queue SET due_at = now() - interval '1 second' WHERE state = 'ready'",
		"UPDATE order_placed_queue SET due_at = "+sqliteAgo(1)+" WHERE state = 'ready'",
	))
}

func mustUUID(text string) scalars.UUID {
	id, err := scalars.ParseUUID(text)
	if err != nil {
		panic(err)
	}
	return id
}

// sameMessage reports whether two messages hold the same values: the same
// instant, wherever its location, and an empty list as no list.
func sameMessage(a, b types.OrderPlaced) bool {
	if !time.Time(a.PlacedAt).Equal(time.Time(b.PlacedAt)) {
		return false
	}
	a.PlacedAt, b.PlacedAt = scalars.TemporalDateTime{}, scalars.TemporalDateTime{}
	if len(a.Tags) == 0 {
		a.Tags = nil
	}
	if len(b.Tags) == 0 {
		b.Tags = nil
	}
	return reflect.DeepEqual(a, b)
}

// fullGoMessage is a message that sets every field, as Go enqueues it.
func fullGoMessage() *types.OrderPlaced {
	return &types.OrderPlaced{
		OrderId:  mustUUID("6f1c2f2e-7d1a-4b4a-9d43-3c7a1b2c3d4e"),
		PlacedAt: scalars.TemporalDateTime(time.Date(2026, 10, 9, 12, 30, 15, 250000000, time.UTC)),
		Priority: types.Priority_High,
		Quantity: 9007199254740991,
		Note:     "leave it by the door",
		ShipTo:   types.Address{Line1: "1 Main St", City: "Springfield"},
		Tags:     []string{"gift", "fragile"},
	}
}

// orderJSON is a message each side enqueues as it is: its ID hyphenated,
// its timestamp in RFC 3339, and tags an array literal quotes and escapes,
// an empty one and one beyond ASCII among them.
func orderJSON(id string) string {
	data, err := json.Marshal(map[string]any{
		"orderId":  id,
		"placedAt": "2026-10-12T16:45:30.75Z",
		"priority": "high",
		"quantity": 42,
		"note":     "from every side",
		"shipTo":   map[string]any{"line1": "5 Birch Ln", "city": "Capital City"},
		"tags":     []string{"a", "b \"c\"", "x,y", "back\\slash", "NULL", "", "ünïcode"},
	})
	if err != nil {
		panic(err)
	}
	return string(data)
}

// order is orderJSON's message as Go's types hold it.
func order(id string) types.OrderPlaced {
	return types.OrderPlaced{
		OrderId:  mustUUID(id),
		PlacedAt: scalars.TemporalDateTime(time.Date(2026, 10, 12, 16, 45, 30, 750000000, time.UTC)),
		Priority: types.Priority_High,
		Quantity: 42,
		Note:     "from every side",
		ShipTo:   types.Address{Line1: "5 Birch Ln", City: "Capital City"},
		Tags:     []string{"a", "b \"c\"", "x,y", "back\\slash", "NULL", "", "ünïcode"},
	}
}

// TestQueueParity runs the Go ORM's queue, the TypeScript client and the
// Rust client against one Postgres, each subtest in a schema of its own.
func TestQueueParity(t *testing.T) {
	t.Run("GoAndTypeScript", testGoAndTypeScript)
	t.Run("RustReadsGoAndTypeScript", testRustReadsGoAndTypeScript)
	t.Run("GoAndTypeScriptReadRust", testGoAndTypeScriptReadRust)
	t.Run("EachChangesTheOthersClaims", testEachChangesTheOthersClaims)
	t.Run("AttemptsAndStatesAgree", testAttemptsAndStatesAgree)
	t.Run("BackoffGrowsForEverySide", testBackoffGrowsForEverySide)
	t.Run("EachPrunesTheOthersDoneMessages", testEachPrunesTheOthersDoneMessages)
	t.Run("LocationsAndDurationsAgree", testLocationsAndDurationsAgree)
}

// testGoAndTypeScript: the Go ORM enqueues a message, which the TypeScript
// client claims and reads field for field; Go fails that claim under the
// TypeScript token; TypeScript enqueues one, which Go claims and reads,
// its enum's default included; TypeScript releases and completes claims Go
// made; and each fails the other's claims until the message is dead.
func testGoAndTypeScript(t *testing.T) {
	p := newParity(t)
	ctx, queue := p.ctx, p.queue
	ts := func(step string, args ...string) string {
		t.Helper()
		return p.must("TypeScript", step, args...)
	}
	tsClaims := func(n int) []printedClaim {
		t.Helper()
		return p.printed("TypeScript", n)
	}

	// Go enqueues; TypeScript claims and reads every field.
	msg := fullGoMessage()
	if err := p.enqueue(msg); err != nil {
		t.Fatal(err)
	}
	claims := tsClaims(5)
	if len(claims) != 1 || claims[0].Attempt != 1 {
		t.Fatalf("the TypeScript claim = %+v, want the Go message's first attempt", claims)
	}
	want := map[string]any{
		"orderId":  "6f1c2f2e-7d1a-4b4a-9d43-3c7a1b2c3d4e",
		"placedAt": "2026-10-09T12:30:15.250Z",
		"priority": "high",
		"quantity": float64(9007199254740991),
		"note":     "leave it by the door",
		"shipTo":   map[string]any{"line1": "1 Main St", "city": "Springfield"},
		"tags":     []any{"gift", "fragile"},
	}
	if !reflect.DeepEqual(claims[0].Message, want) {
		t.Fatalf("the TypeScript client read %v, want %v", claims[0].Message, want)
	}

	// Go fails the TypeScript claim under its token: due after the backoff.
	fromTS := claims[0]
	if dead, err := queue.Fail(ctx, fromTS.ID, fromTS.Token, "failed in Go"); err != nil || dead {
		t.Fatalf("Go's Fail of the TypeScript claim = %t, %v", dead, err)
	}
	p.expect(fromTS.ID, "ready", 1, "failed in Go")
	if _, err := p.step("TypeScript", "complete", fromTS.ID, fromTS.Token); !errors.Is(err, ErrClaimLost) {
		t.Fatalf("the TypeScript complete under the token Go's Fail ended = %v, want a lost claim", err)
	}
	if s, _, _ := p.state(fromTS.ID); s != "ready" {
		t.Fatalf("a complete under a lost token changed the message: %s", s)
	}

	// TypeScript enqueues, with the enum unset and a timestamp as its ISO
	// text; Go claims and reads it, the enum's default included.
	ts("enqueue", ` + "`" + `{"orderId":"0c7a0e14-6a43-4f0e-8f1b-5d6c7e8f9a0b","placedAt":"2026-10-10T08:00:00.5Z","quantity":7,"shipTo":{"line1":"2 Elm St","city":"Shelbyville"},"tags":["from-ts"]}` + "`" + `)
	p.due()
	got, err := queue.Claim(ctx, 5, time.Minute)
	if err != nil || len(got) != 2 {
		t.Fatalf("Go's Claim = %+v, %v, want both messages", got, err)
	}
	var mine, theirs OrderPlacedClaim
	for _, c := range got {
		if c.ID == fromTS.ID {
			mine = c
		} else {
			theirs = c
		}
	}
	if mine.Attempt != 2 || theirs.Attempt != 1 {
		t.Fatalf("Go claimed attempts %d and %d, want 2 for the failed message and 1 for TypeScript's", mine.Attempt, theirs.Attempt)
	}
	m := theirs.Message
	if m.OrderId != mustUUID("0c7a0e14-6a43-4f0e-8f1b-5d6c7e8f9a0b") || !time.Time(m.PlacedAt).Equal(time.Date(2026, 10, 10, 8, 0, 0, 500000000, time.UTC)) ||
		m.Priority != types.Priority_Low || m.Quantity != 7 || m.Note != "" || m.ShipTo != (types.Address{Line1: "2 Elm St", City: "Shelbyville"}) || !reflect.DeepEqual(m.Tags, []string{"from-ts"}) {
		t.Fatalf("Go read the TypeScript message as %+v", m)
	}

	// TypeScript releases one Go claim, uncounted, and completes the other.
	ts("release", mine.ID, mine.Token)
	p.expect(mine.ID, "ready", 1, "failed in Go")
	ts("complete", theirs.ID, theirs.Token)
	if s, _, _ := p.state(theirs.ID); s != "done" {
		t.Fatalf("after the TypeScript complete: %s, want done", s)
	}
	if err := queue.Complete(ctx, theirs.ID, theirs.Token); !errors.Is(err, ErrClaimLost) {
		t.Fatalf("Go's Complete of a message TypeScript completed = %v, want a lost claim", err)
	}

	// Each fails the other's claims until the retries, three, are spent:
	// the fourth failure, TypeScript's, leaves it dead.
	for attempt := 2; attempt <= 4; attempt++ {
		p.due()
		claims := tsClaims(1)
		if len(claims) != 1 || claims[0].Attempt != attempt {
			t.Fatalf("the TypeScript claim of attempt %d = %+v", attempt, claims)
		}
		if attempt < 4 {
			if dead, err := queue.Fail(ctx, claims[0].ID, claims[0].Token, fmt.Sprintf("try %d", attempt)); err != nil || dead {
				t.Fatalf("Go's Fail of attempt %d = %t, %v", attempt, dead, err)
			}
			continue
		}
		if dead := ts("fail", claims[0].ID, claims[0].Token, "try 4"); dead != "true" {
			t.Fatalf("the TypeScript fail of the last attempt printed %s, want true", dead)
		}
	}
	p.expect(mine.ID, "dead", 4, "try 4")
}

// testRustReadsGoAndTypeScript: the Rust client claims what Go and
// TypeScript enqueue and reads each field as Rust's types serialize it:
// Go's message that sets every field, Go's that leaves the enum, the note
// and the list unset, its timestamp to the microsecond, and TypeScript's
// that leaves them out, each unset enum as its default and each unset list
// empty. Go's types read what
// Rust read of Go's first message as Go wrote it, and while Rust holds the
// claims neither Go nor TypeScript claims a message.
func testRustReadsGoAndTypeScript(t *testing.T) {
	p := newParity(t)
	full := fullGoMessage()
	unset := &types.OrderPlaced{
		OrderId:  mustUUID("5e6f7a8b-9c0d-4e1f-a2b3-c4d5e6f7a8b9"),
		PlacedAt: scalars.TemporalDateTime(time.Date(2026, 10, 9, 13, 0, 0, 123456000, time.UTC)),
		Quantity: -3,
		ShipTo:   types.Address{Line1: "9 Ash Ct", City: "Springfield"},
	}
	for _, msg := range []*types.OrderPlaced{full, unset} {
		if err := p.enqueue(msg); err != nil {
			t.Fatal(err)
		}
	}
	p.must("TypeScript", "enqueue", ` + "`" + `{"orderId":"0c7a0e14-6a43-4f0e-8f1b-5d6c7e8f9a0b","placedAt":"2026-10-10T08:00:00.5Z","quantity":7,"shipTo":{"line1":"2 Elm St","city":"Shelbyville"}}` + "`" + `)

	// Each message as Rust's types serialize it, by its ID in base62.
	want := map[string]map[string]any{
		"3Nf1lmBDcdDKfsC9ccR4DG": {
			"orderId":  "3Nf1lmBDcdDKfsC9ccR4DG",
			"placedAt": "2026-10-09T12:30:15.25Z",
			"priority": "high",
			"quantity": float64(9007199254740991),
			"note":     "leave it by the door",
			"shipTo":   map[string]any{"line1": "1 Main St", "city": "Springfield"},
			"tags":     []any{"gift", "fragile"},
		},
		"2sCDvWPkytfLo9GgrB0aAj": {
			"orderId":  "2sCDvWPkytfLo9GgrB0aAj",
			"placedAt": "2026-10-09T13:00:00.123456Z",
			"priority": "low",
			"quantity": float64(-3),
			"shipTo":   map[string]any{"line1": "9 Ash Ct", "city": "Springfield"},
			"tags":     []any{},
		},
		"NXgbePD9wdfLRTkUZGl1X": {
			"orderId":  "NXgbePD9wdfLRTkUZGl1X",
			"placedAt": "2026-10-10T08:00:00.5Z",
			"priority": "low",
			"quantity": float64(7),
			"shipTo":   map[string]any{"line1": "2 Elm St", "city": "Shelbyville"},
			"tags":     []any{},
		},
	}
	claims := p.printed("Rust", 5)
	if len(claims) != len(want) {
		t.Fatalf("the Rust claim = %+v, want the %d messages", claims, len(want))
	}
	for _, c := range claims {
		id, _ := c.Message["orderId"].(string)
		if !reflect.DeepEqual(c.Message, want[id]) {
			t.Errorf("the Rust client read %v, want %v", c.Message, want[id])
		}
		if c.Attempt != 1 {
			t.Errorf("the Rust claim of %s is attempt %d, want 1", id, c.Attempt)
		}
		p.expect(c.ID, "claimed", 1, "")
		if id == "3Nf1lmBDcdDKfsC9ccR4DG" {
			if got := p.typed("Rust", c.Message); !sameMessage(got, *full) {
				t.Errorf("Go read the Rust client's message as %+v, want %+v", got, *full)
			}
		}
	}

	// The claims are Rust's: neither of the others claims a message.
	for name, s := range p.sides() {
		if name == "Rust" {
			continue
		}
		if got := s.claim(5); len(got) != 0 {
			t.Errorf("%s claimed %+v, which Rust holds", name, got)
		}
	}
}

// testGoAndTypeScriptReadRust: Go and TypeScript claim and read what the
// Rust client enqueues: a message that sets every field, its ID hyphenated
// and its timestamp at an offset, and one in Rust's own wire form, its ID
// in base62, that leaves the enum and the note out, which Rust's types
// write as the enum's default and no note, its timestamp to the
// microsecond. Go reads each, the microseconds too, then releases it,
// uncounted, for TypeScript to read, whose Date holds milliseconds.
func testGoAndTypeScriptReadRust(t *testing.T) {
	p := newParity(t)
	enqueue := func(message map[string]any) {
		t.Helper()
		data, err := json.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		p.must("Rust", "enqueue", string(data))
	}
	enqueue(map[string]any{
		"orderId":  "a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d",
		"placedAt": "2026-10-11T09:15:00.125+02:00",
		"priority": "high",
		"quantity": 9007199254740991,
		"note":     "ring twice",
		"shipTo":   map[string]any{"line1": "3 Oak Ave", "city": "Ogdenville"},
		"tags":     []string{"from-rust", "b \"c\"", "x,y"},
	})
	enqueue(map[string]any{
		"orderId":  "1rQEED3tFAHf9ylzV54KNR",
		"placedAt": "2026-10-11T10:00:00.654321Z",
		"quantity": 0,
		"shipTo":   map[string]any{"line1": "4 Pine Rd", "city": "North Haverbrook"},
		"tags":     []string{},
	})

	wantGo := map[string]types.OrderPlaced{
		"a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d": {
			OrderId:  mustUUID("a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d"),
			PlacedAt: scalars.TemporalDateTime(time.Date(2026, 10, 11, 7, 15, 0, 125000000, time.UTC)),
			Priority: types.Priority_High,
			Quantity: 9007199254740991,
			Note:     "ring twice",
			ShipTo:   types.Address{Line1: "3 Oak Ave", City: "Ogdenville"},
			Tags:     []string{"from-rust", "b \"c\"", "x,y"},
		},
		"3d2b1a0c-9e8f-4a7b-8c6d-5e4f3a2b1c0d": {
			OrderId:  mustUUID("3d2b1a0c-9e8f-4a7b-8c6d-5e4f3a2b1c0d"),
			PlacedAt: scalars.TemporalDateTime(time.Date(2026, 10, 11, 10, 0, 0, 654321000, time.UTC)),
			Priority: types.Priority_Low,
			Quantity: 0,
			ShipTo:   types.Address{Line1: "4 Pine Rd", City: "North Haverbrook"},
		},
	}
	got, err := p.queue.Claim(p.ctx, 5, time.Minute)
	if err != nil || len(got) != len(wantGo) {
		t.Fatalf("Go's Claim = %+v, %v, want the %d Rust messages", got, err, len(wantGo))
	}
	for _, c := range got {
		id := c.Message.OrderId.ToUUID().String()
		if want, ok := wantGo[id]; !ok || !sameMessage(c.Message, want) {
			t.Errorf("Go read the Rust message %s as %+v, want %+v", id, c.Message, want)
		}
		if c.Attempt != 1 {
			t.Errorf("Go's claim of %s is attempt %d, want 1", id, c.Attempt)
		}
		if err := p.queue.Release(p.ctx, c.ID, c.Token); err != nil {
			t.Fatal(err)
		}
		p.expect(c.ID, "ready", 0, "")
	}

	// Each message as TypeScript's types hold it, by its ID.
	wantTS := map[string]map[string]any{
		"a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d": {
			"orderId":  "a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d",
			"placedAt": "2026-10-11T07:15:00.125Z",
			"priority": "high",
			"quantity": float64(9007199254740991),
			"note":     "ring twice",
			"shipTo":   map[string]any{"line1": "3 Oak Ave", "city": "Ogdenville"},
			"tags":     []any{"from-rust", "b \"c\"", "x,y"},
		},
		"3d2b1a0c-9e8f-4a7b-8c6d-5e4f3a2b1c0d": {
			"orderId":  "3d2b1a0c-9e8f-4a7b-8c6d-5e4f3a2b1c0d",
			"placedAt": "2026-10-11T10:00:00.654Z",
			"priority": "low",
			"quantity": float64(0),
			"shipTo":   map[string]any{"line1": "4 Pine Rd", "city": "North Haverbrook"},
			"tags":     []any{},
		},
	}
	claims := p.printed("TypeScript", 5)
	if len(claims) != len(wantTS) {
		t.Fatalf("the TypeScript claim = %+v, want the %d Rust messages", claims, len(wantTS))
	}
	for _, c := range claims {
		id, _ := c.Message["orderId"].(string)
		if !reflect.DeepEqual(c.Message, wantTS[id]) {
			t.Errorf("the TypeScript client read %v, want %v", c.Message, wantTS[id])
		}
		if c.Attempt != 1 {
			t.Errorf("the TypeScript claim of %s is attempt %d, want 1, Go's released uncounted", id, c.Attempt)
		}
	}
}

// testEachChangesTheOthersClaims: for each side that claims and each other
// side that changes the claim, a message the third side enqueues is
// claimed by the first, which reads it as it was written. The second
// extends that claim and releases it, uncounted, under the first's token,
// which then claims the message no more; fails the first's next claim,
// which makes it due after the backoff; and completes the claim after
// that. Once complete, the token is refused as a lost claim by each side.
func testEachChangesTheOthersClaims(t *testing.T) {
	names := []string{"Go", "TypeScript", "Rust"}
	for _, claimer := range names {
		for _, changer := range names {
			if claimer == changer {
				continue
			}
			var enqueuer string
			for _, name := range names {
				if name != claimer && name != changer {
					enqueuer = name
				}
			}
			t.Run(enqueuer+"Enqueues"+claimer+"Claims"+changer+"Changes", func(t *testing.T) {
				p := newParity(t)
				sides := p.sides()
				c, x, e := sides[claimer], sides[changer], sides[enqueuer]
				const id = "9b8a7c6d-5e4f-4a3b-9c2d-1e0f9a8b7c6d"
				e.enqueue(orderJSON(id))

				first := p.one(c, 1)
				if !sameMessage(first.Message, order(id)) {
					t.Fatalf("%s read %s's message as %+v, want %+v", claimer, enqueuer, first.Message, order(id))
				}
				if err := x.extend(first.ID, first.Token, 600); err != nil {
					t.Fatalf("%s's extend of %s's claim: %v", changer, claimer, err)
				}
				if s := p.expiresIn(first.ID); s < 500 || s > 600 {
					t.Fatalf("after %s's extend of %s's claim it expires in %.0fs, want about 600", changer, claimer, s)
				}
				if err := x.release(first.ID, first.Token); err != nil {
					t.Fatalf("%s's release of %s's claim: %v", changer, claimer, err)
				}
				p.expect(first.ID, "ready", 0, "")
				if err := e.complete(first.ID, first.Token); !errors.Is(err, ErrClaimLost) {
					t.Fatalf("%s's complete under the token %s released = %v, want a lost claim", enqueuer, changer, err)
				}

				second := p.one(c, 1)
				if second.Token == first.Token {
					t.Fatalf("%s's claim after the release kept the token %s", claimer, first.Token)
				}
				cause := "failed in " + changer
				if dead, err := x.fail(second.ID, second.Token, cause); err != nil || dead {
					t.Fatalf("%s's fail of %s's claim = %t, %v", changer, claimer, dead, err)
				}
				p.expect(second.ID, "ready", 1, cause)
				if got := c.claim(1); len(got) != 0 {
					t.Fatalf("%s claimed %+v within the backoff", claimer, got)
				}
				p.due()

				third := p.one(c, 2)
				if err := x.complete(third.ID, third.Token); err != nil {
					t.Fatalf("%s's complete of %s's claim: %v", changer, claimer, err)
				}
				p.expect(third.ID, "done", 2, cause)
				for _, s := range []side{c, x, e} {
					if err := s.complete(third.ID, third.Token); !errors.Is(err, ErrClaimLost) {
						t.Errorf("%s's complete of the message %s completed = %v, want a lost claim", s.name, changer, err)
					}
					if dead, err := s.fail(third.ID, third.Token, "late"); !errors.Is(err, ErrClaimLost) || dead {
						t.Errorf("%s's fail of the message %s completed = %t, %v, want a lost claim", s.name, changer, dead, err)
					}
				}
				if err := c.extend(third.ID, third.Token, 60); !errors.Is(err, ErrClaimLost) {
					t.Errorf("%s's extend of the message %s completed = %v, want a lost claim", claimer, changer, err)
				}
				if err := e.release(third.ID, third.Token); !errors.Is(err, ErrClaimLost) {
					t.Errorf("%s's release of the message %s completed = %v, want a lost claim", enqueuer, changer, err)
				}
				p.expect(third.ID, "done", 2, cause)
				if got := c.claim(1); len(got) != 0 {
					t.Fatalf("%s claimed %+v, which is done", claimer, got)
				}
			})
		}
	}
}

// testAttemptsAndStatesAgree: a message Rust enqueues passes through every
// side, each attempt claimed by one and failed by another, each side
// counting the same attempt, until the retries, three, are spent and the
// fourth failure, Rust's of Go's claim, leaves it dead, which no side
// claims. Then a claim TypeScript lets expire Rust claims again, one
// attempt more under a new token, and the expired token is lost to each.
func testAttemptsAndStatesAgree(t *testing.T) {
	p := newParity(t)
	sides := p.sides()
	const id = "9b8a7c6d-5e4f-4a3b-9c2d-1e0f9a8b7c6d"
	sides["Rust"].enqueue(orderJSON(id))
	lastError := ""
	for i, round := range []struct{ claimer, failer string }{
		{"Go", "TypeScript"},
		{"TypeScript", "Rust"},
		{"Rust", "Go"},
		{"Go", "Rust"},
	} {
		attempt := i + 1
		p.due()
		c := p.one(sides[round.claimer], attempt)
		if !sameMessage(c.Message, order(id)) {
			t.Fatalf("%s read attempt %d as %+v", round.claimer, attempt, c.Message)
		}
		p.expect(c.ID, "claimed", attempt, lastError)
		lastError = fmt.Sprintf("attempt %d failed in %s", attempt, round.failer)
		dead, err := sides[round.failer].fail(c.ID, c.Token, lastError)
		if err != nil || dead != (attempt == 4) {
			t.Fatalf("%s's fail of %s's attempt %d = %t, %v; want dead after the fourth", round.failer, round.claimer, attempt, dead, err)
		}
		state := "ready"
		if attempt == 4 {
			state = "dead"
		}
		p.expect(c.ID, state, attempt, lastError)
	}
	p.due()
	for name, s := range sides {
		if got := s.claim(5); len(got) != 0 {
			t.Fatalf("%s claimed %+v, which is dead", name, got)
		}
	}

	const expiring = "0c7a0e14-6a43-4f0e-8f1b-5d6c7e8f9a0b"
	sides["Go"].enqueue(orderJSON(expiring))
	first := p.one(sides["TypeScript"], 1)
	p.expireClaim(first.ID)
	again := p.one(sides["Rust"], 2)
	if again.ID != first.ID || again.Token == first.Token {
		t.Fatalf("Rust claimed %+v, want the expired claim %s again under a new token", again, first.ID)
	}
	for name, s := range sides {
		if err := s.complete(first.ID, first.Token); !errors.Is(err, ErrClaimLost) {
			t.Errorf("%s's complete under the expired token = %v, want a lost claim", name, err)
		}
	}
	if err := sides["Go"].complete(again.ID, again.Token); err != nil {
		t.Fatalf("Go's complete of Rust's claim: %v", err)
	}
	p.expect(again.ID, "done", 2, "")
}

// testBackoffGrowsForEverySide: a message TypeScript enqueues fails three
// times, each failure another side's of a third side's claim. OrderPlaced's
// backoff is a minute and its maximum 200s, so the first failure makes it
// due in 60s, the second in 120s and the third in 200s, not 240s. Within
// each wait no side claims it: brought to two seconds before it is due,
// every side's claim finds nothing; once due, the next side claims it. The
// fourth failure, past the three retries, leaves it dead.
func testBackoffGrowsForEverySide(t *testing.T) {
	p := newParity(t)
	sides := p.sides()
	const id = "9b8a7c6d-5e4f-4a3b-9c2d-1e0f9a8b7c6d"
	sides["TypeScript"].enqueue(orderJSON(id))
	for i, round := range []struct {
		claimer, failer string
		wait            float64
	}{
		{"Go", "Rust", 60},
		{"Rust", "TypeScript", 120},
		{"TypeScript", "Go", 200},
	} {
		attempt := i + 1
		c := p.one(sides[round.claimer], attempt)
		cause := fmt.Sprintf("attempt %d failed in %s", attempt, round.failer)
		if dead, err := sides[round.failer].fail(c.ID, c.Token, cause); err != nil || dead {
			t.Fatalf("%s's fail of %s's attempt %d = %t, %v", round.failer, round.claimer, attempt, dead, err)
		}
		p.expect(c.ID, "ready", attempt, cause)
		if left := p.dueIn(c.ID); left > round.wait || left < round.wait-5 {
			t.Fatalf("after %s's fail of attempt %d the message is due in %.1fs, want about %.0fs", round.failer, attempt, left, round.wait)
		}
		p.dueSoon(c.ID)
		for name, s := range sides {
			if got := s.claim(1); len(got) != 0 {
				t.Fatalf("%s claimed %+v within the backoff of attempt %d", name, got, attempt)
			}
		}
		p.due()
	}
	last := p.one(sides["Go"], 4)
	if dead, err := sides["Rust"].fail(last.ID, last.Token, "the last"); err != nil || !dead {
		t.Fatalf("Rust's fail of the fourth attempt = %t, %v, want dead", dead, err)
	}
	p.expect(last.ID, "dead", 4, "the last")
}

// testEachPrunesTheOthersDoneMessages: for each side that prunes, the other
// two each enqueue, claim and complete three messages, which are then done
// two hours ago, past OrderPlaced's hour's retention; one more done a
// minute ago is within it, and one dead a month is never pruned. The
// pruner's batches of four delete four, then the other two, then nothing,
// and leave the recent one and the dead one.
func testEachPrunesTheOthersDoneMessages(t *testing.T) {
	names := []string{"Go", "TypeScript", "Rust"}
	for _, pruner := range names {
		t.Run(pruner+"Prunes", func(t *testing.T) {
			p := newParity(t)
			sides := p.sides()
			var old []string
			for _, other := range names {
				if other == pruner {
					continue
				}
				s := sides[other]
				for i := range 3 {
					s.enqueue(orderJSON(fmt.Sprintf("9b8a7c6d-5e4f-4a3b-9c2d-1e0f9a8b7c%02d", len(old)+i)))
				}
				claims := s.claim(3)
				if len(claims) != 3 {
					t.Fatalf("%s claimed %d of its three messages", other, len(claims))
				}
				for _, c := range claims {
					if err := s.complete(c.ID, c.Token); err != nil {
						t.Fatalf("%s's complete: %v", other, err)
					}
					old = append(old, c.ID)
				}
			}
			for _, id := range old {
				p.doneAgo(id, 2*time.Hour)
			}
			sides["Go"].enqueue(orderJSON("0c7a0e14-6a43-4f0e-8f1b-5d6c7e8f9a0b"))
			recent := p.one(sides["Go"], 1)
			if err := sides["Rust"].complete(recent.ID, recent.Token); err != nil {
				t.Fatal(err)
			}
			p.doneAgo(recent.ID, time.Minute)
			sides["TypeScript"].enqueue(orderJSON("5e6f7a8b-9c0d-4e1f-a2b3-c4d5e6f7a8b9"))
			dead := p.one(sides["TypeScript"], 1)
			p.markDead(dead.ID)

			for i, want := range []int{4, 2, 0} {
				if n, err := sides[pruner].prune(4); err != nil || n != want {
					t.Fatalf("%s's prune %d deleted %d, %v; want %d", pruner, i+1, n, err, want)
				}
			}
			deadIDs, doneIDs := p.deadAndDone()
			if deadIDs != dead.ID || doneIDs != recent.ID {
				t.Fatalf("after %s's prunes the queue holds dead %q and done %q, want the dead %s and the recent %s", pruner, deadIDs, doneIDs, dead.ID, recent.ID)
			}
		})
	}
}

// deliveryJSON is a message of DeliveryPlanned as JSON.
func deliveryJSON(message map[string]any) string {
	data, err := json.Marshal(message)
	if err != nil {
		panic(err)
	}
	return string(data)
}

// location is a Geo.Location as Go's types hold it.
func location(lat, lon float64) *types.GeoLocation {
	return &types.GeoLocation{Lat: lat, Lon: lon}
}

// duration is a Temporal.Duration as Go's types hold it.
func duration(d time.Duration) *types.TemporalDuration {
	value := types.TemporalDuration(d)
	return &value
}

// testLocationsAndDurationsAgree: on DeliveryPlanned, whose locations are
// POINT columns and whose durations INTERVAL ones, each side enqueues a
// message: Go's with every field set and a duration of a nanosecond
// remainder, TypeScript's that leaves the optional fields out and has a
// negative duration of nanoseconds, and Rust's in minutes and
// microseconds. Each side then claims all three and reads each field as
// its types hold it: a location as {lat, lon}, a duration in the scalar
// core's canonical text (Go's as a time.Duration), truncated to its
// microseconds toward zero, and an unset field as absent. Then an interval
// written with days and months reads on each side as their hours, a day
// 24 and a month 30 days.
func testLocationsAndDurationsAgree(t *testing.T) {
	if onSQLite() {
		t.Skip("SQLite has no storage for a location, so DeliveryPlanned is Postgres's alone")
	}
	p := newParity(t)
	p.on = "DeliveryPlanned"
	queue := p.db.DeliveryPlannedQueue()
	const fromGo, fromTS, fromRust = "6f1c2f2e-7d1a-4b4a-9d43-3c7a1b2c3d4e", "0c7a0e14-6a43-4f0e-8f1b-5d6c7e8f9a0b", "3d2b1a0c-9e8f-4a7b-8c6d-5e4f3a2b1c0d"

	var goMessage types.DeliveryPlanned
	if err := json.Unmarshal([]byte(deliveryJSON(map[string]any{
		"orderId": fromGo,
		"dropAt":  map[string]any{"lat": 37.7749, "lon": -122.4194},
		"wait":    "26h0m0.0000015s",
		"meetAt":  map[string]any{"lat": -33.8688, "lon": 151.2093},
		"grace":   "-1m30.25s",
	})), &goMessage); err != nil {
		t.Fatal(err)
	}
	if err := p.db.Transaction(p.ctx, func(tx TxInterface) error { return EnqueueDeliveryPlanned(p.ctx, tx, &goMessage) }); err != nil {
		t.Fatalf("Go's enqueue: %v", err)
	}
	p.must("TypeScript", "enqueue", deliveryJSON(map[string]any{
		"orderId": fromTS,
		"dropAt":  map[string]any{"lat": -90, "lon": 180},
		"wait":    "-1500ns",
	}))
	p.must("Rust", "enqueue", deliveryJSON(map[string]any{
		"orderId": fromRust,
		"dropAt":  map[string]any{"lat": 0, "lon": 0},
		"wait":    "90m",
		"meetAt":  map[string]any{"lat": 51.5074, "lon": -0.1278},
		"grace":   "250us",
	}))

	// Each message as TypeScript's and Rust's types hold it, by its ID,
	// and as Go's do.
	wantPrinted := map[string]map[string]any{
		fromGo: {
			"dropAt": map[string]any{"lat": 37.7749, "lon": -122.4194},
			"wait":   "26h0m0.000001s",
			"meetAt": map[string]any{"lat": -33.8688, "lon": 151.2093},
			"grace":  "-1m30.25s",
		},
		fromTS: {
			"dropAt": map[string]any{"lat": float64(-90), "lon": float64(180)},
			"wait":   "-1us",
		},
		fromRust: {
			"dropAt": map[string]any{"lat": float64(0), "lon": float64(0)},
			"wait":   "1h30m0s",
			"meetAt": map[string]any{"lat": 51.5074, "lon": -0.1278},
			"grace":  "250us",
		},
	}
	wantGo := map[string]types.DeliveryPlanned{
		fromGo:   {DropAt: *location(37.7749, -122.4194), Wait: types.TemporalDuration(26*time.Hour + time.Microsecond), MeetAt: location(-33.8688, 151.2093), Grace: duration(-(time.Minute + 30*time.Second + 250*time.Millisecond))},
		fromTS:   {DropAt: *location(-90, 180), Wait: types.TemporalDuration(-time.Microsecond)},
		fromRust: {DropAt: *location(0, 0), Wait: types.TemporalDuration(90 * time.Minute), MeetAt: location(51.5074, -0.1278), Grace: duration(250 * time.Microsecond)},
	}
	check := func(round string, want func(id string) (map[string]any, types.DeliveryPlanned)) {
		t.Helper()
		got, err := queue.Claim(p.ctx, 5, time.Minute)
		if err != nil || len(got) != 3 {
			t.Fatalf("%s: Go's Claim = %+v, %v, want the three messages", round, got, err)
		}
		for _, c := range got {
			id := c.Message.OrderId.ToUUID().String()
			_, w := want(id)
			w.OrderId = c.Message.OrderId
			if !reflect.DeepEqual(c.Message, w) {
				t.Errorf("%s: Go read %s as %+v, want %+v", round, id, c.Message, w)
			}
			if err := queue.Release(p.ctx, c.ID, c.Token); err != nil {
				t.Fatal(err)
			}
		}
		for _, side := range []string{"TypeScript", "Rust"} {
			claims := p.printed(side, 5)
			if len(claims) != 3 {
				t.Fatalf("%s: the %s claim = %+v, want the three messages", round, side, claims)
			}
			for _, c := range claims {
				id := p.typedDelivery(side, c.Message).OrderId.ToUUID().String()
				w, _ := want(id)
				message := map[string]any{}
				for k, v := range c.Message {
					if k != "orderId" {
						message[k] = v
					}
				}
				if !reflect.DeepEqual(message, w) {
					t.Errorf("%s: the %s client read %s as %v, want %v", round, side, id, message, w)
				}
				p.must(side, "release", c.ID, c.Token)
			}
		}
	}
	check("as written", func(id string) (map[string]any, types.DeliveryPlanned) { return wantPrinted[id], wantGo[id] })

	// An interval written with days and months reads as their hours.
	if _, err := p.pool.Exec(p.ctx, "UPDATE delivery_planned_queue SET wait = '1 mon 1 day -01:00:00', grace = '-2 days'"); err != nil {
		t.Fatal(err)
	}
	check("with days and months", func(id string) (map[string]any, types.DeliveryPlanned) {
		printed := map[string]any{"wait": "743h0m0s", "grace": "-48h0m0s"}
		for k, v := range wantPrinted[id] {
			if k != "wait" && k != "grace" {
				printed[k] = v
			}
		}
		g := wantGo[id]
		g.Wait, g.Grace = types.TemporalDuration(743*time.Hour), duration(-48*time.Hour)
		return printed, g
	})
}

// typedDelivery reads a DeliveryPlanned message a side printed as Go's
// types read it.
func (p *parity) typedDelivery(side string, message map[string]any) types.DeliveryPlanned {
	p.t.Helper()
	data, err := json.Marshal(message)
	if err != nil {
		p.t.Fatal(err)
	}
	var m types.DeliveryPlanned
	if err := json.Unmarshal(data, &m); err != nil {
		p.t.Fatalf("Go's types read the %s message %s: %v", side, data, err)
	}
	return m
}
`
