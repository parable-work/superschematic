import { reachesOf, rolesByName } from './access.js';
import type { Principal } from './auth.js';
import { accessCallerOf } from './caller.js';
import { audienceConditions, ormDialect, type PlainRepository } from './orm.js';
import { postgresQuery, searchPlan } from './search-query.js';
import {
  indexSearch,
  rankIndex,
  scopeForms,
  searchSourcesOf,
  type OrmShape,
  type SearchEmbeddings,
  type SearchHit,
  type SearchIndexing,
  type SearchOptions,
  type SearchRequest,
  type SearchableDefinition,
} from './search-tables.js';
import { idKey, uuidFromDb, uuidToDb } from './uuid.js';

/*
One search index per environment (#740): a search across the databases of
an environment ranks one index table, the environment's, which the stack's
search database holds (@stack({ search }), outputs.sql.search.environmentIndex).

Each source database (outputs.sql.search.environment) keeps its own index
as D155 builds it, and its worker copies each index row into the
environment's index after its commit (indexEnvironmentSearch): the row as
its database's index row holds it, where it came from (the database, the
table, the key and the object, "row"), its reach, and a version. Its
tables' triggers enqueue every write of a row (a delete, a soft delete, a
change of key, an audience), and its grants table's trigger every write of
a grant, so every change reaches the copy once the worker drains.

- The copy of a row runs in a transaction of the source database: it takes
  the database's grants lock shared, and an advisory lock of the row, reads
  the row's index row and nextval of the sequence search_environment_version,
  computes the row's reach, and writes the environment row, an upsert that
  takes effect only over a lower version, before it commits. So a row's
  copies are written in the order they were read, and a late write never
  overwrites a newer one. A row that is gone becomes a tombstone at its
  version: no text, no embedding, an empty audience and no reach, which no
  search finds and which holds off a late write.
- A row's reach is what its database's grants reach it by, each grant that
  is not revoked and not ended by its date: "<grant>" on the grant's own
  target, "<grant>:<Type>:<path>" on a row one of its role's paths reaches,
  read through the source's repositories as the system, and
  "<grant>:<Type>:*" on every row of a type the role reaches whole. Whether a
  grant is live now stays the authenticator's.
- The copy of a grant takes the grants lock exclusive, takes the grant's
  entries out of every row of its database, and, for a grant that is not
  revoked or ended, adds them to the rows its target and its paths reach.

searchEnvironment ranks the environment's index with the query a
database's search runs (rankIndex), ordered by rank, then database, table
and key: its pages and total are exact, and access is checked inside the
query, on the copied audience and on the reach, whose entries a caller's
grants name (an agent run's narrowed paths, D194, name only those).
*/

/** The environment's search index as the search database's generated orm.ts exports it (environmentSearch, #740). */
export interface EnvironmentSearchDefinition {
  /** The index table, quoted, and the expression of a row's weighed words over it as si. */
  readonly index: string;
  readonly document: string;
  /** The text search configuration its words are in, simple unless set. */
  readonly config?: string;
}

/** The search database: its ORM, which statements run on as the system, and its index's definition. */
export interface EnvironmentIndex {
  readonly orm: object;
  readonly definition: EnvironmentSearchDefinition;
}

/** A source database as its worker copies it: its name in the stack, and the environment's index. */
export interface EnvironmentCopy {
  readonly database: string;
  readonly index: EnvironmentIndex;
}

/** A search of the environment: a search's request, and the databases it keeps to. */
export interface EnvironmentSearchRequest extends SearchRequest {
  /** Only these databases, by name; every source the call names when absent. */
  readonly databases?: readonly string[];
}

/** One hit of the environment's search: a search's hit, and the database its row is of. */
export interface EnvironmentSearchHit extends SearchHit {
  readonly database: string;
}

/** A page of the environment's hits. */
export interface EnvironmentSearchResult {
  readonly items: readonly EnvironmentSearchHit[];
  readonly total: number;
  readonly more: boolean;
  readonly reranked: boolean;
}

/** What the environment's search takes beside the index and the sources. */
export interface EnvironmentSearchOptions {
  /** The permission each table's rows need, by database and then by type name. */
  readonly permissions?: Readonly<Record<string, Readonly<Record<string, string>>>>;
  /** Embeddings, off unless set; the model the rows' copied embeddings are of. */
  readonly embeddings?: SearchEmbeddings;
}

/** The object of a row that indexes a record with its conversation and files. */
export const ENVIRONMENT_OBJECT_ROW = 'row';
/** The sequence of a source database whose nextval versions each copy. */
export const SEARCH_ENVIRONMENT_SEQUENCE = 'search_environment_version';

const MAX_LIMIT = 100;
const MAX_OFFSET = 10_000;
/** The most rows a grant's copy reads of one path at a time. */
const PAGE = 1000;

type Values = Record<string, unknown>;

/** A repository as the copy reads through it. */
type Repository = Pick<PlainRepository, 'def' | 'findOne' | 'findMany'>;

function whole(value: number | undefined, fallback: number, min: number, max: number, what: string): number {
  if (value === undefined) return fallback;
  if (!Number.isInteger(value) || value < min || value > max) throw new TypeError(`searchEnvironment: ${what} is a whole number from ${min} to ${max}, not ${value}`);
  return value;
}

/** The repositories of orm, each by its ORM member. */
function members(orm: object): Map<string, Repository> {
  const out = new Map<string, Repository>();
  for (const [member, value] of Object.entries(orm)) {
    const repository = value as Repository | undefined;
    if (typeof repository === 'object' && repository !== null && typeof repository.def === 'object' && typeof repository.findMany === 'function') out.set(member, repository);
  }
  return out;
}

function repositoryOfType(orm: object, type: string): Repository | undefined {
  for (const repository of members(orm).values()) if (repository.def?.name === type) return repository;
  return undefined;
}

function accessRepository(orm: object, kind: 'grants' | 'roles'): Repository | undefined {
  for (const repository of members(orm).values()) if (repository.def?.access === kind) return repository;
  return undefined;
}

/**
 * The filter of a role's path from table type to a grant's target (D156),
 * as a repository reads it: unit.contract is
 * { unit: { contract: { <key>: { eq: targetId } } } }. undefined for a path
 * that names no relation, or that ends at another table than the target's,
 * as the repositories read it.
 */
function reachFilter(orm: object, type: string, path: string, targetType: string, targetId: string): Values | undefined {
  const byMember = members(orm);
  let at = repositoryOfType(orm, type);
  const steps = path.split('.');
  for (const step of steps) {
    if (!at) return undefined;
    const one = at.def.relations?.find(rel => rel.field === step);
    const many = one ? undefined : at.def.many?.find(rel => rel.name === step);
    const member = one?.target ?? many?.target;
    at = member === undefined ? undefined : byMember.get(member);
  }
  if (!at || at.def.name !== targetType) return undefined;
  let filter: Values = { [at.def.key]: { eq: targetId } };
  for (const step of [...steps].reverse()) filter = { [step]: filter };
  return filter;
}

/** A grant as the copy reads it, with its role's reaches. */
interface CopiedGrant {
  readonly id: string;
  readonly targetType: string;
  readonly targetId: string;
  readonly reaches: Readonly<Record<string, readonly string[]>>;
}

/** Whether a grant row reaches rows a search finds: not revoked, not ended by its date, and no calendar feed's, whose token reads its feed alone (D152). */
function copied(grant: Values, now: Date): boolean {
  if (grant.revokedAt !== null && grant.revokedAt !== undefined) return false;
  if (grant.endsAt !== null && grant.endsAt !== undefined && new Date(grant.endsAt as string).getTime() <= now.getTime()) return false;
  const type = String(grant.targetType);
  return grant.holderKind !== 'user' && type !== 'CalendarFeed' && !type.startsWith('CalendarFeed;');
}

/** The grants of the source whose reach the copy holds, with their roles' reaches; none for a database with no grants table. */
async function grantsOf(system: object, where: Values = {}): Promise<CopiedGrant[]> {
  const grants = accessRepository(system, 'grants');
  if (!grants) return [];
  const now = new Date();
  const { rows } = await grants.findMany({ revokedAt: { isNull: true }, ...where }, { count: false, limit: 10000 });
  const kept = rows.filter(row => copied(row, now));
  const roles = await rolesByName(accessRepository(system, 'roles') as never, kept.map(row => row.role));
  return kept.map(row => ({
    id: idKey(row.id),
    targetType: String(row.targetType),
    targetId: String(row.targetId),
    reaches: reachesOf(roles.get(String(row.role))?.reaches as readonly string[] | null | undefined),
  }));
}

/** The entries of one grant's reach a caller's grant names: its target, and each of its paths. */
function entriesOf(grant: CopiedGrant | { readonly id: string; readonly reaches: Readonly<Record<string, readonly string[]>> }): string[] {
  const id = idKey(grant.id);
  const out = [id];
  for (const [type, paths] of Object.entries(grant.reaches)) for (const path of paths) out.push(`${id}:${type}:${path}`);
  return out;
}

/** The reach of the row of table whose key, as its index holds it, is key: the entries of grants that reach it. */
async function reachOfRow(system: object, grants: readonly CopiedGrant[], table: string, definition: SearchableDefinition, key: string): Promise<string[]> {
  const repository = repositoryOfType(system, table);
  if (!repository || grants.length === 0) return [];
  const wire = definition.uuid ? uuidFromDb(key) : key;
  const out: string[] = [];
  for (const grant of grants) {
    if (grant.targetType === table && idKey(grant.targetId) === idKey(wire)) out.push(grant.id);
    for (const path of grant.reaches[table] ?? []) {
      if (path === '*') {
        out.push(`${grant.id}:${table}:*`);
        continue;
      }
      const filter = reachFilter(system, table, path, grant.targetType, grant.targetId);
      if (filter === undefined) continue;
      const { rows } = await repository.findMany({ [repository.def.key]: { eq: wire }, ...filter }, { count: false, limit: 1 });
      if (rows.length > 0) out.push(`${grant.id}:${table}:${path}`);
    }
  }
  return [...new Set(out)].sort();
}

/** The advisory lock keys of a source database's copies: its grants', and one row's. */
const grantsLock = (database: string) => `search_environment:${database}:grants`;
const rowLock = (database: string, table: string, key: string) => `search_environment:${database}:${table}:${key}`;

/** The search database's statement runner, as the system. */
function indexRunner(index: EnvironmentIndex): OrmShape {
  return (index.orm as OrmShape).asSystem();
}

/**
 * Copies the index rows of keys, rows of the @searchable table table of
 * the source system, into the environment's index (#740), one transaction
 * of the source each: the grants lock shared and the row's lock, its index
 * row, its reach and a version, and the write. Answers the keys whose row
 * it copied, the others written as tombstones.
 */
async function copyRows(system: OrmShape, copy: EnvironmentCopy, table: string, definition: SearchableDefinition, keys: readonly string[], grants?: readonly CopiedGrant[]): Promise<Set<string>> {
  const index = indexRunner(copy.index);
  const target = copy.index.definition.index;
  const live = grants ?? (await grantsOf(system));
  const copiedKeys = new Set<string>();
  for (const key of new Set(keys)) {
    await system.transaction(async tx => {
      await tx.query(`SELECT pg_advisory_xact_lock_shared(hashtextextended($1, 0))`, [grantsLock(copy.database)]);
      await tx.query(`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, [rowLock(copy.database, table, key)]);
      const read = await tx.query(
        `SELECT v.version, si.title, si.body, si.comments, si.files, si.scope, si.audience::text[] AS audience, si.agents, si.text_hash, si.embedding, si.embedding_model, si.source_key IS NOT NULL AS present
         FROM (SELECT nextval('${SEARCH_ENVIRONMENT_SEQUENCE}') AS version) AS v
         LEFT JOIN ${definition.index} AS si ON si.source_table = $1 AND si.source_key = $2`,
        [table, key]
      );
      const row = read.rows[0]!;
      const present = row.present === true;
      const reach = present ? await reachOfRow(system, live, table, definition, key) : [];
      if (present) copiedKeys.add(key);
      await index.query(
        `INSERT INTO ${target} AS e (database, source_table, source_key, object, title, body, comments, files, scope, audience, agents, reach, text_hash, embedding, embedding_model, version, indexed_at)
         VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::uuid[], $11, $12::text[], $13, $14::float8[], $15, $16, now())
         ON CONFLICT (database, source_table, source_key, object) DO UPDATE SET
           title = EXCLUDED.title, body = EXCLUDED.body, comments = EXCLUDED.comments, files = EXCLUDED.files, scope = EXCLUDED.scope,
           audience = EXCLUDED.audience, agents = EXCLUDED.agents, reach = EXCLUDED.reach, text_hash = EXCLUDED.text_hash,
           embedding = EXCLUDED.embedding, embedding_model = EXCLUDED.embedding_model, version = EXCLUDED.version, indexed_at = EXCLUDED.indexed_at
         WHERE e.version < EXCLUDED.version`,
        present
          ? [copy.database, table, key, ENVIRONMENT_OBJECT_ROW, row.title ?? '', row.body, row.comments, row.files, row.scope, row.audience, row.agents, reach, row.text_hash, row.embedding, row.embedding_model, String(row.version)]
          : // A tombstone: found by no search, it holds off a late write of the row.
            [copy.database, table, key, ENVIRONMENT_OBJECT_ROW, '', null, null, null, null, [], 'none', [], null, null, null, String(row.version)]
      );
    });
  }
  return copiedKeys;
}

/**
 * Copies the reach of the grant whose key, as the database writes it as
 * text, is key into the environment's index (#740): under the grants lock,
 * exclusive, its entries leave every row of the database, and a grant that
 * is not revoked or ended adds them to the rows its target and its paths
 * reach.
 */
async function copyGrant(system: OrmShape, copy: EnvironmentCopy, key: string): Promise<void> {
  const grants = accessRepository(system, 'grants')!;
  const index = indexRunner(copy.index);
  const target = copy.index.definition.index;
  const id = idKey(key);
  await system.transaction(async tx => {
    await tx.query(`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, [grantsLock(copy.database)]);
    await index.query(
      `UPDATE ${target} SET reach = ARRAY(SELECT e FROM unnest(reach) AS e WHERE e <> $2 AND left(e, length($2) + 1) <> $2 || ':' ORDER BY e)
       WHERE database = $1 AND EXISTS (SELECT 1 FROM unnest(reach) AS e WHERE e = $2 OR left(e, length($2) + 1) = $2 || ':')`,
      [copy.database, id]
    );
    const [grant] = await grantsOf(system, { [grants.def.key]: { eq: key } });
    if (!grant) return;
    const sources = searchSourcesOf(system);
    const add = async (table: string, entry: string, keys: readonly string[] | undefined) => {
      await index.query(
        `UPDATE ${target} SET reach = ARRAY(SELECT DISTINCT e FROM unnest(array_append(COALESCE(reach, '{}'), $3)) AS e ORDER BY e)
         WHERE database = $1 AND source_table = $2 AND object = '${ENVIRONMENT_OBJECT_ROW}' AND text_hash IS NOT NULL${keys === undefined ? '' : ' AND source_key = ANY($4::text[])'}`,
        keys === undefined ? [copy.database, table, entry] : [copy.database, table, entry, keys]
      );
    };
    // Each key as the index holds it: a UUID hyphenated.
    const held = (definition: SearchableDefinition, value: unknown) => (definition.uuid ? uuidToDb(value) : String(value));
    const own = sources.get(grant.targetType);
    if (own) await add(grant.targetType, grant.id, [held(own.definition, grant.targetId)]);
    for (const [table, paths] of Object.entries(grant.reaches)) {
      const source = sources.get(table);
      const repository = repositoryOfType(system, table);
      if (!source || !repository) continue;
      for (const path of paths) {
        const entry = `${grant.id}:${table}:${path}`;
        if (path === '*') {
          await add(table, entry, undefined);
          continue;
        }
        const filter = reachFilter(system, table, path, grant.targetType, grant.targetId);
        if (filter === undefined) continue;
        for (let offset = 0; ; offset += PAGE) {
          const { rows } = await repository.findMany(filter, { count: false, limit: PAGE, offset, orderBy: [{ field: repository.def.key }] });
          if (rows.length > 0) await add(table, entry, rows.map(row => held(source.definition, row[repository.def.key])));
          if (rows.length < PAGE) break;
        }
      }
    }
  });
}

/**
 * What the worker of a source database's @searchable queue runs for each
 * message when the database's index rows are copied into the environment's
 * (#740): indexSearch, as D155 builds the row's index row, then the copy of
 * that row, or of the grant a grants table's message names. Answers what
 * it did: indexed or removed, as indexSearch does, grant, or unknown for a
 * message of no table it copies.
 */
export async function indexEnvironmentSearch(
  orm: object,
  copy: EnvironmentCopy,
  message: SearchIndexing,
  options: SearchOptions = {}
): Promise<'indexed' | 'removed' | 'grant' | 'unknown'> {
  const system = (orm as OrmShape).asSystem();
  if (ormDialect(system) === 'sqlite') throw new Error("indexEnvironmentSearch: an environment's search index is Postgres's alone (#740); a database on SQLite or D1 keeps its own index");
  const grants = accessRepository(system, 'grants');
  if (grants && message.sourceTable === grants.def.name) {
    await copyGrant(system, copy, message.sourceKey);
    return 'grant';
  }
  const done = await indexSearch(orm, message, options);
  if (done === 'unknown') return done;
  const definition = searchSourcesOf(system).get(message.sourceTable)!.definition;
  await copyRows(system, copy, message.sourceTable, definition, [message.sourceKey]);
  return done;
}

/**
 * Copies every index row of the source database orm, or of its tables,
 * into the environment's index again (#740), batch rows at a time, with
 * the reach of its grants as they stand; then deletes the database's rows
 * whose index row is gone, and its tombstones. What a database that joins
 * an environment needs once, and what a change of a relation a grant's
 * path follows, past the row it is on, needs. Answers how many rows it
 * copied.
 */
export async function reindexEnvironmentSearch(orm: object, copy: EnvironmentCopy, options: { readonly tables?: readonly string[]; readonly batch?: number } = {}): Promise<number> {
  const system = (orm as OrmShape).asSystem();
  const batch = options.batch ?? 500;
  const index = indexRunner(copy.index);
  const target = copy.index.definition.index;
  const grants = await grantsOf(system);
  let count = 0;
  for (const [table, { definition }] of searchSourcesOf(system)) {
    if (options.tables && !options.tables.includes(table)) continue;
    const kept: string[] = [];
    let after: string | undefined;
    for (;;) {
      const { rows } = await system.query(
        `SELECT source_key FROM ${definition.index} WHERE source_table = $1${after === undefined ? '' : ' AND source_key > $2'} ORDER BY source_key LIMIT ${batch}`,
        after === undefined ? [table] : [table, after]
      );
      const keys = rows.map(row => String(row.source_key));
      count += (await copyRows(system, copy, table, definition, keys, grants)).size;
      kept.push(...keys);
      if (rows.length < batch) break;
      after = keys[keys.length - 1];
    }
    await index.query(`DELETE FROM ${target} WHERE database = $1 AND source_table = $2 AND (text_hash IS NULL OR NOT (source_key = ANY($3::text[])))`, [copy.database, table, kept]);
  }
  return count;
}

/**
 * Searches the environment's index as principal (#740): the rows of the
 * sources' @searchable tables that match request.q, of those principal may
 * read, best first, one page of them, and how many there are. sources are
 * the environment's databases, each its ORM by its name in the stack:
 * which tables each has, how a hit's key is written, and the permission
 * each table's rows need (options.permissions). The access check is inside
 * the query: the audience on the copied columns, and a caller with grants
 * kept to the rows whose reach holds one of its grants' entries.
 */
export async function searchEnvironment(
  index: EnvironmentIndex,
  sources: Readonly<Record<string, object>>,
  principal: Principal | null,
  request: EnvironmentSearchRequest,
  options: EnvironmentSearchOptions = {}
): Promise<EnvironmentSearchResult> {
  if (typeof request.q !== 'string') throw new TypeError('searchEnvironment: q is the text to search for');
  const limit = whole(request.limit, 20, 1, MAX_LIMIT, 'limit');
  const offset = whole(request.offset, 0, 0, MAX_OFFSET, 'offset');
  const plan = searchPlan(request.q);
  const empty: EnvironmentSearchResult = { items: [], total: 0, more: false, reranked: false };
  if (plan.all.length === 0 && request.vector === undefined) return empty;
  for (const database of request.databases ?? []) {
    if (!(database in sources)) throw new TypeError(`searchEnvironment: ${database} is no database of the environment's search`);
  }
  // Each database's @searchable tables the caller holds the permission of.
  const tables = new Map<string, Map<string, SearchableDefinition>>();
  const known = new Set<string>();
  for (const [database, orm] of Object.entries(sources)) {
    const all = searchSourcesOf((orm as OrmShape).asSystem());
    for (const table of all.keys()) known.add(table);
    if (request.databases && !request.databases.includes(database)) continue;
    const kept = new Map<string, SearchableDefinition>();
    for (const [table, { definition }] of all) {
      if (request.tables && !request.tables.includes(table)) continue;
      const permission = options.permissions?.[database]?.[table];
      if (permission !== undefined && !(principal?.permissions ?? []).includes(permission)) continue;
      kept.set(table, definition);
    }
    if (kept.size > 0) tables.set(database, kept);
  }
  for (const table of request.tables ?? []) {
    if (!known.has(table)) throw new TypeError(`searchEnvironment: ${table} is no @searchable table`);
  }
  if (tables.size === 0) return empty;
  const { index: table, document, config = 'simple' } = index.definition;
  const regconfig = `'${config}'::regconfig`;
  const caller = accessCallerOf(principal);
  const args: unknown[] = [];
  const param = (value: unknown): string => {
    args.push(value);
    return `$${args.length}`;
  };
  const byVector = plan.all.length === 0;
  const query = postgresQuery(plan, regconfig, param);
  const conditions = byVector ? (plan.none.length > 0 ? [`NOT (${document} @@ (${query}))`] : []) : [`${document} @@ q.q`];
  conditions.push(`si.object = ${param(ENVIRONMENT_OBJECT_ROW)}`);
  const branches = [...tables].map(([database, kept]) => `(si.database = ${param(database)} AND si.source_table = ANY(${param([...kept.keys()])}::text[]))`);
  conditions.push(`(${branches.join(' OR ')})`);
  // A caller with grants: the rows whose reach holds an entry of one of them.
  if (caller.grants !== undefined) {
    const entries = [...new Set(caller.grants.flatMap(grant => entriesOf(grant)))];
    if (entries.length === 0) return empty;
    conditions.push(`si.reach && ${param(entries)}::text[]`);
  }
  conditions.push(...audienceConditions({ effective: 'si.audience', agents: 'si.agents' }, caller, args, args.length));
  if (request.scope !== undefined) conditions.push(`si.scope = ANY(${param(scopeForms(request.scope))}::text[])`);
  const ranked = await rankIndex(
    (index.orm as OrmShape).asSystem(),
    { index: table, document, regconfig, databases: true },
    { conditions, args, query, byVector, q: request.q, vector: request.vector, embeddings: options.embeddings, limit, offset },
    'searchEnvironment'
  );
  const items: EnvironmentSearchHit[] = ranked.rows.map(row => {
    const database = row.database!;
    const definition = tables.get(database)?.get(row.table);
    return {
      database,
      table: row.table,
      key: definition?.uuid ? uuidFromDb(row.key) : row.key,
      title: row.title,
      matched: row.matched,
      snippet: row.snippet,
      place: row.place,
    };
  });
  return { items, total: ranked.total, more: offset + items.length < ranked.total, reranked: ranked.reranked };
}
