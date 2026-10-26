package ormgen

import (
	"os"
	"testing"
)

// syncFixture is the database a bank sync upserts its line items in (D89).
const syncFixture = "fixture-sync-db"

// syncHydratingCalls are the calls of upsertParityScript after whose
// statements the Go ORM hydrates the line items' accounts, which the
// TypeScript repositories leave to getManyByIds.
var syncHydratingCalls = map[string]bool{"findWithoutACount": true}

// TestUpsertParityOfGoAndTypeScriptOnPostgres runs upsertParityScript, a
// script of fixture-sync-db's upserts (D89), through both ORMs on one
// Postgres and requires the same statements, as
// TestTableParityOfGoAndTypeScriptOnPostgres does: upsertMany only if
// changed, which reads back the lines it left, in batches, and by a key of
// two columns that sets a location, a JSON value and a list;
// upsertOneResult only if changed, its read, an update that sets nothing
// and one that sets a column null; findMany without its count, with in on a
// time and a boolean; and a transaction at an isolation level, read only,
// and one with savepoints, one of which rolls back.
func TestUpsertParityOfGoAndTypeScriptOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the Go and TypeScript repositories against one Postgres")
	}
	runTableParity(t, syncFixture, upsertParityScript, upsertParityORMTest, "TestUpsertParity", syncHydratingCalls)
}

// upsertParityScript is the TypeScript side, recorded as tableParityScript
// records its calls.
const upsertParityScript = `import { readFileSync, writeFileSync } from 'node:fs';
import pg from 'pg';
import type { OrmPool, OrmQueryable } from '@superschematic/http-runtime/postgres';
import { createOrm } from '@TYPES@/orm';

const [createSQL, out] = process.argv.slice(2);
const dsn = process.env.SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL!;
const schema = 'upsert_parity_ts_' + process.pid + '_' + Date.now();
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
  const account = await call('createAccount', () => orm.bankAccount.createOne({ code: 'chk', name: 'Checking' }));
  const line = (i: number, amount = -100 - i) => ({
    plaidTransactionId: 'txn-' + i,
    accountId: account.id,
    reference: 'ref-' + i,
    name: 'Line ' + i,
    amount: amount as never,
    postedAt: new Date(Date.UTC(2026, 9, 1 + i)) as never,
    tags: [],
  });
  const fields = ['name', 'amount', 'postedAt'] as const;
  await call('upsertMany', () => orm.lineItem.upsertMany([line(0), line(1), line(2)], ['plaidTransactionId'], fields, { onlyIfChanged: true }));
  await call('upsertManyAgain', () => orm.lineItem.upsertMany([line(0), line(1, -1), line(2)], ['plaidTransactionId'], fields, { onlyIfChanged: true }));
  await call('upsertManyBatches', () => orm.lineItem.upsertMany([line(0), line(1), line(3)], ['plaidTransactionId'], fields, { batch: 2 }));
  await call('upsertManyByReference', () =>
    orm.lineItem.upsertMany(
      [{ ...line(0), location: { lat: 1.5, lon: 2 } as never, details: { merchant: 'Cafe' }, tags: ['coffee'] }],
      ['account', 'reference'],
      ['tags', 'details', 'location'],
      { onlyIfChanged: true }
    )
  );
  const one = { ...line(9), location: { lat: 51.5, lon: -0.125 } as never, details: { merchant: 'Shop' } };
  const update = { name: one.name, location: one.location, details: one.details };
  await call('upsertOneResult', () => orm.lineItem.upsertOneResult(one, ['plaidTransactionId'], update, { onlyIfChanged: true }));
  await call('upsertOneResultUnchanged', () => orm.lineItem.upsertOneResult(one, ['plaidTransactionId'], update, { onlyIfChanged: true }));
  await call('upsertOneResultKeep', () => orm.lineItem.upsertOneResult(one, ['plaidTransactionId'], {}));
  await call('upsertOneSetNull', () => orm.lineItem.upsertOne(one, ['plaidTransactionId'], { details: null }, { onlyIfChanged: true }));
  await call('findWithoutACount', () =>
    orm.lineItem.findMany(
      { postedAt: { in: [new Date(Date.UTC(2026, 9, 1)), new Date(Date.UTC(2026, 9, 3))] as never }, pending: { in: [false] } },
      { count: false, orderBy: [{ field: 'reference' }] }
    )
  );
  await call('transactionReadOnly', () =>
    orm.transaction(tx => tx.bankAccount.findOne({ code: { eq: 'chk' } }), { isolation: 'repeatable read', readOnly: true })
  );
  await call('savepoints', () =>
    orm.transaction(async tx => {
      await tx.bankAccount.createOne({ code: 'kept', name: 'Kept' });
      await tx.transaction(inner => inner.bankAccount.createOne({ code: 'inner', name: 'Inner' }));
      try {
        await tx.transaction(inner => inner.bankAccount.createOne({ code: 'kept', name: 'Again' }));
      } catch {
        // The duplicate rolls back to the savepoint; the transaction goes on.
      }
      await tx.bankAccount.createOne({ code: 'after', name: 'After' });
    })
  );
  writeFileSync(out!, JSON.stringify(calls, null, 2));
} finally {
  await base.end();
  await admin.query('DROP SCHEMA ' + schema + ' CASCADE');
  await admin.end();
}
`

// upsertParityORMTest is the Go side: the same calls through the Go ORM
// over a pool whose tracer records each statement.
const upsertParityORMTest = `package orm

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
	types "example.com/schemas/types/go/fixture-sync-db"
)

type parityCall struct {
	Name       string   ` + "`json:\"name\"`" + `
	Statements []string ` + "`json:\"statements\"`" + `
}

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

func TestUpsertParity(t *testing.T) {
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
	schema := fmt.Sprintf("upsert_parity_go_%d", time.Now().UnixNano())
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

	rec.call("createAccount")
	account, err := db.BankAccount.CreateOne(ctx, &types.BankAccount{Code: "chk", Name: "Checking"})
	must(err)
	line := func(i int, amount ...int) *types.LineItem {
		a := -100 - i
		if len(amount) > 0 {
			a = amount[0]
		}
		return &types.LineItem{
			PlaidTransactionId: fmt.Sprintf("txn-%d", i),
			Account:            types.BankAccount{Id: account.Id},
			Reference:          fmt.Sprintf("ref-%d", i),
			Name:               fmt.Sprintf("Line %d", i),
			Amount:             types.FinanceSignedMoney(a),
			PostedAt:           types.TemporalDateTime(time.Date(2026, 10, 1+i, 0, 0, 0, 0, time.UTC)),
			Tags:               []string{},
		}
	}
	fields := LineItemFields{Name: true, Amount: true, PostedAt: true}
	onlyIfChanged := &UpsertOptions{OnlyIfChanged: true}

	rec.call("upsertMany")
	_, err = db.LineItem.UpsertMany(ctx, []*types.LineItem{line(0), line(1), line(2)}, LineItemUniqueByPlaidTransactionId, fields, onlyIfChanged)
	must(err)
	rec.call("upsertManyAgain")
	_, err = db.LineItem.UpsertMany(ctx, []*types.LineItem{line(0), line(1, -1), line(2)}, LineItemUniqueByPlaidTransactionId, fields, onlyIfChanged)
	must(err)
	rec.call("upsertManyBatches")
	_, err = db.LineItem.UpsertMany(ctx, []*types.LineItem{line(0), line(1), line(3)}, LineItemUniqueByPlaidTransactionId, fields, &UpsertOptions{Batch: 2})
	must(err)
	rec.call("upsertManyByReference")
	located := line(0)
	located.Location = &types.GeoLocation{Lat: 1.5, Lon: 2}
	cafe := types.GenericJSON("{\"merchant\": \"Cafe\"}")
	located.Details = &cafe
	located.Tags = []string{"coffee"}
	_, err = db.LineItem.UpsertMany(ctx, []*types.LineItem{located}, LineItemUniqueByAccountReference, LineItemFields{Tags: true, Details: true, Location: true}, onlyIfChanged)
	must(err)
	one := func() *types.LineItem {
		l := line(9)
		l.Location = &types.GeoLocation{Lat: 51.5, Lon: -0.125}
		shop := types.GenericJSON("{\"merchant\": \"Shop\"}")
		l.Details = &shop
		return l
	}
	update := func(l *types.LineItem) *LineItemUpdate {
		return &LineItemUpdate{Name: &l.Name, Location: l.Location, Details: l.Details}
	}
	rec.call("upsertOneResult")
	first := one()
	_, err = db.LineItem.UpsertOneResult(ctx, first, LineItemUniqueByPlaidTransactionId, update(first), onlyIfChanged)
	must(err)
	rec.call("upsertOneResultUnchanged")
	second := one()
	_, err = db.LineItem.UpsertOneResult(ctx, second, LineItemUniqueByPlaidTransactionId, update(second), onlyIfChanged)
	must(err)
	rec.call("upsertOneResultKeep")
	_, err = db.LineItem.UpsertOneResult(ctx, one(), LineItemUniqueByPlaidTransactionId, &LineItemUpdate{}, nil)
	must(err)
	rec.call("upsertOneSetNull")
	_, err = db.LineItem.UpsertOneResult(ctx, one(), LineItemUniqueByPlaidTransactionId, &LineItemUpdate{DetailsSetNull: true}, onlyIfChanged)
	must(err)
	rec.call("findWithoutACount")
	_, _, err = db.LineItem.FindMany(ctx, &LineItemFilter{
		PostedAt: &DateTimeFilter{In: []time.Time{time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)}},
		Pending:  &BoolFilter{In: []bool{false}},
	}, &LineItemFindOptions{Fields: LineItemFields{}.All(), OrderBy: []LineItemOrderBy{{Field: LineItemOrderByReference}}, SkipCount: true})
	must(err)
	code := "chk"
	rec.call("transactionReadOnly")
	must(db.TransactionWith(ctx, TxOptions{Isolation: RepeatableRead, ReadOnly: true}, func(tx TxInterface) error {
		_, err := tx.GetBankAccountRepository().FindOne(ctx, &BankAccountFilter{Code: &StringFilter{Eq: &code}}, nil)
		return err
	}))
	rec.call("savepoints")
	must(db.Transaction(ctx, func(tx TxInterface) error {
		if _, err := tx.GetBankAccountRepository().CreateOne(ctx, &types.BankAccount{Code: "kept", Name: "Kept"}); err != nil {
			return err
		}
		if err := tx.Transaction(ctx, func(inner TxInterface) error {
			_, err := inner.GetBankAccountRepository().CreateOne(ctx, &types.BankAccount{Code: "inner", Name: "Inner"})
			return err
		}); err != nil {
			return err
		}
		// The duplicate rolls back to the savepoint; the transaction goes on.
		_ = tx.Transaction(ctx, func(inner TxInterface) error {
			_, err := inner.GetBankAccountRepository().CreateOne(ctx, &types.BankAccount{Code: "kept", Name: "Again"})
			return err
		})
		_, err := tx.GetBankAccountRepository().CreateOne(ctx, &types.BankAccount{Code: "after", Name: "After"})
		return err
	}))

	data, err := json.MarshalIndent(rec.calls, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
`
