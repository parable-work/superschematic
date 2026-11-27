// Revisions's class (D103): the build derives revisions.behavior.json from it
// (make behaviors), which the behaviors' core and the engine read.
import { Generic, Temporal } from "superscalar";
import { computed, declaresBehavior, subTable, type Integer, type JsonSchema } from "@superschematic/db";

/** A string that is not empty. */
type Named = JsonSchema<string, { minLength: 1 }>;

interface RevisionsConfig {
  /** Turns on the review step: propose, approve, reject and listProposals. */
  review?: {
    /** The permission a caller needs to approve or reject a proposal. */
    permission: Named;
  };
}

interface ListRevisionsParams {
  /** At most this many revisions; 50 when absent. */
  limit?: JsonSchema<Integer, { minimum: 1; maximum: 500 }>;
  /** The next of the previous page. */
  cursor?: string;
}

interface ListRevisionsResult {
  items: {
    /** Its number: 1, 2, 3, ... */
    revision: Integer;
    /** The instance's own fields as the change left them. */
    data: object;
    /** The subject of the caller whose change it records. */
    createdBy: string;
    /** When, in epoch milliseconds. */
    createdAt: Integer;
    /** The proposal whose approval made it. */
    proposal?: Integer;
  }[];
  /** The cursor of the next page; null after the last. */
  next: string | null;
}

interface GetRevisionParams {
  /** The revision's number. */
  revision: JsonSchema<Integer, { minimum: 1 }>;
}

interface GetRevisionResult {
  /** Its number: 1, 2, 3, ... */
  revision: Integer;
  /** The instance's own fields as the change left them. */
  data: object;
  /** The subject of the caller whose change it records. */
  createdBy: string;
  /** When, in epoch milliseconds. */
  createdAt: Integer;
  /** The proposal whose approval made it. */
  proposal?: Integer;
}

interface ProposeParams {
  /** A JSON merge patch (RFC 7386) of the instance's own fields. */
  patch: JsonSchema<object, { minProperties: 1 }>;
  /** Why, for the reviewer. */
  note?: JsonSchema<string, { maxLength: 10000 }>;
  /**
   * Instances the proposal cites, for the reviewer: each one must exist and
   * the proposer must be able to read its schema, and a revision must be one
   * it has had, of a schema that composes Revisions. They are kept as given;
   * a later change of an instance cited leaves them as they are.
   */
  evidence?: JsonSchema<{
    /** The schema of the instance cited. */
    schema: JsonSchema<string, { pattern: "^[A-Za-z][A-Za-z0-9_-]{0,127}$" }>;
    /** The instance's id. */
    id: JsonSchema<string, { pattern: "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$" }>;
    /** The revision of it the proposal rests on. */
    revision?: JsonSchema<Integer, { minimum: 1 }>;
  }[], { minItems: 1; maxItems: 64; uniqueItems: true }>;
}

interface ProposeResult {
  /** The proposal's number on the instance: 1, 2, 3, ... */
  id: Integer;
  patch: object;
  note?: string;
  /** The instances the proposal cites. */
  evidence?: {
    schema: string;
    id: string;
    revision?: Integer;
  }[];
  /** The instance's revision when it was proposed. */
  base?: Integer;
  state: JsonSchema<"pending" | "approved" | "rejected", { type: "string" }>;
  createdBy: string;
  createdAt: Integer;
  reviewedBy?: string;
  reviewedAt?: Integer;
  reason?: string;
  revision?: Integer;
}

interface ApproveParams {
  /** The id of the pending proposal. */
  proposal: JsonSchema<Integer, { minimum: 1 }>;
}

interface ApproveResult {
  id: Integer;
  patch: object;
  note?: string;
  /** The instances the proposal cites. */
  evidence?: {
    schema: string;
    id: string;
    revision?: Integer;
  }[];
  base?: Integer;
  state: JsonSchema<"pending" | "approved" | "rejected", { type: "string" }>;
  createdBy: string;
  createdAt: Integer;
  reviewedBy?: string;
  reviewedAt?: Integer;
  reason?: string;
  /** The instance's revision after the approval. */
  revision?: Integer;
}

interface RejectParams {
  /** The id of the pending proposal. */
  proposal: JsonSchema<Integer, { minimum: 1 }>;
  /** Why, for the proposer. */
  reason?: JsonSchema<string, { maxLength: 10000 }>;
}

interface RejectResult {
  id: Integer;
  patch: object;
  note?: string;
  /** The instances the proposal cites. */
  evidence?: {
    schema: string;
    id: string;
    revision?: Integer;
  }[];
  base?: Integer;
  state: JsonSchema<"pending" | "approved" | "rejected", { type: "string" }>;
  createdBy: string;
  createdAt: Integer;
  reviewedBy?: string;
  reviewedAt?: Integer;
  reason?: string;
  revision?: Integer;
}

interface ListProposalsParams {
  /** Only proposals in this state. */
  state?: JsonSchema<"pending" | "approved" | "rejected", { type: "string" }>;
  /** At most this many proposals; 50 when absent. */
  limit?: JsonSchema<Integer, { minimum: 1; maximum: 500 }>;
  /** The next of the previous page. */
  cursor?: string;
}

interface ListProposalsResult {
  items: {
    id: Integer;
    patch: object;
    note?: string;
    evidence?: {
      schema: string;
      id: string;
      revision?: Integer;
    }[];
    base?: Integer;
    state: JsonSchema<"pending" | "approved" | "rejected", { type: "string" }>;
    createdBy: string;
    createdAt: Integer;
    reviewedBy?: string;
    reviewedAt?: Integer;
    reason?: string;
    revision?: Integer;
  }[];
  /** The cursor of the next page; null after the last. */
  next: string | null;
}

/** The codes Revisions's refusals carry. */
enum RevisionsVeto {
  /**
   * propose, approve, reject or listProposals on a type whose Revisions
   * config sets no review.
   */
  no_review,
  /** approve or reject of a proposal that is no longer pending. */
  not_pending,
}

/**
 * Immutable revisions of the instance's own fields: one when it is created
 * and one each time they change, numbered 1, 2, 3, ..., read a page at a time
 * or one by its number. With review in its config, a caller can propose a
 * change as a merge patch, citing instances as evidence, and a caller who
 * holds the review permission approves it, which applies the patch as an
 * update, or rejects it; pendingProposals counts the proposals waiting.
 * Deleting the instance deletes its revisions and proposals.
 */
@declaresBehavior({ vetoes: RevisionsVeto })
export abstract class Revisions<C extends RevisionsConfig> {
  /** The number of the instance's latest revision. */
  revision: Generic.Int64;
  /**
   * With review in the config, how many of the instance's proposals are
   * pending; absent without review.
   */
  @computed pendingProposals: unknown;

  /**
   * Lists the instance's revisions, oldest first, one page at a time.
   */
  abstract listRevisions(params: ListRevisionsParams): ListRevisionsResult;

  /**
   * Reads one of the instance's revisions by its number: the own fields as
   * that change left them. A number the instance has not reached is
   * not_found.
   * @returns The revision.
   */
  abstract getRevision(params: GetRevisionParams): GetRevisionResult;

  /**
   * Proposes a change to the instance's own fields as a JSON merge patch, for
   * a reviewer to approve or reject. The patch must change the instance and
   * leave it valid; nothing changes until a reviewer approves it.
   * @writes
   * @returns The proposal.
   */
  abstract propose(params: ProposeParams): ProposeResult;

  /**
   * Approves a pending proposal: applies its patch to the instance as an
   * update, whose checks run, and records the revision it makes. Needs the
   * review permission.
   * @writes
   * @returns The proposal, approved.
   */
  abstract approve(params: ApproveParams): ApproveResult;

  /**
   * Rejects a pending proposal; the instance does not change. Needs the
   * review permission.
   * @writes
   * @returns The proposal, rejected.
   */
  abstract reject(params: RejectParams): RejectResult;

  /**
   * Lists the instance's proposals, oldest first, one page at a time; with
   * state, only those in it.
   */
  abstract listProposals(params: ListProposalsParams): ListProposalsResult;

  /**
   * On a table, one row per revision of the row, keyed by its number: the own
   * fields as the change left them, who made it and when, and the proposal
   * whose approval made it. A revision is never changed or removed while the
   * row exists; a delete of the row deletes them.
   */
  @subTable({ key: "revision" })
  static revisions = class {
    /**
     * The revision's number on the row, from 1, in ten digits zero-padded so
     * the key's order is theirs.
     */
    revision: string;
    /** The row's own fields as the change left them. */
    data: Generic.JSON;
    /** The subject of the caller whose change it records. */
    createdBy: string;
    /** When, in epoch milliseconds. */
    createdAt: Temporal.DateTime;
    /** The proposal whose approval made it; absent for any other change. */
    proposal: Generic.Int64;
  };

  /**
   * On a table, one row per proposal on the row, keyed by its number: the
   * merge patch, the note and the evidence it was proposed with, the revision
   * it was made against, its state, and who settled it, when, why and the
   * revision its approval made. A delete of the row deletes them.
   */
  @subTable({ key: "proposal" })
  static proposals = class {
    /**
     * The proposal's number on the row, from 1, in ten digits zero-padded so
     * the key's order is theirs.
     */
    proposal: string;
    /** The JSON merge patch of the row's own fields it proposes. */
    patch: Generic.JSON;
    /** Why, for the reviewer. */
    note: string;
    /** The rows it cites, as given. */
    evidence: Generic.JSON;
    /** The row's revision when it was proposed; absent before its first. */
    base: Generic.Int64;
    /** pending, approved or rejected. */
    state: string;
    /** The subject of the caller who proposed it. */
    createdBy: string;
    /** When it was proposed, in epoch milliseconds. */
    createdAt: Temporal.DateTime;
    /** The subject of the caller who settled it. */
    reviewedBy: string;
    /** When it was settled, in epoch milliseconds. */
    reviewedAt: Temporal.DateTime;
    /** Why it was rejected, for the proposer. */
    reason: string;
    /**
     * The revision its approval made, or the latest one when the row already
     * had its patch.
     */
    revision: Generic.Int64;
  };
}
