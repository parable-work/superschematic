import { covers } from './auth.js';
import { behaviorCaller, behaviorOperation } from './behavior-routes.js';
import type { FieldFilter, RequestFilter } from './filter.js';
import type { RequestContext } from './operation.js';
import { decodeCursor, encodeCursor, refusedCursor, type Page, type PageRequest } from './page.js';
import { HttpProblem, badRequest, conflict, forbidden, notFound, unauthorized } from './problem.js';
import { revealed } from './secret.js';
import { uuidValue } from './uuid.js';

/*
The handlers of a generated router's table operations (D106): the routes of
an API's operations marked @tableOperation, which read and write the rows
of one table of the API's database through the table's repository in the
database's orm.ts, with no implementation method. The route runs every
step an implemented route runs (the service step, the end-user step and
the permission check among them) and decodes the request as an implemented
route's is; the handler then does what the operation's kind says, a read,
a list, a create, an update or a delete, and answers the row's view: the
members the operation's result lists, each the row's member of the same
name, and nothing else of the row.

Every handler runs in one transaction of the repositories, as the
request's caller, the route's principal (orm.as), so the row's audit
fields record who called (D87) whichever copy of the runtime the
repositories were opened with, and as the behaviors' caller that principal
gives (behaviorCaller), so a table that composes behaviors is written
through the behaviors' core and a rule's refusal is answered as a behavior
operation answers it (D103). A handler has no hook before or after it.

An operation with a row rule (D108) reaches only the rows that name the
caller: a read, a list, an update and a delete keep the rows one of the
rule's members holds the caller's subject in, so a row that names another
is the 404 of one that is not there, and a create sets each member but
createdBy to the caller. A caller who holds the rule's override permission
reaches every row.

The refusals are the ones a hand-written operation answers (D42), mapped
once the transaction has rolled back: 404 not_found for a key that holds
no live row under the path's parents, 409 conflict for a unique key
another row holds and for a delete of a row another still references, 400
at the body's member for a relation's key that names no row and for a
null a column does not take, 404 for a parent the path names that does
not exist, and 401 unauthorized for a write that stamps its caller and
has none. No refusal carries a statement's or the
driver's words.

It imports nothing from pg or the repositories, so the main entry stays
free of them: it takes the repositories as a value, reads a table's
columns from the definition its repository holds, and recognizes an error
by its shape, the repositories' NotFoundError, NoUserError and
PageKeyError by their names and a Postgres error by its SQLSTATE,
whichever copy of the runtime or the driver threw it.
*/

/** What a table operation does, which its HTTP method and shape decide (ir.TableOperationKinds). */
export type TableOperationKind = 'read' | 'list' | 'create' | 'update' | 'delete';

/**
 * A table operation as the generated router carries it (D106): what the
 * loader filled of the operation's @tableOperation, and the members of its
 * views.
 */
export interface TableOperation {
  readonly kind: TableOperationKind;
  /** The table, or for a read and a list a keyed projection (D90), by its type: what a refusal names. */
  readonly type: string;
  /** The member of the repositories that holds the type's repository. */
  readonly repository: string;
  /** The table's key: the path parameter of a read, an update and a delete, and the row's member. Absent for a list and a create. */
  readonly key?: string;
  /**
   * The path parameters beside the key, each the key of one of the table's
   * to-one relations and the row's member that holds it (teamId): a create
   * sets each from the path, and every other kind keeps only the rows that
   * hold the path's.
   */
  readonly parents?: readonly string[];
  /** The members of the result's view, each the row's member of the same name. */
  readonly view: readonly string[];
  /** The members of the view marked @decrypted, each the opened value of its Encrypted<T> column. */
  readonly decrypted?: readonly string[];
  /** The members of a create's and an update's body, each the row's member it sets. */
  readonly body?: readonly string[];
  /** A list's SearchQuery argument (D62), by its query parameter. */
  readonly search?: string;
  /**
   * The row rule (D108): the row's members that name who may reach it,
   * each a member that holds a subject, a UUID, a to-one relation's key,
   * text or createdBy. Absent reaches every row the route admits.
   */
  readonly by?: readonly string[];
  /** The permission whose holder the row rule does not hold back. */
  readonly overridePermission?: string;
}

/** The request a table operation's handler takes: what the route decoded of it. */
export interface TableRequest {
  readonly path: Readonly<Record<string, unknown>>;
  readonly query: Readonly<Record<string, unknown>>;
  /** The body, as the operation's input type's parser read it: a member the request left out is absent, and one it sent as null is null. */
  readonly input?: unknown;
}

type Row = Readonly<Record<string, unknown>>;
type Values = Record<string, unknown>;

/** A column of a table's definition, as a repository holds it (orm.ts's TableColumn): what the handlers read of it. */
interface ColumnShape {
  /** The row's member. */
  readonly name: string;
  /** Its column, quoted for SQL where it must be. */
  readonly column: string;
  /** The column is NOT NULL. */
  readonly required?: boolean;
  /** The target type of a to-one relation, whose key the column holds. */
  readonly relation?: string;
  /** The column holds a UUID. */
  readonly uuid?: boolean;
  /** The audit field the column is, which the repository stamps. */
  readonly audit?: string;
  /** The operations of the column's filter. */
  readonly filter?: readonly string[];
}

/** A table's repository, as the handlers run it (orm.ts's Repository), with the definition it holds. */
interface RepositoryShape {
  readonly def?: { readonly columns?: readonly ColumnShape[] };
  findOne(filter: Values): Promise<Row | null>;
  findPage(filter: Values, options: Values): Promise<{ readonly rows: readonly Row[]; readonly next: unknown }>;
  createOne(input: Values): Promise<Row>;
  updateOne(id: unknown, update: Values): Promise<Row>;
  deleteOne(id: unknown): Promise<void>;
}

/** The repositories, as the handlers open a transaction of them (orm.ts's Orm). */
interface OrmShape {
  as(principal: unknown): OrmShape;
  withBehaviorCaller(caller: unknown): OrmShape;
  transaction<T>(fn: (tx: Readonly<Record<string, RepositoryShape | undefined>>) => Promise<T>): Promise<T>;
}

/** The columns of a repository's table, or none when it holds no definition. */
function columnsOf(repository: RepositoryShape): readonly ColumnShape[] {
  return repository.def?.columns ?? [];
}

/** The body's validation shape (hono.ts's inputRefusal): the 400 of one member of an input that breaks a rule. */
function memberRefusal(member: string, validator: string, message: string, cause: unknown): HttpProblem {
  return badRequest('Request body does not match the declared input', {
    cause,
    details: { location: 'body', reason: 'validation failed' },
    extensions: { errors: { [member]: [{ validator, message }] } },
  });
}

/** A Postgres error, as the driver throws one: its SQLSTATE, and the words it has for the row and the columns. */
interface DatabaseError {
  readonly code: string;
  readonly detail?: string;
  readonly column?: string;
}

/** The SQLSTATEs the handlers answer: a unique key held, a foreign key that names no row, and a null a column does not take. */
const UNIQUE_VIOLATION = '23505';
const FOREIGN_KEY_VIOLATION = '23503';
const NOT_NULL_VIOLATION = '23502';

const SQLSTATE = /^[0-9A-Z]{5}$/u;

/** The Postgres error an error is, or is caused by; undefined for any other. */
function databaseError(error: unknown): DatabaseError | undefined {
  for (let candidate = error, depth = 0; candidate !== null && typeof candidate === 'object' && depth < 4; depth++) {
    const { code, detail, column } = candidate as { code?: unknown; detail?: unknown; column?: unknown };
    if (typeof code === 'string' && SQLSTATE.test(code)) {
      return {
        code,
        ...(typeof detail === 'string' ? { detail } : {}),
        ...(typeof column === 'string' ? { column } : {}),
      };
    }
    candidate = (candidate as { cause?: unknown }).cause;
  }
  return undefined;
}

/** A column's name as Postgres reports it: without the quotes a statement names it in. */
function unquoted(column: string): string {
  return column.startsWith('"') && column.endsWith('"') && column.length > 1 ? column.slice(1, -1).replaceAll('""', '"') : column;
}

/**
 * The columns a constraint's violation names: Postgres's detail lists them
 * before the values, "Key (team_id, slug)=(…)", whatever language its
 * words are in. None for a detail that lists none, and for a key over an
 * expression, whose parts are no column.
 */
function violatedColumns(detail: string | undefined, columns: readonly ColumnShape[]): ColumnShape[] | undefined {
  const listed = /\(([^()]*)\)=\(/u.exec(detail ?? '')?.[1];
  if (listed === undefined) return undefined;
  const out: ColumnShape[] = [];
  for (const part of listed.split(',')) {
    const name = unquoted(part.trim());
    const column = columns.find(candidate => unquoted(candidate.column) === name);
    if (!column) return undefined;
    out.push(column);
  }
  return out.length > 0 ? out : undefined;
}

/** A list of names as a sentence holds it: a, b and c. */
function listed(names: readonly string[]): string {
  return names.length < 2 ? names.join('') : `${names.slice(0, -1).join(', ')} and ${names[names.length - 1]}`;
}

/**
 * The problem a table operation answers an error of its transaction with
 * (D42), or the error itself for one it has no answer for, which the route
 * then answers as any error it did not expect:
 *
 * - the repositories' NotFoundError, a row that went between the
 *   handler's read and its write: 404 not_found;
 * - their NoUserError, a write that stamps its caller with none: 401
 *   unauthorized;
 * - their PageKeyError, a cursor that names no row of its order: the
 *   refused-cursor 400;
 * - a unique key another row holds (23505): 409 conflict, naming the key's
 *   members when the violation lists its columns;
 * - on a create and an update, a foreign key that names no row (23503):
 *   400 at the body's member that holds it, validator reference, or 404
 *   not_found when the member is a parent's, the row the path names;
 * - on a delete, a foreign key that still holds the row (23503 too):
 *   409 conflict, the row is still referenced;
 * - a null a column does not take (23502): 400 at the body's member,
 *   validator required, as updateOne's own check answers it.
 */
function refusalOf(operation: TableOperation, columns: readonly ColumnShape[], error: unknown): unknown {
  if (error instanceof HttpProblem) return error;
  const name = error instanceof Error ? error.name : undefined;
  if (name === 'NotFoundError') return notFound(`${operation.type} not found`, { cause: error });
  if (name === 'NoUserError') return unauthorized(undefined, { cause: error });
  if (name === 'PageKeyError') return refusedCursor();
  const failed = databaseError(error);
  if (!failed) return error;
  const body = operation.body ?? [];
  switch (failed.code) {
    case UNIQUE_VIOLATION: {
      const held = violatedColumns(failed.detail, columns);
      return conflict(
        held ? `${operation.type} with that ${listed(held.map(column => column.name))} already exists` : `${operation.type} conflicts with an existing row`,
        { cause: error }
      );
    }
    case FOREIGN_KEY_VIOLATION: {
      // A delete breaks a foreign key only from the other side: a row of
      // another table still points at this one.
      if (operation.kind === 'delete') return conflict(`${operation.type} is still referenced`, { cause: error });
      if (operation.kind !== 'create' && operation.kind !== 'update') return error;
      for (const column of violatedColumns(failed.detail, columns) ?? []) {
        const related = column.relation ?? 'row';
        if (operation.parents?.includes(column.name)) return notFound(`${related} not found`, { cause: error });
        if (body.includes(column.name)) return memberRefusal(column.name, 'reference', `names no ${related}`, error);
      }
      return error;
    }
    case NOT_NULL_VIOLATION: {
      const column = columns.find(candidate => unquoted(candidate.column) === failed.column);
      return column && body.includes(column.name) ? memberRefusal(column.name, 'required', 'required field', error) : error;
    }
  }
  return error;
}

/**
 * Runs a table operation's work in one transaction of the repositories,
 * as the route's principal, or as no one without one or with one whose
 * subject is no user's id, as the Go server's WithCaller binds no one for
 * it, so a write that stamps its caller is the 401 and a read is served;
 * and as the behaviors' caller the route gives, on the operation's
 * repository. It resolves to what the work answers. An error is mapped once the transaction has rolled
 * back: a refusal of the behaviors' core as a behavior operation answers
 * it (behaviorOperation), and any other as refusalOf says.
 */
async function inTransaction<T>(orm: object, operation: TableOperation, ctx: RequestContext, work: (repository: RepositoryShape) => Promise<T>): Promise<T> {
  let columns: readonly ColumnShape[] = [];
  const acting = ctx.principal && uuidValue(ctx.principal.subject) !== undefined ? ctx.principal : null;
  try {
    return await behaviorOperation(() =>
      (orm as OrmShape)
        .as(acting)
        .withBehaviorCaller(behaviorCaller(ctx))
        .transaction(tx => {
          const repository = tx[operation.repository];
          if (!repository) throw new Error(`table operation: the repositories hold no ${operation.repository}, the repository of ${operation.type}`);
          columns = columnsOf(repository);
          return work(repository);
        })
    );
  } catch (error) {
    throw refusalOf(operation, columns, error);
  }
}

/** The condition no row meets: a key in no keys. */
const NO_ROW = Symbol('no row');

/**
 * The rows an operation's row rule reaches for the request's caller
 * (D108): undefined for every row, an operation with no rule's and a
 * caller's who holds its override permission; else the repository's
 * filter that keeps the rows one of the rule's members holds the caller's
 * subject in, or NO_ROW when none can, a request with no caller's and a
 * subject that is no UUID against members that hold only UUIDs. A member
 * that holds a UUID is compared with a subject that is one, in either of
 * its forms, and text with the subject as it is. A member the repository
 * keeps no equality filter for is the generator's error, thrown before
 * anything is read: a filter drops a member it does not know, and a rule
 * that dropped one would reach every row.
 */
function reachOf(operation: TableOperation, columns: readonly ColumnShape[], ctx: RequestContext): Values | typeof NO_ROW | undefined {
  const by = operation.by ?? [];
  if (by.length === 0) return undefined;
  const members = by.map(member => {
    const column = columns.find(candidate => candidate.name === member);
    if (!column?.filter?.includes('eq')) {
      throw new Error(`table operation: the row rule of ${operation.type} names ${member}, which its repository keeps no equality filter for; rebuild the database's types`);
    }
    return column;
  });
  const principal = ctx.principal;
  const override = operation.overridePermission;
  if (override !== undefined && principal?.permissions.some(granted => covers(granted, override))) return undefined;
  const subject = principal?.subject;
  if (subject === undefined || subject === '') return NO_ROW;
  const named: Values[] = [];
  for (const column of members) {
    if (column.uuid === true && uuidValue(subject) === undefined) continue;
    named.push({ [column.name]: { eq: subject } });
  }
  return named.length === 0 ? NO_ROW : { anyOf: named };
}

/** The row's view: the members the operation's result lists, a @decrypted one opened. */
function viewOf(operation: TableOperation, row: Row): Values {
  const view: [string, unknown][] = operation.view.map(member => [
    member,
    operation.decrypted?.includes(member) ? revealed(row[member]) : row[member],
  ]);
  return Object.fromEntries(view);
}

/** The condition each of the operation's parents is: the row's member holds the path's. */
function parentsOf(operation: TableOperation, request: TableRequest): Values {
  return Object.fromEntries((operation.parents ?? []).map((parent): [string, unknown] => [parent, { eq: request.path[parent] }]));
}

/**
 * The live row whose key is the path's, whose parents are the path's and
 * which the operation's row rule reaches for the caller (D108), or the
 * 404: a row that names another caller is not told apart from one that is
 * not there.
 */
async function liveRow(repository: RepositoryShape, operation: TableOperation, ctx: RequestContext, request: TableRequest): Promise<Row> {
  const key = operation.key;
  if (key === undefined) throw new Error(`table operation: a ${operation.kind} of ${operation.type} names no key`);
  const reach = reachOf(operation, columnsOf(repository), ctx);
  if (reach === NO_ROW) throw notFound(`${operation.type} not found`);
  const row = await repository.findOne({
    ...parentsOf(operation, request),
    [key]: { eq: request.path[key] },
    ...(reach === undefined ? {} : { allOf: [reach] }),
  });
  if (row === null) throw notFound(`${operation.type} not found`);
  return row;
}

/** The body's members, as its input type's parser read them. */
function bodyOf(operation: TableOperation, request: TableRequest): Row {
  const input = request.input;
  if (input === null || typeof input !== 'object' || Array.isArray(input)) {
    throw new Error(`table operation: a ${operation.kind} of ${operation.type} takes its body's members, and the route decoded none`);
  }
  return input as Row;
}

/**
 * A read (D106): the view of the live row whose key is the path's, under
 * the path's parents; 404 not_found for a key that holds none, a
 * soft-deleted row's and a row's under another parent among them.
 */
export function tableRead(orm: object, operation: TableOperation, ctx: RequestContext, request: TableRequest): Promise<unknown> {
  return inTransaction(orm, operation, ctx, async repository => viewOf(operation, await liveRow(repository, operation, ctx, request)));
}

/** The text a contains matches, as the repository's ilike reads it between its two %: with \, % and _ escaped by \, so each matches itself. */
function likeText(text: string): string {
  return text.replace(/[\\%_]/gu, '\\$&');
}

/**
 * A request's filter (FilterBy) as the repository's filter (orm.ts): each
 * field's member the row's member of the same name, eq, in, gt, gte, lt,
 * lte and isNull as they are, and contains the repository's ilike, the
 * text it holds in any case, with what a pattern reads otherwise escaped.
 */
export function tableFilter(filter: RequestFilter): Record<string, Record<string, unknown>> {
  const out: [string, Record<string, unknown>][] = [];
  for (const [field, member] of Object.entries(filter)) {
    const { contains, ...compared }: FieldFilter = member;
    out.push([field, contains === undefined ? compared : { ...compared, ilike: likeText(contains) }]);
  }
  return Object.fromEntries(out);
}

/**
 * A list (D106): a page of the live rows' views through the repository's
 * findPage. The rows are the ones under the path's parents that the
 * request's filter keeps (tableFilter) and, with a search argument that
 * holds text, whose search fields hold each of its words (D62); the page
 * holds the request's limit of them in its sort's order, the repository's
 * own, by key, without one, after the request's cursor. A cursor is the
 * repository's page key, as encodeCursor writes it; one that names no row
 * of the order is the refused-cursor 400. The router binds a sorted page's
 * next to its sort as it binds an implementation's (bindPage).
 */
export function tableList(
  orm: object,
  operation: TableOperation,
  ctx: RequestContext,
  request: TableRequest,
  page: PageRequest,
  filter: RequestFilter = {}
): Promise<Page<unknown>> {
  return inTransaction(orm, operation, ctx, async repository => {
    const search = operation.search === undefined ? undefined : request.query[operation.search];
    const parents = parentsOf(operation, request);
    const reach = reachOf(operation, columnsOf(repository), ctx);
    // A caller the row rule names in no row lists none.
    if (reach === NO_ROW) return { items: [], next: null };
    // The parents and the row rule hold beside the filter, whatever it says of them.
    const held = [...(Object.keys(parents).length > 0 ? [parents] : []), ...(reach === undefined ? [] : [reach])];
    const rows: Values = {
      ...tableFilter(filter),
      ...(typeof search === 'string' && search !== '' ? { searchText: search } : {}),
      ...(held.length > 0 ? { allOf: held } : {}),
    };
    let after: unknown;
    if (page.cursor !== undefined) {
      after = decodeCursor<unknown>(page.cursor);
      if (after === null || typeof after !== 'object' || Array.isArray(after)) throw refusedCursor();
    }
    const found = await repository.findPage(rows, {
      limit: page.limit,
      ...(page.sort ? { sort: { field: page.sort.field, desc: page.sort.desc } } : {}),
      ...(after !== undefined ? { after } : {}),
    });
    return {
      items: found.rows.map(row => viewOf(operation, row)),
      next: found.next === null || found.next === undefined ? null : encodeCursor(found.next),
    };
  });
}

/**
 * A create (D106): a row of the body's members, one that is absent or
 * null left unset, so its column takes its default, and of each parent the
 * path names; the view of the row as the write left it.
 */
export function tableCreate(orm: object, operation: TableOperation, ctx: RequestContext, request: TableRequest): Promise<unknown> {
  return inTransaction(orm, operation, ctx, async repository => {
    const body = bodyOf(operation, request);
    const input: Values = {};
    for (const member of operation.body ?? []) {
      const value = Object.hasOwn(body, member) ? body[member] : undefined;
      if (value !== undefined && value !== null) input[member] = value;
    }
    for (const parent of operation.parents ?? []) input[parent] = request.path[parent];
    // The row rule's members name the caller in the new row (D108):
    // createdBy as the repository stamps it, each other one set here.
    const columns = columnsOf(repository);
    for (const member of operation.by ?? []) {
      const column = columns.find(candidate => candidate.name === member);
      if (column?.audit !== undefined) continue;
      const subject = ctx.principal?.subject;
      if (subject === undefined || subject === '') throw unauthorized();
      if (column?.uuid === true && uuidValue(subject) === undefined) {
        throw forbidden(`${operation.type}: its ${member} names who it is for by id, and the caller has none`);
      }
      input[member] = subject;
    }
    return viewOf(operation, await repository.createOne(input));
  });
}

/**
 * An update (D106): of the live row under the path's key and parents, 404
 * without one, the members the body sets. A member the request left out
 * leaves its column as it is; one that is null sets a column that may be
 * null to NULL, and is the 400 at that member, validator required, for a
 * column that may not; a body that sets no member writes nothing (D105).
 * It answers the view of the row as the write left it.
 */
export function tableUpdate(orm: object, operation: TableOperation, ctx: RequestContext, request: TableRequest): Promise<unknown> {
  return inTransaction(orm, operation, ctx, async repository => {
    const row = await liveRow(repository, operation, ctx, request);
    const body = bodyOf(operation, request);
    const columns = columnsOf(repository);
    const update: Values = {};
    for (const member of operation.body ?? []) {
      if (!Object.hasOwn(body, member) || body[member] === undefined) continue;
      if (body[member] === null && columns.find(column => column.name === member)?.required) {
        throw memberRefusal(member, 'required', 'required field', undefined);
      }
      update[member] = body[member];
    }
    if (Object.keys(update).length === 0) return viewOf(operation, row);
    return viewOf(operation, await repository.updateOne(row[operation.key!], update));
  });
}

/**
 * A delete (D106): of the live row under the path's key and parents, 404
 * without one, through the repository's deleteOne, which stamps a
 * soft-deletable table's row deleted and removes any other's. It answers
 * the view of the row as it was before.
 */
export function tableDelete(orm: object, operation: TableOperation, ctx: RequestContext, request: TableRequest): Promise<unknown> {
  return inTransaction(orm, operation, ctx, async repository => {
    const row = await liveRow(repository, operation, ctx, request);
    await repository.deleteOne(row[operation.key!]);
    return viewOf(operation, row);
  });
}
