package idempotency

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/parable-work/superschematic/runtime/http/go/serviceauth"
	"github.com/parable-work/superschematic/runtime/http/go/session"
	_ "modernc.org/sqlite"
)

// postgresURLEnv names a Postgres the scenarios also run on, as the
// identity store's tests name theirs.
const postgresURLEnv = "SUPERSCHEMATIC_IDENTITY_TEST_DATABASE_URL"

// names is the reserved table as a DB schema's naming writes it.
var names = Table{Table: "idempotency_key", Columns: Columns{
	Lookup: "lookup", Operation: "operation", Principal: "principal", Key: "key", RequestHash: "request_hash",
	State: "state", Status: "status", Body: "body", StartedAt: "started_at", ExpiresAt: "expires_at",
}}

// ddl creates the reserved table as sqlgen writes it for each dialect: the
// times and the status are numbers.
func ddl(table, number string) string {
	return `CREATE TABLE "` + table + `" (
  "lookup" TEXT NOT NULL UNIQUE, "operation" TEXT NOT NULL, "principal" TEXT NOT NULL, "key" TEXT NOT NULL,
  "request_hash" TEXT NOT NULL, "state" TEXT NOT NULL, "status" ` + number + `, "body" TEXT,
  "started_at" ` + number + ` NOT NULL, "expires_at" ` + number + ` NOT NULL)`
}

// stores opens an empty store per dialect the test can reach.
func stores(t *testing.T) map[string]func() Store {
	t.Helper()
	out := map[string]func() Store{
		"sqlite": func() Store {
			db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "keys.db")+"?_pragma=busy_timeout(5000)")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			if _, err := db.Exec(ddl(names.Table, "REAL")); err != nil {
				t.Fatal(err)
			}
			return NewSQLStore(db, SQLite, names)
		},
	}
	if url := os.Getenv(postgresURLEnv); url != "" {
		var tables atomic.Int64
		out["postgres"] = func() Store {
			db, err := sql.Open("pgx", url)
			if err != nil {
				t.Fatal(err)
			}
			table := fmt.Sprintf("idempotency_key_go_%d_%d", os.Getpid(), tables.Add(1))
			if _, err := db.Exec(ddl(table, "DOUBLE PRECISION")); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = db.Exec(`DROP TABLE "` + table + `"`)
				_ = db.Close()
			})
			table2 := names
			table2.Table = table
			return NewSQLStore(db, Postgres, table2)
		}
	}
	return out
}

// parity is runtime/http/testdata/idempotency_parity.json.
type parity struct {
	Keys struct {
		Valid, Invalid []string
	}
	Lookups []struct {
		Name, Operation, Principal, Key, Lookup string
	}
	Hashes []struct {
		Name, Method, Path, Body, Hash string
	}
	Scenarios []struct {
		Name  string
		Steps []struct {
			Call        string
			At          int64
			Lookup      string
			RequestHash string
			TTLMs       int64 `json:"ttlMs"`
			InFlightMs  int64
			StartedAt   int64
			Status      int
			Body        string
			Limit       int
			Want        json.RawMessage
		}
	}
}

func readParity(t *testing.T) parity {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "idempotency_parity.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vectors parity
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	if len(vectors.Keys.Valid) == 0 || len(vectors.Keys.Invalid) == 0 || len(vectors.Lookups) == 0 || len(vectors.Hashes) == 0 || len(vectors.Scenarios) == 0 {
		t.Fatal("a section holds no vector")
	}
	return vectors
}

// TestParityVectors: IsKey, Lookup and RequestHash answer each vector of
// idempotency_parity.json, and the SQL store each scenario, as the
// TypeScript runtime does.
func TestParityVectors(t *testing.T) {
	vectors := readParity(t)
	for _, key := range vectors.Keys.Valid {
		if !IsKey(key) {
			t.Errorf("IsKey(%q) = false", key)
		}
	}
	for _, key := range vectors.Keys.Invalid {
		if IsKey(key) {
			t.Errorf("IsKey(%q) = true", key)
		}
	}
	for _, vector := range vectors.Lookups {
		if got := Lookup(vector.Operation, vector.Principal, vector.Key); got != vector.Lookup {
			t.Errorf("%s: Lookup = %s, want %s", vector.Name, got, vector.Lookup)
		}
	}
	for _, vector := range vectors.Hashes {
		if got := RequestHash(vector.Method, vector.Path, []byte(vector.Body)); got != vector.Hash {
			t.Errorf("%s: RequestHash = %s, want %s", vector.Name, got, vector.Hash)
		}
	}
	ctx := context.Background()
	for dialect, open := range stores(t) {
		for _, scenario := range vectors.Scenarios {
			t.Run(dialect+"/"+scenario.Name, func(t *testing.T) {
				store := open()
				for i, step := range scenario.Steps {
					switch step.Call {
					case "begin":
						var want struct {
							Kind      Kind
							Status    int
							Body      string
							StartedAt int64
						}
						if err := json.Unmarshal(step.Want, &want); err != nil {
							t.Fatal(err)
						}
						got, err := store.Begin(ctx, Attempt{
							Lookup: step.Lookup, Operation: "orders.createOrder", Principal: "user-1", Key: "k", RequestHash: step.RequestHash,
							Now: step.At, ExpiresAt: step.At + step.TTLMs, TakeOverBefore: step.At - step.InFlightMs,
						})
						if err != nil || got != (Begin{Kind: want.Kind, Status: want.Status, Body: want.Body, StartedAt: want.StartedAt}) {
							t.Fatalf("step %d: Begin = %+v, %v, want %+v", i, got, err, want)
						}
					case "finish":
						if err := store.Finish(ctx, step.Lookup, step.StartedAt, step.Status, step.Body); err != nil {
							t.Fatalf("step %d: Finish: %v", i, err)
						}
					case "abandon":
						if err := store.Abandon(ctx, step.Lookup, step.StartedAt); err != nil {
							t.Fatalf("step %d: Abandon: %v", i, err)
						}
					case "sweep":
						var want int64
						if err := json.Unmarshal(step.Want, &want); err != nil {
							t.Fatal(err)
						}
						if got, err := store.Sweep(ctx, step.At, step.Limit); err != nil || got != want {
							t.Fatalf("step %d: Sweep = %d, %v, want %d", i, got, err, want)
						}
					default:
						t.Fatalf("step %d: unknown call %q", i, step.Call)
					}
				}
			})
		}
	}
}

// TestBeginRace: of many requests under one key at once, one starts and
// the others find it running.
func TestBeginRace(t *testing.T) {
	for dialect, open := range stores(t) {
		t.Run(dialect, func(t *testing.T) {
			store := open()
			var started, running atomic.Int64
			var wg sync.WaitGroup
			for range 16 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					got, err := store.Begin(context.Background(), Attempt{Lookup: strings.Repeat("9", 64), Operation: "o", Key: "k", RequestHash: "h", Now: 1000, ExpiresAt: 2000, TakeOverBefore: 0})
					switch {
					case err != nil:
						t.Error(err)
					case got.Kind == Started:
						started.Add(1)
					case got.Kind == Running:
						running.Add(1)
					}
				}()
			}
			wg.Wait()
			if started.Load() != 1 || running.Load() != 15 {
				t.Errorf("%d started and %d found it running, want 1 and 15", started.Load(), running.Load())
			}
		})
	}
}

// clock is a time a test moves.
type clock struct{ at atomic.Int64 }

func (c *clock) now() time.Time { return time.UnixMilli(c.at.Load()) }

// serve is a route under the middleware whose handler counts its runs and
// answers what answer says.
func serve(t *testing.T, opts Options, answer func(w http.ResponseWriter, r *http.Request, run int64)) (http.Handler, *atomic.Int64) {
	t.Helper()
	var runs atomic.Int64
	handler := Middleware(opts)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		answer(w, r, runs.Add(1))
	}))
	return chimiddleware.RequestID(handler), &runs
}

func call(handler http.Handler, method, path, body string, headers ...string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	for i := 0; i+1 < len(headers); i += 2 {
		request.Header.Set(headers[i], headers[i+1])
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func problemCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var problem struct{ Code string }
	if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
		t.Fatalf("no problem body: %s", recorder.Body)
	}
	return problem.Code
}

// TestMiddleware: a request with a key runs the handler once, which reads
// the body the middleware hashed; a repeat is answered the stored status
// and body with Idempotent-Replay, and never runs the handler; another
// body is 422; another principal's same key is its own; a request without
// the header runs each time, unless the route requires one; a key no
// header may hold is 400; and a 500 stores nothing, so a retry runs again.
func TestMiddleware(t *testing.T) {
	for dialect, open := range stores(t) {
		t.Run(dialect, func(t *testing.T) {
			now := &clock{}
			now.at.Store(1_800_000_000_000)
			opts := Options{Store: open(), Operation: "orders.createOrder", Now: now.now, Principal: func(r *http.Request) string { return r.Header.Get("X-User") }}
			handler, runs := serve(t, opts, func(w http.ResponseWriter, r *http.Request, run int64) {
				body, _ := io.ReadAll(r.Body)
				if strings.Contains(string(body), "fail") {
					http.Error(w, "boom", http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_, _ = fmt.Fprintf(w, `{"data":{"run":%d,"body":%q},"meta":{}}`, run, body)
			})

			first := call(handler, "POST", "/api/orders", `{"sku":"tea"}`, KeyHeader, "order-7", "X-User", "u1")
			if first.Code != http.StatusCreated || first.Header().Get(ReplayHeader) != "" || runs.Load() != 1 {
				t.Fatalf("first: %d %s, %d runs", first.Code, first.Body, runs.Load())
			}
			want := `{"data":{"run":1,"body":"{\"sku\":\"tea\"}"},"meta":{}}`
			if first.Body.String() != want {
				t.Errorf("first body = %s", first.Body)
			}
			again := call(handler, "POST", "/api/orders", `{"sku":"tea"}`, KeyHeader, "order-7", "X-User", "u1")
			if again.Code != http.StatusCreated || again.Body.String() != want || again.Header().Get(ReplayHeader) != "true" ||
				again.Header().Get("Content-Type") != "application/json" || runs.Load() != 1 {
				t.Errorf("repeat: %d %s %v, %d runs", again.Code, again.Body, again.Header(), runs.Load())
			}
			other := call(handler, "POST", "/api/orders", `{"sku":"coffee"}`, KeyHeader, "order-7", "X-User", "u1")
			if other.Code != http.StatusUnprocessableEntity || problemCode(t, other) != CodeReused || runs.Load() != 1 {
				t.Errorf("another body: %d %s", other.Code, other.Body)
			}
			if query := call(handler, "POST", "/api/orders?dryRun=true", `{"sku":"tea"}`, KeyHeader, "order-7", "X-User", "u1"); query.Code != http.StatusUnprocessableEntity {
				t.Errorf("another query: %d", query.Code)
			}
			if second := call(handler, "POST", "/api/orders", `{"sku":"tea"}`, KeyHeader, "order-7", "X-User", "u2"); second.Code != http.StatusCreated || second.Header().Get(ReplayHeader) != "" || runs.Load() != 2 {
				t.Errorf("another principal: %d, %d runs", second.Code, runs.Load())
			}
			for range 2 {
				if none := call(handler, "POST", "/api/orders", `{"sku":"tea"}`, "X-User", "u1"); none.Code != http.StatusCreated {
					t.Errorf("no key: %d", none.Code)
				}
			}
			if runs.Load() != 4 {
				t.Errorf("%d runs, want 4: a request without a key runs each time", runs.Load())
			}
			for _, key := range []string{"", strings.Repeat("k", 256), "caf\u00e9"} {
				if bad := call(handler, "POST", "/api/orders", `{}`, KeyHeader, key); bad.Code != http.StatusBadRequest || problemCode(t, bad) != CodeInvalid {
					t.Errorf("key %q: %d %s", key, bad.Code, bad.Body)
				}
			}

			// A 500 stores nothing: the retry runs again, and its answer is kept.
			if failed := call(handler, "POST", "/api/orders", `{"fail":true}`, KeyHeader, "order-8"); failed.Code != http.StatusInternalServerError {
				t.Errorf("a failing run: %d", failed.Code)
			}
			if retried := call(handler, "POST", "/api/orders", `{"fail":true}`, KeyHeader, "order-8"); retried.Code != http.StatusInternalServerError || retried.Header().Get(ReplayHeader) != "" {
				t.Errorf("its retry: %d %v", retried.Code, retried.Header())
			}
			if runs.Load() != 6 {
				t.Errorf("%d runs, want 6: a 500 is run again", runs.Load())
			}

			// Past its ttl the key is new.
			now.at.Add(DefaultTTL.Milliseconds())
			if late := call(handler, "POST", "/api/orders", `{"sku":"coffee"}`, KeyHeader, "order-7", "X-User", "u1"); late.Code != http.StatusCreated || late.Header().Get(ReplayHeader) != "" {
				t.Errorf("after the ttl: %d %s", late.Code, late.Body)
			}

			required, requiredRuns := serve(t, Options{Store: opts.Store, Operation: "orders.payOrder", Required: true, Now: now.now}, func(w http.ResponseWriter, _ *http.Request, _ int64) {
				w.WriteHeader(http.StatusNoContent)
			})
			if none := call(required, "POST", "/api/orders/7/pay", ""); none.Code != http.StatusBadRequest || problemCode(t, none) != CodeRequired || requiredRuns.Load() != 0 {
				t.Errorf("a required key that is missing: %d %s", none.Code, none.Body)
			}
			for i := range 2 {
				paid := call(required, "POST", "/api/orders/7/pay", "", KeyHeader, "pay-1")
				if paid.Code != http.StatusNoContent || paid.Body.Len() != 0 || (paid.Header().Get(ReplayHeader) == "true") != (i == 1) {
					t.Errorf("pay %d: %d %q %v", i, paid.Code, paid.Body, paid.Header())
				}
			}
			if requiredRuns.Load() != 1 {
				t.Errorf("the required route ran %d times, want 1", requiredRuns.Load())
			}
		})
	}
}

// TestInFlight: a repeat while the first request runs is 409 with
// Retry-After, the seconds left of the route's time limit; past the limit
// it takes the key over and runs; and a handler that panics leaves no row.
func TestInFlight(t *testing.T) {
	for dialect, open := range stores(t) {
		t.Run(dialect, func(t *testing.T) {
			now := &clock{}
			now.at.Store(1_800_000_000_000)
			release := make(chan struct{})
			entered := make(chan struct{}, 4)
			handler, runs := serve(t, Options{Store: open(), Operation: "orders.createOrder", InFlight: 30 * time.Second, Now: now.now}, func(w http.ResponseWriter, r *http.Request, run int64) {
				if r.Header.Get("X-Panic") != "" {
					panic("the handler broke")
				}
				entered <- struct{}{}
				if run == 1 {
					<-release
				}
				_, _ = fmt.Fprintf(w, `{"run":%d}`, run)
			})
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- call(handler, "POST", "/api/orders", `{}`, KeyHeader, "slow") }()
			<-entered

			now.at.Add(10_500)
			waiting := call(handler, "POST", "/api/orders", `{}`, KeyHeader, "slow")
			if waiting.Code != http.StatusConflict || problemCode(t, waiting) != CodeInFlight || waiting.Header().Get("Retry-After") != "20" {
				t.Errorf("while the first runs: %d %s, Retry-After %q", waiting.Code, waiting.Body, waiting.Header().Get("Retry-After"))
			}
			now.at.Add(19_501)
			if took := call(handler, "POST", "/api/orders", `{}`, KeyHeader, "slow"); took.Code != http.StatusOK || took.Body.String() != `{"run":2}` {
				t.Errorf("past the time limit: %d %s", took.Code, took.Body)
			}
			close(release)
			if first := <-done; first.Code != http.StatusOK || first.Body.String() != `{"run":1}` {
				t.Errorf("the first request: %d %s", first.Code, first.Body)
			}
			// The first attempt lost its row, so the second's answer is kept.
			if replay := call(handler, "POST", "/api/orders", `{}`, KeyHeader, "slow"); replay.Body.String() != `{"run":2}` || replay.Header().Get(ReplayHeader) != "true" {
				t.Errorf("the replay: %s %v", replay.Body, replay.Header())
			}
			if runs.Load() != 2 {
				t.Errorf("%d runs, want 2", runs.Load())
			}

			func() {
				defer func() { _ = recover() }()
				call(handler, "POST", "/api/orders", `{}`, KeyHeader, "broken", "X-Panic", "1")
			}()
			if after := call(handler, "POST", "/api/orders", `{}`, KeyHeader, "broken"); after.Code != http.StatusOK || after.Header().Get(ReplayHeader) != "" {
				t.Errorf("after a panic: %d %s", after.Code, after.Body)
			}
		})
	}
}

// TestShortTTLHoldsTheKeyWhileTheFirstRuns: a ttl shorter than the
// route's time limit keeps a running row until the limit, so a repeat
// after the ttl and before the limit is 409 and does not run the handler
// a second time, and a sweep then leaves the row.
func TestShortTTLHoldsTheKeyWhileTheFirstRuns(t *testing.T) {
	for dialect, open := range stores(t) {
		t.Run(dialect, func(t *testing.T) {
			now := &clock{}
			now.at.Store(1_800_000_000_000)
			release := make(chan struct{})
			entered := make(chan struct{}, 4)
			store := open()
			handler, runs := serve(t, Options{Store: store, Operation: "orders.createOrder", TTL: time.Second, InFlight: 45 * time.Second, Now: now.now}, func(w http.ResponseWriter, _ *http.Request, run int64) {
				entered <- struct{}{}
				if run == 1 {
					<-release
				}
				_, _ = fmt.Fprintf(w, `{"run":%d}`, run)
			})
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- call(handler, "POST", "/api/orders", `{}`, KeyHeader, "short") }()
			<-entered

			now.at.Add(31_000)
			if swept, err := store.Sweep(context.Background(), now.at.Load(), 100); err != nil || swept != 0 {
				t.Errorf("a sweep past the ttl and within the limit = %d, %v, want 0", swept, err)
			}
			waiting := call(handler, "POST", "/api/orders", `{}`, KeyHeader, "short")
			if waiting.Code != http.StatusConflict || problemCode(t, waiting) != CodeInFlight || waiting.Header().Get("Retry-After") != "14" {
				t.Errorf("past the ttl, within the limit: %d %s, Retry-After %q", waiting.Code, waiting.Body, waiting.Header().Get("Retry-After"))
			}
			close(release)
			if first := <-done; first.Code != http.StatusOK || first.Body.String() != `{"run":1}` {
				t.Errorf("the first request: %d %s", first.Code, first.Body)
			}
			if runs.Load() != 1 {
				t.Errorf("%d runs, want 1", runs.Load())
			}
		})
	}
}

// TestCallerPrincipal: a key is the authenticated principal's, a service
// caller's when it forwards no end user, and no one's on a public route.
func TestCallerPrincipal(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/orders", nil)
	if got := CallerPrincipal(request); got != "" {
		t.Errorf("a request with no caller is %q's", got)
	}
	service := request.WithContext(serviceauth.WithCaller(request.Context(), &serviceauth.Caller{Deployable: "shop-web"}))
	if got := CallerPrincipal(service); got != "service:shop-web" {
		t.Errorf("a service caller's request is %q's", got)
	}
	user := service.WithContext(session.ContextWithPrincipalID(service.Context(), "ada"))
	if got := CallerPrincipal(user); got != "ada" {
		t.Errorf("a request a service forwards a user on is %q's", got)
	}
}

// TestBindLeavesQuotedTextAlone: a ? in a configured table or column name,
// which the store quotes, or in a literal, is no placeholder.
func TestBindLeavesQuotedTextAlone(t *testing.T) {
	cols := Columns{Lookup: "look?up", State: `st"?"ate`, Status: "status", Body: "body", StartedAt: "started_at"}
	s := NewSQLStore(nil, Postgres, Table{Table: "idem?keys", Columns: cols})
	c := s.c
	query := "UPDATE " + s.table + " SET " + c.State + " = 'done?', " + c.Status + " = ?, " + c.Body + " = ? WHERE " + c.Lookup + " = ? AND " + c.StartedAt + " = ?"
	want := `UPDATE "idem?keys" SET "st""?""ate" = 'done?', "status" = $1, "body" = $2 WHERE "look?up" = $3 AND "started_at" = $4`
	if got := s.bind(query); got != want {
		t.Errorf("Postgres bind = %s, want %s", got, want)
	}
	if got := NewSQLStore(nil, SQLite, Table{Table: "idem?keys", Columns: cols}).bind(query); got != query {
		t.Errorf("SQLite bind = %s, want the query as written", got)
	}
}
