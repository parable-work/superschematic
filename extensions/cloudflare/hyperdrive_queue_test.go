package cloudflare_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

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

	d1 := newMigrateFixture(t)
	resolved, err := stack.Resolve(d1.reg, stack.Input{Stack: d1Stack(), Services: d1Services(), Environment: "Staging"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = queues(t, d1.reg).RunQueue(context.Background(), registry.QueueRequest{
		Run: registry.Run{Environment: resolved}, Database: "reviews-db", Service: "reviews-db", Document: []byte(plaidDead),
	})
	if want := "cloudflare: database reviews-db of environment Staging is on cloudflare.d1, where no worker claims; stack queue reaches a queue on cloudflare.hyperdrive, a Postgres outside Cloudflare, alone"; err == nil || err.Error() != want {
		t.Errorf("a queue on D1 = %v", err)
	}
	if len(d1.calls) != 0 {
		t.Errorf("the runner ran %d times on D1", len(d1.calls))
	}
}
