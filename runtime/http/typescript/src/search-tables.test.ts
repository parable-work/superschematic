import { describe, expect, test } from 'bun:test';
import { MAX_SEARCH_WORDS, callSearchTool, searchEmbeddingsOf, searchMcpTool, searchRoutes, searchSourcesOf, searchTables, searchWords } from './search-tables.js';

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
