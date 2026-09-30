// What every generated TypeScript facade of a version graph shares (D19):
// the typed operations over the engine and the Postgres adapter, and the
// conversions between typed values and canonical rows. tsgen writes one
// subclass per graph beside the TypeScript types (versiongraph/<name>.ts):
// its descriptor, its kinds, and the columns each typed value's fields map
// to. The counterpart of the shared declarations the Go ORM generator writes
// into database.go.
//
// A typed value becomes a canonical row through the schema runtime's JSON
// (JSON.stringify of the typed fields, so an instant is its ISO string) and
// the canonical rules; a canonical row becomes a typed value through the
// type's generated parse<Type>FromJSON, so an instant comes back as a Date.
// A value read back has its canonical form: a UUID in base62, a time of day
// as HH:MM:SS, a duration in the scalar core's form.

import { canonicalRow } from "./canonical.js";
import type { Descriptor, Finding, Take } from "./contract.js";
import {
  Engine,
  type Change,
  type CommitOptions,
  type SweepOptions,
  type SweepReport,
  type TreeResult,
} from "./engine.js";
import { init, type VersionGraph } from "./index.js";
import { PostgresAdapter, type Client } from "./postgres.js";
import type { Commit, Ref, Release } from "./storage.js";

export type { Client } from "./postgres.js";
export type { Commit, Ref, Release } from "./storage.js";
export type { CommitOptions, SweepReport } from "./engine.js";
export type { Descriptor, Finding, Take } from "./contract.js";
export { DefaultDiscardGrace, DefaultWalkCeiling, errorCode } from "./engine.js";
export * from "./errors.js";
export { NameTakenError, NotFoundError, VersionConflictError } from "./storage.js";
export { VersionGraphError } from "./index.js";

/** One column of a kind's canonical rows and the typed field it holds. */
export interface FacadeColumn {
  /** The column's name. */
  column: string;
  /** The typed value's field. */
  field: string;
  /**
   * For a to-one relation, the target's key field: the field holds an
   * object whose key field is the column's value.
   */
  relationKey?: string;
  /**
   * True for a column a typed upsert writes: every column but the row id,
   * the version, the ref, the root, the tombstone and the audit columns,
   * which the engine and its adapter write.
   */
  write?: boolean;
}

/** How a facade reads and writes one member kind. */
export interface FacadeKind<T> {
  /** The kind's name in the descriptor. */
  kind: string;
  /** Every column of the kind's rows, with the field it holds. */
  columns: readonly FacadeColumn[];
  /** The type's generated parse<Type>FromJSON: the schema runtime's JSON to a typed value. */
  parse: (json: unknown) => T;
}

/** A generated facade's graph: its descriptor, schema epoch, snapshot interval and kinds. */
export interface FacadeGraph<Kinds> {
  descriptor: Descriptor;
  schemaEpoch: number;
  snapshotEvery: number;
  /** The history_actor_setting naming key the schema was generated with. */
  historyActorSetting: string;
  kinds: { [K in keyof Kinds]: FacadeKind<Kinds[K]> };
}

/**
 * One kind's edits of a ref, applied by a graph's save: every kind's
 * upserts, then its deletes, then its unsets. upsert writes each value as
 * the ref's override of its entity, found by its entity key; a value without
 * one is a new entity, whose key the database generates. delete writes a
 * row that deletes each entity on the ref. unset removes the ref's own row
 * of each entity, so the ref reads the entity through its base again.
 */
export interface GraphEdits<T> {
  upsert?: readonly T[];
  delete?: readonly string[];
  unset?: readonly string[];
}

/** A graph's edits, per kind. */
export type TypedEdits<Kinds> = { [K in keyof Kinds]?: GraphEdits<Kinds[K]> };

/**
 * A tree of a graph: the live rows of each kind, typed, ordered by their
 * order column and then by entity key, with the core's hash of its content
 * and, for a composed ref, the problems compose found, such as a row whose
 * parent is absent.
 */
export type TypedTree<Kinds> = { [K in keyof Kinds]: Kinds[K][] } & {
  contentHash: string;
  findings: Finding[];
};

/**
 * One unit both sides of a merge changed differently, or an edit against a
 * delete (path ""). base, ours and theirs are the unit's canonical values as
 * JSON text, absent where the unit is absent; the authors are each side's
 * author column.
 */
export interface TypedConflict<KindName extends string> {
  kind: KindName;
  entityKey: string;
  path: string;
  base?: string;
  ours?: string;
  theirs?: string;
  oursAuthor?: string;
  theirsAuthor?: string;
}

/**
 * Settles the conflict at path of one entity: take the unit from one side,
 * or give its value as JSON text ("null" is a value).
 */
export type TypedResolution<KindName extends string> =
  | { kind: KindName; entityKey: string; path: string; take: Take; value?: undefined }
  | { kind: KindName; entityKey: string; path: string; value: string; take?: undefined };

/** One entity two commits differ on. row is the entity's canonical row in the later tree, as JSON text, absent for a DELETE. */
export interface TypedChange<KindName extends string, Operation extends string> {
  kind: KindName;
  entityKey: string;
  operation: Operation;
  row?: string;
}

/** A saved ref at its new version and the rows save upserted, in edit order per kind. */
export interface TypedSaveResult<Kinds> {
  ref: Ref;
  saved: TypedTree<Kinds>;
}

/** A ref at its new version and the commit written, null when there was nothing to commit. */
export interface TypedCommitResult {
  ref: Ref;
  commit: Commit | null;
}

/**
 * The target of a merge, or the change set a rebase moved, and the commit
 * written. When conflicts is not empty nothing was written and ref is the
 * ref as it was.
 */
export interface TypedMergeResult<KindName extends string> {
  ref: Ref;
  commit: Commit | null;
  conflicts: TypedConflict<KindName>[];
}

/** A root's release pointer and the tree of the commit it names. */
export interface TypedReleased<Kinds> {
  release: Release;
  tree: TypedTree<Kinds>;
}

/**
 * Configure a maintenance pass. actor is who the pass writes as.
 * discardGrace is how long after a ref is discarded its member rows are
 * kept, in milliseconds (absent is DefaultDiscardGrace); a positive
 * abandonAfter discards every change set with no write for that many
 * milliseconds; pruneBatch caps how many history images of each kind a pass
 * prunes (absent is no cap).
 */
export type GraphSweepOptions = SweepOptions;

/** Configure a facade. */
export interface FacadeOptions {
  /** Who the facade's writes are recorded as: a UUID. A write without one fails with NoActorError. */
  actor?: string;
  /** How many commits a read walks before it fails with WalkCeilingError; absent is DefaultWalkCeiling. */
  walkCeiling?: number;
}

let sharedCore: Promise<VersionGraph> | undefined;

/** The wasm core every facade runs on, instantiated once. */
function core(): Promise<VersionGraph> {
  sharedCore ??= init().catch((err: unknown) => {
    sharedCore = undefined;
    throw err;
  });
  return sharedCore;
}

/**
 * A version graph's typed operations over the engine and the Postgres
 * adapter: refs that hold sparse override rows of each member kind, commits
 * that pin the exact row versions a ref sealed, and merges between refs. A
 * primary line takes writes only from merge; work happens on change sets,
 * which rebase catches up with their parent. Each root's release pointer
 * names one tagged commit. Every operation runs in one transaction of the
 * client, and every write through a ref takes the ref's expected version
 * and fails with VersionConflictError when the ref has moved on.
 */
export class VersionGraphFacade<
  Kinds extends { [K in keyof Kinds]: object },
  KindName extends string = string,
  Operation extends string = Change["operation"],
> {
  readonly #graph: FacadeGraph<Kinds>;
  readonly #client: Client;
  readonly #options: FacadeOptions;
  readonly #byKind: Map<string, [keyof Kinds, FacadeKind<unknown>]>;
  #engine: Promise<Engine> | undefined;

  constructor(graph: FacadeGraph<Kinds>, client: Client, options: FacadeOptions = {}) {
    this.#graph = graph;
    this.#client = client;
    this.#options = options;
    this.#byKind = new Map(
      (Object.keys(graph.kinds) as (keyof Kinds)[]).map((name) => {
        const kind = graph.kinds[name] as FacadeKind<unknown>;
        return [kind.kind, [name, kind]];
      }),
    );
  }

  /** The options this facade was built with. */
  protected get options(): FacadeOptions {
    return this.#options;
  }

  /** The client this facade runs on. */
  protected get client(): Client {
    return this.#client;
  }

  #run(): Promise<Engine> {
    this.#engine ??= core().then((c) => {
      const adapter = new PostgresAdapter(this.#graph.descriptor, {
        historyActorSetting: this.#graph.historyActorSetting,
      });
      const engine = new Engine(c, this.#graph.descriptor, adapter.storage(this.#client), {
        schemaEpoch: this.#graph.schemaEpoch,
        snapshotEvery: this.#graph.snapshotEvery,
      });
      return this.#options.walkCeiling !== undefined ? engine.withWalkCeiling(this.#options.walkCeiling) : engine;
    });
    return this.#engine;
  }

  get #actor(): string {
    return this.#options.actor ?? "";
  }

  /** Creates a primary line of root: a ref with no parent. */
  async createPrimary(root: string, name: string): Promise<Ref> {
    return (await this.#run()).createPrimary(this.#actor, root, name);
  }

  /** Creates a change set of fromRef whose base is fromRef's head. */
  async branch(fromRef: string, name: string): Promise<Ref> {
    return (await this.#run()).branch(this.#actor, fromRef, name);
  }

  /**
   * Applies edits to a change set at version: upserts, then deletes, then
   * unsets, kind by kind. It refuses a sealed ref, and a primary line with
   * PrimaryMergeOnlyError.
   */
  async save(ref: string, version: number, edits: TypedEdits<Kinds>): Promise<TypedSaveResult<Kinds>> {
    const canonical: Record<string, { upsert: string[]; delete: string[]; unset: string[] }> = {};
    for (const name of Object.keys(edits) as (keyof Kinds)[]) {
      const kindEdits = edits[name];
      if (kindEdits === undefined) {
        continue;
      }
      const kind = this.#graph.kinds[name] as FacadeKind<unknown>;
      if (kind === undefined) {
        throw new Error(`version graph: unknown kind ${String(name)}`);
      }
      const upsert = (kindEdits.upsert ?? []).map((value) => this.#row(kind, value));
      const del = [...(kindEdits.delete ?? [])];
      const unset = [...(kindEdits.unset ?? [])];
      if (upsert.length + del.length + unset.length > 0) {
        canonical[kind.kind] = { upsert, delete: del, unset };
      }
    }
    const saved = await (await this.#run()).save(this.#actor, ref, version, canonical);
    return { ref: saved.ref, saved: this.#tree({ tree: saved.saved, contentHash: "", findings: [] }) };
  }

  /**
   * Composes a change set at version, diffs it against its last commit (or
   * its base), writes a commit with a patch per changed entity and moves the
   * ref's head. It rejects with NothingToCommitError when nothing changed,
   * an InvalidTreeError when the composed tree breaks the graph's rules, and
   * PrimaryMergeOnlyError for a primary line.
   */
  async commit(ref: string, version: number, options: CommitOptions = {}): Promise<TypedCommitResult> {
    return (await this.#run()).commit(this.#actor, ref, version, options);
  }

  /** Commits a change set at version when it has changes, and seals it: the ref then refuses writes. */
  async seal(ref: string, version: number): Promise<TypedCommitResult> {
    return (await this.#run()).seal(this.#actor, ref, version);
  }

  /**
   * Merges source's head into target at targetVersion, against source's
   * base. Without conflicts it writes the result onto target and commits it
   * in the same transaction, with options' message and tag. With conflicts
   * left after resolutions it returns them and writes nothing. It is the
   * only write a primary line takes.
   */
  async merge(
    source: string,
    target: string,
    targetVersion: number,
    resolutions: readonly TypedResolution<KindName>[] = [],
    options: CommitOptions = {},
  ): Promise<TypedMergeResult<KindName>> {
    const merged = await (await this.#run()).merge(this.#actor, source, target, targetVersion, resolutions, options);
    return merged as TypedMergeResult<KindName>;
  }

  /**
   * Moves a change set at version onto its parent's head: it merges the
   * parent's head into the change set against the change set's base, with
   * its composed tree, uncommitted work included, as ours. Without
   * conflicts it rewrites the change set's rows over the new base, moves its
   * base and commits on it after its previous head. With conflicts left
   * after resolutions it returns them and writes nothing. A primary line is
   * NoParentError.
   */
  async rebase(
    draft: string,
    version: number,
    resolutions: readonly TypedResolution<KindName>[] = [],
  ): Promise<TypedMergeResult<KindName>> {
    const rebased = await (await this.#run()).rebase(this.#actor, draft, version, resolutions);
    return rebased as TypedMergeResult<KindName>;
  }

  /**
   * Writes the rows that make a change set at version compose to the tree
   * of toCommit, and commits them. History is never rewritten. A primary
   * line is PrimaryMergeOnlyError: revert a change set of it and merge that.
   */
  async revert(ref: string, version: number, toCommit: string): Promise<TypedCommitResult> {
    return (await this.#run()).revert(this.#actor, ref, version, toCommit);
  }

  /**
   * Points root's release at commit, a tagged commit of root, fenced by the
   * pointer's version: 0 for the root's first release. It writes no member
   * rows, so a rollback is a release to an earlier tagged commit, and the
   * pointer's history is the release log. An untagged commit is
   * NotTaggedError.
   */
  async release(root: string, commit: string, version: number): Promise<Release> {
    return (await this.#run()).release(this.#actor, root, commit, version);
  }

  /** Reads root's release pointer and the tree of the commit it names: the released content. A root never released is NotFoundError. */
  async released(root: string): Promise<TypedReleased<Kinds>> {
    const released = await (await this.#run()).released(root);
    return { release: released.release, tree: this.#tree(released) };
  }

  /**
   * Reads a commit's tree: the nearest snapshot on its chain with each later
   * commit's patches laid over it, the nearest winning and a DELETE removing
   * the entity.
   */
  async materialize(commit: string): Promise<TypedTree<Kinds>> {
    return this.#tree(await (await this.#run()).materialize(commit));
  }

  /** Reads a ref's tree: its base commit's tree with the ref's own rows laid over it. */
  async compose(ref: string): Promise<TypedTree<Kinds>> {
    return this.#tree(await (await this.#run()).compose(ref));
  }

  /** Lists the entities the trees of two commits differ on. */
  async diff(from: string, to: string): Promise<TypedChange<KindName, Operation>[]> {
    const changes = await (await this.#run()).diff(from, to);
    return changes as unknown as TypedChange<KindName, Operation>[];
  }

  /** Lists the commits a ref wrote, newest first. */
  async history(ref: string): Promise<Commit[]> {
    return (await this.#run()).history(ref);
  }

  /** Soft-deletes a ref at version, which frees its name. */
  async discard(ref: string, version: number): Promise<void> {
    return (await this.#run()).discard(this.#actor, ref, version);
  }

  /**
   * Runs one maintenance pass of the graph in a transaction of its own, as
   * options.actor rather than the facade's actor, under the graph's sweep
   * lock; while another pass holds the lock it does nothing and reports
   * skipped. It discards change sets idle past options.abandonAfter, deletes
   * the member rows of refs discarded longer ago than options.discardGrace,
   * prunes each kind's history past its retention while keeping every pinned
   * row version, and writes missing snapshots. Nothing calls it unless a
   * service does.
   */
  async sweep(options: GraphSweepOptions): Promise<SweepReport> {
    return (await this.#run()).sweep(options);
  }

  /**
   * Runs a sweep pass now and then once every intervalMs milliseconds until
   * signal aborts, and then rejects with the signal's reason. One replica
   * sweeps at a time: a pass that finds the sweep lock held is skipped.
   * onPass, when given, receives each pass's report or error; an error does
   * not stop the sweeper.
   */
  async runSweeper(
    intervalMs: number,
    options: GraphSweepOptions,
    onPass?: (report: SweepReport | null, err: unknown) => void,
    signal?: AbortSignal,
  ): Promise<never> {
    return (await this.#run()).runSweeper(intervalMs, options, onPass, signal);
  }

  /**
   * The canonical row of a typed value of kind, whose every written column
   * holds the schema runtime's JSON of its field. The engine writes the id,
   * root, ref, tombstone, version and audit columns, and a value without an
   * entity key makes a new entity.
   */
  #row(kind: FacadeKind<unknown>, value: unknown): string {
    if (value === null || typeof value !== "object") {
      throw new Error(`version graph: a ${kind.kind} upsert is an object`);
    }
    const fields = value as Record<string, unknown>;
    const values: Record<string, unknown> = {};
    for (const c of kind.columns) {
      if (c.write !== true) {
        continue;
      }
      let v = fields[c.field];
      if (c.relationKey !== undefined) {
        v = v === null || v === undefined ? null : (v as Record<string, unknown>)[c.relationKey];
      }
      // An unset field is null: an optional field left out, or an entity
      // key left out, which makes a new entity.
      values[c.column] = v ?? null;
    }
    const columns = (this.#graph.descriptor.kinds.find((k) => k.kind === kind.kind)?.columns ?? {}) as Record<string, string>;
    return canonicalRow(columns, JSON.stringify(values));
  }

  /** Types a tree of canonical rows the engine returned. */
  #tree(result: TreeResult): TypedTree<Kinds> {
    const out = { contentHash: result.contentHash, findings: result.findings } as Record<string, unknown>;
    for (const name of Object.keys(this.#graph.kinds)) {
      out[name] = [];
    }
    for (const [kindName, rows] of Object.entries(result.tree)) {
      const entry = this.#byKind.get(kindName);
      if (entry === undefined) {
        throw new Error(`version graph: the engine returned rows of an unknown kind ${kindName}`);
      }
      const [name, kind] = entry;
      out[name as string] = rows.map((row) => this.#value(kind, row));
    }
    return out as TypedTree<Kinds>;
  }

  /** The typed value of a canonical row of kind. */
  #value(kind: FacadeKind<unknown>, row: string): unknown {
    const columns = JSON.parse(row) as Record<string, unknown>;
    const fields: Record<string, unknown> = {};
    for (const c of kind.columns) {
      if (!Object.prototype.hasOwnProperty.call(columns, c.column)) {
        continue;
      }
      const v = columns[c.column];
      fields[c.field] = c.relationKey !== undefined && v !== null ? { [c.relationKey]: v } : v;
    }
    return kind.parse(fields);
  }
}
