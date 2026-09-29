// The TypeScript engine of the version graph (D17, D19): every graph
// operation, written once over a storage adapter (storage.ts) and the
// core's wasm build. It ports the Go engine (runtime/versiongraph/go/engine)
// operation for operation, with the same rules and the same error codes.
//
// The engine reads and writes canonical rows only: JSON objects keyed by
// column name whose values are each column's canonical JSON
// (runtime/versiongraph/README.md), carried as JSON text so a number keeps
// its digits. An adapter normalizes what its database returns; a typed
// facade, such as the one tsgen writes per graph, turns typed values into
// canonical rows and back. Every id the engine takes or returns is a UUID;
// it reads the canonical form (base62) and the hyphenated one, and returns
// the canonical form.
//
// Each operation runs in one transaction of the adapter. Every write takes
// an actor, recorded in the audit columns, and every write through a ref
// takes the ref's expected version and fails with VersionConflictError when
// the ref has moved on.
//
// A primary line (a ref with no parent) takes writes only from merge: work
// happens on a change set, which rebase catches up with its parent's head
// and merge brings back. A root's release pointer names one tagged commit,
// which release moves and released reads. A commit is snapshotted, its full
// pin set stored, when it is tagged, released, or snapshotEvery commits past
// the nearest snapshot on its chain, and materialize stops at the nearest
// snapshot. sweep and runSweeper are the graph's maintenance.

import { CanonicalError, uuidCanonical } from "./canonical.js";
import type { Descriptor, Finding, OperationName, Take } from "./contract.js";
import {
  EngineError,
  EntityNotFoundError,
  HistoryMissingError,
  InvalidTreeError,
  MergeIntoItselfError,
  NoActorError,
  NoParentError,
  NothingToCommitError,
  NotTaggedError,
  PrimaryMergeOnlyError,
  RefSealedError,
  RootMismatchError,
  SchemaEpochError,
  WalkCeilingError,
} from "./errors.js";
import { init, VersionGraphError, type VersionGraph } from "./index.js";
import { compareCodePoints, isJsonObject, JsonNumber, parseJson, stringifyJson, type JsonValue } from "./json.js";
import {
  NotFoundError,
  VersionConflictError,
  type Commit,
  type CommitNode,
  type Patch,
  type Ref,
  type Release,
  type SnapshotEntry,
  type Storage,
  type Tx,
} from "./storage.js";

/** How many commits materialize reads, walking a commit's parents, before it stops with WalkCeilingError. */
export const DefaultWalkCeiling = 4096;

/**
 * How many commits past the nearest snapshot on its chain a commit is
 * snapshotted at, unless EngineOptions.snapshotEvery says otherwise.
 */
export const DefaultSnapshotEvery = 64;

/** How long after a ref is discarded sweep keeps its member rows, in milliseconds: seven days. */
export const DefaultDiscardGrace = 7 * 24 * 60 * 60 * 1000;

/** Configure an Engine. */
export interface EngineOptions {
  /** The graph's schema epoch: every commit records it, and materialize refuses a commit from a newer one. 0 when absent. */
  schemaEpoch?: number;
  /** Bounds a commit walk; absent or not positive is DefaultWalkCeiling. */
  walkCeiling?: number;
  /** The graph's snapshot interval (@versionGraph({ snapshotEvery })); absent or not positive is DefaultSnapshotEvery. */
  snapshotEvery?: number;
  /** The core to run on; absent, Engine.create instantiates the wasm build shipped with the package. */
  core?: VersionGraph;
}

/** A tree of canonical rows (JSON text) by kind. A kind with no rows is absent. */
export type Tree = Record<string, string[]>;

/**
 * One kind's edits of a ref. upsert writes each row (a canonical row as
 * JSON text) as the ref's row of its entity, found by its entity key; a row
 * without one is a new entity, whose key the database generates. delete
 * writes a row that deletes each entity, by entity key, on the ref. unset
 * removes the ref's own row of each entity, so the ref reads the entity
 * through its base again.
 */
export interface KindEdits {
  upsert?: readonly string[];
  delete?: readonly string[];
  unset?: readonly string[];
}

/**
 * A save's edits by kind. Save applies every kind's upserts, then every
 * kind's deletes, then every kind's unsets, each in descriptor order.
 */
export type Edits = Readonly<Record<string, KindEdits>>;

/** A saved ref at its new version and the rows save upserted, as stored, in edit order per kind. */
export interface SaveResult {
  ref: Ref;
  saved: Tree;
}

/** A commit's message and whether it is tagged: a tagged commit takes the root's next sequence. */
export interface CommitOptions {
  message?: string;
  tag?: boolean;
}

/** A ref at its new version and the commit written, null when there was nothing to commit. */
export interface CommitResult {
  ref: Ref;
  commit: Commit | null;
}

/**
 * One unit both sides of a merge changed differently, or an edit against a
 * delete (path ""). base, ours and theirs are the unit's canonical values as
 * JSON text, absent where the unit is absent; the authors are each side's
 * author column.
 */
export interface Conflict {
  kind: string;
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
export type Resolution =
  | { kind: string; entityKey: string; path: string; take: Take; value?: undefined }
  | { kind: string; entityKey: string; path: string; value: string; take?: undefined };

/**
 * The target of a merge, or the change set a rebase moved, and the commit
 * written. When conflicts is not empty nothing was written and ref is the
 * ref as it was.
 */
export interface MergeResult {
  ref: Ref;
  commit: Commit | null;
  conflicts: Conflict[];
}

/**
 * A tree in the core's order (rows by their order column, then by entity
 * key), its content hash, and, for a composed ref, the problems compose
 * found.
 */
export interface TreeResult {
  tree: Tree;
  contentHash: string;
  findings: Finding[];
}

/** A root's release pointer and the tree of the commit it names. */
export interface ReleasedResult extends TreeResult {
  release: Release;
}

/** One entity two trees differ on. row is the later tree's row (JSON text): its tombstone for a DELETE, when it has one. */
export interface Change {
  kind: string;
  entityKey: string;
  operation: "ADD" | "UPDATE" | "DELETE";
  row?: string;
}

/** Configure a maintenance pass. */
export interface SweepOptions {
  /** Who the pass writes as: the discards it makes and the deletes of discarded refs' rows record it. */
  actor: string;
  /** How long after a ref is discarded its member rows are kept, in milliseconds; absent or 0 is DefaultDiscardGrace. */
  discardGrace?: number;
  /** When positive, discards every live change set with no write for that many milliseconds. Absent or 0 discards none. */
  abandonAfter?: number;
  /** Caps how many history images of each kind the pass prunes; absent or 0 is no cap. */
  pruneBatch?: number;
}

/** What one pass did. Each count is keyed by kind and holds only kinds with a nonzero count. */
export interface SweepReport {
  /** True when another pass held the graph's sweep lock, and this one did nothing. */
  skipped: boolean;
  /** How many idle change sets the pass discarded. */
  abandoned: number;
  /** How many discarded refs had member rows the pass deleted. */
  collectedRefs: number;
  /** How many rows of each kind the pass deleted. */
  collectedRows: Record<string, number>;
  /** How many history images of each kind the pass pruned. */
  pruned: Record<string, number>;
  /** How many missing snapshots the pass wrote. */
  snapshots: number;
}

/** The columns of a kind's rows the engine reads. */
interface KindRoles {
  name: string;
  key: string;
  id: string;
  tombstone: string;
  version: string;
}

/** What the engine reads of one row. */
interface RowRoles {
  key: string;
  id: string;
  version: number;
  tombstone: boolean;
}

/** One entity of a tree: its kind and entity key. */
function entity(kind: string, key: string): string {
  return kind + "\u0000" + key;
}

/** A commit's tree as the row versions it holds, by entity. */
type PinSet = Map<string, SnapshotEntry>;

/** Rows of a tree by kind, then by entity key. */
type Index = Map<string, Map<string, string>>;

/** The stable code of an error: an engine error's, the core's for an input it refused, else "". */
export function errorCode(err: unknown): string {
  if (err instanceof EngineError) {
    return err.code;
  }
  if (err instanceof VersionGraphError) {
    return err.code;
  }
  return "";
}

/** Normalizes a UUID argument to its canonical form. */
function id(what: string, value: string): string {
  try {
    return uuidCanonical(value);
  } catch (err) {
    if (err instanceof CanonicalError) {
      throw new CanonicalError("", "uuid", `engine: ${what}: ${err.detail}`);
    }
    throw err;
  }
}

/** Normalizes a write's actor; an empty one is NoActorError. */
function actorID(actor: string): string {
  if (actor === "") {
    throw new NoActorError();
  }
  return id("actor", actor);
}

/** Runs one graph's operations. */
export class Engine {
  readonly #core: VersionGraph;
  readonly #descriptor: string;
  readonly #kinds: KindRoles[];
  readonly #byName: Map<string, KindRoles>;
  readonly #storage: Storage;
  readonly #schemaEpoch: number;
  readonly #walkCeiling: number;
  readonly #snapshotEvery: number;

  /**
   * Returns the engine of the graph descriptor describes (version 2, as
   * JSON text or an object), over storage. Without options.core it
   * instantiates the wasm build shipped with the package. The core checks
   * the descriptor.
   */
  static async create(descriptor: string | Descriptor, storage: Storage, options: EngineOptions = {}): Promise<Engine> {
    const core = options.core ?? (await init());
    return new Engine(core, descriptor, storage, options);
  }

  /** Engine.create with a core already instantiated. */
  constructor(core: VersionGraph, descriptor: string | Descriptor, storage: Storage, options: EngineOptions = {}) {
    const text = typeof descriptor === "string" ? descriptor : JSON.stringify(descriptor);
    core.run("validate", `{"descriptor":${text},"tree":{}}`);
    const parsed = JSON.parse(text) as {
      kinds: { kind: string; key: string; id: string; tombstone: string; version: string }[];
    };
    this.#core = core;
    this.#descriptor = text;
    this.#kinds = parsed.kinds.map((k) => ({
      name: k.kind,
      key: k.key,
      id: k.id,
      tombstone: k.tombstone,
      version: k.version,
    }));
    this.#byName = new Map(this.#kinds.map((k) => [k.name, k]));
    this.#storage = storage;
    this.#schemaEpoch = options.schemaEpoch ?? 0;
    this.#walkCeiling = options.walkCeiling !== undefined && options.walkCeiling > 0 ? options.walkCeiling : DefaultWalkCeiling;
    this.#snapshotEvery =
      options.snapshotEvery !== undefined && options.snapshotEvery > 0 ? options.snapshotEvery : DefaultSnapshotEvery;
  }

  #copy(changes: { storage?: Storage; walkCeiling?: number }): Engine {
    return new Engine(this.#core, this.#descriptor, changes.storage ?? this.#storage, {
      schemaEpoch: this.#schemaEpoch,
      walkCeiling: changes.walkCeiling ?? this.#walkCeiling,
      snapshotEvery: this.#snapshotEvery,
    });
  }

  /** A copy of this engine over storage. */
  withStorage(storage: Storage): Engine {
    return this.#copy({ storage });
  }

  /**
   * A copy of this engine that walks at most n commits to read a commit's
   * tree, and fails with WalkCeilingError past them. n <= 0 is
   * DefaultWalkCeiling.
   */
  withWalkCeiling(n: number): Engine {
    return this.#copy({ walkCeiling: n > 0 ? n : DefaultWalkCeiling });
  }

  #kind(name: string): KindRoles {
    const k = this.#byName.get(name);
    if (k === undefined) {
      throw new Error(`engine: unknown kind ${JSON.stringify(name)}`);
    }
    return k;
  }

  #transact<T>(fn: (tx: Tx) => Promise<T>): Promise<T> {
    return this.#storage.transact(fn);
  }

  /** Creates a primary line of root: a ref with no parent. */
  async createPrimary(actor: string, root: string, name: string): Promise<Ref> {
    actor = actorID(actor);
    root = id("id", root);
    return this.#transact((tx) => tx.createRef({ root, parent: null, base: null, name, actor }));
  }

  /** Creates a change set of fromRef whose base is fromRef's head. */
  async branch(actor: string, fromRef: string, name: string): Promise<Ref> {
    actor = actorID(actor);
    fromRef = id("id", fromRef);
    return this.#transact(async (tx) => {
      const from = await this.#readRef(tx, fromRef, undefined, false);
      return tx.createRef({ root: from.root, parent: from.id, base: from.head, name, actor });
    });
  }

  /**
   * Applies edits to a change set at version. It refuses a sealed ref, and
   * a primary line with PrimaryMergeOnlyError.
   */
  async save(actor: string, ref: string, version: number, edits: Edits): Promise<SaveResult> {
    actor = actorID(actor);
    ref = id("id", ref);
    for (const name of Object.keys(edits)) {
      this.#kind(name);
    }
    return this.#transact(async (tx) => {
      const saved: Tree = {};
      const r = await this.#readDraft(tx, ref, version);
      let deletes = false;
      for (const k of this.#kinds) {
        for (const row of edits[k.name]?.upsert ?? []) {
          const stored = await this.#writeRow(tx, k.name, r, row, false, actor);
          (saved[k.name] ??= []).push(stored);
        }
        deletes ||= (edits[k.name]?.delete?.length ?? 0) > 0;
      }
      if (deletes) {
        const { tree: composed } = await this.#compose(tx, r);
        const byKey = this.#index(composed);
        for (const k of this.#kinds) {
          for (const key of edits[k.name]?.delete ?? []) {
            await this.#deleteEntity(tx, r, byKey, k.name, id("id", key), actor);
          }
        }
      }
      for (const k of this.#kinds) {
        for (const key of edits[k.name]?.unset ?? []) {
          const entityKey = id("id", key);
          const removed = await tx.removeRow(k.name, r.id, entityKey, actor);
          if (!removed) {
            throw new EntityNotFoundError(`entity not found on the ref: no ${k.name} override of ${entityKey}`);
          }
        }
      }
      const moved = await tx.updateRef({ id: r.id, version: r.version, head: null, base: null, seal: false, actor });
      return { ref: moved, saved };
    });
  }

  /**
   * Composes a change set at version, diffs it against its last commit (or
   * its base), writes a commit with a patch per changed entity and moves the
   * ref's head. It rejects with NothingToCommitError when nothing changed,
   * an InvalidTreeError when the composed tree breaks the graph's rules, and
   * PrimaryMergeOnlyError for a primary line, whose commits merge writes.
   */
  commit(actor: string, ref: string, version: number, options: CommitOptions = {}): Promise<CommitResult> {
    return this.#commitRef(actor, ref, version, options, false, false);
  }

  /**
   * Commits a change set at version when it has changes, and seals it: the
   * ref then refuses writes. A primary line is PrimaryMergeOnlyError.
   */
  seal(actor: string, ref: string, version: number): Promise<CommitResult> {
    return this.#commitRef(actor, ref, version, {}, true, true);
  }

  async #commitRef(
    actor: string,
    ref: string,
    version: number,
    options: CommitOptions,
    allowEmpty: boolean,
    seal: boolean,
  ): Promise<CommitResult> {
    actor = actorID(actor);
    ref = id("id", ref);
    return this.#transact(async (tx) => {
      const r = await this.#readDraft(tx, ref, version);
      return this.#commitAndMove(tx, r, options, allowEmpty, seal, actor);
    });
  }

  /**
   * Merges source's head into target at targetVersion, against source's
   * base. Without conflicts it writes the result onto target and commits it
   * in the same transaction, with options' message and tag. With conflicts
   * left after resolutions it returns them and writes nothing. Merge is the
   * only write a primary line takes.
   */
  async merge(
    actor: string,
    source: string,
    target: string,
    targetVersion: number,
    resolutions: readonly Resolution[] = [],
    options: CommitOptions = {},
  ): Promise<MergeResult> {
    actor = actorID(actor);
    source = id("id", source);
    target = id("id", target);
    return this.#transact(async (tx) => {
      const t = await this.#readRef(tx, target, targetVersion, true);
      const s = await this.#readRef(tx, source, undefined, false);
      if (s.root !== t.root) {
        throw new RootMismatchError();
      }
      if (s.id === t.id) {
        throw new MergeIntoItselfError();
      }
      const conflicts = await this.#merge(tx, s, t, resolutions, actor);
      if (conflicts.length > 0) {
        return { ref: t, commit: null, conflicts };
      }
      const moved = await this.#commitAndMove(tx, t, options, true, false, actor);
      return { ...moved, conflicts: [] };
    });
  }

  /**
   * Moves a change set at version onto its parent's head. It merges the
   * parent's head into the change set, with the change set's base as the
   * merge base and its composed tree, uncommitted work included, as ours.
   * With conflicts left after resolutions it returns them and writes
   * nothing. Otherwise it writes the change set's rows so it composes to the
   * merged tree over the parent's head, makes that head its base, and
   * commits on it with its previous head (or, with none, its new base) as
   * the parent, so its history keeps its commits. A primary line has no
   * parent to rebase onto: NoParentError.
   */
  async rebase(actor: string, draft: string, version: number, resolutions: readonly Resolution[] = []): Promise<MergeResult> {
    actor = actorID(actor);
    draft = id("id", draft);
    return this.#transact(async (tx) => {
      const d = await this.#readRef(tx, draft, version, true);
      if (d.parent === null) {
        throw new NoParentError();
      }
      const parent = await this.#readRef(tx, d.parent, undefined, false);
      if (parent.head === d.base) {
        // Already on the parent's head: nothing to merge.
        const moved = await tx.updateRef({ id: d.id, version: d.version, head: null, base: null, seal: false, actor });
        return { ref: moved, commit: null, conflicts: [] };
      }
      const base = await this.#materialize(tx, d.base);
      const theirs = await this.#materialize(tx, parent.head);
      const { tree: ours, own } = await this.#compose(tx, d);
      const merged = this.#coreMerge(base, ours, theirs, resolutions);
      if (merged.conflicts.length > 0) {
        return { ref: d, commit: null, conflicts: merged.conflicts };
      }
      const moved: Ref = { ...d, base: parent.head };
      await this.#overlay(tx, moved, theirs, merged.merged, ours, own, actor);
      let written: Commit | null = null;
      try {
        written = await this.#commit(tx, moved, {}, actor);
      } catch (err) {
        if (!(err instanceof NothingToCommitError)) {
          throw err;
        }
      }
      const ref = await tx.updateRef({
        id: d.id,
        version: d.version,
        head: written?.id ?? null,
        base: parent.head,
        seal: false,
        actor,
      });
      return { ref, commit: written, conflicts: [] };
    });
  }

  /**
   * Writes the rows that make a change set at version compose to the tree
   * of toCommit, and commits them. History is never rewritten. A primary
   * line is PrimaryMergeOnlyError: revert a change set of it and merge that.
   */
  async revert(actor: string, ref: string, version: number, toCommit: string): Promise<CommitResult> {
    actor = actorID(actor);
    ref = id("id", ref);
    toCommit = id("id", toCommit);
    return this.#transact(async (tx) => {
      const r = await this.#readDraft(tx, ref, version);
      const commit = await tx.readCommit(toCommit);
      if (commit.root !== r.root) {
        throw new RootMismatchError();
      }
      const tree = await this.#materialize(tx, toCommit);
      await this.#revert(tx, r, tree, actor);
      return this.#commitAndMove(tx, r, {}, true, false, actor);
    });
  }

  /**
   * Points root's release at commit, a tagged commit of root, fenced by the
   * pointer's version: 0 for the root's first release. It snapshots the
   * commit and writes no member rows, so a rollback is a release to an
   * earlier tagged commit, and the pointer's history is the release log. An
   * untagged commit is NotTaggedError; another root's is RootMismatchError.
   */
  async release(actor: string, root: string, commit: string, version: number): Promise<Release> {
    actor = actorID(actor);
    root = id("id", root);
    commit = id("id", commit);
    return this.#transact(async (tx) => {
      const c = await tx.readCommit(commit);
      if (c.root !== root) {
        throw new RootMismatchError();
      }
      if (c.sequence === null) {
        throw new NotTaggedError();
      }
      await this.#ensureSnapshot(tx, c);
      return tx.writeRelease({ root, commit: c.id, version, actor });
    });
  }

  /** Reads root's release pointer and the tree of the commit it names. A root never released is NotFoundError. */
  async released(root: string): Promise<ReleasedResult> {
    root = id("id", root);
    return this.#transact(async (tx) => {
      const release = await tx.readRelease(root);
      const tree = this.#order(await this.#materialize(tx, release.commit));
      return { release, ...this.#treeResult(tree, []) };
    });
  }

  /**
   * Reads a commit's tree: the nearest snapshot on its chain with each later
   * commit's patches laid over it, the nearest winning and a DELETE removing
   * the entity.
   */
  async materialize(commit: string): Promise<TreeResult> {
    commit = id("id", commit);
    return this.#transact(async (tx) => {
      const tree = this.#order(await this.#materialize(tx, commit));
      return this.#treeResult(tree, []);
    });
  }

  /** Reads a ref's tree: its base commit's tree with the ref's own rows laid over it. */
  async compose(ref: string): Promise<TreeResult> {
    ref = id("id", ref);
    return this.#transact(async (tx) => {
      const r = await this.#readRef(tx, ref, undefined, false);
      const { tree, findings } = await this.#compose(tx, r);
      return this.#treeResult(tree, findings);
    });
  }

  /** Lists the entities the trees of two commits differ on. */
  async diff(from: string, to: string): Promise<Change[]> {
    from = id("id", from);
    to = id("id", to);
    return this.#transact(async (tx) => {
      const fromTree = await this.#materialize(tx, from);
      const toTree = await this.#materialize(tx, to);
      return this.#diff(fromTree, toTree);
    });
  }

  /** Lists the commits a ref wrote, newest first. */
  async history(ref: string): Promise<Commit[]> {
    ref = id("id", ref);
    return this.#transact(async (tx) => {
      const r = await this.#readRef(tx, ref, undefined, false);
      if (r.head === null) {
        return [];
      }
      return tx.refCommits(r.id, r.head, this.#walkCeiling);
    });
  }

  /** Soft-deletes a ref at version, which frees its name. */
  async discard(actor: string, ref: string, version: number): Promise<void> {
    actor = actorID(actor);
    ref = id("id", ref);
    return this.#transact(async (tx) => {
      await this.#readRef(tx, ref, version, false);
      await tx.discardRef(ref, version, actor);
    });
  }

  /**
   * Runs one maintenance pass in one transaction, under the graph's sweep
   * lock; when another pass holds it, sweep does nothing and reports
   * skipped. In order, the pass discards the change sets idle past
   * abandonAfter, leaving one a write reaches after the pass read it;
   * deletes the member rows of refs discarded longer ago than discardGrace,
   * keeping their ref rows and commits as the audit trail; prunes each
   * kind's history past its declared retention, keeping every row version a
   * patch or a snapshot pins; and writes each snapshot the graph's rules
   * call for and it lacks. Nothing calls sweep unless a service does.
   */
  async sweep(options: SweepOptions): Promise<SweepReport> {
    const actor = actorID(options.actor);
    const grace = options.discardGrace !== undefined && options.discardGrace !== 0 ? options.discardGrace : DefaultDiscardGrace;
    return this.#transact(async (tx) => {
      const report: SweepReport = {
        skipped: false,
        abandoned: 0,
        collectedRefs: 0,
        collectedRows: {},
        pruned: {},
        snapshots: 0,
      };
      if (!(await tx.sweepLock())) {
        report.skipped = true;
        return report;
      }
      if (options.abandonAfter !== undefined && options.abandonAfter > 0) {
        for (const ref of await tx.idleDrafts(options.abandonAfter)) {
          // A write that reached the ref after idleDrafts read it moved its
          // version, so the ref is no longer idle: leave it.
          try {
            await tx.discardRef(ref.id, ref.version, actor);
          } catch (err) {
            if (err instanceof VersionConflictError) {
              continue;
            }
            throw err;
          }
          report.abandoned++;
        }
      }
      for (const ref of await tx.discardedRefs(grace)) {
        let rows = 0;
        for (const k of this.#kinds) {
          const n = await tx.removeRefRows(k.name, ref.id, actor);
          if (n > 0) {
            report.collectedRows[k.name] = (report.collectedRows[k.name] ?? 0) + n;
            rows += n;
          }
        }
        if (rows > 0) {
          report.collectedRefs++;
        }
      }
      for (const k of this.#kinds) {
        const n = await tx.prune(k.name, 0, options.pruneBatch ?? 0);
        if (n > 0) {
          report.pruned[k.name] = n;
        }
      }
      report.snapshots = await this.#backfill(tx);
      return report;
    });
  }

  /**
   * Runs a sweep pass now and then once every intervalMs milliseconds until
   * signal aborts, and then rejects with the signal's reason. Each pass
   * takes the graph's sweep lock, so one replica sweeps at a time and a pass
   * that finds it held is skipped. onPass, when given, receives each pass's
   * report or error; an error does not stop the sweeper.
   */
  async runSweeper(
    intervalMs: number,
    options: SweepOptions,
    onPass?: (report: SweepReport | null, err: unknown) => void,
    signal?: AbortSignal,
  ): Promise<never> {
    if (!(intervalMs > 0)) {
      throw new Error("engine: a sweeper's interval must be positive");
    }
    let next = Date.now();
    for (;;) {
      next += intervalMs;
      try {
        const report = await this.sweep(options);
        onPass?.(report, null);
      } catch (err) {
        onPass?.(null, err);
      }
      if (signal?.aborted) {
        throw signal.reason;
      }
      await sleep(Math.max(0, next - Date.now()), signal);
      next = Math.max(next, Date.now());
    }
  }

  /**
   * Reads a ref. With expected set it locks the ref's row and refuses a ref
   * at another version, and with write set a sealed ref.
   */
  async #readRef(tx: Tx, refID: string, expected: number | undefined, write: boolean): Promise<Ref> {
    const ref = expected !== undefined ? await tx.lockRef(refID) : await tx.readRef(refID);
    if (ref.discarded) {
      throw new NotFoundError();
    }
    if (expected !== undefined && ref.version !== expected) {
      throw new VersionConflictError();
    }
    if (write && ref.sealed) {
      throw new RefSealedError();
    }
    return ref;
  }

  /** Reads a ref to write through at version: a live, unsealed change set. A primary line takes writes only from merge. */
  async #readDraft(tx: Tx, refID: string, version: number): Promise<Ref> {
    const ref = await this.#readRef(tx, refID, version, true);
    if (ref.parent === null) {
      throw new PrimaryMergeOnlyError();
    }
    return ref;
  }

  #readRow(kind: KindRoles, raw: string): RowRoles {
    const columns = parseJson(raw);
    if (!isJsonObject(columns)) {
      throw new Error(`engine: decode ${kind.name} row: not an object`);
    }
    const text = (column: string): string => {
      const value = columns.get(column);
      if (value === undefined) {
        throw new Error(`engine: ${kind.name} row ${column}: unexpected end of JSON input`);
      }
      if (value === null) {
        return "";
      }
      if (typeof value !== "string") {
        throw new Error(`engine: ${kind.name} row ${column}: not a string`);
      }
      return value;
    };
    const versionValue = columns.get(kind.version);
    let version = 0;
    if (versionValue === undefined) {
      throw new Error(`engine: ${kind.name} row ${kind.version}: unexpected end of JSON input`);
    } else if (versionValue !== null) {
      if (!(versionValue instanceof JsonNumber) || !/^-?(?:0|[1-9][0-9]*)$/.test(versionValue.text)) {
        throw new Error(`engine: ${kind.name} row ${kind.version}: not an integer`);
      }
      version = Number(versionValue.text);
    }
    let tombstone = false;
    const tombstoneValue = columns.get(kind.tombstone);
    if (tombstoneValue !== undefined && tombstoneValue !== null) {
      if (typeof tombstoneValue !== "boolean") {
        throw new Error(`engine: ${kind.name} row ${kind.tombstone}: not a boolean`);
      }
      tombstone = tombstoneValue;
    }
    return { key: text(kind.key), id: text(kind.id), version, tombstone };
  }

  /** Maps each kind's rows by entity key. */
  #index(tree: Tree): Index {
    const byKey: Index = new Map();
    for (const [name, rows] of Object.entries(tree)) {
      const kind = this.#kind(name);
      const rowsByKey = new Map<string, string>();
      for (const raw of rows) {
        rowsByKey.set(this.#readRow(kind, raw).key, raw);
      }
      byKey.set(name, rowsByKey);
    }
    return byKey;
  }

  /** Reads every row a ref holds, tombstones included. */
  async #ownRows(tx: Tx, ref: string): Promise<Tree> {
    const tree: Tree = {};
    for (const k of this.#kinds) {
      const rows = await tx.rows(k.name, ref);
      if (rows.length > 0) {
        tree[k.name] = rows;
      }
    }
    return tree;
  }

  /**
   * Reads the pin set of a commit's tree: it walks the commit's parents to
   * the nearest snapshot, takes that snapshot's pins, and lays each nearer
   * commit's patches over them, the nearest winning and a DELETE removing
   * the entity. It also returns the commit's distance from the nearest
   * snapshot on its chain: 0 for a snapshotted commit, else how many commits
   * separate them, the snapshot excluded, counting a chain with no snapshot
   * from before its first commit. An empty commit is the empty set at
   * distance 0.
   */
  async #resolve(tx: Tx, commit: string | null): Promise<{ pins: PinSet; distance: number }> {
    const pins: PinSet = new Map();
    if (commit === null || commit === "") {
      return { pins, distance: 0 };
    }
    const chain = await tx.walk(commit, this.#walkCeiling);
    if (chain.length === 0) {
      throw new NotFoundError();
    }
    for (const c of chain) {
      if (c.schemaEpoch > this.#schemaEpoch) {
        throw new SchemaEpochError(
          `the commit is from a newer schema epoch: commit ${c.id} has epoch ${c.schemaEpoch}, this graph ${this.#schemaEpoch}`,
        );
      }
    }
    const last = chain[chain.length - 1]!;
    let distance = chain.length;
    let patched = chain;
    if (last.snapshot) {
      for (const entry of await tx.snapshot(last.id)) {
        pins.set(entity(entry.kind, entry.entityKey), entry);
      }
      distance = chain.length - 1;
      patched = chain.slice(0, -1);
    } else if (last.parent !== null) {
      throw new WalkCeilingError(`the commit walk passed its ceiling: ${this.#walkCeiling} commits`);
    }
    if (patched.length === 0) {
      return { pins, distance };
    }
    const depth = new Map(patched.map((c, i) => [c.id, i]));
    const patches = await tx.patches(patched.map((c) => c.id));
    const nearest = new Map<string, Patch>();
    for (const p of patches) {
      const at = entity(p.kind, p.entityKey);
      const seen = nearest.get(at);
      if (seen === undefined || depth.get(p.commit)! < depth.get(seen.commit)!) {
        nearest.set(at, p);
      }
    }
    applyPatches(pins, nearest);
    return { pins, distance };
  }

  /** Reads the history image of every row version a pin set holds. */
  async #images(tx: Tx, pins: PinSet): Promise<Tree> {
    const byKind = new Map<string, { id: string; version: number }[]>();
    for (const entry of pins.values()) {
      let list = byKind.get(entry.kind);
      if (list === undefined) {
        byKind.set(entry.kind, (list = []));
      }
      list.push({ id: entry.entityId, version: entry.entityVersion });
    }
    const tree: Tree = {};
    for (const k of this.#kinds) {
      const want = byKind.get(k.name) ?? [];
      if (want.length === 0) {
        continue;
      }
      const images = await tx.images(k.name, want);
      if (images.length !== want.length) {
        throw new HistoryMissingError(
          `a row version a commit names is missing from history: ${want.length - images.length} of ${want.length} ${k.name} rows`,
        );
      }
      tree[k.name] = images;
    }
    return tree;
  }

  /** Reads a commit's tree: the row versions its pin set holds, read from history. An empty commit is the empty tree. */
  async #materialize(tx: Tx, commit: string | null): Promise<Tree> {
    return (await this.#materializePins(tx, commit)).tree;
  }

  /** materialize, with the commit's pin set and its distance from the nearest snapshot (see resolve). */
  async #materializePins(tx: Tx, commit: string | null): Promise<{ tree: Tree; pins: PinSet; distance: number }> {
    const { pins, distance } = await this.#resolve(tx, commit);
    const tree = await this.#images(tx, pins);
    return { tree, pins, distance };
  }

  /**
   * Snapshots a commit that has no snapshot yet. Reports whether it wrote
   * one: a commit whose tree is empty has no entries to write.
   */
  async #ensureSnapshot(tx: Tx, commit: { id: string; snapshot: boolean }): Promise<boolean> {
    if (commit.snapshot) {
      return false;
    }
    const { pins } = await this.#resolve(tx, commit.id);
    if (pins.size === 0) {
      return false;
    }
    await tx.insertSnapshot(commit.id, pinEntries(pins));
    return true;
  }

  /** core.compose(materialize(ref.base) or empty, the ref's own rows), with the core's findings and the own rows. */
  async #compose(tx: Tx, ref: Ref): Promise<{ tree: Tree; findings: Finding[]; own: Tree }> {
    const base = await this.#materialize(tx, ref.base);
    const own = await this.#ownRows(tx, ref.id);
    const output = this.#run("compose", `{"descriptor":${this.#descriptor},"base":${treeJson(base)},"overlay":${treeJson(own)}}`);
    return { tree: decodeTree(output.get("tree")!), findings: decodeFindings(output.get("findings")!), own };
  }

  /** tree in the core's order: rows by their order column, then by entity key. */
  #order(tree: Tree): Tree {
    const output = this.#run("compose", `{"descriptor":${this.#descriptor},"base":${treeJson(tree)},"overlay":{}}`);
    return decodeTree(output.get("tree")!);
  }

  #contentHash(tree: Tree): string {
    const output = this.#run("content_hash", `{"descriptor":${this.#descriptor},"tree":${treeJson(tree)}}`);
    return output.get("contentHash") as string;
  }

  #treeResult(tree: Tree, findings: Finding[]): TreeResult {
    return { tree, contentHash: this.#contentHash(tree), findings };
  }

  #diff(from: Tree, to: Tree): Change[] {
    const output = this.#run("diff", `{"descriptor":${this.#descriptor},"from":${treeJson(from)},"to":${treeJson(to)}}`);
    return (output.get("changes") as JsonValue[]).map((value) => {
      const change = value as Map<string, JsonValue>;
      const out: Change = {
        kind: change.get("kind") as string,
        entityKey: change.get("entityKey") as string,
        operation: change.get("operation") as Change["operation"],
      };
      const row = change.get("row");
      if (row !== undefined) {
        out.row = stringifyJson(row);
      }
      return out;
    });
  }

  /** Runs one core operation on a JSON request and parses its output exactly. */
  #run(operation: OperationName, request: string): Map<string, JsonValue> {
    return parseJson(this.#core.run(operation, request)) as Map<string, JsonValue>;
  }

  /**
   * Writes a row onto a ref as the ref's row of its entity: live, or with
   * tombstone set, the row that deletes the entity on the ref. Returns the
   * row as stored.
   */
  #writeRow(tx: Tx, kind: string, ref: Ref, row: string, tombstone: boolean, actor: string): Promise<string> {
    return tx.upsertRow(kind, { ref: ref.id, root: ref.root, row, tombstone, actor });
  }

  /**
   * Writes the row that deletes an entity on a ref: a copy of the entity's
   * effective row, so every required column holds, with its tombstone set.
   */
  async #deleteEntity(tx: Tx, ref: Ref, composed: Index, kind: string, key: string, actor: string): Promise<void> {
    const row = composed.get(kind)?.get(key);
    if (row === undefined) {
      throw new EntityNotFoundError(`entity not found on the ref: ${kind} ${key}`);
    }
    await this.#writeRow(tx, kind, ref, row, true, actor);
  }

  /**
   * Composes the ref, diffs it against its last commit (or its base) and
   * writes a commit with a patch per changed entity. Returns the new commit,
   * or rejects with NothingToCommitError. The caller moves the ref's head.
   */
  async #commit(tx: Tx, ref: Ref, options: CommitOptions, actor: string): Promise<Commit> {
    const { tree: composed, own } = await this.#compose(tx, ref);
    const validated = this.#run("validate", `{"descriptor":${this.#descriptor},"tree":${treeJson(composed)}}`);
    const findings = decodeFindings(validated.get("findings")!);
    if (findings.length > 0) {
      throw new InvalidTreeError(findings);
    }
    const parent = ref.head ?? ref.base;
    const previous = await this.#materializePins(tx, parent);
    const changes = this.#diff(previous.tree, composed);
    if (changes.length === 0) {
      throw new NothingToCommitError();
    }
    const ownByKey = this.#index(own);
    const previousByKey = this.#index(previous.tree);
    const patches: Patch[] = [];
    const nearest = new Map<string, Patch>();
    for (const change of changes) {
      const kind = this.#kind(change.kind);
      // An ADD or UPDATE pins the winning row. A DELETE pins the ref's
      // tombstone, or the entity's last committed row when a removed parent
      // took it with it.
      let pinned = change.row;
      if (change.operation === "DELETE") {
        pinned = previousByKey.get(change.kind)?.get(change.entityKey);
        const raw = ownByKey.get(change.kind)?.get(change.entityKey);
        if (raw !== undefined) {
          let tombstone = false;
          try {
            tombstone = this.#readRow(kind, raw).tombstone;
          } catch {
            // As the Go engine: a row it cannot read is not the tombstone.
          }
          if (tombstone) {
            pinned = raw;
          }
        }
      }
      if (pinned === undefined) {
        throw new Error(`engine: ${change.kind} row ${kind.key}: unexpected end of JSON input`);
      }
      const r = this.#readRow(kind, pinned);
      const patch: Patch = {
        commit: "",
        kind: change.kind,
        entityKey: change.entityKey,
        entityId: r.id,
        entityVersion: r.version,
        operation: change.operation,
      };
      patches.push(patch);
      nearest.set(entity(patch.kind, patch.entityKey), patch);
    }
    const contentHash = this.#contentHash(composed);
    let sequence: number | null = null;
    if (options.tag === true) {
      sequence = await tx.nextSequence(ref.root);
    }
    const written = await tx.insertCommit({
      root: ref.root,
      ref: ref.id,
      parent,
      message: options.message ?? "",
      schemaEpoch: this.#schemaEpoch,
      contentHash,
      sequence,
      actor,
    });
    await tx.insertPatches(written.id, patches);
    // A tagged commit is snapshotted, and so is one snapshotEvery commits
    // past the nearest snapshot on its chain: its parent's pins with its own
    // patches laid over them.
    if (options.tag === true || previous.distance + 1 >= this.#snapshotEvery) {
      applyPatches(previous.pins, nearest);
      if (previous.pins.size > 0) {
        await tx.insertSnapshot(written.id, pinEntries(previous.pins));
        written.snapshot = true;
      }
    }
    return written;
  }

  /**
   * Commits the ref and moves its head, sealing it when asked. Nothing to
   * commit is not an error when allowEmpty is set: the ref's version still
   * moves and the returned commit is null.
   */
  async #commitAndMove(
    tx: Tx,
    ref: Ref,
    options: CommitOptions,
    allowEmpty: boolean,
    seal: boolean,
    actor: string,
  ): Promise<CommitResult> {
    let written: Commit | null = null;
    try {
      written = await this.#commit(tx, ref, options, actor);
    } catch (err) {
      if (!(allowEmpty && err instanceof NothingToCommitError)) {
        throw err;
      }
    }
    const moved = await tx.updateRef({ id: ref.id, version: ref.version, head: written?.id ?? null, base: null, seal, actor });
    return { ref: moved, commit: written };
  }

  /**
   * Merges source's head into target against source's base. With no
   * conflicts it writes every entity the target does not already hold as
   * the merge left it: the merged row, or the row that deletes the entity.
   */
  async #merge(tx: Tx, source: Ref, target: Ref, resolutions: readonly Resolution[], actor: string): Promise<Conflict[]> {
    const base = await this.#materialize(tx, source.base);
    let theirs = base;
    if (source.head !== null) {
      theirs = await this.#materialize(tx, source.head);
    }
    const { tree: ours } = await this.#compose(tx, target);
    const result = this.#coreMergeResult(base, ours, theirs, resolutions);
    const conflicts = decodeConflicts(result.get("conflicts")!);
    if (conflicts.length > 0) {
      return conflicts;
    }
    const merged = decodeTree(result.get("merged")!);
    const mergedByKey = this.#index(merged);
    const oursByKey = this.#index(ours);
    for (const value of result.get("entities") as JsonValue[]) {
      const outcome = value as Map<string, JsonValue>;
      const kind = outcome.get("kind") as string;
      const entityKey = outcome.get("entityKey") as string;
      // A result equal to ours is ours' side, a delete on both sides
      // included, so a delete from another side deletes a live entity of
      // ours.
      if (outcome.get("side") === "ours") {
        continue;
      }
      if (outcome.get("deleted") === true) {
        await this.#deleteEntity(tx, target, oursByKey, kind, entityKey, actor);
        continue;
      }
      const row = mergedByKey.get(kind)?.get(entityKey);
      if (row === undefined) {
        throw new Error(`engine: the merge left no ${kind} row for ${entityKey}`);
      }
      await this.#writeRow(tx, kind, target, row, false, actor);
    }
    return [];
  }

  /** Runs the core's three-way merge of three trees, with the resolutions' entity keys in their canonical form. */
  #coreMergeResult(base: Tree, ours: Tree, theirs: Tree, resolutions: readonly Resolution[]): Map<string, JsonValue> {
    const encoded = resolutions.map((resolution) => {
      const members = [
        `"kind":${JSON.stringify(resolution.kind)}`,
        `"entityKey":${JSON.stringify(id("resolution entity key", resolution.entityKey))}`,
        `"path":${JSON.stringify(resolution.path)}`,
      ];
      if (resolution.take !== undefined && resolution.take !== ("" as Take)) {
        members.push(`"take":${JSON.stringify(resolution.take)}`);
      }
      if (resolution.value !== undefined && resolution.value !== "") {
        members.push(`"value":${stringifyJson(parseJson(resolution.value))}`);
      }
      return "{" + members.join(",") + "}";
    });
    let request = `{"descriptor":${this.#descriptor},"base":${treeJson(base)},"ours":${treeJson(ours)},"theirs":${treeJson(theirs)}`;
    if (encoded.length > 0) {
      request += `,"resolutions":[${encoded.join(",")}]`;
    }
    return this.#run("merge", request + "}");
  }

  /** coreMergeResult's merged tree, or its conflicts. */
  #coreMerge(
    base: Tree,
    ours: Tree,
    theirs: Tree,
    resolutions: readonly Resolution[],
  ): { merged: Tree; conflicts: Conflict[] } {
    const result = this.#coreMergeResult(base, ours, theirs, resolutions);
    const conflicts = decodeConflicts(result.get("conflicts")!);
    if (conflicts.length > 0) {
      return { merged: {}, conflicts };
    }
    return { merged: decodeTree(result.get("merged")!), conflicts: [] };
  }

  /**
   * Writes a ref's own rows so that, over the tree of its base (base), it
   * composes to want. current is what the ref composes to now and own its
   * rows. Each entity want holds differently from base gets the ref's row of
   * it (a tombstone where want lacks it) unless the ref's row already gives
   * it; every other row the ref holds is removed, so the entity reads
   * through the base.
   */
  async #overlay(tx: Tx, ref: Ref, base: Tree, want: Tree, current: Tree, own: Tree, actor: string): Promise<void> {
    const changes = this.#diff(base, want);
    const moved = this.#diff(current, want);
    const differs = new Set(moved.map((change) => entity(change.kind, change.entityKey)));
    const ownByKey = this.#index(own);
    const baseByKey = this.#index(base);
    const kept = new Set<string>();
    for (const change of changes) {
      const at = entity(change.kind, change.entityKey);
      kept.add(at);
      const kind = this.#kind(change.kind);
      const ownRow = ownByKey.get(change.kind)?.get(change.entityKey);
      const tombstone = ownRow !== undefined && this.#readRow(kind, ownRow).tombstone;
      if (change.operation === "DELETE") {
        if (ownRow !== undefined && tombstone) {
          continue;
        }
        await this.#deleteEntity(tx, ref, baseByKey, change.kind, change.entityKey, actor);
        continue;
      }
      if (ownRow !== undefined && !tombstone && !differs.has(at)) {
        continue;
      }
      await this.#writeRow(tx, change.kind, ref, change.row!, false, actor);
    }
    for (const k of this.#kinds) {
      for (const key of ownByKey.get(k.name)?.keys() ?? []) {
        if (kept.has(entity(k.name, key))) {
          continue;
        }
        await tx.removeRow(k.name, ref.id, key, actor);
      }
    }
  }

  /**
   * Writes the rows that make the ref compose to tree: each entity tree
   * holds that the ref composes differently is written from tree, and each
   * entity only the ref holds is deleted on it.
   */
  async #revert(tx: Tx, ref: Ref, tree: Tree, actor: string): Promise<void> {
    const { tree: current } = await this.#compose(tx, ref);
    const changes = this.#diff(current, tree);
    const currentByKey = this.#index(current);
    for (const change of changes) {
      if (change.operation === "DELETE") {
        await this.#deleteEntity(tx, ref, currentByKey, change.kind, change.entityKey, actor);
        continue;
      }
      await this.#writeRow(tx, change.kind, ref, change.row!, false, actor);
    }
  }

  /**
   * Writes every snapshot the graph's rules call for and it lacks: of a
   * tagged commit (release takes only tagged ones, so this covers a released
   * commit), and of a commit snapshotEvery commits past the nearest snapshot
   * on its chain. It visits each commit after its parent, so every walk it
   * takes stops at a snapshot at most snapshotEvery commits away. Returns
   * how many snapshots it wrote.
   */
  async #backfill(tx: Tx): Promise<number> {
    const nodes = await tx.commits();
    const byID = new Map(nodes.map((n) => [n.id, n]));
    // Each visited commit's distance from the nearest snapshot on its
    // chain, 0 for a snapshotted commit.
    const distance = new Map<string, number>();
    let written = 0;
    for (const start of nodes) {
      // Visit start's unvisited ancestors from the oldest down.
      const path: CommitNode[] = [];
      for (let at: CommitNode | undefined = start; at !== undefined; at = at.parent === null ? undefined : byID.get(at.parent)) {
        if (distance.has(at.id)) {
          break;
        }
        path.push(at);
        if (at.parent === null) {
          break;
        }
      }
      for (let i = path.length - 1; i >= 0; i--) {
        const n = path[i]!;
        if (n.snapshot) {
          distance.set(n.id, 0);
          continue;
        }
        let d = (n.parent !== null ? (distance.get(n.parent) ?? 0) : 0) + 1;
        if (n.tagged || d >= this.#snapshotEvery) {
          if (await this.#ensureSnapshot(tx, { id: n.id, snapshot: false })) {
            written++;
          }
          d = 0;
        }
        distance.set(n.id, d);
      }
    }
    return written;
  }
}

/** Lays patches over a pin set: a DELETE removes its entity, and any other patch pins its row version. */
function applyPatches(pins: PinSet, patches: Map<string, Patch>): void {
  for (const [at, p] of patches) {
    if (p.operation === "DELETE") {
      pins.delete(at);
      continue;
    }
    pins.set(at, { kind: p.kind, entityKey: p.entityKey, entityId: p.entityId, entityVersion: p.entityVersion });
  }
}

/** A pin set as a snapshot's entries, ordered by kind and entity key. */
function pinEntries(pins: PinSet): SnapshotEntry[] {
  return [...pins.values()].sort(
    (a, b) => compareCodePoints(a.kind, b.kind) || compareCodePoints(a.entityKey, b.entityKey),
  );
}

/** A tree as the core reads it: its kinds sorted, each a JSON array of its rows. */
function treeJson(tree: Tree): string {
  const kinds = Object.keys(tree)
    .filter((kind) => tree[kind]!.length > 0)
    .sort(compareCodePoints);
  return "{" + kinds.map((kind) => `${JSON.stringify(kind)}:[${tree[kind]!.join(",")}]`).join(",") + "}";
}

/** A tree the core returned, without its empty kinds. */
function decodeTree(value: JsonValue): Tree {
  const tree: Tree = {};
  if (!isJsonObject(value)) {
    throw new Error("engine: decode tree: not an object");
  }
  for (const [kind, rows] of value) {
    if (!Array.isArray(rows)) {
      throw new Error(`engine: decode tree: ${kind} is not a list`);
    }
    if (rows.length > 0) {
      tree[kind] = rows.map(stringifyJson);
    }
  }
  return tree;
}

function decodeFindings(value: JsonValue): Finding[] {
  return (value as JsonValue[]).map((item) => {
    const finding = item as Map<string, JsonValue>;
    const out: Finding = {
      code: finding.get("code") as Finding["code"],
      kind: finding.get("kind") as string,
      message: finding.get("message") as string,
    };
    const entityKey = finding.get("entityKey");
    if (typeof entityKey === "string" && entityKey !== "") {
      out.entityKey = entityKey;
    }
    return out;
  });
}

function decodeConflicts(value: JsonValue): Conflict[] {
  return (value as JsonValue[]).map((item) => {
    const conflict = item as Map<string, JsonValue>;
    const out: Conflict = {
      kind: conflict.get("kind") as string,
      entityKey: conflict.get("entityKey") as string,
      path: conflict.get("path") as string,
    };
    for (const member of ["base", "ours", "theirs", "oursAuthor", "theirsAuthor"] as const) {
      const unit = conflict.get(member);
      if (unit !== undefined) {
        out[member] = stringifyJson(unit);
      }
    }
    return out;
  });
}

function sleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(signal.reason);
      return;
    }
    const timer = setTimeout(() => {
      signal?.removeEventListener("abort", onAbort);
      resolve();
    }, ms);
    const onAbort = () => {
      clearTimeout(timer);
      reject(signal!.reason);
    };
    signal?.addEventListener("abort", onAbort, { once: true });
  });
}

export * from "./canonical.js";
export * from "./errors.js";
export * from "./json.js";
export * from "./storage.js";
export type { Finding, FindingCode, Take } from "./contract.js";
export { VersionGraphError } from "./index.js";
