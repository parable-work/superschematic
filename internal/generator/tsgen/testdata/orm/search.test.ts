// fixture-search-db end to end (D155): six @searchable tables share one
// search index. TestSearchTablesEndToEnd generates the package,
// type-checks it, applies the generated create.sql in a schema of its own
// on Postgres and runs these cases through the generated repositories and
// the runtime's search. drain() runs what the queue's worker runs: each
// ready message of the SearchIndexing queue, handed to indexSearch.
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import {
  callSearchTool,
  grantAuthenticator,
  indexSearch,
  issueGrant,
  processFiles,
  reindexSearch,
  revokeGrant,
  searchRoutes,
  searchTables,
  type SearchEmbeddings,
  type SearchOptions,
} from '@superschematic/http-runtime/postgres';
import { ObjectNotFoundError, type Bucket, type Principal } from '@superschematic/http-runtime';
import { defineProvider, providersFrom, type FileProcessor } from '@superschematic/http-runtime/providers';
import { createOrm, type FixtureSearchDbOrm } from './orm';
import { paritySearches, parityAnswer, writeParityCorpus } from './search_parity';
import { alice, bob, database, dsn, type TestDatabase } from './harness';

const party = '3cKSm7Pv2EM8Wl4z4XvMh1';
const user = (subject: string): Principal => ({ subject, permissions: [] });

/** The fields each table's index row's title and body are of. */
const indexed: Record<string, { readonly member: string; readonly fields: readonly string[] }> = {
  Folder: { member: 'folder', fields: ['name'] },
  Document: { member: 'document', fields: ['title', 'text'] },
  Contract: { member: 'contract', fields: ['name', 'summary'] },
  Unit: { member: 'unit', fields: ['name', 'notes'] },
  Request: { member: 'request', fields: ['title', 'details', 'resolution'] },
  Note: { member: 'note', fields: ['title', 'text'] },
};

const wordsOf = (text: unknown) => String(text ?? '').toLowerCase().split(/[^\p{L}\p{N}]+/u);

describe.skipIf(dsn === '')('search across tables on Postgres', () => {
  let db: TestDatabase;
  let orm: FixtureSearchDbOrm;
  const ids: Record<string, string> = {};

  /** What the queue's worker runs: every ready message, indexed. */
  const drain = async (options?: SearchOptions): Promise<number> => {
    let n = 0;
    for (;;) {
      const { rows } = await db.pool.query(
        `DELETE FROM search_indexing_queue WHERE id IN (SELECT id FROM search_indexing_queue WHERE state = 'ready' ORDER BY created_at LIMIT 100) RETURNING source_table, source_key`
      );
      if (rows.length === 0) return n;
      for (const row of rows) await indexSearch(orm, { sourceTable: row.source_table, sourceKey: row.source_key }, options);
      n += rows.length;
    }
  };

  /** The rows of every @searchable table principal's list reads show whose title or body holds word, as table:key. */
  const listed = async (principal: Principal, word: string): Promise<string[]> => {
    const as = orm.as(principal) as unknown as Record<string, { findMany(filter: object, options?: object): Promise<{ rows: Record<string, unknown>[] }> }>;
    const out: string[] = [];
    for (const [table, { member, fields }] of Object.entries(indexed)) {
      const { rows } = await as[member]!.findMany({}, { limit: 1000 });
      for (const row of rows) if (fields.some(field => wordsOf(row[field]).includes(word))) out.push(`${table}:${String(row.id)}`);
    }
    return out.sort();
  };

  const found = async (principal: Principal | null, q: string, options?: SearchOptions, extra: object = {}) => {
    const result = await searchTables(orm, principal, { q, limit: 100, ...extra }, options);
    return { result, keys: result.items.map(hit => `${hit.table}:${hit.key}`) };
  };

  beforeAll(async () => {
    db = await database('ts_orm_search');
    // The contract's terms are sealed under this key.
    orm = createOrm(db.pool, { user: alice, fieldKeys: Buffer.alloc(32, 7).toString('base64') });
    const system = orm.asSystem();
    const asAlice = orm.as(user(alice));
    // Two folders: alice's own, and one everyone sees; a document in each.
    ids.privateFolder = (await asAlice.folder.createOne({ name: 'Walnut private', owner: alice, audience: 'private', createdAt: new Date() } as never)).id as string;
    ids.sharedFolder = (await asAlice.folder.createOne({ name: 'Walnut shared', owner: alice, createdAt: new Date() } as never)).id as string;
    ids.privateDoc = (await asAlice.document.createOne({ folderId: ids.privateFolder, title: 'Walnut plan', text: 'The plan in brief.', createdAt: new Date() } as never)).id as string;
    ids.sharedDoc = (await asAlice.document.createOne({ folderId: ids.sharedFolder, title: 'Memo', text: 'A walnut desk for the hall.', createdAt: new Date() } as never)).id as string;
    // Two contracts, the first with a unit and a request about it.
    ids.contractA = (await system.contract.createOne({ name: 'Walnut supply', summary: 'Timber for the year.', terms: 'Pay in obsidian.' } as never)).id as string;
    ids.contractB = (await system.contract.createOne({ name: 'Bolts', summary: 'Bolts and a walnut trim.' } as never)).id as string;
    ids.unitA = (await system.unit.createOne({ name: 'Walnut unit', contractId: ids.contractA } as never)).id as string;
    ids.unitB = (await system.unit.createOne({ name: 'Annex', notes: 'Walnut floors.', contractId: ids.contractB } as never)).id as string;
    ids.requestA = (await system.request.createOne({ unitId: ids.unitA, title: 'Walnut leak', details: 'Water under the sink.' } as never)).id as string;
    ids.requestB = (await system.request.createOne({ unitId: ids.unitB, title: 'Hinge', details: 'A walnut door sticks.' } as never)).id as string;
    // Notes in two projects, one deleted.
    ids.noteP1 = (await system.note.createOne({ project: 'p1', title: 'Walnut idea', text: 'Oil it twice.' } as never)).id as string;
    ids.noteP2 = (await system.note.createOne({ project: 'p2', title: 'Pine idea', text: 'A walnut stain on pine.' } as never)).id as string;
    ids.noteGone = (await system.note.createOne({ project: 'p1', title: 'Walnut draft', text: 'Gone soon.' } as never)).id as string;
    await system.note.deleteOne(ids.noteGone);
    // A role a party's grant names: the units and requests of its contract.
    await system.role.createOne({ name: 'contract-party', permissions: [], reaches: ['Unit:contract', 'Request:unit.contract'] } as never);
    expect(await drain()).toBeGreaterThan(0);
  });

  afterAll(async () => {
    await db?.drop();
  });

  test('two principals with different access search the same text and each gets exactly the rows their list reads would show, ranked across at least five tables', async () => {
    for (const subject of [alice, bob]) {
      const principal = user(subject);
      const { result, keys } = await found(principal, 'walnut');
      expect([...keys].sort()).toEqual(await listed(principal, 'walnut'));
      expect(result.total).toBe(keys.length);
      expect(new Set(result.items.map(hit => hit.table)).size).toBeGreaterThanOrEqual(5);
      // A page of three at a time reads the same rows, in the same order,
      // with the same total on every page: no gap where a hidden row is.
      const paged: string[] = [];
      for (let offset = 0; ; offset += 3) {
        const page = await searchTables(orm, principal, { q: 'Walnut', limit: 3, offset });
        expect(page.total).toBe(result.total);
        paged.push(...page.items.map(hit => `${hit.table}:${hit.key}`));
        expect(page.items.map(hit => hit.place)).toEqual(page.items.map((_, i) => offset + i + 1));
        if (!page.more) break;
      }
      expect(paged).toEqual(keys);
    }
    const asAlice = (await found(user(alice), 'walnut')).keys;
    const asBob = (await found(user(bob), 'walnut')).keys;
    expect(asAlice).toContain(`Folder:${ids.privateFolder}`);
    expect(asAlice).toContain(`Document:${ids.privateDoc}`);
    expect(asBob).not.toContain(`Folder:${ids.privateFolder}`);
    expect(asBob).not.toContain(`Document:${ids.privateDoc}`);
    expect(asBob).toContain(`Document:${ids.sharedDoc}`);
    expect(asAlice.length - asBob.length).toBe(2);
    // A soft-deleted note is in no one's search.
    expect(asAlice).not.toContain(`Note:${ids.noteGone}`);
    // A match in a title ranks above one in a body alone.
    expect(asBob.indexOf(`Contract:${ids.contractA}`)).toBeLessThan(asBob.indexOf(`Contract:${ids.contractB}`));
    const hit = (await found(user(bob), 'walnut')).result.items.find(h => h.key === ids.contractB)!;
    expect(hit.matched).toBe('body');
    expect(hit.snippet.filter(part => part.match).map(part => part.text.toLowerCase())).toEqual(['walnut']);
    // Two words: a row holds both.
    expect((await found(user(bob), 'walnut sink')).keys).toEqual([`Request:${ids.requestA}`]);
    // An anonymous caller sees what everyone may.
    expect((await found(null, 'walnut')).keys.sort()).toEqual([...asBob].sort());
  });

  test('a party with a grant finds only the rows its grant reaches, through the route and the MCP tool too, and a revoked grant finds nothing', async () => {
    const { grant, token } = await issueGrant(orm.asSystem(), { holder: party, holderKind: 'party', role: 'contract-party', targetType: 'Contract', targetId: ids.contractA });
    const authenticate = grantAuthenticator({ orm });
    const ctx = { headers: new Headers({ authorization: `Bearer ${token}` }) } as never;
    const principal = (await authenticate(ctx))!;
    expect(principal.kind).toBe('party');
    const { result, keys } = await found(principal, 'walnut');
    expect([...keys].sort()).toEqual(await listed(principal, 'walnut'));
    expect([...keys].sort()).toEqual([`Contract:${ids.contractA}`, `Request:${ids.requestA}`, `Unit:${ids.unitA}`].sort());
    expect(result.total).toBe(3);
    // The route, as the grant's holder.
    const route = searchRoutes({ orm, authenticate: async request => authenticate({ headers: request.headers } as never) });
    const response = (await route(new Request('http://api.test/api/search?q=walnut&limit=2', { headers: { authorization: `Bearer ${token}` } })))!;
    expect(response.status).toBe(200);
    const body = (await response.json()) as { data: { items: { table: string; key: string }[]; total: number; more: boolean } };
    expect(body.data.total).toBe(3);
    expect(body.data.more).toBe(true);
    expect(body.data.items.map(hit => `${hit.table}:${hit.key}`)).toEqual(keys.slice(0, 2));
    // The MCP tool, as the same principal.
    const called = await callSearchTool(orm, principal, { q: 'walnut', tables: ['Unit', 'Request', 'Folder'] });
    expect('result' in called && called.result.items.map(hit => `${hit.table}:${hit.key}`).sort()).toEqual([`Request:${ids.requestA}`, `Unit:${ids.unitA}`].sort());
    // Revoked, the very next call is refused, and no principal finds nothing.
    await revokeGrant(orm.asSystem(), String(grant.id));
    expect(authenticate(ctx)).rejects.toThrow();
    const refused = (await route(new Request('http://api.test/api/search?q=walnut', { headers: { authorization: `Bearer ${token}` } })).catch(() => null)) as Response | null;
    expect(refused === null || refused.status === 401).toBe(true);
    expect((await searchRoutes({ orm, authenticate: async () => null })(new Request('http://api.test/api/search?q=walnut')))!.status).toBe(401);
  });

  test("a row's conversation and files are indexed with it, and an encrypted field never is", async () => {
    await orm.withBehaviorCaller({ subject: alice, permissions: [] }).unit.comments.post(ids.unitB, { body: 'The sycamore by the gate needs a trim.' } as never);
    await db.pool.query(
      `INSERT INTO unit_attachments (unit_id, name, media_type, size, content_hash, object_key, state, extraction, extracted_text)
       VALUES ($1, 'survey.pdf', 'application/pdf', 10, $2, 'attachments/survey', 'ready', 'done', 'Juniper hedges line the drive.')`,
      [uuidOf(ids.unitB), '0'.repeat(64)]
    );
    expect(await drain()).toBe(1);
    const sycamore = (await found(user(bob), 'sycamore')).result.items;
    expect(sycamore.map(hit => `${hit.table}:${hit.key}`)).toEqual([`Unit:${ids.unitB}`]);
    expect(sycamore[0]!.matched).toBe('comments');
    const juniper = (await found(user(bob), 'juniper')).result.items;
    expect(juniper.map(hit => `${hit.table}:${hit.key}`)).toEqual([`Unit:${ids.unitB}`]);
    expect(juniper[0]!.matched).toBe('files');
    // The contract's terms are sealed at rest and never reach the index.
    expect((await found(user(alice), 'obsidian')).result.total).toBe(0);
    const { rows } = await db.pool.query(`SELECT count(*)::int AS n FROM search_index WHERE concat_ws(' ', title, body, comments, files) ILIKE '%obsidian%'`);
    expect(rows[0].n).toBe(0);
  });

  test("a file's text the file processor extracts is indexed with its row (D154, D159, #702)", async () => {
    // A completed upload of a plain text file asks its text: extraction pending.
    const objects = new Map<string, Uint8Array>([['attachments/plan', new TextEncoder().encode('Hawthorn rows along the north wall.')]]);
    const bucket = {
      name: 'mem',
      async get(name: string) {
        const bytes = objects.get(name);
        if (!bytes) throw new ObjectNotFoundError('mem', name);
        return { object: { name, size: bytes.byteLength }, body: new Response(bytes).body! };
      },
      async put(name: string, body: Uint8Array) {
        objects.set(name, body);
        return { name, size: body.byteLength, created: true };
      },
    } as unknown as Bucket;
    await db.pool.query(
      `INSERT INTO unit_attachments (unit_id, name, media_type, size, content_hash, object_key, state, extraction)
       VALUES ($1, 'plan.txt', 'text/plain', 35, $2, 'attachments/plan', 'ready', 'pending')`,
      [uuidOf(ids.unitA), '1'.repeat(64)]
    );
    await drain();
    expect((await found(user(alice), 'hawthorn')).result.total).toBe(0);
    const plainText = defineProvider({
      name: 'plain-text',
      implements: 'files',
      config: {},
      create: (): FileProcessor => ({
        mediaTypes: ['text/plain'],
        extractText: async file => ({ text: new TextDecoder().decode(file.bytes) }),
        preview: async () => null,
      }),
    });
    const providers = providersFrom({ slots: { FILES: 'files' }, config: { FILES: 'plain-text' }, definitions: [plainText] });
    expect(await processFiles({ orm, providers, bucket })).toEqual({ extracted: 1, previewed: 0, failed: 0 });
    const { rows } = await db.pool.query(`SELECT extraction, extracted_text FROM unit_attachments WHERE object_key = 'attachments/plan'`);
    expect(rows[0]).toEqual({ extraction: 'done', extracted_text: 'Hawthorn rows along the north wall.' });
    // The write of the text enqueues the row, whose index row then holds it.
    expect(await drain()).toBe(1);
    const hawthorn = (await found(user(alice), 'hawthorn')).result.items;
    expect(hawthorn.map(hit => `${hit.table}:${hit.key}`)).toEqual([`Unit:${ids.unitA}`]);
    expect(hawthorn[0]!.matched).toBe('files');
  });

  test('an index row follows its row: an audience change and a delete at once, a change of text once the queue drains', async () => {
    const asAlice = orm.as(user(alice));
    expect((await found(user(bob), 'walnut')).keys).toContain(`Folder:${ids.sharedFolder}`);
    // Alice keeps the shared folder to herself: its documents inherit it.
    // Neither shows to bob, with no drain, since the trigger copied it.
    await asAlice.folder.updateOne(ids.sharedFolder, { audience: 'private' } as never);
    const bobs = (await found(user(bob), 'walnut')).keys;
    expect(bobs).not.toContain(`Folder:${ids.sharedFolder}`);
    expect(bobs).not.toContain(`Document:${ids.sharedDoc}`);
    expect([...bobs].sort()).toEqual(await listed(user(bob), 'walnut'));
    await asAlice.folder.updateOne(ids.sharedFolder, { audience: 'everyone' } as never);
    expect((await found(user(bob), 'walnut')).keys).toContain(`Document:${ids.sharedDoc}`);
    // A delete takes the index row with it, at once.
    await orm.asSystem().request.deleteOne(ids.requestB);
    expect((await found(user(bob), 'walnut')).keys).not.toContain(`Request:${ids.requestB}`);
    // A change of text waits for the queue: found by its old words until
    // the worker indexes it again.
    await orm.asSystem().note.updateOne(ids.noteP1, { title: 'Chestnut idea' } as never);
    expect((await found(user(bob), 'chestnut')).result.total).toBe(0);
    expect(await drain()).toBe(1);
    expect((await found(user(bob), 'chestnut')).keys).toEqual([`Note:${ids.noteP1}`]);
    // An update that changes no indexed field enqueues nothing.
    await orm.asSystem().unit.updateOne(ids.unitA, { contractId: ids.contractA } as never);
    expect(await drain()).toBe(0);
  });

  test("scope keeps a search to the app's filter", async () => {
    const p1 = await found(user(bob), 'idea', undefined, { scope: 'p1' });
    expect(p1.keys).toEqual([`Note:${ids.noteP1}`]);
    const both = await found(user(bob), 'idea', undefined, { scope: ['p1', 'p2'] });
    expect(both.keys.sort()).toEqual([`Note:${ids.noteP1}`, `Note:${ids.noteP2}`].sort());
    // A relation's key is a scope too, in its wire form.
    const unit = await found(user(bob), 'walnut', undefined, { scope: ids.unitA });
    expect(unit.keys).toEqual([`Request:${ids.requestA}`]);
    // tables keeps to tables, and one that is not @searchable is refused.
    expect((await found(user(bob), 'walnut', undefined, { tables: ['Contract'] })).keys.sort()).toEqual([`Contract:${ids.contractA}`, `Contract:${ids.contractB}`].sort());
    expect(searchTables(orm, user(bob), { q: 'walnut', tables: ['ContractParty'] })).rejects.toThrow(TypeError);
  });

  test('embeddings re-rank the first hits, are kept while the text holds, and can be switched off', async () => {
    // A stand-in model: a text's vector says whether it is about a door.
    let calls = 0;
    const embeddings: SearchEmbeddings = {
      model: 'stand-in-1',
      async embed(texts) {
        calls += texts.length;
        return texts.map(text => (/door/iu.test(text) ? [1, 0] : [0, 1]));
      },
    };
    const everyRow = `INSERT INTO search_indexing_queue (source_table, source_key) SELECT source_table, source_key FROM search_index`;
    await db.pool.query(everyRow);
    await drain({ embeddings });
    const counts = (await db.pool.query(`SELECT count(*)::int AS rows, count(*) FILTER (WHERE embedding_model = 'stand-in-1')::int AS embedded FROM search_index`)).rows[0];
    expect(counts.embedded).toBe(counts.rows);
    // A row whose text did not change keeps its embedding: no model call.
    const before = calls;
    await db.pool.query(everyRow);
    await drain({ embeddings });
    expect(calls).toBe(before);
    // A change of text clears its embedding, until it is embedded again.
    await orm.asSystem().contract.updateOne(ids.contractB, { summary: 'Bolts, a walnut trim and a door.' } as never);
    await drain();
    const stored = async () => (await db.pool.query(`SELECT embedding FROM search_index WHERE source_table = 'Contract' AND source_key = $1`, [uuidOf(ids.contractB)])).rows[0].embedding;
    expect(await stored()).toBeNull();
    await db.pool.query(`INSERT INTO search_indexing_queue (source_table, source_key) VALUES ('Contract', $1)`, [uuidOf(ids.contractB)]);
    await drain({ embeddings });
    expect(await stored()).toEqual([1, 0]);

    // The text alone ranks the body-only match low; a query about doors
    // lifts it, over the same rows and the same total.
    const plain = await found(user(bob), 'walnut');
    expect(plain.result.reranked).toBe(false);
    const doors = await found(user(bob), 'walnut', { embeddings }, { vector: [1, 0] });
    expect(doors.result.reranked).toBe(true);
    expect(doors.result.total).toBe(plain.result.total);
    expect([...doors.keys].sort()).toEqual([...plain.keys].sort());
    const place = (keys: string[]) => keys.indexOf(`Contract:${ids.contractB}`);
    expect(place(doors.keys)).toBeLessThan(place(plain.keys));
    // With embeddings on and no vector, the query is embedded: the same.
    const embedded = await found(user(bob), 'walnut', { embeddings: { ...embeddings, embed: async texts => texts.map(() => [1, 0]) } });
    expect(embedded.keys).toEqual(doors.keys);
    // A model that fails leaves the text order; switched off, the text order.
    const failing = await found(user(bob), 'walnut', { embeddings: { ...embeddings, embed: async () => Promise.reject(new Error('down')) } });
    expect(failing.result.reranked).toBe(false);
    expect(failing.keys).toEqual(plain.keys);
    expect((await found(user(bob), 'walnut')).keys).toEqual(plain.keys);

    // A reindex embeds a page of rows in one model call: with every
    // embedding cleared, each table's rows are embedded again in one call,
    // and its index rows are what indexing each row wrote.
    const indexRows = async (): Promise<string[]> =>
      (
        await db.pool.query(
          `SELECT format('%s %s %L %L %L %L %L %L %L %s %L %L', source_table, source_key, title, body, comments, files, scope, audience::text, agents::text, text_hash, embedding::text, embedding_model) AS row
           FROM search_index ORDER BY source_table, source_key`
        )
      ).rows.map(r => String(r.row));
    const written = await indexRows();
    await db.pool.query(`UPDATE search_index SET embedding = NULL, embedding_model = NULL`);
    let modelCalls = 0;
    const textsBefore = calls;
    const n = await reindexSearch(orm, { embeddings: { ...embeddings, embed: texts => (modelCalls++, embeddings.embed(texts)) } });
    // The index rows and the soft-deleted note.
    expect(n).toBe(written.length + 1);
    expect([modelCalls, calls - textsBefore]).toEqual([6, written.length]);
    expect(await indexRows()).toEqual(written);
  });

  test("the parity file's searches answer the same titles in every runtime (#740)", async () => {
    await writeParityCorpus('postgres', (text, values) => db.pool.query(text, values));
    const answers: Record<string, unknown> = {};
    for (const search of paritySearches) answers[search.name] = await parityAnswer(searchTables, orm, user(alice), search);
    if (process.env.SEARCH_PARITY_PRINT) console.log(JSON.stringify(answers));
    expect(answers).toEqual(Object.fromEntries(paritySearches.map(search => [search.name, search.postgres ?? null])));
  });
});

/** A base62 UUID's hyphenated form, as a statement binds it. */
function uuidOf(base62: string): string {
  const alphabet = '0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz';
  let n = 0n;
  for (const c of base62) n = n * 62n + BigInt(alphabet.indexOf(c));
  const hex = n.toString(16).padStart(32, '0');
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}
