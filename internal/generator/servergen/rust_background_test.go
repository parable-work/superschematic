package servergen_test

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator"
	ir "github.com/parable-work/superschematic/ir"
)

// queueRowsTool prints, as JSON, the rows a query returns: queuerows url
// query. It runs in pgxModule's module, beside identitySchema's tool, since
// this module depends on no Postgres driver.
const queueRowsTool = `package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"
)

func main() {
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer conn.Close(ctx)
	rows, err := conn.Query(ctx, os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	list, err := pgx.CollectRows(rows, pgx.RowToMap)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if list == nil {
		list = []map[string]any{}
	}
	if err := json.NewEncoder(os.Stdout).Encode(list); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
`

// stockLowRow is a row of rs-db's stock_low_queue as the test reads it.
type stockLowRow struct {
	SKU       string  `json:"sku"`
	State     string  `json:"state"`
	Attempts  int     `json:"attempts"`
	Token     *string `json:"claim_token"`
	LastError *string `json:"last_error"`
}

// TestRustJobAndWorkerRunOnPostgres: rs-stock's job and worker crates build
// in the output root's workspace with the servers, and run on the
// variables the stack derives for rs-stock, less its callers field, which
// neither is given, against a Postgres schema that holds rs-db's tables
// and its queue's (D65), as TestTypeScriptJobAndWorkerRunOnPostgres runs
// TypeScript's.
//
//   - The job, PlaceRestockOrders, builds rs-stock's Deps, enqueues a
//     StockLow message per SKU in a transaction of its own that commits,
//     after one in a transaction that rolls back, which leaves nothing, and
//     logs job started, then job done, and exits 0.
//   - The worker, ReorderStock, claims them four at a time, as
//     WORKER_CONCURRENCY says: a message its handler handles is done; one
//     that fails once is due again after the backoff and done on its
//     second attempt; one that fails every time is dead after its one
//     retry, with its error. On SIGTERM it claims no more, gives the
//     handler that holds its message the two seconds of its grace, then
//     drops it and releases its message, ready again with its attempt not
//     counted, and exits 0.
func TestRustJobAndWorkerRunOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo build in -short mode")
	}
	dsn := os.Getenv(queueDatabaseVariable)
	if dsn == "" {
		t.Skipf("set %s to run a Rust job and worker against Postgres", queueDatabaseVariable)
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust build")
	}
	repoRoot, bin := buildRustFixture(t, cargo)
	out := filepath.Join(repoRoot, "schemas", "dist")

	// rs-db's tables and its queue's, in a schema of the test's own.
	module := pgxModule(t)
	databaseURL := identitySchema(t, module, dsn, filepath.Join(generator.SQLDir(out, "rs-db"), "create.sql"))
	tool := filepath.Join(module, "internal", "queuerows")
	if err := os.MkdirAll(tool, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tool, "main.go"), []byte(queueRowsTool), 0o644); err != nil {
		t.Fatal(err)
	}
	queueRows := filepath.Join(t.TempDir(), "queuerows")
	goCommand(t, module, "build", "-o", queueRows, "./internal/queuerows")
	rows := func() map[string]stockLowRow {
		t.Helper()
		output, err := exec.Command(queueRows, databaseURL, "SELECT sku, state, attempts, claim_token::text AS claim_token, last_error FROM stock_low_queue ORDER BY sku").CombinedOutput()
		if err != nil {
			t.Fatalf("read stock_low_queue: %v\n%s", err, output)
		}
		var list []stockLowRow
		if err := json.Unmarshal(output, &list); err != nil {
			t.Fatalf("read stock_low_queue: %v\n%s", err, output)
		}
		byKey := map[string]stockLowRow{}
		for _, r := range list {
			byKey[r.SKU] = r
		}
		return byKey
	}

	// The variables the stack derives for rs-stock's job and worker: its
	// database and the callee it holds a client of, which neither reaches
	// here, and its setting.
	env := append([]string{"RS_DB_DATABASE_URL=" + libpqSearchPath(t, databaseURL), "WAREHOUSE=north"},
		variables(t, "RS_PRICING_SERVICE", ir.ServiceEndpoint{URL: "http://127.0.0.1:9"})...)
	cmd := exec.Command(filepath.Join(bin, rustStockJob))
	cmd.Env = append(os.Environ(), append(slices.Clone(env), "RESTOCK_SKUS=tea,flaky,poison,slow")...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the job failed: %v\n%s", err, output)
	}
	for _, want := range []string{`"msg":"job started"`, `"api":"rs-stock","class":"PlaceRestockOrders"`, `"msg":"job done"`, `"job":"` + rustStockJob + `"`} {
		if !strings.Contains(string(output), want) {
			t.Errorf("the job's output lacks %s:\n%s", want, output)
		}
	}
	enqueued := rows()
	if len(enqueued) != 4 || enqueued["tea"].State != ir.QueueStateReady || enqueued["tea"].Attempts != 0 {
		t.Fatalf("the job enqueued %v, want tea, flaky, poison and slow ready, and nothing rolled back", enqueued)
	}

	worker := exec.Command(filepath.Join(bin, rustStockWorker))
	worker.Env = append(os.Environ(), append(slices.Clone(env), "WORKER_CONCURRENCY=4")...)
	var logs lockedBuffer
	worker.Stdout, worker.Stderr = &logs, &logs
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- worker.Wait() }()
	t.Cleanup(func() { _ = worker.Process.Kill() })
	deadline := time.Now().Add(30 * time.Second)
	for {
		r := rows()
		if r["tea"].State == ir.QueueStateDone && r["flaky"].State == ir.QueueStateDone && r["poison"].State == ir.QueueStateDead && r["slow"].State == ir.QueueStateClaimed {
			break
		}
		select {
		case err := <-exited:
			t.Fatalf("the worker exited: %v\n%s", err, logs.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("the worker did not settle the messages: %v\n%s", r, logs.String())
		}
		time.Sleep(200 * time.Millisecond)
	}
	settled := rows()
	if settled["flaky"].Attempts != 2 || settled["poison"].Attempts != 2 || settled["poison"].LastError == nil || *settled["poison"].LastError != "poison every time" {
		t.Errorf("flaky took %d attempts and poison %d with %v; want two each, and poison's error kept", settled["flaky"].Attempts, settled["poison"].Attempts, settled["poison"].LastError)
	}

	stopped := time.Now()
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
	if took := time.Since(stopped); took < 2*time.Second {
		t.Errorf("the worker stopped %s after SIGTERM, before its grace of two seconds was over", took)
	}
	if slow := rows()["slow"]; slow.State != ir.QueueStateReady || slow.Attempts != 0 || slow.Token != nil {
		t.Errorf("the message the worker held at SIGTERM is %+v, want it released: ready, its attempt not counted", slow)
	}
	for _, want := range []string{
		`"msg":"worker started"`,
		`"api":"rs-stock","class":"ReorderStock","queue":"StockLow","concurrency":4`,
		`"msg":"message handled"`,
		`"msg":"message failed and will be retried"`,
		`"msg":"message failed and is dead: its retries are spent"`,
		`"msg":"shutting down"`,
		`"msg":"stopping: letting the running handlers finish"`,
		`"queue":"StockLow","running":1,"grace":2.0`,
		`"msg":"worker stopped: the grace is over, and the unfinished messages are released"`,
		`"queue":"StockLow","released":1,"unfinished":1`,
		`"worker":"` + rustStockWorker + `"`,
	} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("the worker's output lacks %s:\n%s", want, logs.String())
		}
	}
}
