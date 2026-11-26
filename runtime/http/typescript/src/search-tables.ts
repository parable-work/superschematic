import type { Principal } from './auth.js';
import { accessCallerOf } from './caller.js';
import { audienceConditions } from './orm.js';
import { sha256 } from './sha256.js';
import { uuidFromDb, uuidToDb } from './uuid.js';

/*
Search across tables (D155): one search finds a row of any @searchable
table, for a UI and for an agent alike.

The database keeps one index table. Each @searchable table's rows have a
row each in it: the record's table and key, its title, its body, the text
of its conversation (Comments) and its files' extracted text
(@attachments), its scope, an optional embedding, and its effective
audience and agent access (D156). A trigger on the table deletes the index
row with its row, and copies a change of the row's audience into it, in
the writer's transaction; every write that changes the indexed text
enqueues the row on the declaration's queue, whose worker calls
indexSearch. So a search's access check is never behind the row, and its
text is behind it by the queue's drain: on Workers, the next Cron firing.

searchTables ranks every table's index rows in one query, so its pages and
its total are exact and a row the caller may not read never shows, as a gap
or otherwise:

- The words: each whitespace-separated word of the query is a phrase of
  the words Postgres's parser finds in it, and a row matches when it holds
  every word, as the engine's Search reads a plain query (D58). Words fold
  to lower case and none is stemmed or dropped (the simple configuration,
  the engine's index without unaccent).
- The order: ts_rank_cd over the row's words, weighed by where each is
  (title A, body B, conversation C, files D, the weights the engine gives
  four weight classes), then the table and the key. With an embedding, the
  first RRF_WINDOW by text are re-ranked by reciprocal rank fusion of their
  text place and their place by cosine similarity, RRF_K the engine's
  constant (superschematic_engine::search); a row past the window keeps its
  text order after them.
- Access: the caller's audience is checked on the index row's copied
  columns with audienceConditions, as a repository checks a row's. A
  caller with grants, a party or an agent run, reaches a table's rows only
  through its grants' relation paths, which no copy can hold: for it each
  table's rows are kept by an EXISTS back to the table, with the
  conditions the table's own repository applies for that caller, so its
  search keeps to what its list reads show. A table no grant reaches is
  left out of the query.
- scope is the app's filter: the value of the field each declaration names
  as its scope, a workspace or a project, copied into the index row.

Nothing here imports pg or Hono: the ORM is the generated createOrm's, taken
as a value; searchRoutes answers a fetch Request with a Response.
*/

/** A @searchable table's index as its generated repository's definition carries it (D155). */
export interface SearchableDefinition {
  /** The index table, quoted, and the expression of an index row's weighed words over it as si. */
  readonly index: string;
  readonly document: string;
  /** The table, quoted; its key column, quoted; the key's SQL type; and whether its wire form is base62. */
  readonly table: string;
  readonly key: string;
  readonly keyType: string;
  readonly uuid?: boolean;
  /** The columns an index row's title, body and scope are of, quoted. */
  readonly title: string;
  readonly body?: readonly string[];
  readonly scope?: string;
  /** A soft-deleting table's deleted_at, quoted. */
  readonly deletedAt?: string;
  /** An @audience table's resolved columns, quoted. */
  readonly audience?: { readonly effective: string; readonly agents: string };
  /** The table's Comments messages, whose posted bodies an index row holds. */
  readonly comments?: { readonly table: string; readonly row: string; readonly body: string; readonly state: string; readonly order: string };
  /** The table's files, whose extracted text an index row holds. */
  readonly files?: { readonly table: string; readonly row: string; readonly text: string; readonly state: string };
}

/** A search. */
export interface SearchRequest {
  /** The words: a row matches when it holds every one. */
  readonly q: string;
  /** Only these tables, by type name; every @searchable table the caller may read when absent. */
  readonly tables?: readonly string[];
  /** Only rows whose scope is this value, or one of these. */
  readonly scope?: string | readonly string[];
  /** The page: at most limit hits (20 unless set, at most 100) after offset (at most 10000). */
  readonly limit?: number;
  readonly offset?: number;
  /** A query embedding, from SearchEmbeddings.model, which re-ranks the first hits; the options' embeddings compute one when absent. */
  readonly vector?: readonly number[];
}

/** A part of a snippet: text, which a match is marked in. */
export interface SnippetPart {
  readonly text: string;
  readonly match?: true;
}

/** One hit. */
export interface SearchHit {
  /** The row's table, by type name, and its key in its wire form. */
  readonly table: string;
  readonly key: string;
  readonly title: string;
  /** Where the words matched first: title, body, comments or files. */
  readonly matched: 'title' | 'body' | 'comments' | 'files';
  /** A few words around the matches of that text, each match marked. */
  readonly snippet: readonly SnippetPart[];
  /** Its place in the whole ranking, from 1. */
  readonly place: number;
}

/** A page of hits. */
export interface SearchResult {
  readonly items: readonly SearchHit[];
  /** How many rows match, of those the caller may read. */
  readonly total: number;
  /** Whether rows follow the page. */
  readonly more: boolean;
  /** Whether embeddings re-ranked the first hits. */
  readonly reranked: boolean;
}

/**
 * The embeddings a search re-ranks by, and indexSearch computes: a model's
 * name, which an index row records with its embedding, and the function
 * that embeds texts. Off unless given: no index row is embedded and no
 * search re-ranked.
 */
export interface SearchEmbeddings {
  readonly model: string;
  embed(texts: readonly string[]): Promise<readonly (readonly number[])[]>;
  /** The most characters of a row's text it embeds; 8000 unless set. */
  readonly maxChars?: number;
}

/** What a search and its indexing take beside the ORM. */
export interface SearchOptions {
  /** The permission each table's rows need, by type name: a caller without it finds none of them. */
  readonly permissions?: Readonly<Record<string, string>>;
  /** Embeddings, off unless set. */
  readonly embeddings?: SearchEmbeddings;
}

/** A message of a @searchable declaration's queue: a row to index again. */
export interface SearchIndexing {
  readonly sourceTable: string;
  readonly sourceKey: string;
}

/** Reciprocal rank fusion's constant and window, the engine's (superschematic_engine::search, D16). */
export const RRF_K = 60;
export const RRF_WINDOW = 200;

/** The most words of a query that count, and the most characters of one. */
export const MAX_SEARCH_WORDS = 16;
const MAX_WORD = 200;
/** The most characters of each of an index row's texts. */
export const MAX_INDEXED_TEXT = 262_144;
const MAX_LIMIT = 100;
const MAX_OFFSET = 10_000;
/** The marks a snippet's matches are between, which indexed text never holds. */
const OPEN = '\u0002';
const CLOSE = '\u0003';
const MARKS = /[\u0000\u0002\u0003]/gu;
const WHITE_SPACE = /\p{White_Space}+/u;
/** ts_rank_cd's weights of the classes D, C, B and A. */
const WEIGHTS = `'{0.1, 0.2, 0.4, 1}'::real[]`;

type Values = Record<string, unknown>;

interface RepositoryShape {
  readonly def?: { readonly name?: string; readonly searchable?: SearchableDefinition };
  conditions(filter: null, args: unknown[], offset: number, write?: boolean): string[];
}

interface OrmShape {
  as(principal: Principal | null | undefined): OrmShape;
  asSystem(): OrmShape;
  query(text: string, values?: readonly unknown[]): Promise<{ rows: Values[] }>;
  transaction<T>(fn: (tx: OrmShape) => Promise<T>): Promise<T>;
}

/** The @searchable tables of orm, each with its repository, by type name. */
export function searchSourcesOf(orm: object): Map<string, { readonly definition: SearchableDefinition; readonly repository: RepositoryShape }> {
  const out = new Map<string, { readonly definition: SearchableDefinition; readonly repository: RepositoryShape }>();
  for (const value of Object.values(orm)) {
    const repository = value as RepositoryShape | undefined;
    const def = typeof repository === 'object' && repository !== null ? repository.def : undefined;
    if (def?.searchable && def.name && typeof repository!.conditions === 'function') out.set(def.name, { definition: def.searchable, repository: repository! });
  }
  return out;
}

/** The words of a query that count: its runs of characters that are not white space, the first MAX_SEARCH_WORDS. */
export function searchWords(q: string): string[] {
  return q
    .split(WHITE_SPACE)
    .map(word => word.replace(MARKS, ''))
    .filter(word => word !== '')
    .slice(0, MAX_SEARCH_WORDS)
    .map(word => word.slice(0, MAX_WORD));
}

function whole(value: number | undefined, fallback: number, min: number, max: number, what: string): number {
  if (value === undefined) return fallback;
  if (!Number.isInteger(value) || value < min || value > max) throw new TypeError(`searchTables: ${what} is a whole number from ${min} to ${max}, not ${value}`);
  return value;
}

function unit(vector: readonly number[]): number[] {
  let sum = 0;
  for (const x of vector) {
    if (typeof x !== 'number' || !Number.isFinite(x)) throw new TypeError('an embedding holds finite numbers');
    sum += x * x;
  }
  const norm = Math.sqrt(sum);
  return norm === 0 ? vector.map(() => 0) : vector.map(x => x / norm);
}

/** A snippet's parts, from ts_headline's text with OPEN and CLOSE around each match. */
function snippetParts(text: string): SnippetPart[] {
  const parts: SnippetPart[] = [];
  let rest = text;
  while (rest !== '') {
    const open = rest.indexOf(OPEN);
    if (open < 0) {
      parts.push({ text: rest });
      break;
    }
    if (open > 0) parts.push({ text: rest.slice(0, open) });
    const close = rest.indexOf(CLOSE, open);
    const end = close < 0 ? rest.length : close;
    if (end > open + 1) parts.push({ text: rest.slice(open + 1, end), match: true });
    rest = close < 0 ? '' : rest.slice(close + 1);
  }
  return parts;
}

/**
 * Searches every @searchable table of orm as principal (D155): the rows
 * that hold every word of request.q, of those principal may read, best
 * first, one page of them, and how many there are. A table a caller lacks
 * the permission of (options.permissions) is left out, as is every table a
 * caller with grants reaches no row of.
 */
export async function searchTables(orm: object, principal: Principal | null, request: SearchRequest, options: SearchOptions = {}): Promise<SearchResult> {
  if (typeof request.q !== 'string') throw new TypeError('searchTables: q is the text to search for');
  const limit = whole(request.limit, 20, 1, MAX_LIMIT, 'limit');
  const offset = whole(request.offset, 0, 0, MAX_OFFSET, 'offset');
  const words = searchWords(request.q);
  const empty: SearchResult = { items: [], total: 0, more: false, reranked: false };
  if (words.length === 0) return empty;
  const db = (orm as OrmShape).as(principal);
  const all = searchSourcesOf(db);
  for (const table of request.tables ?? []) {
    if (!all.has(table)) throw new TypeError(`searchTables: ${table} is no @searchable table`);
  }
  const sources = [...all].filter(([table]) => {
    if (request.tables && !request.tables.includes(table)) return false;
    const permission = options.permissions?.[table];
    return permission === undefined || (principal?.permissions ?? []).includes(permission);
  });
  if (sources.length === 0) return empty;
  const { index, document } = sources[0]![1].definition;

  const caller = accessCallerOf(principal);
  const args: unknown[] = [];
  const param = (value: unknown): string => {
    args.push(value);
    return `$${args.length}`;
  };
  const query = words.map(word => `phraseto_tsquery('simple'::regconfig, ${param(word)})`).join(' && ');
  const conditions = [`${document} @@ q.q`];
  // The tables: for a caller with grants, each through its repository's
  // conditions for that caller, an EXISTS back to its rows.
  if (caller.grants === undefined) {
    conditions.push(`si.source_table = ANY(${param(sources.map(([table]) => table))}::text[])`);
  } else {
    const branches: string[] = [];
    for (const [table, { definition, repository }] of sources) {
      const kept = repository.conditions(null, args, args.length);
      if (kept.length === 1 && kept[0] === 'FALSE') continue;
      const name = param(table);
      const match = `${definition.table}.${definition.key} = si.source_key::${definition.keyType}`;
      branches.push(`(si.source_table = ${name} AND EXISTS (SELECT 1 FROM ${definition.table} WHERE ${[match, ...kept].join(' AND ')}))`);
    }
    if (branches.length === 0) return empty;
    conditions.push(`(${branches.join(' OR ')})`);
  }
  // The caller's audience, on the index row's copy of each row's.
  conditions.push(...audienceConditions({ effective: 'si.audience', agents: 'si.agents' }, caller, args, args.length));
  if (request.scope !== undefined) {
    const values = [request.scope].flat();
    const forms = new Set<string>();
    for (const value of values) {
      if (typeof value !== 'string') throw new TypeError('searchTables: a scope is a text');
      forms.add(value);
      try {
        forms.add(uuidToDb(value));
      } catch {
        // Not a UUID: its text alone.
      }
    }
    conditions.push(`si.scope = ANY(${param([...forms])}::text[])`);
  }

  // The query's embedding, when embeddings are on or the request brings one.
  let vector: number[] | undefined;
  let model: string | undefined;
  if (request.vector) {
    if (!options.embeddings) throw new TypeError('searchTables: a vector needs embeddings, which say its model');
    vector = unit(request.vector);
    model = options.embeddings.model;
  } else if (options.embeddings) {
    try {
      const [embedded] = await options.embeddings.embed([request.q]);
      if (embedded && embedded.length > 0) {
        vector = unit(embedded);
        model = options.embeddings.model;
      }
    } catch {
      // A model that fails leaves the text ranking as it is.
    }
  }

  const ranked = `ranked AS (
      SELECT h.*, row_number() OVER (ORDER BY h.rank DESC, h.source_table, h.source_key) AS text_place
      FROM hits AS h
    )`;
  let order = 'r.text_place';
  let fused = '';
  if (vector && model) {
    const v = param(vector);
    const m = param(model);
    fused = `,
    scored AS (
      SELECT r.*, CASE WHEN r.text_place <= ${RRF_WINDOW} AND r.embedding IS NOT NULL AND r.embedding_model = ${m}
        AND cardinality(r.embedding) = cardinality(${v}::float8[])
        THEN (SELECT sum(a * b) FROM unnest(r.embedding, ${v}::float8[]) AS t(a, b)) END AS similarity
      FROM ranked AS r
    ),
    placed AS (
      SELECT s.*, CASE WHEN s.similarity IS NOT NULL
        THEN row_number() OVER (PARTITION BY s.similarity IS NOT NULL ORDER BY s.similarity DESC, s.text_place) END AS vector_place
      FROM scored AS s
    )`;
    order = `(r.text_place > ${RRF_WINDOW}), CASE WHEN r.text_place <= ${RRF_WINDOW} THEN 1.0 / (${RRF_K} + r.text_place) + COALESCE(1.0 / (${RRF_K} + r.vector_place), 0) END DESC, r.text_place`;
  }
  const headline = `'StartSel=${OPEN}, StopSel=${CLOSE}, MaxWords=24, MinWords=12, ShortWord=0, HighlightAll=false, MaxFragments=0'`;
  const matches = (column: string) => `to_tsvector('simple'::regconfig, COALESCE(p.${column}, '')) @@ q.q`;
  const sql = `WITH q AS (SELECT ${query} AS q),
    hits AS (
      SELECT si.source_table, si.source_key, si.title, si.body, si.comments, si.files, si.embedding, si.embedding_model,
        ts_rank_cd(${WEIGHTS}, ${document}, q.q) AS rank
      FROM ${index} AS si CROSS JOIN q
      WHERE ${conditions.join(' AND ')}
    ),
    ${ranked}${fused}
    SELECT c.total, p.source_table, p.source_key, p.title, p.matched,
      ts_headline('simple'::regconfig, p.shown, q.q, ${headline}) AS snippet
    FROM (SELECT count(*) AS total FROM hits) AS c
    CROSS JOIN q
    LEFT JOIN LATERAL (
      SELECT r.source_table, r.source_key, r.title, r.ord,
        CASE WHEN ${matches('title')} THEN 'title' WHEN ${matches('body')} THEN 'body' WHEN ${matches('comments')} THEN 'comments' WHEN ${matches('files')} THEN 'files' ELSE 'title' END AS matched,
        CASE WHEN ${matches('title')} THEN p.title WHEN ${matches('body')} THEN p.body WHEN ${matches('comments')} THEN p.comments WHEN ${matches('files')} THEN p.files ELSE p.title END AS shown
      FROM (
        SELECT r.*, row_number() OVER (ORDER BY ${order}) AS ord
        FROM ${vector && model ? 'placed' : 'ranked'} AS r
        ORDER BY ${order}
        LIMIT ${limit} OFFSET ${offset}
      ) AS r
      CROSS JOIN LATERAL (SELECT r.title, r.body, r.comments, r.files) AS p
    ) AS p ON TRUE
    ORDER BY p.ord`;
  const { rows } = await db.asSystem().query(sql, args);
  const total = Number(rows[0]?.total ?? 0);
  const items: SearchHit[] = [];
  for (const row of rows) {
    if (row.source_table === null || row.source_table === undefined) continue;
    const table = String(row.source_table);
    const definition = all.get(table)?.definition;
    const key = String(row.source_key);
    items.push({
      table,
      key: definition?.uuid ? uuidFromDb(key) : key,
      title: String(row.title ?? ''),
      matched: row.matched as SearchHit['matched'],
      snippet: snippetParts(String(row.snippet ?? '')),
      place: offset + items.length + 1,
    });
  }
  return { items, total, more: offset + items.length < total, reranked: vector !== undefined };
}

function clean(text: unknown): string | null {
  if (text === null || text === undefined) return null;
  const out = String(text).replace(MARKS, ' ').slice(0, MAX_INDEXED_TEXT);
  return out.trim() === '' ? null : out;
}

function hex(bytes: Uint8Array): string {
  return [...bytes].map(b => b.toString(16).padStart(2, '0')).join('');
}

/**
 * Builds the index row of the row a @searchable declaration's queue names
 * (D155), as the system, which its worker calls for each message: the
 * row's title, body, scope, conversation and files' text, and its access
 * columns, read under a share lock of the row, so a change of its audience
 * waits for it and copies into the row it writes. A row that is gone, or
 * soft deleted, has its index row deleted. With embeddings on, a row whose
 * text changed is embedded after the transaction, and its embedding stored
 * only while its text is still what was embedded. Answers what it did.
 */
export async function indexSearch(orm: object, message: SearchIndexing, options: SearchOptions = {}): Promise<'indexed' | 'removed' | 'unknown'> {
  const system = (orm as OrmShape).asSystem();
  const definition = searchSourcesOf(system).get(message.sourceTable)?.definition;
  if (!definition) return 'unknown';
  const d = definition;
  const where = `source_table = $1 AND source_key = $2`;
  const written = await system.transaction(async tx => {
    const columns = [`${d.title} AS title`, ...(d.body ?? []).map((c, i) => `${c} AS b${i}`), `${d.key}::text AS key`];
    if (d.scope) columns.push(`${d.scope}::text AS scope`);
    if (d.deletedAt) columns.push(`${d.deletedAt} IS NOT NULL AS deleted`);
    const row = (await tx.query(`SELECT ${columns.join(', ')} FROM ${d.table} WHERE ${d.key} = $1::${d.keyType} FOR SHARE`, [message.sourceKey])).rows[0];
    if (!row || row.deleted === true) {
      await tx.query(`DELETE FROM ${d.index} WHERE ${where}`, [message.sourceTable, message.sourceKey]);
      return null;
    }
    const title = clean(row.title) ?? '';
    const body = clean((d.body ?? []).map((_, i) => row[`b${i}`]).filter(v => v !== null && v !== undefined).join('\n'));
    let comments: string | null = null;
    if (d.comments) {
      const c = d.comments;
      const read = await tx.query(
        `SELECT string_agg(${c.body}, E'\\n' ORDER BY ${c.order}) AS text FROM ${c.table} WHERE ${c.row} = $1::${d.keyType} AND ${c.state} IN ('posted', 'interrupted') AND ${c.body} <> ''`,
        [message.sourceKey]
      );
      comments = clean(read.rows[0]?.text);
    }
    let files: string | null = null;
    if (d.files) {
      const f = d.files;
      const read = await tx.query(`SELECT string_agg(${f.text}, E'\\n' ORDER BY ${f.text}) AS text FROM ${f.table} WHERE ${f.row} = $1::${d.keyType} AND ${f.state} = 'ready' AND ${f.text} IS NOT NULL`, [
        message.sourceKey,
      ]);
      files = clean(read.rows[0]?.text);
    }
    const text = [title, body, comments, files].filter((t): t is string => t !== null).join('\n');
    const hash = hex(sha256(new TextEncoder().encode(text)));
    const key = String(row.key);
    // The access columns come from the row itself, which the share lock
    // holds as it is until the index row is written.
    const audience = d.audience ? `s.${d.audience.effective.split('.').pop()}` : 'NULL::uuid[]';
    const agents = d.audience ? `s.${d.audience.agents.split('.').pop()}` : `'followRole'`;
    const stored = await tx.query(
      `INSERT INTO ${d.index} AS i (source_table, source_key, title, body, comments, files, scope, audience, agents, text_hash, indexed_at)
       SELECT $1::text, $2::text, $3::text, $4::text, $5::text, $6::text, $7::text, ${audience}, ${agents}, $8::text, now() FROM ${d.table} AS s WHERE s.${d.key} = $9::${d.keyType}
       ON CONFLICT (source_table, source_key) DO UPDATE SET
         title = EXCLUDED.title, body = EXCLUDED.body, comments = EXCLUDED.comments, files = EXCLUDED.files, scope = EXCLUDED.scope,
         audience = EXCLUDED.audience, agents = EXCLUDED.agents, indexed_at = EXCLUDED.indexed_at,
         embedding = CASE WHEN i.text_hash = EXCLUDED.text_hash THEN i.embedding END,
         embedding_model = CASE WHEN i.text_hash = EXCLUDED.text_hash THEN i.embedding_model END,
         text_hash = EXCLUDED.text_hash
       RETURNING embedding_model`,
      [message.sourceTable, key, title, body, comments, files, row.scope ?? null, hash, key]
    );
    return { key, text, hash, model: stored.rows[0]?.embedding_model as string | null | undefined };
  });
  if (written === null) return 'removed';
  const embeddings = options.embeddings;
  if (embeddings && written.model !== embeddings.model) {
    const [vector] = await embeddings.embed([written.text.slice(0, embeddings.maxChars ?? 8000)]);
    if (vector && vector.length > 0) {
      await system.query(`UPDATE ${d.index} SET embedding = $3::float8[], embedding_model = $4 WHERE ${where} AND text_hash = $5`, [
        message.sourceTable,
        written.key,
        unit(vector),
        embeddings.model,
        written.hash,
      ]);
    }
  }
  return 'indexed';
}

/**
 * Builds the index row of every row of orm's @searchable tables, or of
 * tables, as indexSearch does one, in key order, batch rows a read: what a
 * table that became @searchable with rows in it needs once, and what a
 * change of embeddings' model needs. Answers how many rows it indexed.
 */
export async function reindexSearch(orm: object, options: SearchOptions & { readonly tables?: readonly string[]; readonly batch?: number } = {}): Promise<number> {
  const system = (orm as OrmShape).asSystem();
  const batch = options.batch ?? 500;
  let count = 0;
  for (const [table, { definition }] of searchSourcesOf(system)) {
    if (options.tables && !options.tables.includes(table)) continue;
    let after: string | undefined;
    for (;;) {
      const { rows } = await system.query(
        `SELECT ${definition.key}::text AS key, ${definition.key} AS k FROM ${definition.table}${after === undefined ? '' : ` WHERE ${definition.key} > $1::${definition.keyType}`} ORDER BY ${definition.key} LIMIT ${batch}`,
        after === undefined ? [] : [after]
      );
      for (const row of rows) {
        await indexSearch(orm, { sourceTable: table, sourceKey: String(row.key) }, options);
        count++;
      }
      if (rows.length < batch) break;
      after = String(rows[rows.length - 1]!.key);
    }
  }
  return count;
}

/** Search's embeddings over a model provider (D159) that embeds, under model. */
export function searchEmbeddingsOf(provider: { embed?(request: { readonly model?: string; readonly texts: readonly string[] }): Promise<{ readonly vectors: readonly (readonly number[])[] }> }, model: string, maxChars?: number): SearchEmbeddings {
  if (typeof provider.embed !== 'function') throw new TypeError('searchEmbeddingsOf: the model provider does not embed');
  return {
    model,
    ...(maxChars !== undefined ? { maxChars } : {}),
    async embed(texts) {
      return (await provider.embed!({ model, texts })).vectors;
    },
  };
}

// ---- the route and the MCP tool ----

/** What searchRoutes takes. */
export interface SearchRoutesOptions extends SearchOptions {
  readonly orm: object;
  /** Establishes the caller of a search from the request: a session's user, a key's, a grant's party or agent run. */
  readonly authenticate: (request: Request) => Promise<Principal | null>;
  /** The path the route is at; /api/search unless set. */
  readonly base?: string;
}

function problem(status: number, title: string, detail: string): Response {
  return new Response(JSON.stringify({ type: 'about:blank', title, status, detail }), { status, headers: { 'content-type': 'application/problem+json' } });
}

function list(values: readonly string[]): string[] {
  return values.flatMap(value => value.split(',')).filter(Boolean);
}

/**
 * The search route (D155), as a fetch handler the app mounts (it answers
 * null for a request it does not serve):
 *
 * - GET {base}?q&tables&scope&limit&offset: the caller's search, as JSON
 *   { data: SearchResult }; 401 with no caller, 400 for a request it
 *   cannot read.
 */
export function searchRoutes(options: SearchRoutesOptions): (request: Request) => Promise<Response | null> {
  const base = (options.base ?? '/api/search').replace(/\/$/u, '');
  return async (request: Request) => {
    const url = new URL(request.url);
    if (url.pathname !== base) return null;
    if (request.method !== 'GET') return problem(405, 'Method Not Allowed', 'search is a GET');
    const principal = await options.authenticate(request);
    if (!principal) return problem(401, 'Unauthorized', 'Authentication required');
    const tables = list(url.searchParams.getAll('tables'));
    const scope = list(url.searchParams.getAll('scope'));
    const number = (name: string) => (url.searchParams.has(name) ? Number(url.searchParams.get(name)) : undefined);
    const limit = number('limit');
    const offset = number('offset');
    try {
      const result = await searchTables(
        options.orm,
        principal,
        {
          q: url.searchParams.get('q') ?? '',
          ...(tables.length > 0 ? { tables } : {}),
          ...(scope.length > 0 ? { scope } : {}),
          ...(limit !== undefined ? { limit } : {}),
          ...(offset !== undefined ? { offset } : {}),
        },
        options
      );
      return new Response(JSON.stringify({ data: result }), { status: 200, headers: { 'content-type': 'application/json' } });
    } catch (error) {
      if (error instanceof TypeError) return problem(400, 'Bad Request', error.message);
      throw error;
    }
  };
}

/** Search as an MCP tool (D155): what tools/list answers of it. */
export const searchMcpTool = {
  name: 'search',
  title: 'Search',
  description:
    'Finds the rows of every searchable table that hold all the given words, best first, of those the caller may read: each hit names its table and key, with its title and a snippet.',
  inputSchema: {
    type: 'object',
    additionalProperties: false,
    required: ['q'],
    properties: {
      q: { type: 'string', minLength: 1, description: 'The words to find; a row matches when it holds every one.' },
      tables: { type: 'array', items: { type: 'string' }, description: 'Only these tables, by name.' },
      scope: { type: 'array', items: { type: 'string' }, description: "Only rows in this scope, the app's filter: a workspace or a project." },
      limit: { type: 'integer', minimum: 1, maximum: MAX_LIMIT, description: 'At most this many hits; 20 unless set.' },
      offset: { type: 'integer', minimum: 0, maximum: MAX_OFFSET, description: 'Hits to skip, for the next page.' },
    },
  },
  annotations: { readOnlyHint: true, idempotentHint: true },
} as const;

/**
 * Runs a call of searchMcpTool as principal: its result, or its refusal.
 * An agent run calls it as the principal its run token's grant resolves
 * to, so its hits keep to the rows its grants reach and its audience.
 */
export async function callSearchTool(
  orm: object,
  principal: Principal | null,
  args: Readonly<Record<string, unknown>>,
  options: SearchOptions = {}
): Promise<{ readonly result: SearchResult } | { readonly problem: { readonly status: number; readonly title: string; readonly detail: string } }> {
  if (typeof args.q !== 'string') return { problem: { status: 400, title: 'Bad Request', detail: 'q is required' } };
  const strings = (value: unknown) => (Array.isArray(value) ? value.filter((s): s is string => typeof s === 'string') : typeof value === 'string' ? [value] : undefined);
  const tables = strings(args.tables);
  const scope = strings(args.scope);
  try {
    return {
      result: await searchTables(
        orm,
        principal,
        {
          q: args.q,
          ...(tables ? { tables } : {}),
          ...(scope ? { scope } : {}),
          ...(typeof args.limit === 'number' ? { limit: args.limit } : {}),
          ...(typeof args.offset === 'number' ? { offset: args.offset } : {}),
        },
        options
      ),
    };
  } catch (error) {
    if (error instanceof TypeError) return { problem: { status: 400, title: 'Bad Request', detail: error.message } };
    throw error;
  }
}
