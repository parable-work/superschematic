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
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
	"github.com/parable-work/superschematic/internal/generator/typegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// TestQueueParityWithTypeScriptOnPostgres runs the Go ORM's queue and the
// TypeScript queue client against one Postgres, on the queue OrderPlaced
// of fixture-queue-db, whose message holds a field of every kind a column
// takes (D53). The Go ORM enqueues a message, which the TypeScript client
// claims and reads field for field; the Go ORM fails that claim under the
// TypeScript token; the TypeScript client enqueues a message, which the Go
// ORM claims and reads back, its enum's default included; and the
// TypeScript client releases and completes claims the Go ORM made. Both
// run the statements sqlutil.PostgresQueueStatements writes, so each sees
// the other's states, attempts and tokens. It needs Bun, the HTTP runtime's
// build, and the database at SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL, and
// skips without the database.
func TestQueueParityWithTypeScriptOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the Go and TypeScript queues against one Postgres")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun not available: %v", err))
	}
	paths := testpaths.Local(t)
	for _, built := range []string{filepath.Join(paths.HTTPRuntimeTypeScript, "dist", "postgres.js"), filepath.Join(paths.ScalarTypeScript, "dist")} {
		if _, err := os.Stat(built); err != nil {
			testpaths.RequireOrSkipTS(t, fmt.Sprintf("the TypeScript runtimes are not built (bun run build in runtime/http/typescript): %v", err))
		}
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, queueFixture))
	if err != nil {
		t.Fatalf("load %s: %v", queueFixture, err)
	}
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
	ormOutput, err := Generate(schema, Options{SchemaName: queueFixture, ModulePath: "example.com/schemas/orm/" + queueFixture, TypesModule: typesModule, Clock: clock})
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
	tsOutput, err := tsgen.Generate(schema, tsgen.Options{SchemaName: queueFixture, Clock: clock})
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

	ddl, err := sqlgen.Generate(schema, sqlgen.Options{SchemaName: queueFixture})
	if err != nil {
		t.Fatalf("generate ddl: %v", err)
	}
	ddlDir := t.TempDir()
	if err := sqlgen.WriteDDL(ddl, ddlDir); err != nil {
		t.Fatal(err)
	}
	t.Setenv("QUEUE_PARITY_BUN", bun)
	t.Setenv("QUEUE_PARITY_SCRIPT", filepath.Join(scriptDir, "parity.ts"))
	out := runGeneratedORMModule(t, ormDir, filepath.Join(ddlDir, "create.sql"), "parity_test.go", queueParityORMTest)
	if !strings.Contains(out, "--- PASS: TestQueueParityWithTypeScript") {
		t.Fatal("the generated parity test did not pass")
	}
}

// queueParityScript runs one step of the TypeScript queue client on the
// database its second argument names, and prints its result as JSON.
const queueParityScript = `import pg from 'pg';
import { enqueue, postgresQueue } from '@superschematic/http-runtime/postgres';
import { orderPlacedQueue } from '@TYPES@/queues';

const [step, url, ...args] = process.argv.slice(2);
const pool = new pg.Pool({ connectionString: url, max: 1 });
try {
  const queue = postgresQueue(pool, orderPlacedQueue);
  let result: unknown = null;
  switch (step) {
    case 'claim':
      result = await queue.claim(Number(args[0]), 60);
      break;
    case 'enqueue': {
      const client = await pool.connect();
      try {
        await client.query('BEGIN');
        await enqueue(client, orderPlacedQueue, JSON.parse(args[0]));
        await client.query('COMMIT');
      } finally {
        client.release();
      }
      break;
    }
    case 'fail':
      result = await queue.fail(args[0], args[1], args[2]);
      break;
    case 'release':
      await queue.release(args[0], args[1]);
      break;
    case 'complete':
      await queue.complete(args[0], args[1]);
      break;
    default:
      throw new Error('no step ' + step);
  }
  console.log(JSON.stringify(result));
} catch (error) {
  console.error(error instanceof Error ? error.name + ': ' + error.message : String(error));
  process.exitCode = 1;
} finally {
  await pool.end();
}
`

const queueParityORMTest = `package orm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	scalars "github.com/parable-work/superscalar/go"
	types "example.com/schemas/types/go/fixture-queue-db"
)

// tsClaim is a claim the TypeScript client printed: its message as the
// TypeScript types hold it, a timestamp as its ISO text.
type tsClaim struct {
	ID      string ` + "`json:\"id\"`" + `
	Token   string ` + "`json:\"token\"`" + `
	Attempt int    ` + "`json:\"attempt\"`" + `
	Message map[string]any ` + "`json:\"message\"`" + `
}

func TestQueueParityWithTypeScript(t *testing.T) {
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
	queue := db.OrderPlacedQueue()

	// The TypeScript client reaches the same schema.
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("options", "-c search_path="+schema+",public")
	u.RawQuery = q.Encode()
	run := func(step string, args ...string) (string, error) {
		cmd := exec.Command(os.Getenv("QUEUE_PARITY_BUN"), append([]string{os.Getenv("QUEUE_PARITY_SCRIPT"), step, u.String()}, args...)...)
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	ts := func(step string, args ...string) string {
		t.Helper()
		out, err := run(step, args...)
		if err != nil {
			t.Fatalf("the TypeScript client's %s: %v\n%s", step, err, out)
		}
		return out
	}
	tsClaims := func(n int) []tsClaim {
		t.Helper()
		var claims []tsClaim
		if err := json.Unmarshal([]byte(ts("claim", fmt.Sprint(n))), &claims); err != nil {
			t.Fatal(err)
		}
		return claims
	}
	stateOf := func(id string) (string, int, string) {
		t.Helper()
		var s, lastError string
		var attempts int
		if err := pool.QueryRow(ctx, "SELECT state, attempts, COALESCE(last_error, '') FROM order_placed_queue WHERE id = $1::uuid", id).Scan(&s, &attempts, &lastError); err != nil {
			t.Fatal(err)
		}
		return s, attempts, lastError
	}
	due := func() {
		t.Helper()
		if _, err := pool.Exec(ctx, "UPDATE order_placed_queue SET due_at = now() - interval '1 second' WHERE state = 'ready'"); err != nil {
			t.Fatal(err)
		}
	}

	// Go enqueues; TypeScript claims and reads every field.
	placed := scalars.TemporalDateTime(time.Date(2026, 10, 9, 12, 30, 15, 250000000, time.UTC))
	id, _ := scalars.ParseUUID("6f1c2f2e-7d1a-4b4a-9d43-3c7a1b2c3d4e")
	msg := &types.OrderPlaced{
		OrderId:  id,
		PlacedAt: placed,
		Priority: types.Priority_High,
		Quantity: 9007199254740991,
		Note:     "leave it by the door",
		ShipTo:   types.Address{Line1: "1 Main St", City: "Springfield"},
		Tags:     []string{"gift", "fragile"},
	}
	if err := db.Transaction(ctx, func(tx TxInterface) error { return EnqueueOrderPlaced(ctx, tx, msg) }); err != nil {
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
	if s, attempts, lastError := stateOf(fromTS.ID); s != "ready" || attempts != 1 || lastError != "failed in Go" {
		t.Fatalf("after Go's Fail: %s, %d attempts, %q", s, attempts, lastError)
	}
	if out, err := run("complete", fromTS.ID, fromTS.Token); err == nil || !strings.Contains(out, "ClaimLostError") {
		t.Fatalf("the TypeScript complete under the token Go's Fail ended = %v, want a lost claim:\n%s", err, out)
	}
	if s, _, _ := stateOf(fromTS.ID); s != "ready" {
		t.Fatalf("a complete under a lost token changed the message: %s", s)
	}

	// TypeScript enqueues, with the enum unset and a timestamp as its ISO
	// text; Go claims and reads it, the enum's default included.
	ts("enqueue", ` + "`" + `{"orderId":"0c7a0e14-6a43-4f0e-8f1b-5d6c7e8f9a0b","placedAt":"2026-10-10T08:00:00.5Z","quantity":7,"shipTo":{"line1":"2 Elm St","city":"Shelbyville"},"tags":["from-ts"]}` + "`" + `)
	due()
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
	tsID, _ := scalars.ParseUUID("0c7a0e14-6a43-4f0e-8f1b-5d6c7e8f9a0b")
	if m.OrderId != tsID || !time.Time(m.PlacedAt).Equal(time.Date(2026, 10, 10, 8, 0, 0, 500000000, time.UTC)) ||
		m.Priority != types.Priority_Low || m.Quantity != 7 || m.Note != "" || m.ShipTo != (types.Address{Line1: "2 Elm St", City: "Shelbyville"}) || !reflect.DeepEqual(m.Tags, []string{"from-ts"}) {
		t.Fatalf("Go read the TypeScript message as %+v", m)
	}

	// TypeScript releases one Go claim, uncounted, and completes the other.
	ts("release", mine.ID, mine.Token)
	if s, attempts, _ := stateOf(mine.ID); s != "ready" || attempts != 1 {
		t.Fatalf("after the TypeScript release: %s after %d attempts, want ready after 1", s, attempts)
	}
	ts("complete", theirs.ID, theirs.Token)
	if s, _, _ := stateOf(theirs.ID); s != "done" {
		t.Fatalf("after the TypeScript complete: %s, want done", s)
	}
	if err := queue.Complete(ctx, theirs.ID, theirs.Token); err == nil {
		t.Fatal("Go completed a message TypeScript completed")
	}

	// Each fails the other's claims until the retries, three, are spent:
	// the fourth failure, TypeScript's, leaves it dead.
	for attempt := 2; attempt <= 4; attempt++ {
		due()
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
	if s, attempts, lastError := stateOf(mine.ID); s != "dead" || attempts != 4 || lastError != "try 4" {
		t.Fatalf("after the last failure: %s, %d attempts, %q", s, attempts, lastError)
	}
}
`
