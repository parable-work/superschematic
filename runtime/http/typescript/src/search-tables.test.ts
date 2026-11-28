import { describe, expect, test } from 'bun:test';
import { createHash } from 'node:crypto';
import {
  MAX_SEARCH_WORDS,
  callSearchTool,
  indexSearch,
  reindexSearch,
  searchEmbeddingsOf,
  searchMcpTool,
  searchRoutes,
  searchSourcesOf,
  searchTables,
  searchWords,
  type SearchableDefinition,
} from './search-tables.js';

// The search across tables' own rules (D155), without a database: the
// words a query counts, the refusals of a request it cannot read, and the
// route's and the tool's answers. TestSearchTablesEndToEnd runs the search
// itself on Postgres.

/** An ORM with no @searchable table, whose as() is itself. */
const bare = { as: () => bare, asSystem: () => bare, query: async () => ({ rows: [] }), transaction: async (fn: (tx: unknown) => unknown) => fn(bare) };

describe('search across tables', () => {
  test('a query counts its first words, with no snippet marks', () => {
    expect(searchWords('  walnut\tdesk   hall ')).toEqual(['walnut', 'desk', 'hall']);
    expect(searchWords('a\u0002b c\u0003')).toEqual(['ab', 'c']);
    expect(searchWords(' ')).toEqual([]);
    expect(searchWords(Array.from({ length: 40 }, (_, i) => `w${i}`).join(' '))).toHaveLength(MAX_SEARCH_WORDS);
  });

  test('a blank query finds nothing, and a page out of bounds is refused', async () => {
    expect(await searchTables(bare, null, { q: '   ' })).toEqual({ items: [], total: 0, more: false, reranked: false });
    expect(searchTables(bare, null, { q: 'walnut', limit: 0 })).rejects.toThrow(TypeError);
    expect(searchTables(bare, null, { q: 'walnut', offset: -1 })).rejects.toThrow(TypeError);
    expect(searchTables(bare, null, { q: 'walnut', tables: ['Nothing'] })).rejects.toThrow('Nothing is no @searchable table');
    expect(searchSourcesOf(bare).size).toBe(0);
  });

  test('the route needs a caller and reads its query', async () => {
    const route = searchRoutes({ orm: bare, authenticate: async request => (request.headers.has('authorization') ? { subject: 'u', permissions: [] } : null) });
    expect(await route(new Request('http://api.test/api/other'))).toBeNull();
    expect((await route(new Request('http://api.test/api/search?q=walnut')))!.status).toBe(401);
    const asked = (q: string) => route(new Request(`http://api.test/api/search?${q}`, { headers: { authorization: 'Bearer x' } }));
    expect((await asked('q=walnut&limit=1000'))!.status).toBe(400);
    const ok = (await asked('q=walnut'))!;
    expect(ok.status).toBe(200);
    expect(await ok.json()).toEqual({ data: { items: [], total: 0, more: false, reranked: false } });
    expect((await route(new Request('http://api.test/api/search', { method: 'POST' })))!.status).toBe(405);
  });

  test('the MCP tool reads only, and refuses a call without q', async () => {
    expect(searchMcpTool.name).toBe('search');
    expect(searchMcpTool.annotations.readOnlyHint).toBe(true);
    expect(await callSearchTool(bare, null, {})).toEqual({ problem: { status: 400, title: 'Bad Request', detail: 'q is required' } });
    expect(await callSearchTool(bare, null, { q: 'walnut', limit: 0 })).toMatchObject({ problem: { status: 400 } });
  });

  test("embeddings come from a model provider that embeds, under the model's name", async () => {
    expect(() => searchEmbeddingsOf({}, 'm')).toThrow(TypeError);
    const embeddings = searchEmbeddingsOf({ embed: async ({ model, texts }) => ({ vectors: texts.map(t => [t.length, model === 'm' ? 1 : 0]) }) }, 'm');
    expect(embeddings.model).toBe('m');
    expect(await embeddings.embed(['ab', 'abc'])).toEqual([
      [2, 1],
      [3, 1],
    ]);
  });
});

/** The definition of a table of notes held in memory. */
const noteIndex: SearchableDefinition = {
  index: 'search_index',
  document: '',
  table: 'note',
  key: 'id',
  keyType: 'text',
  title: 'title',
  body: ['text'],
  deletedAt: 'deleted_at',
  comments: { table: 'note_comments', row: 'note_id', body: 'body', state: 'state', order: 'message' },
};

/**
 * An ORM over a table of notes held in memory, which answers the
 * statements indexSearch and reindexSearch run and records them: the
 * batching itself, without a database. TestSearchTablesEndToEnd runs them
 * on Postgres.
 */
function notesOrm(notes: Map<string, { title: string; text: string | null; deleted?: boolean }>, comments: Map<string, string> = new Map()) {
  const statements: string[] = [];
  const upserts: unknown[][] = [];
  const updates: unknown[][] = [];
  const deletes: string[][] = [];
  const query = async (sql: string, values: readonly unknown[] = []): Promise<{ rows: Record<string, unknown>[] }> => {
    statements.push(sql);
    if (sql.startsWith('SELECT id::text AS key, id AS k FROM note')) {
      const after = values[0] as string | undefined;
      const limit = Number(/LIMIT (\d+)$/.exec(sql)![1]);
      return { rows: [...notes.keys()].sort().filter(k => after === undefined || k > after).slice(0, limit).map(key => ({ key })) };
    }
    if (sql.startsWith('SELECT given.key AS given')) {
      return { rows: (values[0] as string[]).filter(k => notes.has(k)).map(k => ({ given: k, key: k, title: notes.get(k)!.title, b0: notes.get(k)!.text, deleted: notes.get(k)!.deleted === true })) };
    }
    if (sql.includes('string_agg(body')) return { rows: (values[0] as string[]).filter(k => comments.has(k)).map(k => ({ key: k, text: comments.get(k) })) };
    if (sql.startsWith('INSERT INTO search_index')) {
      upserts.push([...values]);
      return { rows: (values[1] as string[]).map(k => ({ source_key: k, embedding_model: null })) };
    }
    if (sql.startsWith('DELETE FROM search_index')) {
      deletes.push(values[1] as string[]);
      return { rows: [] };
    }
    if (sql.startsWith('UPDATE search_index')) {
      updates.push([...values]);
      return { rows: [] };
    }
    throw new Error(`unexpected statement ${sql}`);
  };
  const orm = {
    note: { def: { name: 'Note', searchable: noteIndex }, conditions: () => [] },
    as: () => orm,
    asSystem: () => orm,
    query,
    transaction: async (fn: (tx: unknown) => unknown) => fn(orm),
  };
  return { orm, statements, upserts, updates, deletes };
}

const hashOf = (text: string) => createHash('sha256').update(text).digest('hex');

describe('search indexing', () => {
  test('a reindex runs a page of rows in as many statements as one, with one model call', async () => {
    const notes = new Map(Array.from({ length: 7 }, (_, i) => [`n${i + 1}`, { title: `Note ${i + 1}`, text: `Text ${i + 1}`, deleted: i + 1 === 5 }]));
    const { orm, statements, upserts, updates, deletes } = notesOrm(notes, new Map([['n1', 'First comment']]));
    const calls: string[][] = [];
    const embeddings = {
      model: 'm',
      async embed(texts: readonly string[]) {
        calls.push([...texts]);
        return texts.map(() => [3, 4]);
      },
    };
    expect(await reindexSearch(orm, { embeddings, batch: 3 })).toBe(7);
    // Three pages: one model call each, with every text of the page that
    // was indexed, the soft-deleted note's index row deleted instead.
    expect(calls.map(texts => texts.length)).toEqual([3, 2, 1]);
    expect(calls[0]![0]).toBe('Note 1\nText 1\nFirst comment');
    expect(deletes).toEqual([['n5']]);
    // Each page runs the same statements whatever its size: its keys, its
    // rows, their conversations, the delete of a page with a row gone,
    // the upsert and the embeddings' update.
    expect(statements).toHaveLength(3 * 5 + 1);
    expect(upserts).toHaveLength(3);
    expect(upserts[0]![1]).toEqual(['n1', 'n2', 'n3']);
    expect((upserts[0]![7] as string[]).slice(0, 2)).toEqual([hashOf('Note 1\nText 1\nFirst comment'), hashOf('Note 2\nText 2')]);
    expect((upserts[0]![4] as unknown[]).slice(0, 2)).toEqual(['First comment', null]);
    expect(updates).toHaveLength(3);
    expect(updates[1]![2]).toEqual(['n4', 'n6']);
    expect(updates[1]![4]).toEqual(['{0.6,0.8}', '{0.6,0.8}']);
  });

  test('indexSearch is one key of it', async () => {
    const { orm, updates, deletes } = notesOrm(new Map([['n1', { title: 'Note 1', text: null }]]));
    let calls = 0;
    const embeddings = {
      model: 'm',
      async embed() {
        calls++;
        return [[]];
      },
    };
    expect(await indexSearch(orm, { sourceTable: 'Note', sourceKey: 'n1' }, { embeddings })).toBe('indexed');
    // A model that gives no embedding leaves the row unembedded.
    expect([calls, updates.length]).toEqual([1, 0]);
    expect(await indexSearch(orm, { sourceTable: 'Note', sourceKey: 'gone' }, { embeddings })).toBe('removed');
    expect(await indexSearch(orm, { sourceTable: 'Other', sourceKey: 'n1' }, { embeddings })).toBe('unknown');
    expect([deletes, calls]).toEqual([[['gone']], 1]);
  });
});
