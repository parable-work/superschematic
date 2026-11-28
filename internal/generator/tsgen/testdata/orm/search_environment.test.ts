// One search index per environment end to end (#740): two source databases
// of fixture-search-env-db, east and west, each a schema of its own with
// the package's create.sql, and the search database,
// fixture-search-index-db's, in a third. TestEnvironmentSearchEndToEnd
// generates both packages, applies each create.sql and runs these cases
// through the generated repositories and the runtime. drain() runs what a
// source's queue worker runs: each ready message of its SearchIndexing
// queue, handed to indexEnvironmentSearch, which indexes the row in its
// database and copies it into the environment's index.
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import pg from 'pg';
import {
  grantAuthenticator,
  indexEnvironmentSearch,
  issueGrant,
  reindexEnvironmentSearch,
  revokeGrant,
  searchEnvironment,
  searchTables,
  type EnvironmentCopy,
  type EnvironmentIndex,
  type EnvironmentSearchRequest,
  type SearchOptions,
  type SearchRequest,
} from '@superschematic/http-runtime/postgres';
import type { Principal } from '@superschematic/http-runtime';
import { createOrm, type FixtureSearchEnvDbOrm } from './orm';
import { createOrm as createIndexOrm, environmentSearch } from '../fixture-search-index-db/orm';
import { paritySearches, parityAnswer, writeEnvironmentParityCorpus } from './search_parity';

const dsn = process.env.SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL ?? '';
const alice = 'TKbHsHDO6x9RCjbs5c2u4';
const bob = '1JwchoUsfFfo9IGmolouwH';
const party = '3cKSm7Pv2EM8Wl4z4XvMh1';
const user = (subject: string): Principal => ({ subject, permissions: [] });

interface Database {
  readonly pool: pg.Pool;
  drop(): Promise<void>;
}

let schemas = 0;

/** A schema of its own with the create.sql at url applied, and a pool on it. */
async function database(prefix: string, url: URL): Promise<Database> {
  const schema = `${prefix}_${process.pid}_${Date.now()}_${++schemas}`;
  const admin = new pg.Client({ connectionString: dsn });
  await admin.connect();
  await admin.query(`CREATE SCHEMA ${schema}`);
  const pool = new pg.Pool({ connectionString: dsn, options: `-c search_path=${schema},public`, max: 4 });
  await pool.query(readFileSync(url, 'utf8'));
  return {
    pool,
    async drop() {
      await pool.end();
      await admin.query(`DROP SCHEMA ${schema} CASCADE`);
      await admin.end();
    },
  };
}

/** The fields each table's title and body are of. */
const indexed: Record<string, { readonly member: string; readonly fields: readonly string[] }> = {
  Folder: { member: 'folder', fields: ['name'] },
  Document: { member: 'document', fields: ['title', 'text'] },
  Contract: { member: 'contract', fields: ['name', 'summary'] },
  Unit: { member: 'unit', fields: ['name', 'notes'] },
  Note: { member: 'note', fields: ['title', 'text'] },
};

const wordsOf = (text: unknown) => String(text ?? '').toLowerCase().split(/[^\p{L}\p{N}]+/u);

describe.skipIf(dsn === '')('one search index per environment on Postgres (#740)', () => {
  let east: Database;
  let west: Database;
  let search: Database;
  let eastOrm: FixtureSearchEnvDbOrm;
  let westOrm: FixtureSearchEnvDbOrm;
  let index: EnvironmentIndex;
  let sources: Record<string, FixtureSearchEnvDbOrm>;
  const ids: Record<string, string> = {};

  const copyOf = (name: string): EnvironmentCopy => ({ database: name, index });

  /** What a source's queue worker runs: every ready message, indexed and copied. */
  const drain = async (name: 'east' | 'west'): Promise<number> => {
    const db = name === 'east' ? east : west;
    const orm = sources[name]!;
    let n = 0;
    for (;;) {
      const { rows } = await db.pool.query(
        `DELETE FROM search_indexing_queue WHERE id IN (SELECT id FROM search_indexing_queue WHERE state = 'ready' ORDER BY created_at LIMIT 100) RETURNING source_table, source_key`
      );
      if (rows.length === 0) return n;
      for (const row of rows) await indexEnvironmentSearch(orm, copyOf(name), { sourceTable: row.source_table, sourceKey: row.source_key });
      n += rows.length;
    }
  };

  /** The rows of every @searchable table of both databases principal's list reads show whose title or body holds word, as database:table:key. */
  const listed = async (principal: Principal, word: string): Promise<string[]> => {
    const out: string[] = [];
    for (const [name, orm] of Object.entries(sources)) {
      const as = orm.as(principal) as unknown as Record<string, { findMany(filter: object, options?: object): Promise<{ rows: Record<string, unknown>[] }> }>;
      for (const [table, { member, fields }] of Object.entries(indexed)) {
        const { rows } = await as[member]!.findMany({}, { limit: 1000 });
        for (const row of rows) if (fields.some(field => wordsOf(row[field]).includes(word))) out.push(`${name}:${table}:${String(row.id)}`);
      }
    }
    return out.sort();
  };

  const found = async (principal: Principal | null, q: string, extra: Partial<EnvironmentSearchRequest> = {}) => {
    const result = await searchEnvironment(index, sources, principal, { q, limit: 100, ...extra });
    return { result, keys: result.items.map(hit => `${hit.database}:${hit.table}:${hit.key}`) };
  };

  beforeAll(async () => {
    east = await database('ts_env_east', new URL('./create.sql', import.meta.url));
    west = await database('ts_env_west', new URL('./create.sql', import.meta.url));
    search = await database('ts_env_index', new URL('../fixture-search-index-db/create.sql', import.meta.url));
    eastOrm = createOrm(east.pool, { user: alice });
    westOrm = createOrm(west.pool, { user: alice });
    index = { orm: createIndexOrm(search.pool), definition: environmentSearch };
    sources = { east: eastOrm, west: westOrm };
    for (const [name, orm] of Object.entries(sources)) {
      const system = orm.asSystem();
      const asAlice = orm.as(user(alice));
      // Alice's own folder, and one everyone sees, each with a document.
      ids[`${name}PrivateFolder`] = (await asAlice.folder.createOne({ name: `Walnut private ${name}`, owner: alice, audience: 'private', createdAt: new Date() } as never)).id as string;
      ids[`${name}SharedFolder`] = (await asAlice.folder.createOne({ name: `Walnut shared ${name}`, owner: alice, createdAt: new Date() } as never)).id as string;
      ids[`${name}PrivateDoc`] = (await asAlice.document.createOne({ folderId: ids[`${name}PrivateFolder`], title: 'Walnut plan', text: `The ${name} plan.`, createdAt: new Date() } as never)).id as string;
      ids[`${name}SharedDoc`] = (await asAlice.document.createOne({ folderId: ids[`${name}SharedFolder`], title: 'Memo', text: `A walnut desk in the ${name} hall.`, createdAt: new Date() } as never)).id as string;
      // A contract with a unit, and notes in two projects.
      ids[`${name}Contract`] = (await system.contract.createOne({ name: `Walnut supply ${name}`, summary: 'Timber for the year.' } as never)).id as string;
      ids[`${name}Unit`] = (await system.unit.createOne({ name: 'Walnut unit', notes: `In ${name}.`, contractId: ids[`${name}Contract`] } as never)).id as string;
      ids[`${name}Note`] = (await system.note.createOne({ project: 'p1', title: 'Walnut idea', text: 'Oil it twice.' } as never)).id as string;
      ids[`${name}Pine`] = (await system.note.createOne({ project: 'p2', title: 'Pine idea', text: 'A walnut stain on pine.' } as never)).id as string;
      await system.role.createOne({ name: 'contract-party', permissions: [], reaches: ['Unit:contract'] } as never);
    }
    expect(await drain('east')).toBeGreaterThan(0);
    expect(await drain('west')).toBeGreaterThan(0);
  });

  afterAll(async () => {
    await east?.drop();
    await west?.drop();
    await search?.drop();
  });

  test('one search ranks the rows of both databases, and two principals each get exactly their rows, with right totals', async () => {
    for (const subject of [alice, bob]) {
      const principal = user(subject);
      const { result, keys } = await found(principal, 'walnut');
      expect([...keys].sort()).toEqual(await listed(principal, 'walnut'));
      expect(result.total).toBe(keys.length);
      expect(new Set(result.items.map(hit => hit.database))).toEqual(new Set(['east', 'west']));
      // A page of three at a time reads the same rows in the same order,
      // with the same total on every page.
      const paged: string[] = [];
      for (let offset = 0; ; offset += 3) {
        const page = await searchEnvironment(index, sources, principal, { q: 'walnut', limit: 3, offset });
        expect(page.total).toBe(result.total);
        paged.push(...page.items.map(hit => `${hit.database}:${hit.table}:${hit.key}`));
        if (!page.more) break;
      }
      expect(paged).toEqual(keys);
    }
    const asAlice = (await found(user(alice), 'walnut')).keys;
    const asBob = (await found(user(bob), 'walnut')).keys;
    expect(asAlice).toContain(`east:Folder:${ids.eastPrivateFolder}`);
    expect(asBob).not.toContain(`west:Document:${ids.westPrivateDoc}`);
    expect(asAlice.length - asBob.length).toBe(4);
    // Over one database's rows, the environment's search ranks as the
    // database's own index does: the same hits in the same order.
    for (const name of ['east', 'west'] as const) {
      for (const q of ['walnut', 'walnut OR pine', 'idea -pine', '"walnut desk"', 'wal*']) {
        const own = await searchTables(sources[name]!, user(bob), { q, limit: 100 });
        const env = await searchEnvironment(index, sources, user(bob), { q, limit: 100, databases: [name] });
        expect(env.items.map(hit => [hit.table, hit.key, hit.title, hit.matched, hit.snippet])).toEqual(own.items.map(hit => [hit.table, hit.key, hit.title, hit.matched, hit.snippet]));
        expect(env.total).toBe(own.total);
      }
    }
    // tables and scope keep to what they name, in every database.
    expect((await found(user(bob), 'idea', { scope: 'p1' })).keys.sort()).toEqual([`east:Note:${ids.eastNote}`, `west:Note:${ids.westNote}`].sort());
    expect((await found(user(bob), 'walnut', { tables: ['Contract'] })).keys.sort()).toEqual([`east:Contract:${ids.eastContract}`, `west:Contract:${ids.westContract}`].sort());
    expect(searchEnvironment(index, sources, user(bob), { q: 'walnut', databases: ['north'] })).rejects.toThrow(TypeError);
    // A table's permission keeps a caller from its rows in that database alone.
    const held = await searchEnvironment(index, sources, user(bob), { q: 'walnut', limit: 100 }, { permissions: { west: { Contract: 'contracts.read' } } });
    expect(held.items.map(hit => `${hit.database}:${hit.table}`)).not.toContain('west:Contract');
    expect(held.items.map(hit => `${hit.database}:${hit.table}`)).toContain('east:Contract');
  });

  test("a party's grant in one database reaches its rows there alone, and once revoked they stop showing after that database's worker drains", async () => {
    const { grant, token } = await issueGrant(eastOrm.asSystem(), { holder: party, holderKind: 'party', role: 'contract-party', targetType: 'Contract', targetId: ids.eastContract! });
    const authenticate = grantAuthenticator({ orm: eastOrm });
    const principal = (await authenticate({ headers: new Headers({ authorization: `Bearer ${token}` }) } as never))!;
    // Before east's worker drains the grant's message, its copy reaches nothing.
    expect((await found(principal, 'walnut')).result.total).toBe(0);
    expect(await drain('east')).toBe(1);
    const reached = await found(principal, 'walnut');
    expect([...reached.keys].sort()).toEqual([`east:Contract:${ids.eastContract}`, `east:Unit:${ids.eastUnit}`].sort());
    expect([...reached.keys].sort()).toEqual(await listed(principal, 'walnut'));
    expect(reached.result.total).toBe(2);
    // A unit written under the contract later is reached once it is copied.
    ids.eastUnit2 = (await eastOrm.asSystem().unit.createOne({ name: 'Walnut annex', contractId: ids.eastContract } as never)).id as string;
    expect(await drain('east')).toBe(1);
    expect((await found(principal, 'walnut')).keys).toContain(`east:Unit:${ids.eastUnit2}`);
    // Revoked: the copy still holds its reach until the worker drains.
    await revokeGrant(eastOrm.asSystem(), String(grant.id));
    expect((await found(principal, 'walnut')).result.total).toBe(3);
    expect(await drain('east')).toBe(1);
    expect((await found(principal, 'walnut')).result).toEqual({ items: [], total: 0, more: false, reranked: false });
    expect(authenticate({ headers: new Headers({ authorization: `Bearer ${token}` }) } as never)).rejects.toThrow();
  });

  test('a delete, a soft delete and an audience change leave the index once the worker drains, and a late write never overwrites a newer one', async () => {
    const tombstone = async (name: string, table: string, key: string) =>
      (await search.pool.query(`SELECT title, audience::text AS audience, reach, text_hash FROM environment_search_index WHERE database = $1 AND source_table = $2 AND source_key = $3`, [name, table, uuidOf(key)])).rows[0];
    // A delete: found until west's worker drains, then gone, its row a tombstone.
    await westOrm.asSystem().unit.deleteOne(ids.westUnit!);
    expect((await found(user(bob), 'walnut')).keys).toContain(`west:Unit:${ids.westUnit}`);
    expect(await drain('west')).toBe(1);
    expect((await found(user(bob), 'walnut')).keys).not.toContain(`west:Unit:${ids.westUnit}`);
    expect(await tombstone('west', 'Unit', ids.westUnit!)).toEqual({ title: '', audience: '{}', reach: [], text_hash: null });
    // A soft delete.
    await westOrm.asSystem().note.deleteOne(ids.westNote!);
    expect(await drain('west')).toBe(1);
    expect((await found(user(bob), 'idea')).keys).not.toContain(`west:Note:${ids.westNote}`);
    // An audience change: alice keeps east's shared folder to herself, and
    // its document inherits it.
    await eastOrm.as(user(alice)).folder.updateOne(ids.eastSharedFolder!, { audience: 'private' } as never);
    await drain('east');
    const bobs = (await found(user(bob), 'walnut')).keys;
    expect(bobs).not.toContain(`east:Folder:${ids.eastSharedFolder}`);
    expect(bobs).not.toContain(`east:Document:${ids.eastSharedDoc}`);
    expect([...bobs].sort()).toEqual(await listed(user(bob), 'walnut'));
    // A copy read before a newer one was written does not overwrite it:
    // with the row's version set past the sequence, the next copy is held off.
    await search.pool.query(`UPDATE environment_search_index SET version = version + 1000000 WHERE database = 'east' AND source_table = 'Note' AND source_key = $1`, [uuidOf(ids.eastNote!)]);
    await eastOrm.asSystem().note.updateOne(ids.eastNote!, { title: 'Chestnut idea' } as never);
    expect(await drain('east')).toBe(1);
    expect((await found(user(bob), 'chestnut')).result.total).toBe(0);
    // reindexEnvironmentSearch copies every row again, and deletes the tombstones.
    await search.pool.query(`UPDATE environment_search_index SET version = 0 WHERE database = 'east'`);
    expect(await reindexEnvironmentSearch(eastOrm, copyOf('east'))).toBeGreaterThan(0);
    expect((await found(user(bob), 'chestnut')).keys).toEqual([`east:Note:${ids.eastNote}`]);
    await reindexEnvironmentSearch(westOrm, copyOf('west'));
    expect(await tombstone('west', 'Unit', ids.westUnit!)).toBeUndefined();
  });

  test("the parity file's searches answer the same titles over the environment's index as over a database's (#740)", async () => {
    await writeEnvironmentParityCorpus('east', (text, values) => search.pool.query(text, values));
    const across = (_orm: object, principal: Principal | null, request: SearchRequest, options?: SearchOptions) =>
      searchEnvironment(index, sources, principal, { ...request, databases: ['east'] }, options?.embeddings ? { embeddings: options.embeddings } : {});
    const answers: Record<string, unknown> = {};
    for (const parity of paritySearches) answers[parity.name] = await parityAnswer(across, eastOrm, user(alice), parity);
    if (process.env.SEARCH_PARITY_PRINT) console.log(JSON.stringify(answers));
    expect(answers).toEqual(Object.fromEntries(paritySearches.map(parity => [parity.name, parity.postgres ?? null])));
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
