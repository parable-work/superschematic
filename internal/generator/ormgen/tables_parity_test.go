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

// tableParityFixture is the fixture both ORMs run the parity script on.
const tableParityFixture = "fixture-ledger-db"

// parityCall is one repository call of the parity script and the
// statements it sent, as each side records them.
type parityCall struct {
	Name       string   `json:"name"`
	Statements []string `json:"statements"`
}

// hydratingCalls are the calls after whose statements the Go ORM hydrates
// the to-one relations a full field selection names, with a statement or
// two of its own per relation, which the TypeScript repositories leave to
// getManyByIds: the TypeScript statements are the Go ones' prefix.
var hydratingCalls = map[string]bool{"findEntries": true, "pageEntries": true}

// TestTableParityOfGoAndTypeScriptOnPostgres runs one script of repository
// calls through the Go ORM and through the TypeScript repositories of
// fixture-ledger-db (D79), each in a schema of its own on the Postgres at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL, each recording the text of every
// statement it sends, and requires the two equal, call by call: every
// read, write, filter kind, search, page, upsert, soft delete and
// transaction with an enqueue. Both write their statements from
// sqlutil.TableStatementsOf and sqlutil.PostgresClauses, so this holds the
// two fills to one another. A transaction's BEGIN and COMMIT are left out,
// pgx writing them in lower case. It needs Bun, builds the HTTP runtime,
// and skips without the database, or without Bun unless
// SUPERSCHEMATIC_REQUIRE_TS_CHECKS says the gate must run.
func TestTableParityOfGoAndTypeScriptOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the Go and TypeScript repositories against one Postgres")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun not available: %v", err))
	}
	paths := testpaths.Local(t)
	testpaths.BuildHTTPRuntime(t, bun, paths)

	ormDir := generateORMModule(t, tableParityFixture)
	schema, err := loader.LoadService(filepath.Join(fixturesDir, tableParityFixture))
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	typesDir := filepath.Join(root, "types", "typescript", tableParityFixture)
	output, err := tsgen.Generate(schema, tsgen.Options{SchemaName: tableParityFixture, Clock: codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))})
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
	script := strings.ReplaceAll(tableParityScript, "@TYPES@", naming.Default().NpmTypesPackage(tableParityFixture))
	createSQL := filepath.Join(ormDir, "testdata", "create.sql")
	for name, content := range map[string]string{"parity.ts": script} {
		if err := os.WriteFile(filepath.Join(typesDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	tsOut := filepath.Join(root, "statements.ts.json")
	run := exec.Command(bun, "parity.ts", createSQL, tsOut)
	run.Dir = typesDir
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("the TypeScript side failed: %v\n%s", err, out)
	}

	goOut := filepath.Join(root, "statements.go.json")
	t.Setenv("TABLE_PARITY_OUT", goOut)
	if err := os.WriteFile(filepath.Join(ormDir, "table_parity_test.go"), []byte(tableParityORMTest), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := runORMModule(t, ormDir); !strings.Contains(out, "--- PASS: TestTableParity ") {
		t.Fatalf("the Go side did not pass:\n%s", out)
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
		ts := tsCalls[i]
		if g.Name != ts.Name {
			t.Fatalf("call %d is %s in Go and %s in TypeScript", i, g.Name, ts.Name)
		}
		if len(ts.Statements) == 0 {
			t.Errorf("%s: TypeScript sent no statement", ts.Name)
		}
		want := g.Statements
		if hydratingCalls[g.Name] && len(want) > len(ts.Statements) {
			for _, extra := range want[len(ts.Statements):] {
				if !strings.HasPrefix(extra, "SELECT ") {
					t.Errorf("%s: Go hydrates with %q, which reads nothing", g.Name, extra)
				}
			}
			want = want[:len(ts.Statements)]
		}
		if strings.Join(want, "\n;\n") != strings.Join(ts.Statements, "\n;\n") {
			t.Errorf("%s: the statements differ\nGo:\n%s\nTypeScript:\n%s", g.Name, strings.Join(g.Statements, "\n"), strings.Join(ts.Statements, "\n"))
		}
		statements += len(ts.Statements)
	}
	t.Logf("Go and TypeScript sent the same %d statements over %d calls", statements, len(tsCalls))
}

// tableParityScript is the TypeScript side: it runs the parity script over
// the repositories of the generated package @TYPES@, on a schema of its
// own with create.sql (its first argument) applied, and writes each call's
// statements as JSON to its second argument.
const tableParityScript = `import { readFileSync, writeFileSync } from 'node:fs';
import pg from 'pg';
import { enqueue, type OrmPool, type OrmQueryable } from '@superschematic/http-runtime/postgres';
import { createOrm } from '@TYPES@/orm';
import { entryPostedQueue } from '@TYPES@/queues';

const [createSQL, out] = process.argv.slice(2);
const dsn = process.env.SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL!;
const schema = 'table_parity_ts_' + process.pid + '_' + Date.now();
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
  const orm = createOrm(pool, 'TKbHsHDO6x9RCjbs5c2u4');
  const day = (d: string) => d as never;
  const cents = (n: number) => n as never;

  const cash = await call('createAccount', () => orm.account.createOne({ name: 'Cash' as never, code: 'cash' }));
  const [bank, card] = await call('createAccounts', () =>
    orm.account.createMany([
      { name: 'Bank' as never, code: 'bank', openedOn: day('2026-10-01') },
      { name: 'Card' as never, code: 'card' },
    ])
  );
  await call('getAccount', () => orm.account.getOne(cash.id));
  await call('getAccounts', () => orm.account.getManyByIds([cash.id, bank!.id, cash.id]));
  await call('findAccount', () => orm.account.findOne({ code: { eq: 'bank' } }));
  await call('findAccounts', () =>
    orm.account.findMany({ name: { ilike: 'a' }, searchText: 'ca', referencedByEntryAccount: { exists: false } }, { orderBy: [{ field: 'code', desc: true }], limit: 10, offset: 1 })
  );
  const first = await call('pageAccounts', () => orm.account.findPage(null, { sort: { field: 'name' }, limit: 2 }));
  await call('pageAccountsAfter', () => orm.account.findPage(null, { sort: { field: 'name' }, limit: 2, after: first.next }));
  await call('pageAccountsByKey', () => orm.account.findPage({ code: { in: ['bank', 'card'] } }, { sort: { field: 'id', desc: true }, limit: 1, after: first.next }));
  await call('updateAccount', () => orm.account.updateOne(cash.id, { name: 'Cash box' as never }));
  await call('updateAccounts', () => orm.account.updateMany({ code: { in: ['bank', 'card'] } }, { openedOn: day('2026-10-02') }));
  await call('upsertAccount', () => orm.account.upsertOne({ name: 'Cash' as never, code: 'cash' }, ['code'], { name: 'Cash again' as never }));

  const e1 = await call('createEntry', () =>
    orm.entry.createOne({ accountId: cash.id, amountCents: cents(-1250), bookedOn: day('2026-10-25'), memo: 'Coffee', tags: ['food'] })
  );
  await call('createEntries', () =>
    orm.entry.createMany([
      { accountId: bank!.id, amountCents: cents(500), bookedOn: day('2026-10-26'), tags: [], externalId: 'x1', kind: 'credit' as never },
      { accountId: card!.id, transferId: cash.id, amountCents: cents(-75), bookedOn: day('2026-10-27'), tags: [], memo: 'Transfer' },
    ])
  );
  await call('getEntry', () => orm.entry.getOne(e1.id));
  await call('findEntry', () =>
    orm.entry.findOne({
      amountCents: { between: [cents(-2000), cents(0)] },
      bookedOn: { gte: day('2026-10-01') },
      kind: { in: ['debit' as never] },
      memo: { isNull: false },
      accountId: { eq: cash.id },
      searchText: 'coffee',
    })
  );
  await call('findEntries', () =>
    orm.entry.findMany({ reviewedAt: { isNull: true }, transferId: { isNull: true }, amountCents: { in: [cents(500), cents(-1250)] } }, { orderBy: [{ field: 'amountCents' }] })
  );
  await call('pageEntries', () => orm.entry.findPage({ accountId: { in: [cash.id, bank!.id] } }, { sort: { field: 'amountCents', desc: true }, limit: 1 }));
  await call('updateEntry', () => orm.entry.updateOne(e1.id, { memo: null, amountCents: cents(-1300), transferId: bank!.id }));
  await call('updateEntries', () => orm.entry.updateMany({ accountId: { in: [bank!.id, card!.id] } }, { kind: 'credit' as never }));
  const upsert = { accountId: bank!.id, externalId: 'x1', amountCents: cents(600), bookedOn: day('2026-10-26'), tags: [], kind: 'credit' as never };
  await call('upsertEntry', () => orm.entry.upsertOne(upsert, ['account', 'externalId'], { amountCents: cents(600) }));
  await call('upsertEntryKeep', () => orm.entry.upsertOne(upsert, ['account', 'externalId'], {}));
  await call('deleteEntry', () => orm.entry.deleteOne(e1.id));
  await call('deleteEntries', () => orm.entry.deleteMany({ accountId: { eq: card!.id } }));
  await call('findDeleted', () => orm.entry.findOne({ accountId: { eq: card!.id } }, { includeDeleted: true }));
  await call('hardDeleteEntry', () => orm.entry.hardDeleteOne(e1.id));
  await call('transaction', () =>
    orm.transaction(async tx => {
      const posted = await tx.entry.createOne({ accountId: bank!.id, amountCents: cents(4200), bookedOn: day('2026-10-28'), tags: [] });
      await enqueue(tx, entryPostedQueue, { entryId: posted.id, amountCents: posted.amountCents });
    })
  );
  await call('deleteAccount', () => orm.account.deleteOne(card!.id));
  await call('deleteAccounts', () => orm.account.deleteMany({ code: { eq: 'bank' } }));
  writeFileSync(out!, JSON.stringify(calls, null, 2));
} finally {
  await base.end();
  await admin.query('DROP SCHEMA ' + schema + ' CASCADE');
  await admin.end();
}
`

// tableParityORMTest is the Go side, a test of the generated ORM module: it
// runs the parity script, call for call the TypeScript side's, through the
// Go ORM over a pool whose tracer records each statement, and writes each
// call's statements as JSON to TABLE_PARITY_OUT.
const tableParityORMTest = `package orm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	types "example.com/schemas/types/go/fixture-ledger-db"
)

type parityCall struct {
	Name       string   ` + "`json:\"name\"`" + `
	Statements []string ` + "`json:\"statements\"`" + `
}

// recorder records the text of each statement the pool sends, under the
// call that sends it.
type recorder struct {
	mu    sync.Mutex
	calls []parityCall
}

func (r *recorder) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) > 0 {
		r.calls[len(r.calls)-1].Statements = append(r.calls[len(r.calls)-1].Statements, data.SQL)
	}
	return ctx
}

func (r *recorder) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (r *recorder) call(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, parityCall{Name: name})
}

func TestTableParity(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	out := os.Getenv("TABLE_PARITY_OUT")
	if dsn == "" || out == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL and TABLE_PARITY_OUT to run the parity script")
	}
	createSQL, err := os.ReadFile("testdata/create.sql")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := fmt.Sprintf("table_parity_go_%d", time.Now().UnixNano())
	if _, err := base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = base.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") }()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	rec := &recorder{}
	cfg.ConnConfig.Tracer = rec
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(createSQL)); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	db, err := ConnectWithPool(pool)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	alice, err := types.ParseIdentityUUID("TKbHsHDO6x9RCjbs5c2u4")
	if err != nil {
		t.Fatal(err)
	}
	ctx = WithUserID(ctx, alice)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", rec.calls[len(rec.calls)-1].Name, err)
		}
	}
	str := func(s string) *string { return &s }
	no, yes := false, true
	day := func(d string) types.TemporalDate { return types.TemporalDate(d) }
	all := AccountFields{}.All()

	rec.call("createAccount")
	cash, err := db.Account.CreateOne(ctx, &types.Account{Name: "Cash", Code: "cash"})
	must(err)
	rec.call("createAccounts")
	opened := day("2026-10-01")
	accounts, err := db.Account.CreateMany(ctx, []*types.Account{{Name: "Bank", Code: "bank", OpenedOn: &opened}, {Name: "Card", Code: "card"}})
	must(err)
	bank, card := accounts[0], accounts[1]
	rec.call("getAccount")
	_, err = db.Account.GetOne(ctx, *cash.Id, nil)
	must(err)
	rec.call("getAccounts")
	_, err = db.Account.GetManyByIDs(ctx, []types.IdentityUUID{*cash.Id, *bank.Id, *cash.Id})
	must(err)
	rec.call("findAccount")
	_, err = db.Account.FindOne(ctx, &AccountFilter{Code: &StringFilter{Eq: str("bank")}}, nil)
	must(err)
	rec.call("findAccounts")
	_, _, err = db.Account.FindMany(ctx, &AccountFilter{Name: &StringFilter{ILike: str("a")}, SearchText: str("ca"), ReferencedByEntryAccount: &ReferencedByFilter{Exists: false}},
		&AccountFindOptions{Fields: all, OrderBy: []AccountOrderBy{{Field: AccountOrderByCode, Desc: true}}, Limit: 10, Offset: 1})
	must(err)
	rec.call("pageAccounts")
	first, err := db.Account.FindPage(ctx, nil, &AccountPageOptions{Fields: all, Sort: AccountSort{Field: AccountSortByName}, Limit: 2})
	must(err)
	rec.call("pageAccountsAfter")
	_, err = db.Account.FindPage(ctx, nil, &AccountPageOptions{Fields: all, Sort: AccountSort{Field: AccountSortByName}, Limit: 2, After: first.Next})
	must(err)
	rec.call("pageAccountsByKey")
	_, err = db.Account.FindPage(ctx, &AccountFilter{Code: &StringFilter{In: []string{"bank", "card"}}}, &AccountPageOptions{Fields: all, Sort: AccountSort{Field: AccountSortById, Desc: true}, Limit: 1, After: first.Next})
	must(err)
	rec.call("updateAccount")
	box := types.IdentityName("Cash box")
	_, err = db.Account.UpdateOne(ctx, *cash.Id, &AccountUpdate{Name: &box})
	must(err)
	rec.call("updateAccounts")
	second := day("2026-10-02")
	_, err = db.Account.UpdateMany(ctx, &AccountFilter{Code: &StringFilter{In: []string{"bank", "card"}}}, &AccountUpdate{OpenedOn: &second})
	must(err)
	rec.call("upsertAccount")
	again := types.IdentityName("Cash again")
	_, err = db.Account.UpsertOne(ctx, &types.Account{Name: "Cash", Code: "cash"}, AccountUniqueByCode, &AccountUpdate{Name: &again})
	must(err)

	rec.call("createEntry")
	e1, err := db.Entry.CreateOne(ctx, &types.Entry{Account: types.Account{Id: cash.Id}, AmountCents: -1250, BookedOn: day("2026-10-25"), Memo: "Coffee", Tags: []string{"food"}})
	must(err)
	rec.call("createEntries")
	_, err = db.Entry.CreateMany(ctx, []*types.Entry{
		{Account: types.Account{Id: bank.Id}, AmountCents: 500, BookedOn: day("2026-10-26"), Tags: []string{}, ExternalId: "x1", Kind: "credit"},
		{Account: types.Account{Id: card.Id}, Transfer: &types.Account{Id: cash.Id}, AmountCents: -75, BookedOn: day("2026-10-27"), Tags: []string{}, Memo: "Transfer"},
	})
	must(err)
	rec.call("getEntry")
	_, err = db.Entry.GetOne(ctx, *e1.Id, nil)
	must(err)
	rec.call("findEntry")
	_, err = db.Entry.FindOne(ctx, &EntryFilter{
		AmountCents: &Int64Filter{Between: &Int64Range{Min: -2000, Max: 0}},
		BookedOn:    &DateFilter{Gte: str("2026-10-01")},
		Kind:        &StringFilter{In: []string{"debit"}},
		Memo:        &StringFilter{IsNull: &no},
		AccountID:   &UUIDFilter{Eq: cash.Id},
		SearchText:  str("coffee"),
	}, nil)
	must(err)
	rec.call("findEntries")
	_, _, err = db.Entry.FindMany(ctx, &EntryFilter{ReviewedAt: &DateTimeFilter{IsNull: &yes}, TransferID: &UUIDFilter{IsNull: &yes}, AmountCents: &Int64Filter{In: []int64{500, -1250}}},
		&EntryFindOptions{Fields: EntryFields{}.All(), OrderBy: []EntryOrderBy{{Field: EntryOrderByAmountCents}}})
	must(err)
	rec.call("pageEntries")
	_, err = db.Entry.FindPage(ctx, &EntryFilter{AccountID: &UUIDFilter{In: []types.IdentityUUID{*cash.Id, *bank.Id}}}, &EntryPageOptions{Fields: EntryFields{}.All(), Sort: EntrySort{Field: EntrySortByAmountCents, Desc: true}, Limit: 1})
	must(err)
	rec.call("updateEntry")
	amount := types.GenericInt64(-1300)
	_, err = db.Entry.UpdateOne(ctx, *e1.Id, &EntryUpdate{MemoSetNull: true, AmountCents: &amount, TransferID: bank.Id})
	must(err)
	rec.call("updateEntries")
	credit := types.EntryKind("credit")
	_, err = db.Entry.UpdateMany(ctx, &EntryFilter{AccountID: &UUIDFilter{In: []types.IdentityUUID{*bank.Id, *card.Id}}}, &EntryUpdate{Kind: &credit})
	must(err)
	upsert := func() *types.Entry {
		return &types.Entry{Account: types.Account{Id: bank.Id}, ExternalId: "x1", AmountCents: 600, BookedOn: day("2026-10-26"), Tags: []string{}, Kind: "credit"}
	}
	rec.call("upsertEntry")
	six := types.GenericInt64(600)
	_, err = db.Entry.UpsertOne(ctx, upsert(), EntryUniqueByAccountExternalId, &EntryUpdate{AmountCents: &six})
	must(err)
	rec.call("upsertEntryKeep")
	_, err = db.Entry.UpsertOne(ctx, upsert(), EntryUniqueByAccountExternalId, &EntryUpdate{})
	must(err)
	rec.call("deleteEntry")
	must(db.Entry.DeleteOne(ctx, *e1.Id))
	rec.call("deleteEntries")
	_, err = db.Entry.DeleteMany(ctx, &EntryFilter{AccountID: &UUIDFilter{Eq: card.Id}})
	must(err)
	rec.call("findDeleted")
	_, err = db.Entry.FindOne(ctx, &EntryFilter{AccountID: &UUIDFilter{Eq: card.Id}}, &EntryFindOptions{IncludeDeleted: true})
	must(err)
	rec.call("hardDeleteEntry")
	must(db.Entry.HardDeleteOne(ctx, *e1.Id))
	rec.call("transaction")
	must(db.Transaction(ctx, func(tx TxInterface) error {
		posted, err := tx.GetEntryRepository().CreateOne(ctx, &types.Entry{Account: types.Account{Id: bank.Id}, AmountCents: 4200, BookedOn: day("2026-10-28"), Tags: []string{}})
		if err != nil {
			return err
		}
		return EnqueueEntryPosted(ctx, tx, &types.EntryPosted{EntryId: *posted.Id, AmountCents: posted.AmountCents})
	}))
	rec.call("deleteAccount")
	must(db.Account.DeleteOne(ctx, *card.Id))
	rec.call("deleteAccounts")
	_, err = db.Account.DeleteMany(ctx, &AccountFilter{Code: &StringFilter{Eq: str("bank")}})
	must(err)

	data, err := json.MarshalIndent(rec.calls, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
`
