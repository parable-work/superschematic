// fixture-audience-queue-db's pages, which inherit their folder's audience
// through a queue (D156, #734), end to end. TestAudienceQueueEndToEnd
// generates the package, type-checks it, applies the generated create.sql
// in a schema of its own and runs these cases through the generated
// repositories on Postgres. A folder's change of audience commits without
// touching its pages: it enqueues the folder on AudienceResolving, once
// however many changes it takes, and a page keeps the audience it had
// until the queue's worker runs resolveAudience for the message, which
// resolves every page a batch at a time. Right after, a page shows the
// folder's new audience.
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { resolveAudience, type AudienceResolving } from '@superschematic/http-runtime/postgres';
import { createOrm, type FixtureAudienceQueueDbOrm } from './orm';
import { alice, aliceHyphenated, bob, database, dsn, type TestDatabase } from './harness';

/** How many pages the folder holds: more than one batch of resolveAudience's. */
const PAGES = 1234;

describe.skipIf(dsn === '')('Postgres', () => {
  let db: TestDatabase;
  /** Outside any operation: it reads and writes every row, as a job's writes do. */
  let system: FixtureAudienceQueueDbOrm;
  let asAlice: FixtureAudienceQueueDbOrm;
  let asBob: FixtureAudienceQueueDbOrm;

  beforeAll(async () => {
    db = await database('ts_orm_audience_queue');
    system = createOrm(db.pool);
    asAlice = createOrm(db.pool, alice);
    asBob = createOrm(db.pool, bob);
  });

  afterAll(async () => {
    await db?.drop();
  });

  /** A folder of alice's, everyone's, holding PAGES pages that inherit its audience, written in one statement. */
  const folderOfPages = async (): Promise<{ folder: string; folderText: string }> => {
    const folder = (await asAlice.folder.createOne({ name: 'a large folder', owner: alice, audience: 'everyone' } as never)).id as string;
    const folderText = String((await db.pool.query('SELECT id::text AS id FROM folder ORDER BY created_at DESC LIMIT 1')).rows[0].id);
    await db.pool.query(
      `INSERT INTO page (folder_id, title, audience, created_at, created_by)
       SELECT $1::uuid, 'page ' || n, 'inherit', now(), $2::uuid FROM generate_series(1, ${PAGES}) AS n`,
      [folderText, aliceHyphenated],
    );
    return { folder, folderText };
  };
  const visible = async (as: FixtureAudienceQueueDbOrm, folder: string) => (await as.page.findMany({ folderId: { eq: folder } } as never, { limit: 1 })).total;
  const messages = async (folderText: string) =>
    (await db.pool.query('SELECT child_table, parent_key FROM audience_resolving_queue WHERE parent_key = $1', [folderText])).rows as { child_table: string; parent_key: string }[];
  /** What a worker of the queue does: claims each ready message, resolves it and deletes it. */
  const drain = async (): Promise<number> => {
    const claimed = (await db.pool.query("SELECT id, child_table, parent_key FROM audience_resolving_queue WHERE state = 'ready'")).rows;
    let resolved = 0;
    for (const row of claimed) {
      const message: AudienceResolving = { childTable: row.child_table, parentKey: row.parent_key };
      resolved += await resolveAudience(system, message, { batch: 100 });
      await db.pool.query('DELETE FROM audience_resolving_queue WHERE id = $1', [row.id]);
    }
    return resolved;
  };

  test("a folder's change of audience commits without its pages, enqueued once on the folder's queue", async () => {
    const { folder, folderText } = await folderOfPages();
    expect(await visible(asBob, folder)).toBe(PAGES);
    await asAlice.folder.updateOne(folder as never, { audience: 'private' } as never);
    await asAlice.folder.updateOne(folder as never, { audience: 'named', audienceNamed: [] } as never);
    await asAlice.folder.updateOne(folder as never, { audience: 'private' } as never);
    // The pages keep the audience they had until the worker runs.
    expect(await visible(asBob, folder)).toBe(PAGES);
    expect(await messages(folderText)).toEqual([{ child_table: 'Page', parent_key: folderText }]);
    await drain();
  });

  test('a page read right after the worker drains the queue shows the folder\'s new audience', async () => {
    const { folder, folderText } = await folderOfPages();
    await asAlice.folder.updateOne(folder as never, { audience: 'private' } as never);
    expect(await visible(asBob, folder)).toBe(PAGES);
    expect(await drain()).toBe(PAGES);
    expect(await messages(folderText)).toEqual([]);
    expect(await visible(asBob, folder)).toBe(0);
    expect(await visible(asAlice, folder)).toBe(PAGES);
    // And back: the folder made everyone's again shows its pages to bob once the worker has run.
    await asAlice.folder.updateOne(folder as never, { audience: 'everyone' } as never);
    expect(await visible(asBob, folder)).toBe(0);
    expect(await drain()).toBe(PAGES);
    expect(await visible(asBob, folder)).toBe(PAGES);
  });
});
