package d1fake

import (
	"bytes"
	"encoding/json"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func newServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(filepath.Join(t.TempDir(), "d1.db"), Options{Token: "token", AccountID: "acct", DatabaseID: "db"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

// answer is a decoded response.
type answer struct {
	status  int
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	Result []struct {
		Success bool            `json:"success"`
		Results json.RawMessage `json:"results"`
		Meta    struct {
			Changes int64 `json:"changes"`
		} `json:"meta"`
	} `json:"result"`
}

func (a answer) message() string {
	var out []string
	for _, e := range a.Errors {
		out = append(out, e.Message)
	}
	return strings.Join(out, "; ")
}

func post(t *testing.T, s *Server, path, token string, body any) answer {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, s.BaseURL()+path, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := s.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var a answer
	if err := json.NewDecoder(resp.Body).Decode(&a); err != nil {
		t.Fatal(err)
	}
	a.status = resp.StatusCode
	return a
}

const raw = "/accounts/acct/d1/database/db/raw"

// run posts a batch to the raw endpoint, each statement without params.
func run(t *testing.T, s *Server, statements ...string) answer {
	t.Helper()
	batch := make([]map[string]any, len(statements))
	for i, sql := range statements {
		batch[i] = map[string]any{"sql": sql}
	}
	return post(t, s, raw, "token", map[string]any{"batch": batch})
}

// rows reads a raw result's rows.
func rows(t *testing.T, a answer, i int) [][]any {
	t.Helper()
	var r struct {
		Rows [][]any `json:"rows"`
	}
	if !a.Success {
		t.Fatalf("request failed: %d %s", a.status, a.message())
	}
	if err := json.Unmarshal(a.Result[i].Results, &r); err != nil {
		t.Fatal(err)
	}
	return r.Rows
}

func TestAuthAndRoutes(t *testing.T) {
	s := newServer(t)
	if a := post(t, s, raw, "wrong", map[string]any{"sql": "SELECT 1"}); a.status != http.StatusForbidden || a.Success || a.Errors[0].Code != codeAuthentication {
		t.Fatalf("a wrong token: %+v", a)
	}
	if a := post(t, s, "/accounts/acct/d1/database/other/raw", "token", map[string]any{"sql": "SELECT 1"}); a.status != http.StatusNotFound {
		t.Fatalf("another database: %+v", a)
	}
	if a := post(t, s, raw, "token", map[string]any{"sql": "SELECT 1"}); !a.Success || a.status != http.StatusOK {
		t.Fatalf("SELECT 1: %+v", a)
	}
}

// TestResults: raw answers columns and rows, query an object per row with
// its members in column order; params are strings bound to ?N; meta
// carries changes.
func TestResults(t *testing.T) {
	s := newServer(t)
	run(t, s, "CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT)")
	a := post(t, s, raw, "token", map[string]any{"sql": "INSERT INTO t (id, name) VALUES (?1, ?2), (?1 + 1, ?2)", "params": []string{"1", "Ada"}})
	if !a.Success || a.Result[0].Meta.Changes != 2 {
		t.Fatalf("insert: %+v", a)
	}
	a = post(t, s, raw, "token", map[string]any{"sql": "SELECT name, id FROM t WHERE id = ?1", "params": []string{"2"}})
	var got struct {
		Columns []string `json:"columns"`
		Rows    [][]any  `json:"rows"`
	}
	if err := json.Unmarshal(a.Result[0].Results, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Columns, []string{"name", "id"}) || !reflect.DeepEqual(got.Rows, [][]any{{"Ada", float64(2)}}) {
		t.Fatalf("raw: %+v", got)
	}
	a = post(t, s, "/accounts/acct/d1/database/db/query", "token", map[string]any{"sql": "SELECT name, id FROM t ORDER BY id"})
	if string(a.Result[0].Results) != `[{"name":"Ada","id":1},{"name":"Ada","id":2}]` {
		t.Fatalf("query: %s", a.Result[0].Results)
	}
	if a := post(t, s, raw, "token", map[string]any{"sql": "SELECT ?1", "params": []any{1}}); a.status != http.StatusBadRequest {
		t.Fatalf("a number param: %+v", a)
	}
}

// TestStatementsInOneSQL: sql splits at the semicolons that end a
// statement, and takes no params with several.
func TestStatementsInOneSQL(t *testing.T) {
	s := newServer(t)
	a := post(t, s, raw, "token", map[string]any{"sql": `CREATE TABLE t (a TEXT); INSERT INTO t VALUES ('x;y');
CREATE TRIGGER tr AFTER INSERT ON t BEGIN SELECT 1; SELECT 2; END; SELECT a FROM t`})
	if len(a.Result) != 4 || !reflect.DeepEqual(rows(t, a, 3), [][]any{{"x;y"}}) {
		t.Fatalf("four statements: %+v", a)
	}
	a = post(t, s, raw, "token", map[string]any{"sql": "SELECT ?1; SELECT ?1", "params": []string{"a"}})
	if a.status != http.StatusBadRequest || !strings.Contains(a.message(), "params with multiple statements is not supported") {
		t.Fatalf("params with two statements: %+v", a)
	}
}

// TestARequestIsOneTransaction: a failed statement rolls back the whole
// request, and the answer has no results.
func TestARequestIsOneTransaction(t *testing.T) {
	s := newServer(t)
	run(t, s, "CREATE TABLE t (id INTEGER PRIMARY KEY)")
	a := run(t, s, "INSERT INTO t VALUES (1)", "CREATE TABLE u (id INTEGER)", "INSERT INTO missing VALUES (1)")
	if a.Success || a.status != http.StatusBadRequest || a.Errors[0].Code != codeStatement || a.message() != "no such table: missing: SQLITE_ERROR" || len(a.Result) != 0 {
		t.Fatalf("a failed batch: %+v", a)
	}
	left := run(t, s, "SELECT count(*) FROM t", "SELECT count(*) FROM sqlite_master WHERE name = 'u'")
	if got, tables := rows(t, left, 0), rows(t, left, 1); got[0][0] != float64(0) || tables[0][0] != float64(0) {
		t.Fatalf("the failed batch left %v rows and %v tables", got, tables)
	}
	a = run(t, s, "INSERT INTO t VALUES (1)", "INSERT INTO t VALUES (1)")
	if a.Success || a.message() != "UNIQUE constraint failed: t.id: SQLITE_CONSTRAINT" {
		t.Fatalf("a duplicate key: %+v", a)
	}
}

// TestForeignKeys: enforcement stays on through PRAGMA foreign_keys = OFF;
// defer_foreign_keys defers the checks to the end of the request, which a
// violation still fails, and lasts only for it.
func TestForeignKeys(t *testing.T) {
	s := newServer(t)
	run(t, s,
		"CREATE TABLE parent (id INTEGER PRIMARY KEY)",
		"CREATE TABLE child (id INTEGER PRIMARY KEY, parent_id INTEGER NOT NULL REFERENCES parent (id) ON DELETE CASCADE)",
		"INSERT INTO parent VALUES (1)",
		"INSERT INTO child VALUES (1, 1)",
	)
	a := run(t, s, "PRAGMA foreign_keys = OFF", "PRAGMA foreign_keys", "DELETE FROM parent")
	if got := rows(t, a, 1); got[0][0] != float64(1) {
		t.Fatalf("foreign_keys after PRAGMA foreign_keys = OFF: %v", got)
	}
	if got := rows(t, run(t, s, "SELECT count(*) FROM child"), 0); got[0][0] != float64(0) {
		t.Fatalf("the cascade did not run: %v children", got)
	}

	if a := run(t, s, "INSERT INTO child VALUES (2, 2)"); a.Success || a.message() != "FOREIGN KEY constraint failed: SQLITE_CONSTRAINT" {
		t.Fatalf("an orphan: %+v", a)
	}
	if a := run(t, s, "PRAGMA defer_foreign_keys = ON", "INSERT INTO child VALUES (2, 2)", "INSERT INTO parent VALUES (2)"); !a.Success {
		t.Fatalf("a deferred check: %s", a.message())
	}
	a = run(t, s, "PRAGMA defer_foreign_keys = ON", "INSERT INTO child VALUES (3, 3)")
	if a.Success || a.message() != "FOREIGN KEY constraint failed: SQLITE_CONSTRAINT" {
		t.Fatalf("an orphan at the end of a request: %+v", a)
	}
	if got := rows(t, run(t, s, "PRAGMA defer_foreign_keys", "SELECT count(*) FROM child"), 0); got[0][0] != float64(0) {
		t.Fatalf("defer_foreign_keys outlived its request: %v", got)
	}
}

// TestRefusals: BEGIN, COMMIT and their kin, and PRAGMAs outside D1's list,
// are refused.
func TestRefusals(t *testing.T) {
	s := newServer(t)
	for _, sql := range []string{"BEGIN", "BEGIN IMMEDIATE TRANSACTION", "COMMIT", "END", "ROLLBACK", "SAVEPOINT a", "RELEASE a", "  -- note\n begin"} {
		if a := run(t, s, sql); a.Success || !strings.Contains(a.message(), "state.storage.transaction()") {
			t.Errorf("%q: %+v", sql, a)
		}
	}
	for _, sql := range []string{"PRAGMA busy_timeout = 0", "PRAGMA main.journal_mode = DELETE"} {
		if a := run(t, s, sql); a.Success || a.message() != "not authorized: SQLITE_AUTH" {
			t.Errorf("%q: %+v", sql, a)
		}
	}
	for _, sql := range []string{"PRAGMA legacy_alter_table = ON", "PRAGMA main.table_list", "CREATE TABLE begin_here (a)"} {
		if a := run(t, s, sql); !a.Success {
			t.Errorf("%q: %s", sql, a.message())
		}
	}
}
