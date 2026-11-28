package tsgen

import "testing"

// commentsInboxCheck is what tsc checks fixture-comments-inbox-db's
// package against (#713): the notifier over its ORM and dispatch queue, a
// table's Comments' markRead, follow and view, and the queue of the events
// its Comments emits.
const commentsInboxCheck = `import { createNotifier, type Notifier } from '@superschematic/http-runtime/postgres';
import { createOrm } from './orm';
import { noticeWorkQueue, ticketEventQueue } from './queues';
import type * as T from './types';

export function notifier(pool: Parameters<typeof createOrm>[0]): Notifier {
  return createNotifier({ orm: createOrm(pool), dispatch: noticeWorkQueue });
}
export async function unread(pool: Parameters<typeof createOrm>[0], key: T.IdentityUUID): Promise<unknown> {
  const orm = createOrm(pool);
  await orm.ticket.comments.markRead(key, { upTo: 1 });
  await orm.ticket.comments.follow(key, { thread: 1, on: false });
  return (await orm.ticket.comments.view(key)).unread;
}
export const events: string = ticketEventQueue.name;
`

// commentsInboxCases are the cases comments_inbox.test.ts runs: a table's
// conversation in the notifier's inbox (#713, D153, D154) and a message's
// via and for (D157).
var commentsInboxCases = []string{
	"a mention reaches the mentioned principal's inbox, and a principal who cannot read the row is not notified",
	"a reply reaches the thread's followers, a follower it mentions once, and never its author",
	"markRead clears a reader's unread, and follow turns a thread's replies on and off",
	"a message posted for a sender over a channel records its via and its for",
}

// commentsInboxRun is the end-to-end test of a table's Comments through
// the notifier on Postgres.
var commentsInboxRun = featureRun{
	fixture: "fixture-comments-inbox-db",
	test:    "comments_inbox.test.ts",
	check:   map[string]string{"comments_inbox.check.ts": commentsInboxCheck},
	cases:   commentsInboxCases,
}

// TestCommentsInboxEndToEnd generates fixture-comments-inbox-db's
// TypeScript package, type-checks it against commentsInboxCheck, and runs
// testdata/orm/comments_inbox.test.ts in it with bun over the generated
// create.sql in a schema of its own at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL: a ticket's Comments emits its
// mentions and replies, the table's event queue hands each to the notifier,
// whose routes reach the subjects the events name who may read the ticket;
// markRead and follow run on the table, and the view answers the reader's
// unread; a message posted as an agent for a party over SMS records its for
// and its via. Every case of commentsInboxCases must pass. It skips
// without the database.
func TestCommentsInboxEndToEnd(t *testing.T) {
	runFeature(t, commentsInboxRun, false)
}
