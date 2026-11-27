package tsgen

import (
	"testing"
)

// notificationsFixture is the DB service whose tickets raise events and
// which holds the notifier's tables and channel identities (D153).
const notificationsFixture = "fixture-notifications-db"

// notificationsCheck is the file tsc checks the fixture's package against:
// the notifier over its ORM and dispatch queue, and the definitions that
// carry each table's events and notifier part.
const notificationsCheck = `import { createNotifier, type Notifier } from '@superschematic/http-runtime/postgres';
import { createOrm, ticketTable, noticeTable } from './orm';
import { noticeWorkQueue } from './queues';

export function notifier(pool: Parameters<typeof createOrm>[0]): Notifier {
  return createNotifier({ orm: createOrm(pool), dispatch: noticeWorkQueue });
}
export const kinds: string[] = (ticketTable.events ?? []).map(e => e.kind);
export const part: 'inbox' | undefined = noticeTable.notifier === 'inbox' ? 'inbox' : undefined;
`

// notificationsProviders are the stub channel adapters of
// examples/runtime-providers (D159) the end-to-end test sends through,
// copied beside it.
var notificationsProviders = map[string]string{
	"sms-relay-a.ts": "../../../examples/runtime-providers/providers/sms-relay-a/src/index.ts",
	"mail-relay.ts":  "../../../examples/runtime-providers/providers/mail-relay/src/index.ts",
}

// notificationsCases are the cases notifications.test.ts runs: D153's
// "done when" in the generated repositories.
var notificationsCases = []string{
	"an urgent event reaches two recipients by SMS, skips one who cannot read the record, and escalates to email when not acknowledged in time",
	"a reply of ack acknowledges the notice and stops its escalation, and undo takes it back",
	"a reply of done 142 changes the record as its sender, and the audit names the channel and what read the text",
	"a low-trust channel is refused an operation its trust does not allow, and an address entered by hand only acknowledges",
	"a reply names its notice by reference unless exactly one waits on its sender",
	"an address is verified by the code sent to it and replied with, and an unknown sender reaches nothing",
	"a failed send falls through to the next channel, and an adapter that is down is skipped and alerted on the others",
	"quiet hours hold a normal notice, digests batch, and record sends nothing",
	"a delivery's statuses move it on and a cleared reference is recorded on its holder",
	"password reset sends a code through the email adapter and the code sets the password",
}

// notificationsSQLiteCheck is what tsc checks the package against on
// SQLite (D189): the notifier of the ./d1 entry over the ./orm/sqlite
// entry's repositories, its dispatch queue claimed over the SqlDatabase.
const notificationsSQLiteCheck = `import { createNotifier, type Notifier, type SqlDatabase, type WorkerQueue } from '@superschematic/http-runtime/d1';
import { createOrm } from './orm-sqlite';
import { noticeWorkQueue } from './queues';

export function notifier(db: SqlDatabase): [Notifier, WorkerQueue] {
  const notifier = createNotifier({ orm: createOrm(db), dispatch: noticeWorkQueue });
  return [notifier, notifier.workerQueue(db)];
}
`

// notificationsRun is the notifier's end-to-end test (D153) on either
// dialect.
var notificationsRun = featureRun{
	fixture:     notificationsFixture,
	test:        "notifications.test.ts",
	stubs:       notificationsProviders,
	check:       map[string]string{"notifications.check.ts": notificationsCheck},
	sqliteCheck: map[string]string{"notifications.check.ts": notificationsSQLiteCheck},
	cases:       notificationsCases,
	keep:        "NOTIFY_KEEP",
	secrets:     []string{"relay-token"},
}

// TestNotificationsEndToEnd generates fixture-notifications-db's
// TypeScript package, type-checks it against notificationsCheck, and runs
// testdata/orm/notifications.test.ts in it with bun over the generated
// create.sql in a schema of its own at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL, with the runtime-providers
// example's SMS and mail stubs as the channels: every case of
// notificationsCases must pass. It skips without the database.
func TestNotificationsEndToEnd(t *testing.T) {
	runFeature(t, notificationsRun, false)
}

// TestNotificationsEndToEndOnSQLite runs the same cases on SQLite (D189):
// the notifier over the ./orm/sqlite entry's repositories on openSqlite's
// connection to a file the database's SQLite create.sql made, its dispatch
// queue's delays, escalations and claims on the file. It needs no
// database server.
func TestNotificationsEndToEndOnSQLite(t *testing.T) {
	runFeature(t, notificationsRun, true)
}
