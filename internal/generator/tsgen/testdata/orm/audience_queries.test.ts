// fixture-audience-queries-db's declared queries over @audience tables on
// Postgres (#735, D86, D156): each query marks where a table's audience
// goes, :audience(d), and the ORM fills the mark with the audience of the
// caller its reads keep to. Two users get each their own documents of one
// query, an anonymous caller the ones everyone may see, an agent run the
// ones it or whom it acts for may see and its access lets it read, and an
// ORM outside any operation, or the system actor, every one. A join holds
// both tables, a paged query numbers the audience's parameters after its
// page key's, and a query over a projection's view holds the view to its
// source's rows.
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { withCaller } from '@superschematic/http-runtime/hono';
import { createOrm, type FixtureAudienceQueriesDbOrm } from './orm';
import { alice, bob, database, dsn, recording, type TestDatabase } from './harness';

/** An agent run's id, base62, acting for alice. */
const agentRun = '5ZKg8RX1XQFqHDuG1p3jOk';

let db: TestDatabase;
let system: FixtureAudienceQueriesDbOrm;
let asAlice: FixtureAudienceQueriesDbOrm;
let asBob: FixtureAudienceQueriesDbOrm;
let statements: string[];
const folders: Record<string, string> = {};

const titles = (rows: readonly { title: string }[]) => rows.map(row => row.title);

beforeAll(async () => {
  db = await database('ts_orm_audience_queries');
  const recorded = recording(db.pool);
  statements = recorded.statements;
  system = createOrm(recorded.pool);
  asAlice = createOrm(recorded.pool, alice);
  asBob = createOrm(recorded.pool, bob);
  folders.shared = (await asAlice.folder.createOne({ name: 'shared', owner: alice, audience: 'everyone' } as never)).id as string;
  folders.alice = (await asAlice.folder.createOne({ name: "alice's", owner: alice, audience: 'private' } as never)).id as string;
  folders.bob = (await asBob.folder.createOne({ name: "bob's", owner: bob, audience: 'private' } as never)).id as string;
  await asAlice.document.createOne({ title: 'Alpha', folderId: folders.shared } as never);
  await asAlice.document.createOne({ title: 'Bravo', folderId: folders.alice } as never);
  await asBob.document.createOne({ title: 'Charlie', folderId: folders.bob } as never);
  await asBob.document.createOne({ title: 'Delta', audience: 'named', audienceNamed: [alice] } as never);
  await asAlice.document.createOne({ title: 'Echo', folderId: folders.shared, agentAccess: 'none' } as never);
});

afterAll(async () => {
  await db?.drop();
});

describe.skipIf(dsn === '')('declared queries over an audience', () => {
  test('two callers get each their own rows of one query, an anonymous one everyone\'s and the system every one', async () => {
    expect(titles(await asAlice.queries.visibleDocuments({ search: null }))).toEqual(['Alpha', 'Bravo', 'Delta', 'Echo']);
    expect(titles(await asBob.queries.visibleDocuments({ search: null }))).toEqual(['Alpha', 'Charlie', 'Delta', 'Echo']);
    expect(titles(await withCaller(null, () => system.queries.visibleDocuments({ search: null })))).toEqual(['Alpha', 'Echo']);
    expect(titles(await system.queries.visibleDocuments({ search: null }))).toEqual(['Alpha', 'Bravo', 'Charlie', 'Delta', 'Echo']);
    expect(titles(await asBob.asSystem().queries.visibleDocuments({ search: null }))).toEqual(['Alpha', 'Bravo', 'Charlie', 'Delta', 'Echo']);
    // An ORM opened with no user keeps to the caller of the operation it runs in.
    expect(titles(await withCaller({ subject: bob }, () => system.queries.visibleDocuments({ search: 'a' })))).toEqual(['Alpha', 'Charlie', 'Delta']);
    // The audience's parameter follows the query's own.
    const text = statements.findLast(s => s.includes('effective_audience'))!;
    expect(text).toContain('($1::text IS NULL OR d.title');
    expect(text).toContain('(d.effective_audience IS NULL OR d.effective_audience && ARRAY[$2]::uuid[])');
  });

  test('an agent run sees what it or whom it acts for may, and its access lets it read', async () => {
    const agent = system.as({ subject: agentRun, kind: 'agent', actingFor: { kind: 'user', subject: alice } } as never);
    expect(titles(await agent.queries.visibleDocuments({ search: null }))).toEqual(['Alpha', 'Bravo', 'Delta']);
  });

  test('a join holds each table it marks to the caller', async () => {
    const rows = await asAlice.queries.folderDocuments({ folder: folders.shared } as never);
    expect(rows.map(row => [row.title, row.folderName])).toEqual([['Alpha', 'shared'], ['Echo', 'shared']]);
    expect(await asAlice.queries.folderDocuments({ folder: folders.bob } as never)).toEqual([]);
    expect(titles(await asBob.queries.folderDocuments({ folder: folders.bob } as never))).toEqual(['Charlie']);
    expect(titles(await system.queries.folderDocuments({ folder: folders.alice } as never))).toEqual(['Bravo']);
  });

  test('a paged query numbers the audience after its page key, page by page', async () => {
    const first = await asBob.queries.documentPage({ search: null }, { limit: 2 });
    expect(titles(first.rows)).toEqual(['Alpha', 'Charlie']);
    const second = await asBob.queries.documentPage({ search: null }, { limit: 2, after: first.next });
    expect(titles(second.rows)).toEqual(['Delta', 'Echo']);
    expect(second.next).toBeNull();
    const text = statements.findLast(s => s.includes('effective_audience'))!;
    expect(text).toContain('(q.title, q.id) > ($2, $3)');
    expect(text).toContain('ARRAY[$4]::uuid[]');
    expect(titles((await system.queries.documentPage({ search: null }, { limit: 10 })).rows)).toEqual(['Alpha', 'Bravo', 'Charlie', 'Delta', 'Echo']);
  });

  test("a query over a projection's view keeps to its source's audience, in a transaction too", async () => {
    expect(titles(await asAlice.queries.listedDocuments({ search: null }))).toEqual(['Alpha', 'Bravo', 'Delta', 'Echo']);
    expect(titles(await asBob.transaction(tx => tx.queries.listedDocuments({ search: null })))).toEqual(['Alpha', 'Charlie', 'Delta', 'Echo']);
    expect(titles(await withCaller(null, () => system.queries.listedDocuments({ search: null })))).toEqual(['Alpha', 'Echo']);
  });
});
