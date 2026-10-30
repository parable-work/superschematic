package sqlite_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	rules "github.com/parable-work/superschematic/runtime/rules/go"
	"github.com/parable-work/superschematic/runtime/rules/go/internal/rulestest"
	"github.com/parable-work/superschematic/runtime/rules/go/sqlite"
)

// The SQLite store over a transaction of a SQLite file of the test's own,
// each statement it sends recorded: the unit in a savepoint, each write's
// rows changed and each check fenced.

// recordingTx records the text of each statement sent through a *sql.Tx.
type recordingTx struct {
	tx         *sql.Tx
	statements []string
}

func (r *recordingTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	r.statements = append(r.statements, query)
	return r.tx.ExecContext(ctx, query, args...)
}

func (r *recordingTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	r.statements = append(r.statements, query)
	return r.tx.QueryContext(ctx, query, args...)
}

func withStore(t *testing.T, db *rulestest.DB, fn func(ctx context.Context, store rules.Store, rec *recordingTx, tx *rulestest.Tx)) {
	t.Helper()
	err := db.InTx(context.Background(), func(tx *rulestest.Tx) error {
		rec := &recordingTx{tx: tx.SQLite()}
		fn(context.Background(), sqlite.NewStore(rec), rec, tx)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func setup(t *testing.T) *rulestest.DB {
	t.Helper()
	db := rulestest.SQLite(t)
	db.Exec(t, `CREATE TABLE t (id TEXT PRIMARY KEY, v INTEGER NOT NULL, _version INTEGER DEFAULT 1 NOT NULL)`)
	for _, ddl := range db.BumpVersion("t") {
		db.Exec(t, ddl)
	}
	db.Exec(t, `INSERT INTO t (id, v) VALUES ('a', 0), ('b', 0)`)
	return db
}

func update(id string, v int) rules.Statement {
	return rules.Statement{Text: `UPDATE t SET v = ?1 WHERE id = ?2`, Values: []any{v, id}, Expect: 1}
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

func TestTheStoreSpeaksSQLiteAndFencesItsLocks(t *testing.T) {
	db := setup(t)
	withStore(t, db, func(ctx context.Context, store rules.Store, rec *recordingTx, _ *rulestest.Tx) {
		if store.Dialect() != rules.DialectSQLite || store.Locks() != rules.LocksFenced {
			t.Errorf("store = %s, %s; want sqlite, fenced", store.Dialect(), store.Locks())
		}
		// A lock takes nothing, and an empty unit runs no statement.
		if err := store.Lock(ctx, "Stock/sku-1"); err != nil {
			t.Fatal(err)
		}
		if applied, err := store.Apply(ctx, nil); err != nil || !applied {
			t.Fatalf("an empty unit = %v, %v", applied, err)
		}
		want(t, rec.statements)
	})
}

func TestAUnitAppliesInASavepointItReleases(t *testing.T) {
	db := setup(t)
	withStore(t, db, func(ctx context.Context, store rules.Store, rec *recordingTx, _ *rulestest.Tx) {
		applied, err := store.Apply(ctx, []rules.Statement{update("a", 1), update("b", 2)})
		if err != nil || !applied {
			t.Fatalf("Apply = %v, %v; want applied", applied, err)
		}
		want(t, rec.statements, "SAVEPOINT rules_apply", `UPDATE t SET v = ?1 WHERE id = ?2`, `UPDATE t SET v = ?1 WHERE id = ?2`, "RELEASE SAVEPOINT rules_apply")
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
		want(t, rec.statements, "SAVEPOINT rules_apply", `UPDATE t SET v = ?1 WHERE id = ?2`, `UPDATE t SET v = ?1 WHERE id = ?2`, "ROLLBACK TO SAVEPOINT rules_apply", "RELEASE SAVEPOINT rules_apply")
		// A write the version trigger follows counts its own row alone.
		applied, err = store.Apply(ctx, []rules.Statement{update("a", 4)})
		if err != nil || !applied {
			t.Fatalf("Apply under a trigger = %v, %v; want applied", applied, err)
		}
		applied, err = store.Apply(ctx, []rules.Statement{{Text: `UPDATE t SET v = 9 WHERE id = 'missing'`, Expect: rules.AnyRows}})
		if err != nil || !applied {
			t.Errorf("Apply of AnyRows = %v, %v; want applied", applied, err)
		}
	})
	if got := values(t, db); got["a"] != 4 || got["b"] != 0 {
		t.Errorf("rows = %v, want a written once and b not", got)
	}
	if v := db.Rows(t, `SELECT _version FROM t WHERE id = 'a'`)[0][0]; v != int64(2) {
		t.Errorf("a's version = %v, want 2", v)
	}
}

func TestAUnitWithAFailedStatementRollsBackAndReturnsTheError(t *testing.T) {
	db := setup(t)
	withStore(t, db, func(ctx context.Context, store rules.Store, rec *recordingTx, tx *rulestest.Tx) {
		_, err := store.Apply(ctx, []rules.Statement{update("a", 1), {Text: `INSERT INTO t (id, v) VALUES ('b', 0)`, Expect: 1}})
		if err == nil || !strings.Contains(err.Error(), "UNIQUE") || !strings.HasPrefix(err.Error(), "rules: ") {
			t.Fatalf("err = %v, want the unique violation, wrapped", err)
		}
		want(t, rec.statements, "SAVEPOINT rules_apply", `UPDATE t SET v = ?1 WHERE id = ?2`, `INSERT INTO t (id, v) VALUES ('b', 0)`, "ROLLBACK TO SAVEPOINT rules_apply", "RELEASE SAVEPOINT rules_apply")
		if _, err := tx.Exec(ctx, `UPDATE t SET v = 5 WHERE id = 'b'`); err != nil {
			t.Fatalf("the transaction after a failed unit: %v", err)
		}
	})
	if got := values(t, db); got["a"] != 0 || got["b"] != 5 {
		t.Errorf("rows = %v, want a as it was and b written after", got)
	}
}

func TestACheckIsFencedAndAFalseOneRollsBack(t *testing.T) {
	db := setup(t)
	check := func(id string) rules.Statement {
		return rules.Statement{Text: `EXISTS (SELECT 1 FROM t WHERE id = ?1)`, Values: []any{id}, Check: true}
	}
	withStore(t, db, func(ctx context.Context, store rules.Store, rec *recordingTx, _ *rulestest.Tx) {
		if applied, err := store.Apply(ctx, []rules.Statement{check("a"), update("a", 1)}); err != nil || !applied {
			t.Fatalf("Apply = %v, %v; want applied", applied, err)
		}
		if applied, err := store.Apply(ctx, []rules.Statement{update("b", 2), check("missing"), update("b", 3)}); err != nil || applied {
			t.Fatalf("Apply = %v, %v; want moved", applied, err)
		}
		want(t, rec.statements,
			"SAVEPOINT rules_apply",
			`SELECT CASE WHEN (EXISTS (SELECT 1 FROM t WHERE id = ?1)) THEN 1 ELSE 0 END AS held`,
			`UPDATE t SET v = ?1 WHERE id = ?2`,
			"RELEASE SAVEPOINT rules_apply",
			"SAVEPOINT rules_apply",
			`UPDATE t SET v = ?1 WHERE id = ?2`,
			`SELECT CASE WHEN (EXISTS (SELECT 1 FROM t WHERE id = ?1)) THEN 1 ELSE 0 END AS held`,
			"ROLLBACK TO SAVEPOINT rules_apply",
			"RELEASE SAVEPOINT rules_apply",
		)
	})
	if got := values(t, db); got["a"] != 1 || got["b"] != 0 {
		t.Errorf("rows = %v, want b's write undone", got)
	}
}

func TestQueryReadsEachRowsValues(t *testing.T) {
	db := setup(t)
	withStore(t, db, func(ctx context.Context, store rules.Store, _ *recordingTx, _ *rulestest.Tx) {
		rows, err := store.Query(ctx, `SELECT id, v, NULL AS n FROM t ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 2 || rows[0][0] != "a" || rows[0][1] != int64(0) || rows[0][2] != nil {
			t.Errorf("rows = %v", rows)
		}
		if _, err := store.Query(ctx, `SELECT nothing FROM t`); err == nil {
			t.Error("a query that fails read rows")
		}
	})
}
