import type {
  ApprovalsGate,
  BehaviorConfigs,
  BudgetMeter,
  LeaseConfig,
  PresenceTransition,
  RetriesConfig,
  TimersDeadline,
  WorkflowOutcome,
  WorkflowTransition,
} from "@superschematic/schema";

// FK ON DELETE actions expressible on a relation. SET NULL clears the
// reference instead of deleting the row, so it needs a nullable foreign key
// column: the loader accepts it only on Nullable<Relation<T, ...>> (D83).
export type OnDeleteAction = "CASCADE" | "RESTRICT" | "NO ACTION" | "SET NULL";

// Options carried by a relation's second type argument. onDelete controls the
// generated FK's ON DELETE action; omitting it keeps the default (CASCADE).
export type RelationOptions = { onDelete?: OnDeleteAction };

// Opts is a constrained phantom: the `extends RelationOptions` bound gives the
// author typecheck and autocomplete on the option value, while the superschematic Go
// loader reads the actual value from the second type-argument node (mirroring
// Validate<T, C>). No embed is added because nothing reads a `__relationOpts`.
export type Relation<T, Opts extends RelationOptions = {}> = T & { readonly __relation: true };
// The rows of T whose relation points at this row.
export type HasMany<T> = T[] & { readonly __hasMany: true };
export type ManyToMany<T> = T[] & { readonly __manyToMany: true };
// The database generates the value on insert.
export type AutoGenerate<T> = T & { readonly __autogen: true };
// The field is stored as JSONB.
export type JsonField<T> = T & { readonly __jsonField: true };
// The column holds the value encrypted with the DB service's field keys
// (outputs.sql.encryption, D85): AES-256-GCM, bound to the table, the column
// and the row's key. The ORMs encrypt it on write and decrypt it on read, and
// a read holds it as a SecretValue. It has no filter, index or default.
export type Encrypted<T extends string> = T & { readonly __encrypted: true };

// Workflow's config on a table (D103, D104): @superschematic/schema's
// WorkflowConfig, whose transitions may name who makes them (by), with the
// permission that moves a row past by (overridePermission) and a move to the
// state the row is in answered as no change (repeat). An engine type's
// @behavior("Workflow", ...) takes the schema package's config, which has
// none of the three, and the build refuses them on it.
export interface WorkflowConfig extends Omit<BehaviorConfigs["Workflow"], "transitions"> {
  /** The moves the status may make. A state that no transition leaves is terminal. */
  readonly transitions: readonly WorkflowTransitionOn[];
  /** The permission that moves a caller past every transition's by; a transition's own permission is still needed. */
  readonly overridePermission?: string;
  /** "unchanged": a transition to the state the row is in answers { from, to, unchanged: true } and writes nothing; absent, it is the veto already_in_state. */
  readonly repeat?: "unchanged";
}

/** One move a table's Workflow may make: @superschematic/schema's WorkflowTransition, and who may make it. */
export interface WorkflowTransitionOn extends WorkflowTransition {
  /**
   * The row's own fields that hold a subject (string, Identity.UUID or
   * Identity.UserID), createdBy among them, and seats of the table's Seats,
   * each as "seat:<name>" (D104): only a caller who is the subject one of
   * the fields holds, or who is seated in one of the seats, makes the
   * transition, with its permission too when it names one. A field is only
   * as strong as its writes, and a seat as its take permission: keep the
   * field out of any input a caller who should not make the move may write,
   * and name a take permission for the seat.
   */
  readonly by?: readonly string[];
}
// A state machine on the field (D103):
// `status: Workflow<{ states: ["draft", "paid"]; transitions: [{ from: "draft"; to: "paid" }] }>`.
// The config is a type argument of literal types, or `typeof` a constant
// declared `as const`. The type stays a table: the field's column holds the
// state, and each of Workflow's other fields takes a column named for the
// field (status_entered_at). Only Workflow's operations (status.transition)
// change them; the generated repository reads them and leaves them out of its
// inputs. The field's type is the union of the config's states. A table
// carries Workflow on any number of fields, and never beside @behavior.
export type Workflow<C extends WorkflowConfig> = C["states"][number];

/**
 * Timers' config on a table (D103): @superschematic/schema's TimersConfig,
 * its field starts and its status named by the table's own fields. sweepMs
 * is the engine's: a table's deadlines fall due one by one through the
 * API's @behaviorWorker, which reads none.
 */
export interface TimersOn<T> {
  readonly deadlines: Readonly<Record<string, TimersDeadlineOn<T>>>;
  /** The permission a caller needs to reset or snooze a deadline; nobody may when absent. */
  readonly permission?: string;
  /** The field whose Workflow the deadlines read and move (enters starts, then, until); implied when the table has one Workflow, needed when it has several. */
  readonly status?: Extract<keyof T, string>;
}

/** One deadline of TimersOn<T>: a field start names a field of T. */
export interface TimersDeadlineOn<T> extends Omit<TimersDeadline, "start"> {
  readonly start: { readonly create: true } | { readonly enters: string } | { readonly field: Extract<keyof T, string> };
}

/**
 * Named deadlines on a table's rows (D103):
 * `export abstract class Ticket implements Timers<Ticket, { deadlines: { ... } }>`.
 * The type stays a table. Timers keeps its state in a sub-table of the
 * row (ticket_timers_deadlines), arms each deadline as a message of the
 * table's deadline queue (ticket_deadline_queue), and fires it when the
 * API's @behaviorWorker(Ticket) claims it. Its operations
 * (timers.fireTimer, resetTimer, snoozeTimer) change it; the generated
 * repository has no write path for its sub-table. Like Trait<T> and
 * User<Config>, Timers resolves to the empty object type, so the compiler
 * imposes no members. The first type argument is the table itself.
 */
export type Timers<T, C extends TimersOn<T>> = NonNullable<unknown>;

/**
 * Reactions' config on a table (D103): its rules, each a when and a then, the
 * fields and relations they name typed by the table. holds and revised wait
 * for Rollups, Revisions and Branches on the core.
 */
export interface ReactionsOn<T> {
  readonly rules: readonly ReactionRuleOn<T>[];
  /** The field whose Workflow the rules read and move (enters, a changes rule on it, a then without a link); implied when the table has one Workflow, needed when it has several. */
  readonly status?: Extract<keyof T, string>;
}

/** One rule of ReactionsOn<T>. */
export interface ReactionRuleOn<T> {
  readonly when:
    | { readonly enters: string }
    | { readonly changes: { readonly field: Extract<keyof T, string>; readonly to?: unknown; readonly from?: unknown } }
    | { readonly allTerminal: { readonly schema: string; readonly link: string; readonly outcomes?: readonly WorkflowOutcome[] } }
    | { readonly anyTerminal: { readonly schema: string; readonly link: string; readonly outcomes: readonly WorkflowOutcome[] } };
  readonly then: { readonly transition: string; readonly link?: Extract<keyof T, string> };
}

/**
 * Rules that move Workflow statuses after a write commits (D103):
 * `export abstract class Project implements Reactions<Project, { rules: [...] }>`.
 * The type stays a table. A rule hears every write of the table's rows, and
 * of the rows of another table whose relation names this one (allTerminal,
 * anyTerminal: schema the table, link its relation field), whoever makes it,
 * through the table's change queue, which the API's @behaviorWorker runs.
 * Reactions has no operations. Like Timers, it resolves to the empty object
 * type; the first type argument is the table itself.
 */
export type Reactions<T, C extends ReactionsOn<T>> = NonNullable<unknown>;

/**
 * Constants' config on a table (D103, D104): @superschematic/schema's
 * ConstantsConfig, its fields named by the table's own, and fields that move
 * one way: up, a boolean that only turns true or a number or a date-time that
 * only grows; down, the other way. An engine type's @behavior("Constants", ...)
 * takes the schema package's config, which has no oneWay, and the build
 * refuses it there.
 */
export interface ConstantsOn<T> {
  /** The table's own fields that keep the value their create gives them. */
  readonly fields: readonly Extract<keyof T, string>[];
  /** The permission a caller needs to change them after the create, or move a oneWay field back; nobody may when absent. */
  readonly permission?: string;
  /** Fields that move one way after the create, unless the caller holds permission; clearing one is a move down. */
  readonly oneWay?: { readonly [F in Extract<keyof T, string>]?: "up" | "down" };
}

/**
 * Fields a table's create sets for good, and fields that move one way (D103,
 * D104): `export abstract class Task implements Constants<Task, { fields: ["owner"] }>`.
 * The type stays a table. An update through the core that changes one is
 * refused; a raw SQL write is not. Constants has no operations and stores
 * nothing. Like Timers, it resolves to the empty object type; the first type
 * argument is the table itself.
 */
export type Constants<T, C extends ConstantsOn<T>> = NonNullable<unknown>;

/** StateRules' config on a table (D103, D104): its fields named by the table's own, who may change a frozen field (by), and the Workflow it reads (status). */
export interface StateRulesOn<T> {
  readonly states: Readonly<Record<string, StateRuleOn<T>>>;
  /** The permission a caller needs to change a field a state freezes; nobody may when absent. What a state requires holds for every caller. */
  readonly permission?: string;
  /** The field whose Workflow the states are of; implied when the table has one Workflow, needed when it has several. */
  readonly status?: Extract<keyof T, string>;
}

/** One state's rules under StateRulesOn<T>. */
export interface StateRuleOn<T> {
  readonly required?: readonly Extract<keyof T, string>[];
  readonly frozen?: readonly Extract<keyof T, string>[] | "all";
  /**
   * The row's own fields that hold a subject (string, Identity.UUID or
   * Identity.UserID), createdBy among them, and seats of the table's Seats,
   * each as "seat:<name>" (D104): the subject one of the fields holds, or a
   * subject seated in one of the seats, changes a field the state freezes. A
   * field is only as strong as its writes, and a seat as its take
   * permission: keep the field out of any input a caller who should not
   * change what it frees may write, or list it in Constants' fields, and
   * name a take permission for the seat.
   */
  readonly by?: readonly (Extract<keyof T, string> | `seat:${string}`)[];
}

/**
 * Rules per state of a table's Workflow (D103, D104):
 * `export abstract class Task implements StateRules<Task, { states: { ... } }>`.
 * The type stays a table. StateRules has no operations and stores nothing;
 * it resolves to the empty object type.
 */
export type StateRules<T, C extends StateRulesOn<T>> = NonNullable<unknown>;

/** Approvals' config on a table (D103, D104): its gates' resetOn and pinned, and the Workflow it reads (status). */
export interface ApprovalsOn<T> {
  readonly gates: Readonly<Record<string, ApprovalsGateOn<T>>>;
  /** Whether an update consumes every gate's sign-offs that names no resetOn; true when absent. */
  readonly resetOnChange?: boolean;
  /** The field whose Workflow the gates are of; implied when the table has one Workflow, needed when it has several. */
  readonly status?: Extract<keyof T, string>;
}

/** One gate of ApprovalsOn<T>: @superschematic/schema's ApprovalsGate, the fields whose change consumes its sign-offs, and whether a sign-off names the version its signer read. */
export interface ApprovalsGateOn<T> extends ApprovalsGate {
  /** The own fields whose change, by an update, consumes the gate's sign-offs; [] for none; absent, resetOnChange decides. */
  readonly resetOn?: readonly Extract<keyof T, string>[];
  /** signOff names the row's version its signer read, and a stale one is vetoed. */
  readonly pinned?: boolean;
}

/**
 * Sign-offs that gate the states of a table's Workflow (D103, D104):
 * `export abstract class Task implements Approvals<Task, { gates: { ... } }>`.
 * The type stays a table. Approvals keeps its sign-offs in a sub-table of the
 * row (task_approvals_sign_offs); its operations (approvals.signOff,
 * withdrawSignOff, listSignOffs) change and read them. A gate that excludes
 * the creator reads the table's createdBy. Like Timers, it resolves to the
 * empty object type; the first type argument is the table itself.
 */
export type Approvals<T, C extends ApprovalsOn<T>> = NonNullable<unknown>;

/**
 * Lease's config on a table (D103, D104): @superschematic/schema's
 * LeaseConfig, its maxHoldField named by the table's own fields, and the
 * Workflow its moves read (status). sweepMs is the engine's runner's, and a
 * table's lease expires at its own deadline, which the API's @behaviorWorker
 * runs; directOn waits for Revisions and Branches on the core: a table takes
 * neither. An engine type's @behavior("Lease", ...) takes the schema
 * package's config, which has no status, and the build refuses it there.
 */
export interface LeaseOn<T> extends Omit<LeaseConfig, "maxHoldField" | "sweepMs" | "directOn"> {
  /** A Generic.Int64 field of the table whose positive value overrides maxHoldMs for the row. */
  readonly maxHoldField?: Extract<keyof T, string>;
  /** The field whose Workflow onExpiry and escalate move, and in whose terminal states an expiry does not count; implied when the table has one Workflow, needed when it has several. */
  readonly status?: Extract<keyof T, string>;
  /** The engine's runner's: a table's lease expires at its own deadline, so a table's Lease takes none. */
  readonly sweepMs?: never;
  /** Waits for Revisions and Branches on the core (D103, step 4), so a table's Lease takes none. */
  readonly directOn?: never;
}

/**
 * An exclusive, time-bounded lease on a table's rows, numbered by a fencing
 * token (D16, D104): `export abstract class Job implements Lease<Job, { ... }>`.
 * The type stays a table. Its holder, token, times, expiries and how the last
 * lease ended are columns of the row (lease_holder, ...), its directives a
 * sub-table (job_lease_directives), and its expiry a message of the table's
 * deadline queue (job_deadline_queue), which the API's @behaviorWorker(Job)
 * runs. A write presents the token as Lease's precondition
 * ({ preconditions: { Lease: { token } } }). Its operations (lease.acquire,
 * heartbeat, release, expire, direct, acknowledge, resetExpiries) change it.
 * Like Timers, it resolves to the empty object type; the first type argument
 * is the table itself.
 */
export type Lease<T, C extends LeaseOn<T>> = NonNullable<unknown>;

/** A backoff of a Retries class on a table (D104). */
export interface RetryBackoffOn {
  /** The wait after the class's first failure since the row's last success, in milliseconds. */
  readonly firstMs: number;
  /** The longest wait, in milliseconds, at least firstMs. */
  readonly maxMs: number;
  /** How many times longer each next wait is; 2 when absent. */
  readonly factor?: number;
}

/** A capped failure class of RetriesOn<T>: its cap, its hint, and how long a failure of it waits (D104). */
export interface RetryClassOn {
  readonly attempts: number;
  readonly hint?: string;
  readonly backoff?: RetryBackoffOn;
}

/**
 * Retries' config on a table (D103, D104): @superschematic/schema's
 * RetriesConfig, its fields named by the table's own, with the waits a table
 * keeps: a class's backoff, the states an attempt starts in (retryInto), the
 * move its end makes (then), a time to give up after (giveUpAfterMs), a cap of
 * attempts in a window, and a field whose value keys the counts (keyField).
 * resultField writes through an update an operation makes, which waits for
 * the core to run one, so a table takes none. An engine type's
 * @behavior("Retries", ...) takes the schema package's config, and the build
 * refuses each of these there.
 */
export interface RetriesOn<T> extends Omit<RetriesConfig, "classes" | "limitsField" | "resultField"> {
  readonly classes: { readonly [failure: string]: RetryClassOn | "terminal" };
  /** A Generic.JSON field of the table with the row's own caps. */
  readonly limitsField?: Extract<keyof T, string>;
  /** The field whose Workflow exhaustedState, from, retryInto and then name; implied when the table has one Workflow, needed when it has several. */
  readonly status?: Extract<keyof T, string>;
  /** The states an attempt starts in: a move into one waits for the row's retryAt and the window. */
  readonly retryInto?: readonly string[];
  /** The move a row's backoff makes when it ends, from one of from. */
  readonly then?: { readonly transition: string; readonly from: readonly string[] };
  /** Exhausts the row at its first failure this long after the first failure since its last success, in milliseconds. */
  readonly giveUpAfterMs?: number;
  /** At most attempts recorded in any ms milliseconds before the next start waits. */
  readonly window?: { readonly attempts: number; readonly ms: number };
  /** An own field whose value keys every class's count: a new value starts them again. */
  readonly keyField?: Extract<keyof T, string>;
  /** Writes through an update an operation makes, which waits for the core to run one (D103, step 4), so a table's Retries takes none. */
  readonly resultField?: never;
}

/**
 * Retries per failure class on a table's rows, with waits between attempts
 * (D16, D104): `export abstract class Job implements Retries<Job, { ... }>`.
 * The type stays a table. Its counts, exhaustion and waits are columns of the
 * row (retries_total, ...), its attempts a sub-table (job_retries_attempts),
 * and then's move a message of the table's deadline queue. Its operation
 * (retries.recordAttempt) changes them. Like Timers, it resolves to the empty
 * object type; the first type argument is the table itself.
 */
export type Retries<T, C extends RetriesOn<T>> = NonNullable<unknown>;

/**
 * Presence's config on a table (D103, D104): @superschematic/schema's
 * PresenceConfig, its principalField named by the table's own fields, how long
 * a row stays present in each state of its Workflow (ttlByState) and the
 * Workflow it reads (status). sweepMs is the engine's runner's: a table's
 * misses fall due one by one through the API's @behaviorWorker, and the build
 * refuses it.
 */
export interface PresenceOn<T> {
  /** How long a row stays present after its create or its last beat, in milliseconds, at least 1000; with ttlByState, in each state it does not list. */
  readonly ttlMs: number;
  /** The field that holds the subject of the principal the row stands for (string, Identity.UUID or Identity.UserID), the only principal that may beat it; it does not change once set. */
  readonly principalField: Extract<keyof T, string>;
  /** Moves the row's Workflow status, through transition, when it is missed while the status is one of from. */
  readonly onMissed?: PresenceTransition;
  /** Moves the row's Workflow status, through transition, at a beat while the status is one of from. */
  readonly onBeat?: PresenceTransition;
  /** Tables of the database that implement Lease: a miss expires the leases the principal holds on them through Lease's expire, but for one renewed since its last beat. The table's @behaviorWorker needs each one's Lease overridePermission. */
  readonly releaseLeases?: readonly string[];
  /** How long a row stays present in a state of its Workflow, by state, in milliseconds, at least 1000: counted from the later of the last beat and the entry into the state. */
  readonly ttlByState?: { readonly [state: string]: number };
  /** The field whose Workflow onMissed, onBeat and ttlByState read and move; implied when the table has one Workflow, needed when it has several. */
  readonly status?: Extract<keyof T, string>;
  /** The engine's runner's: a table's row is missed at its own deadline, so a table's Presence takes none. */
  readonly sweepMs?: never;
}

/**
 * A heartbeat on a table's rows (D103, D104):
 * `export abstract class Session implements Presence<Session, { ttlMs: 180000, principalField: "device" }>`.
 * The type stays a table. Presence keeps its state in owned columns
 * (presence_deadline, presence_last_beat_at, presence_missed,
 * presence_released) and arms each row's miss as a message of the table's
 * deadline queue, which the API's @behaviorWorker(Session) runs as it falls
 * due. Its operations (presence.beat, miss) change it. Like Timers, it
 * resolves to the empty object type; the first type argument is the table
 * itself.
 */
export type Presence<T, C extends PresenceOn<T>> = NonNullable<unknown>;

/** Budget's config on a table (D103, D104): its meters' fields and scopes named by the table's own, their periods, the permission observe needs, and the Workflow escalate moves (status). */
export interface BudgetOn<T> {
  readonly meters: { readonly [meter: string]: BudgetMeterOn<T> };
  /** The permission a caller needs to change a meter's limit, with setLimit or through limitField. */
  readonly limitPermission?: string;
  /** The permission a caller needs to record a provider's usage with observe; observe is refused when absent. */
  readonly observePermission?: string;
  /** A directive sent to the holder of the row's active lease when usage takes it or an enclosing scope over its limit; needs Lease. */
  readonly onExceeded?: { readonly direct: string };
  /** Moves the row's Workflow status, through transition, when usage leaves it over a limit while the status is one of from. */
  readonly escalate?: { readonly transition: string; readonly from: readonly string[] };
  /** The field whose Workflow escalate moves; implied when the table has one Workflow, needed when it has several. */
  readonly status?: Extract<keyof T, string>;
}

/** One meter of BudgetOn<T>: @superschematic/schema's BudgetMeter, its fields and its scope named by the table's own, and its period. */
export interface BudgetMeterOn<T> extends Omit<BudgetMeter, "limitField" | "reserveField" | "scope" | "reset"> {
  /** An integer field of the table that holds the row's limit; limit is the limit while it holds no value of at least 0. */
  readonly limitField?: Extract<keyof T, string>;
  /** An integer field of the table whose positive value is the amount reserve takes, in place of reserve. */
  readonly reserveField?: Extract<keyof T, string>;
  /** A to-one relation of the table whose target, which implements Budget with the meter, is the enclosing scope. */
  readonly scope?: Extract<keyof T, string>;
  /** When used starts again at 0, in UTC: each hour, day, Monday, first of the month, or multiple of everyMs (whole minutes) from the epoch. */
  readonly reset?: "hourly" | "daily" | "weekly" | "monthly" | { readonly everyMs: number };
  /** rolling: the meter counts its last period, from buckets of a sixtieth of it; needs reset. */
  readonly window?: "rolling";
}

/**
 * Reserve-then-settle budgets on a table's rows (D103, D104):
 * `export abstract class Account implements Budget<Account, { meters: { ... } }>`.
 * The type stays a table. Budget keeps its meters in sub-tables of the row
 * (account_budget_meters, account_budget_holds, account_budget_buckets); its
 * operations (budget.reserve, checkReserve, recordUsage, settle, setLimit,
 * observe and the scope operations) change and read them. A meter's scope is
 * a to-one relation. Like Timers, it resolves to the empty object type; the
 * first type argument is the table itself.
 */
export type Budget<T, C extends BudgetOn<T>> = NonNullable<unknown>;

/** One seat of SeatsOn<T> (D104): how many it seats, what a take of a full seat does, the permissions that take and assign it, and the field whose subject it always seats. */
export interface SeatsSeatOn<T> {
  /** How many subjects the seat holds at most, the subject its fromField holds among them; no cap when absent. */
  readonly max?: number;
  /** What a take of a full seat does: "refuse", the veto seat_full (the default), or "displace", which unseats the subject seated longest. Needs max. */
  readonly onFull?: "refuse" | "displace";
  /** The permission a caller needs to seat itself; any caller may when absent. */
  readonly take?: string;
  /** The permission a caller needs to seat or unseat another subject; no caller may when absent. */
  readonly assign?: string;
  /**
   * An own field that holds a subject (string, Identity.UUID or
   * Identity.UserID): the subject it holds is seated, counts toward max, is
   * never displaced and cannot leave while the field holds them. An update
   * that gives it a subject not seated takes the seat past no permission,
   * held to max: seat_full under "refuse", and under "displace" the subject
   * seated longest leaves. A field is only as strong as its writes: keep it
   * out of any input a caller who should not sit in the seat may write, or
   * list it in Constants' fields.
   */
  readonly fromField?: Extract<keyof T, string>;
}

/** Seats' config on a table (D104): its seats, by name. */
export interface SeatsOn<T> {
  readonly seats: Readonly<Record<string, SeatsSeatOn<T>>>;
}

/**
 * Named seats on a table's rows, each holding the subjects seated in it
 * (D104): `export abstract class Problem implements Seats<Problem, { seats: { ... } }>`.
 * The type stays a table. Seats keeps its occupants in a sub-table of the
 * row (problem_seats_occupants); its operations (seats.takeSeat, leaveSeat,
 * listSeats) change and read them, and a Workflow transition's or a
 * StateRules state's by names a seat as "seat:<name>". The behaviors' core
 * alone implements it: no engine type composes it. Like Timers, it resolves
 * to the empty object type; the first type argument is the table itself.
 */
export type Seats<T, C extends SeatsOn<T>> = NonNullable<unknown>;

/** Reconcile's config on a field (D104). */
export interface ReconcileConfig {
  /** The field whose Workflow an observation moves, along the fewest transitions it allows. */
  readonly status: string;
  /** The state of that Workflow the stale deadline moves a row to: every state but a terminal one reaches it, and it reaches every state. Needs staleAfterMs. */
  readonly unreachable?: string;
  /** How long after an observation's at a row goes stale, in milliseconds, at least 1000. Needs unreachable. */
  readonly staleAfterMs?: number;
  /** The permission a caller needs to observe. */
  readonly observePermission: string;
}

/**
 * The state a row's provider last reported, beside the Workflow field it
 * moves (D104):
 * `observed: Reconcile<{ status: "status"; unreachable: "unreachable"; staleAfterMs: 300000; observePermission: "sandboxes.observe" }>`.
 * The type stays a table: the field's column holds the observed state,
 * null before the first observation, and Reconcile's other fields take
 * columns named for the field (observed_observed_at, observed_drift,
 * observed_reconciled_at). Only its operation (observed.observe) and its
 * stale deadline, which the API's @behaviorWorker runs, change them. A
 * repository's row types the field string | null, as it types the
 * Workflow's status string; observe's input and result, in the API and the
 * SDKs, type it by the Workflow's states (<Table>ObservedState). The
 * behaviors' core alone implements it: no engine type composes it.
 */
export type Reconcile<C extends ReconcileConfig> = string | null;

/** A kind of schedule (D104): after, one run a delay after it is set; every, a run at each whole interval from when it is set; cron, a run at each time a cron line names. */
export type ScheduleKind = "after" | "every" | "cron";

/** Schedules' config on a table (D104): how many schedules a row holds live, the bounds of a delay, an interval and a cron line's runs, the kinds a schedule may be, the zone a cron line is read in, the outcomes a run reports, the outcome that tries a run again, the states that end every schedule, and the permission its operations need. */
export interface SchedulesOn<T> {
  /** How many schedules a row holds live, active or paused, at once: 1 to 100. */
  readonly max: number;
  /** The least delay or interval, and the least gap between a cron line's times of day, in milliseconds, at least 1000; a minute when absent. */
  readonly minMs?: number;
  /** The greatest delay or interval, and how far off a cron line's next run may be when it is set, in milliseconds; thirty days when absent. */
  readonly maxMs?: number;
  /** The kinds a schedule may be. */
  readonly kinds: readonly ScheduleKind[];
  /** The IANA time zone a cron line is read in when setSchedule names none; UTC when absent. Needs cron among kinds. */
  readonly timeZone?: string;
  /** The outcomes reportRun records of a run. */
  readonly outcomes: readonly string[];
  /** The outcome that tries a run again, afterMs after its report, unless the next regular run comes first. */
  readonly retry?: { readonly outcome: string; readonly afterMs: number };
  /** The states of the table's Workflow whose entry finishes every live schedule of the row. */
  readonly endOn?: { readonly enters: readonly string[] };
  /** The field whose Workflow endOn reads; implied when the table has one Workflow, needed when it has several. */
  readonly status?: Extract<keyof T, string>;
  /** The permission every operation of Schedules needs; any caller the route admits when absent. */
  readonly permission?: string;
}

/**
 * Named schedules on a table's rows (D104):
 * `export abstract class Problem implements Schedules<Problem, { max: 10, kinds: [...], outcomes: [...] }>`.
 * The type stays a table. Schedules keeps a row per schedule in a sub-table
 * (problem_schedules_schedules), arms each live one's next run as a message
 * of the table's deadline queue and writes each run's event to its event
 * queue, which the API's @behaviorWorker(Problem) runs, handing each event to
 * the project's Workers method; its operations (schedules.setSchedule,
 * pauseSchedule, resumeSchedule, cancelSchedule, reportRun, listSchedules)
 * change and read them. The behaviors' core alone implements it: no engine
 * type composes it. Like Timers, it resolves to the empty object type; the
 * first type argument is the table itself.
 */
export type Schedules<T, C extends SchedulesOn<T>> = NonNullable<unknown>;
