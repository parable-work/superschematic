package ormgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGeneratedUpsertManyOnPostgres generates the ORM module for
// fixture-sync-db, then builds, vets and tests it against the Postgres at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL (it skips without the database).
// Its generated test is sync.test.ts's in Go (D89): a sync of 500 line
// items is one statement, which reports each inserted, then updated or
// unchanged only if changed, an unchanged line keeping its updated_at and
// its trigger enqueuing nothing; a batch takes a statement each in one
// transaction, which a failed batch rolls back; UpsertOneResult compares
// a location as its text and reads back a line it left; FindMany skips its
// count; In on a time and a boolean; TransactionWith's isolation and read
// only, and Tx.Transaction's savepoints.
func TestGeneratedUpsertManyOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	ormDir := generateORMModule(t, "fixture-sync-db")
	if err := os.WriteFile(filepath.Join(ormDir, "sync_test.go"), []byte(upsertManyORMTest), 0o644); err != nil {
		t.Fatalf("write sync test: %v", err)
	}
	out := runORMModule(t, ormDir)
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") == "" {
		return
	}
	for _, name := range []string{
		"TestUpsertManyOnPostgres/a_sync_of_500_lines",
		"TestUpsertManyOnPostgres/batches",
		"TestUpsertManyOnPostgres/one_result",
		"TestUpsertManyOnPostgres/find_without_a_count",
		"TestUpsertManyOnPostgres/transactions",
	} {
		if !strings.Contains(out, "--- PASS: "+name) {
			t.Errorf("the generated ORM did not pass %s:\n%s", name, out)
		}
	}
}

const upsertManyORMTest = `package orm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	types "example.com/schemas/types/go/fixture-sync-db"
)

// statements records the text of each statement a pool sends.
type statements struct {
	mu   sync.Mutex
	sent []string
}

func (s *statements) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, data.SQL)
	return ctx
}

func (s *statements) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// take returns the statements sent since the last take, and forgets them.
func (s *statements) take() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.sent
	s.sent = nil
	return out
}

// starting are the statements that begin with prefix.
func starting(sent []string, prefix string) []string {
	var out []string
	for _, st := range sent {
		if strings.HasPrefix(st, prefix) {
			out = append(out, st)
		}
	}
	return out
}

func TestUpsertManyOnPostgres(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to upsert on Postgres")
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
	schema := fmt.Sprintf("upsert_many_%d", time.Now().UnixNano())
	if _, err := base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = base.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") }()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	rec := &statements{}
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
	account, err := db.BankAccount.CreateOne(ctx, &types.BankAccount{Code: "chk", Name: "Checking"})
	if err != nil {
		t.Fatal(err)
	}
	messages := func() int {
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM line_item_written_queue").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	clear := func() {
		if _, err := pool.Exec(ctx, "DELETE FROM line_item_written_queue"); err != nil {
			t.Fatal(err)
		}
	}
	lines := func(prefix string, n int) []*types.LineItem {
		out := make([]*types.LineItem, n)
		for i := range out {
			out[i] = &types.LineItem{
				PlaidTransactionId: fmt.Sprintf("%s-%d", prefix, i),
				Account:            types.BankAccount{Id: account.Id},
				Reference:          fmt.Sprintf("%s-ref-%d", prefix, i),
				Name:               fmt.Sprintf("Line %d", i),
				Amount:             types.FinanceSignedMoney(-100 - i),
				PostedAt:           types.TemporalDateTime(time.Date(2026, 10, 1+i%28, 0, 0, 0, 0, time.UTC)),
				Tags:               []string{},
			}
		}
		return out
	}
	fields := LineItemFields{Name: true, Amount: true, PostedAt: true}
	statuses := func(results []UpsertResult[*types.LineItem]) map[UpsertStatus]int {
		out := map[UpsertStatus]int{}
		for _, r := range results {
			out[r.Status]++
		}
		return out
	}

	t.Run("a sync of 500 lines", func(t *testing.T) {
		batch := lines("txn", 500)
		clear()
		rec.take()
		first, err := db.LineItem.UpsertMany(ctx, batch, LineItemUniqueByPlaidTransactionId, fields, &UpsertOptions{OnlyIfChanged: true})
		if err != nil {
			t.Fatal(err)
		}
		if inserts := starting(rec.take(), "INSERT"); len(inserts) != 1 {
			t.Fatalf("a sync of 500 lines sent %d inserts, want one statement", len(inserts))
		}
		if got := statuses(first); got[UpsertInserted] != 500 || len(first) != 500 {
			t.Fatalf("the first sync = %v, want 500 inserted", got)
		}
		for i, r := range first {
			if r.Row.PlaidTransactionId != batch[i].PlaidTransactionId {
				t.Fatalf("result %d is line %s, want its input's %s", i, r.Row.PlaidTransactionId, batch[i].PlaidTransactionId)
			}
		}
		if n := messages(); n != 500 {
			t.Fatalf("the first sync enqueued %d messages, want 500", n)
		}

		// A sync again, three lines of which the bank changed.
		clear()
		rec.take()
		changed := func(i int) bool { return i%200 == 7 }
		again := lines("txn", 500)
		for i := range again {
			if changed(i) {
				again[i].Amount--
			}
		}
		second, err := db.LineItem.UpsertMany(ctx, again, LineItemUniqueByPlaidTransactionId, fields, &UpsertOptions{OnlyIfChanged: true})
		if err != nil {
			t.Fatal(err)
		}
		sent := rec.take()
		if len(starting(sent, "INSERT")) != 1 || len(starting(sent, "SELECT")) != 1 {
			t.Fatalf("the second sync sent %q, want an insert and a read of the lines it left", sent)
		}
		for i, r := range second {
			want := UpsertUnchanged
			if changed(i) {
				want = UpsertUpdated
			}
			if r.Status != want || r.Row.PlaidTransactionId != batch[i].PlaidTransactionId {
				t.Fatalf("line %d = %s %s, want %s", i, r.Row.PlaidTransactionId, r.Status, want)
			}
			if !changed(i) && !time.Time(r.Row.UpdatedAt).Equal(time.Time(first[i].Row.UpdatedAt)) {
				t.Fatalf("line %d's updated_at moved from %v to %v, though it was unchanged", i, first[i].Row.UpdatedAt, r.Row.UpdatedAt)
			}
		}
		if n := messages(); n != 3 {
			t.Fatalf("the second sync enqueued %d messages, want 3", n)
		}

		// Without OnlyIfChanged every line is written, and enqueues.
		clear()
		third, err := db.LineItem.UpsertMany(ctx, again, LineItemUniqueByPlaidTransactionId, fields, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := statuses(third); got[UpsertUpdated] != 500 {
			t.Fatalf("the third sync = %v, want 500 updated", got)
		}
		if n := messages(); n != 500 {
			t.Fatalf("the third sync enqueued %d messages, want 500", n)
		}
	})

	t.Run("batches", func(t *testing.T) {
		manual := lines("manual", 1)[0]
		manual.PlaidTransactionId = ""
		batch := append(lines("chunk", 4), manual)
		rec.take()
		first, err := db.LineItem.UpsertMany(ctx, batch, LineItemUniqueByPlaidTransactionId, fields, &UpsertOptions{Batch: 2})
		if err != nil {
			t.Fatal(err)
		}
		if inserts := starting(rec.take(), "INSERT"); len(inserts) != 3 {
			t.Fatalf("5 lines in batches of 2 sent %d inserts, want 3", len(inserts))
		}
		if got := statuses(first); got[UpsertInserted] != 5 || first[4].Row.Reference != "manual-ref-0" || first[4].Row.PlaidTransactionId != "" {
			t.Fatalf("the batches = %v, the last %+v; want 5 inserted, the manual line last", got, first[4].Row)
		}
		// A line without the bank's id holds no key, so it is inserted
		// again, and the unique reference refuses it: nothing is written.
		_, before, err := db.LineItem.FindMany(ctx, nil, &LineItemFindOptions{Fields: LineItemFields{Id: true}, Limit: 1000})
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range batch {
			l.Name += " again"
		}
		if _, err := db.LineItem.UpsertMany(ctx, batch, LineItemUniqueByPlaidTransactionId, fields, &UpsertOptions{Batch: 2}); err == nil || !strings.Contains(err.Error(), "line_item") {
			t.Fatalf("a batch with a line the reference refuses = %v, want the reference's violation", err)
		}
		_, after, err := db.LineItem.FindMany(ctx, nil, &LineItemFindOptions{Fields: LineItemFields{Id: true}, Limit: 1000})
		if err != nil || after != before {
			t.Fatalf("after the failed batch %d lines, %v; want %d", after, err, before)
		}
		byReference, err := db.LineItem.UpsertMany(ctx, batch, LineItemUniqueByAccountReference, LineItemFields{Name: true}, nil)
		if err != nil || statuses(byReference)[UpsertUpdated] != 5 || byReference[0].Row.Name != "Line 0 again" {
			t.Fatalf("an upsert by account and reference = %v, %v; want 5 updated", statuses(byReference), err)
		}
		if _, err := db.LineItem.UpsertMany(ctx, batch, LineItemUniqueByPlaidTransactionId, LineItemFields{Id: true}, nil); err == nil || !strings.Contains(err.Error(), "not id") {
			t.Fatalf("an upsert that sets the key = %v, want it refused", err)
		}
		if _, err := db.LineItem.UpsertMany(ctx, batch, LineItemUniqueByPlaidTransactionId, fields, &UpsertOptions{Batch: -1}); err == nil {
			t.Fatal("a batch of -1 rows was not refused")
		}
	})

	t.Run("one result", func(t *testing.T) {
		input := func() *types.LineItem {
			l := lines("one", 1)[0]
			l.Location = &types.GeoLocation{Lat: 51.5, Lon: -0.125}
			details := types.GenericJSON("{\"merchant\": \"Cafe\"}")
			l.Details = &details
			return l
		}
		update := func(l *types.LineItem) *LineItemUpdate {
			return &LineItemUpdate{Name: &l.Name, Location: l.Location, Details: l.Details}
		}
		in := input()
		inserted, err := db.LineItem.UpsertOneResult(ctx, in, LineItemUniqueByPlaidTransactionId, update(in), &UpsertOptions{OnlyIfChanged: true})
		if err != nil || inserted.Status != UpsertInserted {
			t.Fatalf("the first upsert = %+v, %v; want inserted", inserted, err)
		}
		rec.take()
		in = input()
		unchanged, err := db.LineItem.UpsertOneResult(ctx, in, LineItemUniqueByPlaidTransactionId, update(in), &UpsertOptions{OnlyIfChanged: true})
		if err != nil || unchanged.Status != UpsertUnchanged || *unchanged.Row.Id != *inserted.Row.Id || !time.Time(unchanged.Row.UpdatedAt).Equal(time.Time(inserted.Row.UpdatedAt)) {
			t.Fatalf("the same upsert = %+v, %v; want the line unchanged", unchanged, err)
		}
		sent := rec.take()
		if inserts := starting(sent, "INSERT"); len(inserts) != 1 || !strings.Contains(inserts[0], "(line_item.\"name\", CAST(line_item.location AS POINT)::text, line_item.details) IS DISTINCT FROM ($12, CAST($13 AS POINT)::text, $14)") {
			t.Fatalf("the upsert only if changed sent %q", sent)
		}
		in = input()
		in.Location = &types.GeoLocation{Lat: 51.5, Lon: 0}
		moved, err := db.LineItem.UpsertOneResult(ctx, in, LineItemUniqueByPlaidTransactionId, update(in), &UpsertOptions{OnlyIfChanged: true})
		if err != nil || moved.Status != UpsertUpdated || moved.Row.Location.Lon != 0 {
			t.Fatalf("a moved line = %+v, %v; want updated", moved, err)
		}
		kept, err := db.LineItem.UpsertOneResult(ctx, input(), LineItemUniqueByPlaidTransactionId, nil, nil)
		if err != nil || kept.Status != UpsertUnchanged {
			t.Fatalf("an upsert that sets nothing = %+v, %v; want unchanged", kept, err)
		}
		if err := db.LineItem.DeleteOne(ctx, *moved.Row.Id); err != nil {
			t.Fatal(err)
		}
		in = input()
		fresh, err := db.LineItem.UpsertOneResult(ctx, in, LineItemUniqueByPlaidTransactionId, update(in), &UpsertOptions{OnlyIfChanged: true})
		if err != nil || fresh.Status != UpsertInserted || *fresh.Row.Id == *moved.Row.Id {
			t.Fatalf("an upsert after the delete = %+v, %v; want a new line", fresh, err)
		}
		in = input()
		in.Id = moved.Row.Id
		if _, err := db.LineItem.UpsertOneResult(ctx, in, LineItemUniqueById, update(in), &UpsertOptions{OnlyIfChanged: true}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("an upsert by a deleted line's key = %v, want ErrNotFound", err)
		}
	})

	t.Run("find without a count", func(t *testing.T) {
		if _, err := db.LineItem.UpsertMany(ctx, lines("find", 3), LineItemUniqueByPlaidTransactionId, fields, nil); err != nil {
			t.Fatal(err)
		}
		rec.take()
		no := false
		found, total, err := db.LineItem.FindMany(ctx, &LineItemFilter{
			PlaidTransactionId: &StringFilter{In: []string{"find-0", "find-1", "find-2"}},
			PostedAt:           &DateTimeFilter{In: []time.Time{time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)}},
			Pending:            &BoolFilter{In: []bool{no}},
		}, &LineItemFindOptions{Fields: LineItemFields{PlaidTransactionId: true}, SkipCount: true})
		if err != nil || total != -1 || len(found) != 2 {
			t.Fatalf("lines of two days = %d, %d, %v; want 2 and no count", len(found), total, err)
		}
		sent := rec.take()
		if len(sent) != 1 || !strings.Contains(sent[0], "line_item.posted_at IN ($4, $5) AND line_item.pending IN ($6)") {
			t.Fatalf("FindMany without a count sent %q", sent)
		}
	})

	t.Run("transactions", func(t *testing.T) {
		rec.take()
		var level string
		if err := db.TransactionWith(ctx, TxOptions{Isolation: Serializable, ReadOnly: true}, func(tx TxInterface) error {
			rows, err := tx.Query(ctx, "SHOW transaction_isolation")
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				if err := rows.Scan(&level); err != nil {
					return err
				}
			}
			return rows.Err()
		}); err != nil || level != "serializable" {
			t.Fatalf("a serializable transaction ran at %q, %v", level, err)
		}
		if sent := rec.take(); sent[0] != "BEGIN ISOLATION LEVEL SERIALIZABLE READ ONLY" {
			t.Fatalf("the transaction began with %q", sent[0])
		}
		if err := db.TransactionWith(ctx, TxOptions{ReadOnly: true}, func(tx TxInterface) error {
			_, err := tx.GetBankAccountRepository().CreateOne(ctx, &types.BankAccount{Code: "read-only", Name: "Nope"})
			return err
		}); err == nil || !strings.Contains(err.Error(), "read-only") {
			t.Fatalf("a write in a read-only transaction = %v", err)
		}
		if err := db.TransactionWith(ctx, TxOptions{Isolation: "snapshot"}, func(TxInterface) error { return nil }); err == nil {
			t.Fatal("a snapshot transaction was not refused")
		}
		rec.take()
		if err := db.Transaction(ctx, func(tx TxInterface) error {
			if _, err := tx.GetBankAccountRepository().CreateOne(ctx, &types.BankAccount{Code: "kept", Name: "Kept"}); err != nil {
				return err
			}
			err := tx.Transaction(ctx, func(inner TxInterface) error {
				if _, err := inner.GetBankAccountRepository().CreateOne(ctx, &types.BankAccount{Code: "dropped", Name: "Dropped"}); err != nil {
					return err
				}
				if err := inner.Transaction(ctx, func(innermost TxInterface) error {
					_, err := innermost.GetBankAccountRepository().CreateOne(ctx, &types.BankAccount{Code: "deeper", Name: "Deeper"})
					return err
				}); err != nil {
					return err
				}
				_, err := inner.GetBankAccountRepository().CreateOne(ctx, &types.BankAccount{Code: "dropped", Name: "Again"})
				return err
			})
			if err == nil || !strings.Contains(err.Error(), "duplicate key") {
				return fmt.Errorf("the savepoint's duplicate = %v", err)
			}
			// The transaction goes on after the savepoint's failed statement.
			_, err = tx.GetBankAccountRepository().CreateOne(ctx, &types.BankAccount{Code: "after", Name: "After"})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		var savepoints []string
		for _, st := range rec.take() {
			if strings.Contains(st, "SAVEPOINT") {
				savepoints = append(savepoints, st)
			}
		}
		if want := "SAVEPOINT sp_1|SAVEPOINT sp_2|RELEASE SAVEPOINT sp_2|ROLLBACK TO SAVEPOINT sp_1|RELEASE SAVEPOINT sp_1"; strings.Join(savepoints, "|") != want {
			t.Fatalf("the savepoints = %q, want %q", savepoints, want)
		}
		found, _, err := db.BankAccount.FindMany(ctx, &BankAccountFilter{Code: &StringFilter{In: []string{"kept", "dropped", "deeper", "after"}}}, &BankAccountFindOptions{Fields: BankAccountFields{Code: true}, OrderBy: []BankAccountOrderBy{{Field: BankAccountOrderByCode}}})
		if err != nil || len(found) != 2 || found[0].Code != "after" || found[1].Code != "kept" {
			t.Fatalf("the accounts the transaction kept = %+v, %v; want after and kept", found, err)
		}
	})
}
`
