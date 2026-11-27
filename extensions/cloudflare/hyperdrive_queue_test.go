package cloudflare_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"

	"github.com/parable-work/superschematic/extensions/cloudflare"
)

// The queue runner on a database on Hyperdrive (D96), over the origin
// fixture: a fake Cloudflare that holds finances-db's Hyperdrive config and
// a stand-in for superschematic-migrate.

// plaidDead is the queue document `stack queue dead` writes for
// finances-db's PlaidSyncRequested, and plaidDeadResult what the runner
// prints for it: one dead message, whose reason is personal.
const (
	plaidDead       = `{"version":1,"service":"finances-db","queue":"PlaidSyncRequested","operation":"dead","limit":100}`
	plaidDeadResult = `{"version": 1, "service": "finances-db", "queue": "PlaidSyncRequested", "operation": "dead", "total": 1, "messages": [
  {"id": "00000000-0000-4000-8000-0000000000b1", "attempts": 9, "lastError": "ITEM_LOGIN_REQUIRED", "diedAt": "2026-10-27T09:00:00Z", "message": {"plaid_item_id": "item-1", "reason": "the bank of a private person"}}]}
`
)

// queueRequest is a queue operation's request for finances-db of the
// ledger fixture's environment env, carrying document.
func (f *originFixture) queueRequest(t *testing.T, env, document string) registry.QueueRequest {
	t.Helper()
	resolved, err := stack.Resolve(f.reg, stack.Input{Stack: ledgerStack(), Services: ledgerServices(), Environment: env})
	if err != nil {
		t.Fatal(err)
	}
	return registry.QueueRequest{
		Run:      registry.Run{Environment: resolved},
		Database: "finances-db",
		Service:  "finances-db",
		Document: []byte(document),
		Log:      new(bytes.Buffer),
	}
}

// queues is the target's queue runner, which the cloudflare target has.
func queues(t *testing.T, reg *registry.Registry) registry.QueueRunner {
	t.Helper()
	target, _ := reg.Target(cloudflare.Target)
	if target.Queues == nil {
		t.Fatal("the cloudflare target has no queue runner")
	}
	return target.Queues
}

// TestQueueOperationOnTheOrigin: a queue operation on finances-db reads its
// Hyperdrive config through Cloudflare's API, finds it reaches the
// database, host, port and user of the connection string the shell holds,
// and runs superschematic-migrate queue on the document, the connection
// string in DATABASE_URL alone, and returns what the runner printed. The
// log says where it ran and holds neither the result, whose messages may
// be personal, nor the password, which no argument holds either, and the
// document's file is gone. A config that caches reads, which a deploy
// refuses without caching: true, is no reason to refuse it: the operation
// does not read through the config.
func TestQueueOperationOnTheOrigin(t *testing.T) {
	f := newOriginFixture(t)
	f.result = plaidDeadResult
	req := f.queueRequest(t, "Staging", plaidDead)
	out, err := queues(t, f.reg).RunQueue(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != plaidDeadResult {
		t.Errorf("RunQueue returned %s, want what the runner printed", out)
	}
	if len(f.runs) != 1 {
		t.Fatalf("the runner ran %d times, want once", len(f.runs))
	}
	run := f.runs[0]
	if len(run.args) != 4 || run.args[0] != runnerPath || run.args[1] != "queue" || run.args[2] != "--queue" {
		t.Errorf("the runner ran as %v", run.args)
	}
	if run.databaseURL != stagingOrigin || run.queue != plaidDead {
		t.Errorf("the runner got DATABASE_URL %q and queue document %s", run.databaseURL, run.queue)
	}
	log := req.Log.(*bytes.Buffer).String()
	if want := "  finances-db: the queue operation on database finances of db.example.com:5432 as finances, through Hyperdrive config " + stagingHyperdrive + ", with " + runnerPath + " on this machine\n"; log != want {
		t.Errorf("the log is %q, want %q", log, want)
	}
	for _, where := range append(slices.Clone(run.args), log) {
		if strings.Contains(where, "s3cret-password") || strings.Contains(where, "a private person") {
			t.Errorf("%q holds the origin's password or a message", where)
		}
	}
	if _, err := os.Stat(run.args[3]); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the queue document's file is left behind: %v", err)
	}

	f.runs = nil
	f.cloud.hyperdrive[stagingHyperdrive].Caching.Disabled = false
	if _, err := queues(t, f.reg).RunQueue(context.Background(), f.queueRequest(t, "Staging", plaidDead)); err != nil || len(f.runs) != 1 {
		t.Errorf("a queue operation through a config that caches reads = %v, %d runs", err, len(f.runs))
	}
}

// TestQueueOperationOnTheOriginRefusals: the runner refuses, before it runs
// anything, an origin the shell does not hold, naming the variable and the
// config its value must match, one that is no postgres:// connection
// string, a Hyperdrive config the account does not hold, and one that
// reaches another database, user, host or port than the variable. A
// runner's refusal comes back as its own line, with where it ran, and a
// queue on D1 is refused.
func TestQueueOperationOnTheOriginRefusals(t *testing.T) {
	mismatch := func(reached string) string {
		return "cloudflare: database finances-db's Hyperdrive config " + stagingHyperdrive + " reaches " + reached +
			", and HYPERDRIVE_ORIGIN_FINANCES_DB reaches database finances of db.example.com:5432 as finances: stack queue would read and change the queues of one database while its Workers drain another's; point the config or the variable at the same database, as the same user"
	}
	for name, tc := range map[string]struct {
		edit func(*originFixture)
		want string
	}{
		"no origin": {
			edit: func(f *originFixture) { delete(f.shell, cloudflare.OriginEnv("finances-db")) },
			want: "cloudflare: database finances-db is on cloudflare.hyperdrive, and stack queue runs from this machine on its origin: set HYPERDRIVE_ORIGIN_FINANCES_DB to the origin's connection string, postgres://<user>:<password>@<host>:<port>/<database>, with the host, port, database and user of its Hyperdrive config, " + stagingHyperdrive,
		},
		"no connection string": {
			edit: func(f *originFixture) { f.shell[cloudflare.OriginEnv("finances-db")] = "finances.example.com" },
			want: "cloudflare: HYPERDRIVE_ORIGIN_FINANCES_DB is no postgres:// connection string with a host",
		},
		"no config": {
			edit: func(f *originFixture) { delete(f.cloud.hyperdrive, stagingHyperdrive) },
			want: "cloudflare: database finances-db's settings name Hyperdrive config " + stagingHyperdrive + ", which account " + accountID + " does not hold; create it for database finances of db.example.com:5432 as finances, or correct the id",
		},
		"another database": {
			edit: func(f *originFixture) { f.cloud.hyperdrive[stagingHyperdrive].Origin.Database = "finances_old" },
			want: mismatch("database finances_old of db.example.com:5432 as finances"),
		},
		"another user": {
			edit: func(f *originFixture) { f.cloud.hyperdrive[stagingHyperdrive].Origin.User = "app" },
			want: mismatch("database finances of db.example.com:5432 as app"),
		},
		"another host": {
			edit: func(f *originFixture) { f.cloud.hyperdrive[stagingHyperdrive].Origin.Host = "replica.example.com" },
			want: mismatch("database finances of replica.example.com:5432 as finances"),
		},
		"another port": {
			edit: func(f *originFixture) { f.cloud.hyperdrive[stagingHyperdrive].Origin.Port = 6543 },
			want: mismatch("database finances of db.example.com:6543 as finances"),
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newOriginFixture(t)
			tc.edit(f)
			_, err := queues(t, f.reg).RunQueue(context.Background(), f.queueRequest(t, "Staging", plaidDead))
			if err == nil || err.Error() != tc.want {
				t.Fatalf("RunQueue = %v\nwant %s", err, tc.want)
			}
			if len(f.runs) != 0 {
				t.Errorf("the runner ran %d times", len(f.runs))
			}
		})
	}

	f := newOriginFixture(t)
	f.refusal = "queue PlaidSyncRequested of finances-db: 1 of the 1 message(s) named are not dead, so none was retried: 00000000-0000-4000-8000-0000000000b1 is ready"
	_, err := queues(t, f.reg).RunQueue(context.Background(), f.queueRequest(t, "Staging", plaidDead))
	if want := "cloudflare: the queue operation on finances-db failed on database finances of db.example.com:5432 as finances: " + f.refusal; err == nil || err.Error() != want {
		t.Errorf("a runner's refusal = %v\nwant %s", err, want)
	}
}

// reviewsDead is the queue document `stack queue dead` writes for
// reviews-db's queue on D1, of SQLite's statements, and reviewsDeadResult
// what the runner prints for it.
const (
	reviewsDead       = `{"version":1,"service":"reviews-db","queue":"ReviewFlagged","dialect":"sqlite","operation":"dead","limit":100}`
	reviewsDeadResult = `{"version": 1, "service": "reviews-db", "queue": "ReviewFlagged", "operation": "dead", "total": 1, "messages": [
  {"id": "00000000-0000-4000-8000-0000000000e1", "attempts": 5, "lastError": "the reviewer's address bounced", "diedAt": "2026-11-20T09:00:00Z", "message": {"review_id": "r-1"}}]}
`
)

// d1QueueRequest is a queue operation's request for reviews-db of the D1
// fixture's environment env, carrying document, whose outputs are the
// fixture's.
func (f *migrateFixture) d1QueueRequest(t *testing.T, env string, params map[string]string, document string) registry.QueueRequest {
	t.Helper()
	m := f.request(t, env, params, ir.MigrationExpand)
	return registry.QueueRequest{
		Run:      m.Run,
		Database: "reviews-db",
		Service:  "reviews-db",
		Document: []byte(document),
		Log:      new(bytes.Buffer),
		Outputs:  m.Outputs,
	}
}

// TestQueueOperationOnD1: a queue operation on reviews-db, a database on
// D1, runs superschematic-migrate queue on this machine on the run's D1
// database (D68): at its d1:// URL, the account's and the uuid the
// deploy's outputs hold, a member of a preview its own, with the shell's
// CLOUDFLARE_API_TOKEN, on the document, of SQLite's statements, which a
// file of its own held. It returns what the runner printed, which the log
// does not hold. A shell without a token, a run whose outputs hold no
// uuid, a caller that reads no outputs and a runner's refusal are refused,
// each naming what is missing.
func TestQueueOperationOnD1(t *testing.T) {
	f := newMigrateFixture(t)
	f.result = reviewsDeadResult
	req := f.d1QueueRequest(t, "Staging", nil, reviewsDead)
	out, err := queues(t, f.reg).RunQueue(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != reviewsDeadResult {
		t.Errorf("RunQueue returned %s, want what the runner printed", out)
	}
	if len(f.calls) != 1 {
		t.Fatalf("the runner ran %d times, want once", len(f.calls))
	}
	call := f.calls[0]
	if len(call.args) != 6 || call.args[0] != runnerPath || call.args[1] != "queue" || call.args[2] != "--queue" ||
		call.args[4] != "--database-url" || call.args[5] != cloudflare.D1URL(accountID, reviewsUUID) {
		t.Errorf("the runner ran as %v", call.args)
	}
	if call.token != "the-deployers-token" || call.queue != reviewsDead {
		t.Errorf("the runner got the token %q and the document %s", call.token, call.queue)
	}
	if _, err := os.Stat(call.args[3]); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the queue document's file is left behind: %v", err)
	}
	log := req.Log.(*bytes.Buffer).String()
	if want := "  reviews-db: the queue operation on D1 database reviews-stack-staging-reviews-db (" + reviewsUUID + "), with " + runnerPath + " on this machine\n"; log != want {
		t.Errorf("the log is %q, want %q", log, want)
	}
	if strings.Contains(log, "bounced") || strings.Contains(log, "the-deployers-token") {
		t.Errorf("the log holds a message or the token: %q", log)
	}

	// A member of a preview runs on its own database.
	f.calls = nil
	if _, err := queues(t, f.reg).RunQueue(context.Background(), f.d1QueueRequest(t, "Preview", map[string]string{"pr": "123"}, reviewsDead)); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || f.calls[0].args[5] != cloudflare.D1URL(accountID, reviewsPrUUID) {
		t.Errorf("a preview member's operation ran as %v", f.calls)
	}

	for name, tc := range map[string]struct {
		edit func(*migrateFixture, *registry.QueueRequest)
		want string
	}{
		"no token": {
			edit: func(f *migrateFixture, _ *registry.QueueRequest) { delete(f.shell, cloudflare.TokenEnv) },
			want: "cloudflare: set CLOUDFLARE_API_TOKEN to an API token of the account; stack bootstrap --github sets the CI's",
		},
		"no uuid": {
			edit: func(f *migrateFixture, _ *registry.QueueRequest) { delete(f.outputs, "Staging") },
			want: "cloudflare: the queue operation on reviews-db: the outputs of Staging hold no uuid of node " + d1Node + ", D1 database reviews-stack-staging-reviews-db; deploy it first",
		},
		"no outputs": {
			edit: func(_ *migrateFixture, req *registry.QueueRequest) { req.Outputs = nil },
			want: "cloudflare: the queue operation on reviews-db: the command reads the runner no outputs, where the D1 database's uuid is",
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newMigrateFixture(t)
			req := f.d1QueueRequest(t, "Staging", nil, reviewsDead)
			tc.edit(f, &req)
			if _, err := queues(t, f.reg).RunQueue(context.Background(), req); err == nil || err.Error() != tc.want {
				t.Fatalf("RunQueue = %v\nwant %s", err, tc.want)
			}
			if len(f.calls) != 0 {
				t.Errorf("the runner ran %d times", len(f.calls))
			}
		})
	}

	f = newMigrateFixture(t)
	f.fail, f.refusal = errors.New("exit status 1"), "queue ReviewFlagged of reviews-db: 1 of the 1 message(s) named are not dead, so none was dropped: 00000000-0000-4000-8000-0000000000e1 is ready"
	_, err = queues(t, f.reg).RunQueue(context.Background(), f.d1QueueRequest(t, "Staging", nil, reviewsDead))
	if want := "cloudflare: the queue operation on reviews-db failed on D1 database reviews-stack-staging-reviews-db (" + reviewsUUID + "): " + f.refusal; err == nil || err.Error() != want {
		t.Errorf("a runner's refusal = %v\nwant %s", err, want)
	}
}
