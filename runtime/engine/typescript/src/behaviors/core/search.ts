/*
Search, the core's full-text search (D16). The config names the type's
own top-level text fields to index, a string or a scalar whose values are
strings each, at most 16, and optionally a weight for each. The index is
one FTS5 table the behavior owns, with a column per indexed field in the
config's order, beside a table that gives each instance's row its
namespace, schema and id. afterChange keeps it in the transaction of every
create, update, delete and operation that changes an indexed field, so a
search never sees a stale row.

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
place in that order. Each carries a snippet of the indexed field with the most
matches.

The FTS5 table is shared by every schema and namespace in the file, so
bm25's statistics (how many rows hold a term, how long a field is on
average) are taken over all of them. The ids and snippets a search returns
are always the caller's own, but the order among them can shift with
another namespace's text, which is why a hit carries its place and not
bm25's score.

afterConfigChange rebuilds a namespace's index of the schema when the
indexed fields change or the behavior is added (a first version
included), and drops it when the behavior is removed; a change of weights
alone needs neither, since they apply when a search runs. The rebuild
runs in the publish's transaction and holds the write lock until it has
indexed every instance of the schema.
*/

import { EngineError, OperationParamsError } from '../../errors.js';
import { SqliteError, type Row, type SqlValue } from '../../storage/driver.js';
import { BehaviorConfigError, defineBehavior, type FrozenJSON, type TableReader, type TableWriter } from '../behavior.js';
import { page, pageRequest } from '../paging.js';
import declaration from './declarations/Search.behavior.json' with { type: 'json' };

/** Search's config, parsed. */
export interface SearchConfig {
  /** The indexed fields, by JSON key, in the config's order: the index's columns. */
  readonly fields: readonly string[];
  /** Each indexed field's weight, in the order of fields; 1 for one the config does not weigh. */
  readonly weights: readonly number[];
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
  /** The indexed field the snippet is from. */
  readonly field?: string;
  readonly snippet?: readonly SnippetPart[];
}

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

// changed reports whether an indexed field differs between two versions of
// an instance's own fields.
function changed(config: SearchConfig, before: FrozenJSON, after: FrozenJSON): boolean {
  return config.fields.some((field) => textOf(before[field]) !== textOf(after[field]));
}

// index writes an instance's indexed fields, replacing what the index held
// for it.
function index(sql: TableWriter, config: SearchConfig, namespace: string, schema: string, id: string, data: FrozenJSON): void {
  const rows = sql.table('rows');
  const text = sql.table('text');
  sql.run(`INSERT INTO ${rows} (namespace, schema, id) VALUES (?, ?, ?) ON CONFLICT (namespace, schema, id) DO NOTHING`, key(namespace, schema, id));
  const row = Number((sql.get(`SELECT row FROM ${rows} WHERE namespace = ? AND schema = ? AND id = ?`, key(namespace, schema, id)) as Row).row);
  sql.run(`DELETE FROM ${text} WHERE rowid = ?`, [row]);
  const columns = config.fields.map((_, position) => COLUMNS[position]);
  sql.run(`INSERT INTO ${text} (rowid, ${columns.join(', ')}) VALUES (?, ${columns.map(() => '?').join(', ')})`, [
    row,
    ...config.fields.map((field) => textOf(data[field])),
  ]);
}

function unindex(sql: TableWriter, namespace: string, schema: string, id: string): void {
  const rows = sql.table('rows');
  const found = sql.get(`SELECT row FROM ${rows} WHERE namespace = ? AND schema = ? AND id = ?`, key(namespace, schema, id));
  if (found) {
    sql.run(`DELETE FROM ${sql.table('text')} WHERE rowid = ?`, [Number(found.row)]);
    sql.run(`DELETE FROM ${rows} WHERE row = ?`, [Number(found.row)]);
  }
}

// clear drops a namespace's index of a schema.
function clear(sql: TableWriter, namespace: string, schema: string): void {
  const rows = sql.table('rows');
  sql.run(`DELETE FROM ${sql.table('text')} WHERE rowid IN (SELECT row FROM ${rows} WHERE namespace = ? AND schema = ?)`, [namespace, schema]);
  sql.run(`DELETE FROM ${rows} WHERE namespace = ? AND schema = ?`, [namespace, schema]);
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
    .map((word) => `"${word.replace(/"/g, '""')}"`)
    .join(' ');
}

// snippetOf reads a snippet FTS5 wrote with OPEN and CLOSE around each match.
function snippetOf(value: SqlValue): SnippetPart[] {
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

// hit builds a result from a row: the snippet of the field with the most
// matches, the earliest in the config on a tie.
function hit(row: Row, rank: number, fields: readonly string[]): SearchHit {
  let best: { field: string; parts: SnippetPart[]; matches: number } | undefined;
  fields.forEach((field, position) => {
    const parts = snippetOf(row[`s${position}`]);
    const matches = parts.filter((part) => part.match).length;
    if (matches > 0 && (best === undefined || matches > best.matches)) {
      best = { field, parts, matches };
    }
  });
  const id = String(row.id);
  return best === undefined ? { id, rank } : { id, rank, field: best.field, snippet: best.parts };
}

function matches(sql: TableReader, config: SearchConfig, namespace: string, schema: string, match: string, syntax: string, limit: number, after: number): Row[] {
  const text = sql.table('text');
  const snippets = config.fields.map((_, position) => `snippet(${text}, ${position}, ?, ?, '...', ${SNIPPET_WORDS}) AS s${position}`);
  const weights = COLUMNS.map((_, position) => config.weights[position] ?? 0);
  // CROSS JOIN keeps the full-text match as the outer loop: some SQLite
  // builds would otherwise walk the schema's rows and run the match once
  // for each of them.
  try {
    return sql.all(
      `SELECT r.id AS id, ${snippets.join(', ')}
       FROM ${text} CROSS JOIN ${sql.table('rows')} AS r ON r.row = ${text}.rowid
       WHERE ${text} MATCH ? AND r.namespace = ? AND r.schema = ?
       ORDER BY bm25(${text}, ${weights.map(() => '?').join(', ')}), r.row
       LIMIT ? OFFSET ?`,
      [...config.fields.flatMap(() => [OPEN, CLOSE]), match, namespace, schema, ...weights, limit + 1, after]
    );
  } catch (error) {
    if (syntax === 'fts5' && error instanceof SqliteError && QUERY_ERROR.test(error.message)) {
      throw new OperationParamsError(NAME, 'search', [{ path: '/query', message: `is not an FTS5 expression this index takes: ${error.message}` }]);
    }
    throw error;
  }
}

export const search = defineBehavior<SearchConfig>({
  declaration,

  parseConfig(json, target) {
    const raw = json as { fields: string[]; weights?: Record<string, number> };
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
    return { fields: [...raw.fields], weights: raw.fields.map((field) => raw.weights?.[field] ?? 1) };
  },

  // Fields and weights may change, and the behavior may be added to or
  // removed from a schema with instances: afterConfigChange rebuilds or
  // drops the index.
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
      return;
    }
    clear(sql, namespace, schema);
    if (config !== undefined) {
      context.eachInstance((instance) => index(sql, config, namespace, schema, instance.id, instance.data));
    }
  },

  schemaOperations: {
    search(context, params) {
      if (!context.schemas.readable(context.schema)) {
        throw new EngineError('forbidden', `${context.principal.subject} may not read ${context.schema} in namespace ${context.namespace}`);
      }
      const { limit, after } = pageRequest(NAME, 'search', params);
      const syntax = (params.syntax as string | undefined) ?? 'words';
      const match = expression(params.query as string, syntax);
      if (match === undefined) {
        return { items: [], next: null };
      }
      const rows = matches(context.sql, context.config, context.namespace, context.schema, match, syntax, limit, after);
      const { items, next } = page(
        rows.map((row, position) => ({ row, rank: after + position + 1 })),
        limit,
        (item) => item.rank
      );
      return { items: items.map(({ row, rank }) => hit(row, rank, context.config.fields)), next };
    },
  },
});
