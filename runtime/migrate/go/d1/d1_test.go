package d1

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	migrate "github.com/parable-work/superschematic/runtime/migrate/go"
)

func TestParseURL(t *testing.T) {
	for raw, want := range map[string][2]string{
		"d1://0123abcd/5c1e8a2b-9f0d-4c3e-8b7a-1d2e3f4a5b6c": {"0123abcd", "5c1e8a2b-9f0d-4c3e-8b7a-1d2e3f4a5b6c"},
		"D1://acct/db": {"acct", "db"},
	} {
		account, database, err := ParseURL(raw)
		if err != nil || account != want[0] || database != want[1] {
			t.Errorf("ParseURL(%q) = %q, %q, %v", raw, account, database, err)
		}
		if !IsURL(raw) {
			t.Errorf("IsURL(%q) is false", raw)
		}
	}
	for _, raw := range []string{"d1://acct", "d1://acct/", "d1:///db", "d1://acct/db/more", "d1://acct/db?x=1", "d1://user@acct/db", "sqlite://acct/db"} {
		if _, _, err := ParseURL(raw); err == nil {
			t.Errorf("ParseURL(%q) took it", raw)
		}
	}
	if IsURL("/var/lib/d1.db") || IsURL("postgres://h/db") {
		t.Error("IsURL took a URL of another database")
	}
}

func TestPlaceholders(t *testing.T) {
	for in, want := range map[string]string{
		`SELECT a FROM t WHERE b = $1 AND c = $12`:     `SELECT a FROM t WHERE b = ?1 AND c = ?12`,
		`UPDATE t SET a = '$1', "$2" = $3 -- $4`:       `UPDATE t SET a = '$1', "$2" = ?3 -- $4`,
		"SELECT 'it''s $1', $2 /* $3 */, [$4], `$5`":   "SELECT 'it''s $1', ?2 /* $3 */, [$4], `$5`",
		`SELECT price$1 FROM t WHERE x = $1`:           `SELECT price$1 FROM t WHERE x = ?1`,
		`SELECT $ FROM t`:                              `SELECT $ FROM t`,
		`INSERT INTO t VALUES ($1, $2) RETURNING '$3'`: `INSERT INTO t VALUES (?1, ?2) RETURNING '$3'`,
	} {
		if got := placeholders(in); got != want {
			t.Errorf("placeholders(%q) = %q, want %q", in, got, want)
		}
	}
	s, err := prepare(`SELECT $1, $2`, []any{"a", int64(3)})
	if err != nil || s.sql != `SELECT ?1, ?2` || strings.Join(s.params, ",") != "a,3" {
		t.Fatalf("prepare = %+v, %v", s, err)
	}
	if s, _ := prepare(`SELECT '$1'`, nil); s.sql != `SELECT '$1'` || s.params != nil {
		t.Fatalf("a statement without arguments changed: %+v", s)
	}
	if _, err := prepare(`SELECT $1`, []any{1.5}); err == nil {
		t.Fatal("prepare took a float")
	}
}

// TestOpenRefusesNoToken: Open refuses an empty token and names the
// variable the binary reads it from.
func TestOpenRefusesNoToken(t *testing.T) {
	_, err := Open(context.Background(), "d1://acct/db", Options{})
	if err == nil || !strings.Contains(err.Error(), TokenEnv) {
		t.Fatalf("Open without a token = %v", err)
	}
	if _, err := Open(context.Background(), "d1://acct", Options{Token: "t"}); err == nil {
		t.Fatal("Open took a URL without a database")
	}
}

// TestRefusals: the driver refuses a step outside a transaction and a step
// that turns foreign keys off, naming the step and why.
func TestRefusals(t *testing.T) {
	d, err := Open(context.Background(), "d1://acct/db", Options{Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	var _ migrate.StepChecker = d
	for _, c := range []struct {
		step *migrate.Step
		says []string
	}{
		{&migrate.Step{Index: 2, Subject: "table/order/index/order_total_key"}, []string{"step 2 (table/order/index/order_total_key)", "not transactional", "no BEGIN or COMMIT"}},
		{&migrate.Step{Index: 3, Subject: "table/customer", Transactional: true, ForeignKeysOff: true}, []string{"step 3 (table/customer)", "turns foreign keys off", "keeps foreign key enforcement on", "SQLite file"}},
	} {
		err := d.CheckStep(c.step)
		for _, want := range c.says {
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("CheckStep(%+v) = %v, want it to say %q", c.step, err, want)
			}
		}
	}
	if err := d.CheckStep(&migrate.Step{Index: 1, Subject: "table/a", Transactional: true}); err != nil {
		t.Errorf("CheckStep of a transactional step = %v", err)
	}
	if err := d.Session(context.Background(), nil); err == nil {
		t.Error("Session ran")
	}
	if err := d.Transact(context.Background(), migrate.TxOptions{ForeignKeysOff: true}, nil); err == nil {
		t.Error("Transact turned foreign keys off")
	}
}

// recorder is a D1 API that records each request's body and answers with
// the response its answer function returns.
type recorder struct {
	mu     sync.Mutex
	bodies []map[string]any
	tokens []string
	answer func(body map[string]any) (int, string)
}

func (rec *recorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rec.mu.Lock()
	rec.bodies = append(rec.bodies, body)
	rec.tokens = append(rec.tokens, r.Header.Get("Authorization"))
	rec.mu.Unlock()
	status, response := rec.answer(body)
	w.WriteHeader(status)
	_, _ = w.Write([]byte(response))
}

// succeed answers each statement of a request with no rows and changes.
func succeed(changes int) func(map[string]any) (int, string) {
	return func(body map[string]any) (int, string) {
		n := 1
		if batch, ok := body["batch"].([]any); ok {
			n = len(batch)
		}
		results := make([]map[string]any, n)
		for i := range results {
			results[i] = map[string]any{"success": true, "results": map[string]any{"columns": []string{}, "rows": []any{}}, "meta": map[string]any{"changes": changes}}
		}
		out, _ := json.Marshal(map[string]any{"success": true, "errors": []any{}, "messages": []any{}, "result": results})
		return http.StatusOK, string(out)
	}
}

func open(t *testing.T, rec *recorder) *Driver {
	t.Helper()
	server := httptest.NewServer(rec)
	t.Cleanup(server.Close)
	d, err := Open(context.Background(), "d1://acct/db", Options{Token: "secret", BaseURL: server.URL + "/client/v4", HTTPClient: server.Client(), Holder: "runner-a"})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// TestABatch: a transaction's writes go as one request in the batch form,
// led by PRAGMA defer_foreign_keys = ON and, while the driver holds the
// lease, its renewal; parameters are ?N and strings. A query goes at once,
// in the plain form.
func TestABatch(t *testing.T) {
	rec := &recorder{answer: succeed(1)}
	d := open(t, rec)
	ctx := context.Background()
	unlock, err := d.Lock(ctx, "shop")
	if err != nil {
		t.Fatal(err)
	}
	err = d.Transact(ctx, migrate.TxOptions{}, func(ctx context.Context, conn migrate.Conn) error {
		if err := conn.Query(ctx, `SELECT step FROM log WHERE plan = $1`, []any{"p"}, func(func(...any) error) error { return nil }); err != nil {
			return err
		}
		if err := conn.Exec(ctx, `CREATE TABLE a (id INTEGER)`); err != nil {
			return err
		}
		if err := conn.Exec(ctx, `INSERT INTO log (plan, step) VALUES ($1, $2)`, "p", int64(3)); err != nil {
			return err
		}
		if err := conn.Query(ctx, `SELECT 1`, nil, nil); err == nil {
			return errors.New("a query after a write ran")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := unlock(ctx); err != nil {
		t.Fatal(err)
	}

	// Lock: the lock table, then the conditional write. Then the query,
	// the batch and the release.
	if len(rec.bodies) != 5 {
		t.Fatalf("%d requests: %v", len(rec.bodies), rec.bodies)
	}
	for _, token := range rec.tokens {
		if token != "Bearer secret" {
			t.Fatalf("Authorization %q", token)
		}
	}
	if sql, _ := rec.bodies[1]["sql"].(string); !strings.Contains(sql, "INSERT INTO superschematic_lock") || !strings.Contains(sql, "expires_at < strftime") {
		t.Fatalf("the lease's write: %v", rec.bodies[1])
	}
	query := rec.bodies[2]
	if query["sql"] != `SELECT step FROM log WHERE plan = ?1` || len(query["params"].([]any)) != 1 {
		t.Fatalf("the query: %v", query)
	}
	batch, _ := rec.bodies[3]["batch"].([]any)
	if len(batch) != 4 {
		t.Fatalf("the batch: %v", rec.bodies[3])
	}
	statement := func(i int) map[string]any { return batch[i].(map[string]any) }
	if statement(0)["sql"] != "PRAGMA defer_foreign_keys = ON" {
		t.Fatalf("the batch leads with %v", statement(0))
	}
	if sql, _ := statement(1)["sql"].(string); !strings.HasPrefix(sql, "UPDATE superschematic_lock") {
		t.Fatalf("the batch does not renew the lease: %v", statement(1))
	}
	if statement(2)["sql"] != `CREATE TABLE a (id INTEGER)` || statement(2)["params"] != nil {
		t.Fatalf("statement 2: %v", statement(2))
	}
	params, _ := statement(3)["params"].([]any)
	if statement(3)["sql"] != `INSERT INTO log (plan, step) VALUES (?1, ?2)` || len(params) != 2 || params[0] != "p" || params[1] != "3" {
		t.Fatalf("statement 3: %v", statement(3))
	}
	if sql, _ := rec.bodies[4]["sql"].(string); !strings.HasPrefix(sql, "UPDATE superschematic_lock SET expires_at") {
		t.Fatalf("the release: %v", rec.bodies[4])
	}

	// Without the lease, a batch has no renewal; a read-only transaction
	// and one that wrote nothing send nothing.
	rec.bodies = nil
	_ = d.Transact(ctx, migrate.TxOptions{}, func(ctx context.Context, conn migrate.Conn) error { return conn.Exec(ctx, "DROP TABLE a") })
	_ = d.Transact(ctx, migrate.TxOptions{}, func(context.Context, migrate.Conn) error { return nil })
	if err := d.Transact(ctx, migrate.TxOptions{ReadOnly: true}, func(ctx context.Context, conn migrate.Conn) error { return conn.Exec(ctx, "DROP TABLE a") }); err == nil {
		t.Fatal("a read-only transaction wrote")
	}
	if len(rec.bodies) != 1 || len(rec.bodies[0]["batch"].([]any)) != 2 {
		t.Fatalf("requests: %v", rec.bodies)
	}
}

// TestAFailedStatement: a response that names the statement that failed
// makes the commit's error a *migrate.StatementError naming it; one that
// does not leaves the error D1's.
func TestAFailedStatement(t *testing.T) {
	failed := `{"success":false,"errors":[{"code":7500,"message":"no such table: missing: SQLITE_ERROR"}],"messages":[],"result":[%s]}`
	for _, c := range []struct {
		results   string
		statement string
	}{
		{`{"success":true},{"success":true},{"success":false}`, "INSERT INTO missing VALUES (1)"},
		{``, ""},
	} {
		rec := &recorder{answer: func(map[string]any) (int, string) {
			return http.StatusBadRequest, strings.Replace(failed, "%s", c.results, 1)
		}}
		d := open(t, rec)
		err := d.Transact(context.Background(), migrate.TxOptions{}, func(ctx context.Context, conn migrate.Conn) error {
			_ = conn.Exec(ctx, "CREATE TABLE a (id INTEGER)")
			return conn.Exec(ctx, "INSERT INTO missing VALUES (1)")
		})
		var statementErr *migrate.StatementError
		var apiErr *Error
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest || !strings.Contains(err.Error(), "no such table: missing: SQLITE_ERROR (code 7500)") {
			t.Fatalf("commit = %v", err)
		}
		if errors.As(err, &statementErr) != (c.statement != "") || (statementErr != nil && statementErr.Statement != c.statement) {
			t.Fatalf("commit = %#v, want the statement %q", err, c.statement)
		}
	}
}

// TestLeaseLost: a batch whose renewal finds another holder fails, and the
// error says the lease passed to another runner.
func TestLeaseLost(t *testing.T) {
	rec := &recorder{}
	rec.answer = func(body map[string]any) (int, string) {
		if _, ok := body["batch"]; ok {
			return http.StatusBadRequest, `{"success":false,"errors":[{"code":7500,"message":"NOT NULL constraint failed: superschematic_lock.holder: SQLITE_CONSTRAINT"}],"messages":[],"result":[]}`
		}
		return succeed(1)(body)
	}
	d := open(t, rec)
	ctx := context.Background()
	if _, err := d.Lock(ctx, "shop"); err != nil {
		t.Fatal(err)
	}
	err := d.Transact(ctx, migrate.TxOptions{}, func(ctx context.Context, conn migrate.Conn) error { return conn.Exec(ctx, "DROP TABLE a") })
	if err == nil || !strings.Contains(err.Error(), "another runner took over the lease of service shop") {
		t.Fatalf("commit after the lease passed = %v", err)
	}
}

// TestLockGivesUp: Lock tries a lease another runner holds every LockPoll
// and gives up after LockWait, naming the holder.
func TestLockGivesUp(t *testing.T) {
	var takes int
	rec := &recorder{}
	rec.answer = func(body map[string]any) (int, string) {
		sql, _ := body["sql"].(string)
		switch {
		case strings.HasPrefix(sql, "INSERT INTO superschematic_lock"):
			takes++
			return succeed(0)(body)
		case strings.HasPrefix(sql, "SELECT holder"):
			return http.StatusOK, `{"success":true,"errors":[],"messages":[],"result":[{"success":true,"results":{"columns":["holder","expires_at"],"rows":[["runner-b","2026-10-05T12:00:00.000Z"]]},"meta":{"changes":0}}]}`
		}
		return succeed(0)(body)
	}
	server := httptest.NewServer(rec)
	t.Cleanup(server.Close)
	d, err := Open(context.Background(), "d1://acct/db", Options{Token: "t", BaseURL: server.URL, HTTPClient: server.Client(), LockWait: 50 * time.Millisecond, LockPoll: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Lock(context.Background(), "shop")
	if err == nil || !strings.Contains(err.Error(), "runner-b holds the lease of service shop until 2026-10-05T12:00:00.000Z") {
		t.Fatalf("Lock = %v", err)
	}
	if takes < 3 {
		t.Fatalf("Lock tried %d times", takes)
	}
}

// TestIsLockTimeout: a request D1 turned away before running it is retried;
// a failed statement is not.
func TestIsLockTimeout(t *testing.T) {
	d := &Driver{}
	for err, want := range map[error]bool{
		&Error{Status: http.StatusTooManyRequests}: true,
		&Error{Status: http.StatusServiceUnavailable, Errors: []APIError{{Code: 7500, Message: "D1 DB is overloaded. Requests queued for too long."}}}: true,
		&Error{Status: http.StatusBadRequest, Errors: []APIError{{Code: 7500, Message: "no such table: a: SQLITE_ERROR"}}}:                             false,
		errors.New("d1: connection refused"): false,
	} {
		if got := d.IsLockTimeout(&migrate.StepError{Err: err}); got != want {
			t.Errorf("IsLockTimeout(%v) = %v", err, got)
		}
	}
}

// TestNotJSON: a response that is not the API's JSON is an error with its
// start.
func TestNotJSON(t *testing.T) {
	rec := &recorder{answer: func(map[string]any) (int, string) { return http.StatusBadGateway, "<html>bad gateway</html>" }}
	d := open(t, rec)
	err := d.Transact(context.Background(), migrate.TxOptions{}, func(ctx context.Context, conn migrate.Conn) error { return conn.Exec(ctx, "DROP TABLE a") })
	if err == nil || !strings.Contains(err.Error(), "HTTP 502: <html>bad gateway</html>") {
		t.Fatalf("commit = %v", err)
	}
}
