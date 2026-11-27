// fixture-audience-behaviors-db's Task end to end (D156, D103): a task its
// owner keeps private or shows to the principals it names, whose status is
// a Workflow and which holds a conversation. TestAudienceBehaviorsEndToEnd
// generates the package, type-checks it, applies the generated DDL and
// runs these cases: on bun:sqlite, the HTTP runtime's adapter over the
// layout orm.ts embeds reads a private row, whose audience ids SQLite
// holds as JSON text, as a list of UUIDs; on Postgres, the generated
// repository runs a Workflow transition, a Comments post and list, an
// update and a delete of a private row and of a named one, whose audience
// ids are a uuid[]. Each answers the principals the row is visible to, and
// is not found (NotFoundError, a 404 at a route) for anyone else, never an
// internal error and never a success.
import { Database } from 'bun:sqlite';
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { initSync } from '@superschematic/rules';
import { execute, readRow, runCreate, sqliteRulesStore, type RulesHost } from '@superschematic/http-runtime/d1';
import { openSqlite, type SqliteDatabase } from '@superschematic/http-runtime/sqlite';
import { NotFoundError } from '@superschematic/http-runtime/postgres';
import { createOrm, fixtureAudienceBehaviorsDbRules as layout, type FixtureAudienceBehaviorsDbOrm } from './orm';
import { alice, bob, database, dsn, type TestDatabase } from './harness';

const carol = '3cKSm7Pv2EM8Wl4z4XvMh1';

describe('bun:sqlite', () => {
  let dir: string;
  let db: SqliteDatabase;
  let host: RulesHost;

  beforeAll(() => {
    dir = mkdtempSync(join(tmpdir(), 'audience-behaviors-db-'));
    const file = join(dir, 'tasks.db');
    const ddl = new Database(file, { create: true });
    ddl.exec(readFileSync(new URL('./create.sqlite.sql', import.meta.url), 'utf8'));
    ddl.close();
    db = openSqlite(file);
    host = { core: initSync(), layout, store: sqliteRulesStore(db) };
  });

  afterAll(() => {
    db?.close();
    if (dir) rmSync(dir, { recursive: true, force: true });
  });

  test("the core reads a private row's audience ids as a list of UUIDs, on bun:sqlite", async () => {
    expect(layout.types.Task.own.filter(column => column.list).map(column => column.field)).toEqual(['audienceNamed', 'effectiveAudience']);
    const caller = { subject: alice, permissions: [] };
    const key = await runCreate(host, 'Task', { title: 'Renew passport', owner: alice, audience: 'private' }, caller);
    const row = await readRow(host, 'Task', key);
    expect(row!.own.effectiveAudience).toEqual([alice]);
    const action = { kind: 'operation' as const, key, useId: 'status', operation: 'transition', params: { to: 'done' } };
    expect((await execute(host, 'Task', { caller, now: Date.now(), action })).result).toEqual({ from: 'open', to: 'done' });
    // A named row's ids, its own and those it names, read alike.
    const named = await runCreate(host, 'Task', { title: 'Book flights', owner: alice, audience: 'named', audienceNamed: [bob] }, caller);
    const read = await readRow(host, 'Task', named);
    expect(read!.own.audienceNamed).toEqual([bob]);
    expect([...(read!.own.effectiveAudience as string[])].sort()).toEqual([alice, bob].sort());
  });
});

describe.skipIf(dsn === '')('Postgres', () => {
  let db: TestDatabase;
  let orm: FixtureAudienceBehaviorsDbOrm;

  beforeAll(async () => {
    db = await database('ts_orm_audience_behaviors');
    orm = createOrm(db.pool, alice);
  });

  afterAll(async () => {
    await db?.drop();
  });

  /** Every behavior call, and an update and a delete, of key as orm's user is not found. */
  async function notFound(as: FixtureAudienceBehaviorsDbOrm, key: string): Promise<void> {
    await expect(as.task.status.transition(key as never, { to: 'done' } as never)).rejects.toBeInstanceOf(NotFoundError);
    await expect(as.task.comments.post(key as never, { body: 'Can I see this?' } as never)).rejects.toBeInstanceOf(NotFoundError);
    await expect(as.task.comments.listMessages(key as never, {} as never)).rejects.toBeInstanceOf(NotFoundError);
    await expect(as.task.updateOne(key as never, { title: 'Mine now' } as never)).rejects.toBeInstanceOf(NotFoundError);
    await expect(as.task.deleteOne(key as never)).rejects.toBeInstanceOf(NotFoundError);
  }

  /** A transition, a post and a list of key as orm's user, which the row answers. */
  async function answers(as: FixtureAudienceBehaviorsDbOrm, key: string, author: string): Promise<void> {
    expect(await as.task.status.transition(key as never, { to: 'done' } as never)).toEqual({ from: 'open', to: 'done' });
    const posted = (await as.task.comments.post(key as never, { body: 'Done on Tuesday.' } as never)) as any;
    expect([posted.id, posted.author]).toEqual([1, author]);
    const page = (await as.task.comments.listMessages(key as never, {} as never)) as any;
    expect(page.items.map((item: any) => [item.id, item.body])).toEqual([[1, 'Done on Tuesday.']]);
  }

  test("a private row's behaviors answer its owner, and are not found for anyone else, on Postgres", async () => {
    const key = (await orm.task.createOne({ title: 'Renew passport', owner: alice, audience: 'private' } as never)).id as string;
    expect((await orm.task.getOne(key as never)).effectiveAudience).toEqual([alice]);
    await notFound(createOrm(db.pool, carol), key);
    await answers(orm, key, alice);
    // Its owner updates it, the audience among what it sets, and the row
    // is everyone's; private again, it is hers alone.
    expect((await orm.task.updateOne(key as never, { audience: 'everyone' } as never)).effectiveAudience ?? null).toBeNull();
    expect((await createOrm(db.pool, carol).task.getOne(key as never)).title).toBe('Renew passport');
    expect((await orm.task.updateOne(key as never, { audience: 'private' } as never)).effectiveAudience).toEqual([alice]);
    await notFound(createOrm(db.pool, carol), key);
    await orm.task.deleteOne(key as never);
    expect(await orm.task.findOne({ id: { eq: key } } as never)).toBeNull();
  });

  test("a named row's behaviors answer the principals it names, and are not found for anyone else, on Postgres", async () => {
    const key = (await orm.task.createOne({ title: 'Book flights', owner: alice, audience: 'named', audienceNamed: [bob] } as never)).id as string;
    const asBob = createOrm(db.pool, bob);
    expect((await asBob.task.getOne(key as never)).audienceNamed).toEqual([bob]);
    await notFound(createOrm(db.pool, carol), key);
    await answers(asBob, key, bob);
    expect(((await orm.task.comments.listMessages(key as never, {} as never)) as any).items).toHaveLength(1);
    await asBob.task.deleteOne(key as never);
    expect(await orm.task.findOne({ id: { eq: key } } as never)).toBeNull();
  });
});
