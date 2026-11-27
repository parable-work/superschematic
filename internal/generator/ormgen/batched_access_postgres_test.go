package ormgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// batchedAccessORMCases are the cases batchedAccessORMTest runs over
// fixture-merge-access-db, by name.
var batchedAccessORMCases = []string{
	"GetManyByIDs reads only the rows the caller sees, in one statement",
	"a party's CreateMany of a row its grant does not reach is refused, and writes nothing",
	"CreateMany past the parameters of one statement writes every row, in order, or none",
	"GetManyByIDs of more keys than one statement binds reads them a query at a time",
}

// TestBatchedReadsAndWritesOnPostgres holds the generated Go ORM's reads
// and writes of many rows to one statement where they used to run one a
// row: GetManyByIDs of a table held by per-record access (D156) puts the
// caller's conditions in its own statement, and reads more keys than one
// statement binds a statement at a time, CreateMany checks the reach of
// the rows a party's create made in one read, inside the create's
// transaction, and CreateMany writes more rows than one statement binds
// the parameters of in statements of a batch each, in one transaction. It
// loads fixture-merge-access-db, generates its Go types, ORM and DDL into a
// temporary tree, and runs batchedAccessORMTest on the Postgres at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL; every case must pass. It skips
// without the database and in -short mode.
func TestBatchedReadsAndWritesOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated ORM's batched reads and writes in -short mode")
	}
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the generated ORM's batched reads and writes against Postgres")
	}
	ormDir := generateORMModule(t, "fixture-merge-access-db")
	if err := os.WriteFile(filepath.Join(ormDir, "batched_access_test.go"), []byte(batchedAccessORMTest), 0o644); err != nil {
		t.Fatalf("write the test: %v", err)
	}
	out := runORMModule(t, ormDir)
	for _, name := range batchedAccessORMCases {
		if want := "--- PASS: TestBatchedReadsAndWrites/" + strings.ReplaceAll(name, " ", "_"); !strings.Contains(out, want) {
			t.Errorf("the generated module's test did not pass %q:\n%s", name, out)
		}
	}
}

// batchedAccessORMTest is the generated module's test. Its pool counts the
// statements it runs.
const batchedAccessORMTest = `package orm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	types "example.com/schemas/types/go/fixture-merge-access-db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/parable-work/superschematic/runtime/schema/go/access"
)

// statements counts the statements a pool runs.
type statements struct{ n atomic.Int64 }

func (s *statements) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	s.n.Add(1)
	return ctx
}

func (s *statements) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestBatchedReadsAndWrites(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run against Postgres")
	}
	createSQL, err := os.ReadFile("testdata/create.sql")
	if err != nil {
		t.Fatalf("read create.sql: %v", err)
	}
	background := context.Background()
	base, err := pgxpool.New(background, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer base.Close()
	schema := fmt.Sprintf("ormgen_batched_access_%d", time.Now().UnixNano())
	if _, err := base.Exec(background, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	defer func() { _, _ = base.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") }()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	counted := &statements{}
	cfg.ConnConfig.Tracer = counted
	pool, err := pgxpool.NewWithConfig(background, cfg)
	if err != nil {
		t.Fatalf("connect schema pool: %v", err)
	}
	if _, err := pool.Exec(background, string(createSQL)); err != nil {
		pool.Close()
		t.Fatalf("apply create.sql: %v", err)
	}
	db, err := ConnectWithPool(pool)
	if err != nil {
		pool.Close()
		t.Fatalf("connect generated orm: %v", err)
	}
	defer db.Close()

	id := func(t *testing.T, u uuid.UUID) types.IdentityUUID {
		t.Helper()
		parsed, err := types.ParseIdentityUUID(u.String())
		if err != nil {
			t.Fatalf("an id: %v", err)
		}
		return parsed
	}
	ada, bob, party := uuid.New(), uuid.New(), uuid.New()
	// No principal: the writes that set the rows up read and write every
	// row, as a job's do.
	ctx := WithUserID(background, id(t, ada))
	asUser := func(who uuid.UUID) context.Context {
		return WithPrincipal(ctx, access.Principal{Subject: who.String(), Kind: access.KindUser})
	}
	label := func(t *testing.T, name string, owner uuid.UUID, audience string) types.IdentityUUID {
		t.Helper()
		row, err := db.Label.CreateOne(ctx, &types.Label{Name: name, Owner: id(t, owner), Audience: audience, AgentAccess: "readOnly"})
		if err != nil {
			t.Fatalf("create the label %s: %v", name, err)
		}
		return *row.Id
	}
	held := func(t *testing.T, where string, args ...any) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM label WHERE "+where, args...).Scan(&n); err != nil {
			t.Fatalf("count the labels: %v", err)
		}
		return n
	}

	t.Run("GetManyByIDs reads only the rows the caller sees, in one statement", func(t *testing.T) {
		mine, theirs, open := label(t, "mine", ada, "private"), label(t, "theirs", bob, "private"), label(t, "open", bob, "everyone")
		missing := id(t, uuid.New())
		ids := []types.IdentityUUID{mine, theirs, open, missing, mine}
		for _, tc := range []struct {
			name string
			ctx  context.Context
			want []types.IdentityUUID
		}{
			{"ada", asUser(ada), []types.IdentityUUID{mine, open}},
			{"bob", asUser(bob), []types.IdentityUUID{theirs, open}},
			{"no principal", ctx, []types.IdentityUUID{mine, theirs, open}},
			{"the system actor", AsSystem(asUser(ada)), []types.IdentityUUID{mine, theirs, open}},
		} {
			before := counted.n.Load()
			rows, err := db.Label.GetManyByIDs(tc.ctx, ids)
			if err != nil {
				t.Fatalf("%s's GetManyByIDs: %v", tc.name, err)
			}
			if ran := counted.n.Load() - before; ran != 1 {
				t.Errorf("%s's GetManyByIDs ran %d statements, want 1", tc.name, ran)
			}
			if len(rows) != len(tc.want) {
				t.Errorf("%s's GetManyByIDs read %d rows, want %d", tc.name, len(rows), len(tc.want))
			}
			for _, want := range tc.want {
				if row, ok := rows[want]; !ok || *row.Id != want {
					t.Errorf("%s's GetManyByIDs missed %v", tc.name, want)
				}
			}
		}
	})

	t.Run("a party's CreateMany of a row its grant does not reach is refused, and writes nothing", func(t *testing.T) {
		reached, other := id(t, uuid.New()), id(t, uuid.New())
		asParty := WithPrincipal(ctx, access.Principal{
			Subject: party.String(),
			Kind:    access.KindParty,
			Grants:  []access.GrantReach{{ID: "g1", TargetType: "Label", TargetID: reached.String()}},
		})
		row := func(key types.IdentityUUID, name string) *types.Label {
			return &types.Label{Id: &key, Name: name, Owner: id(t, party), Audience: "everyone", AgentAccess: "readOnly"}
		}
		// No transaction is open: the create's own rolls back.
		if _, err := db.Label.CreateMany(asParty, []*types.Label{row(reached, "reached"), row(other, "other")}); !errors.Is(err, ErrAccessRefused) {
			t.Fatalf("a create of a row the grant does not reach: %v, want ErrAccessRefused", err)
		}
		if n := held(t, "id = ANY($1::uuid[])", []any{reached.ToUUID(), other.ToUUID()}); n != 0 {
			t.Fatalf("the refused create kept %d rows", n)
		}
		// In the caller's transaction, the refusal rolls it back.
		err := db.Transaction(asParty, func(tx TxInterface) error {
			_, err := tx.GetLabelRepository().CreateMany(asParty, []*types.Label{row(other, "other"), row(reached, "reached")})
			return err
		})
		if !errors.Is(err, ErrAccessRefused) {
			t.Fatalf("a create in a transaction of a row the grant does not reach: %v, want ErrAccessRefused", err)
		}
		if n := held(t, "id = ANY($1::uuid[])", []any{reached.ToUUID(), other.ToUUID()}); n != 0 {
			t.Fatalf("the refused create kept %d rows", n)
		}
		created, err := db.Label.CreateMany(asParty, []*types.Label{row(reached, "reached")})
		if err != nil || len(created) != 1 || *created[0].Id != reached {
			t.Fatalf("a create of the row the grant reaches: %v, %v", created, err)
		}
	})

	var bulkIDs []types.IdentityUUID
	t.Run("CreateMany past the parameters of one statement writes every row, in order, or none", func(t *testing.T) {
		// Four parameters a row: more rows than one statement binds.
		const rows = 20000
		bulk := func(prefix string) []*types.Label {
			inputs := make([]*types.Label, rows)
			for i := range inputs {
				inputs[i] = &types.Label{Name: fmt.Sprintf("%s-%05d", prefix, i), Owner: id(t, ada), Audience: "everyone", AgentAccess: "readOnly"}
			}
			return inputs
		}
		before := counted.n.Load()
		created, err := db.Label.CreateMany(ctx, bulk("bulk"))
		if err != nil {
			t.Fatalf("CreateMany of %d rows: %v", rows, err)
		}
		// A statement a batch, and the transaction's BEGIN and COMMIT.
		if ran := counted.n.Load() - before; ran < 3 {
			t.Errorf("CreateMany of %d rows ran %d statements, want a batch each", rows, ran)
		}
		if len(created) != rows {
			t.Fatalf("CreateMany returned %d rows, want %d", len(created), rows)
		}
		for _, row := range created {
			bulkIDs = append(bulkIDs, *row.Id)
		}
		for i, row := range created {
			if want := fmt.Sprintf("bulk-%05d", i); row.Name != want || row.Id == nil {
				t.Fatalf("row %d is %q, want %q", i, row.Name, want)
			}
		}
		if n := held(t, "name LIKE 'bulk-%'"); n != rows {
			t.Fatalf("the table holds %d of the rows, want %d", n, rows)
		}
		// The last row repeats the first's key: its batch fails, and the
		// batches before it roll back with it.
		failing := bulk("dup")
		key := id(t, uuid.New())
		failing[0].Id, failing[rows-1].Id = &key, &key
		if _, err := db.Label.CreateMany(ctx, failing); err == nil {
			t.Fatal("CreateMany of a key twice wrote")
		}
		if n := held(t, "name LIKE 'dup-%'"); n != 0 {
			t.Fatalf("the failed CreateMany kept %d rows", n)
		}
	})

	t.Run("GetManyByIDs of more keys than one statement binds reads them a query at a time", func(t *testing.T) {
		if len(bulkIDs) == 0 {
			t.Skip("the bulk create made no rows")
		}
		// 70,000 keys, the bulk rows among keys of no row, read by a user
		// whose audience binds a parameter of its own: two queries.
		ids := append([]types.IdentityUUID{}, bulkIDs...)
		for len(ids) < 70000 {
			ids = append(ids, id(t, uuid.New()))
		}
		before := counted.n.Load()
		rows, err := db.Label.GetManyByIDs(asUser(bob), ids)
		if err != nil {
			t.Fatalf("GetManyByIDs of %d keys: %v", len(ids), err)
		}
		if ran := counted.n.Load() - before; ran != 2 {
			t.Errorf("GetManyByIDs of %d keys ran %d statements, want 2", len(ids), ran)
		}
		if len(rows) != len(bulkIDs) {
			t.Fatalf("GetManyByIDs read %d rows, want %d", len(rows), len(bulkIDs))
		}
		for _, want := range bulkIDs {
			if _, ok := rows[want]; !ok {
				t.Fatalf("GetManyByIDs missed %v", want)
			}
		}
	})
}
`
