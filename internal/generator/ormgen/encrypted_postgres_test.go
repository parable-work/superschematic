package ormgen

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// encryptedFixture is the fixture whose Encrypted<T> fields (D85) both
// ORMs seal and open.
const encryptedFixture = "fixture-vault-db"

// TestEncryptedFieldsAcrossGoAndTypeScriptOnPostgres holds the Go ORM and
// the TypeScript repositories of fixture-vault-db to one another on the
// Postgres at SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL (D85):
//
//   - each runs one script of creates, reads, updates, upserts by the
//     primary key and by itemId, one and many, which set the access token
//     by a second statement sealed under the row's key (D89), a reencrypt
//     and a delete in a schema of its own, recording every statement it
//     sends, and the two are the same, call by call;
//   - in one shared schema, Go writes a row, which is ciphertext there and
//     plaintext through TypeScript's repository; TypeScript writes another,
//     and rotates the key with reencrypt; and Go reads both under the new
//     key alone. Each side refuses a wrong key and no key with its typed
//     error.
//
// It needs Bun, builds the HTTP runtime, and skips without the database,
// or without Bun unless SUPERSCHEMATIC_REQUIRE_TS_CHECKS says it must run.
func TestEncryptedFieldsAcrossGoAndTypeScriptOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the Go and TypeScript repositories against one Postgres")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun not available: %v", err))
	}
	paths := testpaths.Local(t)
	testpaths.BuildHTTPRuntime(t, bun, paths)

	ormDir := generateORMModule(t, encryptedFixture)
	schema, err := loader.LoadService(filepath.Join(fixturesDir, encryptedFixture))
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	typesDir := filepath.Join(root, "types", "typescript", encryptedFixture)
	output, err := tsgen.Generate(schema, tsgen.Options{SchemaName: encryptedFixture, Clock: codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))})
	if err != nil {
		t.Fatalf("generate typescript types: %v", err)
	}
	if err := tsgen.SetScalarLibSpec(output, paths, typesDir); err != nil {
		t.Fatal(err)
	}
	if err := tsgen.WriteTypes(output, typesDir); err != nil {
		t.Fatal(err)
	}
	if err := (tsgen.WorkspaceRoot{OutputRoot: root, Paths: paths}).Write(); err != nil {
		t.Fatal(err)
	}
	install := exec.Command(bun, "install")
	install.Dir = root
	if out, err := install.CombinedOutput(); err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun install failed (likely offline): %v\n%s", err, out))
	}
	script := strings.ReplaceAll(encryptedScript, "@TYPES@", naming.Default().NpmTypesPackage(encryptedFixture))
	if err := os.WriteFile(filepath.Join(typesDir, "vault.ts"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ormDir, "vault_test.go"), []byte(encryptedORMTest), 0o644); err != nil {
		t.Fatal(err)
	}
	createSQL := filepath.Join(ormDir, "testdata", "create.sql")
	shared := fmt.Sprintf("vault_shared_%d", time.Now().UnixNano())
	exchange := filepath.Join(root, "exchange.json")
	t.Setenv("VAULT_SCHEMA", shared)
	t.Setenv("VAULT_EXCHANGE", exchange)
	ts := func(mode string, args ...string) {
		t.Helper()
		run := exec.Command(bun, append([]string{"vault.ts", mode, createSQL}, args...)...)
		run.Dir = typesDir
		if out, err := run.CombinedOutput(); err != nil {
			t.Fatalf("the TypeScript side's %s failed: %v\n%s", mode, err, out)
		}
	}
	goPhase := func(phase string) string {
		t.Helper()
		t.Setenv("VAULT_PHASE", phase)
		cmd := exec.Command("go", "test", "-count=1", "-v", "-run", "TestVault", "./...")
		cmd.Dir = ormDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("the Go side's %s failed: %v\n%s", phase, err, out)
		}
		return string(out)
	}

	// The statements, each side in a schema of its own.
	tsOut := filepath.Join(root, "statements.ts.json")
	goOut := filepath.Join(root, "statements.go.json")
	ts("parity", tsOut)
	t.Setenv("VAULT_PARITY_OUT", goOut)
	if out := runORMModule(t, ormDir); !strings.Contains(out, "--- PASS: TestVaultParity ") {
		t.Fatalf("the Go side's parity did not pass:\n%s", out)
	}
	read := func(path string) []parityCall {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var calls []parityCall
		if err := json.Unmarshal(data, &calls); err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		// A transaction's BEGIN and COMMIT are left out, pgx writing them in
		// lower case: an upsert that seals an update's field opens one (D89).
		for i := range calls {
			kept := calls[i].Statements[:0]
			for _, st := range calls[i].Statements {
				switch strings.ToUpper(strings.TrimSpace(st)) {
				case "BEGIN", "COMMIT", "ROLLBACK":
					continue
				}
				kept = append(kept, st)
			}
			calls[i].Statements = kept
		}
		return calls
	}
	goCalls, tsCalls := read(goOut), read(tsOut)
	if len(goCalls) != len(tsCalls) {
		t.Fatalf("Go made %d calls and TypeScript %d", len(goCalls), len(tsCalls))
	}
	statements := 0
	for i, g := range goCalls {
		if g.Name != tsCalls[i].Name || len(tsCalls[i].Statements) == 0 || strings.Join(g.Statements, "\n;\n") != strings.Join(tsCalls[i].Statements, "\n;\n") {
			t.Errorf("%s: the statements differ\nGo:\n%s\nTypeScript (%s):\n%s", g.Name, strings.Join(g.Statements, "\n"), tsCalls[i].Name, strings.Join(tsCalls[i].Statements, "\n"))
		}
		statements += len(g.Statements)
	}
	t.Logf("Go and TypeScript sent the same %d statements over %d calls", statements, len(goCalls))

	// One schema, both ORMs.
	if out := goPhase("write"); !strings.Contains(out, "--- PASS: TestVaultCross ") {
		t.Fatalf("the Go side's write did not pass:\n%s", out)
	}
	ts("cross")
	if out := goPhase("read"); !strings.Contains(out, "--- PASS: TestVaultCross ") {
		t.Fatalf("the Go side's read did not pass:\n%s", out)
	}
}

// encryptedScript is the TypeScript side. "parity" runs the parity script
// over the repositories of @TYPES@ in a schema of its own, with create.sql
// (its second argument) applied, and writes each call's statements as JSON
// to its third. "cross" reads the row the Go side wrote to VAULT_SCHEMA,
// whose id VAULT_EXCHANGE holds, writes one of its own, rotates the key,
// and records its id there for the Go side.
const encryptedScript = `import { readFileSync, writeFileSync } from 'node:fs';
import pg from 'pg';
import { FieldEncryptionError, FieldKeyring, uuidToDb, type OrmPool, type OrmQueryable } from '@superschematic/http-runtime/postgres';
import { createOrm } from '@TYPES@/orm';

const current = 'AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA=';
const next = 'ZWZnaGlqa2xtbm9wcXJzdHV2d3h5ent8fX5/gIGCg4Q=';
const [mode, createSQL, out] = process.argv.slice(2);
const dsn = process.env.SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL!;

function check(ok: boolean, what: string): void {
  if (!ok) throw new Error('check failed: ' + what);
}

async function reason(op: () => Promise<unknown>): Promise<string> {
  try {
    await op();
  } catch (error) {
    if (error instanceof FieldEncryptionError) return error.reason;
    throw error;
  }
  return 'none';
}

if (mode === 'parity') {
  const schema = 'vault_parity_ts_' + process.pid + '_' + Date.now();
  const admin = new pg.Client({ connectionString: dsn });
  await admin.connect();
  await admin.query('CREATE SCHEMA ' + schema);
  const base = new pg.Pool({ connectionString: dsn, options: '-c search_path=' + schema + ',public' });
  try {
    await base.query(readFileSync(createSQL!, 'utf8'));
    const calls: { name: string; statements: string[] }[] = [];
    const record = (q: OrmQueryable): OrmQueryable => ({
      query(config) {
        calls[calls.length - 1]!.statements.push(config.text);
        return q.query(config as never) as never;
      },
    });
    const pool: OrmPool = {
      ...record(base as unknown as OrmQueryable),
      async connect() {
        const client = await base.connect();
        return { ...record(client as unknown as OrmQueryable), release: (err?: Error | boolean) => client.release(err) };
      },
    };
    const call = async <T>(name: string, fn: () => Promise<T>): Promise<T> => {
      calls.push({ name, statements: [] });
      return fn();
    };
    // A soft delete stamps who made it, so the ORM acts as a user.
    const orm = createOrm(pool, { fieldKeys: current, user: 'TKbHsHDO6x9RCjbs5c2u4' });
    const first = await call('createItem', () => orm.plaidItem.createOne({ itemId: 'item-1', institution: 'First Bank', accessToken: 'access-1', webhookSecret: 'whsec-1' }));
    const [second] = await call('createItems', () =>
      orm.plaidItem.createMany([
        { itemId: 'item-2', institution: 'Second Bank', accessToken: 'access-2' },
        { itemId: 'item-3', institution: 'Third Bank', accessToken: 'access-3', webhookSecret: 'whsec-3' },
      ])
    );
    await call('createCredential', () => orm.apiCredential.createOne({ service: 'voyage', secret: 'pa-voyage' }));
    await call('getItem', () => orm.plaidItem.getOne(first.id));
    await call('getItems', () => orm.plaidItem.getManyByIds([first.id, second!.id]));
    await call('findItems', () => orm.plaidItem.findMany({ institution: { ilike: 'bank' } }, { orderBy: [{ field: 'itemId' }] }));
    await call('pageItems', () => orm.plaidItem.findPage(null, { limit: 2 }));
    await call('updateItem', () => orm.plaidItem.updateOne(first.id, { accessToken: 'access-1b', webhookSecret: null }));
    await call('upsertItem', () => orm.plaidItem.upsertOne({ id: first.id, itemId: 'item-1', institution: 'First Bank', accessToken: 'access-1c' }, ['id'], { accessToken: 'access-1c' }));
    await call('upsertItemByItemId', () =>
      orm.plaidItem.upsertOneResult({ itemId: 'item-1', institution: 'First Bank', accessToken: 'access-1d' }, ['itemId'], { accessToken: 'access-1d', institution: 'Renamed Bank' }, { onlyIfChanged: true })
    );
    await call('upsertItems', () =>
      orm.plaidItem.upsertMany(
        [
          { itemId: 'item-2', institution: 'Second Bank', accessToken: 'access-2' },
          { itemId: 'item-4', institution: 'Fourth Bank', accessToken: 'access-4' },
        ],
        ['itemId'],
        ['institution', 'accessToken'],
        { onlyIfChanged: true }
      )
    );
    await call('reencrypt', () => createOrm(pool, { fieldKeys: next + ',' + current }).plaidItem.reencrypt({ batch: 2 }));
    await call('deleteItem', () => orm.plaidItem.deleteOne(second!.id));
    writeFileSync(out!, JSON.stringify(calls, null, 2));
  } finally {
    await base.end();
    await admin.query('DROP SCHEMA ' + schema + ' CASCADE');
    await admin.end();
  }
} else {
  const exchange = JSON.parse(readFileSync(process.env.VAULT_EXCHANGE!, 'utf8')) as { go: string; ts?: string };
  const pool = new pg.Pool({ connectionString: dsn, options: '-c search_path=' + process.env.VAULT_SCHEMA + ',public' });
  try {
    const orm = createOrm(pool, { fieldKeys: current });
    const fromGo = await orm.plaidItem.getOne(exchange.go as never);
    check(fromGo.accessToken.reveal() === 'go-token' && fromGo.webhookSecret?.reveal() === 'go-hook', 'Go wrote the plaintext TypeScript reads');
    check((await reason(() => createOrm(pool, { fieldKeys: next }).plaidItem.getOne(exchange.go as never))) === 'unknown-key', 'a wrong key is unknown-key');
    check((await reason(() => createOrm(pool).plaidItem.getOne(exchange.go as never))) === 'no-keys', 'no key is no-keys');
    const mine = await orm.plaidItem.createOne({ itemId: 'ts-item', institution: 'TS Bank', accessToken: 'ts-token' });
    const raw = await pool.query('SELECT access_token FROM plaid_item WHERE id = $1', [uuidToDb(mine.id)]);
    const id = FieldKeyring.parse(current).currentId;
    check(raw.rows[0].access_token.startsWith('v1:' + id + ':') && !raw.rows[0].access_token.includes('ts-token'), 'TypeScript stores ciphertext');
    const resealed = await createOrm(pool, { fieldKeys: next + ',' + current }).plaidItem.reencrypt();
    check(resealed === 2, 'reencrypt reseals both rows, not ' + resealed);
    writeFileSync(process.env.VAULT_EXCHANGE!, JSON.stringify({ ...exchange, ts: mine.id }));
  } finally {
    await pool.end();
  }
}
`

// encryptedORMTest is the Go side, a test of the generated ORM module.
// TestVaultParity runs the parity script, call for call the TypeScript
// side's, and writes each call's statements to VAULT_PARITY_OUT.
// TestVaultCross is the Go side of the shared schema: VAULT_PHASE "write"
// creates the schema and writes a row, and "read" reads the row the
// TypeScript side wrote, under the key it rotated to, and drops the
// schema.
const encryptedORMTest = `package orm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/parable-work/superschematic/runtime/schema/go/fieldcrypt"
	types "example.com/schemas/types/go/fixture-vault-db"
)

const (
	currentKey = "AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA="
	nextKey    = "ZWZnaGlqa2xtbm9wcXJzdHV2d3h5ent8fX5/gIGCg4Q="
)

type vaultCall struct {
	Name       string   ` + "`json:\"name\"`" + `
	Statements []string ` + "`json:\"statements\"`" + `
}

type vaultRecorder struct {
	mu    sync.Mutex
	calls []vaultCall
}

func (r *vaultRecorder) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) > 0 {
		r.calls[len(r.calls)-1].Statements = append(r.calls[len(r.calls)-1].Statements, data.SQL)
	}
	return ctx
}

func (r *vaultRecorder) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (r *vaultRecorder) call(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, vaultCall{Name: name})
}

// vaultPool is a pool on schema, which it creates with create.sql when
// create is set, recording through tracer when one is given.
func vaultPool(t *testing.T, dsn, schema string, create bool, tracer pgx.QueryTracer) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	if create {
		admin, err := pgx.Connect(ctx, dsn)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
			t.Fatal(err)
		}
		_ = admin.Close(ctx)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	cfg.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if create {
		createSQL, err := os.ReadFile("testdata/create.sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(createSQL)); err != nil {
			t.Fatal(err)
		}
	}
	return pool
}

func dropSchema(dsn, schema string) {
	ctx := context.Background()
	if admin, err := pgx.Connect(ctx, dsn); err == nil {
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		_ = admin.Close(ctx)
	}
}

func TestVaultParity(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	out := os.Getenv("VAULT_PARITY_OUT")
	if dsn == "" || out == "" || os.Getenv("VAULT_PHASE") != "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL and VAULT_PARITY_OUT to run the parity script")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("vault_parity_go_%d", time.Now().UnixNano())
	rec := &vaultRecorder{}
	pool := vaultPool(t, dsn, schema, true, rec)
	defer dropSchema(dsn, schema)
	defer pool.Close()
	db, err := ConnectWithPool(pool, WithFieldKeys(currentKey))
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := ConnectWithPool(pool, WithFieldKeys(nextKey+","+currentKey))
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", rec.calls[len(rec.calls)-1].Name, err)
		}
	}
	// A soft delete stamps who made it, so the ORM acts as a user.
	alice, err := types.ParseIdentityUUID("TKbHsHDO6x9RCjbs5c2u4")
	must(err)
	ctx = WithUserID(ctx, alice)
	str := func(s string) *string { return &s }
	all := PlaidItemFields{}.All()

	rec.call("createItem")
	first, err := db.PlaidItem.CreateOne(ctx, &types.PlaidItem{ItemId: "item-1", Institution: "First Bank", AccessToken: "access-1", WebhookSecret: "whsec-1"})
	must(err)
	rec.call("createItems")
	items, err := db.PlaidItem.CreateMany(ctx, []*types.PlaidItem{
		{ItemId: "item-2", Institution: "Second Bank", AccessToken: "access-2"},
		{ItemId: "item-3", Institution: "Third Bank", AccessToken: "access-3", WebhookSecret: "whsec-3"},
	})
	must(err)
	second := items[0]
	rec.call("createCredential")
	_, err = db.ApiCredential.CreateOne(ctx, &types.ApiCredential{Service: "voyage", Secret: "pa-voyage"})
	must(err)
	rec.call("getItem")
	_, err = db.PlaidItem.GetOne(ctx, *first.Id, nil)
	must(err)
	rec.call("getItems")
	_, err = db.PlaidItem.GetManyByIDs(ctx, []types.IdentityUUID{*first.Id, *second.Id})
	must(err)
	rec.call("findItems")
	_, _, err = db.PlaidItem.FindMany(ctx, &PlaidItemFilter{Institution: &StringFilter{ILike: str("bank")}}, &PlaidItemFindOptions{Fields: all, OrderBy: []PlaidItemOrderBy{{Field: PlaidItemOrderByItemId}}})
	must(err)
	rec.call("pageItems")
	_, err = db.PlaidItem.FindPage(ctx, nil, &PlaidItemPageOptions{Fields: all, Limit: 2})
	must(err)
	rec.call("updateItem")
	_, err = db.PlaidItem.UpdateOne(ctx, *first.Id, &PlaidItemUpdate{AccessToken: str("access-1b"), WebhookSecretSetNull: true})
	must(err)
	rec.call("upsertItem")
	_, err = db.PlaidItem.UpsertOne(ctx, &types.PlaidItem{Id: first.Id, ItemId: "item-1", Institution: "First Bank", AccessToken: "access-1c"}, PlaidItemUniqueById, &PlaidItemUpdate{AccessToken: str("access-1c")})
	must(err)
	rec.call("upsertItemByItemId")
	_, err = db.PlaidItem.UpsertOneResult(ctx, &types.PlaidItem{ItemId: "item-1", Institution: "First Bank", AccessToken: "access-1d"}, PlaidItemUniqueByItemId,
		&PlaidItemUpdate{AccessToken: str("access-1d"), Institution: str("Renamed Bank")}, &UpsertOptions{OnlyIfChanged: true})
	must(err)
	rec.call("upsertItems")
	_, err = db.PlaidItem.UpsertMany(ctx, []*types.PlaidItem{
		{ItemId: "item-2", Institution: "Second Bank", AccessToken: "access-2"},
		{ItemId: "item-4", Institution: "Fourth Bank", AccessToken: "access-4"},
	}, PlaidItemUniqueByItemId, PlaidItemFields{Institution: true, AccessToken: true}, &UpsertOptions{OnlyIfChanged: true})
	must(err)
	rec.call("reencrypt")
	_, err = rotated.PlaidItem.Reencrypt(ctx, 2)
	must(err)
	rec.call("deleteItem")
	must(db.PlaidItem.DeleteOne(ctx, *second.Id))

	data, err := json.MarshalIndent(rec.calls, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestVaultCross(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	phase, schema, exchange := os.Getenv("VAULT_PHASE"), os.Getenv("VAULT_SCHEMA"), os.Getenv("VAULT_EXCHANGE")
	if dsn == "" || phase == "" {
		t.Skip("set VAULT_PHASE, VAULT_SCHEMA and VAULT_EXCHANGE to run the shared schema's phases")
	}
	ctx := context.Background()
	pool := vaultPool(t, dsn, schema, phase == "write", nil)
	defer pool.Close()
	open := func(secret string) *Database {
		t.Helper()
		db, err := ConnectWithPool(pool, WithFieldKeys(secret))
		if err != nil {
			t.Fatal(err)
		}
		return db
	}
	var ids struct {
		Go string ` + "`json:\"go\"`" + `
		TS string ` + "`json:\"ts,omitempty\"`" + `
	}
	switch phase {
	case "write":
		db := open(currentKey)
		item, err := db.PlaidItem.CreateOne(ctx, &types.PlaidItem{ItemId: "go-item", Institution: "Go Bank", AccessToken: "go-token", WebhookSecret: "go-hook"})
		if err != nil {
			t.Fatal(err)
		}
		if item.AccessToken != "go-token" || item.WebhookSecret != "go-hook" {
			t.Fatalf("the create returned %q and %q", item.AccessToken, item.WebhookSecret)
		}
		// psql sees the envelope, never the plaintext.
		var stored string
		if err := pool.QueryRow(ctx, "SELECT access_token FROM plaid_item WHERE id = $1", item.Id.ToUUID()).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		keys, _ := fieldcrypt.ParseKeys(currentKey)
		if !strings.HasPrefix(stored, "v1:"+keys.CurrentID()+":") || strings.Contains(stored, "go-token") {
			t.Fatalf("the column holds %q", stored)
		}
		ids.Go = item.Id.String()
		data, _ := json.Marshal(ids)
		if err := os.WriteFile(exchange, data, 0o644); err != nil {
			t.Fatal(err)
		}
	case "read":
		defer dropSchema(dsn, schema)
		data, err := os.ReadFile(exchange)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &ids); err != nil || ids.TS == "" {
			t.Fatalf("the exchange holds %s (%v)", data, err)
		}
		goID, err := types.ParseIdentityUUID(ids.Go)
		if err != nil {
			t.Fatal(err)
		}
		tsID, err := types.ParseIdentityUUID(ids.TS)
		if err != nil {
			t.Fatal(err)
		}
		// TypeScript resealed every row under the next key, so the current
		// one may leave the secret.
		db := open(nextKey)
		fromTS, err := db.PlaidItem.GetOne(ctx, tsID, nil)
		if err != nil || fromTS.AccessToken != "ts-token" {
			t.Fatalf("TypeScript's row reads %+v, %v", fromTS, err)
		}
		fromGo, err := db.PlaidItem.GetOne(ctx, goID, nil)
		if err != nil || fromGo.AccessToken != "go-token" || fromGo.WebhookSecret != "go-hook" {
			t.Fatalf("Go's resealed row reads %+v, %v", fromGo, err)
		}
		if _, err := open(currentKey).PlaidItem.GetOne(ctx, tsID, nil); !errors.Is(err, fieldcrypt.ErrUnknownKey) {
			t.Fatalf("a wrong key reads %v, not ErrUnknownKey", err)
		}
		keyless, err := ConnectWithPool(pool)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := keyless.PlaidItem.GetOne(ctx, tsID, nil); !errors.Is(err, fieldcrypt.ErrNoKeys) {
			t.Fatalf("no key reads %v, not ErrNoKeys", err)
		}
		if _, err := keyless.PlaidItem.CreateOne(ctx, &types.PlaidItem{ItemId: "x", Institution: "X", AccessToken: "x"}); !errors.Is(err, fieldcrypt.ErrNoKeys) {
			t.Fatalf("no key writes %v, not ErrNoKeys", err)
		}
		token := "other"
		var refused *EncryptedWriteError
		if _, err := db.PlaidItem.UpdateMany(ctx, nil, &PlaidItemUpdate{AccessToken: &token}); !errors.As(err, &refused) || refused.Field != "accessToken" {
			t.Fatalf("UpdateMany of an encrypted field reads %v", err)
		}
		// An upsert on itemId seals the token under the key of the row it
		// updates, by a second statement (D89).
		upserted, err := db.PlaidItem.UpsertOneResult(ctx, &types.PlaidItem{ItemId: "go-item", Institution: "Go Bank", AccessToken: "x"}, PlaidItemUniqueByItemId, &PlaidItemUpdate{AccessToken: &token}, nil)
		if err != nil || upserted.Status != UpsertUpdated || *upserted.Row.Id != goID || upserted.Row.AccessToken != "other" {
			t.Fatalf("an upsert on itemId of an encrypted field reads %+v, %v", upserted, err)
		}
		if again, err := db.PlaidItem.GetOne(ctx, goID, nil); err != nil || again.AccessToken != "other" {
			t.Fatalf("the upserted token reads %+v, %v", again, err)
		}
		// MaskSecrets clears it, as it does a Secret<T>.
		if masked := fromGo.MaskSecrets(); masked.AccessToken != "" || masked.WebhookSecret != "" {
			t.Fatalf("MaskSecrets keeps %q and %q", masked.AccessToken, masked.WebhookSecret)
		}
	}
}
`
