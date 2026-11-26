// Reconcile's class (D103): the build derives reconcile.behavior.json from it
// (make behaviors), which the behaviors' core and the engine read.
import { Generic, Temporal } from "superscalar";
import { declaresBehavior, valueField, type Integer, type JsonSchema } from "@superschematic/db";

/** A string that is not empty. */
type Named = JsonSchema<string, { minLength: 1 }>;

interface ReconcileConfig {
  /**
   * The field of the type whose Workflow an observation moves; a Workflow
   * anchored on the type is named Workflow.
   */
  status: Named;
  /**
   * The state of that Workflow the stale deadline moves a row to: every state
   * but a terminal one reaches it, and it reaches every state. Needs
   * staleAfterMs.
   */
  unreachable?: Named;
  /**
   * How long after an observation's at a row goes stale, in milliseconds: its
   * deadline then moves the row to unreachable. Needs unreachable.
   */
  staleAfterMs?: JsonSchema<Integer, { minimum: 1000 }>;
  /** The permission a caller needs to observe. */
  observePermission: Named;
}

interface ObserveParams {
  /** The state the provider reports, a state of the Workflow status names. */
  state: Named;
  /**
   * When the provider was observed in it, in epoch milliseconds, no later
   * than the call.
   */
  at: JsonSchema<Integer, { minimum: 0 }>;
}

interface ObserveResult {
  /** The observed state after the call. */
  observed: string;
  /** When it was observed, in epoch milliseconds. */
  observedAt: Integer;
  /**
   * The Workflow's status after the call; null for a row with no status,
   * which only a raw write leaves.
   */
  status: string | null;
  /** The transitions made, in order; [] for none. */
  moves: {
    /** The state the row was in. */
    from: string;
    /** The state it moved to. */
    to: string;
  }[];
  /** Why the status is not the observed state; absent while it is. */
  drift?: {
    /** The status where the path stopped; absent for a row with no status. */
    status?: string;
    /** The state it was moving to. */
    observed: string;
    /**
     * The veto code of the step refused, forbidden for a permission the
     * observer lacks, no_path when no transitions lead there, or no_status.
     */
    code: string;
  };
  /**
   * True when the observation was no newer than the last one, so nothing was
   * written; absent otherwise.
   */
  unchanged?: boolean;
}

/**
 * The state a row's provider last reported, beside the Workflow field it
 * moves. observe records an observation newer than the last, then moves the
 * Workflow to the observed state along the fewest transitions its config
 * allows, each through Workflow's transition as the observer, so every guard
 * and permission holds each step; a step refused stops the path and records
 * drift. With staleAfterMs, a row no observation has reached for that long
 * moves to unreachable when its deadline falls due. The behaviors' core alone
 * implements it, on a field of a table; no engine type composes it.
 */
@declaresBehavior({ requires: ["Workflow"], armsDeadlines: true })
export abstract class Reconcile<C extends ReconcileConfig> {
  /**
   * The state of the Workflow the last observation reported, or the
   * unreachable state once the stale deadline fell due; absent before the
   * first observation.
   */
  @valueField observed?: string;
  /**
   * When the last observation was made, its at, in epoch milliseconds; absent
   * before the first.
   */
  observedAt: Temporal.DateTime;
  /**
   * Why the status is not the observed state: { status, observed, code }, the
   * status where the path stopped, the state it was moving to, and the veto
   * code of the step refused, forbidden for a permission the observer lacks,
   * or no_path when no transitions lead there; absent while the status is the
   * observed state.
   */
  drift: Generic.JSON;
  /**
   * When an observation or the stale deadline last left the status at the
   * observed state, in epoch milliseconds; absent before.
   */
  reconciledAt: Temporal.DateTime;

  /**
   * Records the state the provider reports, when at is newer than the last
   * observation's, and moves the Workflow there along the fewest transitions
   * its config allows, each as the caller, so every guard and permission
   * holds each step; a step refused stops there and records drift. An
   * observation no newer is answered unchanged. Needs observePermission.
   * @writes
   */
  abstract observe(params: ObserveParams): ObserveResult;
}
