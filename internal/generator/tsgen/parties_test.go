package tsgen

import (
	"testing"
)

// partiesFixture is the DB service whose contacts are parties, which holds
// the conversations, the run requests queue, the grants a party reaches
// jobs through, and the notifier's tables a user's reply runs through
// (D157).
const partiesFixture = "fixture-parties-db"

// partiesCheck is the file tsc checks the fixture's package against: the
// inbound path over its ORM, notifier and run requests queue, and the
// definitions that mark the party table and the conversations.
const partiesCheck = `import { createInbound, createNotifier, type Inbound, type RunRequest } from '@superschematic/http-runtime/postgres';
import { createOrm, contactTable, messageTable } from './orm';
import { noticeWorkQueue, runRequestQueue } from './queues';

export function inbound(pool: Parameters<typeof createOrm>[0]): Inbound {
  const orm = createOrm(pool);
  return createInbound({ orm, notifier: createNotifier({ orm, dispatch: noticeWorkQueue }), runs: runRequestQueue });
}
export const title: string | undefined = contactTable.party?.title;
export const part: 'messages' | undefined = messageTable.conversations === 'messages' ? 'messages' : undefined;
export const shape = (r: RunRequest): [string, string, string, string] => [r.trigger, r.party.table, r.message, r.grant];
`

// partiesCases are the cases parties.test.ts runs: D157's "done when" and
// the path's other turns, in the generated repositories.
var partiesCases = []string{
	"a text with a photo from a party with a grant becomes a row on the granted record, with the photo attached",
	"a text from an unknown number lands in triage",
	"the same text from a party whose grant has ended reaches nothing, until its identity is verified again",
	"email failing DMARC is treated as unknown, and an authenticated email threads to its party",
	"a party with no grant is on its timeline and in triage, and a user's text is the notifier's reply",
	"two parties sharing an address are told apart by the thread, and held in triage otherwise",
	"sending to a party takes the permission, keeps to the rate limit, waits for approval when asked, and stops after STOP",
}

// partiesSQLiteCheck is what tsc checks the package against on SQLite
// (D189): the inbound path of the ./d1 entry over the ./orm/sqlite entry's
// repositories, its run requests claimed over the SqlDatabase.
const partiesSQLiteCheck = `import { createInbound, createNotifier, type Inbound, type RunRequest, type SqlDatabase, type WorkerQueue } from '@superschematic/http-runtime/d1';
import { createOrm } from './orm-sqlite';
import { noticeWorkQueue, runRequestQueue } from './queues';

export function inbound(db: SqlDatabase, handle: (request: RunRequest) => Promise<void>): [Inbound, WorkerQueue] {
  const orm = createOrm(db);
  const inbound = createInbound({ orm, notifier: createNotifier({ orm, dispatch: noticeWorkQueue }), runs: runRequestQueue });
  return [inbound, inbound.runWorker(db, handle)];
}
`

// partiesRun is the inbound path's end-to-end test (D157) on either
// dialect.
var partiesRun = featureRun{
	fixture:     partiesFixture,
	test:        "parties.test.ts",
	stubs:       notificationsProviders,
	check:       map[string]string{"parties.check.ts": partiesCheck},
	sqliteCheck: map[string]string{"parties.check.ts": partiesSQLiteCheck},
	cases:       partiesCases,
	keep:        "PARTIES_KEEP",
	secrets:     []string{"relay-token"},
}

// TestPartiesEndToEnd generates fixture-parties-db's TypeScript package,
// type-checks it against partiesCheck, and runs
// testdata/orm/parties.test.ts in it with bun over the generated create.sql
// in a schema of its own at SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL, with
// the runtime-providers example's SMS and mail relays as the stub
// adapters: every case of partiesCases must pass. It skips without the
// database.
func TestPartiesEndToEnd(t *testing.T) {
	runFeature(t, partiesRun, false)
}

// TestPartiesEndToEndOnSQLite runs the same cases on SQLite (D189): the
// inbound path over the ./orm/sqlite entry's repositories on openSqlite's
// connection to a file the database's SQLite create.sql made, its run
// requests claimed on the file. It needs no database server.
func TestPartiesEndToEndOnSQLite(t *testing.T) {
	runFeature(t, partiesRun, true)
}
