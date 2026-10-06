/*
Search, the core's full-text and vector search (D16). The config names the
type's own top-level text fields to index, a string or a scalar whose
values are strings each, at most 16, optionally a weight for each, and
optionally vectors: their dimensions, a label for the model they come
from and the permission an embedder needs to settle them. The index is
one FTS5 table the behavior owns, with a column per indexed field in the
config's order, beside a table that gives each instance's row its
namespace, schema and id, and with vectors, the hash of the text its
vector is computed from and the vector. afterChange keeps both in the
transaction of every create, update, delete and operation that changes an
indexed field, so a search never sees a stale row.

search is schema-level and read-only. It answers only a caller who may
read the schema (the policy is asked for read on it, beside the read with
the operation's name every schema-level call asks), and only from the
instances of the caller's namespace: the namespace and the schema are
conditions of the query itself. A query is plain words by default: each
whitespace-separated word goes to FTS5 as a quoted string, so quotes,
operators and column names in it are text, and an instance matches when
its indexed fields hold every word. syntax: fts5 passes the query through
as an FTS5 expression, without column filters; one FTS5 cannot parse is
invalid_argument. Results come best first, by bm25 with the config's
weights, then in the order the instances were first indexed; rank is the
place in that order. Each carries a snippet of the indexed field with the
most matches.

The FTS5 table is shared by every schema and namespace in the file, so
bm25's statistics (how many rows hold a term, how long a field is on
average) are taken over all of them. The ids and snippets a search returns
are always the caller's own, but the order among them can shift with
another namespace's text, which is why a text hit carries its place and
not bm25's score.

Vectors (D16, amended: search's vectors). The engine calls no embedding
provider and loads no SQLite extension. An outside embedder pulls the
instances whose text has no vector yet (staleEmbeddings: each id, the
indexed fields' text joined by a blank line, and the SHA-256 of that text,
the model and the dimensions), computes the vectors, and settles them
(settleEmbeddings) with the hash it pulled. A settle stores a vector only
where the hash still holds: a write of the text since, or a version that
changes the model or the dimensions, moves the hash and clears the vector,
and a settle for it is skipped. A vector is stored as little-endian 32-bit
floats, scaled to unit length, so a cosine is a dot product. A settle
appends no event: no read of an instance shows a vector, and the text and
the model it is computed from are what the log records; it records who
settled and when beside the vector.

A search with a vector ranks the instances whose vectors are settled by
cosine similarity to it, in a scan of the schema's vectors in the
namespace (vectorIndex: a brute-force scan behind an interface an
approximate index could replace). With a query too, the two rankings, the
first RRF_WINDOW of each, are fused by reciprocal rank fusion: a hit
scores 1 / (RRF_K + its place) in each ranking it is in, and the scores
add. Such hits carry the score, their place in each ranking and their
cosine similarity, so a client can explain the order. similar ranks the
instances nearest to one by its own vector and a full-text search for its
longest words, fused the same way, leaving it out.

searchSchemas is the engine-level search across a namespace's schemas
(engine.search, the HTTP route and the MCP tool): it runs search on every
schema the namespace reaches that composes Search and that the caller may
read, each as the caller, skipping one the policy refuses, and fuses the
schemas' rankings the same way.

afterConfigChange rebuilds a namespace's index of the schema when the
indexed fields change or the behavior is added (a first version
included), and drops it when the behavior is removed; a change of weights
alone needs neither, since they apply when a search runs. A version that
adds vectors, or changes their model or dimensions, hashes every
instance's text and clears every vector; one that removes them clears the
hashes and the vectors. Each runs in the publish's transaction and holds
the write lock until it has visited every instance of the schema.
*/

import { createHash } from 'node:crypto';

import { Ajv2020, type ValidateFunction } from 'ajv/dist/2020.js';

import type { Principal } from '../../access.js';
import { EngineError, OperationParamsError, type SchemaIssue } from '../../errors.js';
import type { InstanceStore } from '../../instances/store.js';
import type { SchemaRegistry } from '../../registry/registry.js';
import { SqliteError, type Row, type SqlValue } from '../../storage/driver.js';
import { BehaviorConfigError, defineBehavior, type FrozenJSON, type SchemaContext, type TableReader, type TableWriter } from '../behavior.js';
import { deepFreeze, jsonCopy } from '../json.js';
import { page, pageRequest, type Page } from '../paging.js';
import { BehaviorRegistry } from '../registry.js';
import declaration from './declarations/Search.behavior.json' with { type: 'json' };

/** Search's vectors, as its config gives them. */
export interface SearchVectors {
  /** How many numbers a vector holds. */
  readonly dimensions: number;
  /** The label of the model the vectors come from, which the engine only compares. */
  readonly model: string;
  /** The permission an embedder needs to settle vectors. */
  readonly permission: string;
}

/** Search's config, parsed. */
export interface SearchConfig {
  /** The indexed fields, by JSON key, in the config's order: the index's columns. */
  readonly fields: readonly string[];
  /** Each indexed field's weight, in the order of fields; 1 for one the config does not weigh. */
  readonly weights: readonly number[];
  /** The vectors it keeps; absent when it keeps none. */
  readonly vectors?: SearchVectors;
}

/** One part of a snippet: text, and whether it is a match. */
export interface SnippetPart {
  readonly text: string;
  readonly match: boolean;
}

/** One instance a search returns. */
export interface SearchHit {
  readonly id: string;
  /** The instance's place in the ranking, from 1, across pages. */
  readonly rank: number;
  /**
   * With a vector: the fused score, 1 / (RRF_K + its place) summed over
   * the rankings it is in, which orders the hits.
   */
  readonly score?: number;
  /** The indexed field the snippet is from. */
  readonly field?: string;
  readonly snippet?: readonly SnippetPart[];
  /** With a vector: its place in the full-text ranking, when the query matched it. */
  readonly text?: { readonly rank: number };
  /** With a vector: its place in the vector ranking and its vector's cosine similarity to the one searched for. */
  readonly vector?: { readonly rank: number; readonly similarity: number };
}

/** One hit of a search across a namespace's schemas (searchSchemas). */
export interface SchemaSearchHit extends SearchHit {
  /** The schema the instance is of. */
  readonly schema: string;
  readonly score: number;
}

/** Reciprocal rank fusion's constant: a hit scores 1 / (RRF_K + its place) in each ranking it is in. */
export const RRF_K = 60;

/**
 * How far into each ranking fusion reads: a search with both a query and a
 * vector, similar, and a search across schemas fuse the first RRF_WINDOW
 * of each ranking they fuse.
 */
export const RRF_WINDOW = 200;

/** The words of an instance's text that similar searches for, at most: its longest. */
export const SIMILAR_TERMS = 32;

const NAME = 'Search';

// How many fields one schema indexes at most: the index table's columns.
const MAX_FIELDS = 16;

const COLUMNS = Array.from({ length: MAX_FIELDS }, (_, index) => `f${index + 1}`);

// The marks a snippet's matches are wrapped in. Indexed text has them
// replaced by spaces, so a snippet's parts read back exactly.
const OPEN = '\u0002';
const CLOSE = '\u0003';
const MARKS = /[\u0000\u0002\u0003]/g;

/** The words a snippet holds at most. */
const SNIPPET_WORDS = 12;

// The shortest word similar searches for: shorter ones are mostly
// function words, each of which matches nearly every row.
const MIN_TERM_LENGTH = 3;

// How many stored vectors the scan reads per statement, so a schema's
// vectors are never all in memory at once.
const SCAN_BATCH = 512;

// What an embedder's text joins the indexed fields with.
const JOINER = '\n\n';

// FTS5's own refusals of an expression: its syntax, its depth, a special
// query, and a column exclusion (`-word`), which names a column.
const QUERY_ERROR = /^(fts5[: ]|unterminated string|expected integer|unknown special query|no such column)/;

function key(namespace: string, schema: string, id: string): [string, string, string] {
  return [namespace, schema, id];
}

// isText holds a field's JSON Schema to a string, required or not, that is
// no enum.
function isText(schema: unknown): boolean {
  if (typeof schema !== 'object' || schema === null) {
    return false;
  }
  const { type, enum: values } = schema as { type?: unknown; enum?: unknown };
  if (values !== undefined) {
    return false;
  }
  return type === 'string' || (Array.isArray(type) && type.length === 2 && type[0] === 'string' && type[1] === 'null');
}

function textOf(value: unknown): string | null {
  return typeof value === 'string' ? value.replace(MARKS, ' ') : null;
}

function sameFields(a: SearchConfig, b: SearchConfig): boolean {
  return a.fields.length === b.fields.length && a.fields.every((field, index) => b.fields[index] === field);
}

// sameVectors reports whether two configs' vectors are computed alike; the
// permission only says who settles them.
function sameVectors(a: SearchVectors | undefined, b: SearchVectors | undefined): boolean {
  return a === undefined || b === undefined ? a === b : a.model === b.model && a.dimensions === b.dimensions;
}

// changed reports whether an indexed field differs between two versions of
// an instance's own fields.
function changed(config: SearchConfig, before: FrozenJSON, after: FrozenJSON): boolean {
  return config.fields.some((field) => textOf(before[field]) !== textOf(after[field]));
}

// texts is an instance's indexed fields as the index holds them.
function texts(config: SearchConfig, data: FrozenJSON): Array<string | null> {
  return config.fields.map((field) => textOf(data[field]));
}

// composed is the text a vector is computed from: the indexed fields that
// hold more than whitespace, in the config's order, joined by a blank line.
function composed(values: ReadonlyArray<SqlValue | undefined>): string {
  return values.filter((value): value is string => typeof value === 'string' && value.trim() !== '').join(JOINER);
}

// sourceHash is the hash a vector is computed under: of the text, the
// model and the dimensions. Text that holds nothing wants no vector.
function sourceHash(vectors: SearchVectors, text: string): string | null {
  return text === '' ? null : createHash('sha256').update(JSON.stringify([vectors.model, vectors.dimensions, text]), 'utf8').digest('hex');
}

// track records the hash of an instance's text under the config's vectors,
// and clears its vector when the hash moves.
function track(sql: TableWriter, vectors: SearchVectors, namespace: string, schema: string, id: string, values: ReadonlyArray<string | null>): void {
  const hash = sourceHash(vectors, composed(values));
  sql.run(
    `UPDATE ${sql.table('rows')} SET source_hash = ?, vector = NULL, settled_at = NULL, settled_by = NULL
     WHERE namespace = ? AND schema = ? AND id = ? AND source_hash IS NOT ?`,
    [hash, ...key(namespace, schema, id), hash]
  );
}

// index writes an instance's indexed fields, replacing what the index held
// for it, and with vectors the hash of its text.
function index(sql: TableWriter, config: SearchConfig, namespace: string, schema: string, id: string, data: FrozenJSON): void {
  const rows = sql.table('rows');
  const text = sql.table('text');
  sql.run(`INSERT INTO ${rows} (namespace, schema, id) VALUES (?, ?, ?) ON CONFLICT (namespace, schema, id) DO NOTHING`, key(namespace, schema, id));
  const row = Number((sql.get(`SELECT row FROM ${rows} WHERE namespace = ? AND schema = ? AND id = ?`, key(namespace, schema, id)) as Row).row);
  sql.run(`DELETE FROM ${text} WHERE rowid = ?`, [row]);
  const columns = config.fields.map((_, position) => COLUMNS[position]);
  const values = texts(config, data);
  sql.run(`INSERT INTO ${text} (rowid, ${columns.join(', ')}) VALUES (?, ${columns.map(() => '?').join(', ')})`, [row, ...values]);
  if (config.vectors !== undefined) {
    track(sql, config.vectors, namespace, schema, id, values);
  }
}

function unindex(sql: TableWriter, namespace: string, schema: string, id: string): void {
  const rows = sql.table('rows');
  const found = sql.get(`SELECT row FROM ${rows} WHERE namespace = ? AND schema = ? AND id = ?`, key(namespace, schema, id));
  if (found) {
    sql.run(`DELETE FROM ${sql.table('text')} WHERE rowid = ?`, [Number(found.row)]);
    sql.run(`DELETE FROM ${rows} WHERE row = ?`, [Number(found.row)]);
  }
}

// clear drops a namespace's index of a schema, its vectors with it.
function clear(sql: TableWriter, namespace: string, schema: string): void {
  const rows = sql.table('rows');
  sql.run(`DELETE FROM ${sql.table('text')} WHERE rowid IN (SELECT row FROM ${rows} WHERE namespace = ? AND schema = ?)`, [namespace, schema]);
  sql.run(`DELETE FROM ${rows} WHERE namespace = ? AND schema = ?`, [namespace, schema]);
}

// forget drops a namespace's vectors of a schema and their hashes.
function forget(sql: TableWriter, namespace: string, schema: string): void {
  sql.run(`UPDATE ${sql.table('rows')} SET source_hash = NULL, vector = NULL, settled_at = NULL, settled_by = NULL WHERE namespace = ? AND schema = ?`, [
    namespace,
    schema,
  ]);
}

// expression turns a query into the FTS5 expression it searches with, or
// undefined for a query with nothing to search for.
function expression(query: string, syntax: string): string | undefined {
  const cleaned = query.replace(/\u0000/g, ' ');
  if (cleaned.trim() === '') {
    return undefined;
  }
  if (syntax === 'fts5') {
    // A column filter would name the index's columns, not the type's
    // fields; outside a string, ':', '{' and '}' only start one.
    let quoted = false;
    for (const character of cleaned) {
      if (character === '"') {
        quoted = !quoted;
      } else if (!quoted && (character === ':' || character === '{' || character === '}')) {
        throw new OperationParamsError(NAME, 'search', [
          { path: '/query', message: 'holds a column filter, which search does not take: a query searches every indexed field' },
        ]);
      }
    }
    return cleaned;
  }
  return cleaned
    .split(/\s+/u)
    .filter((word) => word !== '')
    .map(quote)
    .join(' ');
}

function quote(word: string): string {
  return `"${word.replace(/"/g, '""')}"`;
}

// likeness is the FTS5 expression similar searches with: any of the
// instance's SIMILAR_TERMS longest distinct words of at least
// MIN_TERM_LENGTH characters, among equally long ones the more frequent,
// then the earlier. bm25 weighs a rarer word more, and a longer word is
// mostly a rarer one. Undefined when the text holds no such word.
function likeness(text: string): string | undefined {
  const words = new Map<string, { length: number; count: number; first: number }>();
  let position = 0;
  for (const match of text.toLowerCase().matchAll(/[\p{L}\p{N}]+/gu)) {
    const word = match[0];
    const length = [...word].length;
    if (length >= MIN_TERM_LENGTH) {
      const seen = words.get(word);
      if (seen) {
        seen.count += 1;
      } else {
        words.set(word, { length, count: 1, first: position });
      }
    }
    position += 1;
  }
  const chosen = [...words.entries()]
    .sort(([, a], [, b]) => b.length - a.length || b.count - a.count || a.first - b.first)
    .slice(0, SIMILAR_TERMS)
    .map(([word]) => quote(word));
  return chosen.length === 0 ? undefined : chosen.join(' OR ');
}

// snippetOf reads a snippet FTS5 wrote with OPEN and CLOSE around each match.
function snippetOf(value: SqlValue | undefined): SnippetPart[] {
  const parts: SnippetPart[] = [];
  let text = '';
  let match = false;
  for (const character of typeof value === 'string' ? value : '') {
    if (character === OPEN || character === CLOSE) {
      if (text !== '') {
        parts.push({ text, match });
      }
      text = '';
      match = character === OPEN;
    } else {
      text += character;
    }
  }
  if (text !== '') {
    parts.push({ text, match });
  }
  return parts;
}

// snippet is a text match's snippet: that of the indexed field with the
// most matches, the earliest in the config on a tie; none without a match.
function snippet(row: Row | undefined, fields: readonly string[]): { field?: string; snippet?: SnippetPart[] } {
  let best: { field: string; parts: SnippetPart[]; matches: number } | undefined;
  fields.forEach((field, position) => {
    const parts = snippetOf(row?.[`s${position}`]);
    const matches = parts.filter((part) => part.match).length;
    if (matches > 0 && (best === undefined || matches > best.matches)) {
      best = { field, parts, matches };
    }
  });
  return best === undefined ? {} : { field: best.field, snippet: best.parts };
}

// matches runs a full-text match over a namespace's index of a schema:
// count rows from offset in rank order, each with its id, its row and a
// snippet per field, leaving out the row exclude names.
function matches(
  sql: TableReader,
  config: SearchConfig,
  namespace: string,
  schema: string,
  match: string,
  syntax: string,
  count: number,
  offset: number,
  exclude?: number
): Row[] {
  const text = sql.table('text');
  const snippets = config.fields.map((_, position) => `snippet(${text}, ${position}, ?, ?, '...', ${SNIPPET_WORDS}) AS s${position}`);
  const weights = COLUMNS.map((_, position) => config.weights[position] ?? 0);
  // CROSS JOIN keeps the full-text match as the outer loop: some SQLite
  // builds would otherwise walk the schema's rows and run the match once
  // for each of them.
  try {
    return sql.all(
      `SELECT r.id AS id, r.row AS row, ${snippets.join(', ')}
       FROM ${text} CROSS JOIN ${sql.table('rows')} AS r ON r.row = ${text}.rowid
       WHERE ${text} MATCH ? AND r.namespace = ? AND r.schema = ? AND r.row IS NOT ?
       ORDER BY bm25(${text}, ${weights.map(() => '?').join(', ')}), r.row
       LIMIT ? OFFSET ?`,
      [...config.fields.flatMap(() => [OPEN, CLOSE]), match, namespace, schema, exclude ?? null, ...weights, count, offset]
    );
  } catch (error) {
    if (syntax === 'fts5' && error instanceof SqliteError && QUERY_ERROR.test(error.message)) {
      throw new OperationParamsError(NAME, 'search', [{ path: '/query', message: `is not an FTS5 expression this index takes: ${error.message}` }]);
    }
    throw error;
  }
}

// encode stores a vector as little-endian 32-bit floats.
function encode(vector: Float32Array): Uint8Array {
  const bytes = new Uint8Array(vector.length * 4);
  const view = new DataView(bytes.buffer);
  vector.forEach((value, position) => view.setFloat32(position * 4, value, true));
  return bytes;
}

// decode reads a stored vector; undefined for none.
function decode(value: SqlValue | undefined): Float32Array | undefined {
  if (!(value instanceof Uint8Array) || value.byteLength === 0 || value.byteLength % 4 !== 0) {
    return undefined;
  }
  const view = new DataView(value.buffer, value.byteOffset, value.byteLength);
  const vector = new Float32Array(value.byteLength / 4);
  for (let position = 0; position < vector.length; position += 1) {
    vector[position] = view.getFloat32(position * 4, true);
  }
  return vector;
}

// unit holds a vector to the config's dimensions and to values a 32-bit
// float holds, and scales it to unit length; a string says why it cannot.
function unit(values: readonly number[], dimensions: number): Float32Array | string {
  if (values.length !== dimensions) {
    return `holds ${values.length} numbers, and the config's vectors hold ${dimensions}`;
  }
  const vector = Float32Array.from(values);
  const at = vector.findIndex((value) => !Number.isFinite(value));
  if (at >= 0) {
    return `holds ${String(values[at])} at ${at}, which a 32-bit float cannot hold`;
  }
  let sum = 0;
  for (const value of vector) {
    sum += value * value;
  }
  const norm = Math.sqrt(sum);
  if (norm === 0) {
    return 'is all zeros, which has no direction to compare';
  }
  for (let position = 0; position < vector.length; position += 1) {
    vector[position] /= norm;
  }
  return vector;
}

/** A stored vector near a query: its instance, its row and its cosine similarity. */
interface Neighbour {
  readonly id: string;
  readonly row: number;
  readonly similarity: number;
}

/**
 * Ranks a namespace's stored vectors of a schema against a unit vector,
 * most similar first, then in the order the instances were first indexed,
 * leaving out a row. The scan is the one built; an approximate index could
 * stand behind the same call, at the cost of an exact ranking.
 */
interface VectorIndex {
  nearest(sql: TableReader, namespace: string, schema: string, query: Float32Array, count: number, exclude?: number): Neighbour[];
}

// scan is the brute-force VectorIndex: it reads every settled vector of the
// schema in the namespace, SCAN_BATCH at a time through an index, and keeps
// each one's similarity.
const scan: VectorIndex = {
  nearest(sql, namespace, schema, query, count, exclude) {
    const all: Neighbour[] = [];
    let after = 0;
    for (;;) {
      const batch = sql.all(
        `SELECT row, id, vector FROM ${sql.table('rows')}
         WHERE namespace = ? AND schema = ? AND vector IS NOT NULL AND row > ? ORDER BY row LIMIT ?`,
        [namespace, schema, after, SCAN_BATCH]
      );
      for (const found of batch) {
        const row = Number(found.row);
        const vector = row === exclude ? undefined : decode(found.vector);
        if (vector !== undefined && vector.length === query.length) {
          let dot = 0;
          for (let position = 0; position < vector.length; position += 1) {
            dot += vector[position] * query[position];
          }
          all.push({ id: String(found.id), row, similarity: dot });
        }
      }
      if (batch.length < SCAN_BATCH) {
        break;
      }
      after = Number(batch[batch.length - 1].row);
    }
    all.sort((a, b) => b.similarity - a.similarity || a.row - b.row);
    return all.slice(0, count);
  },
};

const vectorIndex: VectorIndex = scan;

/** A hit as fusion ranks it. */
interface Fused {
  readonly id: string;
  readonly row: number;
  score: number;
  textRank?: number;
  textRow?: Row;
  vectorRank?: number;
  similarity?: number;
}

// fuse merges a full-text ranking and a vector ranking by reciprocal rank
// fusion: the higher score first, then the better place in the text
// ranking, then in the vector ranking, then the earlier indexed.
function fuse(text: readonly Row[], near: readonly Neighbour[]): Fused[] {
  const byId = new Map<string, Fused>();
  text.forEach((row, position) => {
    byId.set(String(row.id), { id: String(row.id), row: Number(row.row), score: 1 / (RRF_K + position + 1), textRank: position + 1, textRow: row });
  });
  near.forEach((neighbour, position) => {
    const fused = byId.get(neighbour.id) ?? { id: neighbour.id, row: neighbour.row, score: 0 };
    fused.score += 1 / (RRF_K + position + 1);
    fused.vectorRank = position + 1;
    fused.similarity = neighbour.similarity;
    byId.set(neighbour.id, fused);
  });
  const last = Number.POSITIVE_INFINITY;
  return [...byId.values()].sort(
    (a, b) =>
      b.score - a.score || (a.textRank ?? last) - (b.textRank ?? last) || (a.vectorRank ?? last) - (b.vectorRank ?? last) || a.row - b.row
  );
}

// textPage pages a full-text ranking read from offset after, with one row
// more than the page.
function textPage(rows: readonly Row[], limit: number, after: number, fields: readonly string[]): Page<SearchHit> {
  const { items, next } = page(
    rows.map((row, position) => ({ row, rank: after + position + 1 })),
    limit,
    (item) => item.rank
  );
  return { items: items.map(({ row, rank }) => ({ id: String(row.id), rank, ...snippet(row, fields) })), next };
}

// vectorPage pages a vector ranking read from the start.
function vectorPage(near: readonly Neighbour[], limit: number, after: number): Page<SearchHit> {
  const { items, next } = page(
    near.slice(after).map((neighbour, position) => ({ neighbour, rank: after + position + 1 })),
    limit,
    (item) => item.rank
  );
  return {
    items: items.map(({ neighbour, rank }) => ({
      id: neighbour.id,
      rank,
      score: 1 / (RRF_K + rank),
      vector: { rank, similarity: neighbour.similarity },
    })),
    next,
  };
}

// fusedPage pages a fused ranking.
function fusedPage(fused: readonly Fused[], limit: number, after: number, fields: readonly string[]): Page<SearchHit> {
  const { items, next } = page(
    fused.slice(after, after + limit + 1).map((hit, position) => ({ hit, rank: after + position + 1 })),
    limit,
    (item) => item.rank
  );
  return {
    items: items.map(({ hit, rank }) => ({
      id: hit.id,
      rank,
      score: hit.score,
      ...snippet(hit.textRow, fields),
      ...(hit.textRank !== undefined ? { text: { rank: hit.textRank } } : {}),
      ...(hit.vectorRank !== undefined ? { vector: { rank: hit.vectorRank, similarity: hit.similarity as number } } : {}),
    })),
    next,
  };
}

// mayRead refuses a caller who may not read the schema: a search returns
// ids and text, and a settle says which ids exist.
function mayRead(context: SchemaContext<SearchConfig>): void {
  if (!context.schemas.readable(context.schema)) {
    throw new EngineError('forbidden', `${context.principal.subject} may not read ${context.schema} in namespace ${context.namespace}`);
  }
}

// vectorsOf is the config's vectors, or the refusal of an operation that
// needs them.
function vectorsOf(context: SchemaContext<SearchConfig>, operation: string, path: string): SearchVectors {
  if (context.config.vectors === undefined) {
    throw new OperationParamsError(NAME, operation, [{ path, message: `${context.schema}'s Search keeps no vectors: its config has none` }]);
  }
  return context.config.vectors;
}

// queryVector checks a search's vector against the config's and returns
// it at unit length.
function queryVector(context: SchemaContext<SearchConfig>, params: FrozenJSON): Float32Array {
  const vectors = vectorsOf(context, 'search', '/vector');
  if (params.model !== undefined && params.model !== vectors.model) {
    throw new OperationParamsError(NAME, 'search', [
      { path: '/model', message: `is ${JSON.stringify(params.model)}, and ${context.schema}'s vectors come from ${JSON.stringify(vectors.model)}` },
    ]);
  }
  const vector = unit(params.vector as readonly number[], vectors.dimensions);
  if (typeof vector === 'string') {
    throw new OperationParamsError(NAME, 'search', [{ path: '/vector', message: vector }]);
  }
  return vector;
}

// ownText is an instance's text, as the index holds its fields.
function ownText(sql: TableReader, config: SearchConfig, row: number): string {
  const columns = config.fields.map((_, position) => COLUMNS[position]);
  const found = sql.get(`SELECT ${columns.join(', ')} FROM ${sql.table('text')} WHERE rowid = ?`, [row]);
  return composed(columns.map((column) => found?.[column]));
}

export const search = defineBehavior<SearchConfig>({
  declaration,

  parseConfig(json, target) {
    const raw = json as { fields: string[]; weights?: Record<string, number>; vectors?: SearchVectors };
    for (const field of raw.fields) {
      if (!Object.prototype.hasOwnProperty.call(target.fieldSchemas, field)) {
        throw new BehaviorConfigError(`fields: ${target.type} has no field ${field}`);
      }
      if (!isText(target.fieldSchemas[field])) {
        throw new BehaviorConfigError(`fields: ${target.type}.${field} is not a string, or a scalar whose values are strings`);
      }
    }
    for (const field of Object.keys(raw.weights ?? {})) {
      if (!raw.fields.includes(field)) {
        throw new BehaviorConfigError(`weights: ${field} is not an indexed field (fields: ${raw.fields.join(', ')})`);
      }
    }
    const vectors = raw.vectors;
    return {
      fields: [...raw.fields],
      weights: raw.fields.map((field) => raw.weights?.[field] ?? 1),
      ...(vectors !== undefined ? { vectors: { dimensions: vectors.dimensions, model: vectors.model, permission: vectors.permission } } : {}),
    };
  },

  // Fields, weights and vectors may change, and the behavior may be added
  // to or removed from a schema with instances: afterConfigChange rebuilds
  // or drops the index, and hashes the text again or forgets the vectors.
  configChange() {
    return undefined;
  },

  migrations: [
    {
      version: 1,
      name: 'index',
      up(sql) {
        sql.run(`CREATE TABLE ${sql.table('rows')} (
          row       INTEGER PRIMARY KEY,
          namespace TEXT    NOT NULL,
          schema    TEXT    NOT NULL,
          id        TEXT    NOT NULL,
          UNIQUE (namespace, schema, id)
        ) STRICT`);
        sql.run(`CREATE VIRTUAL TABLE ${sql.table('text')} USING fts5(${COLUMNS.join(', ')}, tokenize = 'unicode61 remove_diacritics 2')`);
      },
    },
    {
      // Each instance's vector: the hash of the text it is computed from
      // (null without vectors, and for text that holds nothing), the
      // vector (null until it is settled under that hash), and who settled
      // it and when. One index finds what an embedder pulls, the other
      // what a scan reads.
      version: 2,
      name: 'vectors',
      up(sql) {
        const rows = sql.table('rows');
        sql.run(`ALTER TABLE ${rows} ADD COLUMN source_hash TEXT`);
        sql.run(`ALTER TABLE ${rows} ADD COLUMN vector BLOB`);
        sql.run(`ALTER TABLE ${rows} ADD COLUMN settled_at INTEGER`);
        sql.run(`ALTER TABLE ${rows} ADD COLUMN settled_by TEXT`);
        sql.run(`CREATE INDEX ${sql.table('rows_stale')} ON ${rows} (namespace, schema, row) WHERE source_hash IS NOT NULL AND vector IS NULL`);
        sql.run(`CREATE INDEX ${sql.table('rows_embedded')} ON ${rows} (namespace, schema, row) WHERE vector IS NOT NULL`);
      },
    },
  ],

  afterChange(context, change) {
    const { sql, config, namespace, schema, id, data } = context;
    switch (change.kind) {
      case 'create':
        index(sql, config, namespace, schema, id, data);
        return;
      case 'delete':
        unindex(sql, namespace, schema, id);
        return;
      case 'update':
        if (changed(config, change.before, data)) {
          index(sql, config, namespace, schema, id, data);
        }
        return;
      case 'operation':
        if (change.before !== undefined && changed(config, change.before, data)) {
          index(sql, config, namespace, schema, id, data);
        }
        return;
    }
  },

  afterConfigChange(context) {
    const { sql, config, before, namespace, schema } = context;
    if (config !== undefined && before !== undefined && sameFields(before, config)) {
      const vectors = config.vectors;
      if (sameVectors(before.vectors, vectors)) {
        return;
      }
      if (vectors === undefined) {
        forget(sql, namespace, schema);
      } else {
        context.eachInstance((instance) => track(sql, vectors, namespace, schema, instance.id, texts(config, instance.data)));
      }
      return;
    }
    clear(sql, namespace, schema);
    if (config !== undefined) {
      context.eachInstance((instance) => index(sql, config, namespace, schema, instance.id, instance.data));
    }
  },

  schemaOperations: {
    search(context, params) {
      mayRead(context);
      const { config, sql, namespace, schema } = context;
      const { limit, after } = pageRequest(NAME, 'search', params);
      const syntax = (params.syntax as string | undefined) ?? 'words';
      const query = params.query as string | undefined;
      const match = query === undefined ? undefined : expression(query, syntax);
      if (params.vector === undefined) {
        if (match === undefined) {
          return { items: [], next: null };
        }
        return textPage(matches(sql, config, namespace, schema, match, syntax, limit + 1, after), limit, after, config.fields);
      }
      const vector = queryVector(context, params);
      if (query === undefined) {
        return vectorPage(vectorIndex.nearest(sql, namespace, schema, vector, after + limit + 1), limit, after);
      }
      const text = match === undefined ? [] : matches(sql, config, namespace, schema, match, syntax, RRF_WINDOW, 0);
      return fusedPage(fuse(text, vectorIndex.nearest(sql, namespace, schema, vector, RRF_WINDOW)), limit, after, config.fields);
    },

    similar(context, params) {
      mayRead(context);
      const { config, sql, namespace, schema } = context;
      const { limit, after } = pageRequest(NAME, 'similar', params);
      const id = params.id as string;
      const own = sql.get(`SELECT row, vector FROM ${sql.table('rows')} WHERE namespace = ? AND schema = ? AND id = ?`, key(namespace, schema, id));
      if (!own) {
        throw new EngineError('not_found', `${schema} ${id} does not exist in namespace ${namespace}`);
      }
      const row = Number(own.row);
      const vector = config.vectors === undefined ? undefined : decode(own.vector);
      const match = likeness(ownText(sql, config, row));
      if (vector === undefined) {
        if (match === undefined) {
          return { items: [], next: null, embedded: false };
        }
        const text = matches(sql, config, namespace, schema, match, 'words', limit + 1, after, row);
        return { ...textPage(text, limit, after, config.fields), embedded: false };
      }
      if (match === undefined) {
        return { ...vectorPage(vectorIndex.nearest(sql, namespace, schema, vector, after + limit + 1, row), limit, after), embedded: true };
      }
      const text = matches(sql, config, namespace, schema, match, 'words', RRF_WINDOW, 0, row);
      const near = vectorIndex.nearest(sql, namespace, schema, vector, RRF_WINDOW, row);
      return { ...fusedPage(fuse(text, near), limit, after, config.fields), embedded: true };
    },

    staleEmbeddings(context, params) {
      const vectors = vectorsOf(context, 'staleEmbeddings', '');
      mayRead(context);
      const { config, sql, namespace, schema } = context;
      const { limit, after } = pageRequest(NAME, 'staleEmbeddings', params);
      const columns = config.fields.map((_, position) => `t.${COLUMNS[position]} AS ${COLUMNS[position]}`);
      const rows = sql.all(
        `SELECT r.row AS row, r.id AS id, r.source_hash AS source_hash, ${columns.join(', ')}
         FROM ${sql.table('rows')} AS r JOIN ${sql.table('text')} AS t ON t.rowid = r.row
         WHERE r.namespace = ? AND r.schema = ? AND r.source_hash IS NOT NULL AND r.vector IS NULL AND r.row > ?
         ORDER BY r.row LIMIT ?`,
        [namespace, schema, after, limit + 1]
      );
      const { items, next } = page(rows, limit, (row) => Number(row.row));
      return {
        model: vectors.model,
        dimensions: vectors.dimensions,
        items: items.map((row) => ({
          id: String(row.id),
          text: composed(config.fields.map((_, position) => row[COLUMNS[position]])),
          sourceHash: String(row.source_hash),
        })),
        next,
      };
    },

    settleEmbeddings(context, params) {
      const vectors = vectorsOf(context, 'settleEmbeddings', '');
      mayRead(context);
      const { sql, namespace, schema, principal } = context;
      if (!context.can(vectors.permission)) {
        throw new EngineError(
          'forbidden',
          `${principal.subject} may not settle the vectors of ${schema} in namespace ${namespace}: it needs ${vectors.permission}`
        );
      }
      const items = params.items as ReadonlyArray<{ id: string; sourceHash: string; vector: readonly number[] }>;
      const issues: SchemaIssue[] = [];
      const seen = new Set<string>();
      const units = items.map((item, position) => {
        if (seen.has(item.id)) {
          issues.push({ path: `/items/${position}/id`, message: `settles ${item.id} a second time in one call` });
        }
        seen.add(item.id);
        const vector = unit(item.vector, vectors.dimensions);
        if (typeof vector === 'string') {
          issues.push({ path: `/items/${position}/vector`, message: vector });
        }
        return vector;
      });
      if (issues.length > 0) {
        throw new OperationParamsError(NAME, 'settleEmbeddings', issues);
      }
      const rows = sql.table('rows');
      const skipped: Array<{ id: string; reason: 'moved' | 'not_found' }> = [];
      let settled = 0;
      items.forEach((item, position) => {
        const found = sql.get(`SELECT row, source_hash FROM ${rows} WHERE namespace = ? AND schema = ? AND id = ?`, key(namespace, schema, item.id));
        if (!found) {
          skipped.push({ id: item.id, reason: 'not_found' });
        } else if (found.source_hash !== item.sourceHash) {
          skipped.push({ id: item.id, reason: 'moved' });
        } else {
          sql.run(`UPDATE ${rows} SET vector = ?, settled_at = ?, settled_by = ? WHERE row = ?`, [
            encode(units[position] as Float32Array),
            context.now,
            principal.subject,
            Number(found.row),
          ]);
          settled += 1;
        }
      });
      return { settled, skipped };
    },
  },
});

// A search across schemas takes search's parameters, with a vector only
// beside the model it was computed with, which picks the schemas it ranks.
const searchProperties = JSON.parse(
  JSON.stringify((declaration.operations.find((operation) => operation.name === 'search') as { paramsSchema: { properties: object } }).paramsSchema.properties)
) as Record<string, unknown>;

/** The parameters of a search across a namespace's schemas (searchSchemas), as a JSON Schema. */
export const SEARCH_SCHEMAS_PARAMS: Readonly<Record<string, unknown>> = deepFreeze({
  type: 'object',
  additionalProperties: false,
  anyOf: [{ required: ['query'] }, { required: ['vector'] }],
  dependentRequired: { vector: ['model'], model: ['vector'] },
  properties: {
    ...searchProperties,
    vector: {
      description: 'The vector to search for, computed with model: it ranks the schemas whose vectors come from that model, with as many dimensions.',
      type: 'array',
      minItems: 1,
      maxItems: 4096,
      items: { type: 'number' },
    },
    model: { description: 'The model the vector was computed with; required with a vector.', type: 'string', minLength: 1, maxLength: 200 },
  },
});

let checkSchemasParams: ValidateFunction | undefined;

// schemasParams checks the parameters of a search across schemas.
function schemasParams(params: unknown): FrozenJSON {
  checkSchemasParams ??= new Ajv2020({ strict: false, validateFormats: false, allErrors: true }).compile(
    JSON.parse(JSON.stringify(SEARCH_SCHEMAS_PARAMS)) as Record<string, unknown>
  );
  const copied = jsonCopy(params ?? {});
  if (!('value' in copied)) {
    throw new OperationParamsError(NAME, 'search', [{ path: '', message: copied.problem }]);
  }
  if (!checkSchemasParams(copied.value)) {
    throw new OperationParamsError(NAME, 'search', BehaviorRegistry.issues(checkSchemasParams.errors));
  }
  return deepFreeze(copied.value as FrozenJSON);
}

// searchConfig is the Search config a schema's live version gives, as the
// schema holds it; undefined when it does not compose Search, or the
// principal may not read it, or the engine cannot run it.
function searchConfig(schemas: SchemaRegistry, principal: Principal, schema: string, namespace: string): { vectors?: SearchVectors } | undefined {
  try {
    const bound = schemas.behaviors(principal, schema, { namespace }).find((behavior) => behavior.name === NAME);
    return bound === undefined ? undefined : (bound.config as { vectors?: SearchVectors });
  } catch (error) {
    if (error instanceof EngineError && (error.code === 'unavailable' || error.code === 'forbidden' || error.code === 'not_found')) {
      return undefined;
    }
    throw error;
  }
}

/**
 * searchSchemas searches every schema a namespace reaches that composes
 * Search and that the principal may read, with each schema's search as
 * the principal, and fuses their rankings: each schema ranks its own
 * instances, the first RRF_WINDOW by text and by vector, and a hit scores
 * as search scores it, 1 / (RRF_K + its place) in each ranking of its
 * schema it is in, so one schema's best hit ties another's. A vector ranks
 * the schemas whose vectors come from its model, with its dimensions, and
 * the query alone ranks the others, or none without one. A schema the
 * principal may not read, whose search the policy refuses, or whose
 * behaviors this engine cannot run is skipped, not refused, as the tools
 * document leaves it out.
 */
export function searchSchemas(
  schemas: SchemaRegistry,
  instances: InstanceStore,
  principal: Principal,
  params: unknown,
  namespace: string
): Page<SchemaSearchHit> {
  const checked = schemasParams(params);
  const { limit, after } = pageRequest(NAME, 'search', checked);
  const query = checked.query as string | undefined;
  const vector = checked.vector as readonly number[] | undefined;
  const found: Array<{ hit: SchemaSearchHit; place: number }> = [];
  for (const summary of schemas.list(principal, { namespace })) {
    const config = summary.liveVersion === null ? undefined : searchConfig(schemas, principal, summary.name, namespace);
    if (config === undefined) {
      continue;
    }
    const ranksVector = vector !== undefined && config.vectors?.model === checked.model && config.vectors?.dimensions === vector.length;
    if (query === undefined && !ranksVector) {
      continue;
    }
    const call = {
      ...(query !== undefined ? { query, ...(checked.syntax !== undefined ? { syntax: checked.syntax } : {}) } : {}),
      ...(ranksVector ? { vector, model: checked.model } : {}),
      limit: ranksVector && query !== undefined ? 2 * RRF_WINDOW : RRF_WINDOW,
    };
    let ranked: Page<SearchHit>;
    try {
      ranked = instances.invokeSchema(principal, summary.name, 'search', call, { namespace }) as Page<SearchHit>;
    } catch (error) {
      if (error instanceof EngineError && error.code === 'forbidden') {
        continue;
      }
      throw error;
    }
    for (const { id, rank, score, ...how } of ranked.items) {
      found.push({
        place: rank,
        hit: { schema: summary.name, id, rank: 0, score: score ?? 1 / (RRF_K + rank), ...how, ...(ranksVector ? {} : { text: { rank } }) },
      });
    }
  }
  // The list is by schema name and the sort is stable, so equal scores
  // keep the better place in their schema, then the schema's name.
  found.sort((a, b) => b.hit.score - a.hit.score || a.place - b.place);
  return page(
    found.slice(after, after + limit + 1).map(({ hit }, position) => ({ ...hit, rank: after + position + 1 })),
    limit,
    (hit) => hit.rank
  );
}
