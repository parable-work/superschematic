// Pool's class (D103): the build derives pool.behavior.json from it
// (make behaviors), which the behaviors' core and the engine read.
import { Temporal } from "superscalar";
import { declaresBehavior, type Integer, type JsonSchema } from "@superschematic/db";

/** A string that is not empty. */
type Named = JsonSchema<string, { minLength: 1 }>;

interface PoolConfig {
  /**
   * The own number field whose value ranks the members, lowest first, a
   * member with no value after every one with one, ties by key.
   */
  order: Named;
  /**
   * How long markOut, and a check that finds no room, puts a member out when
   * it names no time, in milliseconds; 1800000, thirty minutes, when absent.
   */
  outForMs?: JsonSchema<Integer, { minimum: 1000 }>;
  /**
   * How often an out member past its outUntil is rechecked until a check
   * comes, in milliseconds, and the least time after a check without room
   * before the next; 120000, two minutes, when absent.
   */
  recheckAfterMs?: JsonSchema<Integer, { minimum: 1000 }>;
  /**
   * The permission a caller needs for markOut and reportRoom; any caller the
   * route admits when absent. pick needs none.
   */
  permission?: Named;
}

interface MarkOutParams {
  /**
   * Until when the member is out, in epoch milliseconds, as the provider
   * named it; outForMs from now when absent, and now for a time already past.
   */
  until?: JsonSchema<Integer, { minimum: 0 }>;
  /** Why it is out: the provider's word, or the caller's. */
  reason: JsonSchema<string, { minLength: 1; maxLength: 256 }>;
}

interface MarkOutResult {
  /** The member's availability: out. */
  availability: "open" | "near" | "out";
  /** Until when the member is out, in epoch milliseconds. */
  outUntil: Integer;
  /** Why it is out. */
  outReason: string;
  /**
   * True when the member was out until then already, for the reason, so
   * nothing was written; absent otherwise.
   */
  unchanged?: boolean;
}

interface ReportRoomParams {
  /** Whether the provider has room for the member now. */
  room: boolean;
  /**
   * With room: whether the member is near its limit, which pick tries after
   * the open members. Read only with room.
   */
  near?: boolean;
  /**
   * With no room: when the provider names the member's room back, in epoch
   * milliseconds. Read only without room.
   */
  resetsAt?: JsonSchema<Integer, { minimum: 0 }>;
  /**
   * With no room: why, the provider's word; when absent, the member's reason
   * when it was out already, else no room. Read only without room.
   */
  reason?: JsonSchema<string, { minLength: 1; maxLength: 256 }>;
}

interface ReportRoomResult {
  /** The member's availability after the check. */
  availability: "open" | "near" | "out";
  /**
   * When the check was recorded, the command's time, in epoch milliseconds.
   */
  checkedAt: Integer;
  /**
   * Until when the member is out, in epoch milliseconds; absent unless it is
   * out.
   */
  outUntil?: Integer;
  /** Why the member is out; absent unless it is out. */
  outReason?: string;
  /** True when the check opened an out member; absent otherwise. */
  opened?: boolean;
}

interface PickParams {
  /**
   * The members to pick among, by the values own fields of the table hold, a
   * null member absent; every member when absent.
   */
  match?: Record<string, JsonSchema<unknown, { type: ["string", "number", "boolean", "null"] }>>;
  /**
   * The members to try first, by the values own fields of the table hold, a
   * null member absent; every member when absent.
   */
  prefer?: Record<string, JsonSchema<unknown, { type: ["string", "number", "boolean", "null"] }>>;
  /**
   * The keys of the members the caller tried in this attempt, which pick
   * leaves out.
   */
  exclude?: JsonSchema<Named[], { maxItems: 100 }>;
}

interface PickResult {
  /** The member picked; null when no member match keeps is open or near. */
  member: {
    /** The member's key, in its wire form. */
    key: string;
    /** The member's availability: open or near. */
    availability: "open" | "near" | "out";
    /**
     * The member's value of the config's order field; null when it has none.
     */
    order: number | null;
    /**
     * Whether prefer keeps the member: false when pick fell back to the other
     * members.
     */
    preferred: boolean;
  } | null;
  /**
   * With no member: the soonest time a member match keeps may be back, in
   * epoch milliseconds: an out member's outUntil, or recheckAfterMs from now
   * for one past it awaiting a check, or the time its Capacity gives it room
   * again, the later of the two for a member out both ways; absent when no
   * such member has a time.
   */
  until?: Integer;
}

interface PoolRecheckEvent {
  /** Until when the member was out, in epoch milliseconds. */
  outUntil: Integer;
  /** Why it was out; absent when it has no reason. */
  outReason?: string;
  /**
   * When the last check of it was reported, in epoch milliseconds; absent
   * before the first.
   */
  checkedAt?: Integer;
  /** When the recheck was due, in epoch milliseconds. */
  dueAt: Integer;
  /**
   * When it fired, in epoch milliseconds: the later of its due time and the
   * worker's clock.
   */
  firedAt: Integer;
}

/** The codes Pool's refusals carry. */
enum PoolVeto {
  /**
   * pick of a table more members of which match keeps than it reads, 2000;
   * details give the max.
   */
  pool_too_large,
}

/**
 * Members of a pool, the rows of the table, each open, near its limit or out
 * until a time, among which pick chooses one to run work on, a read of the
 * table as a whole. A caller tells the pool what a provider said with markOut
 * and reportRoom. An out member opens only through a check with room made at
 * or after its outUntil, never by the clock alone, as a breaker opens only on
 * its probe. An out member past its outUntil is rechecked at its outUntil and
 * then every recheckAfterMs until a check comes: each recheck emits the event
 * recheck, which the API's behavior worker hands to the project's handler,
 * which asks the provider and reports with reportRoom; the core calls no
 * provider. A member whose table also implements a behavior that implements
 * the contract Capacity, as Budget does, counts as out while it has no room.
 * The behaviors' core alone implements it, on a table; no engine type
 * composes it.
 */
@declaresBehavior({ armsDeadlines: true, vetoes: PoolVeto, asks: { Capacity: "A member whose table's Capacity, Budget's meters, has no room for it counts as out at a pick until the time the contract gives." } })
export abstract class Pool<C extends PoolConfig> {
  /**
   * open, near its limit, or out; absent on a row that predates the use,
   * which reads as open.
   */
  availability: string;
  /**
   * While the member is out, until when, in epoch milliseconds: the earliest
   * time a check may open it; absent while it is open or near.
   */
  outUntil: Temporal.DateTime;
  /**
   * While the member is out, why, as markOut or the check that put it out
   * said; absent while it is open or near.
   */
  outReason: string;
  /**
   * When the last check of the member was reported, in epoch milliseconds;
   * absent before the first.
   */
  checkedAt: Temporal.DateTime;

  /**
   * Puts the member out until until, or outForMs from now when it names none,
   * for reason: what a caller a provider refused tells the pool. An out
   * member keeps the later of its outUntil and the new one and takes the new
   * reason, and a call that moves neither is answered unchanged. Arms the
   * member's recheck at its outUntil. Needs permission.
   * @writes
   * @checks permission
   */
  abstract markOut(params: MarkOutParams): MarkOutResult;

  /**
   * Records a check of the member at its provider, made now. With room it
   * opens an out member whose outUntil has come, near when near says so, and
   * keeps an open or near one open or near as near says; a check with room
   * before an out member's outUntil records checkedAt alone. With no room the
   * member is out until the later of its outUntil and resetsAt, or outForMs
   * from now when the check names no time, and its recheck is armed then, or
   * recheckAfterMs from now when that is later. Needs permission.
   * @writes
   * @checks permission
   */
  abstract reportRoom(params: ReportRoomParams): ReportRoomResult;

  /**
   * Picks a member to run work on, a read of the table as a whole that writes
   * nothing: of the members match keeps, leaving out those exclude names, the
   * first by order of the open members prefer keeps, then of its near ones,
   * then of the other open and near ones. A member counts as out while its
   * table's Capacity has no room for it. With none it answers member null and
   * until, the soonest time a member match keeps may be back, so a caller
   * waits on a deadline of its own. It reads at most 2000 members.
   * @scope schema
   */
  abstract pick(params: PickParams): PickResult;

  /**
   * An out member past its outUntil with no check since was due a recheck:
   * the project's handler asks the member's provider and reports what it says
   * with reportRoom. Delivery is at least once, so a handler is idempotent by
   * the row's key and dueAt, which no two rechecks of a row share.
   * @event
   */
  abstract recheck(payload: PoolRecheckEvent): void;
}
