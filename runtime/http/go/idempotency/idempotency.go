// Package idempotency keeps idempotency keys (D140). An operation declared
// @idempotent answers a repeated call what the first one answered: a
// caller sends `Idempotency-Key`, the route keeps a row per operation,
// principal and key in the reserved table of the API's database, and a
// second request under the key gets the stored answer with
// `Idempotent-Replay: true`, never a second run.
//
// Before the handler the middleware inserts a `running` row. If it
// inserted, the handler runs: an answer under 500 is stored as `done`, and
// one of 500 or more deletes the row, so a retry runs again. A `done` row
// with the same request hash is replayed; another hash is 422
// `idempotency_key_reused`; a `running` row younger than the route's time
// limit is 409 `idempotency_in_flight` with Retry-After, and an older one
// is taken over. The row is not in the handler's transaction: a crash
// after the handler commits and before the answer is stored leaves
// `running`, and a repeat after the time limit runs the handler again. It
// is once per key but for that window.
//
// The TypeScript runtime's idempotency.ts keeps the same rows and decides
// each request alike (runtime/http/testdata/idempotency_parity.json).
package idempotency

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parable-work/superschematic/runtime/http/go/internal/sqldialect"
	"github.com/parable-work/superschematic/runtime/http/go/requestctx"
	"github.com/parable-work/superschematic/runtime/http/go/response"
	"github.com/parable-work/superschematic/runtime/http/go/serviceauth"
	"github.com/parable-work/superschematic/runtime/http/go/session"
)

const (
	// KeyHeader is the request header that carries the key.
	KeyHeader = "Idempotency-Key"
	// ReplayHeader is the response header of a stored answer.
	ReplayHeader = "Idempotent-Replay"
	// DefaultTTL is how long a key's answer is kept when @idempotent names
	// no ttl.
	DefaultTTL = 24 * time.Hour
	// DefaultInFlight is how long a running row holds its key on a route
	// with no @timeout.
	DefaultInFlight = 60 * time.Second
	// SweepInterval is how often a server sweeps expired rows, at most,
	// and SweepLimit how many it deletes.
	SweepInterval = time.Minute
	SweepLimit    = 100
)

// The problem codes of the middleware's refusals.
const (
	// CodeRequired is 400: the operation requires a key and the request
	// sent none.
	CodeRequired = "idempotency_key_required"
	// CodeInvalid is 400: the key is not 1 to 255 printable ASCII
	// characters.
	CodeInvalid = "idempotency_key_invalid"
	// CodeReused is 422: the key was used for another request.
	CodeReused = "idempotency_key_reused"
	// CodeInFlight is 409: the first request under the key is still
	// running.
	CodeInFlight = "idempotency_in_flight"
)

// IsKey reports whether key is one a caller may send: 1 to 255 printable
// ASCII characters, space among them.
func IsKey(key string) bool {
	if len(key) < 1 || len(key) > 255 {
		return false
	}
	for i := 0; i < len(key); i++ {
		if key[i] < 0x20 || key[i] > 0x7e {
			return false
		}
	}
	return true
}

// Lookup is what names a row: the SHA-256 of the operation, the principal
// and the key, each as its length in bytes, a colon and its bytes, so no
// two triples share a name. A key is one caller's, on one operation:
// another caller's same key is another row.
func Lookup(operation, principal, key string) string {
	h := sha256.New()
	for _, text := range []string{operation, principal, key} {
		_, _ = io.WriteString(h, strconv.Itoa(len(text))+":"+text)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// RequestHash is what a request is, for a key to be reused only with the
// same one: the SHA-256 of its method, a line feed, its path with its
// query, a line feed, and its body's bytes.
func RequestHash(method, pathAndQuery string, body []byte) string {
	h := sha256.New()
	_, _ = io.WriteString(h, strings.ToUpper(method)+"\n"+pathAndQuery+"\n")
	_, _ = h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// Attempt is one request under a key, as Begin takes it. Times are
// milliseconds since the epoch.
type Attempt struct {
	Lookup      string
	Operation   string
	Principal   string
	Key         string
	RequestHash string
	Now         int64
	// ExpiresAt is when the key's row expires.
	ExpiresAt int64
	// TakeOverBefore: a running row that started before it is taken over.
	TakeOverBefore int64
}

// Kind is what Begin decided.
type Kind string

const (
	// Started: the row is this request's, and the handler runs.
	Started Kind = "started"
	// Replay: a done row with the same hash, whose answer Begin holds.
	Replay Kind = "replay"
	// Mismatch: a row with another hash.
	Mismatch Kind = "mismatch"
	// Running: a running row with the same hash, too young to take over.
	Running Kind = "running"
)

// Begin is Store.Begin's answer.
type Begin struct {
	Kind Kind
	// Status and Body are a replay's stored answer.
	Status int
	Body   string
	// StartedAt is when a running row started.
	StartedAt int64
}

// Store is where the keys are kept. Finish and Abandon name the attempt by
// when it started, so an attempt whose row another took over changes
// nothing.
type Store interface {
	Begin(ctx context.Context, attempt Attempt) (Begin, error)
	// Finish stores the answer of the attempt that started at startedAt,
	// as done.
	Finish(ctx context.Context, lookup string, startedAt int64, status int, body string) error
	// Abandon deletes the running row of the attempt that started at
	// startedAt, so a retry runs again.
	Abandon(ctx context.Context, lookup string, startedAt int64) error
	// Sweep deletes up to limit rows that expired at or before now, and
	// answers how many.
	Sweep(ctx context.Context, now int64, limit int) (int64, error)
}

// Table is the reserved table's name and its columns' (D140), which the
// build reads from the DB schema's `implements IdempotencyKeys` class and
// its entrypoint hands the store.
type Table struct {
	Table   string
	Columns Columns
}

// Columns are the reserved table's columns.
type Columns struct {
	Lookup, Operation, Principal, Key, RequestHash, State, Status, Body, StartedAt, ExpiresAt string
}

// Dialect is the SQL the store writes.
type Dialect int

const (
	// Postgres numbers its placeholders.
	Postgres Dialect = iota
	// SQLite takes ? placeholders.
	SQLite
)

// SQLStore is the Store over a database/sql handle.
type SQLStore struct {
	db      *sql.DB
	dialect Dialect
	table   string
	c       Columns
}

// NewSQLStore is the store on db, a Postgres or a SQLite database whose
// schema declares the reserved table names describes.
func NewSQLStore(db *sql.DB, dialect Dialect, names Table) *SQLStore {
	quote := sqldialect.QuoteIdent
	c := names.Columns
	return &SQLStore{db: db, dialect: dialect, table: quote(names.Table), c: Columns{
		Lookup: quote(c.Lookup), Operation: quote(c.Operation), Principal: quote(c.Principal), Key: quote(c.Key),
		RequestHash: quote(c.RequestHash), State: quote(c.State), Status: quote(c.Status), Body: quote(c.Body),
		StartedAt: quote(c.StartedAt), ExpiresAt: quote(c.ExpiresAt),
	}}
}

// bind writes a statement's ? placeholders as the dialect numbers them,
// leaving quoted names and literals alone.
func (s *SQLStore) bind(query string) string {
	if s.dialect != Postgres {
		return query
	}
	return sqldialect.NumberPlaceholders(query)
}

func (s *SQLStore) exec(ctx context.Context, query string, args ...any) (int64, error) {
	result, err := s.db.ExecContext(ctx, s.bind(query), args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// beginAttempts is how often Begin starts over when another request
// changes the row under it.
const beginAttempts = 5

// Begin implements Store.
func (s *SQLStore) Begin(ctx context.Context, a Attempt) (Begin, error) {
	c := s.c
	for tries := 0; tries < beginAttempts; tries++ {
		inserted, err := s.exec(ctx,
			"INSERT INTO "+s.table+" ("+c.Lookup+", "+c.Operation+", "+c.Principal+", "+c.Key+", "+c.RequestHash+", "+c.State+", "+c.StartedAt+", "+c.ExpiresAt+") VALUES (?, ?, ?, ?, ?, 'running', ?, ?) ON CONFLICT ("+c.Lookup+") DO NOTHING",
			a.Lookup, a.Operation, a.Principal, a.Key, a.RequestHash, float64(a.Now), float64(a.ExpiresAt))
		if err != nil {
			return Begin{}, fmt.Errorf("idempotency: begin: %w", err)
		}
		if inserted == 1 {
			return Begin{Kind: Started}, nil
		}
		var hash, state string
		var status sql.NullFloat64
		var body sql.NullString
		var startedAt, expiresAt float64
		err = s.db.QueryRowContext(ctx, s.bind("SELECT "+c.RequestHash+", "+c.State+", "+c.Status+", "+c.Body+", "+c.StartedAt+", "+c.ExpiresAt+" FROM "+s.table+" WHERE "+c.Lookup+" = ?"), a.Lookup).
			Scan(&hash, &state, &status, &body, &startedAt, &expiresAt)
		if err == sql.ErrNoRows {
			// Swept or abandoned since the insert found it: insert again.
			continue
		}
		if err != nil {
			return Begin{}, fmt.Errorf("idempotency: begin: %w", err)
		}
		started := int64(math.Round(startedAt))
		expired := int64(math.Round(expiresAt)) <= a.Now
		stale := state == "running" && started < a.TakeOverBefore
		switch {
		case !expired && hash != a.RequestHash:
			return Begin{Kind: Mismatch}, nil
		case !expired && state == "done":
			return Begin{Kind: Replay, Status: int(math.Round(status.Float64)), Body: body.String}, nil
		case !expired && !stale:
			return Begin{Kind: Running, StartedAt: started}, nil
		}
		// An expired row, or a running one past the time limit, becomes
		// this request's, unless another request changed it first.
		taken, err := s.exec(ctx,
			"UPDATE "+s.table+" SET "+c.RequestHash+" = ?, "+c.State+" = 'running', "+c.Status+" = NULL, "+c.Body+" = NULL, "+c.StartedAt+" = ?, "+c.ExpiresAt+" = ? WHERE "+c.Lookup+" = ? AND "+c.StartedAt+" = ? AND "+c.State+" = ?",
			a.RequestHash, float64(a.Now), float64(a.ExpiresAt), a.Lookup, startedAt, state)
		if err != nil {
			return Begin{}, fmt.Errorf("idempotency: begin: %w", err)
		}
		if taken == 1 {
			return Begin{Kind: Started}, nil
		}
	}
	// Every try lost its row to another request: one of them is running.
	return Begin{Kind: Running, StartedAt: a.Now}, nil
}

// Finish implements Store.
func (s *SQLStore) Finish(ctx context.Context, lookup string, startedAt int64, status int, body string) error {
	c := s.c
	if _, err := s.exec(ctx, "UPDATE "+s.table+" SET "+c.State+" = 'done', "+c.Status+" = ?, "+c.Body+" = ? WHERE "+c.Lookup+" = ? AND "+c.StartedAt+" = ? AND "+c.State+" = 'running'",
		float64(status), body, lookup, float64(startedAt)); err != nil {
		return fmt.Errorf("idempotency: finish: %w", err)
	}
	return nil
}

// Abandon implements Store.
func (s *SQLStore) Abandon(ctx context.Context, lookup string, startedAt int64) error {
	c := s.c
	if _, err := s.exec(ctx, "DELETE FROM "+s.table+" WHERE "+c.Lookup+" = ? AND "+c.StartedAt+" = ? AND "+c.State+" = 'running'", lookup, float64(startedAt)); err != nil {
		return fmt.Errorf("idempotency: abandon: %w", err)
	}
	return nil
}

// Sweep implements Store.
func (s *SQLStore) Sweep(ctx context.Context, now int64, limit int) (int64, error) {
	c := s.c
	swept, err := s.exec(ctx, "DELETE FROM "+s.table+" WHERE "+c.Lookup+" IN (SELECT "+c.Lookup+" FROM "+s.table+" WHERE "+c.ExpiresAt+" <= ? ORDER BY "+c.ExpiresAt+" LIMIT ?)", float64(now), limit)
	if err != nil {
		return 0, fmt.Errorf("idempotency: sweep: %w", err)
	}
	return swept, nil
}

// Options are one route's @idempotent, as its middleware takes it.
type Options struct {
	// Store keeps the keys.
	Store Store
	// Operation names the operation, `<namespace>.<name>`.
	Operation string
	// TTL is how long a key's answer is kept; zero is DefaultTTL. A row is
	// kept at least InFlight, whatever TTL says.
	TTL time.Duration
	// Required refuses a request without the header.
	Required bool
	// InFlight is the route's time limit, past which a running row is
	// taken over; zero is DefaultInFlight.
	InFlight time.Duration
	// Principal reads the caller a key belongs to, "" for a request with
	// none; nil reads none.
	Principal func(*http.Request) string
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// sweeper sweeps a store at most once per SweepInterval.
type sweeper struct {
	mu   sync.Mutex
	last map[Store]int64
}

var sweeps = sweeper{last: map[Store]int64{}}

func (s *sweeper) due(store Store, now int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if now-s.last[store] < SweepInterval.Milliseconds() {
		return false
	}
	s.last[store] = now
	return true
}

// recorder passes a handler's answer on and keeps a copy of it.
type recorder struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (r *recorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *recorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	r.body.Write(p)
	return r.ResponseWriter.Write(p)
}

// Unwrap lets http.ResponseController reach the writer under it.
func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// CallerPrincipal names the caller a generated route keeps a key for: the
// authenticated principal's ID the auth middleware put on the request
// (session.GetPrincipalID); for a service that calls with no end user,
// "service:" and its deployable; "" for a request with neither, a public
// route's. The TypeScript router names a caller the same way, so two
// servers over one database share their keys.
func CallerPrincipal(r *http.Request) string {
	if principal := session.GetPrincipalID(r.Context()); principal != "" {
		return principal
	}
	if caller, ok := serviceauth.CallerFromContext(r.Context()); ok {
		return "service:" + caller.Deployable
	}
	return ""
}

// Middleware runs a route under the request's key: the handler runs once
// per key, and a repeat is answered what the first request was.
func Middleware(opts Options) func(http.Handler) http.Handler {
	ttl, inFlight, now := opts.TTL, opts.InFlight, opts.Now
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if inFlight <= 0 {
		inFlight = DefaultInFlight
	}
	if now == nil {
		now = time.Now
	}
	// A row expires no sooner than its in-flight limit, so a ttl shorter
	// than the route's time limit never lets a repeat take over a first
	// request that may still be running, nor a sweep delete its row.
	keep := max(ttl, inFlight)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// A route's check (D98) keeps no key: the key is the run's, and
			// a check that kept it would answer the run what it answered.
			if requestctx.IsRouteCheck(r.Context()) {
				next.ServeHTTP(w, r)
				return
			}
			keys, sent := r.Header[KeyHeader]
			if !sent {
				if opts.Required {
					response.RequestError(w, r, http.StatusBadRequest, opts.Operation+" needs an "+KeyHeader+" header", CodeRequired)
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			key := strings.Join(keys, ", ")
			if !IsKey(key) {
				response.RequestError(w, r, http.StatusBadRequest, KeyHeader+" is 1 to 255 printable ASCII characters", CodeInvalid)
				return
			}
			var body []byte
			if r.Body != nil {
				read, err := io.ReadAll(r.Body)
				if err != nil {
					// The body limit's refusal, or a broken connection: the
					// handler answers it as it does without a key.
					r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(read), errReader{err}))
					next.ServeHTTP(w, r)
					return
				}
				body = read
				r.Body = io.NopCloser(bytes.NewReader(body))
			}
			principal := ""
			if opts.Principal != nil {
				principal = opts.Principal(r)
			}
			pathAndQuery := r.URL.EscapedPath()
			if r.URL.RawQuery != "" {
				pathAndQuery += "?" + r.URL.RawQuery
			}
			lookup := Lookup(opts.Operation, principal, key)
			startedAt := now().UnixMilli()
			begin, err := opts.Store.Begin(r.Context(), Attempt{
				Lookup: lookup, Operation: opts.Operation, Principal: principal, Key: key,
				RequestHash: RequestHash(r.Method, pathAndQuery, body),
				Now:         startedAt, ExpiresAt: startedAt + keep.Milliseconds(), TakeOverBefore: startedAt - inFlight.Milliseconds(),
			})
			if err != nil {
				response.LoggedError(w, r, http.StatusInternalServerError, "An unexpected error occurred", "internal_error")
				return
			}
			switch begin.Kind {
			case Replay:
				contentType := "application/json"
				if begin.Status >= 400 {
					contentType = "application/problem+json"
				}
				w.Header().Set("Content-Type", contentType)
				w.Header().Set("Cache-Control", "no-store")
				w.Header().Set(ReplayHeader, "true")
				w.WriteHeader(begin.Status)
				if begin.Status != http.StatusNoContent && begin.Status != http.StatusNotModified {
					_, _ = io.WriteString(w, begin.Body)
				}
				return
			case Mismatch:
				response.RequestError(w, r, http.StatusUnprocessableEntity, "This "+KeyHeader+" was used for another request", CodeReused)
				return
			case Running:
				retryAfter := int(math.Ceil(float64(begin.StartedAt+inFlight.Milliseconds()-startedAt) / 1000))
				if retryAfter < 1 {
					retryAfter = 1
				}
				w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
				response.RequestErrorWithDetails(w, r, http.StatusConflict, "The first request under this "+KeyHeader+" is still running", CodeInFlight, map[string]any{"retryAfterSeconds": retryAfter})
				return
			}
			if sweeps.due(opts.Store, startedAt) {
				go func() { _, _ = opts.Store.Sweep(context.WithoutCancel(r.Context()), startedAt, SweepLimit) }()
			}
			record := &recorder{ResponseWriter: w}
			finished := false
			// A handler that panics stored nothing: the row goes, so a
			// retry runs again.
			defer func() {
				if !finished {
					_ = opts.Store.Abandon(context.WithoutCancel(r.Context()), lookup, startedAt)
				}
			}()
			next.ServeHTTP(record, r)
			status := record.status
			if status == 0 {
				status = http.StatusOK
			}
			if status >= 500 {
				return
			}
			finished = true
			// The request's context may be done once the answer is written.
			_ = opts.Store.Finish(context.WithoutCancel(r.Context()), lookup, startedAt, status, record.body.String())
		})
	}
}

// errReader fails every read with its error.
type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }
