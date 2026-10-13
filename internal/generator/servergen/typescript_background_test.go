package servergen_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/pgtest"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// queueDatabaseVariable names the Postgres the TypeScript queue's tests run
// on. They share the Go ORM's queue tests' variable, which CI sets, and
// skip without it.
const queueDatabaseVariable = "SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL"

// sqlScript runs one query string against the database its first argument
// names and prints the last statement's rows as JSON. The tests reach
// Postgres through it, with the pg the workspace installs, since this
// module depends on no Postgres driver.
const sqlScript = `import pg from 'pg';

const [url, text] = process.argv.slice(2);
const client = new pg.Client({ connectionString: url });
await client.connect();
try {
  const result = await client.query(text);
  const last = Array.isArray(result) ? result[result.length - 1] : result;
  console.log(JSON.stringify(last?.rows ?? []));
} finally {
  await client.end();
}
`

// lockedBuffer collects a process's output as it writes it.
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

// queueRow is a row of cart_abandoned_queue as the tests read it.
type queueRow struct {
	SKU       string  `json:"sku"`
	State     string  `json:"state"`
	Attempts  int     `json:"attempts"`
	Token     *string `json:"claim_token"`
	LastError *string `json:"last_error"`
}

// TestTypeScriptJobAndWorkerRunOnPostgres: ts-shop's job and worker
// packages install in the output root's workspace, type-check, and run
// under Bun on the variables the stack derives, against a Postgres schema
// that holds ts-db's tables and its queue's (D52, D53).
//
//   - The job, SweepCarts, builds ts-shop's Deps, enqueues a message per
//     SKU in a transaction of its own that commits, after one in a
//     transaction that rolls back, which leaves nothing, and logs job
//     started, then job done, and exits 0. Told to fail, it logs job failed
//     with the error, and exits 1.
//   - The worker, RemindShoppers, claims them four at a time, as
//     WORKER_CONCURRENCY says: a message its handler handles is done; one
//     that fails once is due again after the backoff and done on its
//     second attempt; one that fails every time is dead after its one
//     retry, with its error. On SIGTERM it claims no more, gives the
//     handler that holds its message the two seconds of its grace, then
//     aborts its signal and releases its message, ready again with its
//     attempt not counted, and exits 0.
func TestTypeScriptJobAndWorkerRunOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	dsn := os.Getenv(queueDatabaseVariable)
	if dsn == "" {
		t.Skipf("set %s to run a TypeScript job and worker against Postgres", queueDatabaseVariable)
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun not available: %v", err))
	}
	paths := testpaths.Local(t)
	installTypeScriptRuntime(t, bun, paths)
	repoRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := loadTypeScriptFixture(t)
	f.build(t, repoRoot, paths, append([]string{"ts-db"}, append(slices.Clone(tsAPIs), "ts-stack")...)...)
	names := naming.Default()
	for service, code := range map[string]string{"ts-pricing": tsPricingImplementation, "ts-shop": tsShopImplementation} {
		if err := os.WriteFile(filepath.Join(names.TypeScriptImplementationDir(repoRoot, service), "index.ts"), []byte(code), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(repoRoot, "schemas", "dist")
	install := exec.Command(bun, "install")
	install.Dir = out
	if output, err := install.CombinedOutput(); err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun install failed at the output root (likely offline): %v\n%s", err, output))
	}
	tsc := filepath.Join(paths.HTTPRuntimeTypeScript, "node_modules", ".bin", "tsc")
	jobDir := servergen.ServerDir(out, "ts-stack", tsSweepCarts)
	workerDir := servergen.ServerDir(out, "ts-stack", tsRemindShoppers)
	for _, dir := range []string{jobDir, workerDir, names.TypeScriptImplementationDir(repoRoot, "ts-shop")} {
		check := exec.Command(tsc, "--noEmit", "-p", "tsconfig.json")
		check.Dir = dir
		if output, err := check.CombinedOutput(); err != nil {
			t.Fatalf("tsc in %s: %v\n%s", dir, err, output)
		}
	}

	// ts-db's tables and its queue's, in a schema of the test's own. The
	// script sits in the worker's package, which depends on pg.
	script := filepath.Join(workerDir, "queue-sql.ts")
	if err := os.WriteFile(script, []byte(sqlScript), 0o644); err != nil {
		t.Fatal(err)
	}
	query := func(url, text string) string {
		t.Helper()
		cmd := exec.Command(bun, script, url, text)
		cmd.Dir = workerDir
		output, err := cmd.Output()
		if err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				output = append(output, exit.Stderr...)
			}
			t.Fatalf("query %q: %v\n%s", text, err, output)
		}
		return string(output)
	}
	schema := fmt.Sprintf("ts_queue_%d", time.Now().UnixNano())
	query(dsn, "CREATE SCHEMA "+schema)
	t.Cleanup(func() {
		cmd := exec.Command(bun, script, dsn, "DROP SCHEMA "+schema+" CASCADE")
		cmd.Dir = workerDir
		_ = cmd.Run()
	})
	db := withSearchPath(t, dsn, schema)
	ddl, err := sqlgen.Generate(f.schemas["ts-db"], sqlgen.Options{SchemaName: "ts-db"})
	if err != nil {
		t.Fatal(err)
	}
	sqlDir := t.TempDir()
	if err := sqlgen.WriteDDL(ddl, sqlDir); err != nil {
		t.Fatal(err)
	}
	createSQL, err := os.ReadFile(filepath.Join(sqlDir, "create.sql"))
	if err != nil {
		t.Fatal(err)
	}
	query(db, string(pgtest.CreateSQL(createSQL)))
	rows := func() map[string]queueRow {
		t.Helper()
		var list []queueRow
		if err := json.Unmarshal([]byte(query(db, "SELECT sku, state, attempts, claim_token, last_error FROM cart_abandoned_queue ORDER BY sku")), &list); err != nil {
			t.Fatal(err)
		}
		byKey := map[string]queueRow{}
		for _, r := range list {
			byKey[r.SKU] = r
		}
		return byKey
	}

	// The variables the stack derives for ts-shop: its database, the
	// callee it holds a client of, and its bucket, which neither the job
	// nor the worker reaches here.
	env := append([]string{"TS_DB_DATABASE_URL=" + db}, variables(t, "TS_PRICING_SERVICE", ir.ServiceEndpoint{URL: "http://127.0.0.1:9"})...)
	env = append(env, variables(t, "TS_MEDIA_BUCKET", ir.BucketConnection{Name: "ts-media", Endpoint: "http://127.0.0.1:9"})...)
	run := func(dir string, extra ...string) (string, error) {
		cmd := exec.Command(bun, servergen.TypeScriptMainFile)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), append(slices.Clone(env), extra...)...)
		output, err := cmd.CombinedOutput()
		return string(output), err
	}

	output, err := run(jobDir, "SWEEP_SKUS=tea,flaky,poison,slow")
	if err != nil {
		t.Fatalf("the job failed: %v\n%s", err, output)
	}
	for _, want := range []string{`"msg":"job started"`, `"api":"ts-shop","class":"SweepCarts"`, `"msg":"swept"`, `"msg":"job done"`, `"job":"ts-shop-sweep-carts"`} {
		if !strings.Contains(output, want) {
			t.Errorf("the job's output lacks %s:\n%s", want, output)
		}
	}
	enqueued := rows()
	if len(enqueued) != 4 || enqueued["tea"].State != ir.QueueStateReady || enqueued["tea"].Attempts != 0 {
		t.Fatalf("the job enqueued %v, want tea, flaky, poison and slow ready, and nothing rolled back", enqueued)
	}
	output, err = run(jobDir, "SWEEP_FAIL=1")
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(output, `"msg":"job failed"`) || !strings.Contains(output, `"error":"the sweep failed"`) {
		t.Errorf("a failing job = %v, want exit status 1 with job failed and its error:\n%s", err, output)
	}

	worker := exec.Command(bun, servergen.TypeScriptMainFile)
	worker.Dir = workerDir
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
			t.Fatalf("the worker exited %v on SIGTERM, want 0:\n%s", err, logs.String())
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
		`"api":"ts-shop","class":"RemindShoppers","queue":"CartAbandoned","concurrency":4`,
		`"tags":"abandoned+tea","region":"eu"`,
		`"msg":"message handled"`,
		`"msg":"message failed and will be retried"`,
		`"msg":"message failed and is dead: its retries are spent"`,
		`"msg":"stopping: letting the running handlers finish"`,
		`"queue":"CartAbandoned","running":1,"grace":2`,
		`"msg":"worker stopped: the grace is over, and the unfinished messages are released"`,
		`"queue":"CartAbandoned","released":1,"unfinished":1`,
		`"worker":"ts-shop-remind-shoppers"`,
	} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("the worker's output lacks %s:\n%s", want, logs.String())
		}
	}
}

// withSearchPath is the connection string dsn with the search path
// schema,public, which pg sends as a startup option.
func withSearchPath(t *testing.T, dsn, schema string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("options", "-c search_path="+schema+",public")
	u.RawQuery = q.Encode()
	return u.String()
}
