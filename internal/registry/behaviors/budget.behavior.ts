// Budget's class (D103): the build derives budget.behavior.json from it
// (make behaviors), which the behaviors' core and the engine read.
import { Generic, Temporal } from "superscalar";
import { declaresBehavior, subTable, volatile, type Integer, type JsonSchema, type Capacity } from "@superschematic/db";

/** A string that is not empty. */
type Named = JsonSchema<string, { minLength: 1 }>;

interface BudgetConfig {
  /** The meters, by name: camelCase, at most 64 characters. */
  meters: JsonSchema<Record<JsonSchema<string, { pattern: "^[a-z][A-Za-z0-9]{0,63}$" }>, {
    /**
     * The most the instance may have used and reserved together; with
     * limitField, the limit while the field holds no value of at least 0. No
     * limit of its own when neither gives one.
     */
    limit?: JsonSchema<Integer, { minimum: 1 }>;
    /**
     * An integer field of the type that holds the instance's limit; while it
     * holds no value of at least 0, limit is the limit, and without limit
     * there is none. Only a principal with limitPermission changes it once
     * the instance exists.
     */
    limitField?: Named;
    /**
     * The amount reserve takes for the meter when it is given no meter: what
     * a claim reserves. With reserveField, the amount when the field holds no
     * positive integer.
     */
    reserve?: JsonSchema<Integer, { minimum: 1 }>;
    /**
     * An integer field of the type whose positive value is the amount reserve
     * takes for the meter when it is given no meter, in place of reserve.
     */
    reserveField?: Named;
    /**
     * A link of the type's Links config whose target is the enclosing scope:
     * its schema composes Budget with the same meter, and every reservation
     * and usage of the meter here is taken there too, and on up its own
     * scope.
     */
    scope?: JsonSchema<string, { pattern: "^[a-z][A-Za-z0-9]{0,63}$" }>;
    /**
     * When the used amount starts again at 0, in UTC; reservations are kept.
     * daily: at each UTC day. hourly, weekly (each Monday), monthly (each
     * first of the month) and { everyMs } (at each multiple of everyMs from
     * the epoch, a whole number of minutes) are a table's alone: an engine
     * type refuses them.
     */
    reset?: "hourly" | "daily" | "weekly" | "monthly" | {
      /** The period's length, in milliseconds. */
      everyMs: JsonSchema<Integer, { minimum: 60000; multipleOf: 60000 }>;
    };
    /**
     * rolling: the meter counts the usage of its last period rather than of
     * the current one, from buckets of a sixtieth of the period each (a month
     * is thirty days), so no boundary lets twice the limit through. Needs
     * reset. A table's alone: an engine type refuses it.
     */
    window?: "rolling";
  }>, { minProperties: 1 }>;
  /**
   * The permission a principal needs to change a meter's limit, with setLimit
   * or through limitField. Absent, no limit changes once the instance exists.
   */
  limitPermission?: Named;
  /**
   * The permission a caller needs to record the usage a provider reports with
   * observe; absent, observe is refused (not_configured). A table's alone: an
   * engine type refuses it.
   */
  observePermission?: Named;
  /**
   * A directive sent to the holder of the instance's active lease, through
   * Lease's direct, when usage takes the instance or an enclosing scope over
   * its limit: once per meter per lease. Needs Lease on the type.
   */
  onExceeded?: {
    /**
     * The directive's name. Its data is { meter, used, limit, scope }, scope
     * being the instance over its limit as { schema, id }.
     */
    direct: JsonSchema<string, { pattern: "^[A-Za-z][A-Za-z0-9_.:-]{0,63}$" }>;
  };
  /**
   * Moves the instance's Workflow status when usage leaves it over the limit
   * of a meter, while the status is one of from, in the transaction of that
   * usage: an instance's own usage, or one an instance inside it passes up,
   * so a scope's config moves the scope. A move a guard vetoes or the caller
   * may not make leaves the status, and the usage counts all the same. Needs
   * Workflow on the type.
   */
  escalate?: {
    /** The state to move to, through Workflow's transition. */
    transition: Named;
    /** The states the status moves from; in any other state it stays. */
    from: JsonSchema<Named[], { minItems: 1; uniqueItems: true }>;
  };
  /**
   * The field of the type whose Workflow escalate moves. Implied when the
   * type has one Workflow; needed when it has several. A Workflow anchored on
   * the type is named Workflow. A table's alone: an engine type refuses it.
   */
  status?: Named;
}

interface ReserveParams {
  /** The meter; every meter with a configured reservation when absent. */
  meter?: Named;
  /**
   * How much to reserve of the meter; its configured reservation when absent.
   * Needs meter.
   */
  amount?: JsonSchema<Integer, { minimum: 1 }>;
}

interface ReserveResult {
  /** What this call reserved, by meter. */
  reserved: Record<string, Integer>;
}

interface CheckReserveParams {
  /** The meter; every meter with a configured reservation when absent. */
  meter?: Named;
  /**
   * How much of the meter; its configured reservation when absent. Needs
   * meter.
   */
  amount?: JsonSchema<Integer, { minimum: 1 }>;
}

interface CheckReserveResult {
  /** Whether the reservation fits now. */
  fits: boolean;
  /**
   * When it does not fit: the start of the next UTC day, when the daily
   * meters starting again make it fit with nothing else changing; null when
   * only a change can make it fit, and when it fits.
   */
  until: Integer | null;
  /**
   * The enclosing scopes the answer read, innermost first, each with the
   * values of it the answer turns on: a move of one can change it.
   */
  scopes: {
    schema: string;
    id: string;
    /**
     * The values of the scope the answer turns on, as a reference hears them:
     * a JSON pointer into the scope as a read returns it
     * (/behaviors/Budget/meters/<meter>/remaining,
     * /behaviors/Links/targets/<link>), and, for a number, the value it
     * changes at when it crosses it. The meter's remaining, which the
     * reservation fits while it is at or above the number; where a daily
     * meter keeps it out now, the scope's reserved and limit; and the link to
     * the scope's own scope.
     */
    hears: {
      path: string;
      crosses?: number;
    }[];
  }[];
}

interface RecordUsageParams {
  /** The meter. */
  meter: Named;
  /** How much was used. */
  amount: JsonSchema<Integer, { minimum: 1 }>;
}

interface RecordUsageResult {
  meter: string;
  /** The instance's usage of the meter after this call. */
  used: Integer;
  /** The part of the instance's own reservation the usage drew down. */
  released: Integer;
  /**
   * Every instance of the chain whose usage of the meter is over its limit
   * after this call, this one first, then outward.
   */
  overruns: {
    schema: string;
    id: string;
    used: Integer;
    limit: Integer;
    /** Whether its escalate moved its status in this call. */
    escalated: boolean;
  }[];
  /** Whether this call sent onExceeded's directive. */
  directed: boolean;
}

interface SettleParams {
  /** The meter; every meter when absent. */
  meter?: Named;
}

interface SettleResult {
  /** What this call released, by meter. */
  released: Record<string, Integer>;
}

interface SetLimitParams {
  /** The meter. */
  meter: Named;
  /** The new limit. */
  limit: JsonSchema<Integer, { minimum: 0 }>;
}

interface SetLimitResult {
  meter: string;
  limit: Integer;
  /** The limit before; null when there was none. */
  previous: Integer | null;
}

interface ReserveForParams {
  /** The meter. */
  meter: Named;
  /** The schema of the instance inside this scope. */
  schema: Named;
  /** Its id. */
  id: Named;
  /** How much to hold. */
  amount: JsonSchema<Integer, { minimum: 1 }>;
}

interface ReserveForResult {
  /** What this instance now holds of the meter for that one. */
  held: Integer;
}

interface SettleForParams {
  /** The meter. */
  meter: Named;
  /** The schema of the instance inside this scope. */
  schema: Named;
  /** Its id. */
  id: Named;
  /** How much to release. */
  amount: JsonSchema<Integer, { minimum: 1 }>;
}

interface SettleForResult {
  /** What this instance released. */
  released: Integer;
}

interface RecordUsageForParams {
  /** The meter. */
  meter: Named;
  /** The schema of the instance inside this scope. */
  schema: Named;
  /** Its id. */
  id: Named;
  /** How much it used. */
  amount: JsonSchema<Integer, { minimum: 1 }>;
  /** How much of its reservation the usage drew down. */
  released: JsonSchema<Integer, { minimum: 0 }>;
}

interface RecordUsageForResult {
  /** What this instance released. */
  released: Integer;
  /**
   * This instance and every scope up from it whose usage of the meter is over
   * its limit after the usage, innermost first.
   */
  overruns: {
    schema: string;
    id: string;
    used: Integer;
    limit: Integer;
    /** Whether its escalate moved its status in this call. */
    escalated: boolean;
  }[];
}

interface ObserveParams {
  /** The meter. */
  meter: Named;
  /** What the provider counts as used. */
  used: JsonSchema<Integer, { minimum: 0 }>;
  /**
   * The limit the provider holds the row to, while the observation holds; the
   * meter's own when absent.
   */
  limit?: JsonSchema<Integer, { minimum: 0 }>;
  /**
   * When the provider's count starts again, in epoch milliseconds: the
   * observation lapses then.
   */
  resetsAt?: JsonSchema<Integer, { minimum: 0 }>;
}

interface ObserveResult {
  meter: string;
  /** The meter's usage after this call: the figure given. */
  used: Integer;
  /** The meter's limit while the observation holds; null when it has none. */
  limit: Integer | null;
  /**
   * When the observation lapses, in epoch milliseconds: resetsAt, else the
   * meter's next reset; null when it holds until the next observe.
   */
  resetsAt: Integer | null;
}

/** The codes Budget's refusals carry. */
enum BudgetVeto {
  /**
   * The reservation does not fit a meter's limit, here or in an enclosing
   * scope (details.scope names which).
   */
  over_limit,
  /** On a type that composes Lease, reserve needs an active lease. */
  not_leased,
  /**
   * The meter's reservation is held through a scope its link no longer points
   * at; it is settled before a reservation through the new one.
   */
  scope_moved,
  /** A scope link does not change while a reservation is held through it. */
  scope_reserved,
  /** A limit below what the instance has used and reserved. */
  below_committed,
  /**
   * A scope holds no more for an instance than that instance has reserved.
   */
  exceeds_reservation,
  /** The config names no limitPermission, which changing a limit needs. */
  not_configured,
}

/**
 * Reserve-then-settle budgets on the instance, in units the deployment names.
 * Each meter counts what the instance has used and the reservations it holds
 * against its limit. A reservation that does not fit here, or in an enclosing
 * scope that a Links link points at, is refused, and the whole chain is left
 * as it was. Usage is never refused: it draws the instance's own reservation
 * down, counts here and in every enclosing scope, reports each instance it
 * takes over its limit, and can move the status of each through escalate.
 * checkReserve answers whether a reservation would fit now, which Queue
 * copies so that work over its budget is not tried. On a type that composes
 * Lease, a reservation lives as long as the lease it was made under.
 */
@declaresBehavior({ vetoes: BudgetVeto })
export abstract class Budget<C extends BudgetConfig> implements /** hasRoom: whether a row has room in every meter now, used and reserved counted as checkReserve counts them, and when not, the earliest time the clock alone gives every meter room again. */ Capacity {
  /**
   * Each meter, by name: used, reserved (this instance's own reservation and
   * what it holds for the instances inside it), limit and remaining (limit
   * minus used and reserved), limit and remaining null when the meter has no
   * limit. A daily meter whose day has passed shows used 0.
   */
  @volatile meters: unknown;

  /**
   * Reserves an amount of one meter, or of every meter the config gives a
   * reservation, for the instance and every enclosing scope. Refused when the
   * instance or an enclosing scope cannot fit it, and then nothing is
   * reserved anywhere. On a type that composes Lease it needs an active
   * lease, and the reservation is settled when that lease ends.
   * @writes
   */
  abstract reserve(params: ReserveParams): ReserveResult;

  /**
   * Whether reserve with the same parameters would fit now, here and in every
   * enclosing scope, after the reservations of a lease that is no longer
   * active are settled, as reserve settles them; it changes nothing and does
   * not ask for a lease. Queue copies its answer so that a claim that cannot
   * fit is not tried.
   */
  abstract checkReserve(params: CheckReserveParams): CheckReserveResult;

  /**
   * Records usage of a meter, which is never refused. The part of it the
   * instance's own reservation covers is released, here and in every
   * enclosing scope, and the usage counts in each of them. Reports every
   * instance of the chain whose usage is over its limit after it, moves the
   * status of each whose config gives escalate, and sends onExceeded's
   * directive to the holder of the active lease once per meter per lease.
   * @writes
   */
  abstract recordUsage(params: RecordUsageParams): RecordUsageResult;

  /**
   * Releases the instance's own reservation of one meter, or of every meter,
   * here and in every enclosing scope.
   * @writes
   */
  abstract settle(params: SettleParams): SettleResult;

  /**
   * Sets a meter's limit on the instance, raising or lowering it; through
   * limitField when the meter has one. Needs limitPermission, and is refused
   * when the config names none, and below what the instance has used and
   * reserved.
   * @writes
   */
  abstract setLimit(params: SetLimitParams): SetLimitResult;

  /**
   * Holds an amount of a meter for an instance whose scope link points here,
   * as that instance's reserve does, and passes it on up this instance's own
   * scope. Refused when the amount does not fit, when that instance does not
   * draw the meter from here, and beyond what it has reserved and this
   * instance does not hold for it yet.
   * @writes
   */
  abstract reserveFor(params: ReserveForParams): ReserveForResult;

  /**
   * Releases up to an amount of what this instance holds of a meter for
   * another, never more than that one no longer holds itself, and passes the
   * release on up this instance's own scope.
   * @writes
   */
  abstract settleFor(params: SettleForParams): SettleForResult;

  /**
   * Counts usage of a meter by an instance whose scope link points here,
   * releases the part of what this instance holds for it that its own
   * reservation covered, never more than that one no longer holds, and passes
   * both on up this instance's own scope. Never refused for the amount.
   * @writes
   */
  abstract recordUsageFor(params: RecordUsageForParams): RecordUsageForResult;

  /**
   * Records the usage of a meter a provider reports, in place of what the
   * meter counts until resetsAt (until the meter's next reset without it, and
   * until the next observe for a meter with no reset): the meter's used is
   * the figure given, usage recorded after it adds to it, and limit, when
   * given, is the meter's limit while it holds. Nothing passes up to an
   * enclosing scope. Needs observePermission. A table's alone: an engine type
   * refuses it.
   * @writes
   */
  abstract observe(params: ObserveParams): ObserveResult;

  /**
   * One row per meter the row has written, keyed by the meter's name: what it
   * has used in its current period, what it has reserved (its own reservation
   * and what it holds for the rows inside it), the limit setLimit gave it,
   * the start of the period its used counts from, its own reservation with
   * the lease token it was made under, the token onExceeded last directed
   * under, the scope that holds its reservation, and the usage a provider
   * last reported, with its limit, when, and until when it holds.
   */
  @subTable({ key: "meter" })
  static meters = class {
    /** The meter's name. */
    meter: string;
    /**
     * What the row has used in its current period (everything without reset;
     * 0 for a rolling meter, whose buckets hold its usage).
     */
    used: Generic.Int64;
    /**
     * What the row has reserved: its own reservation and what it holds for
     * the rows inside it.
     */
    reserved: Generic.Int64;
    /** The limit setLimit gave the row; absent for none. */
    limitValue: Generic.Int64;
    /**
     * The start of the period used counts from, for a meter with a fixed
     * reset.
     */
    periodStart: Temporal.DateTime;
    /** The row's own reservation, a part of reserved. */
    reservation: Generic.Int64;
    /** The lease token the own reservation was made under. */
    token: Generic.Int64;
    /** The lease token onExceeded's directive was last sent under. */
    directed: Generic.Int64;
    /** The type of the scope that holds the reservation. */
    scopeType: string;
    /** Its key. */
    scopeKey: string;
    /**
     * The usage a provider last reported, with what the row recorded since;
     * absent before an observe.
     */
    observed: Generic.Int64;
    /** The limit the provider reported; absent for none. */
    observedLimit: Generic.Int64;
    /** When observe recorded it. */
    observedAt: Temporal.DateTime;
    /** The resetsAt observe was given; absent without one. */
    observedUntil: Temporal.DateTime;
  };

  /**
   * What the row, a scope, holds of a meter for each row inside it, keyed by
   * the meter, that row's type and its key.
   */
  @subTable({ key: "hold" })
  static holds = class {
    /** <meter>:<type>:<key> of the row it holds for. */
    hold: string;
    /** The meter. */
    meter: string;
    /** The type of the row it holds for. */
    innerType: string;
    /** Its key. */
    innerKey: string;
    /** What it holds. */
    amount: Generic.Int64;
  };

  /**
   * A rolling meter's usage, a sixtieth of its period each, keyed by the
   * meter and the bucket's start; a write deletes the ones that left the
   * window.
   */
  @subTable({ key: "bucket" })
  static buckets = class {
    /** <meter>:<start in epoch milliseconds, fifteen digits>. */
    bucket: string;
    /** The meter. */
    meter: string;
    /** When the bucket starts. */
    startsAt: Temporal.DateTime;
    /** What the row used in it. */
    used: Generic.Int64;
  };
}
