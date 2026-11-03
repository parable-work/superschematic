package ormgen

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// namingFixture is the fixture whose tables and columns @table and
// @column name in camel case (D85).
const namingFixture = "fixture-naming-db"

// TestNamedTablesAcrossGoAndTypeScriptOnPostgres: the Go ORM and the
// TypeScript repositories of fixture-naming-db each create, read, filter,
// page, update, upsert, soft delete and hard delete its rows on the
// Postgres at SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL, in a schema of
// their own with its create.sql applied, under the quoted camel-case
// names @table and @column give its tables and columns, and claim,
// complete and enqueue the messages of its queue, whose table @table names
// too, the ones its lines' trigger enqueued among them. They send the
// same statements, call by call, but for a transaction's BEGIN and
// COMMIT, which pgx writes in lower case. Each side checks what it reads
// back. It needs
// Bun, builds the HTTP runtime, and skips without the database, or without
// Bun unless SUPERSCHEMATIC_REQUIRE_TS_CHECKS says it must run.
func TestNamedTablesAcrossGoAndTypeScriptOnPostgres(t *testing.T) {
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

	ormDir := generateORMModule(t, namingFixture)
	schema, err := loader.LoadService(filepath.Join(fixturesDir, namingFixture))
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	typesDir := filepath.Join(root, "types", "typescript", namingFixture)
	output, err := tsgen.Generate(schema, tsgen.Options{SchemaName: namingFixture, Clock: codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))})
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
	script := strings.ReplaceAll(namingScript, "@TYPES@", naming.Default().NpmTypesPackage(namingFixture))
	if err := os.WriteFile(filepath.Join(typesDir, "naming.ts"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	createSQL := filepath.Join(ormDir, "testdata", "create.sql")
	tsOut := filepath.Join(root, "statements.ts.json")
	run := exec.Command(bun, "naming.ts", createSQL, tsOut)
	run.Dir = typesDir
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("the TypeScript side failed: %v\n%s", err, out)
	}
	goOut := filepath.Join(root, "statements.go.json")
	t.Setenv("NAMING_PARITY_OUT", goOut)
	if err := os.WriteFile(filepath.Join(ormDir, "naming_test.go"), []byte(namingORMTest), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := runORMModule(t, ormDir); !strings.Contains(out, "--- PASS: TestNaming ") {
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
		return calls
	}
	goCalls, tsCalls := read(goOut), read(tsOut)
	if len(goCalls) != len(tsCalls) {
		t.Fatalf("Go made %d calls and TypeScript %d", len(goCalls), len(tsCalls))
	}
	statements := 0
	control := func(statements []string) []string {
		var out []string
		for _, s := range statements {
			if !slices.Contains([]string{"BEGIN", "COMMIT", "ROLLBACK"}, strings.ToUpper(s)) {
				out = append(out, s)
			}
		}
		return out
	}
	for i := range goCalls {
		goCalls[i].Statements, tsCalls[i].Statements = control(goCalls[i].Statements), control(tsCalls[i].Statements)
	}
	for i, g := range goCalls {
		want := g.Statements
		// The Go ORM hydrates a to-one relation a full selection names with
		// statements of its own, which TypeScript leaves to getManyByIds.
		if (g.Name == "findLines" || g.Name == "pageLines") && len(want) > len(tsCalls[i].Statements) {
			want = want[:len(tsCalls[i].Statements)]
		}
		if g.Name != tsCalls[i].Name || len(tsCalls[i].Statements) == 0 || strings.Join(want, "\n;\n") != strings.Join(tsCalls[i].Statements, "\n;\n") {
			t.Errorf("%s: the statements differ\nGo:\n%s\nTypeScript (%s):\n%s", g.Name, strings.Join(g.Statements, "\n"), tsCalls[i].Name, strings.Join(tsCalls[i].Statements, "\n"))
		}
		statements += len(want)
	}
	t.Logf("Go and TypeScript sent the same %d statements over %d calls", statements, len(goCalls))
}

// namingScript is the TypeScript side: it runs the script over the
// repositories of @TYPES@ in a schema of its own, with create.sql (its
// first argument) applied, checks what each call reads back, and writes
// each call's statements as JSON to its second argument.
const namingScript = `import { readFileSync, writeFileSync } from 'node:fs';
import pg from 'pg';
import { enqueue, postgresQueue, uuidToDb, type OrmPool, type OrmQueryable } from '@superschematic/http-runtime/postgres';
import { createOrm } from '@TYPES@/orm';
import { linePostedQueue } from '@TYPES@/queues';

const [createSQL, out] = process.argv.slice(2);
const dsn = process.env.SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL!;
const schema = 'naming_ts_' + process.pid + '_' + Date.now();
const admin = new pg.Client({ connectionString: dsn });
await admin.connect();
await admin.query('CREATE SCHEMA ' + schema);
const base = new pg.Pool({ connectionString: dsn, options: '-c search_path=' + schema + ',public' });

function check(ok: boolean, what: string): void {
  if (!ok) throw new Error('check failed: ' + what);
}

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

  const food = await call('createCategory', () => orm.category.createOne({ name: 'Food' as never, kind: 'personal' as never }));
  check(food.name === 'Food' && food.createdAt instanceof Date, 'a category reads back by its fields');
  const lines = await call('createLines', () =>
    orm.lineItem.createMany([
      { amountCents: cents(-1250), postedOn: day('2026-10-25'), memo: 'Coffee', externalId: 'x1', categoryId: food.id },
      { amountCents: cents(500), postedOn: day('2026-10-26'), memo: 'Refund' },
    ])
  );
  check(lines.length === 2 && lines[0]!.categoryId === food.id, 'a line holds its category key');
  await call('getLine', () => orm.lineItem.getOne(lines[0]!.id));
  const found = await call('findLines', () => orm.lineItem.findMany({ categoryId: { eq: food.id }, searchText: 'coffee' }, { orderBy: [{ field: 'postedOn', desc: true }] }));
  check(found.total === 1 && found.rows[0]!.memo === 'Coffee', 'a filter, a search and an order on renamed columns');
  const page = await call('pageLines', () => orm.lineItem.findPage(null, { sort: { field: 'amountCents' }, limit: 1 }));
  check(page.rows[0]!.amountCents === -1250 && page.next !== null, 'a page by a renamed column');
  const updated = await call('updateLine', () => orm.lineItem.updateOne(lines[1]!.id, { categoryId: food.id, memo: null }));
  check(updated.memo === null && updated.categoryId === food.id, 'an update of renamed columns');
  const upserted = await call('upsertLine', () =>
    orm.lineItem.upsertOne({ amountCents: cents(-1300), postedOn: day('2026-10-25'), externalId: 'x1', categoryId: food.id }, ['category', 'externalId'], { amountCents: cents(-1300) })
  );
  check(upserted.id === lines[0]!.id && upserted.amountCents === -1300, 'an upsert by a unique index over renamed columns');
  await call('deleteLine', () => orm.lineItem.deleteOne(lines[1]!.id));
  const deleted = await call('findDeleted', () => orm.lineItem.findOne({ amountCents: { eq: cents(500) } }, { includeDeleted: true }));
  check(deleted?.deletedAt instanceof Date, 'a soft delete stamps the renamed deletedAt');
  // The trigger enqueued x1's posting when the line was created, and the
  // upsert's new amount replaced it; the line with no externalId posted
  // nothing. A claimed message holds a UUID hyphenated.
  const postings = postgresQueue(pool, linePostedQueue);
  const posted = await call('claimPostings', () => postings.claim(10, 60));
  check(posted.length === 1 && posted[0]!.message.lineId === uuidToDb(lines[0]!.id) && Number(posted[0]!.message.amountCents) === -1300, 'the trigger enqueued into the renamed queue table');
  await call('completePosting', () => postings.complete(posted[0]!.id, posted[0]!.token));
  await call('enqueuePosting', () => orm.transaction(tx => enqueue(tx, linePostedQueue, { lineId: lines[1]!.id, amountCents: cents(500) })));
  const enqueued = await call('claimEnqueued', () => postings.claim(10, 60));
  check(enqueued.length === 1 && enqueued[0]!.message.lineId === uuidToDb(lines[1]!.id), 'an enqueue writes the renamed columns');
  const note = await call('createNote', () => orm.note.createOne({ body: 'first' }));
  await call('updateNote', () => orm.note.updateOne(note.id, { body: 'second' }));
  await call('hardDeleteNote', () => orm.note.deleteOne(note.id));
  await call('deleteCategory', () => orm.category.deleteOne(food.id));
  writeFileSync(out!, JSON.stringify(calls, null, 2));
} finally {
  await base.end();
  await admin.query('DROP SCHEMA ' + schema + ' CASCADE');
  await admin.end();
}
`

// namingORMTest is the Go side, a test of the generated ORM module: the
// script, call for call the TypeScript side's, over a pool whose tracer
// records each statement, written as JSON to NAMING_PARITY_OUT.
const namingORMTest = `package orm

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
	types "example.com/schemas/types/go/fixture-naming-db"
)

type namingCall struct {
	Name       string   ` + "`json:\"name\"`" + `
	Statements []string ` + "`json:\"statements\"`" + `
}

type namingRecorder struct {
	mu    sync.Mutex
	calls []namingCall
}

func (r *namingRecorder) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) > 0 {
		r.calls[len(r.calls)-1].Statements = append(r.calls[len(r.calls)-1].Statements, data.SQL)
	}
	return ctx
}

func (r *namingRecorder) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (r *namingRecorder) call(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, namingCall{Name: name})
}

func TestNaming(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	out := os.Getenv("NAMING_PARITY_OUT")
	if dsn == "" || out == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL and NAMING_PARITY_OUT to run the script")
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
	schema := fmt.Sprintf("naming_go_%d", time.Now().UnixNano())
	if _, err := base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = base.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") }()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	rec := &namingRecorder{}
	cfg.ConnConfig.Tracer = rec
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, string(createSQL)); err != nil {
		t.Fatal(err)
	}
	db, err := ConnectWithPool(pool)
	if err != nil {
		t.Fatal(err)
	}
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
	check := func(ok bool, what string) {
		t.Helper()
		if !ok {
			t.Fatalf("%s: %s", rec.calls[len(rec.calls)-1].Name, what)
		}
	}
	str := func(s string) *string { return &s }
	day := func(d string) types.TemporalDate { return types.TemporalDate(d) }

	rec.call("createCategory")
	food, err := db.Category.CreateOne(ctx, &types.Category{Name: "Food", Kind: "personal"})
	must(err)
	check(food.Name == "Food" && !time.Time(food.CreatedAt).IsZero(), "a category reads back by its fields")
	rec.call("createLines")
	lines, err := db.LineItem.CreateMany(ctx, []*types.LineItem{
		{AmountCents: -1250, PostedOn: day("2026-10-25"), Memo: "Coffee", ExternalId: "x1", Category: &types.Category{Id: food.Id}},
		{AmountCents: 500, PostedOn: day("2026-10-26"), Memo: "Refund"},
	})
	must(err)
	check(len(lines) == 2 && lines[0].Category != nil && *lines[0].Category.Id == *food.Id, "a line holds its category key")
	rec.call("getLine")
	_, err = db.LineItem.GetOne(ctx, *lines[0].Id, nil)
	must(err)
	rec.call("findLines")
	found, total, err := db.LineItem.FindMany(ctx, &LineItemFilter{CategoryID: &UUIDFilter{Eq: food.Id}, SearchText: str("coffee")},
		&LineItemFindOptions{Fields: LineItemFields{}.All(), OrderBy: []LineItemOrderBy{{Field: LineItemOrderByPostedOn, Desc: true}}})
	must(err)
	check(total == 1 && found[0].Memo == "Coffee", "a filter, a search and an order on renamed columns")
	rec.call("pageLines")
	page, err := db.LineItem.FindPage(ctx, nil, &LineItemPageOptions{Fields: LineItemFields{}.All(), Sort: LineItemSort{Field: LineItemSortByAmountCents}, Limit: 1})
	must(err)
	check(page.Rows[0].AmountCents == -1250 && page.Next != nil, "a page by a renamed column")
	rec.call("updateLine")
	updated, err := db.LineItem.UpdateOne(ctx, *lines[1].Id, &LineItemUpdate{CategoryID: food.Id, MemoSetNull: true})
	must(err)
	check(updated.Memo == "" && updated.Category != nil, "an update of renamed columns")
	rec.call("upsertLine")
	minus := types.GenericInt64(-1300)
	upserted, err := db.LineItem.UpsertOne(ctx, &types.LineItem{AmountCents: -1300, PostedOn: day("2026-10-25"), ExternalId: "x1", Category: &types.Category{Id: food.Id}},
		LineItemUniqueByCategoryExternalId, &LineItemUpdate{AmountCents: &minus})
	must(err)
	check(*upserted.Id == *lines[0].Id && upserted.AmountCents == -1300, "an upsert by a unique index over renamed columns")
	rec.call("deleteLine")
	must(db.LineItem.DeleteOne(ctx, *lines[1].Id))
	rec.call("findDeleted")
	five := int64(500)
	deleted, err := db.LineItem.FindOne(ctx, &LineItemFilter{AmountCents: &Int64Filter{Eq: &five}}, &LineItemFindOptions{IncludeDeleted: true})
	must(err)
	check(deleted.DeletedAt != nil, "a soft delete stamps the renamed deletedAt")
	postings := db.LinePostedQueue()
	rec.call("claimPostings")
	posted, err := postings.Claim(ctx, 10, time.Minute)
	must(err)
	check(len(posted) == 1 && posted[0].Message.LineId == *lines[0].Id && posted[0].Message.AmountCents == -1300, "the trigger enqueued into the renamed queue table")
	rec.call("completePosting")
	must(postings.Complete(ctx, posted[0].ID, posted[0].Token))
	rec.call("enqueuePosting")
	must(db.Transaction(ctx, func(tx TxInterface) error {
		return EnqueueLinePosted(ctx, tx, &types.LinePosted{LineId: *lines[1].Id, AmountCents: 500})
	}))
	rec.call("claimEnqueued")
	enqueued, err := postings.Claim(ctx, 10, time.Minute)
	must(err)
	check(len(enqueued) == 1 && enqueued[0].Message.LineId == *lines[1].Id, "an enqueue writes the renamed columns")
	rec.call("createNote")
	note, err := db.Note.CreateOne(ctx, &types.Note{Body: "first"})
	must(err)
	rec.call("updateNote")
	second := "second"
	_, err = db.Note.UpdateOne(ctx, *note.Id, &NoteUpdate{Body: &second})
	must(err)
	rec.call("hardDeleteNote")
	must(db.Note.DeleteOne(ctx, *note.Id))
	rec.call("deleteCategory")
	must(db.Category.DeleteOne(ctx, *food.Id))

	data, err := json.MarshalIndent(rec.calls, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
`
