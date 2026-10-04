// The seam between the version-graph engine and the database that holds a
// graph (D19): the operations a storage adapter implements, and the values
// they take and return. The TypeScript counterpart of the Go module's
// package storage.
//
// The engine reaches storage only through this interface, and asks for one
// transaction per operation. Engine runs over Storage and Tx, whose methods
// return promises; SyncEngine runs the same operations over SyncStorage and
// SyncTx, whose methods return their values, for a database whose driver
// blocks, as SQLite's does in bun and Node (D32). Every row an adapter
// returns, live or from history, is a canonical row
// (runtime/versiongraph/README.md, "Canonical rows") as JSON text, so its
// numbers keep their digits. Every row the engine hands an adapter is
// canonical too, and every id, of a ref, a commit, a root, a row or an
// actor, is a UUID in its canonical form (base62).
//
// The module has no dependencies, so an adapter can implement it without
// loading the core.

import { EngineError } from "./errors.js";

/** A ref or a commit that does not exist, or a discarded ref. Code `not_found`. */
export class NotFoundError extends EngineError {
  constructor(message = "not found", code: "not_found" | "version_conflict" = "not_found") {
    super(code, message);
    this.name = "NotFoundError";
  }
}

/**
 * A fenced write when the row is at another version. It is a NotFoundError
 * too, as the Go engine's error of that name wraps ErrNotFound. Code
 * `version_conflict`.
 */
export class VersionConflictError extends NotFoundError {
  constructor(message = "not found at the expected version") {
    super(message, "version_conflict");
    this.name = "VersionConflictError";
  }
}

/** A root already has a live ref of that name. Code `name_taken`. */
export class NameTakenError extends EngineError {
  constructor(message = "the root already has a live ref of that name") {
    super("name_taken", message);
    this.name = "NameTakenError";
  }
}

/** Holds one version graph. An adapter builds it from the graph's descriptor. */
export interface Storage {
  /**
   * Runs fn in one transaction. It commits when fn resolves and rolls back
   * when it rejects, rejecting with fn's error.
   */
  transact<T>(fn: (tx: Tx) => Promise<T>): Promise<T>;
}

/** One transaction's view of a graph: the operations the engine builds every graph operation from. */
export interface Tx {
  /** Writes a ref and returns it. */
  createRef(ref: NewRef): Promise<Ref>;
  /** Reads a ref, a discarded one included. A ref that does not exist is NotFoundError. */
  readRef(id: string): Promise<Ref>;
  /** Reads a ref as readRef does and locks it until the transaction ends. */
  lockRef(id: string): Promise<Ref>;
  /**
   * Moves a ref's head or base, seals it, or only bumps its version, fenced
   * by the version it expects. Returns the ref as written, or rejects with
   * VersionConflictError.
   */
  updateRef(update: RefUpdate): Promise<Ref>;
  /**
   * Soft-deletes a live ref at the version it expects, or rejects with
   * VersionConflictError and leaves the transaction usable.
   */
  discardRef(id: string, version: number, actor: string): Promise<void>;

  /** Reads every row a ref holds of one kind, tombstones included, in no particular order. */
  rows(kind: string, ref: string): Promise<string[]>;
  /**
   * Writes a row as the ref's row of its entity: an update of the ref's row
   * of the entity when it has one, else a new row. Returns the row as
   * stored.
   */
  upsertRow(kind: string, write: RowWrite): Promise<string>;
  /**
   * Hard-deletes the ref's row of an entity, recording actor as the
   * delete's actor in history. Reports whether there was one.
   */
  removeRow(kind: string, ref: string, entityKey: string, actor: string): Promise<boolean>;
  /** Reads the history images of row versions. A pin whose image history no longer holds is left out. */
  images(kind: string, pins: readonly Pin[]): Promise<string[]>;

  /** Reads a commit, or rejects with NotFoundError. */
  readCommit(id: string): Promise<Commit>;
  /** Writes a commit and returns it. */
  insertCommit(commit: NewCommit): Promise<Commit>;
  /** Writes a commit's patches. */
  insertPatches(commit: string, patches: readonly Patch[]): Promise<void>;
  /**
   * Reads a commit and its parents, nearest first, at most limit of them,
   * and stops after the first that has a snapshot. A commit that does not
   * exist reads as no commits.
   */
  walk(commit: string, limit: number): Promise<Commit[]>;
  /** Reads head and the parents of it that ref wrote, nearest first, at most limit of them. */
  refCommits(ref: string, head: string, limit: number): Promise<Commit[]>;
  /** Reads every patch of the commits. */
  patches(commits: readonly string[]): Promise<Patch[]>;
  /**
   * Locks the root against other taggers until the transaction ends, and
   * returns the root's next published sequence.
   */
  nextSequence(root: string): Promise<number>;

  /** Reads a commit's snapshot: its full pin set, in no particular order. A commit without one reads as no entries. */
  snapshot(commit: string): Promise<SnapshotEntry[]>;
  /** Writes a commit's snapshot. */
  insertSnapshot(commit: string, entries: readonly SnapshotEntry[]): Promise<void>;
  /** Reads every commit of the graph, in no particular order, with whether each is tagged and snapshotted. */
  commits(): Promise<CommitNode[]>;

  /** Reads a root's release pointer, or rejects with NotFoundError when the root has none. */
  readRelease(root: string): Promise<Release>;
  /**
   * Points a root's release at a commit, fenced by the pointer's version:
   * version 0 writes the root's first pointer, and any other moves the
   * pointer at that version. A pointer at another version, or one that
   * already exists when version is 0, is VersionConflictError. Returns the
   * pointer as written.
   */
  writeRelease(write: ReleaseWrite): Promise<Release>;

  /**
   * Deletes the history images of one kind older than retentionDays (0 for
   * the kind's declared retention), keeping every image a patch or a
   * snapshot pins, at most batchSize of them (0 for no limit). Returns how
   * many it deleted.
   */
  prune(kind: string, retentionDays: number, batchSize: number): Promise<number>;
  /** Reads the refs discarded longer ago than graceMs milliseconds. */
  discardedRefs(graceMs: number): Promise<Ref[]>;
  /** Reads the live change sets whose last write is older than idleMs milliseconds. */
  idleDrafts(idleMs: number): Promise<Ref[]>;
  /**
   * Hard-deletes every row a ref holds of one kind, recording actor as each
   * delete's actor in history. Returns how many it deleted.
   */
  removeRefRows(kind: string, ref: string, actor: string): Promise<number>;
  /**
   * Takes the graph's sweep lock until the transaction ends. Reports false,
   * without waiting, when another transaction holds it.
   */
  sweepLock(): Promise<boolean>;
}

/**
 * Holds one version graph for a SyncEngine: Storage whose transaction runs
 * synchronously. An adapter builds it from the graph's descriptor.
 */
export interface SyncStorage {
  /**
   * Runs fn in one transaction. It commits when fn returns and rolls back
   * when it throws, throwing fn's error.
   */
  transact<T>(fn: (tx: SyncTx) => T): T;
}

/**
 * One synchronous transaction's view of a graph: every method of Tx, each
 * returning its value where Tx's resolves and throwing where Tx's rejects.
 */
export interface SyncTx {
  /** Writes a ref and returns it. */
  createRef(ref: NewRef): Ref;
  /** Reads a ref, a discarded one included. A ref that does not exist is NotFoundError. */
  readRef(id: string): Ref;
  /** Reads a ref as readRef does and locks it until the transaction ends. */
  lockRef(id: string): Ref;
  /**
   * Moves a ref's head or base, seals it, or only bumps its version, fenced
   * by the version it expects. Returns the ref as written, or throws
   * VersionConflictError.
   */
  updateRef(update: RefUpdate): Ref;
  /**
   * Soft-deletes a live ref at the version it expects, or throws
   * VersionConflictError and leaves the transaction usable.
   */
  discardRef(id: string, version: number, actor: string): void;

  /** Reads every row a ref holds of one kind, tombstones included, in no particular order. */
  rows(kind: string, ref: string): string[];
  /**
   * Writes a row as the ref's row of its entity: an update of the ref's row
   * of the entity when it has one, else a new row. Returns the row as
   * stored.
   */
  upsertRow(kind: string, write: RowWrite): string;
  /**
   * Hard-deletes the ref's row of an entity, recording actor as the
   * delete's actor in history. Reports whether there was one.
   */
  removeRow(kind: string, ref: string, entityKey: string, actor: string): boolean;
  /** Reads the history images of row versions. A pin whose image history no longer holds is left out. */
  images(kind: string, pins: readonly Pin[]): string[];

  /** Reads a commit, or throws NotFoundError. */
  readCommit(id: string): Commit;
  /** Writes a commit and returns it. */
  insertCommit(commit: NewCommit): Commit;
  /** Writes a commit's patches. */
  insertPatches(commit: string, patches: readonly Patch[]): void;
  /**
   * Reads a commit and its parents, nearest first, at most limit of them,
   * and stops after the first that has a snapshot. A commit that does not
   * exist reads as no commits.
   */
  walk(commit: string, limit: number): Commit[];
  /** Reads head and the parents of it that ref wrote, nearest first, at most limit of them. */
  refCommits(ref: string, head: string, limit: number): Commit[];
  /** Reads every patch of the commits. */
  patches(commits: readonly string[]): Patch[];
  /**
   * Locks the root against other taggers until the transaction ends, and
   * returns the root's next published sequence.
   */
  nextSequence(root: string): number;

  /** Reads a commit's snapshot: its full pin set, in no particular order. A commit without one reads as no entries. */
  snapshot(commit: string): SnapshotEntry[];
  /** Writes a commit's snapshot. */
  insertSnapshot(commit: string, entries: readonly SnapshotEntry[]): void;
  /** Reads every commit of the graph, in no particular order, with whether each is tagged and snapshotted. */
  commits(): CommitNode[];

  /** Reads a root's release pointer, or throws NotFoundError when the root has none. */
  readRelease(root: string): Release;
  /**
   * Points a root's release at a commit, fenced by the pointer's version:
   * version 0 writes the root's first pointer, and any other moves the
   * pointer at that version. A pointer at another version, or one that
   * already exists when version is 0, is VersionConflictError. Returns the
   * pointer as written.
   */
  writeRelease(write: ReleaseWrite): Release;

  /**
   * Deletes the history images of one kind older than retentionDays (0 for
   * the kind's declared retention), keeping every image a patch or a
   * snapshot pins, at most batchSize of them (0 for no limit). Returns how
   * many it deleted.
   */
  prune(kind: string, retentionDays: number, batchSize: number): number;
  /** Reads the refs discarded longer ago than graceMs milliseconds. */
  discardedRefs(graceMs: number): Ref[];
  /** Reads the live change sets whose last write is older than idleMs milliseconds. */
  idleDrafts(idleMs: number): Ref[];
  /**
   * Hard-deletes every row a ref holds of one kind, recording actor as each
   * delete's actor in history. Returns how many it deleted.
   */
  removeRefRows(kind: string, ref: string, actor: string): number;
  /**
   * Takes the graph's sweep lock until the transaction ends. Reports false,
   * without waiting, when another transaction holds it.
   */
  sweepLock(): boolean;
}

/** A ref's graph columns. An absent reference is null. */
export interface Ref {
  id: string;
  root: string;
  parent: string | null;
  base: string | null;
  head: string | null;
  name: string;
  sealed: boolean;
  /** True once the ref is soft-deleted. */
  discarded: boolean;
  version: number;
}

/**
 * A ref to write: a primary line when parent is null, else a change set of
 * parent whose base is base (null for none).
 */
export interface NewRef {
  root: string;
  parent: string | null;
  base: string | null;
  name: string;
  actor: string;
}

/**
 * Changes a ref at version: moves the head to head and the base to base
 * (each unless it is null), seals the ref when seal is set, and bumps its
 * version in any case.
 */
export interface RefUpdate {
  id: string;
  version: number;
  head: string | null;
  base: string | null;
  seal: boolean;
  actor: string;
}

/**
 * A row to write onto a ref. The adapter writes ref, root and tombstone into
 * the row's ref, root and tombstone columns, and actor and the time into
 * its audit columns, whatever the row says; it never writes the row's id or
 * version columns. A column the row lacks keeps its stored value on an
 * update and its default on an insert; a row without an entity key is a new
 * entity, whose key the database generates.
 */
export interface RowWrite {
  ref: string;
  root: string;
  /** The canonical row, as JSON text. */
  row: string;
  tombstone: boolean;
  actor: string;
}

/** One row version: the row's id and its version. */
export interface Pin {
  id: string;
  version: number;
}

/** A commit. */
export interface Commit {
  id: string;
  root: string;
  ref: string;
  /** Null for a commit with no parent. */
  parent: string | null;
  /** "" for none. */
  message: string;
  schemaEpoch: number;
  contentHash: string;
  /** Null for an untagged commit. */
  sequence: number | null;
  /** The commit's time as a canonical dateTime. */
  createdAt: string;
  createdBy: string;
  /** True when the commit has a snapshot. */
  snapshot: boolean;
}

/** A commit to write. */
export interface NewCommit {
  root: string;
  ref: string;
  parent: string | null;
  message: string;
  schemaEpoch: number;
  contentHash: string;
  sequence: number | null;
  actor: string;
}

/** One entity a commit changed, pinned to the row version it sealed. */
export interface Patch {
  /** Set on a patch read back; "" on one to write. */
  commit: string;
  kind: string;
  entityKey: string;
  entityId: string;
  entityVersion: number;
  operation: "ADD" | "UPDATE" | "DELETE";
}

/** One entity of a snapshotted commit's tree, pinned to the row version the tree holds. */
export interface SnapshotEntry {
  kind: string;
  entityKey: string;
  entityId: string;
  entityVersion: number;
}

/** One commit of the graph as a sweep reads it. */
export interface CommitNode {
  id: string;
  /** Null for none. */
  parent: string | null;
  tagged: boolean;
  snapshot: boolean;
}

/** A root's release pointer: the commit it names, fenced by its version. */
export interface Release {
  id: string;
  root: string;
  commit: string;
  version: number;
}

/** Points a root's release at commit, fenced by version (0 for the root's first pointer). */
export interface ReleaseWrite {
  root: string;
  commit: string;
  version: number;
  actor: string;
}
