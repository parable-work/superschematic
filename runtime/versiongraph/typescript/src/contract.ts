// The JSON contract of the version-graph core (runtime/versiongraph/README.md)
// as TypeScript types. Every member is named exactly as the contract names it.
//
// Nothing checks these types at run time. The vector suite
// (test/vectors.test.ts) keeps them honest: it decodes every vector's input
// and output through them, with one decoder field per member that the
// compiler requires to cover the type exactly, and compares the bytes with
// the vector. A member missing, extra or misnamed here fails it.

/** How a content column merges. */
export type Unit = "atomic" | "keyed" | "jsonSchema";

/** A kind's containment edge. */
export interface ParentEdge {
  /** The column holding the parent row's entity key (a string, or null for none). */
  key: string;
  /** The parent's kind, which may be the kind itself. */
  kind: string;
}

/** Which column of a kind's rows plays which role, and how content merges. */
export interface KindDescriptor {
  /** The kind's name: the tree member that holds its rows. Unique. */
  kind: string;
  /** The entity key column: the logical identity rows are matched on. */
  key: string;
  /** The row id column. */
  id: string;
  /** The column naming the ref the row was written on. */
  ref: string;
  /** A boolean column; true marks the row as the entity's delete. */
  tombstone: string;
  /** The row version column. */
  version: string;
  /** The column naming who wrote the row; conflicts report it. */
  author?: string;
  parent?: ParentEdge;
  /** An integer column that orders siblings. */
  order?: string;
  /** At most one live row. Default false. */
  singleton?: boolean;
  /** A conflict unit per content column; atomic when absent. */
  units?: Record<string, Unit>;
  /** Columns that are not content. */
  excluded?: string[];
}

/** The graph descriptor the ORM generator writes as versiongraph/<name>.json. */
export interface Descriptor {
  /** The graph's name; the core does not read it. */
  graph?: string;
  kinds: KindDescriptor[];
}

/**
 * A row as Postgres `to_jsonb(row)` renders it, keyed by column name. Values
 * are whatever the configured JSON parser gives (see `InitOptions.parse`).
 */
export type Row = Record<string, unknown>;

/** `{"<kind>": [row, ...]}`. A missing kind has no rows. */
export type Tree = Record<string, Row[]>;

export type FindingCode =
  | "absent_parent"
  | "duplicate_entity_key"
  | "singleton"
  | "parent_cycle"
  | "order_out_of_range";

/** A problem in a tree that compose reports and validate lists. */
export interface Finding {
  code: FindingCode;
  kind: string;
  /** Absent for a finding about a whole kind (`singleton`). */
  entityKey?: string;
  message: string;
}

export interface ComposeInput {
  descriptor: Descriptor;
  base: Tree;
  overlay: Tree;
}

export interface ComposeOutput {
  /** Every kind of the descriptor, with no tombstones. */
  tree: Tree;
  findings: Finding[];
}

/** The input a resolution takes a unit's value from. */
export type Take = "base" | "ours" | "theirs";

/** Settles one conflict by taking the unit from one input. */
export interface TakeResolution {
  kind: string;
  entityKey: string;
  path: string;
  take: Take;
}

/** Settles one conflict by giving the unit's value (JSON null is a value). */
export interface ValueResolution {
  kind: string;
  entityKey: string;
  path: string;
  value: unknown;
}

export type Resolution = TakeResolution | ValueResolution;

export interface MergeInput {
  descriptor: Descriptor;
  base: Tree;
  ours: Tree;
  theirs: Tree;
  resolutions?: Resolution[];
}

/**
 * A unit both sides changed differently, or an edit against a delete (path
 * ""). `base`, `ours` and `theirs` are the unit's values, absent where the
 * unit is absent; for path "" they are whole rows, absent on a deleted side.
 */
export interface Conflict {
  kind: string;
  entityKey: string;
  /** A JSON Pointer into the row. */
  path: string;
  base?: unknown;
  ours?: unknown;
  theirs?: unknown;
  oursAuthor?: unknown;
  theirsAuthor?: unknown;
}

export type Side = "ours" | "theirs" | "merged" | "conflict";

/** Where an entity's merged result came from. */
export interface EntityOutcome {
  kind: string;
  entityKey: string;
  side: Side;
  /** True when the result is a delete. */
  deleted?: boolean;
}

export interface MergeOutput {
  /** Every settled entity's result; apply it only when `conflicts` is empty. */
  merged: Tree;
  conflicts: Conflict[];
  entities: EntityOutcome[];
}

export interface DiffInput {
  descriptor: Descriptor;
  from: Tree;
  to: Tree;
}

export type ChangeOperation = "ADD" | "UPDATE" | "DELETE";

export interface Change {
  kind: string;
  entityKey: string;
  operation: ChangeOperation;
  /** `to`'s row: its tombstone for a DELETE, when `to` has one. */
  row?: Row;
}

export interface DiffOutput {
  changes: Change[];
}

/** The input of content_hash and validate. */
export interface TreeInput {
  descriptor: Descriptor;
  tree: Tree;
}

export interface ContentHashOutput {
  /** SHA-256, as lowercase hex, of the tree's canonical content. */
  contentHash: string;
}

export interface ValidateOutput {
  /** Empty for a valid tree. */
  findings: Finding[];
}

/** The core's operations, as the vectors and the C ABI (`vg_<op>`) name them. */
export type OperationName = "compose" | "merge" | "diff" | "content_hash" | "validate";

export type ErrorCode =
  | "invalid_json"
  | "invalid_request"
  | "invalid_descriptor"
  | "unknown_kind"
  | "invalid_row"
  | "duplicate_entity_key"
  | "order_out_of_range"
  | "invalid_resolution"
  | "unmatched_resolution"
  | "internal";

/** What the core returns for a refused input. */
export interface ErrorDocument {
  error: {
    code: ErrorCode;
    message: string;
  };
}
