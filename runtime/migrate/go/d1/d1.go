// Package d1 is the runner's Cloudflare D1 driver (D27, amended: SQLite
// rebuilds with foreign keys on, and the runner on D1). It reaches a D1
// database through Cloudflare's REST API and runs SQLite plans.
//
// D1 has no BEGIN or COMMIT: one request runs its statements as one batch.
// So a transaction holds its writes and sends them as one request when it
// commits, led by PRAGMA defer_foreign_keys = ON and the renewal of the
// lease; a query runs at once, as a request of its own. A transactional
// step, its log row and any state change are therefore one batch, and a
// repeated or concurrent run of the step fails on the log row's primary
// key. A lease in superschematic_lock stands in for Postgres's advisory
// lock.
//
// The driver is unverified until TestRealD1 has passed against a D1
// database: Cloudflare documents a Worker's batch as a transaction, not a
// REST request's.
//
// The API it relies on:
//   - https://developers.cloudflare.com/api/resources/d1/subresources/database/methods/raw/
//     POST /accounts/{account_id}/d1/database/{database_id}/raw takes
//     {"sql", "params"} or {"batch": [{"sql", "params"}, ...]}, params an
//     array of strings, and answers {"success", "errors", "messages",
//     "result": [{"success", "results": {"columns", "rows"}, "meta":
//     {"changes", ...}}]}, one result per statement. The query endpoint
//     answers the same with each row a JSON object; raw keeps the columns
//     in order, which a positional scan needs.
//   - https://developers.cloudflare.com/d1/worker-api/prepared-statements/
//     "D1 only supports Ordered (?NNNN) and Anonymous (?) parameters", so
//     the runner's $N becomes ?N.
//   - https://developers.cloudflare.com/d1/sql-api/foreign-keys/ and
//     https://developers.cloudflare.com/d1/sql-api/sql-statements/: foreign
//     keys stay on, and defer_foreign_keys lasts until the end of the
//     transaction.
//
// A request whose sql holds several statements takes no params (D1 answers
// "params with multiple statements is not supported", a reported answer,
// not a documented one), so a batch uses the batch form, each statement
// with its own params.
package d1

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	migrate "github.com/parable-work/superschematic/runtime/migrate/go"
)

// TokenEnv is the environment variable superschematic-migrate reads the API
// token from.
const TokenEnv = "CLOUDFLARE_API_TOKEN"

// DefaultBaseURL is Cloudflare's API.
const DefaultBaseURL = "https://api.cloudflare.com/client/v4"

const (
	// DefaultLease is how long a lease lasts after each renewal.
	DefaultLease = 2 * time.Minute
	// DefaultLockWait is how long Lock waits for another runner's lease.
	DefaultLockWait = 10 * time.Minute
	// DefaultLockPoll is how often Lock tries a lease another runner
	// holds.
	DefaultLockPoll = time.Second
	// DefaultRequestTimeout bounds one request of the default client.
	DefaultRequestTimeout = 2 * time.Minute
)

// maxResponse bounds the body the driver reads from one response.
const maxResponse = 64 << 20

// Options configure a Driver.
type Options struct {
	// Token is a Cloudflare API token that may edit the database. Open
	// refuses an empty one; superschematic-migrate reads it from
	// CLOUDFLARE_API_TOKEN.
	Token string
	// BaseURL is the API's base URL. Empty is DefaultBaseURL; tests point
	// it at a fake server.
	BaseURL string
	// HTTPClient sends the requests. Nil is a client whose timeout is
	// DefaultRequestTimeout.
	HTTPClient *http.Client
	// Lease is how long the lease lasts after each renewal. Zero is
	// DefaultLease.
	Lease time.Duration
	// LockWait is how long Lock waits for a lease another runner holds.
	// Zero is DefaultLockWait.
	LockWait time.Duration
	// LockPoll is how often Lock tries again. Zero is DefaultLockPoll.
	LockPoll time.Duration
	// Holder names this runner in the lease. Empty is the host name, the
	// process id and a random suffix.
	Holder string
}

// Driver is a migrate.Driver over D1's REST API.
type Driver struct {
	endpoint string
	token    string
	client   *http.Client
	lease    time.Duration
	lockWait time.Duration
	lockPoll time.Duration
	holder   string

	mu sync.Mutex
	// leased is the service whose lease the driver holds, or empty.
	leased string
}

// IsURL reports whether url is a d1:// URL.
func IsURL(url string) bool {
	return len(url) >= len("d1://") && strings.EqualFold(url[:len("d1://")], "d1://")
}

// ParseURL reads a d1://<account id>/<database id> URL.
func ParseURL(raw string) (account, database string, err error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "d1" {
		return "", "", fmt.Errorf("d1: %q is not a d1://<account id>/<database id> URL", raw)
	}
	database = strings.TrimPrefix(u.Path, "/")
	if u.Host == "" || database == "" || strings.Contains(database, "/") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", "", fmt.Errorf("d1: %q is not a d1://<account id>/<database id> URL", raw)
	}
	return u.Host, database, nil
}

// Open returns a driver for the database a d1:// URL names. It sends no
// request.
func Open(_ context.Context, databaseURL string, opts Options) (*Driver, error) {
	account, database, err := ParseURL(databaseURL)
	if err != nil {
		return nil, err
	}
	if opts.Token == "" {
		return nil, fmt.Errorf("d1: no API token: set %s to a Cloudflare API token that may edit the database", TokenEnv)
	}
	base := strings.TrimRight(opts.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	d := &Driver{
		endpoint: base + "/accounts/" + url.PathEscape(account) + "/d1/database/" + url.PathEscape(database) + "/raw",
		token:    opts.Token,
		client:   opts.HTTPClient,
		lease:    opts.Lease,
		lockWait: opts.LockWait,
		lockPoll: opts.LockPoll,
		holder:   opts.Holder,
	}
	if d.client == nil {
		d.client = &http.Client{Timeout: DefaultRequestTimeout}
	}
	if d.lease <= 0 {
		d.lease = DefaultLease
	}
	if d.lockWait <= 0 {
		d.lockWait = DefaultLockWait
	}
	if d.lockPoll <= 0 {
		d.lockPoll = DefaultLockPoll
	}
	if d.holder == "" {
		host, _ := os.Hostname()
		var suffix [4]byte
		_, _ = rand.Read(suffix[:])
		d.holder = fmt.Sprintf("%s:%d:%x", host, os.Getpid(), suffix)
	}
	return d, nil
}

// Close closes the client's idle connections. The lease is released by the
// function Lock returned.
func (d *Driver) Close(context.Context) error {
	d.client.CloseIdleConnections()
	return nil
}

// Dialect is migrate.SQLite: D1 runs SQLite plans.
func (d *Driver) Dialect() migrate.Dialect { return migrate.SQLite }

// The lease (D27, amended). Times are D1's clock, as RFC 3339 UTC text with
// milliseconds, so they compare as text and two runners' clocks never
// disagree.
const (
	lockTable = "superschematic_lock"

	createLockTable = `CREATE TABLE IF NOT EXISTS ` + lockTable + ` (
  service    TEXT PRIMARY KEY,
  holder     TEXT NOT NULL,
  expires_at TEXT NOT NULL
)`

	// takeLease is the one conditional write that takes a lease: it inserts
	// the service's row, or takes over a row whose lease has expired. It
	// changes one row when the caller holds the lease and none when another
	// runner's lease has not expired.
	takeLease = `INSERT INTO ` + lockTable + ` (service, holder, expires_at)
VALUES (?1, ?2, strftime('%Y-%m-%dT%H:%M:%fZ', 'now', ?3))
ON CONFLICT (service) DO UPDATE SET holder = excluded.holder, expires_at = excluded.expires_at
WHERE ` + lockTable + `.expires_at < strftime('%Y-%m-%dT%H:%M:%fZ', 'now')`

	// renewLease leads every batch while the driver holds a lease. It
	// extends the lease; when another runner has taken it over, it sets the
	// holder to NULL, which the NOT NULL constraint refuses, so the batch
	// fails and none of it takes effect.
	renewLease = `UPDATE ` + lockTable + ` SET holder = CASE WHEN holder = ?2 THEN holder END,
  expires_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now', ?3)
WHERE service = ?1`

	// releaseLease ends the lease and keeps the row, so a runner that lost
	// its lease still finds another holder in it.
	releaseLease = `UPDATE ` + lockTable + ` SET expires_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE service = ?1 AND holder = ?2`

	readLease = `SELECT holder, expires_at FROM ` + lockTable + ` WHERE service = ?1`
)

// Lock takes the service's lease, waiting up to LockWait for another
// runner's lease to be released or to expire, and trying again every
// LockPoll. Every batch the driver sends renews it, and fails once another
// runner has taken it over.
func (d *Driver) Lock(ctx context.Context, service string) (func(context.Context) error, error) {
	if _, err := d.send(ctx, []statement{{sql: createLockTable}}); err != nil {
		return nil, fmt.Errorf("create %s: %w", lockTable, err)
	}
	deadline := time.Now().Add(d.lockWait)
	for {
		results, err := d.send(ctx, []statement{{sql: takeLease, params: []string{service, d.holder, d.leaseModifier()}}})
		if err != nil {
			return nil, err
		}
		if results[0].Meta.Changes > 0 {
			break
		}
		wait := time.Until(deadline)
		if wait <= 0 {
			return nil, d.leaseHeld(ctx, service)
		}
		timer := time.NewTimer(min(wait, d.lockPoll))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	d.mu.Lock()
	d.leased = service
	d.mu.Unlock()
	return func(ctx context.Context) error {
		d.mu.Lock()
		d.leased = ""
		d.mu.Unlock()
		_, err := d.send(ctx, []statement{{sql: releaseLease, params: []string{service, d.holder}}})
		return err
	}, nil
}

// leaseHeld is the error of a Lock that gave up: it names the holder.
func (d *Driver) leaseHeld(ctx context.Context, service string) error {
	held := fmt.Errorf("d1: another runner holds the lease of service %s; gave up after %s", service, d.lockWait)
	results, err := d.send(ctx, []statement{{sql: readLease, params: []string{service}}})
	if err != nil || len(results[0].Results.Rows) != 1 {
		return held
	}
	var holder, expires string
	if scanRow(results[0].Results.Rows[0], []any{&holder, &expires}) != nil {
		return held
	}
	return fmt.Errorf("d1: %s holds the lease of service %s until %s; gave up after %s", holder, service, expires, d.lockWait)
}

func (d *Driver) leaseModifier() string {
	return fmt.Sprintf("+%.3f seconds", d.lease.Seconds())
}

// Transact runs fn and sends the writes it made as one batch: PRAGMA
// defer_foreign_keys = ON, the lease's renewal, then the writes in order. A
// query in fn runs at once, so fn reads before it writes. A read-only
// transaction, or one that wrote nothing, sends nothing.
func (d *Driver) Transact(ctx context.Context, opts migrate.TxOptions, fn func(ctx context.Context, conn migrate.Conn) error) error {
	if opts.ForeignKeysOff {
		return errors.New("d1: a step cannot turn foreign keys off: D1 keeps enforcement on")
	}
	t := &tx{d: d, readOnly: opts.ReadOnly}
	if err := fn(ctx, t); err != nil {
		return err
	}
	if len(t.writes) == 0 {
		return nil
	}
	return d.commit(ctx, t.writes)
}

// commit sends a transaction's writes as one batch.
func (d *Driver) commit(ctx context.Context, writes []statement) error {
	batch := []statement{{sql: "PRAGMA defer_foreign_keys = ON"}}
	d.mu.Lock()
	service := d.leased
	d.mu.Unlock()
	if service != "" {
		batch = append(batch, statement{sql: renewLease, params: []string{service, d.holder, d.leaseModifier()}})
	}
	lead := len(batch)
	batch = append(batch, writes...)
	_, err := d.send(ctx, batch)
	if err == nil {
		return nil
	}
	var apiErr *Error
	if errors.As(err, &apiErr) {
		if service != "" && apiErr.mentions(lockTable+".holder") {
			return fmt.Errorf("d1: another runner took over the lease of service %s after it expired, so the batch did not run: %w", service, err)
		}
		if apiErr.Index >= lead {
			return &migrate.StatementError{Statement: writes[apiErr.Index-lead].sql, Err: err}
		}
	}
	return err
}

// Session fails: D1 runs every step as one batch.
func (d *Driver) Session(context.Context, func(ctx context.Context, conn migrate.Conn) error) error {
	return errors.New("d1: D1 has no BEGIN or COMMIT, so every step runs as one batch; a step outside a transaction cannot run")
}

// CheckStep refuses a step D1 cannot run: one outside a transaction, and one
// that turns foreign keys off.
func (d *Driver) CheckStep(step *migrate.Step) error {
	switch {
	case !step.Transactional:
		return fmt.Errorf("step %d (%s) is not transactional, and D1 cannot run it: D1 has no BEGIN or COMMIT, so every step runs as one batch", step.Index, step.Subject)
	case step.ForeignKeysOff:
		return fmt.Errorf("step %d (%s) turns foreign keys off, and D1 cannot run it: D1 keeps foreign key enforcement on. A plan written before D27's amendment rebuilds a table this way; apply it to a SQLite file, or plan it again", step.Index, step.Subject)
	}
	return nil
}

// IsLockTimeout reports a request D1 turned away before running it: HTTP
// 429, or an overloaded database. The runner retries the step. (A guess:
// Cloudflare documents neither as retryable.)
func (d *Driver) IsLockTimeout(err error) bool {
	var apiErr *Error
	return errors.As(err, &apiErr) && (apiErr.Status == http.StatusTooManyRequests || apiErr.mentions("overloaded"))
}

// tx is a transaction: it holds the writes until the commit.
type tx struct {
	d        *Driver
	readOnly bool
	writes   []statement
}

func (t *tx) Exec(_ context.Context, sql string, args ...any) error {
	if t.readOnly {
		return errors.New("d1: a write in a read-only transaction")
	}
	s, err := prepare(sql, args)
	if err != nil {
		return err
	}
	t.writes = append(t.writes, s)
	return nil
}

func (t *tx) Query(ctx context.Context, sql string, args []any, row func(scan func(dest ...any) error) error) error {
	if len(t.writes) > 0 {
		return errors.New("d1: a query after a write in one transaction cannot see the write, which is sent at the commit")
	}
	s, err := prepare(sql, args)
	if err != nil {
		return err
	}
	results, err := t.d.send(ctx, []statement{s})
	if err != nil {
		return err
	}
	for _, values := range results[0].Results.Rows {
		if err := row(func(dest ...any) error { return scanRow(values, dest) }); err != nil {
			return err
		}
	}
	return nil
}

// statement is one statement of a request.
type statement struct {
	sql    string
	params []string
}

// prepare turns a statement with the runner's $N placeholders and string
// and int64 arguments into one D1 takes: ?N and strings, the type the API
// documents for params. A column's affinity turns '3' into 3 as SQLite
// does. A statement without arguments is sent as written.
func prepare(sql string, args []any) (statement, error) {
	if len(args) == 0 {
		return statement{sql: sql}, nil
	}
	params := make([]string, len(args))
	for i, arg := range args {
		switch v := arg.(type) {
		case string:
			params[i] = v
		case int64:
			params[i] = strconv.FormatInt(v, 10)
		case int:
			params[i] = strconv.Itoa(v)
		default:
			return statement{}, fmt.Errorf("d1: argument %d is a %T; the driver sends strings and integers", i+1, arg)
		}
	}
	return statement{sql: placeholders(sql), params: params}, nil
}

// placeholders rewrites each $N outside quotes and comments as ?N.
func placeholders(sql string) string {
	var b strings.Builder
	b.Grow(len(sql))
	for i := 0; i < len(sql); {
		c := sql[i]
		switch {
		case c == '\'' || c == '"' || c == '`' || c == '[':
			end := c
			if c == '[' {
				end = ']'
			}
			j := i + 1
			for j < len(sql) && sql[j] != end {
				j++
			}
			j = min(j+1, len(sql))
			b.WriteString(sql[i:j])
			i = j
		case c == '-' && strings.HasPrefix(sql[i:], "--"):
			j := strings.IndexByte(sql[i:], '\n')
			if j < 0 {
				j = len(sql) - i
			}
			b.WriteString(sql[i : i+j])
			i += j
		case c == '/' && strings.HasPrefix(sql[i:], "/*"):
			j := strings.Index(sql[i+2:], "*/")
			if j < 0 {
				j = len(sql) - i
			} else {
				j += 4
			}
			b.WriteString(sql[i : i+j])
			i += j
		case c == '$' && i+1 < len(sql) && isDigit(sql[i+1]) && (i == 0 || !isWord(sql[i-1])):
			b.WriteByte('?')
			i++
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isWord(c byte) bool {
	return isDigit(c) || c == '_' || c == '$' || (c|0x20 >= 'a' && c|0x20 <= 'z') || c >= 0x80
}

// scanRow reads a row of JSON values into *string and *int64 destinations.
func scanRow(values []any, dest []any) error {
	if len(values) != len(dest) {
		return fmt.Errorf("d1: a row of %d columns scanned into %d destinations", len(values), len(dest))
	}
	for i, value := range values {
		if value == nil {
			return fmt.Errorf("d1: column %d is NULL", i+1)
		}
		switch p := dest[i].(type) {
		case *string:
			switch v := value.(type) {
			case string:
				*p = v
			case json.Number:
				*p = v.String()
			default:
				return fmt.Errorf("d1: column %d is a %T, not text", i+1, value)
			}
		case *int64:
			var err error
			switch v := value.(type) {
			case json.Number:
				*p, err = v.Int64()
			case string:
				*p, err = strconv.ParseInt(v, 10, 64)
			default:
				err = fmt.Errorf("a %T", value)
			}
			if err != nil {
				return fmt.Errorf("d1: column %d is not an integer: %w", i+1, err)
			}
		default:
			return fmt.Errorf("d1: cannot scan column %d into a %T", i+1, dest[i])
		}
	}
	return nil
}

// The request and response bodies.
type (
	queryBody struct {
		SQL    string   `json:"sql"`
		Params []string `json:"params,omitempty"`
	}
	batchBody struct {
		Batch []queryBody `json:"batch"`
	}
	response struct {
		Success bool       `json:"success"`
		Errors  []APIError `json:"errors"`
		Result  []result   `json:"result"`
	}
	result struct {
		Success bool `json:"success"`
		Results struct {
			Columns []string `json:"columns"`
			Rows    [][]any  `json:"rows"`
		} `json:"results"`
		Meta struct {
			Changes int64 `json:"changes"`
		} `json:"meta"`
	}
)

// APIError is one of the errors a response carries.
type APIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Error is a request the API failed or refused.
type Error struct {
	// Status is the HTTP status.
	Status int
	// Errors are the response's errors.
	Errors []APIError
	// Index is the statement of the request that failed, from 0, when the
	// response names it, and -1 otherwise.
	Index int
	// Body is the start of a response that was not the API's JSON.
	Body string
}

func (e *Error) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "d1: HTTP %d", e.Status)
	for i, apiErr := range e.Errors {
		sep := "; "
		if i == 0 {
			sep = ": "
		}
		fmt.Fprintf(&b, "%s%s (code %d)", sep, apiErr.Message, apiErr.Code)
	}
	if len(e.Errors) == 0 && e.Body != "" {
		fmt.Fprintf(&b, ": %s", e.Body)
	}
	return b.String()
}

func (e *Error) mentions(s string) bool {
	for _, apiErr := range e.Errors {
		if strings.Contains(strings.ToLower(apiErr.Message), strings.ToLower(s)) {
			return true
		}
	}
	return false
}

// send sends statements as one request: the plain form for one statement,
// the batch form for several. It returns one result per statement.
func (d *Driver) send(ctx context.Context, statements []statement) ([]result, error) {
	var body any
	if len(statements) == 1 {
		body = queryBody{SQL: statements[0].sql, Params: statements[0].params}
	} else {
		batch := batchBody{Batch: make([]queryBody, len(statements))}
		for i, s := range statements {
			batch.Batch[i] = queryBody{SQL: s.sql, Params: s.params}
		}
		body = batch
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("d1: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("d1: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+d.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("d1: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return nil, fmt.Errorf("d1: read the response: %w", err)
	}
	var r response
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&r); err != nil {
		start := string(data)
		if len(start) > 200 {
			start = start[:200]
		}
		return nil, &Error{Status: resp.StatusCode, Index: -1, Body: start}
	}
	if resp.StatusCode/100 != 2 || !r.Success {
		return nil, &Error{Status: resp.StatusCode, Errors: r.Errors, Index: failedIndex(r.Result)}
	}
	if len(r.Result) != len(statements) {
		return nil, fmt.Errorf("d1: %d results for %d statements", len(r.Result), len(statements))
	}
	if i := failedIndex(r.Result); i >= 0 {
		return nil, &Error{Status: resp.StatusCode, Errors: r.Errors, Index: i}
	}
	return r.Result, nil
}

// failedIndex returns the first result that did not succeed, or -1. A
// response that names the failed statement this way is a guess: the
// documented error carries only a code and a message.
func failedIndex(results []result) int {
	for i, r := range results {
		if !r.Success {
			return i
		}
	}
	return -1
}
