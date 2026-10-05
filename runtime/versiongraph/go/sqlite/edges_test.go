package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/runtime/versiongraph/go/sqlite"
)

// edgeClients are the bindings that begin transactions of their own, each
// over a fresh file with a table t whose column p references parent: the
// pool's, which takes back one connection at most, and a connection's.
func edgeClients(t *testing.T) map[string]func(t *testing.T) (sqlite.Client, *sql.DB) {
	return map[string]func(t *testing.T) (sqlite.Client, *sql.DB){
		"pool": func(t *testing.T) (sqlite.Client, *sql.DB) {
			db := openDB(t, "", "")
			db.SetMaxOpenConns(1)
			return sqlite.DB(db), edgeTables(t, db)
		},
		"connection": func(t *testing.T) (sqlite.Client, *sql.DB) {
			db := openDB(t, "", "")
			conn := must(db.Conn(context.Background()))(t)
			t.Cleanup(func() { _ = conn.Close() })
			client := sqlite.DBConn(conn)
			edgeTables(t, db)
			return client, db
		},
	}
}

func edgeTables(t *testing.T, db *sql.DB) *sql.DB {
	t.Helper()
	for _, statement := range []string{
		"CREATE TABLE IF NOT EXISTS parent (id TEXT NOT NULL PRIMARY KEY) STRICT",
		"CREATE TABLE IF NOT EXISTS t (a TEXT NOT NULL, p TEXT REFERENCES parent (id)) STRICT",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

// rowsOfT counts t's rows.
func rowsOfT(t *testing.T, client sqlite.Client) int64 {
	t.Helper()
	var n int64
	err := client.Transact(context.Background(), func(ctx context.Context, conn sqlite.Conn) error {
		return conn.Query(ctx, "SELECT count(*) FROM t", nil, func(scan func(dest ...any) error) error { return scan(&n) })
	})
	if err != nil {
		t.Fatalf("a transaction after the one under test: %v", err)
	}
	return n
}

// TestFailedCommit: a COMMIT that fails, here on a foreign key the
// transaction deferred, leaves SQLite's transaction open; the binding rolls
// it back, so the write is gone and the connection's next transaction
// begins.
func TestFailedCommit(t *testing.T) {
	for binding, open := range edgeClients(t) {
		t.Run(binding, func(t *testing.T) {
			client, _ := open(t)
			err := client.Transact(context.Background(), func(ctx context.Context, conn sqlite.Conn) error {
				if _, err := conn.Exec(ctx, "PRAGMA defer_foreign_keys = ON"); err != nil {
					return err
				}
				_, err := conn.Exec(ctx, "INSERT INTO t (a, p) VALUES ('orphan', 'Missing')")
				return err
			})
			if code, ok := sqlite.ResultCode(err); !ok || code != 787 || !strings.Contains(err.Error(), "sqlite: commit") {
				t.Fatalf("a commit of a row that breaks a deferred key = %v, want the commit's SQLITE_CONSTRAINT_FOREIGNKEY", err)
			}
			if n := rowsOfT(t, client); n != 0 {
				t.Fatalf("t holds %d rows after the failed commit, want none", n)
			}
		})
	}
}

// TestCancelBeforeCommit: a transaction whose context is cancelled after
// its function returns cannot commit, and rolls back all the same, on a
// context that the cancel does not end; the connection's next transaction
// begins.
func TestCancelBeforeCommit(t *testing.T) {
	for binding, open := range edgeClients(t) {
		t.Run(binding, func(t *testing.T) {
			client, _ := open(t)
			ctx, cancel := context.WithCancel(context.Background())
			err := client.Transact(ctx, func(ctx context.Context, conn sqlite.Conn) error {
				_, err := conn.Exec(ctx, "INSERT INTO t (a) VALUES ('cancelled')")
				cancel()
				return err
			})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("a transaction cancelled before its commit = %v, want context.Canceled", err)
			}
			if strings.Contains(err.Error(), "roll back") {
				t.Fatalf("the rollback failed: %v", err)
			}
			if n := rowsOfT(t, client); n != 0 {
				t.Fatalf("t holds %d rows after the cancelled transaction, want none", n)
			}
		})
	}
}

// TestCancelMidStatement: a statement the context interrupts, an INSERT,
// ends SQLite's transaction itself, so the binding's ROLLBACK finds none to
// end; the transaction returns its function's error as it is, and the
// connection's next transaction begins.
func TestCancelMidStatement(t *testing.T) {
	for binding, open := range edgeClients(t) {
		t.Run(binding, func(t *testing.T) {
			client, _ := open(t)
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			var interrupted error
			err := client.Transact(ctx, func(ctx context.Context, conn sqlite.Conn) error {
				_, interrupted = conn.Exec(ctx, "WITH RECURSIVE n (x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM n) INSERT INTO t (a) SELECT x FROM n")
				return interrupted
			})
			if interrupted == nil || err != interrupted {
				t.Fatalf("an interrupted transaction = %v, want its statement's error %v as it is", err, interrupted)
			}
			if n := rowsOfT(t, client); n != 0 {
				t.Fatalf("t holds %d rows after the interrupted insert, want none", n)
			}
		})
	}
}

// TestNestedSavepointRollsBackWithOuter: inside a transaction the caller
// holds, a transaction begun inside another that succeeds releases its
// savepoint, so when the outer one then fails, the rollback reaches the
// outer one's savepoint and takes both one's writes.
func TestNestedSavepointRollsBackWithOuter(t *testing.T) {
	ctx := context.Background()
	db := openDB(t, "", "")
	edgeTables(t, db)
	conn := must(db.Conn(ctx))(t)
	t.Cleanup(func() { _ = conn.Close() })
	tx := must(conn.BeginTx(ctx, nil))(t)
	t.Cleanup(func() { _ = tx.Rollback() })
	client := sqlite.DBTx(tx)
	outer := errors.New("outer")
	err := client.Transact(ctx, func(ctx context.Context, c sqlite.Conn) error {
		if _, err := c.Exec(ctx, "INSERT INTO t (a) VALUES ('outer')"); err != nil {
			return err
		}
		if err := client.Transact(ctx, func(ctx context.Context, nested sqlite.Conn) error {
			_, err := nested.Exec(ctx, "INSERT INTO t (a) VALUES ('inner')")
			return err
		}); err != nil {
			return err
		}
		return outer
	})
	if !errors.Is(err, outer) {
		t.Fatalf("the outer transaction = %v, want its error", err)
	}
	var n int64
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM t").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("the caller's transaction sees %d rows after the outer savepoint rolled back, want none", n)
	}
}

// TestPoolDropsAnUnendedConnection: when a rollback fails, the pool's
// binding closes the connection rather than take it back with its
// transaction open, whether the function failed or did not return; the
// pool, at one connection, opens another for the next transaction.
func TestPoolDropsAnUnendedConnection(t *testing.T) {
	for _, how := range []string{"failed", "did not return"} {
		t.Run(how, func(t *testing.T) {
			open := edgeClients(t)["pool"]
			client, _ := open(t)
			restore := sqlite.SetRollbackStatement("ROLLBAK")
			failed := errors.New("failed")
			done := make(chan error, 1)
			go func() {
				exited := true
				defer func() {
					if exited {
						done <- nil
					}
				}()
				err := client.Transact(context.Background(), func(ctx context.Context, conn sqlite.Conn) error {
					if _, err := conn.Exec(ctx, "INSERT INTO t (a) VALUES ('unended')"); err != nil {
						return err
					}
					if how == "did not return" {
						runtime.Goexit()
					}
					return failed
				})
				exited = false
				done <- err
			}()
			err := <-done
			restore()
			if how == "failed" && (!errors.Is(err, failed) || !strings.Contains(err.Error(), "sqlite: roll back")) {
				t.Fatalf("a transaction whose rollback failed = %v, want its error and the rollback's", err)
			}
			if n := rowsOfT(t, client); n != 0 {
				t.Fatalf("t holds %d rows after the transaction the pool dropped, want none", n)
			}
		})
	}
}
