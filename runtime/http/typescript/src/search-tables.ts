import type { Principal } from './auth.js';
import { accessCallerOf } from './caller.js';
import { requestIdOf } from './envelope.js';
import { audienceConditions, ormDialect, type PlainRepository } from './orm.js';
import { badRequest, methodNotAllowed, problemResponse, unauthorized, type HttpProblem } from './problem.js';
import { sha256Hex } from './sha256.js';
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

On SQLite and D1 (D186) the index is the same table, its audience a JSON
array (D182), and its words an FTS5 table beside it, search_index_fts,
which the index table's triggers keep in the statement that writes an index
row. A search matches it with each word a quoted phrase of the words FTS5's
unicode61 tokenizer finds in it (folded to lower case, no diacritic removed
and none stemmed, as the simple configuration reads them), every one
required, and only in the four texts; it ranks by bm25 with the same weights
of the title, the body, the conversation and the files (1, 0.4, 0.2, 0.1),
best first, then the table and the key; a snippet is FTS5's, of the first
of the four texts that holds every word, as on Postgres. Access is checked
inside the query as on Postgres, the audience through json_each over the
index row's copy and a grant's reach by an EXISTS back to each table with
its repository's conditions on SQLite. Embeddings are Postgres's alone: a
search or an indexing on SQLite that asks for them is refused. indexSearch
reads the row and writes its index row with the row's access columns in
one statement, so on D1, which holds no transaction open, an audience
change between its reads and its write is still copied as it stands.

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
  /** On SQLite (D186): the FTS5 table of the index's words, quoted, in place of document. */
  readonly fts?: string;
  /** On SQLite: how the index's audience, a JSON array, holds one of a caller's ids, and a parameter (sqlutil.SQLiteAudienceContains). */
  readonly indexAudience?: { readonly contains: string; readonly placeholder: string };
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

/** A repository as search runs it: its definition, and the conditions of its reads, which no generated Repository type has. */
type RepositoryShape = Pick<PlainRepository, 'def'> & {
  conditions(filter: null, args: unknown[], offset: number, write?: boolean): string[];
};

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
  if (ormDialect(db) === 'sqlite') {
    if (request.vector || options.embeddings) refuseEmbeddings('searchTables', request.vector !== undefined);
    return searchOnSQLite(db, principal, words, sources, all, request.scope, limit, offset);
  }
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
  if (request.scope !== undefined) conditions.push(`si.scope = ANY(${param(scopeForms(request.scope))}::text[])`);

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

/** Throws for embeddings on SQLite, which re-rank on Postgres alone (D186): a TypeError for a request's vector, an Error for the options'. */
function refuseEmbeddings(what: string, request: boolean): never {
  const message = `${what}: embeddings re-rank and are stored on Postgres alone; a search on SQLite and D1 ranks by its words (D186)`;
  throw request ? new TypeError(message) : new Error(message);
}

/** The scope values a search keeps to, each as text and, for a UUID, in its database form too. */
function scopeForms(scope: string | readonly string[]): string[] {
  const forms = new Set<string>();
  for (const value of [scope].flat()) {
    if (typeof value !== 'string') throw new TypeError('searchTables: a scope is a text');
    forms.add(value);
    try {
      forms.add(uuidToDb(value));
    } catch {
      // Not a UUID: its text alone.
    }
  }
  return [...forms];
}

/** bm25's weights of the FTS5 table's columns on SQLite: the title, the body, the conversation and the files as Postgres weighs A to D, and none for the row's ref and key (D186). */
const FTS_WEIGHTS = '1.0, 0.4, 0.2, 0.1, 0.0, 0.0, 0.0';
/** The texts a search on SQLite matches, the FTS5 table's first four columns, in the order a hit's matched names them. */
const FTS_TEXTS = ['title', 'body', 'comments', 'files'] as const;

/** A query's words as an FTS5 expression, each a quoted phrase, every one required; none for words with no letter or digit, which match nothing on Postgres either. */
function ftsPhrases(words: readonly string[]): string | null {
  const phrases = words.filter(word => /[\p{L}\p{N}]/u.test(word)).map(word => `"${word.replaceAll('"', '""')}"`);
  return phrases.length === 0 ? null : phrases.join(' ');
}

/**
 * searchTables on SQLite and D1 (D186): the rows whose FTS5 words hold
 * every word, of those principal may read, ranked by bm25, one page of
 * them and how many there are, each with where the words matched first
 * and a snippet of that text.
 */
async function searchOnSQLite(
  db: OrmShape,
  principal: Principal | null,
  words: readonly string[],
  sources: readonly (readonly [string, { readonly definition: SearchableDefinition; readonly repository: RepositoryShape }])[],
  all: ReadonlyMap<string, { readonly definition: SearchableDefinition }>,
  scope: string | readonly string[] | undefined,
  limit: number,
  offset: number
): Promise<SearchResult> {
  const empty: SearchResult = { items: [], total: 0, more: false, reranked: false };
  const phrases = ftsPhrases(words);
  if (phrases === null) return empty;
  const { index, fts = '"search_index_fts"', indexAudience } = sources[0]![1].definition;
  const caller = accessCallerOf(principal);
  const args: unknown[] = [];
  const param = (value: unknown): string => {
    args.push(value);
    return `?${args.length}`;
  };
  const conditions = [`${fts} MATCH ${param(`{${FTS_TEXTS.join(' ')}} : (${phrases})`)}`];
  if (caller.grants === undefined) {
    conditions.push(`si.source_table IN (${sources.map(([table]) => param(table)).join(', ')})`);
  } else {
    const branches: string[] = [];
    for (const [table, { definition, repository }] of sources) {
      const kept = repository.conditions(null, args, args.length);
      if (kept.length === 1 && kept[0] === 'FALSE') continue;
      const name = param(table);
      const match = `${definition.table}.${definition.key} = si.source_key`;
      branches.push(`(si.source_table = ${name} AND EXISTS (SELECT 1 FROM ${definition.table} WHERE ${[match, ...kept].join(' AND ')}))`);
    }
    if (branches.length === 0) return empty;
    conditions.push(`(${branches.join(' OR ')})`);
  }
  conditions.push(
    ...audienceConditions(
      { effective: 'si.audience', agents: 'si.agents', contains: indexAudience?.contains ?? 'EXISTS (SELECT 1 FROM json_each(%s) WHERE json_each.value IN (%s))', placeholder: indexAudience?.placeholder ?? '?%d' },
      caller,
      args,
      args.length
    )
  );
  if (scope !== undefined) conditions.push(`si.scope IN (${scopeForms(scope).map(param).join(', ')})`);
  const sql = `WITH hits AS (
      SELECT si.source_table, si.source_key, si.title, ${fts}.rowid AS fts, bm25(${fts}, ${FTS_WEIGHTS}) AS rank
      FROM ${fts} JOIN ${index} AS si ON si.source_table = ${fts}.source_table AND si.source_key = ${fts}.source_key
      WHERE ${conditions.join(' AND ')}
    ),
    ranked AS (
      SELECT h.*, row_number() OVER (ORDER BY h.rank, h.source_table, h.source_key) AS place FROM hits AS h
    )
    SELECT c.total, p.source_table, p.source_key, p.title, p.fts, p.place
    FROM (SELECT count(*) AS total FROM hits) AS c
    LEFT JOIN (SELECT * FROM ranked ORDER BY place LIMIT ${limit} OFFSET ${offset}) AS p ON 1
    ORDER BY p.place`;
  const system = db.asSystem();
  const { rows } = await system.query(sql, args);
  const total = Number(rows[0]?.total ?? 0);
  const page = rows.filter(row => row.source_table !== null && row.source_table !== undefined);
  // Where the words matched first, and a snippet of each text, for the page.
  const matched = new Map<number, string>();
  const snippets = new Map<number, Values>();
  if (page.length > 0) {
    const ids = page.map(row => Number(row.fts));
    const among = (from: number) => ids.map((_, i) => `?${from + i}`).join(', ');
    const texts: unknown[] = [];
    const branches = FTS_TEXTS.map(text => {
      texts.push(`${text} : (${phrases})`, ...ids);
      const at = texts.length - ids.length;
      return `SELECT '${text}' AS text, rowid AS fts FROM ${fts} WHERE ${fts} MATCH ?${at} AND rowid IN (${among(at + 1)})`;
    });
    for (const row of (await system.query(branches.join(' UNION ALL '), texts)).rows) {
      const id = Number(row.fts);
      const text = String(row.text);
      if (!matched.has(id) || FTS_TEXTS.indexOf(text as never) < FTS_TEXTS.indexOf(matched.get(id) as never)) matched.set(id, text);
    }
    const parts = FTS_TEXTS.map((_, i) => `snippet(${fts}, ${i}, ?2, ?3, '', 24) AS s${i}`).join(', ');
    const read = await system.query(`SELECT rowid AS fts, ${parts} FROM ${fts} WHERE ${fts} MATCH ?1 AND rowid IN (${among(4)})`, [`{${FTS_TEXTS.join(' ')}} : (${phrases})`, OPEN, CLOSE, ...ids]);
    for (const row of read.rows) snippets.set(Number(row.fts), row);
  }
  const items: SearchHit[] = page.map((row, i) => {
    const table = String(row.source_table);
    const key = String(row.source_key);
    const id = Number(row.fts);
    const where = (matched.get(id) ?? 'title') as SearchHit['matched'];
    return {
      table,
      key: all.get(table)?.definition.uuid ? uuidFromDb(key) : key,
      title: String(row.title ?? ''),
      matched: where,
      snippet: snippetParts(String(snippets.get(id)?.[`s${FTS_TEXTS.indexOf(where)}`] ?? '')),
      place: offset + i + 1,
    };
  });
  return { items, total, more: offset + items.length < total, reranked: false };
}

function clean(text: unknown): string | null {
  if (text === null || text === undefined) return null;
  const out = String(text).replace(MARKS, ' ').slice(0, MAX_INDEXED_TEXT);
  return out.trim() === '' ? null : out;
}

/**
 * Builds the index row of the row a @searchable declaration's queue names
 * (D155), as the system, which its worker calls for each message: the
 * row's title, body, scope, conversation and files' text, and its access
 * columns, read under a share lock of the row, so a change of its audience
 * waits for it and copies into the row it writes. A row that is gone, or
 * soft deleted, has its index row deleted. With embeddings on, a row whose
 * text changed is embedded after the transaction, and its embedding stored
 * only while its text is still what was embedded. Answers what it did. It
 * is indexRows of one key, which reindexSearch runs a page of keys at a
 * time.
 */
export async function indexSearch(orm: object, message: SearchIndexing, options: SearchOptions = {}): Promise<'indexed' | 'removed' | 'unknown'> {
  const system = (orm as OrmShape).asSystem();
  const definition = searchSourcesOf(system).get(message.sourceTable)?.definition;
  if (!definition) return 'unknown';
  if (ormDialect(system) === 'sqlite') {
    if (options.embeddings) refuseEmbeddings('indexSearch', false);
    return indexOnSQLite(system, definition, message);
  }
  const indexed = await indexRows(system, message.sourceTable, definition, [message.sourceKey], options);
  return indexed.has(message.sourceKey) ? 'indexed' : 'removed';
}

/**
 * Builds the index rows of keys, rows of the @searchable table table, as
 * indexSearch does one, in as many statements for a page of keys as for
 * one: in one transaction, a read of the rows under a share lock, one of
 * their conversations and one of their files (string_agg, GROUP BY the
 * row), a delete of the index rows of the rows gone or soft deleted and an
 * upsert of the others; then, with embeddings on, one model call with the
 * texts of the rows whose embedding is not of the model, and one update of
 * their embeddings. Each key is deleted by the text it is given in, and
 * indexed by its row's key's text. Answers the keys that had a row to
 * index.
 */
async function indexRows(system: OrmShape, table: string, d: SearchableDefinition, keys: readonly string[], options: SearchOptions): Promise<Set<string>> {
  const given = [...new Set(keys)];
  const indexed = new Set<string>();
  const written = await system.transaction(async tx => {
    const columns = [`${d.title} AS title`, ...(d.body ?? []).map((c, i) => `${c} AS b${i}`), `${d.key}::text AS key`];
    if (d.scope) columns.push(`${d.scope}::text AS scope`);
    if (d.deletedAt) columns.push(`${d.deletedAt} IS NOT NULL AS deleted`);
    // Each key's row, locked in the keys' order, beside the key as given;
    // a key with no row has none.
    const { rows } = await tx.query(
      `SELECT given.key AS given, r.* FROM unnest($1::text[]) WITH ORDINALITY AS given(key, n)
       CROSS JOIN LATERAL (SELECT ${columns.join(', ')} FROM ${d.table} WHERE ${d.key} = given.key::${d.keyType} FOR SHARE) AS r ORDER BY given.n`,
      [given]
    );
    const live: Values[] = [];
    const seen = new Set<string>();
    for (const row of rows) {
      if (row.deleted === true) continue;
      indexed.add(String(row.given));
      // Two keys given of one row index it once.
      const key = String(row.key);
      if (!seen.has(key)) {
        seen.add(key);
        live.push(row);
      }
    }
    const removed = given.filter(k => !indexed.has(k));
    if (removed.length > 0) await tx.query(`DELETE FROM ${d.index} WHERE source_table = $1 AND source_key = ANY($2::text[])`, [table, removed]);
    if (live.length === 0) return [];
    const liveKeys = live.map(row => String(row.key));
    // The conversations' and the files' text, of every row at once.
    const aggregate = async (sql: string): Promise<Map<string, unknown>> => new Map((await tx.query(sql, [liveKeys])).rows.map(r => [String(r.key), r.text]));
    const c = d.comments;
    const comments = c
      ? await aggregate(
          `SELECT ${c.row}::text AS key, string_agg(${c.body}, E'\\n' ORDER BY ${c.order}) AS text FROM ${c.table} WHERE ${c.row} = ANY($1::text[]::${d.keyType}[]) AND ${c.state} IN ('posted', 'interrupted') AND ${c.body} <> '' GROUP BY ${c.row}`
        )
      : undefined;
    const f = d.files;
    const files = f
      ? await aggregate(
          `SELECT ${f.row}::text AS key, string_agg(${f.text}, E'\\n' ORDER BY ${f.text}) AS text FROM ${f.table} WHERE ${f.row} = ANY($1::text[]::${d.keyType}[]) AND ${f.state} = 'ready' AND ${f.text} IS NOT NULL GROUP BY ${f.row}`
        )
      : undefined;
    const writes = live.map((row, i) => {
      const key = liveKeys[i]!;
      const title = clean(row.title) ?? '';
      const body = clean((d.body ?? []).map((_, j) => row[`b${j}`]).filter(v => v !== null && v !== undefined).join('\n'));
      const comment = comments ? clean(comments.get(key)) : null;
      const file = files ? clean(files.get(key)) : null;
      const text = [title, body, comment, file].filter((t): t is string => t !== null).join('\n');
      const hash = sha256Hex(text);
      return { key, title, body, comment, file, scope: (row.scope ?? null) as unknown, text, hash, model: undefined as string | null | undefined };
    });
    // The access columns come from the row itself, which the share lock
    // holds as it is until the index row is written.
    const audience = d.audience ? `s.${d.audience.effective.split('.').pop()}` : 'NULL::uuid[]';
    const agents = d.audience ? `s.${d.audience.agents.split('.').pop()}` : `'followRole'`;
    const stored = await tx.query(
      `INSERT INTO ${d.index} AS i (source_table, source_key, title, body, comments, files, scope, audience, agents, text_hash, indexed_at)
       SELECT $1::text, v.key, v.title, v.body, v.comments, v.files, v.scope, ${audience}, ${agents}, v.hash, now()
       FROM unnest($2::text[], $3::text[], $4::text[], $5::text[], $6::text[], $7::text[], $8::text[]) AS v(key, title, body, comments, files, scope, hash), ${d.table} AS s
       WHERE s.${d.key} = v.key::${d.keyType}
       ON CONFLICT (source_table, source_key) DO UPDATE SET
         title = EXCLUDED.title, body = EXCLUDED.body, comments = EXCLUDED.comments, files = EXCLUDED.files, scope = EXCLUDED.scope,
         audience = EXCLUDED.audience, agents = EXCLUDED.agents, indexed_at = EXCLUDED.indexed_at,
         embedding = CASE WHEN i.text_hash = EXCLUDED.text_hash THEN i.embedding END,
         embedding_model = CASE WHEN i.text_hash = EXCLUDED.text_hash THEN i.embedding_model END,
         text_hash = EXCLUDED.text_hash
       RETURNING source_key, embedding_model`,
      [
        table,
        liveKeys,
        writes.map(w => w.title),
        writes.map(w => w.body),
        writes.map(w => w.comment),
        writes.map(w => w.file),
        writes.map(w => w.scope),
        writes.map(w => w.hash),
      ]
    );
    const byKey = new Map(writes.map(w => [w.key, w]));
    for (const r of stored.rows) {
      const w = byKey.get(String(r.source_key));
      if (w) w.model = r.embedding_model as string | null | undefined;
    }
    return writes;
  });
  const embeddings = options.embeddings;
  if (!embeddings) return indexed;
  const pending = written.filter(w => w.model !== embeddings.model);
  if (pending.length === 0) return indexed;
  const vectors = await embeddings.embed(pending.map(w => w.text.slice(0, embeddings.maxChars ?? 8000)));
  // Each embedding as an array literal, which unnest keeps whole; a row
  // the model gave none keeps none, and an embedding that is not finite
  // stops the rows after it, as one at a time would.
  const embedded: { key: string; hash: string; literal: string }[] = [];
  let failed: unknown;
  for (const [i, w] of pending.entries()) {
    const vector = vectors[i];
    if (!vector || vector.length === 0) continue;
    try {
      embedded.push({ key: w.key, hash: w.hash, literal: `{${unit(vector).join(',')}}` });
    } catch (error) {
      failed = error;
      break;
    }
  }
  if (embedded.length > 0) {
    await system.query(
      `UPDATE ${d.index} AS i SET embedding = v.embedding::float8[], embedding_model = $2
       FROM unnest($3::text[], $4::text[], $5::text[]) AS v(key, hash, embedding)
       WHERE i.source_table = $1 AND i.source_key = v.key AND i.text_hash = v.hash`,
      [table, embeddings.model, embedded.map(e => e.key), embedded.map(e => e.hash), embedded.map(e => e.literal)]
    );
  }
  if (failed !== undefined) throw failed;
  return indexed;
}

/**
 * indexSearch on SQLite and D1 (D186): the row read by its key, its
 * conversation's and files' text in their order, and its index row written
 * with the row's access columns, read by the write itself, or deleted for a
 * row that is gone or soft deleted. The index table's triggers write its
 * words into the FTS5 table in the same statement.
 */
async function indexOnSQLite(system: OrmShape, d: SearchableDefinition, message: SearchIndexing): Promise<'indexed' | 'removed'> {
  const written = await system.transaction(async tx => {
    const columns = [`${d.title} AS title`, ...(d.body ?? []).map((c, i) => `${c} AS b${i}`), `CAST(${d.key} AS TEXT) AS key`];
    if (d.scope) columns.push(`CAST(${d.scope} AS TEXT) AS scope`);
    if (d.deletedAt) columns.push(`${d.deletedAt} IS NOT NULL AS deleted`);
    const row = (await tx.query(`SELECT ${columns.join(', ')} FROM ${d.table} WHERE ${d.key} = ?1`, [message.sourceKey])).rows[0];
    if (!row || Number(row.deleted ?? 0) === 1) {
      await tx.query(`DELETE FROM ${d.index} WHERE source_table = ?1 AND source_key = ?2`, [message.sourceTable, message.sourceKey]);
      return null;
    }
    const title = clean(row.title) ?? '';
    const body = clean((d.body ?? []).map((_, i) => row[`b${i}`]).filter(v => v !== null && v !== undefined).join('\n'));
    let comments: string | null = null;
    if (d.comments) {
      const c = d.comments;
      const read = await tx.query(
        `SELECT group_concat(text, char(10)) AS text FROM (SELECT ${c.body} AS text FROM ${c.table} WHERE ${c.row} = ?1 AND ${c.state} IN ('posted', 'interrupted') AND ${c.body} <> '' ORDER BY ${c.order})`,
        [message.sourceKey]
      );
      comments = clean(read.rows[0]?.text);
    }
    let files: string | null = null;
    if (d.files) {
      const f = d.files;
      const read = await tx.query(`SELECT group_concat(text, char(10)) AS text FROM (SELECT ${f.text} AS text FROM ${f.table} WHERE ${f.row} = ?1 AND ${f.state} = 'ready' AND ${f.text} IS NOT NULL ORDER BY ${f.text})`, [
        message.sourceKey,
      ]);
      files = clean(read.rows[0]?.text);
    }
    const text = [title, body, comments, files].filter((t): t is string => t !== null).join('\n');
    const hash = sha256Hex(text);
    const key = String(row.key);
    const audience = d.audience ? `s.${d.audience.effective.split('.').pop()}` : 'NULL';
    const agents = d.audience ? `s.${d.audience.agents.split('.').pop()}` : `'followRole'`;
    await tx.query(
      `INSERT INTO ${d.index} (source_table, source_key, title, body, comments, files, scope, audience, agents, text_hash, indexed_at)
       SELECT ?1, ?2, ?3, ?4, ?5, ?6, ?7, ${audience}, ${agents}, ?8, strftime('%Y-%m-%dT%H:%M:%fZ', 'now') FROM ${d.table} AS s WHERE s.${d.key} = ?9
       ON CONFLICT (source_table, source_key) DO UPDATE SET
         title = excluded.title, body = excluded.body, comments = excluded.comments, files = excluded.files, scope = excluded.scope,
         audience = excluded.audience, agents = excluded.agents, indexed_at = excluded.indexed_at,
         embedding = CASE WHEN ${d.index}.text_hash = excluded.text_hash THEN ${d.index}.embedding END,
         embedding_model = CASE WHEN ${d.index}.text_hash = excluded.text_hash THEN ${d.index}.embedding_model END,
         text_hash = excluded.text_hash`,
      [message.sourceTable, key, title, body, comments, files, row.scope ?? null, hash, message.sourceKey]
    );
    return key;
  });
  return written === null ? 'removed' : 'indexed';
}

/**
 * Builds the index row of every row of orm's @searchable tables, or of
 * tables, as indexSearch does one, in key order, batch rows a read and an
 * indexRows: what a table that became @searchable with rows in it needs
 * once, and what a change of embeddings' model needs. Answers how many
 * rows it indexed.
 */
export async function reindexSearch(orm: object, options: SearchOptions & { readonly tables?: readonly string[]; readonly batch?: number } = {}): Promise<number> {
  const system = (orm as OrmShape).asSystem();
  const batch = options.batch ?? 500;
  let count = 0;
  const sqlite = ormDialect(system) === 'sqlite';
  for (const [table, { definition }] of searchSourcesOf(system)) {
    if (options.tables && !options.tables.includes(table)) continue;
    let after: unknown;
    for (;;) {
      // On SQLite the next page starts after the key as the table holds it.
      const { rows } = await system.query(
        sqlite
          ? `SELECT CAST(${definition.key} AS TEXT) AS key, ${definition.key} AS k FROM ${definition.table}${after === undefined ? '' : ` WHERE ${definition.key} > ?1`} ORDER BY ${definition.key} LIMIT ${batch}`
          : `SELECT ${definition.key}::text AS key, ${definition.key} AS k FROM ${definition.table}${after === undefined ? '' : ` WHERE ${definition.key} > $1::${definition.keyType}`} ORDER BY ${definition.key} LIMIT ${batch}`,
        after === undefined ? [] : [after]
      );
      // A page's rows are indexed together: as many statements and one
      // model call whatever its size.
      if (rows.length > 0) {
        // On SQLite each row is indexed by its own statements (indexOnSQLite).
        if (sqlite) for (const row of rows) await indexSearch(orm, { sourceTable: table, sourceKey: String(row.key) }, options);
        else await indexRows(system, table, definition, rows.map(row => String(row.key)), options);
        count += rows.length;
      }
      if (rows.length < batch) break;
      const last = rows[rows.length - 1]!;
      after = sqlite ? last.k : String(last.key);
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

/** The refusal of a request, through the shared problem envelope, which carries its requestId and code. */
function problem(request: Request, refusal: HttpProblem): Response {
  return problemResponse(refusal, requestIdOf(request));
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
    if (request.method !== 'GET') return problem(request, methodNotAllowed('search is a GET'));
    const principal = await options.authenticate(request);
    if (!principal) return problem(request, unauthorized());
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
      if (error instanceof TypeError) return problem(request, badRequest(error.message));
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
