import { decodeRow, encodeParam, type RowField } from './pgcodec.js';
import { NotFoundError, PageKeyError, TooManyRowsError, fill, type RowPage } from './orm.js';

/*
A DB service's declared queries (D86): @query<Params, Row>({ sql, ... })
in the schema, verified on Postgres by `superschematic queries verify`, and
carried by the types package's ./orm entry as data, each its statement,
written once in Go (sqlutil.QueriesOf) as the Go ORM's Queries runs it,
and the codec's descriptor of each parameter and each row field.

orm.queries.<name>(params) and tx.queries.<name>(params) run it on the pool
or in the transaction: each parameter bound through encodeParam, so a
base62 UUID binds hyphenated, and each row decoded through decodeRow, a
UUID base62, a BIGINT a number, a TIMESTAMPTZ a Date, a NUMERIC a decimal's
canonical text, a vector its numbers.

A query with an orderBy fills its statement with the fixed fragment its
parameter's value names, and refuses any other: no caller text reaches the
SQL. A paged query reads a page in the order of its key, the page after
the key of the page before's last row, and answers the next key when more
rows follow, as findPage does (D60).

A query that returns 'one' answers its one row, and throws NotFoundError
when the statement reads none, as getOne does, and TooManyRowsError when it
reads more than one; one that returns 'optional' answers its row or null,
and throws TooManyRowsError the same (D95).
*/

/** A query's dynamic sort: the parameter that picks it, each value's fixed ORDER BY fragment, and a null parameter's pick. */
export interface QueryOrderDefinition {
  readonly param: string;
  readonly options: Readonly<Record<string, string>>;
  readonly default?: string;
}

/** A paged query's statements, each the query in a subquery ordered by its key, %d the rows to read, and its key's row members. */
export interface QueryPageDefinition {
  readonly first: string;
  readonly after: string;
  readonly key: readonly string[];
}

/** A declared query, as a types package's ./orm entry describes it. */
export interface QueryDefinition {
  /** The statement, a format string: %s is the order's fragment, %% a %. */
  readonly text: string;
  /** The parameters, in positional order: $1 is the first. */
  readonly params: readonly RowField[];
  /** The row's fields, each read from its column. */
  readonly row: readonly RowField[];
  readonly orderBy?: QueryOrderDefinition;
  readonly page?: QueryPageDefinition;
  /** What a call answers: unset, the rows; 'one', the one row; 'optional', the row or null (D95). */
  readonly returns?: 'one' | 'optional';
}

/** The options of a paged query. */
export interface QueryPageOptions<K> {
  /** The key of the last row of the page before; the first page without it. */
  readonly after?: K | null;
  /** The rows a page holds, 1 or more; 100 unless set. */
  readonly limit?: number;
}

/** What runs a query's statement: the ORM's runner, on its pool or its transaction's connection. */
export type QueryRun = (text: string, values: unknown[]) => Promise<{ rows: Record<string, unknown>[] }>;

type Values = Record<string, unknown>;

/** The functions of a database's queries, by name, each over run. */
export function queriesOf(defs: Readonly<Record<string, QueryDefinition>>, run: QueryRun, defaultLimit: number): Record<string, (params?: Values, page?: QueryPageOptions<Values>) => Promise<unknown>> {
  const out: Record<string, (params?: Values, page?: QueryPageOptions<Values>) => Promise<unknown>> = {};
  for (const [name, def] of Object.entries(defs)) {
    out[name] = (params?: Values, page?: QueryPageOptions<Values>) => runQuery(name, def, run, defaultLimit, params ?? {}, page ?? {});
  }
  return out;
}

function encode(name: string, field: RowField, value: unknown, what: string): unknown {
  try {
    return encodeParam(value, field);
  } catch (error) {
    throw new Error(`orm: query ${name}: ${what} ${field.name}: ${error instanceof Error ? error.message : String(error)}`, { cause: error });
  }
}

function decode<T>(name: string, def: QueryDefinition, row: Values): T {
  try {
    return decodeRow<T>(row, def.row);
  } catch (error) {
    throw new Error(`orm: query ${name}: ${error instanceof Error ? error.message : String(error)}`, { cause: error });
  }
}

async function runQuery(name: string, def: QueryDefinition, run: QueryRun, defaultLimit: number, params: Values, page: QueryPageOptions<Values>): Promise<unknown> {
  const args = def.params.map(p => encode(name, p, params[p.name], 'parameter'));
  if (def.orderBy) {
    const order = def.orderBy;
    const value = params[order.param] ?? order.default;
    if (typeof value !== 'string' || !Object.hasOwn(order.options, value)) {
      throw new Error(`orm: query ${name} orders by ${Object.keys(order.options).join(', ')}, not ${JSON.stringify(value)}`);
    }
    const { rows } = await run(fill(def.text, order.options[value]), args);
    return answer(name, def, rows.map(row => decode(name, def, row)));
  }
  if (!def.page) {
    const { rows } = await run(fill(def.text), args);
    return answer(name, def, rows.map(row => decode(name, def, row)));
  }
  if (page.limit !== undefined && page.limit < 0) throw new Error(`orm: a page of query ${name} holds 1 or more rows, not ${page.limit}`);
  const limit = page.limit ? page.limit : defaultLimit;
  let text = def.page.first;
  if (page.after) {
    for (const member of def.page.key) {
      const value = page.after[member];
      if (value === undefined || value === null) throw new PageKeyError(`the key of query ${name} holds no ${member}`);
      const field = def.row.find(f => f.name === member);
      if (!field) throw new Error(`orm: query ${name} pages by ${member}, which is no field of its row`);
      args.push(encode(name, field, value, 'page key'));
    }
    text = def.page.after;
  }
  const { rows } = await run(fill(text, limit + 1), args);
  const decoded = rows.map(row => decode<Values>(name, def, row));
  if (decoded.length <= limit) return { rows: decoded, next: null } satisfies RowPage<Values, Values>;
  const kept = decoded.slice(0, limit);
  const last = kept[limit - 1]!;
  const next: Values = {};
  for (const member of def.page.key) next[member] = last[member];
  return { rows: kept, next } satisfies RowPage<Values, Values>;
}

/** What a call of a query that is not paged answers: its rows, or, when it returns 'one' or 'optional', its one row. */
function answer(name: string, def: QueryDefinition, rows: unknown[]): unknown {
  if (!def.returns) return rows;
  if (rows.length > 1) {
    throw new TooManyRowsError(`orm: query ${name} returns ${def.returns === 'one' ? 'one row' : 'one row or none'}, and its statement read ${rows.length}`);
  }
  if (rows.length === 1) return rows[0];
  if (def.returns === 'optional') return null;
  throw new NotFoundError(`orm: query ${name} returns one row, and its statement read none`);
}
