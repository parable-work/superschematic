package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	rules "github.com/parable-work/superschematic/runtime/rules/go"
	"github.com/parable-work/superschematic/runtime/rules/go/internal/rulestest"
	"github.com/parable-work/superschematic/runtime/rules/go/postgres"
)

// The Postgres store over a transaction of the Postgres
// SUPERSCHEMATIC_RULES_TEST_DATABASE_URL names, each statement it sends
// recorded: rules.test.ts's "the Postgres store" cases, on a real
// database. They skip without the variable.

// recordingTx records the text of each statement sent through a pgx.Tx.
type recordingTx struct {
	tx         pgx.Tx
	statements []string
}

func (r *recordingTx) Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	r.statements = append(r.statements, sql)
	return r.tx.Exec(ctx, sql, arguments...)
}

func (r *recordingTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	r.statements = append(r.statements, sql)
	return r.tx.Query(ctx, sql, args...)
}

// withStore runs fn on a store over a transaction of db, recording what
// it sends, and commits.
func withStore(t *testing.T, db *rulestest.DB, fn func(ctx context.Context, store rules.Store, rec *recordingTx, tx *rulestest.Tx)) {
	t.Helper()
	err := db.InTx(context.Background(), func(tx *rulestest.Tx) error {
		rec := &recordingTx{tx: tx.Postgres()}
		fn(context.Background(), postgres.NewStore(rec), rec, tx)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func setup(t *testing.T) *rulestest.DB {
	t.Helper()
	db := rulestest.Postgres(t)
	db.Exec(t, `CREATE TABLE t (id TEXT PRIMARY KEY, v BIGINT NOT NULL)`)
	db.Exec(t, `INSERT INTO t (id, v) VALUES ('a', 0), ('b', 0)`)
	return db
}

func update(id string, v int) rules.Statement {
	return rules.Statement{Text: `UPDATE t SET v = $1 WHERE id = $2`, Values: []any{v, id}, Expect: 1}
}

func values(t *testing.T, db *rulestest.DB) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	for _, row := range db.Rows(t, `SELECT id, v FROM t`) {
		out[row[0].(string)] = row[1].(int64)
	}
	return out
}

func want(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("statements = %q\nwant         %q", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("statement %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestTheStoreSpeaksPostgresAndHoldsItsLocks(t *testing.T) {
	store := postgres.NewStore(&recordingTx{})
	if store.Dialect() != rules.DialectPostgres || store.Locks() != rules.LocksHeld {
		t.Errorf("store = %s, %s; want postgres, held", store.Dialect(), store.Locks())
	}
}

func TestAUnitAppliesInASavepointItReleases(t *testing.T) {
	db := setup(t)
	withStore(t, db, func(ctx context.Context, store rules.Store, rec *recordingTx, _ *rulestest.Tx) {
		applied, err := store.Apply(ctx, []rules.Statement{update("a", 1), update("b", 2)})
		if err != nil || !applied {
			t.Fatalf("Apply = %v, %v; want applied", applied, err)
		}
		want(t, rec.statements, "SAVEPOINT rules_apply", `UPDATE t SET v = $1 WHERE id = $2`, `UPDATE t SET v = $1 WHERE id = $2`, "RELEASE SAVEPOINT rules_apply")
	})
	if got := values(t, db); got["a"] != 1 || got["b"] != 2 {
		t.Errorf("rows = %v", got)
	}
}

func TestAUnitThatChangesOtherThanTheRowsItExpectsRollsBack(t *testing.T) {
	db := setup(t)
	withStore(t, db, func(ctx context.Context, store rules.Store, rec *recordingTx, _ *rulestest.Tx) {
		applied, err := store.Apply(ctx, []rules.Statement{update("a", 1), update("missing", 2), update("b", 3)})
		if err != nil || applied {
			t.Fatalf("Apply = %v, %v; want moved", applied, err)
		}
		want(t, rec.statements, "SAVEPOINT rules_apply", `UPDATE t SET v = $1 WHERE id = $2`, `UPDATE t SET v = $1 WHERE id = $2`, "ROLLBACK TO SAVEPOINT rules_apply", "RELEASE SAVEPOINT rules_apply")
		// A write that may change any number of rows changes none and
		// holds.
		rec.statements = nil
		applied, err = store.Apply(ctx, []rules.Statement{{Text: `UPDATE t SET v = 9 WHERE id = 'missing'`, Expect: rules.AnyRows}})
		if err != nil || !applied {
			t.Errorf("Apply of AnyRows = %v, %v; want applied", applied, err)
		}
	})
	if got := values(t, db); got["a"] != 0 || got["b"] != 0 {
		t.Errorf("rows = %v, want neither written", got)
	}
}

func TestAUnitWithAFailedStatementRollsBackAndReturnsTheError(t *testing.T) {
	db := setup(t)
	withStore(t, db, func(ctx context.Context, store rules.Store, rec *recordingTx, tx *rulestest.Tx) {
		_, err := store.Apply(ctx, []rules.Statement{update("a", 1), {Text: `INSERT INTO t (id, v) VALUES ('b', 0)`, Expect: 1}})
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
			t.Fatalf("err = %v, want the unique violation, wrapped", err)
		}
		want(t, rec.statements, "SAVEPOINT rules_apply", `UPDATE t SET v = $1 WHERE id = $2`, `INSERT INTO t (id, v) VALUES ('b', 0)`, "ROLLBACK TO SAVEPOINT rules_apply", "RELEASE SAVEPOINT rules_apply")
		// The transaction goes on.
		if _, err := tx.Exec(ctx, `UPDATE t SET v = 5 WHERE id = 'b'`); err != nil {
			t.Fatalf("the transaction after a failed unit: %v", err)
		}
	})
	if got := values(t, db); got["a"] != 0 || got["b"] != 5 {
		t.Errorf("rows = %v, want a as it was and b written after", got)
	}
}

func TestACheckRunsAsASelectAndAFalseOneRollsBack(t *testing.T) {
	db := setup(t)
	check := func(id string) rules.Statement {
		return rules.Statement{Text: `EXISTS (SELECT 1 FROM t WHERE id = $1)`, Values: []any{id}, Check: true}
	}
	withStore(t, db, func(ctx context.Context, store rules.Store, rec *recordingTx, _ *rulestest.Tx) {
		if applied, err := store.Apply(ctx, []rules.Statement{check("a"), update("a", 1)}); err != nil || !applied {
			t.Fatalf("Apply = %v, %v; want applied", applied, err)
		}
		if applied, err := store.Apply(ctx, []rules.Statement{check("missing"), update("b", 2)}); err != nil || applied {
			t.Fatalf("Apply = %v, %v; want moved", applied, err)
		}
		want(t, rec.statements,
			"SAVEPOINT rules_apply",
			`SELECT (EXISTS (SELECT 1 FROM t WHERE id = $1)) AS held`,
			`UPDATE t SET v = $1 WHERE id = $2`,
			"RELEASE SAVEPOINT rules_apply",
			"SAVEPOINT rules_apply",
			`SELECT (EXISTS (SELECT 1 FROM t WHERE id = $1)) AS held`,
			"ROLLBACK TO SAVEPOINT rules_apply",
			"RELEASE SAVEPOINT rules_apply",
		)
	})
	if got := values(t, db); got["a"] != 1 || got["b"] != 0 {
		t.Errorf("rows = %v", got)
	}
}

func TestALockIsTheTransactionsAdvisoryLockAndAnEmptyUnitRunsNothing(t *testing.T) {
	db := setup(t)
	key := "Stock/sku-1/" + db.Schema
	taken := func() bool {
		rows := db.Rows(t, `SELECT pg_try_advisory_xact_lock(hashtextextended($1, 0))`, key)
		return rows[0][0].(bool)
	}
	withStore(t, db, func(ctx context.Context, store rules.Store, rec *recordingTx, _ *rulestest.Tx) {
		if err := store.Lock(ctx, key); err != nil {
			t.Fatal(err)
		}
		if applied, err := store.Apply(ctx, nil); err != nil || !applied {
			t.Fatalf("an empty unit = %v, %v", applied, err)
		}
		want(t, rec.statements, postgres.LockStatement)
		if taken() {
			t.Error("another connection took the lock the transaction holds")
		}
	})
	if !taken() {
		t.Error("the lock outlived the transaction")
	}
}

func TestQueryReadsEachRowsValues(t *testing.T) {
	db := setup(t)
	withStore(t, db, func(ctx context.Context, store rules.Store, _ *recordingTx, _ *rulestest.Tx) {
		rows, err := store.Query(ctx, `SELECT id, v, NULL::text AS n FROM t ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 2 || rows[0][0] != "a" || rows[0][1] != int64(0) || rows[0][2] != nil {
			t.Errorf("rows = %v", rows)
		}
	})
}
