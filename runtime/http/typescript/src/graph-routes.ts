import { covers } from './auth.js';
import type { RequestContext } from './operation.js';
import { HttpProblem, notFound } from './problem.js';
import { uuidValue } from './uuid.js';

/*
The handlers of a generated router's graph operations (D138): the routes
the loader adds for an API's @graphOperations sets, which run each
operation of a version graph through the graph's generated facade in the
database's types package, with no implementation method. The route runs
every step an implemented route runs (the service step, the end-user step
and the permission check of the operation's own permissions among them)
and decodes the request as an implemented route's is; the handler then

- reads the root through its repository: a key that holds no live row, a
  soft-deleted row's among them, is 404 not_found;
- reads the ref or the commit the path names through the graph's own
  repositories: one that does not exist, a discarded ref, and one of
  another root is 404, and so is a ref or a commit the body or the query
  names where the handler reads it (branch's from, merge's into, diff's
  from and to);
- holds an operation that writes a change set to the set's row rule: with
  by createdBy, a ref another caller created is the 404 of one that is not
  there, unless the caller holds the rule's override permission;
- calls the facade, as the caller for a write (withActor), whose subject
  is the actor the graph records, and answers what it returns, each row
  as the API's row type lists it: its entity key and its content members,
  a to-one relation as its key.

A refusal of the graph's engine or core is answered as the problem of its
code: not_found 404; version_conflict, ref_sealed, primary_merge_only,
schema_epoch, name_taken, nothing_to_commit, not_tagged, no_parent,
merge_into_itself and root_mismatch 409; and an input the engine or the
core refuses 422 (invalid_tree with the core's findings as details,
invalid_row, entity_not_found, invalid_arrangement, order_not_a_key,
invalid_resolution, unmatched_resolution and the rest). A write whose
caller has no subject that is a UUID is 403 no_actor. Any other error is
the server's own, answered with none of its words. A merge or a rebase
that finds conflicts answers them, and wrote nothing.

It imports nothing from pg, the repositories or the version-graph
package, so the main entry stays free of them: it takes the repositories
and the facade as values, by the shape of what it calls, and recognizes a
refusal by its code, whichever copy of the engine threw it.
*/

/** The operations of a version graph a set serves (ir.GraphOperationNames). */
export type GraphOperationName =
  | 'branch'
  | 'save'
  | 'arrange'
  | 'commit'
  | 'seal'
  | 'merge'
  | 'rebase'
  | 'revert'
  | 'discard'
  | 'history'
  | 'compose'
  | 'release'
  | 'released'
  | 'materialize'
  | 'diff'
  | 'entityHistory';

/** A field of a member that holds a to-one relation: the row's member that holds its key, the facade's field, and the target's key. */
export interface GraphRelation {
  readonly member: string;
  readonly field: string;
  readonly key: string;
}

/** One member kind of a served graph. */
export interface GraphKind {
  /** The kind as the graph's descriptor names it (line_item). */
  readonly kind: string;
  /** The member of a tree and of the edits that holds its rows, as the facade names it (lineItem). */
  readonly member: string;
  /** The members of its row beside entityKey: the content fields a typed upsert writes. */
  readonly fields: readonly string[];
  /** The fields among them that hold a to-one relation's key. */
  readonly relations?: readonly GraphRelation[];
}

/** A version graph as a generated router serves it (D138): what the handlers read of the graph and its tables. */
export interface GraphSet {
  /** The graph's name, which a refusal names. */
  readonly graph: string;
  /** The root's type, and the member of the repositories that holds its repository. */
  readonly root: string;
  readonly repository: string;
  /** The root's key: the path parameter every route names, and the row's member. */
  readonly key: string;
  /** The members of the repositories that hold the graph's ref and commit tables. */
  readonly refs: string;
  readonly commits: string;
  /** The graph's member kinds. */
  readonly kinds: readonly GraphKind[];
}

/** A graph operation as the generated router carries it (D138): the loader's marker. */
export interface GraphOperation {
  readonly graph: GraphSet;
  readonly operation: GraphOperationName;
  /**
   * The row rule of an operation that writes a change set: the members of
   * the ref that name who may write it (createdBy). Absent admits every
   * caller the route admits.
   */
  readonly by?: readonly string[];
  /** The permission whose holder the row rule does not hold back. */
  readonly overridePermission?: string;
}

/** The request a graph operation's handler takes: what the route decoded of it. */
export interface GraphRequest {
  readonly path: Readonly<Record<string, unknown>>;
  readonly query: Readonly<Record<string, unknown>>;
  /** The body, as the operation's input type's parser read it. */
  readonly input?: unknown;
}

type Values = Record<string, unknown>;
type Row = Readonly<Record<string, unknown>>;

/**
 * A graph's generated facade, as the handlers call it (the version-graph
 * package's VersionGraphFacade): each method by the shape of its
 * arguments and its result. A generated <Name>Graph is one.
 */
export interface GraphFacade {
  withActor(actor: string): GraphFacade;
  createPrimary(root: string, name: string): Promise<unknown>;
  branch(fromRef: string, name: string): Promise<unknown>;
  save(ref: string, version: number, edits: never): Promise<{ readonly ref: unknown; readonly saved: unknown }>;
  arrange(ref: string, version: number, kind: never, parent: string | null | undefined, keys: readonly string[]): Promise<{ readonly ref: unknown; readonly saved: unknown }>;
  commit(ref: string, version: number, options?: { message?: string; tag?: boolean }): Promise<unknown>;
  seal(ref: string, version: number): Promise<unknown>;
  merge(source: string, target: string, targetVersion: number, resolutions?: never, options?: { message?: string; tag?: boolean }): Promise<unknown>;
  rebase(draft: string, version: number, resolutions?: never): Promise<unknown>;
  revert(ref: string, version: number, toCommit: string): Promise<unknown>;
  release(root: string, commit: string, version: number): Promise<unknown>;
  released(root: string): Promise<{ readonly release: unknown; readonly tree: unknown }>;
  materialize(commit: string): Promise<unknown>;
  compose(ref: string): Promise<unknown>;
  diff(from: string, to: string): Promise<unknown>;
  history(ref: string): Promise<unknown>;
  entityHistory(root: string, kind: never, key: string): Promise<unknown>;
  discard(ref: string, version: number): Promise<void>;
}

/** A table's repository, as the handlers read it (orm.ts's Repository). */
interface RepositoryShape {
  findOne(filter: Values): Promise<Row | null>;
}

/** The name of the primary line branch creates for a root that has none. */
export const GRAPH_PRIMARY_NAME = 'main';

/** The refusals of the graph's engine and core the handlers answer, by code, with the status of each. */
const REFUSALS: Readonly<Record<string, number>> = {
  no_actor: 403,
  not_found: 404,
  version_conflict: 409,
  ref_sealed: 409,
  primary_merge_only: 409,
  schema_epoch: 409,
  name_taken: 409,
  nothing_to_commit: 409,
  not_tagged: 409,
  no_parent: 409,
  merge_into_itself: 409,
  root_mismatch: 409,
  invalid_tree: 422,
  entity_not_found: 422,
  invalid_row: 422,
  unknown_kind: 422,
  duplicate_entity_key: 422,
  order_out_of_range: 422,
  invalid_resolution: 422,
  unmatched_resolution: 422,
  no_order_key: 422,
  order_not_a_key: 422,
  invalid_arrangement: 422,
};

/** A refusal of the graph's engine or core, as its errors hold one: a code, and for an invalid tree the core's findings. */
interface Refusal {
  readonly code: string;
  readonly message: string;
  readonly findings?: unknown;
}

/**
 * The refusal an error is, or is caused by: an engine error's or a core
 * error's, by its code, or a value a canonical rule refuses, which is a
 * row that does not fit its columns; undefined for any other error.
 */
function refusalOf(error: unknown): Refusal | undefined {
  for (let candidate = error, depth = 0; candidate !== null && typeof candidate === 'object' && depth < 3; depth++) {
    const { code, message, name, findings, detail } = candidate as { code?: unknown; message?: unknown; name?: unknown; findings?: unknown; detail?: unknown };
    if (typeof code === 'string' && typeof message === 'string' && Object.hasOwn(REFUSALS, code)) {
      return { code, message, ...(Array.isArray(findings) ? { findings } : {}) };
    }
    if (name === 'CanonicalError' && typeof detail === 'string' && typeof message === 'string') {
      return { code: 'invalid_row', message };
    }
    candidate = (candidate as { cause?: unknown }).cause;
  }
  return undefined;
}

/** The code of the graph's refusal an error is, or undefined. */
function refusalCode(error: unknown): string | undefined {
  return refusalOf(error)?.code;
}

/**
 * Runs a graph operation's work and resolves to its result. A refusal of
 * the graph's engine or core is thrown as the HttpProblem of its code's
 * status, with its code, its message and, for an invalid tree, the core's
 * findings as details. A problem the handler threw is rethrown as it is,
 * and so is any other error, which the route answers as its internal
 * error, with none of the error's words.
 */
async function answered<T>(run: () => Promise<T>): Promise<T> {
  try {
    return await run();
  } catch (error) {
    if (error instanceof HttpProblem) throw error;
    const refusal = refusalOf(error);
    if (!refusal) throw error;
    throw new HttpProblem(REFUSALS[refusal.code] ?? 500, refusal.message, {
      code: refusal.code,
      ...(refusal.findings !== undefined ? { details: { findings: refusal.findings } } : {}),
      cause: error,
    });
  }
}

/** The repository of the repositories' member name, or the generator's error. */
function repositoryOf(orm: object, set: GraphSet, member: string): RepositoryShape {
  const repository = (orm as Readonly<Record<string, RepositoryShape | undefined>>)[member];
  if (!repository || typeof repository.findOne !== 'function') {
    throw new Error(`graph operation: the repositories hold no ${member}, a table of the ${set.graph} graph; rebuild the database's types`);
  }
  return repository;
}

/** A UUID the route decoded, or the generator's error. */
function idOf(value: unknown, what: string): string {
  if (typeof value !== 'string' || value === '') throw new Error(`graph operation: the route decoded no ${what}`);
  return value;
}

/** The root's key the path names, once its live row is read: 404 for a key that holds none. */
async function rootOf(orm: object, set: GraphSet, request: GraphRequest): Promise<string> {
  const key = idOf(request.path[set.key], `${set.key}, the key of ${set.root}`);
  const row = await repositoryOf(orm, set, set.repository).findOne({ [set.key]: { eq: key } });
  if (row === null) throw notFound(`${set.root} not found`);
  return key;
}

/** Whether the request's caller holds the permission. */
function holds(ctx: RequestContext, permission: string | undefined): boolean {
  return permission !== undefined && (ctx.principal?.permissions.some(granted => covers(granted, permission)) ?? false);
}

/**
 * The live ref id of root, or the 404: one that does not exist, a
 * discarded one and one of another root. With owned, the operation's row
 * rule holds it too (D138): a ref none of the rule's members names the
 * caller in is the same 404, unless the caller holds the override
 * permission.
 */
async function refOf(orm: object, operation: GraphOperation, ctx: RequestContext, root: string, id: string, owned: boolean): Promise<Row> {
  const set = operation.graph;
  const filter: Values = { id: { eq: id }, rootId: { eq: root } };
  const by = owned ? (operation.by ?? []) : [];
  if (by.length > 0 && !holds(ctx, operation.overridePermission)) {
    const subject = ctx.principal?.subject;
    if (subject === undefined || uuidValue(subject) === undefined) throw notFound('ref not found');
    filter.anyOf = by.map(member => ({ [member]: { eq: subject } }));
  }
  const row = await repositoryOf(orm, set, set.refs).findOne(filter);
  if (row === null) throw notFound('ref not found');
  return row;
}

/** The ref the path names, held to root and, with owned, to the row rule. */
function pathRef(orm: object, operation: GraphOperation, ctx: RequestContext, request: GraphRequest, root: string, owned: boolean): Promise<Row> {
  return refOf(orm, operation, ctx, root, idOf(request.path.ref, 'ref'), owned);
}

/** The commit id of root, or the 404: one that does not exist and one of another root. */
async function commitOf(orm: object, set: GraphSet, root: string, id: string): Promise<string> {
  const row = await repositoryOf(orm, set, set.commits).findOne({ id: { eq: id }, rootId: { eq: root } });
  if (row === null) throw notFound('commit not found');
  return id;
}

/**
 * The facade a write runs through: the graph's, as the request's caller,
 * whose subject the graph records as the write's actor. A request with no
 * caller, and a caller whose subject is no UUID, is 403 no_actor.
 */
function writing(graph: GraphFacade, ctx: RequestContext): GraphFacade {
  const subject = ctx.principal?.subject;
  if (subject === undefined || uuidValue(subject) === undefined) {
    throw new HttpProblem(403, "a write to a version graph records its caller, and the request's caller has no subject that is a UUID", { code: 'no_actor' });
  }
  return graph.withActor(subject);
}

/** The body's members, as its input type's parser read them. */
function bodyOf(operation: GraphOperation, request: GraphRequest): Row {
  const input = request.input;
  if (input === null || typeof input !== 'object' || Array.isArray(input)) {
    throw new Error(`graph operation: ${operation.operation} of the ${operation.graph.graph} graph takes its body's members, and the route decoded none`);
  }
  return input as Row;
}

/** A member of the body that is set, or undefined: null is its absence. */
function given<T>(value: unknown): T | undefined {
  return value === null || value === undefined ? undefined : (value as T);
}

/** The typed value the facade takes for a row the API's types hold: its members, each relation's key as the object that holds it. */
function facadeValue(kind: GraphKind, row: unknown): Values {
  const members = row !== null && typeof row === 'object' ? (row as Row) : {};
  const out: Values = {};
  if (given(members.entityKey) !== undefined) out.entityKey = members.entityKey;
  for (const field of kind.fields) {
    const relation = kind.relations?.find(candidate => candidate.member === field);
    const value = members[field];
    if (relation) {
      out[relation.field] = value === null || value === undefined ? null : { [relation.key]: value };
    } else if (value !== undefined) {
      out[field] = value;
    }
  }
  return out;
}

/** The row the API's types hold of a typed value of the facade's: its entity key and its content members. */
function apiRow(kind: GraphKind, value: unknown): Values {
  const members = value !== null && typeof value === 'object' ? (value as Row) : {};
  const out: Values = { entityKey: members.entityKey };
  for (const field of kind.fields) {
    const relation = kind.relations?.find(candidate => candidate.member === field);
    if (relation) {
      const target = members[relation.field];
      out[field] = target !== null && typeof target === 'object' ? ((target as Row)[relation.key] ?? null) : null;
    } else {
      out[field] = members[field];
    }
  }
  return out;
}

/** Each kind's rows of a tree the facade returned, as the API's row types hold them. */
function rowsOf(set: GraphSet, tree: unknown): Values {
  const members = tree !== null && typeof tree === 'object' ? (tree as Row) : {};
  const out: Values = {};
  for (const kind of set.kinds) {
    const rows = members[kind.member];
    out[kind.member] = Array.isArray(rows) ? rows.map(row => apiRow(kind, row)) : [];
  }
  return out;
}

/** A tree the facade returned, as the API's tree type holds it: each kind's rows, its content hash and its findings. */
function treeOf(set: GraphSet, tree: unknown): Values {
  const members = tree !== null && typeof tree === 'object' ? (tree as Row) : {};
  return { ...rowsOf(set, tree), contentHash: members.contentHash ?? '', findings: Array.isArray(members.findings) ? members.findings : [] };
}

/** The edits the facade takes for the body's: each kind's upserts as its typed values, its deletes and its unsets. */
function editsOf(set: GraphSet, edits: unknown): Values {
  const members = edits !== null && typeof edits === 'object' ? (edits as Row) : {};
  const out: Values = {};
  for (const kind of set.kinds) {
    const own = given<Row>(members[kind.member]);
    if (own === undefined) continue;
    out[kind.member] = {
      upsert: (given<readonly unknown[]>(own.upsert) ?? []).map(row => facadeValue(kind, row)),
      delete: given<readonly string[]>(own.delete) ?? [],
      unset: given<readonly string[]>(own.unset) ?? [],
    };
  }
  return out;
}

/** The resolutions the facade takes for the body's: each with the side it takes or the value it gives. */
function resolutionsOf(resolutions: unknown): Values[] {
  return (given<readonly Row[]>(resolutions) ?? []).map(resolution => {
    const out: Values = { kind: resolution.kind, entityKey: resolution.entityKey, path: resolution.path };
    if (given(resolution.take) !== undefined) out.take = resolution.take;
    if (given(resolution.value) !== undefined) out.value = resolution.value;
    return out;
  });
}

/** A commit's options the body names: its message and whether it is tagged. */
function commitOptions(body: Row): { message?: string; tag?: boolean } {
  const message = given<string>(body.message);
  const tag = given<boolean>(body.tag);
  return { ...(message !== undefined ? { message } : {}), ...(tag !== undefined ? { tag } : {}) };
}

/** The root's primary line, or null for a root that has none. */
function primaryOf(orm: object, set: GraphSet, root: string): Promise<Row | null> {
  return repositoryOf(orm, set, set.refs).findOne({ rootId: { eq: root }, parentRefId: { isNull: true } });
}

/**
 * Branch (D138): a change set of the ref the body's from names, a ref of
 * the root, or of the root's primary line without one. A root with no
 * primary line gets one first, named main, as the caller; when another
 * request made it meanwhile, that one is branched.
 */
export function graphBranch(orm: object, graph: GraphFacade, operation: GraphOperation, ctx: RequestContext, request: GraphRequest): Promise<unknown> {
  return answered(async () => {
    const set = operation.graph;
    const root = await rootOf(orm, set, request);
    const body = bodyOf(operation, request);
    const facade = writing(graph, ctx);
    let from = given<string>(body.from);
    if (from !== undefined) {
      await refOf(orm, operation, ctx, root, from, false);
    } else {
      let primary = await primaryOf(orm, set, root);
      if (primary === null) {
        try {
          await facade.createPrimary(root, GRAPH_PRIMARY_NAME);
        } catch (error) {
          if (refusalCode(error) !== 'name_taken') throw error;
        }
        primary = await primaryOf(orm, set, root);
        if (primary === null) throw new Error(`graph operation: the ${set.graph} graph made no primary line of ${set.root} ${root}`);
      }
      from = idOf(primary.id, "primary line's id");
    }
    return facade.branch(from, idOf(body.name, 'name'));
  });
}

/** Save (D138): the body's edits applied to a change set at its version, and the rows written. */
export function graphSave(orm: object, graph: GraphFacade, operation: GraphOperation, ctx: RequestContext, request: GraphRequest): Promise<unknown> {
  return answered(async () => {
    const set = operation.graph;
    const root = await rootOf(orm, set, request);
    const ref = await pathRef(orm, operation, ctx, request, root, true);
    const body = bodyOf(operation, request);
    const saved = await writing(graph, ctx).save(idOf(ref.id, 'ref'), body.version as number, editsOf(set, body.edits) as never);
    return { ref: saved.ref, saved: rowsOf(set, saved.saved) };
  });
}

/** Arrange (D138): one parent's children of a kind put in the order the body's keys give, and the rows written. */
export function graphArrange(orm: object, graph: GraphFacade, operation: GraphOperation, ctx: RequestContext, request: GraphRequest): Promise<unknown> {
  return answered(async () => {
    const set = operation.graph;
    const root = await rootOf(orm, set, request);
    const ref = await pathRef(orm, operation, ctx, request, root, true);
    const body = bodyOf(operation, request);
    const kind = set.kinds.find(candidate => candidate.kind === body.kind);
    if (!kind) throw new HttpProblem(422, `the ${set.graph} graph has no kind ${String(body.kind)}`, { code: 'unknown_kind' });
    const arranged = await writing(graph, ctx).arrange(
      idOf(ref.id, 'ref'),
      body.version as number,
      kind.member as never,
      given<string>(body.parent) ?? null,
      given<readonly string[]>(body.keys) ?? []
    );
    return { ref: arranged.ref, saved: rowsOf(set, arranged.saved) };
  });
}

/** Commit (D138): a change set's changes committed at its version. */
export function graphCommit(orm: object, graph: GraphFacade, operation: GraphOperation, ctx: RequestContext, request: GraphRequest): Promise<unknown> {
  return answered(async () => {
    const root = await rootOf(orm, operation.graph, request);
    const ref = await pathRef(orm, operation, ctx, request, root, true);
    const body = bodyOf(operation, request);
    return writing(graph, ctx).commit(idOf(ref.id, 'ref'), body.version as number, commitOptions(body));
  });
}

/** Seal (D138): a change set committed when it has changes, and sealed. */
export function graphSeal(orm: object, graph: GraphFacade, operation: GraphOperation, ctx: RequestContext, request: GraphRequest): Promise<unknown> {
  return answered(async () => {
    const root = await rootOf(orm, operation.graph, request);
    const ref = await pathRef(orm, operation, ctx, request, root, true);
    return writing(graph, ctx).seal(idOf(ref.id, 'ref'), bodyOf(operation, request).version as number);
  });
}

/**
 * Merge (D138): the path's change set merged into the ref the body's into
 * names, a ref of the root, or into its parent without one, at that ref's
 * version. Conflicts left after the body's resolutions are answered, and
 * nothing was written. The merge writes its target, so a target that is a
 * change set is held to the row rule as a save to it is: another caller's
 * is the 404. A primary line, which no caller owns, takes a merge from
 * whoever holds the operation's permission, and so does any source: the
 * merge reads it and writes nothing to it.
 */
export function graphMerge(orm: object, graph: GraphFacade, operation: GraphOperation, ctx: RequestContext, request: GraphRequest): Promise<unknown> {
  return answered(async () => {
    const root = await rootOf(orm, operation.graph, request);
    const source = await pathRef(orm, operation, ctx, request, root, false);
    const body = bodyOf(operation, request);
    const facade = writing(graph, ctx);
    let into = given<string>(body.into);
    if (into === undefined) {
      into = given<string>(source.parentRefId);
      if (into === undefined) {
        throw new HttpProblem(409, 'the ref is a primary line, which has no parent to merge into: name the ref to merge into', { code: 'no_parent' });
      }
    }
    const target = await refOf(orm, operation, ctx, root, into, false);
    if (given(target.parentRefId) !== undefined) await refOf(orm, operation, ctx, root, into, true);
    return facade.merge(idOf(source.id, 'ref'), into, body.version as number, resolutionsOf(body.resolutions) as never, commitOptions(body));
  });
}

/** Rebase (D138): a change set moved onto its parent's head at its version. Conflicts left after the body's resolutions are answered, and nothing was written. */
export function graphRebase(orm: object, graph: GraphFacade, operation: GraphOperation, ctx: RequestContext, request: GraphRequest): Promise<unknown> {
  return answered(async () => {
    const root = await rootOf(orm, operation.graph, request);
    const ref = await pathRef(orm, operation, ctx, request, root, true);
    const body = bodyOf(operation, request);
    return writing(graph, ctx).rebase(idOf(ref.id, 'ref'), body.version as number, resolutionsOf(body.resolutions) as never);
  });
}

/** Revert (D138): the rows that make a change set compose to the tree of the body's commit, committed. */
export function graphRevert(orm: object, graph: GraphFacade, operation: GraphOperation, ctx: RequestContext, request: GraphRequest): Promise<unknown> {
  return answered(async () => {
    const root = await rootOf(orm, operation.graph, request);
    const ref = await pathRef(orm, operation, ctx, request, root, true);
    const body = bodyOf(operation, request);
    return writing(graph, ctx).revert(idOf(ref.id, 'ref'), body.version as number, idOf(body.toCommit, 'toCommit'));
  });
}

/**
 * Discard (D138): a change set discarded at its version; true once it is.
 * A primary line is 409 primary_merge_only: discarding it would leave the
 * root a line the next branch makes again, empty.
 */
export function graphDiscard(orm: object, graph: GraphFacade, operation: GraphOperation, ctx: RequestContext, request: GraphRequest): Promise<unknown> {
  return answered(async () => {
    const root = await rootOf(orm, operation.graph, request);
    const ref = await pathRef(orm, operation, ctx, request, root, true);
    if (given(ref.parentRefId) === undefined) {
      throw new HttpProblem(409, 'the ref is a primary line, which is never discarded: discard a change set of it', { code: 'primary_merge_only' });
    }
    await writing(graph, ctx).discard(idOf(ref.id, 'ref'), bodyOf(operation, request).version as number);
    return true;
  });
}

/** History (D138): the commits a ref wrote, newest first. */
export function graphHistory(orm: object, graph: GraphFacade, operation: GraphOperation, ctx: RequestContext, request: GraphRequest): Promise<unknown> {
  return answered(async () => {
    const root = await rootOf(orm, operation.graph, request);
    const ref = await pathRef(orm, operation, ctx, request, root, false);
    return graph.history(idOf(ref.id, 'ref'));
  });
}

/** Compose (D138): a ref's tree, with its content hash and the problems found in it. */
export function graphCompose(orm: object, graph: GraphFacade, operation: GraphOperation, ctx: RequestContext, request: GraphRequest): Promise<unknown> {
  return answered(async () => {
    const set = operation.graph;
    const root = await rootOf(orm, set, request);
    const ref = await pathRef(orm, operation, ctx, request, root, false);
    return treeOf(set, await graph.compose(idOf(ref.id, 'ref')));
  });
}

/** Release (D138): the root's release pointed at the body's commit, at the pointer's version. */
export function graphRelease(orm: object, graph: GraphFacade, operation: GraphOperation, ctx: RequestContext, request: GraphRequest): Promise<unknown> {
  return answered(async () => {
    const root = await rootOf(orm, operation.graph, request);
    const body = bodyOf(operation, request);
    return writing(graph, ctx).release(root, idOf(body.commit, 'commit'), body.version as number);
  });
}

/** Released (D138): the root's release pointer and the tree of the commit it names; 404 for a root never released. */
export function graphReleased(orm: object, graph: GraphFacade, operation: GraphOperation, _ctx: RequestContext, request: GraphRequest): Promise<unknown> {
  return answered(async () => {
    const set = operation.graph;
    const root = await rootOf(orm, set, request);
    const released = await graph.released(root);
    return { release: released.release, tree: treeOf(set, released.tree) };
  });
}

/** Materialize (D138): the tree of the path's commit, a commit of the root. */
export function graphMaterialize(orm: object, graph: GraphFacade, operation: GraphOperation, _ctx: RequestContext, request: GraphRequest): Promise<unknown> {
  return answered(async () => {
    const set = operation.graph;
    const root = await rootOf(orm, set, request);
    const commit = await commitOf(orm, set, root, idOf(request.path.commit, 'commit'));
    return treeOf(set, await graph.materialize(commit));
  });
}

/** Diff (D138): the entities the trees of the query's two commits differ on, each a commit of the root. */
export function graphDiff(orm: object, graph: GraphFacade, operation: GraphOperation, _ctx: RequestContext, request: GraphRequest): Promise<unknown> {
  return answered(async () => {
    const set = operation.graph;
    const root = await rootOf(orm, set, request);
    const from = await commitOf(orm, set, root, idOf(request.query.from, 'from'));
    const to = await commitOf(orm, set, root, idOf(request.query.to, 'to'));
    return graph.diff(from, to);
  });
}

/** Entity history (D138): the commits of the root's primary line that changed one entity, newest first; 404 for a kind the graph has none of. */
export function graphEntityHistory(orm: object, graph: GraphFacade, operation: GraphOperation, _ctx: RequestContext, request: GraphRequest): Promise<unknown> {
  return answered(async () => {
    const set = operation.graph;
    const root = await rootOf(orm, set, request);
    const kind = set.kinds.find(candidate => candidate.kind === request.path.kind);
    if (!kind) throw notFound(`the ${set.graph} graph has no kind ${String(request.path.kind)}`);
    return graph.entityHistory(root, kind.kind as never, idOf(request.path.key, 'key'));
  });
}
