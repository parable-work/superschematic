package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/runtime/versiongraph/go/engine"
	"github.com/parable-work/superschematic/runtime/versiongraph/go/sqlite"
	"github.com/parable-work/superschematic/runtime/versiongraph/go/storage"
)

// TestNew: the adapter takes the fixture's descriptor and refuses what it
// cannot run.
func TestNew(t *testing.T) {
	for _, c := range []struct {
		name   string
		edit   func(t *testing.T, d map[string]any)
		opts   sqlite.Options
		refuse string
	}{
		{"the fixture's descriptor", func(*testing.T, map[string]any) {}, sqlite.Options{Graph: graph}, ""},
		{"a descriptor of version 2", func(_ *testing.T, d map[string]any) { d["version"] = 2 }, sqlite.Options{Graph: graph}, "reads version 3"},
		{"no graph", func(*testing.T, map[string]any) {}, sqlite.Options{}, "needs its graph's name"},
		{"a kind without a root column", func(t *testing.T, d map[string]any) { delete(kindOf(t, d, "cover"), "root") }, sqlite.Options{Graph: graph}, `kind "cover" has no root`},
		{"a role column missing from the kind's columns", func(t *testing.T, d map[string]any) {
			delete(kindOf(t, d, "cover")["columns"].(map[string]any), "_version")
		}, sqlite.Options{Graph: graph}, `version column "_version" is not in its columns`},
		{"a tombstone that is not boolean", func(t *testing.T, d map[string]any) {
			kindOf(t, d, "cover")["columns"].(map[string]any)["deleted_on_ref"] = "integer"
		}, sqlite.Options{Graph: graph}, "a tombstone is a boolean column"},
		{"a version that is not integer", func(t *testing.T, d map[string]any) {
			kindOf(t, d, "cover")["columns"].(map[string]any)["_version"] = "string"
		}, sqlite.Options{Graph: graph}, "a version an integer one"},
		{"a kind without history", func(t *testing.T, d map[string]any) { delete(kindOf(t, d, "cover"), "history") }, sqlite.Options{Graph: graph}, `kind "cover" has no history`},
		{"a history without exclude", func(t *testing.T, d map[string]any) {
			delete(kindOf(t, d, "cover")["history"].(map[string]any), "exclude")
		}, sqlite.Options{Graph: graph}, `kind "cover" has no history`},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := descriptorDoc(t)
			c.edit(t, d)
			_, err := sqlite.New(mustJSON(t, d), c.opts)
			switch {
			case c.refuse == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case c.refuse != "":
				errorContains(t, err, c.refuse, c.name)
			}
		})
	}
}

// TestBinding: the database/sql binding's Conn runs statements with
// numbered placeholders, reports the rows a statement changed and calls row
// once per row, and its errors carry SQLite's extended result code.
func TestBinding(t *testing.T) {
	db := openDB(t, "", "")
	err := sqlite.DB(db).Transact(context.Background(), func(ctx context.Context, conn sqlite.Conn) error {
		if _, err := conn.Exec(ctx, "CREATE TABLE t (a TEXT NOT NULL UNIQUE, b INTEGER NOT NULL) STRICT"); err != nil {
			return err
		}
		for i, a := range []string{"x", "y"} {
			n, err := conn.Exec(ctx, "INSERT INTO t (b, a) VALUES (?2, ?1)", a, int64(i+1))
			if err != nil || n != 1 {
				t.Fatalf("an insert changed %d rows (%v), want 1", n, err)
			}
		}
		if n, err := conn.Exec(ctx, "UPDATE t SET b = b + 10"); err != nil || n != 2 {
			t.Fatalf("an update changed %d rows (%v), want 2", n, err)
		}
		var got []string
		if err := conn.Query(ctx, "SELECT a, b FROM t WHERE b > ?1 ORDER BY a", []any{int64(0)}, func(scan func(dest ...any) error) error {
			var a string
			var b int64
			if err := scan(&a, &b); err != nil {
				return err
			}
			got = append(got, fmt.Sprintf("%s=%d", a, b-10))
			return nil
		}); err != nil {
			return err
		}
		if !slices.Equal(got, []string{"x=1", "y=2"}) {
			t.Fatalf("the query read %q", got)
		}
		calls := 0
		if err := conn.Query(ctx, "SELECT a FROM t WHERE a = ?1", []any{"z"}, func(func(dest ...any) error) error { calls++; return nil }); err != nil || calls != 0 {
			t.Fatalf("a query of no rows called row %d times (%v)", calls, err)
		}
		_, unique := conn.Exec(ctx, "INSERT INTO t (a, b) VALUES (?1, ?2)", "x", int64(3))
		if code, ok := sqlite.ResultCode(unique); !ok || code != sqlite.ResultConstraintUnique || !strings.Contains(unique.Error(), "UNIQUE constraint failed") {
			t.Fatalf("a second x = %v (code %d), want SQLITE_CONSTRAINT_UNIQUE", unique, code)
		}
		syntax := conn.Query(ctx, "SELEC 1", nil, func(func(dest ...any) error) error { return nil })
		if code, ok := sqlite.ResultCode(syntax); !ok || code != 1 {
			t.Fatalf("a syntax error = %v (code %d), want SQLITE_ERROR", syntax, code)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := sqlite.ResultCode(errors.New("not SQLite's")); ok {
		t.Fatal("ResultCode read a code from an error without one")
	}
}

// TestDBTurnsForeignKeysOn: each transaction of the pool's binding runs on
// a connection whose foreign keys it turned on, a new one each time here.
func TestDBTurnsForeignKeysOn(t *testing.T) {
	db := openDB(t, "", "")
	db.SetMaxIdleConns(0)
	for i := 0; i < 3; i++ {
		err := sqlite.DB(db).Transact(context.Background(), func(ctx context.Context, conn sqlite.Conn) error {
			var on int64
			if err := conn.Query(ctx, "PRAGMA foreign_keys", nil, func(scan func(dest ...any) error) error { return scan(&on) }); err != nil {
				return err
			}
			if on != 1 {
				t.Errorf("transaction %d runs with foreign keys off", i)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// TestStorageRefuses: Storage refuses a connection whose foreign keys are
// off, as they are in a transaction the caller began on a connection
// without them, a SQLite older than MinVersion, and one without the JSON
// functions; it takes MinVersion itself.
func TestStorageRefuses(t *testing.T) {
	ctx := context.Background()
	db := openDB(t, "", "")
	adapter := must(sqlite.New(readDescriptor(t), sqlite.Options{Graph: graph}))(t)
	if err := adapter.CreateTables(ctx, sqlite.DB(db)); err != nil {
		t.Fatal(err)
	}
	conn := must(db.Conn(ctx))(t)
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		t.Fatal(err)
	}
	tx := must(conn.BeginTx(ctx, nil))(t)
	t.Cleanup(func() { _ = tx.Rollback() })
	_, err := adapter.Storage(ctx, sqlite.DBTx(tx))
	errorContains(t, err, "the connection's foreign keys are off", "Storage in a transaction without foreign keys")
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		version, refuse   string
		noJSON, emptyJSON bool
	}{
		{version: "3.36.9", refuse: "SQLite 3.36.9 is older than 3.37.0"},
		{version: "2.99.99", refuse: "SQLite 2.99.99 is older than 3.37.0"},
		{version: "3.37.0"},
		{version: "3.38.0"},
		{version: "3.50.4"},
		{version: "4.0.0"},
		{version: "3.38", refuse: `not major.minor.patch`},
		{version: "3.37.2", noJSON: true, refuse: "SQLite 3.37.2 lacks the JSON functions json_each and json_extract"},
		{version: "3.37.2", emptyJSON: true, refuse: "SQLite 3.37.2 lacks the JSON functions json_each and json_extract, which the adapter's statements use (built in from 3.38.0, and in 3.37 with JSON1): they returned no 1"},
	} {
		_, err := adapter.Storage(ctx, versionClient{inner: sqlite.DB(db), version: c.version, noJSON: c.noJSON, emptyJSON: c.emptyJSON})
		if c.refuse == "" && err != nil {
			t.Fatalf("SQLite %s: %v", c.version, err)
		}
		if c.refuse != "" {
			errorContains(t, err, c.refuse, "SQLite "+c.version)
			if strings.HasSuffix(err.Error(), "<nil>") {
				t.Fatalf("SQLite %s: the refusal ends with no cause: %v", c.version, err)
			}
		}
	}
}

// versionClient is a client whose SQLite reports version, lacks the JSON
// functions when noJSON is set, and whose JSON functions return no row
// when emptyJSON is.
type versionClient struct {
	inner             sqlite.Client
	version           string
	noJSON, emptyJSON bool
}

func (c versionClient) Transact(ctx context.Context, fn func(ctx context.Context, conn sqlite.Conn) error) error {
	return c.inner.Transact(ctx, func(ctx context.Context, conn sqlite.Conn) error {
		return fn(ctx, versionConn{Conn: conn, version: c.version, noJSON: c.noJSON, emptyJSON: c.emptyJSON})
	})
}

type versionConn struct {
	sqlite.Conn
	version           string
	noJSON, emptyJSON bool
}

func (c versionConn) Query(ctx context.Context, sql string, args []any, row func(scan func(dest ...any) error) error) error {
	if sql == "SELECT sqlite_version()" {
		sql, args = "SELECT ?1", []any{c.version}
	}
	if c.emptyJSON && strings.Contains(sql, "json_each(") {
		sql = "SELECT 1 WHERE 0"
	}
	if c.noJSON && strings.Contains(sql, "json_") {
		// What a SQLite built without JSON1 says of json_each.
		sql = strings.ReplaceAll(sql, "json_each(", "no_such_table_valued_function(")
	}
	return c.Conn.Query(ctx, sql, args, row)
}

// foreignError is a driver's error that carries SQLite's result code under
// a method of another name.
type foreignError struct {
	message string
	rc      int
}

func (e *foreignError) Error() string   { return e.message }
func (e *foreignError) ResultCode() int { return e.rc }

// foreignClient is a client whose Conn returns foreignErrors.
type foreignClient struct {
	inner sqlite.Client
}

func (c foreignClient) Transact(ctx context.Context, fn func(ctx context.Context, conn sqlite.Conn) error) error {
	return c.inner.Transact(ctx, func(ctx context.Context, conn sqlite.Conn) error {
		return fn(ctx, foreignConn{conn})
	})
}

type foreignConn struct {
	inner sqlite.Conn
}

func foreign(err error) error {
	if code, ok := sqlite.ResultCode(err); ok {
		return &foreignError{message: err.Error(), rc: code}
	}
	return err
}

func (c foreignConn) Query(ctx context.Context, sql string, args []any, row func(scan func(dest ...any) error) error) error {
	return foreign(c.inner.Query(ctx, sql, args, row))
}

func (c foreignConn) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	n, err := c.inner.Exec(ctx, sql, args...)
	return n, foreign(err)
}

// TestResultCodeOption: a classifier the caller gives reads the result
// code of another driver's errors, so a taken name is name_taken there too;
// without it, the adapter does not take that driver's unique violation for
// one.
func TestResultCodeOption(t *testing.T) {
	ctx := context.Background()
	db := openDB(t, "", "")
	classify := func(err error) (int, bool) {
		var f *foreignError
		if errors.As(err, &f) {
			return f.ResultCode(), true
		}
		return 0, false
	}
	for _, c := range []struct {
		name     string
		classify func(error) (int, bool)
		want     string
	}{
		{"the caller's classifier", classify, "name_taken"},
		{"the default classifier", nil, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := setupOn(t, db, foreignClient{sqlite.DB(db)}, sqlite.Options{Graph: c.name, ResultCode: c.classify}, readDescriptor(t))
			must(s.engine.CreatePrimary(ctx, cook, bread, "main"))(t)
			_, err := s.engine.CreatePrimary(ctx, cook, bread, "main")
			if err == nil || engine.ErrorCode(err) != c.want {
				t.Fatalf("a second primary line named main = %v (code %q), want code %q", err, engine.ErrorCode(err), c.want)
			}
		})
	}
}

// TestWriteLockWaits: a second connection's BEGIN IMMEDIATE waits for the
// write lock, and then fails busy; once the first transaction commits, it
// writes.
func TestWriteLockWaits(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/lock.sqlite"
	a := newSetup(t, sqlite.Options{}, path)
	second := openDB(t, path, "_pragma=busy_timeout(300)")
	b := must(must(sqlite.New(readDescriptor(t), sqlite.Options{Graph: graph}))(t).Storage(ctx, sqlite.DB(second)))(t)
	var waited time.Duration
	var busy error
	ran := false
	must(in(a.storage, func(ctx context.Context, tx storage.Tx) (struct{}, error) {
		if _, err := tx.CreateRef(ctx, storage.NewRef{Root: bread, Name: "main", Actor: cook}); err != nil {
			return struct{}{}, err
		}
		started := time.Now()
		_, busy = in(b, func(ctx context.Context, other storage.Tx) (storage.Ref, error) {
			ran = true
			return other.ReadRef(ctx, "Missing")
		})
		waited = time.Since(started)
		return struct{}{}, nil
	}))(t)
	if code, ok := sqlite.ResultCode(busy); !ok || code != sqlite.ResultBusy {
		t.Fatalf("the second connection's transaction = %v (code %d), want SQLITE_BUSY", busy, code)
	}
	if waited < 250*time.Millisecond {
		t.Fatalf("the second connection waited %s for the lock, want its busy timeout, 300ms", waited)
	}
	// The transaction begins by taking the write lock, so even one that
	// would only read never starts.
	if ran {
		t.Fatal("the second connection's transaction ran without the write lock")
	}
	must(in(b, func(ctx context.Context, other storage.Tx) (storage.Ref, error) {
		return other.CreateRef(ctx, storage.NewRef{Root: "Soup", Name: "main", Actor: cook})
	}))(t)
	if n := count(t, second, `SELECT count(*) FROM "graph_ref"`); n != 2 {
		t.Fatalf("the second connection reads %d refs, want both", n)
	}
}

// TestCallerTransaction: inside a transaction the caller holds, each of
// the adapter's transactions is a savepoint that reads the clock once, a
// failing one rolls back alone, and the caller's rollback undoes every
// write.
func TestCallerTransaction(t *testing.T) {
	ctx := context.Background()
	db := openDB(t, "", "")
	conn := must(db.Conn(ctx))(t)
	t.Cleanup(func() { _ = conn.Close() })
	calls := int64(0)
	adapter := must(sqlite.New(readDescriptor(t), sqlite.Options{Graph: graph, Clock: func() int64 { calls++; return 1_800_000_000_000_000 + calls }}))(t)
	if err := adapter.CreateTables(ctx, sqlite.DBConn(conn)); err != nil {
		t.Fatal(err)
	}
	refs := func(q interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
	}) int64 {
		var n int64
		if err := q.QueryRowContext(ctx, `SELECT count(*) FROM "graph_ref"`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	tx := must(conn.BeginTx(ctx, nil))(t)
	t.Cleanup(func() { _ = tx.Rollback() })
	s := must(adapter.Storage(ctx, sqlite.DBTx(tx)))(t)
	calls = 0
	both := must(in(s, func(ctx context.Context, tx storage.Tx) ([]storage.Ref, error) {
		var out []storage.Ref
		for _, root := range []string{bread, "Soup"} {
			ref, err := tx.CreateRef(ctx, storage.NewRef{Root: root, Name: "main", Actor: cook})
			if err != nil {
				return nil, err
			}
			out = append(out, ref)
		}
		return out, nil
	}))(t)
	if calls != 1 {
		t.Fatalf("a transaction read the clock %d times, want once", calls)
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT created_at FROM "graph_ref" WHERE id IN (?1, ?2)`, both[0].ID, both[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	var times []int64
	for rows.Next() {
		var at int64
		if err := rows.Scan(&at); err != nil {
			t.Fatal(err)
		}
		times = append(times, at)
	}
	_ = rows.Close()
	if !slices.Equal(times, []int64{1_800_000_000_000_001}) {
		t.Fatalf("the transaction's refs were created at %v, want its one time", times)
	}
	must(in(s, func(ctx context.Context, tx storage.Tx) (storage.Ref, error) { return tx.ReadRef(ctx, both[0].ID) }))(t)
	if calls != 2 {
		t.Fatalf("a second transaction left the clock read %d times, want 2", calls)
	}
	if _, err := in(s, func(ctx context.Context, tx storage.Tx) (storage.Ref, error) {
		if _, err := tx.CreateRef(ctx, storage.NewRef{Root: "Pie", Name: "main", Actor: cook}); err != nil {
			return storage.Ref{}, err
		}
		return tx.CreateRef(ctx, storage.NewRef{Root: bread, Name: "main", Actor: cook})
	}); !errors.Is(err, storage.ErrNameTaken) {
		t.Fatalf("a transaction that takes a name = %v, want name taken", err)
	}
	if n := refs(tx); n != 2 {
		t.Fatalf("the caller's transaction sees %d refs, want the two the adapter kept", n)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if n := refs(conn); n != 0 {
		t.Fatalf("%d refs after the caller rolled back, want none", n)
	}
	// The engine runs there too.
	tx = must(conn.BeginTx(ctx, nil))(t)
	committed := tx
	t.Cleanup(func() { _ = committed.Rollback() })
	eng := must(engine.New(readDescriptor(t), must(adapter.Storage(ctx, sqlite.DBTx(tx)))(t), engine.Options{SchemaEpoch: 1, SnapshotEvery: 3}))(t)
	main := must(eng.CreatePrimary(ctx, cook, bread, "main"))(t)
	draft := must(eng.Branch(ctx, cook, main.ID, "draft"))(t)
	must(eng.Save(ctx, cook, draft.ID, draft.Version, upserts("step", stepRow(t, "Mix", "Mix"))))(t)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n := refs(conn); n != 2 {
		t.Fatalf("%d refs after the caller committed, want 2", n)
	}
}

// TestDBNested: on the pool's binding and on a connection's, a
// transaction begun with the context of another's function is a savepoint
// on its connection: it sees the outer one's writes, and rolls back alone.
func TestDBNested(t *testing.T) {
	for _, binding := range []string{"pool", "connection"} {
		t.Run(binding, func(t *testing.T) {
			db := openDB(t, "", "")
			client := sqlite.DB(db)
			if binding == "connection" {
				conn := must(db.Conn(context.Background()))(t)
				t.Cleanup(func() { _ = conn.Close() })
				client = sqlite.DBConn(conn)
			}
			testNested(t, client)
		})
	}
}

func testNested(t *testing.T, client sqlite.Client) {
	count := func(ctx context.Context, conn sqlite.Conn) int64 {
		var n int64
		if err := conn.Query(ctx, "SELECT count(*) FROM t", nil, func(scan func(dest ...any) error) error { return scan(&n) }); err != nil {
			t.Fatal(err)
		}
		return n
	}
	inner := errors.New("inner")
	err := client.Transact(context.Background(), func(ctx context.Context, conn sqlite.Conn) error {
		if _, err := conn.Exec(ctx, "CREATE TABLE t (a TEXT NOT NULL) STRICT"); err != nil {
			return err
		}
		if _, err := conn.Exec(ctx, "INSERT INTO t (a) VALUES ('outer')"); err != nil {
			return err
		}
		err := client.Transact(ctx, func(ctx context.Context, nested sqlite.Conn) error {
			if n := count(ctx, nested); n != 1 {
				t.Errorf("the nested transaction sees %d rows, want the outer one's", n)
			}
			if _, err := nested.Exec(ctx, "INSERT INTO t (a) VALUES ('inner')"); err != nil {
				return err
			}
			return inner
		})
		if !errors.Is(err, inner) {
			t.Errorf("the nested transaction = %v, want its error", err)
		}
		if n := count(ctx, conn); n != 1 {
			t.Errorf("after the nested transaction rolled back the outer one sees %d rows, want 1", n)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var rows []string
	err = client.Transact(context.Background(), func(ctx context.Context, conn sqlite.Conn) error {
		return conn.Query(ctx, "SELECT a FROM t", nil, func(scan func(dest ...any) error) error {
			var a string
			if err := scan(&a); err != nil {
				return err
			}
			rows = append(rows, a)
			return nil
		})
	})
	if err != nil || !slices.Equal(rows, []string{"outer"}) {
		t.Fatalf("the file holds %q (%v), want the outer row", rows, err)
	}
}

// TestUnfinishedTransaction: on each binding, a transaction whose function
// does not return, here because its goroutine exits inside it, rolls back,
// and the next transaction on the connection runs.
func TestUnfinishedTransaction(t *testing.T) {
	ctx := context.Background()
	for _, binding := range []string{"pool", "connection", "caller's transaction"} {
		t.Run(binding, func(t *testing.T) {
			db := openDB(t, "", "")
			conn := must(db.Conn(ctx))(t)
			t.Cleanup(func() { _ = conn.Close() })
			if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
				t.Fatal(err)
			}
			adapter := must(sqlite.New(readDescriptor(t), sqlite.Options{Graph: graph}))(t)
			if err := adapter.CreateTables(ctx, sqlite.DBConn(conn)); err != nil {
				t.Fatal(err)
			}
			var client sqlite.Client
			var tx *sql.Tx
			switch binding {
			case "pool":
				client = sqlite.DB(db)
			case "connection":
				client = sqlite.DBConn(conn)
			default:
				tx = must(conn.BeginTx(ctx, nil))(t)
				t.Cleanup(func() { _ = tx.Rollback() })
				client = sqlite.DBTx(tx)
			}
			s := must(adapter.Storage(ctx, client))(t)
			create := func(ctx context.Context, tx storage.Tx, name string) error {
				_, err := tx.CreateRef(ctx, storage.NewRef{Root: bread, Name: name, Actor: cook})
				return err
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				_ = s.Transact(ctx, func(ctx context.Context, tx storage.Tx) error {
					if err := create(ctx, tx, "exited"); err != nil {
						return err
					}
					runtime.Goexit()
					return nil
				})
			}()
			<-done
			must(in(s, func(ctx context.Context, tx storage.Tx) (struct{}, error) { return struct{}{}, create(ctx, tx, "next") }))(t)
			var q interface {
				QueryRowContext(context.Context, string, ...any) *sql.Row
			} = conn
			if tx != nil {
				q = tx
			}
			var names string
			if err := q.QueryRowContext(ctx, `SELECT group_concat(name) FROM "graph_ref"`).Scan(&names); err != nil {
				t.Fatal(err)
			}
			if names != "next" {
				t.Fatalf("the refs are %q, want the next transaction's alone", names)
			}
		})
	}
}
