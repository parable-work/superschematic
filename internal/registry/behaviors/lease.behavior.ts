// Lease's class (D103): the build derives lease.behavior.json from it
// (make behaviors), which the behaviors' core and the engine read.
import { Generic, Temporal } from "superscalar";
import { declaresBehavior, subTable, volatile, type Integer, type JsonSchema } from "@superschematic/db";

/** A string that is not empty. */
type Named = JsonSchema<string, { minLength: 1 }>;

interface LeaseConfig {
  /**
   * How long a lease lasts after its acquire or its last heartbeat, in
   * milliseconds; 60000 when absent.
   */
  ttlMs?: JsonSchema<Integer, { minimum: 1000 }>;
  /**
   * How often the holder should send a heartbeat, in milliseconds, at most
   * half of ttlMs, so one late heartbeat does not lose the lease; a third of
   * ttlMs when absent. acquire returns it.
   */
  heartbeatMs?: JsonSchema<Integer, { minimum: 1 }>;
  /**
   * How often the engine's runner expires the leases past their expiry time
   * or longest hold, in milliseconds; 5000 when absent.
   */
  sweepMs?: JsonSchema<Integer, { minimum: 1000 }>;
  /**
   * The longest a principal may hold a lease after acquiring it, in
   * milliseconds, renewed or not; no limit when absent.
   */
  maxHoldMs?: JsonSchema<Integer, { minimum: 1000 }>;
  /**
   * An integer field of the type that gives the instance's own longest hold,
   * in milliseconds. When the instance holds a positive integer there, it
   * overrides maxHoldMs.
   */
  maxHoldField?: Named;
  /**
   * Moves the instance's Workflow status when a lease ends with the work
   * unfinished: at an expiry, an abandon and a release, while the status is
   * one of from. Needs Workflow on the type.
   */
  onExpiry?: {
    /** The state to move to, through Workflow's transition. */
    transition: Named;
    /** The states the status moves from; in any other state it stays. */
    from: JsonSchema<Named[], { minItems: 1; uniqueItems: true }>;
  };
  /**
   * How many expiries an instance may have, abandons included. Once it has
   * had this many, its lease cannot be acquired again until resetExpiries.
   */
  maxExpiries?: JsonSchema<Integer, { minimum: 1 }>;
  /**
   * Used instead of onExpiry at the expiry or abandon that reaches
   * maxExpiries, which it needs. Needs Workflow on the type.
   */
  escalate?: {
    /** The state to move to, through Workflow's transition. */
    transition: Named;
    /** The states the status moves from; in any other state it stays. */
    from: JsonSchema<Named[], { minItems: 1; uniqueItems: true }>;
  };
  /**
   * Writing operations of the type's other behaviors, each as
   * <Behavior>.<operation>, that a principal other than the holder may run
   * while the lease is active, and that requireToken does not hold to the
   * token.
   */
  exempt?: JsonSchema<JsonSchema<string, { pattern: "^([A-Za-z][A-Za-z0-9_-]*\\.)?[A-Z][A-Za-z0-9]*\\.[a-z][A-Za-z0-9]*$" }>[], { uniqueItems: true }>;
  /**
   * When true, a write to an instance with an active lease that does not
   * present the current token as Lease's precondition is refused, the
   * holder's own included, so every process of a principal fences its writes.
   * Exempt and read-only operations, Lease's own operations and a principal
   * with overridePermission are not held to it.
   */
  requireToken?: boolean;
  /**
   * The permission a principal needs to acquire a lease. Absent, any
   * principal who may write the instance can.
   */
  acquirePermission?: Named;
  /**
   * The permission that lets a principal release another principal's lease,
   * change the instance while another principal holds its lease, reset its
   * expiries, and send directives when directPermission is absent.
   */
  overridePermission?: Named;
  /**
   * The permission a principal needs to send the holder a directive;
   * overridePermission when absent. With neither, direct is refused.
   */
  directPermission?: Named;
  /**
   * Directives the engine's runner sends the holder of an instance's active
   * lease when the target of one of the instance's links moves on: a new
   * revision of Revisions, or a release of Branches; for a pinned link, a
   * revision tells only the instances the target moved past. The runner sends
   * each with direct, as its principal, which needs directPermission, or
   * overridePermission when that is absent, once per lease and move
   * (dedupeKey). An instance with no lease held hears nothing, and its next
   * holder nothing either. Needs Links on the type; one entry per link.
   */
  directOn?: JsonSchema<{
    /** The link whose target's move sends the directive. */
    revised: {
      /** A link of the type's Links config. */
      link: JsonSchema<string, { pattern: "^[a-z][A-Za-z0-9]{0,63}$" }>;
    };
    /** The directive's name, as direct takes it. */
    name: JsonSchema<string, { pattern: "^[A-Za-z][A-Za-z0-9_.:-]{0,63}$" }>;
    /**
     * Its data, beside revised, which the runner sets: the link, the target's
     * schema and id, and its revision or the release's commit.
     */
    data?: object;
  }[], { minItems: 1; maxItems: 16 }>;
  /**
   * The field of the type whose Workflow onExpiry and escalate move, and in
   * whose terminal states an expiry does not count. Implied when the type has
   * one Workflow; needed when it has several. A Workflow anchored on the type
   * is named Workflow. A table's alone: an engine type refuses it.
   */
  status?: Named;
}

interface AcquireParams {
  /**
   * How long the lease lasts after its acquire and each heartbeat, in
   * milliseconds, at most the config's ttlMs; the config's when absent.
   */
  ttlMs?: JsonSchema<Integer, { minimum: 1000 }>;
}

interface AcquireResult {
  /**
   * The lease's fencing token, which the holder's writes present as Lease's
   * precondition.
   */
  token: Integer;
  /** When the lease expires unless renewed, in epoch milliseconds. */
  expiresAt: Integer;
  /** How often to send a heartbeat, in milliseconds. */
  heartbeatMs: Integer;
}

interface HeartbeatParams {
  /**
   * Directives of the lease to mark handled first, as acknowledge does, so a
   * heartbeat acknowledges in the same write; empty, none.
   */
  acknowledge?: JsonSchema<JsonSchema<Integer, { minimum: 1 }>[], { maxItems: 500; uniqueItems: true }>;
}

interface HeartbeatResult {
  /** When the lease now expires unless renewed, in epoch milliseconds. */
  expiresAt: Integer;
  /**
   * The directives sent under this lease and not acknowledged, oldest first.
   */
  directives: {
    /** The directive's number in its lease. */
    id: Integer;
    name: string;
    data?: object;
    /** The key it was sent with, when it was. */
    dedupeKey?: string;
    createdAt: Integer;
    createdBy: string;
  }[];
}

interface ReleaseParams {
  /**
   * Whether the work is given up as failed, so the release counts as an
   * expiry.
   */
  abandon?: boolean;
}

type ReleaseResult = {};

interface ExpireParams {
  /**
   * The principal whose lease to expire now, active or not; it needs
   * overridePermission.
   */
  holder?: Named;
  /**
   * With holder: an active lease its holder acquired or renewed after this
   * time, in epoch milliseconds, is spared, since its own heartbeats show the
   * process holding it is alive. A lapsed one expires all the same.
   */
  notRenewedAfter?: JsonSchema<Integer, { minimum: 0 }>;
}

interface ExpireResult {
  /** Whether a lease expired. */
  expired: boolean;
  /**
   * Why, when one expired: ttl, its holder stopped renewing it; maxHold, it
   * reached its longest hold; holder, it was active and expired by its
   * holder's name.
   */
  reason?: JsonSchema<"ttl" | "maxHold" | "holder", { type: "string" }>;
}

interface DirectParams {
  /** What the directive asks. */
  name: JsonSchema<string, { pattern: "^[A-Za-z][A-Za-z0-9_.:-]{0,63}$" }>;
  /** Its data. */
  data?: object;
  /**
   * Sends the directive once per lease: when one was sent under the current
   * token with this key, acknowledged or not, that one stands and nothing is
   * sent.
   */
  dedupeKey?: JsonSchema<string, { minLength: 1; maxLength: 128 }>;
}

interface DirectResult {
  /**
   * The directive's number in its lease: the one sent, or the one already
   * sent with dedupeKey.
   */
  id: Integer;
  /**
   * Whether this call sent it; false when one with dedupeKey was already
   * sent.
   */
  created: boolean;
}

interface AcknowledgeParams {
  /** The directives' numbers. */
  ids: JsonSchema<JsonSchema<Integer, { minimum: 1 }>[], { minItems: 1; maxItems: 500; uniqueItems: true }>;
}

type AcknowledgeResult = {};

interface ExpireHolderParams {
  /** The principal whose leases to expire. */
  holder: Named;
  /**
   * As expire's: an active lease acquired or renewed after this time is
   * spared.
   */
  notRenewedAfter?: JsonSchema<Integer, { minimum: 0 }>;
}

interface ExpireHolderResult {
  /** How many leases expired. */
  expired: Integer;
  /**
   * How many expired for each reason expire gives, by reason; a reason none
   * expired for is absent.
   */
  reasons: {
    ttl?: Integer;
    maxHold?: Integer;
    holder?: Integer;
  };
  /** The instances whose lease expired, by id. */
  ids: string[];
}

interface ResetExpiriesResult {
  /** The count before the reset. */
  expiries: Integer;
}

interface LeasePrecondition {
  /**
   * The lease's current fencing token, as acquire returned it: a write that
   * presents another is refused (token_stale).
   */
  token: JsonSchema<Integer, { minimum: 0 }>;
}

/** The codes Lease's refusals carry. */
enum LeaseVeto {
  /** Another principal holds the instance's active lease. */
  held_by_another,
  /**
   * The caller already holds the active lease: acquire refuses it, and
   * heartbeat renews it.
   */
  held_by_caller,
  /** No principal holds a lease on the instance. */
  not_leased,
  /** The caller does not hold the lease the call needs it to hold. */
  not_holder,
  /**
   * The lease is past its expiry time or its longest hold: it gives its
   * holder nothing, and only its expiry can follow.
   */
  lapsed,
  /**
   * The token the call presents as Lease's precondition is not the lease's
   * current one: a newer lease, or none, has replaced the one it names.
   */
  token_stale,
  /**
   * The call presents no token as Lease's precondition, and needs one: a
   * heartbeat, an acknowledgement or a holder's release, or any write under
   * requireToken while the lease is active.
   */
  token_required,
  /**
   * The instance has had maxExpiries expiries, so its lease cannot be
   * acquired until resetExpiries.
   */
  max_expiries,
  /** maxHoldField cannot change while a lease is held. */
  hold_limit_fixed,
  /**
   * The config names no permission that allows the call: direct without
   * directPermission or overridePermission, resetExpiries without
   * overridePermission.
   */
  not_configured,
}

/**
 * An exclusive, time-bounded lease on the instance, held by one principal and
 * numbered by a fencing token that advances at every acquire, release and
 * expiry. While the lease is active only its holder may change the instance,
 * and a write that presents a token other than the current one, as Lease's
 * precondition, is refused whoever calls; a heartbeat with the current token
 * renews the lease and delivers the directives sent to the holder. A lease
 * its holder stops renewing expires, and the expiry can move the instance's
 * Workflow status back.
 */
@declaresBehavior({ armsDeadlines: true, vetoes: LeaseVeto })
export abstract class Lease<C extends LeaseConfig> {
  /**
   * The subject of the principal that holds the lease; absent when the
   * instance is free.
   */
  holder: string;
  /**
   * The fencing token. It advances at every acquire, release and expiry, and
   * a write under the lease presents it.
   */
  token: Generic.Int64;
  /**
   * When the lease was acquired, in epoch milliseconds; absent when the
   * instance is free.
   */
  acquiredAt: Temporal.DateTime;
  /**
   * Its acquire or its last heartbeat, in epoch milliseconds; absent when the
   * instance is free.
   */
  renewedAt: Temporal.DateTime;
  /**
   * When the lease expires unless it is renewed, in epoch milliseconds;
   * absent when the instance is free.
   */
  expiresAt: Temporal.DateTime;
  /** Whether a lease is held and has not expired. */
  @volatile active: unknown;
  /**
   * How many leases have expired or been abandoned since the last
   * resetExpiries.
   */
  expiries: Generic.Int64;
  /**
   * How and when the last lease ended (reason and at); absent while one is
   * held.
   */
  ended: Generic.JSON;

  /**
   * Takes the lease for the caller and returns its fencing token. Refused
   * while a lease is active, the caller's own included (heartbeat renews it),
   * without acquirePermission when the config names one, and once the
   * instance has had maxExpiries expiries. Over a lease that expired, it
   * applies that expiry first.
   * @writes
   */
  abstract acquire(params: AcquireParams): AcquireResult;

  /**
   * Renews the lease, never past its longest hold, acknowledges the
   * directives acknowledge names, and returns the directives sent under it
   * that are not acknowledged. Only the holder, presenting the current token
   * as Lease's precondition, while the lease is active: an expired lease
   * cannot be renewed.
   * @writes
   */
  abstract heartbeat(params: HeartbeatParams): HeartbeatResult;

  /**
   * Ends an active lease: the holder, presenting the current token as Lease's
   * precondition, or a principal with overridePermission, who need not. The
   * token advances, so the directives sent under the lease are not delivered
   * again, and onExpiry moves the status when it is still one of its from
   * states. With abandon, the holder gives the work up as failed: the release
   * counts as an expiry, and escalate applies at the one that reaches
   * maxExpiries.
   * @writes
   */
  abstract release(params: ReleaseParams): ReleaseResult;

  /**
   * Applies the expiry of a lease past its expiry time or its longest hold:
   * the holder is cleared, the token advances, the expiry is counted unless
   * the instance is in a terminal state of its Workflow, and onExpiry or
   * escalate moves the status. Any principal who may write the instance may
   * call it; on any other instance it changes nothing. With holder, a
   * principal with overridePermission expires that holder's lease at once,
   * active or not, unless notRenewedAfter spares it. The engine's runner
   * calls it on every lapsed lease, every sweepMs.
   * @writes
   */
  abstract expire(params: ExpireParams): ExpireResult;

  /**
   * Sends the holder of the active lease a directive, which its heartbeats
   * deliver until it acknowledges it. With dedupeKey, a directive already
   * sent under the lease with the key is not sent again. A principal needs
   * directPermission, or overridePermission when that is absent, and is
   * refused when the config names neither; another behavior of the type sends
   * one with call(), as the principal it runs for, without either.
   * @writes
   */
  abstract direct(params: DirectParams): DirectResult;

  /**
   * Marks directives of the lease as handled, so heartbeats stop delivering
   * them. Only the holder, presenting the current token as Lease's
   * precondition.
   * @writes
   */
  abstract acknowledge(params: AcknowledgeParams): AcknowledgeResult;

  /**
   * Expires every lease one principal holds on the schema's instances, active
   * or not, as expire with that holder does on each: for a principal that is
   * gone. With notRenewedAfter, an active lease renewed after that time is
   * spared. Needs overridePermission; refused when the config names none.
   * @writes
   * @scope schema
   */
  abstract expireHolder(params: ExpireHolderParams): ExpireHolderResult;

  /**
   * Sets the instance's count of expiries back to 0, so a lease can be
   * acquired again after maxExpiries. Needs overridePermission; refused when
   * the config names none.
   * @writes
   */
  abstract resetExpiries(): ResetExpiriesResult;

  /**
   * The precondition a caller presents the behavior.
   */
  abstract precondition(entry: LeasePrecondition): void;

  /**
   * One row per directive sent to the holder of the row's lease, keyed by its
   * number in the lease: the token it was sent under, what it asks, its data,
   * the dedupeKey it was sent with, who sent it and when, and when the holder
   * acknowledged it. Every end of a lease deletes its directives, so none
   * reaches the next holder.
   */
  @subTable({ key: "number" })
  static directives = class {
    /**
     * The directive's number in its lease, from 1 in the order they were
     * sent, in ten digits zero-padded so the key's order is theirs.
     */
    number: string;
    /** The lease's token it was sent under. */
    token: Generic.Int64;
    /** What the directive asks. */
    name: string;
    /** Its data. */
    data: Generic.JSON;
    /** The key it was sent with, when it was. */
    dedupeKey: string;
    /**
     * The subject of the caller who sent it, or of the principal a behavior's
     * call ran for.
     */
    sentBy: string;
    /** When it was sent, in epoch milliseconds. */
    sentAt: Temporal.DateTime;
    /**
     * When the holder acknowledged it; absent while heartbeats deliver it.
     */
    acknowledgedAt: Temporal.DateTime;
  };
}
