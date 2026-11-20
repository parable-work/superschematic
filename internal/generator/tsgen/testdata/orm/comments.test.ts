// fixture-comments-db's tables end to end (D77, D103's step 4): a ticket's
// conversation, which a caller holding tickets.moderate moderates.
// TestCommentsTablesEndToEnd generates the package, type-checks it, applies
// the generated DDL and runs these cases on each dialect: on bun:sqlite, the
// SQLite create.sql D27 plans and the HTTP runtime's adapter over the
// layout orm.ts embeds (fixtureCommentsDbRules); on Postgres, the generated
// create.sql and the generated repository itself (createOrm). So the
// generator's layout, its DDL, the use's sub-tables and shared table and
// the adapter are held to one another: a post is a row of the use's
// sub-table messages and moves no version of the ticket, its conversation's
// row of the shared table the one write held to a version; a reply and a
// reaction show on the thread's record; and another's edit is refused
// unless its caller moderates, whose edit keeps what it replaced.
import { Database } from 'bun:sqlite';
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { initSync } from '@superschematic/rules';
import { execute, readRow, runCreate, sqliteRulesStore, type CoreCaller, type RulesHost } from '@superschematic/http-runtime/d1';
import { openSqlite, type SqliteDatabase } from '@superschematic/http-runtime/sqlite';
import { createOrm, fixtureCommentsDbRules as layout } from './orm';
import { alice, database, dsn } from './harness';

const bob = 'bob';
const moderator: CoreCaller = { subject: 'mo', permissions: ['tickets.moderate'] };

/** What a case runs through: on Postgres the generated repository, on bun:sqlite the adapter over the generated layout. */
interface Tables {
  readonly dialect: string;
  /** Creates a ticket through the core and resolves to its key. */
  ticket(title: string): Promise<string>;
  /** Runs one of the ticket's Comments operations as alice, as bob, or as the moderator. */
  invoke(key: string, operation: string, params: Record<string, unknown>, as?: 'bob' | 'moderator'): Promise<any>;
  /** The ticket's version as the store holds it. */
  version(key: string): Promise<number>;
  /** The rows of the ticket's use's sub-table messages, and of its conversation's shared row. */
  stored(key: string): Promise<{ messages: number; conversation: Record<string, unknown> | undefined }>;
  close(): Promise<void>;
}

/** The tables on bun:sqlite: the generated SQLite DDL in a file of the test's own, and the adapter over the generated layout. */
async function sqliteTables(): Promise<Tables> {
  const dir = mkdtempSync(join(tmpdir(), 'comments-db-'));
  const file = join(dir, 'comments.db');
  const ddl = new Database(file, { create: true });
  ddl.exec(readFileSync(new URL('./create.sqlite.sql', import.meta.url), 'utf8'));
  ddl.close();
  const db: SqliteDatabase = openSqlite(file);
  const host: RulesHost = { core: initSync(), layout, store: sqliteRulesStore(db) };
  const callers: Record<string, CoreCaller> = {
    alice: { subject: alice, permissions: [] },
    bob: { subject: bob, permissions: [] },
    moderator,
  };
  const raw = new Database(file);
  return {
    dialect: 'bun:sqlite',
    ticket: title => runCreate(host, 'Ticket', { title }, callers.alice!),
    async invoke(key, operation, params, as) {
      const action = { kind: 'operation' as const, key, useId: 'Comments', operation, params };
      return (await execute(host, 'Ticket', { caller: callers[as ?? 'alice']!, now: Date.now(), action })).result;
    },
    async version(key) {
      return (await readRow(host, 'Ticket', key))!.version;
    },
    async stored(key) {
      const messages = raw.query('SELECT COUNT(*) AS n FROM ticket_comments_messages').get() as { n: number };
      const conversation = raw.query('SELECT * FROM ticket_comments_conversations WHERE conversation = ?').get(key) as Record<string, unknown> | undefined;
      return { messages: messages.n, conversation: conversation ?? undefined };
    },
    async close() {
      raw.close();
      db.close();
      rmSync(dir, { recursive: true, force: true });
    },
  };
}

/** The tables on Postgres: the generated create.sql in a schema of the test's own, and the generated repository, acting as alice. */
async function postgresTables(): Promise<Tables> {
  const db = await database('ts_orm_comments');
  const orm = createOrm(db.pool, alice);
  const as = { bob: orm.withBehaviorCaller({ subject: bob, permissions: [] }), moderator: orm.withBehaviorCaller(moderator) };
  return {
    dialect: 'Postgres',
    async ticket(title) {
      return (await orm.ticket.createOne({ title } as never)).id;
    },
    invoke: (key, operation, params, caller) => ((caller ? as[caller] : orm).ticket.comments as any)[operation](key, params),
    async version(key) {
      return ((await orm.ticket.findOne({ id: { eq: key } } as never)) as any)._version;
    },
    async stored(key) {
      const messages = await db.pool.query('SELECT COUNT(*)::int AS n FROM ticket_comments_messages');
      const conversation = await db.pool.query('SELECT * FROM ticket_comments_conversations WHERE conversation = $1', [key]);
      return { messages: messages.rows[0].n, conversation: conversation.rows[0] };
    },
    close: () => db.drop(),
  };
}

const dialects: { readonly name: string; readonly skip: boolean; readonly open: () => Promise<Tables> }[] = [
  { name: 'bun:sqlite', skip: false, open: sqliteTables },
  { name: 'Postgres', skip: dsn === '', open: postgresTables },
];

for (const dialect of dialects) {
  describe.skipIf(dialect.skip)(dialect.name, () => {
    let t: Tables;

    beforeAll(async () => {
      t = await dialect.open();
    });

    afterAll(async () => {
      await t?.close();
    });

    test(`a post is a row of the use's sub-table and moves no version of the row, on ${dialect.name}`, async () => {
      const key = await t.ticket('Checkout fails');
      const version = await t.version(key);
      const posted = await t.invoke(key, 'post', { body: 'Seen on Safari.', mentions: [bob] });
      expect([posted.id, posted.author, posted.state, posted.stand, posted.mentions]).toEqual([1, alice, 'posted', 1, [bob]]);
      expect(await t.version(key)).toBe(version);
      const stored = await t.stored(key);
      expect(stored.messages).toBe(1);
      expect([Number(stored.conversation?.last_message), Number(stored.conversation?._version)]).toEqual([1, 1]);
      // The next write of the conversation moves its shared row, and still
      // not the ticket.
      await t.invoke(key, 'pin', { message: 1 });
      expect(Number((await t.stored(key)).conversation?._version)).toBe(2);
      expect(await t.version(key)).toBe(version);
    });

    test(`a reply and a reaction show on the thread's record, on ${dialect.name}`, async () => {
      const key = await t.ticket('Slow search');
      await t.invoke(key, 'post', { body: 'Since Tuesday.' });
      const reply = await t.invoke(key, 'post', { body: 'Looking.', thread: 1, quotes: 1 }, 'bob');
      expect([reply.id, reply.thread, reply.quotes, reply.author]).toEqual([2, 1, 1, bob]);
      const reacted = await t.invoke(key, 'react', { message: 1, emoji: 'eyes' }, 'bob');
      expect(reacted.reactions).toEqual({ eyes: { count: 1, mine: true } });
      const top = await t.invoke(key, 'getMessage', { message: 1 });
      expect([top.replies, top.participants, top.reactions]).toEqual([1, [bob, alice], { eyes: { count: 1, mine: false } }]);
      const page = await t.invoke(key, 'listMessages', { thread: 1 });
      expect(page.items.map((item: any) => item.id)).toEqual([2]);
      expect(page.root.id).toBe(1);
    });

    test(`another's edit is refused unless its caller moderates, whose edit keeps what it replaced, on ${dialect.name}`, async () => {
      const key = await t.ticket('Typo');
      await t.invoke(key, 'post', { body: 'Teh button.' });
      await expect(t.invoke(key, 'edit', { message: 1, body: 'The button.' }, 'bob')).rejects.toThrow(/bob is not the author of message 1/);
      const edited = await t.invoke(key, 'edit', { message: 1, body: 'The button.' }, 'moderator');
      expect([edited.body, edited.edits]).toEqual(['The button.', 1]);
      const edits = await t.invoke(key, 'listEdits', { message: 1 });
      expect(edits.items.map((item: any) => [item.edit, item.body, item.by])).toEqual([[1, 'Teh button.', 'mo']]);
    });
  });
}
