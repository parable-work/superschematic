package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	migrate "github.com/parable-work/superschematic/runtime/migrate/go"
	"github.com/parable-work/superschematic/runtime/migrate/go/internal/testdb"
)

// repoRoot is the checkout's root, from this package's directory.
var repoRoot = filepath.Join("..", "..", "..", "..", "..")

// queueDocument writes the golden queue document the compiler writes for
// fixture-queue-db's OrderPlaced (internal/queueops/testdata/golden) where
// the command reads it, and returns its path.
func queueDocument(t *testing.T, name string) string {
	t.Helper()
	doc, err := os.ReadFile(filepath.Join(repoRoot, "internal", "queueops", "testdata", "golden", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), name+".json")
	if err := os.WriteFile(path, doc, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// The messages the golden documents name.
const (
	deadID  = "00000000-0000-4000-8000-000000000001"
	dead2ID = "00000000-0000-4000-8000-000000000002"
	readyID = "00000000-0000-4000-8000-000000000003"
	noID    = "00000000-0000-4000-8000-0000000000ff"
)

// queueTable creates fixture-queue-db's tables, as sqlgen writes them, on
// a database of its own, with two dead messages and a ready one, and
// returns its URL. It skips without a server.
func queueTable(t *testing.T) string {
	t.Helper()
	dbURL := testdb.NewPostgres(t)
	createSQL, err := os.ReadFile(filepath.Join(repoRoot, "internal", "generator", "sqlgen", "testdata", "golden", "fixture-queue-db", "create.sql"))
	if err != nil {
		t.Fatal(err)
	}
	testdb.Exec(t, dbURL, string(createSQL), `INSERT INTO order_placed_queue
  (id, order_id, placed_at, priority, quantity, note, ship_to, tags, state, attempts, last_error, due_at, updated_at)
VALUES
  ('`+deadID+`', gen_random_uuid(), now(), 'high', 2, 'gift', '{"line1":"1 Main St","city":"Leeds"}', '{a,b}', 'dead', 4, 'the card was refused', now() - interval '3 hours', now() - interval '2 hours'),
  ('`+dead2ID+`', gen_random_uuid(), now(), 'low', 1, NULL, '{"line1":"2 Side St","city":"York"}', '{}', 'dead', 1, NULL, now() - interval '2 hours', now() - interval '1 hour'),
  ('`+readyID+`', gen_random_uuid(), now(), 'low', 1, NULL, '{"line1":"3 High St","city":"Hull"}', '{}', 'ready', 0, NULL, now() + interval '1 hour', now())`)
	return dbURL
}

// private are values of the dead messages' bodies and errors, which no
// output of a run with --result may hold.
var private = []string{"gift", "Main St", "Leeds", "York", "the card was refused"}

// noPrivate fails the test when out holds a value of a message.
func noPrivate(t *testing.T, what, out string) {
	t.Helper()
	for _, value := range private {
		if strings.Contains(out, value) {
			t.Errorf("%s holds %q:\n%s", what, value, out)
		}
	}
}

// TestQueueOnPostgres runs each golden queue document on fixture-queue-db's
// tables, as sqlgen writes them, on a database of its own: two dead
// messages and a ready one. It lists the dead, the oldest death first up to
// the limit, with the total, each message's attempts, last error, time of
// death and columns; refuses a retry that names a ready message and one the
// queue does not hold, and retries nothing; retries one, which is ready,
// due now, with no attempts and no claim, its last error kept; drops the
// other, then refuses to drop it again; and retries and drops every dead
// message, leaving the ready one as it was.
func TestQueueOnPostgres(t *testing.T) {
	dbURL := queueTable(t)
	row := func(id string) string {
		t.Helper()
		got := testdb.Strings(t, dbURL, `SELECT state || ' attempts=' || attempts || ' due=' || CASE WHEN due_at <= now() THEN 'now' ELSE 'later' END
  || ' claim=' || (claim_token IS NOT NULL OR claim_expires_at IS NOT NULL) || ' error=' || COALESCE(last_error, '')
  || ' changed=' || (updated_at > now() - interval '1 minute')
  FROM order_placed_queue WHERE id = $1::uuid`, id)
		if len(got) == 0 {
			return "gone"
		}
		return got[0]
	}
	queue := func(name string, code int, out ...string) invocation {
		t.Helper()
		return invoke(t, nil, "queue", "--queue", queueDocument(t, name), "--database-url", dbURL).expect(t, code, out...)
	}
	result := func(i invocation) migrate.QueueResult {
		t.Helper()
		var r migrate.QueueResult
		decoder := json.NewDecoder(strings.NewReader(i.stdout))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&r); err != nil {
			t.Fatalf("the result: %v\n%s", err, i.stdout)
		}
		return r
	}

	// The listing's limit is one: the older death, and the total.
	listed := result(queue("dead", exitOK))
	if listed.Operation != migrate.QueueDead || listed.Total != 2 || len(listed.Messages) != 1 {
		t.Fatalf("listed %+v", listed)
	}
	m := listed.Messages[0]
	if m.ID != deadID || m.Attempts != 4 || m.LastError != "the card was refused" || !regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$`).MatchString(m.DiedAt) {
		t.Fatalf("listed %+v", m)
	}
	var message map[string]any
	if err := json.Unmarshal(m.Message, &message); err != nil {
		t.Fatal(err)
	}
	keys := slices.Sorted(func(yield func(string) bool) {
		for k := range message {
			if !yield(k) {
				return
			}
		}
	})
	if want := []string{"note", "order_id", "placed_at", "priority", "quantity", "ship_to", "tags"}; !slices.Equal(keys, want) {
		t.Errorf("the message's columns are %q, want %q", keys, want)
	}
	if message["note"] != "gift" || message["quantity"] != float64(2) || message["priority"] != "high" {
		t.Errorf("the message is %s", m.Message)
	}

	// A retry that names a message that is not dead changes none.
	queue("retry-refused", exitFailed,
		"superschematic-migrate: queue OrderPlaced of fixture-queue-db: 2 of the 3 message(s) named are not dead, so none was retried: "+
			readyID+" is ready; "+noID+" is not in the queue")
	if got := row(deadID); !strings.HasPrefix(got, "dead attempts=4") {
		t.Fatalf("a refused retry changed %s: %s", deadID, got)
	}

	retried := result(queue("retry", exitOK))
	if !slices.Equal(retried.IDs, []string{deadID}) || retried.Messages != nil {
		t.Fatalf("retried %+v", retried)
	}
	if got, want := row(deadID), "ready attempts=0 due=now claim=false error=the card was refused changed=true"; got != want {
		t.Errorf("the retried message is %q, want %q", got, want)
	}

	dropped := result(queue("drop", exitOK))
	if !slices.Equal(dropped.IDs, []string{dead2ID}) {
		t.Fatalf("dropped %+v", dropped)
	}
	if got := row(dead2ID); got != "gone" {
		t.Errorf("the dropped message is %s", got)
	}
	queue("drop", exitFailed, "1 of the 1 message(s) named are not dead, so none was dropped: "+dead2ID+" is not in the queue")
	if got := result(queue("dead", exitOK)); got.Total != 0 || len(got.Messages) != 0 || !strings.Contains(queue("dead", exitOK).stdout, `"messages": []`) {
		t.Fatalf("listed %+v", got)
	}

	// Every dead message, and the ready one stays as it was.
	testdb.Exec(t, dbURL, `UPDATE order_placed_queue SET state = 'dead', attempts = 4 WHERE id = '`+deadID+`'`,
		`INSERT INTO order_placed_queue (id, order_id, placed_at, priority, quantity, ship_to, state, attempts) VALUES ('`+dead2ID+`', gen_random_uuid(), now(), 'low', 1, '{}', 'dead', 2)`)
	if got := result(queue("retry-all", exitOK)); !slices.Equal(got.IDs, []string{deadID, dead2ID}) {
		t.Fatalf("retried %+v", got)
	}
	for _, id := range []string{deadID, dead2ID} {
		if got := row(id); !strings.HasPrefix(got, "ready attempts=0 due=now claim=false") {
			t.Errorf("the retried message %s is %q", id, got)
		}
	}
	if got := result(queue("retry-all", exitOK)); len(got.IDs) != 0 || got.IDs == nil {
		t.Fatalf("retried %+v with no dead message", got)
	}
	testdb.Exec(t, dbURL, `UPDATE order_placed_queue SET state = 'dead' WHERE id IN ('`+deadID+`', '`+dead2ID+`')`)
	if got := result(queue("drop-all", exitOK)); !slices.Equal(got.IDs, []string{deadID, dead2ID}) {
		t.Fatalf("dropped %+v", got)
	}
	if got := row(readyID); !strings.HasPrefix(got, "ready attempts=0 due=later claim=false error= ") {
		t.Errorf("the ready message is %q", got)
	}
	if got := testdb.Strings(t, dbURL, "SELECT count(*) FROM order_placed_queue"); !slices.Equal(got, []string{"1"}) {
		t.Errorf("the queue holds %v messages, want the ready one", got)
	}
}

// TestQueueRefuses refuses a queue document on a database that is not
// Postgres, a command without one, or with a URL and a Cloud SQL instance,
// an instance without its user and database, and a document it cannot
// read.
func TestQueueRefuses(t *testing.T) {
	doc := queueDocument(t, "dead")
	invoke(t, nil, "queue", "--queue", doc, "--database-url", testdb.NewSQLite(t)).
		expect(t, exitFailed, "queue OrderPlaced of fixture-queue-db: a queue's dead messages are read, retried and dropped on Postgres alone, and the database URL selects sqlite")
	invoke(t, nil, "queue", "--database-url", "postgres://localhost/x").expect(t, exitUsage, "queue needs --queue")
	invoke(t, nil, "queue", "--queue", doc).expect(t, exitUsage, "no database: pass --database-url or set DATABASE_URL")
	invoke(t, nil, "queue", "--queue", doc, "--database-url", "postgres://localhost/x", "--cloud-sql", "acme:us-east1:db").
		expect(t, exitUsage, "queue takes --database-url or --cloud-sql, not both")
	invoke(t, nil, "queue", "--queue", doc, "--cloud-sql", "acme:us-east1:db", "--database", "shop").
		expect(t, exitUsage, "--cloud-sql needs --cloud-sql-user and --database")
	invoke(t, nil, "queue", "--queue", doc, "--cloud-sql", "acme", "--cloud-sql-user", "m@acme.iam", "--database", "shop").
		expect(t, exitUsage, `the Cloud SQL instance "acme" is not a connection name`)
	invoke(t, nil, "queue", "--queue", doc, "--database", "shop").
		expect(t, exitUsage, "--cloud-sql-user and --database name a database on the instance --cloud-sql names")
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte(`{"version": 2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	invoke(t, nil, "queue", "--queue", bad, "--database-url", "postgres://localhost/x").expect(t, exitFailed, "queue document version 2; this runner reads version 1")
}

// fakeResults is a resultWriter that keeps what it is given, or fails to
// reserve or to write.
type fakeResults struct {
	written     map[string][]byte
	reserved    []string
	abandoned   []string
	failReserve error
	failWrite   error
}

func (r *fakeResults) Reserve(_ context.Context, ref string) (resultReservation, error) {
	if r.failReserve != nil {
		return nil, r.failReserve
	}
	r.reserved = append(r.reserved, ref)
	return fakeReservation{r: r, ref: ref}, nil
}

type fakeReservation struct {
	r   *fakeResults
	ref string
}

func (f fakeReservation) Write(_ context.Context, data []byte) error {
	if f.r.failWrite != nil {
		return f.r.failWrite
	}
	f.r.written[f.ref] = data
	return nil
}

func (f fakeReservation) Abandon() { f.r.abandoned = append(f.r.abandoned, f.ref) }

// TestQueueWritesItsResult runs golden documents with --result, as the gcp
// target's queue job does (D68, amended): the place is reserved before the
// operation runs, the result goes there, all of it, and stdout says only
// where it went. A place the runner may not reserve fails the run before
// the queue changes; a write that fails after a retry says the retry was
// applied. Neither stdout nor stderr holds a message's body or error. A
// file as --result is created with mode 0600 before the operation runs,
// one that exists is refused, and a failed operation removes it.
func TestQueueWritesItsResult(t *testing.T) {
	dbURL := queueTable(t)
	state := func(id string) string {
		t.Helper()
		got := testdb.Strings(t, dbURL, "SELECT state FROM order_placed_queue WHERE id = $1::uuid", id)
		if len(got) == 0 {
			return "gone"
		}
		return got[0]
	}
	const ref = "gs://state/superschematic/queue/shop/Staging/0123456789abcdef0123456789abcdef.json"
	results := &fakeResults{written: map[string][]byte{}}
	var stdout, stderr bytes.Buffer
	o := options{stdout: &stdout, stderr: &stderr, getenv: func(string) string { return "" }, results: results}
	queue := func(name string, code int) string {
		t.Helper()
		stdout.Reset()
		stderr.Reset()
		args := []string{"queue", "--queue", queueDocument(t, name), "--database-url", dbURL, "--result", ref}
		if got := runWith(context.Background(), args, o); got != code {
			t.Fatalf("%s: exit %d, want %d: %s", name, got, code, stderr.String())
		}
		noPrivate(t, "the output of "+name, stdout.String()+stderr.String())
		return stdout.String() + stderr.String()
	}

	if got, want := queue("dead", exitOK), "queue OrderPlaced of fixture-queue-db: wrote the result of dead to "+ref+"\n"; got != want {
		t.Errorf("stdout %q, want %q", got, want)
	}
	var r migrate.QueueResult
	if err := json.Unmarshal(results.written[ref], &r); err != nil {
		t.Fatalf("the result: %v\n%s", err, results.written[ref])
	}
	if r.Total != 2 || len(r.Messages) != 1 || !strings.Contains(string(r.Messages[0].Message), "gift") || r.Messages[0].LastError != "the card was refused" {
		t.Errorf("the result is %s", results.written[ref])
	}

	// A place it may not reserve: the retry does not run.
	results.failReserve = errors.New(ref + ".reserved: 403 Forbidden: shop-migrator does not have storage.objects.create access")
	out := queue("retry", exitFailed)
	if want := "superschematic-migrate: queue OrderPlaced of fixture-queue-db: " + results.failReserve.Error() + "; the queue was not changed"; !strings.Contains(out, want) {
		t.Errorf("a refused reservation says %q, want %q", out, want)
	}
	if got := state(deadID); got != "dead" {
		t.Fatalf("a retry whose result had no place ran: %s is %s", deadID, got)
	}

	// A write that fails after the retry committed says so.
	results.failReserve = nil
	results.failWrite = errors.New("write the result to " + ref + ": 503 Service Unavailable")
	out = queue("retry", exitFailed)
	if want := "superschematic-migrate: queue OrderPlaced of fixture-queue-db: the retry was applied, and its result was not written: " + results.failWrite.Error() +
		"; superschematic stack queue dead lists the dead messages as they are now"; !strings.Contains(out, want) {
		t.Errorf("a failed write says %q, want %q", out, want)
	}
	if got := state(deadID); got != "ready" {
		t.Errorf("the retried message is %s", got)
	}

	// A refused operation abandons its place.
	results.failWrite = nil
	queue("retry-refused", exitFailed)
	if !slices.Equal(results.abandoned, []string{ref}) {
		t.Errorf("abandoned %q", results.abandoned)
	}

	file := filepath.Join(t.TempDir(), "result.json")
	doc := queueDocument(t, "dead")
	i := invoke(t, nil, "queue", "--queue", doc, "--database-url", dbURL, "--result", file).expect(t, exitOK, "wrote the result of dead to "+file)
	noPrivate(t, "the output", i.stdout+i.stderr)
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("the result's mode is %v", info.Mode().Perm())
	}
	invoke(t, nil, "queue", "--queue", doc, "--database-url", dbURL, "--result", file).
		expect(t, exitFailed, "reserve "+file+" for the result: open "+file+": file exists; the queue was not changed")
	refused := filepath.Join(t.TempDir(), "refused.json")
	invoke(t, nil, "queue", "--queue", queueDocument(t, "retry-refused"), "--database-url", dbURL, "--result", refused).expect(t, exitFailed, "none was retried")
	if _, err := os.Stat(refused); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a refused operation left its result's file: %v", err)
	}
}

// TestQueueOnCloudSQL runs a queue document as the gcp target's queue job
// does: the document read from a bucket, the result's object reserved by
// an empty marker beside it and then written, through Cloud Storage's API,
// here a fake one, only where no object is, and the database reached
// through the Cloud SQL connector, here a dialer of the local server. A
// second run to the same object fails at its reservation and leaves the
// result as it was.
func TestQueueOnCloudSQL(t *testing.T) {
	dbURL := queueTable(t)
	u, err := url.Parse(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	if password, ok := u.User.Password(); ok {
		t.Setenv("PGPASSWORD", password)
	}
	doc, err := os.ReadFile(queueDocument(t, "dead"))
	if err != nil {
		t.Fatal(err)
	}
	const (
		docObject    = "superschematic/migrations/shop/Staging/queue/0123.json"
		resultObject = "superschematic/queue/shop/Staging/0123456789abcdef0123456789abcdef.json"
	)
	storage := &fakeStorage{objects: map[string][]byte{"state/" + docObject: doc}}
	server := httptest.NewServer(storage)
	defer server.Close()
	var stdout, stderr bytes.Buffer
	dialer := &localDialer{addr: u.Host}
	o := options{
		stdout: &stdout, stderr: &stderr,
		getenv: func(key string) string {
			if key == storageEmulatorEnv {
				return server.URL
			}
			return ""
		},
		cloudSQL: func(_ context.Context, instance string) (sqlDialer, error) {
			dialer.instance = instance
			return dialer, nil
		},
	}
	args := []string{"queue", "--queue", "gs://state/" + docObject,
		"--cloud-sql", "acme-staging:us-east1:shop-db", "--cloud-sql-user", u.User.Username(), "--database", strings.TrimPrefix(u.Path, "/"),
		"--result", "gs://state/" + resultObject}
	if code := runWith(context.Background(), args, o); code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if dialer.instance != "acme-staging:us-east1:shop-db" {
		t.Errorf("dialed %q", dialer.instance)
	}
	noPrivate(t, "the output", stdout.String()+stderr.String())
	var r migrate.QueueResult
	if err := json.Unmarshal(storage.objects["state/"+resultObject], &r); err != nil {
		t.Fatalf("the result object: %v", err)
	}
	if r.Total != 2 || len(r.Messages) != 1 || r.Messages[0].ID != deadID {
		t.Errorf("the result is %s", storage.objects["state/"+resultObject])
	}
	want := []string{"/storage/v1/b/state/o/superschematic%2Fmigrations%2Fshop%2FStaging%2Fqueue%2F0123.json", "POST state/" + resultObject + ".reserved", "POST state/" + resultObject}
	if !slices.Equal(storage.asked, want) {
		t.Errorf("asked %q, want %q", storage.asked, want)
	}
	if marker, ok := storage.objects["state/"+resultObject+".reserved"]; !ok || len(marker) != 0 {
		t.Errorf("the marker is %q (%v)", marker, ok)
	}

	written := slices.Clone(storage.objects["state/"+resultObject])
	stdout.Reset()
	stderr.Reset()
	if code := runWith(context.Background(), args, o); code != exitFailed || !strings.Contains(stderr.String(), "reserve gs://state/"+resultObject+" for the result: gs://state/"+resultObject+".reserved: file already exists; the queue was not changed") {
		t.Errorf("a second run: exit %d, %s", code, stderr.String())
	}
	if !bytes.Equal(storage.objects["state/"+resultObject], written) {
		t.Error("a second run replaced the result")
	}
}

// TestQueueRetryOfAKeyedQueueOnPostgres runs the retries of fixture-queue-db's
// StockChecked, which keeps one ready message per SKU (D80), on its tables
// as sqlgen writes them: of two dead messages of one SKU a retry makes the
// one that died last ready, so a retry that names both changes none and
// says why; a retry of every dead message retries one per SKU, and none of
// a SKU a ready message holds.
func TestQueueRetryOfAKeyedQueueOnPostgres(t *testing.T) {
	dbURL := testdb.NewPostgres(t)
	createSQL, err := os.ReadFile(filepath.Join(repoRoot, "internal", "generator", "sqlgen", "testdata", "golden", "fixture-queue-db", "create.sql"))
	if err != nil {
		t.Fatal(err)
	}
	const (
		heldID    = "00000000-0000-4000-8000-000000000004"
		waitingID = "00000000-0000-4000-8000-000000000005"
		aloneID   = "00000000-0000-4000-8000-000000000006"
	)
	testdb.Exec(t, dbURL, string(createSQL), `INSERT INTO stock_checked_queue (id, sku, quantity, state, attempts, updated_at) VALUES
  ('`+deadID+`', 'sku-1', 1, 'dead', 4, now() - interval '2 hours'),
  ('`+dead2ID+`', 'sku-1', 2, 'dead', 4, now() - interval '1 hour'),
  ('`+heldID+`', 'sku-2', 3, 'dead', 4, now() - interval '1 hour'),
  ('`+waitingID+`', 'sku-2', 4, 'ready', 0, now()),
  ('`+aloneID+`', 'sku-3', 5, 'dead', 4, now() - interval '1 hour')`)
	states := func() string {
		t.Helper()
		return strings.Join(testdb.Strings(t, dbURL, "SELECT sku || ':' || quantity || ':' || state FROM stock_checked_queue ORDER BY sku, quantity"), " ")
	}
	const before = "sku-1:1:dead sku-1:2:dead sku-2:3:dead sku-2:4:ready sku-3:5:dead"
	if got := states(); got != before {
		t.Fatalf("the queue holds %s", got)
	}

	invoke(t, nil, "queue", "--queue", queueDocument(t, "retry-keyed"), "--database-url", dbURL).expect(t, exitFailed,
		"superschematic-migrate: queue StockChecked of fixture-queue-db: 1 of the 2 message(s) named are not dead, so none was retried: "+
			deadID+" is dead, and a ready message holds its key")
	if got := states(); got != before {
		t.Fatalf("a refused retry changed the queue: %s", got)
	}

	i := invoke(t, nil, "queue", "--queue", queueDocument(t, "retry-all-keyed"), "--database-url", dbURL).expect(t, exitOK)
	var r migrate.QueueResult
	if err := json.Unmarshal([]byte(i.stdout), &r); err != nil {
		t.Fatalf("the result: %v\n%s", err, i.stdout)
	}
	if !slices.Equal(r.IDs, []string{dead2ID, aloneID}) {
		t.Errorf("retried %q, want the last death of sku-1 and sku-3's", r.IDs)
	}
	if got, want := states(), "sku-1:1:dead sku-1:2:ready sku-2:3:dead sku-2:4:ready sku-3:5:ready"; got != want {
		t.Errorf("the queue holds %s, want %s", got, want)
	}
}

// TestQueueBackfillOnPostgres runs the golden backfill document of
// fixture-queue-db's OrderChanged (D88) on its tables as sqlgen writes
// them: of three orders, the first with two lines that name a product, the
// second with one, and the third with a line that names none, the backfill
// enqueues one ready message for each of the first two, keyed by order,
// says it enqueued two, and holds no message for the third. Run again, it
// makes each order's message fresh and adds none.
func TestQueueBackfillOnPostgres(t *testing.T) {
	dbURL := testdb.NewPostgres(t)
	createSQL, err := os.ReadFile(filepath.Join(repoRoot, "internal", "generator", "sqlgen", "testdata", "golden", "fixture-queue-db", "create.sql"))
	if err != nil {
		t.Fatal(err)
	}
	const (
		first   = "00000000-0000-4000-8000-0000000000a1"
		second  = "00000000-0000-4000-8000-0000000000a2"
		third   = "00000000-0000-4000-8000-0000000000a3"
		product = "00000000-0000-4000-8000-0000000000b1"
	)
	// The lines' trigger enqueues as they are written; the queue is then
	// emptied, as it is of rows written before the trigger existed.
	testdb.Exec(t, dbURL, string(createSQL),
		`INSERT INTO product (id, "name") VALUES ('`+product+`', 'widget')`,
		`INSERT INTO "order" (id, placed_at) VALUES ('`+first+`', now()), ('`+second+`', now()), ('`+third+`', now())`,
		`INSERT INTO order_line (order_id, product_id, quantity) VALUES
  ('`+first+`', '`+product+`', 1),
  ('`+first+`', '`+product+`', 2),
  ('`+second+`', '`+product+`', 3),
  ('`+third+`', NULL, 4)`,
		`DELETE FROM order_changed_queue`)
	queued := func() string {
		t.Helper()
		return strings.Join(testdb.Strings(t, dbURL, "SELECT order_id::text || ':' || state || ':' || priority FROM order_changed_queue ORDER BY order_id"), " ")
	}

	i := invoke(t, nil, "queue", "--queue", queueDocument(t, "backfill"), "--database-url", dbURL).expect(t, exitOK)
	var r migrate.QueueResult
	decoder := json.NewDecoder(strings.NewReader(i.stdout))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&r); err != nil {
		t.Fatalf("the result: %v\n%s", err, i.stdout)
	}
	if r.Operation != migrate.QueueBackfill || r.Total != 2 || strings.Contains(i.stdout, `"ids"`) {
		t.Fatalf("backfilled %+v:\n%s", r, i.stdout)
	}
	want := first + ":ready:low " + second + ":ready:low"
	if got := queued(); got != want {
		t.Fatalf("the queue holds %s, want %s", got, want)
	}

	i = invoke(t, nil, "queue", "--queue", queueDocument(t, "backfill"), "--database-url", dbURL).expect(t, exitOK)
	if !strings.Contains(i.stdout, `"total": 2`) {
		t.Fatalf("a second backfill answered:\n%s", i.stdout)
	}
	if got := queued(); got != want {
		t.Fatalf("a second backfill left %s, want %s", got, want)
	}
}
