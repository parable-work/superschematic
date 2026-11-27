// Dependencies's class (D103): the build derives dependencies.behavior.json from it
// (make behaviors), which the behaviors' core and the engine read.
import { Temporal } from "superscalar";
import { declaresBehavior, subTable, volatile, type Integer, type JsonSchema } from "@superschematic/db";

/** A string that is not empty. */
type Named = JsonSchema<string, { minLength: 1 }>;

interface DependenciesConfig {
  /**
   * The schemas whose instances may block this type's, each composing
   * Workflow; the type's own schema when absent.
   */
  schemas?: JsonSchema<JsonSchema<string, { pattern: "^[A-Za-z][A-Za-z0-9_-]{0,127}$" }>[], { minItems: 1; uniqueItems: true }>;
  /**
   * The states of the type's Workflow that a transition into waits for every
   * blocker to finish; every terminal state when absent.
   */
  gatedStates?: JsonSchema<Named[], { minItems: 1; uniqueItems: true }>;
  /**
   * The outcomes of a blocker's terminal state that finish it; success when
   * absent. A blocker in a terminal state whose outcome the list lacks blocks
   * until it is removed.
   */
  satisfiedBy?: JsonSchema<("success" | "failure" | "neutral")[], { minItems: 1; uniqueItems: true }>;
}

interface AddBlockerParams {
  /** The blocker's schema; the instance's own when absent. */
  schema?: JsonSchema<string, { pattern: "^[A-Za-z][A-Za-z0-9_-]{0,127}$" }>;
  /** The blocker's id. */
  id: JsonSchema<string, { pattern: "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$" }>;
}

interface AddBlockerResult {
  schema: string;
  id: string;
  /** The blocker's Workflow status. */
  status?: string;
  /**
   * Whether it is not finished, so it blocks: its status is not a terminal
   * state of its schema's Workflow whose outcome the config accepts.
   */
  open: boolean;
}

interface RemoveBlockerParams {
  /** The blocker's schema; the instance's own when absent. */
  schema?: JsonSchema<string, { pattern: "^[A-Za-z][A-Za-z0-9_-]{0,127}$" }>;
  /** The blocker's id. */
  id: JsonSchema<string, { pattern: "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$" }>;
}

interface RemoveBlockerResult {
  schema: string;
  id: string;
}

interface ListBlockersParams {
  /** At most this many blockers; 50 when absent. */
  limit?: JsonSchema<Integer, { minimum: 1; maximum: 500 }>;
  /** The next of the previous page. */
  cursor?: string;
}

interface ListBlockersResult {
  items: {
    schema: string;
    id: string;
    /** The blocker's Workflow status. */
    status?: string;
    /**
     * Whether it is not finished, so it blocks: its status is not a terminal
     * state of its schema's Workflow whose outcome the config accepts.
     */
    open: boolean;
  }[];
  /** The cursor of the next page; null after the last. */
  next: string | null;
}

interface ListDependentsParams {
  /**
   * At most this many dependents; 50 when absent. A page holds fewer when the
   * caller may not read some of their schemas.
   */
  limit?: JsonSchema<Integer, { minimum: 1; maximum: 500 }>;
  /** The next of the previous page. */
  cursor?: string;
}

interface ListDependentsResult {
  items: {
    schema: string;
    id: string;
  }[];
  /** The cursor of the next page; null after the last. */
  next: string | null;
}

interface DependenciesCreateParams {
  /**
   * The instance's blockers from its create, each held to addBlocker's rules.
   */
  blockers?: JsonSchema<{
    /** The blocker's schema; the instance's own when absent. */
    schema?: JsonSchema<string, { pattern: "^[A-Za-z][A-Za-z0-9_-]{0,127}$" }>;
    /** The blocker's id. */
    id: JsonSchema<string, { pattern: "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$" }>;
  }[], { maxItems: 500 }>;
}

/** The codes Dependencies's refusals carry. */
enum DependenciesVeto {
  /**
   * A Workflow transition into a gated state while a blocker is open; details
   * lists the open blockers.
   */
  blocked,
  /**
   * addBlocker, or a create's blockers, naming an instance that already
   * blocks this one.
   */
  already_blocking,
  /**
   * addBlocker, or a create's blockers, giving an edge that would close a
   * cycle.
   */
  cycle,
  /**
   * addBlocker, or a create's blockers, giving an open blocker while the
   * instance is in a gated state no transition leaves.
   */
  gated,
}

/**
 * Blockers between instances. A blocker is finished once its status is a
 * terminal state of its own schema's Workflow whose outcome the config
 * accepts, a success by default. An instance is blocked while a blocker is
 * not finished, and a transition of its own Workflow into a gated state waits
 * until none blocks it. A blocker is an instance of the type's own schema, or
 * of a schema the config lists, that composes Workflow; an edge that would
 * close a cycle is refused. A create may give its blockers. Deleting a
 * blocker removes its edges.
 */
@declaresBehavior({ requires: ["Workflow"], vetoes: DependenciesVeto })
export abstract class Dependencies<C extends DependenciesConfig> {
  /**
   * Whether a blocker is not finished: its status is not a terminal state of
   * its schema's Workflow whose outcome the config accepts.
   */
  @volatile blocked: unknown;

  /**
   * Makes another instance block this one. It must be of the type's own
   * schema or one the config lists, exist, and not be blocked by this
   * instance, directly or through others. An instance in a gated state that
   * is terminal takes no blocker that is not finished.
   * @writes
   * @returns The blocker.
   */
  abstract addBlocker(params: AddBlockerParams): AddBlockerResult;

  /**
   * Stops another instance from blocking this one.
   * @writes
   * @returns The blocker removed.
   */
  abstract removeBlocker(params: RemoveBlockerParams): RemoveBlockerResult;

  /**
   * Lists the instance's blockers in the order they were added, one page at a
   * time, with each one's status and whether it blocks.
   */
  abstract listBlockers(params: ListBlockersParams): ListBlockersResult;

  /**
   * Lists the instances this one blocks, of every schema the caller may read,
   * in the order they were added, one page at a time.
   */
  abstract listDependents(params: ListDependentsParams): ListDependentsResult;

  /**
   * What a create gives the behavior.
   */
  abstract create(params: DependenciesCreateParams): void;

  /**
   * On a table, one row per blocker of the row, keyed by its number on the
   * row, the order they were added in: the blocker's table and key, and who
   * added it when. removeBlocker deletes its row, and the row's delete
   * deletes them all.
   */
  @subTable({ key: "number" })
  static blockers = class {
    /**
     * The edge's number on the row, one past the greatest when it was added,
     * in ten digits zero-padded so the key's order is theirs.
     */
    number: string;
    /** The blocker's table. */
    blockerType: string;
    /** The blocker's key, a hyphenated UUID kept in base62. */
    blockerKey: string;
    /**
     * The subject of the caller that added it, a hyphenated UUID kept in
     * base62; absent for a caller with none.
     */
    addedBy: string;
    /** When it was added, in epoch milliseconds. */
    addedAt: Temporal.DateTime;
  };
}
