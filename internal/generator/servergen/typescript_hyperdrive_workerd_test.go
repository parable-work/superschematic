package servergen_test

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/generator/tsrestgen"
	"github.com/parable-work/superschematic/internal/pgtest"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// tsLedgerImplementation implements ts-ledger over its Deps: its
// repositories, the queue SyncRequested's definition, which requestSync
// enqueues in its transaction, and raw SQL for what state reports. Its
// embedding worker writes each line item's embedding, a vector its
// category's slug gives in place of a model's; its sync worker records each
// run, waits half a second, enqueues a's second sync from the first's run,
// fails flaky's first attempt and every attempt of poison.
const tsLedgerImplementation = `import { enqueue } from '@superschematic/http-runtime/postgres';
import { syncRequestedQueue } from '@schemas/ts-ledger-db-types/queues';
import type { IdentityUUID } from '@schemas/ts-ledger-db-types';
import type { LineItemRow } from '@schemas/ts-ledger-db-types/orm';
import type { Constructor, Deps, LedgerImplementation, WorkersConstructor } from '@schemas/ts-ledger-api';

/** A category's embedding, by its slug, in three dimensions. */
function embedding(slug: string): number[] {
  if (slug === 'groceries') return [1, 0, 0];
  if (slug === 'travel') return [0, 1, 0];
  return [0, 0, 1];
}

export const create: Constructor = deps => ({
  ledger: ledger(deps),
  // The identity runtime has let the caller in only when they hold ledger.close.
  books: { closeBooks: async () => ({ closed: true }) },
});

function ledger(deps: Deps): LedgerImplementation {
  const categoryOf = async (slug: string | undefined) => (slug ? ((await deps.orm.category.findOne({ slug: { eq: slug } }))?.id ?? null) : null);
  const view = async (row: LineItemRow) => ({
    id: row.id,
    name: row.name,
    ...(row.categoryId ? { category: (await deps.orm.category.getOne(row.categoryId)).slug } : {}),
  });
  const rows = async (text: string) => (await deps.db.query(text)).rows;
  return {
    async createCategory({ slug, name }) {
      const row = await deps.orm.category.createOne({ slug, name });
      return { id: row.id, slug: row.slug, name: row.name };
    },
    async createLineItem({ name, category }) {
      return view(await deps.orm.lineItem.createOne({ name, categoryId: await categoryOf(category) }));
    },
    async setCategory({ id, category }) {
      return view(await deps.orm.lineItem.updateOne(id as IdentityUUID, { categoryId: await categoryOf(category) }));
    },
    async neighbours({ vector }) {
      const near = await deps.orm.lineItemEmbedding.nearest('embedding', vector.split(',').map(Number), { k: 10 });
      const names: string[] = [];
      for (const n of near) names.push((await deps.orm.lineItem.getOne(n.row.lineItemId)).name);
      return { names };
    },
    async requestSync({ itemId, reason, rollback }) {
      try {
        await deps.orm.transaction(async tx => {
          await enqueue(tx, syncRequestedQueue, { itemId, reason });
          if (rollback) throw new Error('rolled back');
        });
      } catch (error) {
        if (rollback) return { itemId, enqueued: false };
        throw error;
      }
      return { itemId, enqueued: true };
    },
    async state() {
      return {
        json: JSON.stringify({
          embedQueue: await rows('SELECT line_item_id::text AS key, state, attempts FROM embed_line_item_queue ORDER BY created_at, id'),
          syncQueue: await rows('SELECT item_id AS key, reason, state, attempts, last_error FROM sync_requested_queue ORDER BY created_at, id'),
          embeddings: (await deps.orm.lineItemEmbedding.findMany()).total,
          runs: await rows('SELECT item_id, reason, attempt, extract(epoch FROM started_at) * 1000 AS started, extract(epoch FROM ended_at) * 1000 AS ended FROM sync_run ORDER BY started_at'),
        }),
      };
    },
  };
}

export const createWorkers: WorkersConstructor = deps => ({
  async embedLineItems(message) {
    const item = await deps.orm.lineItem.getOne(message.lineItemId);
    if (!item.categoryId) return;
    const category = await deps.orm.category.getOne(item.categoryId);
    await deps.orm.lineItemEmbedding.upsertOne(
      { lineItemId: item.id, categoryId: category.id, embedding: embedding(category.slug) },
      ['lineItem'],
      { categoryId: category.id, embedding: embedding(category.slug) }
    );
    deps.logger.info('embedded', { lineItem: item.name, category: category.slug });
  },
  async syncItems(message) {
    const { itemId, reason } = message;
    const attempt = (await deps.orm.syncRun.findMany({ itemId: { eq: itemId }, reason: { eq: reason } })).total + 1;
    const run = await deps.orm.syncRun.createOne({ itemId, reason, attempt, startedAt: new Date() });
    if (reason === 'chain') {
      // While a's sync is claimed, its next waits: one claim per item.
      await deps.orm.transaction(tx => enqueue(tx, syncRequestedQueue, { itemId, reason: 'chained' }));
    }
    await new Promise(resolve => setTimeout(resolve, 500));
    await deps.orm.syncRun.updateOne(run.id, { endedAt: new Date() });
    deps.logger.info('synced', { itemId, reason, attempt });
    if (reason === 'poison') throw new Error('poison every time');
    if (reason === 'flaky' && attempt === 1) throw new Error('flaky the first time');
  },
});
`

// ledgerIdentityScript registers or signs in a member of ts-ledger on Bun,
// over the database the Workers use, with the identity runtime's store and
// node:crypto's argon2, and says which argon2 that is: a member moves
// between a server on Bun and one on Workers with one password hash (D92).
// It registers with open registration and no roles, so the first member
// who registers through the Worker is still its administrator.
const ledgerIdentityScript = `import * as nodeCrypto from 'node:crypto';
import pg from 'pg';
import { IdentityService, postgresIdentityStore } from '@superschematic/http-runtime/identity';
import { identityDescriptor } from '@schemas/ts-ledger-db-types/identity';

const [url, op, login, password] = process.argv.slice(2);
const pool = new pg.Pool({ connectionString: url, max: 2 });
try {
  const argon2 = typeof (nodeCrypto as unknown as { argon2?: unknown }).argon2 === 'function' ? "node:crypto's argon2" : 'no argon2 in node:crypto';
  const service = new IdentityService({ store: postgresIdentityStore(pool, identityDescriptor) });
  if (op === 'register') {
    const registered = await service.register({ login, password });
    console.log('registered ' + registered.user.login + ' with ' + argon2);
  } else {
    const session = await service.login({ login, password });
    console.log('signed in ' + session.result.user.login + ' with ' + argon2);
  }
} finally {
  await pool.end();
}
`

// TestTypeScriptHyperdriveUnderWorkerd: ts-ledger-stack's packages install
// in the output root's workspace with ts-ledger's implementation and
// type-check, fetch.ts and both drain.ts among them; each bundles for
// workerd with Node's modules; and under the runtime's pinned workerd,
// with a hyperdrive binding to a Postgres with pgvector, in a database of
// the test's own that holds ts-ledger-db's tables, queues and trigger
// (testdata/workerd/ledger, D92):
//
//   - the server answers /readyz through Hyperdrive, and its repositories,
//     transactions and nearest work unchanged: line items written through
//     them enqueue their embeddings by LineItem's trigger, one waiting per
//     line item, and a sync enqueued in a transaction commits with it, or
//     rolls back with it;
//   - at a firing, EmbedLineItems drains its queue, so nearest finds each
//     line item by its category's vector;
//   - at a firing, SyncItems handles two items at once and one claim per
//     item, retries a sync that fails once after the backoff, and marks one
//     that always fails dead;
//   - the identity runtime serves ts-ledger's members through Hyperdrive,
//     hashing with its portable argon2id: a member a server on Bun
//     registered with node:crypto's argon2 signs in with a cookie; the
//     first member who registers is the administrator, who approves the
//     second and grants him the role a permission-gated route needs; and
//     after the run, the server on Bun signs in a member whose hash the
//     Worker wrote.
func TestTypeScriptHyperdriveUnderWorkerd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	dsn := pgtest.VectorDatabaseURL(t)
	bun, err := exec.LookPath("bun")
	if err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun not available: %v", err))
	}
	paths := testpaths.Local(t)
	wasm := filepath.Join(paths.ScalarTypeScript, "dist", "workerd", "superscalar_wasm_bg.wasm")
	if _, err := os.Stat(wasm); err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("superscalar's workerd build is not built (scripts/superscalar-dep.sh builds it): %v", err))
	}
	workerd := filepath.Join(paths.HTTPRuntimeTypeScript, "test", "workerd", "node_modules", ".bin", "workerd")
	if _, err := os.Stat(workerd); err != nil {
		testpaths.RequireOrSkipTS(t, "workerd is not installed: run bun install --frozen-lockfile in runtime/http/typescript/test/workerd")
	}
	installTypeScriptRuntime(t, bun, paths)
	repoRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := loadTypeScriptLedgerFixture(t)
	f.build(t, repoRoot, paths, tsLedgerOrder...)
	impl := naming.Default().TypeScriptImplementationDir(repoRoot, "ts-ledger")
	if err := os.WriteFile(filepath.Join(impl, tsrestgen.ImplementationFile), []byte(tsLedgerImplementation), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(repoRoot, "schemas", "dist")
	install := exec.Command(bun, "install")
	install.Dir = out
	if output, err := install.CombinedOutput(); err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun install failed at the output root (likely offline): %v\n%s", err, output))
	}
	tsc := filepath.Join(paths.HTTPRuntimeTypeScript, "node_modules", ".bin", "tsc")
	deployables := []string{"ts-ledger", tsEmbedLineItems, tsSyncItems}
	dirs := []string{impl}
	for _, d := range deployables {
		dirs = append(dirs, servergen.ServerDir(out, "ts-ledger-stack", d))
	}
	for _, dir := range dirs {
		check := exec.Command(tsc, "--noEmit", "-p", "tsconfig.json")
		check.Dir = dir
		if output, err := check.CombinedOutput(); err != nil {
			t.Fatalf("tsc in %s: %v\n%s", dir, err, output)
		}
	}
	run := t.TempDir()
	for _, d := range deployables {
		bundle := exec.Command(bun, "bundle.ts", filepath.Join(run, d))
		bundle.Dir = servergen.ServerDir(out, "ts-ledger-stack", d)
		if output, err := bundle.CombinedOutput(); err != nil {
			t.Fatalf("bun bundle.ts in %s: %v\n%s", d, err, output)
		}
	}

	// A database of the test's own, with ts-ledger-db's tables, queues and
	// trigger, and pgvector's extension. The script sits in the server's
	// package, which depends on pg.
	server, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	database := fmt.Sprintf("ts_ledger_%d", time.Now().UnixNano())
	pkg := servergen.ServerDir(out, "ts-ledger-stack", "ts-ledger")
	script := filepath.Join(pkg, "ledger-sql.ts")
	if err := os.WriteFile(script, []byte(sqlScript), 0o644); err != nil {
		t.Fatal(err)
	}
	query := func(target, text string) {
		t.Helper()
		cmd := exec.Command(bun, script, target, text)
		cmd.Dir = pkg
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("query %.60q: %v\n%s", text, err, output)
		}
	}
	query(dsn, "CREATE DATABASE "+database)
	t.Cleanup(func() {
		cmd := exec.Command(bun, script, dsn, "DROP DATABASE IF EXISTS "+database+" WITH (FORCE)")
		cmd.Dir = pkg
		_ = cmd.Run()
	})
	ledger := *server
	ledger.Path = "/" + database
	ddl, err := sqlgen.Generate(f.schemas["ts-ledger-db"], sqlgen.Options{SchemaName: "ts-ledger-db"})
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
	query(ledger.String(), string(pgtest.CreateSQL(createSQL)))

	// carol, registered on Bun, whose hash node:crypto's argon2 writes.
	identity := filepath.Join(pkg, "ledger-identity.ts")
	if err := os.WriteFile(identity, []byte(ledgerIdentityScript), 0o644); err != nil {
		t.Fatal(err)
	}
	member := func(op, login, password string) string {
		t.Helper()
		cmd := exec.Command(bun, identity, ledger.String(), op, login, password)
		cmd.Dir = pkg
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %s on Bun: %v\n%s", op, login, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	if got, want := member("register", "carol@example.com", "carol registered on bun"), "registered carol@example.com with node:crypto's argon2"; got != want {
		t.Fatalf("carol's registration on Bun said %q, want %q", got, want)
	}

	// The Workers, their bundles, superscalar's module and the tester, with
	// the database filled in.
	password, _ := server.User.Password()
	config, err := os.ReadFile(filepath.Join("testdata", "workerd", "ledger", "ledger.capnp"))
	if err != nil {
		t.Fatal(err)
	}
	filled := strings.NewReplacer(
		"$PG_ADDRESS", ledgerCapnpText(server.Host),
		"$PG_DATABASE", database,
		"$PG_USER", ledgerCapnpText(server.User.Username()),
		"$PG_PASSWORD", ledgerCapnpText(password),
	).Replace(string(config))
	if err := os.WriteFile(filepath.Join(run, "ledger.capnp"), []byte(filled), 0o644); err != nil {
		t.Fatal(err)
	}
	for src, dst := range map[string]string{
		filepath.Join("testdata", "workerd", "ledger", "tester.js"): filepath.Join(run, "tester.js"),
		wasm: filepath.Join(run, "superscalar_wasm_bg.wasm"),
	} {
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(workerd, "test", "ledger.capnp")
	cmd.Dir = run
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatal(err)
	}
	if err != nil || testing.Verbose() {
		t.Logf("workerd test ledger.capnp:\n%s", output)
	}
	if err != nil {
		t.Fatalf("workerd test ledger.capnp exited %v", err)
	}
	for _, want := range []string{
		"the ledger under workerd: Postgres through Hyperdrive, nearest, an enqueue that commits with its transaction, and both workers drained at a firing",
		`"msg":"worker started","stack":"ts-ledger-stack","worker":"ts-ledger-sync-items","api":"ts-ledger","class":"SyncItems","queue":"SyncRequested","concurrency":2,"cron":"* * * * *","drainSeconds":6`,
		`"msg":"message failed and will be retried"`,
		`"msg":"message failed and is dead: its retries are spent"`,
		`"msg":"embedded","stack":"ts-ledger-stack","worker":"ts-ledger-embed-line-items","api":"ts-ledger","lineItem":"pen","category":"travel"`,
		"the ledger's members under workerd: a sign-in with a hash node:crypto wrote, the first member the administrator, an approval, cookie sessions and a permission-gated route",
	} {
		if !strings.Contains(string(output), want) {
			t.Errorf("workerd's output does not hold %s:\n%s", want, output)
		}
	}

	// ada's hash, which the Worker's portable argon2id wrote, verifies on
	// Bun with node:crypto's.
	if got, want := member("login", "ada@example.com", "ada the administrator"), "signed in ada@example.com with node:crypto's argon2"; got != want {
		t.Errorf("ada's sign-in on Bun said %q, want %q", got, want)
	}
}

// ledgerCapnpText is s as the contents of a Cap'n Proto text literal.
func ledgerCapnpText(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}
