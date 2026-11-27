// Presence's class (D103): the build derives presence.behavior.json from it
// (make behaviors), which the behaviors' core and the engine read.
import { Generic, Temporal } from "superscalar";
import { declaresBehavior, type Integer, type JsonSchema } from "@superschematic/db";

/** A string that is not empty. */
type Named = JsonSchema<string, { minLength: 1 }>;

interface PresenceConfig {
  /**
   * How long the instance stays present after its create or its last beat, in
   * milliseconds.
   */
  ttlMs: JsonSchema<Integer, { minimum: 1000 }>;
  /**
   * A string field of the type that holds the subject of the principal the
   * instance stands for, the only principal that may beat it. Once set, it
   * cannot change.
   */
  principalField: Named;
  /**
   * Moves the instance's Workflow status when it is missed, while the status
   * is one of from. Needs Workflow on the type.
   */
  onMissed?: {
    /** The state to move to, through Workflow's transition. */
    transition: Named;
    /** The states the status moves from; in any other state it stays. */
    from: JsonSchema<Named[], { minItems: 1; uniqueItems: true }>;
  };
  /**
   * Moves the instance's Workflow status at a beat, while the status is one
   * of from: the way back from the state onMissed moves it to. Needs Workflow
   * on the type.
   */
  onBeat?: {
    /** The state to move to, through Workflow's transition. */
    transition: Named;
    /** The states the status moves from; in any other state it stays. */
    from: JsonSchema<Named[], { minItems: 1; uniqueItems: true }>;
  };
  /**
   * Schemas that compose Lease, whose leases the principal holds are expired
   * when the instance is missed, through Lease's schema-level expireHolder,
   * but for a lease renewed after the principal's last beat, whose own
   * heartbeats show it alive. The principal that applies the miss, the
   * runner's in the sweep, needs Lease's overridePermission on each.
   */
  releaseLeases?: JsonSchema<JsonSchema<string, { pattern: "^[A-Za-z][A-Za-z0-9_-]{0,127}$" }>[], { uniqueItems: true }>;
  /**
   * How often the engine's runner misses the instances past their deadline,
   * in milliseconds; 5000 when absent.
   */
  sweepMs?: JsonSchema<Integer, { minimum: 1000 }>;
  /**
   * How long the row stays present in a state of its Workflow, in
   * milliseconds, by state: a beat arms the deadline its row's state gives,
   * and a transition arms it again, counted from the later of the last beat
   * and the entry into the state; ttlMs for a state it does not list. Needs
   * Workflow on the type. A table's alone: an engine type refuses it.
   */
  ttlByState?: JsonSchema<Record<string, JsonSchema<Integer, { minimum: 1000 }>>, { minProperties: 1 }>;
  /**
   * The field of the type whose Workflow onMissed, onBeat and ttlByState read
   * and move. Implied when the type has one Workflow; needed when it has
   * several. A Workflow anchored on the type is named Workflow. A table's
   * alone: an engine type refuses it.
   */
  status?: Named;
}

interface BeatResult {
  /**
   * When the instance is missed unless beaten again, in epoch milliseconds.
   */
  deadline: Integer;
}

interface MissResult {
  /** Whether this call missed the instance. */
  missed: boolean;
  /**
   * When it did: the instances whose lease it expired, by releaseLeases
   * schema; empty when another instance that stands for the principal is
   * present.
   */
  released?: Record<string, string[]>;
}

/** The codes Presence's refusals carry. */
enum PresenceVeto {
  /**
   * The instance's principalField holds no principal, so no one may beat it.
   */
  no_principal,
  /** The instance stands for another principal, who alone may beat it. */
  not_principal,
  /**
   * principalField holds the principal the instance stands for, and does not
   * change once set.
   */
  principal_fixed,
}

/**
 * A heartbeat on an instance that stands for a worker: the principal whose
 * subject the instance's principalField holds beats it, and an instance not
 * beaten within ttlMs of its create or its last beat is missed. A miss can
 * move the instance's Workflow status and expire the leases the principal
 * holds on the schemas the config names, but for the ones renewed since its
 * last beat; the engine's runner applies the misses every sweepMs.
 */
@declaresBehavior({ armsDeadlines: true, vetoes: PresenceVeto })
export abstract class Presence<C extends PresenceConfig> {
  /**
   * When the instance is missed unless it is beaten, in epoch milliseconds:
   * ttlMs after its create or its last beat; absent on an instance made
   * before the schema composed Presence, until its first beat.
   */
  deadline: Temporal.DateTime;
  /**
   * When the instance was last beaten, in epoch milliseconds; absent before
   * the first beat.
   */
  lastBeatAt: Temporal.DateTime;
  /**
   * Whether the instance missed its deadline and has not been beaten since.
   */
  missed: boolean;
  /**
   * The instances whose lease the last miss expired, by schema; absent before
   * a miss.
   */
  released: Generic.JSON;

  /**
   * Moves the deadline to ttlMs from now and clears missed, and onBeat moves
   * the status when it is one of its from states. Only the principal whose
   * subject the instance's principalField holds.
   * @writes
   */
  abstract beat(): BeatResult;

  /**
   * Applies the miss of an instance past its deadline and not missed yet:
   * sets missed, moves the status with onMissed when it is one of its from
   * states, and expires the principal's leases on the releaseLeases schemas
   * that were not renewed after its last beat, recording which in released.
   * Any principal who may write the instance may call it; on any other
   * instance it changes nothing. The engine's runner calls it every sweepMs.
   * @writes
   */
  abstract miss(): MissResult;
}
