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
// A field of the table T that a behavior's config reads a value of (D103,
// D104): one of T's own, a to-one relation named by the field that holds its
// key, its name and Id (authorityId for authority: Relation<Authority>), as
// the loader asks there, since a config reads the key, not the row it points
// at. Any name, for a T that is any.
export type FieldOf<T> = 0 extends 1 & T ? string : Exclude<Extract<keyof T, string>, RelationNameOf<T>> | RelationKeyOf<T>;
// The names of T's to-one relations: authority for authority:
// Relation<Authority> or Nullable<Relation<Authority>>.
export type RelationNameOf<T> = {
  [K in Extract<keyof T, string>]-?: NonNullable<T[K]> extends { readonly __relation: true } ? K : never;
}[Extract<keyof T, string>];
// The fields that hold the keys of T's to-one relations: authorityId for
// authority.
export type RelationKeyOf<T> = `${RelationNameOf<T>}Id`;
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
// permission that moves a row past by (overridePermission), a move to the
// state the row is in answered as no change (repeat), and the states a
// transition leaves that still count as finished (finished, D113). An
// engine type's @behavior("Workflow", ...) takes the schema package's
// config, which has none of the four, and the build refuses them on it.
export interface WorkflowConfig extends Omit<BehaviorConfigs["Workflow"], "transitions"> {
  /** The moves the status may make. A state that no transition leaves is terminal. */
  readonly transitions: readonly WorkflowTransitionOn[];
  /** The permission that moves a caller past every transition's by; a transition's own permission is still needed. */
  readonly overridePermission?: string;
  /** "unchanged": a transition to the state the row is in answers { from, to, unchanged: true } and writes nothing; absent, it is the veto already_in_state. */
  readonly repeat?: "unchanged";
  /**
   * The outcome of each state it names that a transition leaves (D113):
   * such a state counts as finished for the rules that read an outcome,
   * Reactions' allTerminal and anyTerminal, as a terminal state does,
   * though a row may leave it. `finished: { done: "success" }` beside
   * `{ from: "done", to: "todo" }` lets a task be reopened and still close
   * its project. A terminal state's outcome is outcomes' to name.
   */
  readonly finished?: Readonly<Record<string, WorkflowOutcome>>;
}

/**
 * One move a table's Workflow may make, or several written as one (D109):
 * @superschematic/schema's WorkflowTransition, whose from and to may each
 * list states, and who may make it.
 */
export interface WorkflowTransitionOn extends Omit<WorkflowTransition, "from" | "to"> {
  /**
   * The state the row is in, or a list of them: a transition from each.
   * `{ from: ["backlog", "todo"], to: "started" }` is two transitions, each
   * with this one's permission and by.
   */
  readonly from: string | readonly string[];
  /**
   * The state the row moves to, or a list of them: a transition to each.
   * A state both lists hold moves to the others and not to itself, so the
   * same list in from and to is every move among its states.
   */
  readonly to: string | readonly string[];
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
export type ConstantsOn<T> = ConstantsRules<T> & ({ readonly fields: readonly FieldOf<T>[] } | { readonly oneWay: ConstantsRules<T>["oneWay"] & {} });

/** The members of ConstantsOn<T>, which names fields, oneWay or both. */
export interface ConstantsRules<T> {
  /** The table's own fields that keep the value their create gives them, one at least; left out beside oneWay, for a table whose one rule is a field that moves one way. */
  readonly fields?: readonly FieldOf<T>[];
  /** The permission a caller needs to change them after the create, or move a oneWay field back; nobody may when absent. */
  readonly permission?: string;
  /** Fields that move one way after the create, unless the caller holds permission; clearing one is a move down. */
  readonly oneWay?: { readonly [F in FieldOf<T>]?: "up" | "down" };
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
  readonly required?: readonly FieldOf<T>[];
  readonly frozen?: readonly FieldOf<T>[] | "all";
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
  readonly by?: readonly (FieldOf<T> | `seat:${string}`)[];
}

/**
 * Rules per state of a table's Workflow (D103, D104):
 * `export abstract class Task implements StateRules<Task, { states: { ... } }>`.
 * The type stays a table. StateRules has no operations and stores nothing;
 * it resolves to the empty object type.
 */
export type StateRules<T, C extends StateRulesOn<T>> = NonNullable<unknown>;

/** Approvals' config on a table (D103, D104, D200): its gates, each a requirement or the rows it reads its requirements from, and the Workflow it reads (status). */
export interface ApprovalsOn<T> {
  readonly gates: Readonly<Record<string, ApprovalsGateOn<T>>>;
  /** Whether an update consumes every gate's sign-offs that names no resetOn; true when absent. */
  readonly resetOnChange?: boolean;
  /** The field whose Workflow the gates are of; implied when the table has one Workflow, needed when it has several. */
  readonly status?: Extract<keyof T, string>;
}

/** Which callers a requirement counts, and as whom (D200): a person calling as themselves; an agent run acting for a person, counted as that person; an agent run, counted as its Agent. */
export type ApprovalsCaller = "person" | "acting-for-person" | "agent";

/** What each sign-off of a requirement shows, from the assurance its host reports (D200). */
export type ApprovalsProof = "none" | "recent-sign-in" | "user-verification";

/** One condition on a table's own field under which a requirement applies (D200). */
export interface ApprovalsConditionOn<T> {
  readonly field: Extract<keyof T, string>;
  /** every-matches and any-matches test glob patterns, contains the values, at-most the one number. */
  readonly test: "every-matches" | "any-matches" | "contains" | "at-most";
  readonly values: readonly string[];
  /** Whether the condition holds when its test fails. */
  readonly negate?: boolean;
}

/** What every gate of ApprovalsOn<T> takes: the states it gates from, the fields whose change consumes its sign-offs, and what a sign-off pins. */
export interface ApprovalsGateBaseOn<T> {
  /** The states of the Workflow a transition from is gated; every state when absent. */
  readonly from?: readonly string[];
  /** The own fields whose change, by an update, consumes the gate's sign-offs; [] for none; absent, resetOnChange decides. */
  readonly resetOn?: readonly FieldOf<T>[];
  /** signOff names the row's version its signer read, and a stale one is vetoed. */
  readonly pinned?: boolean;
  /** signOff names in read the value of each of these own fields as its signer read it, and a stale one is vetoed (D200). */
  readonly pinOn?: readonly Extract<keyof T, string>[];
}

/** A gate whose one requirement the config writes: @superschematic/schema's ApprovalsGate and D200's requirement members. */
export interface ApprovalsRequirementGateOn<T> extends ApprovalsGateBaseOn<T>, ApprovalsGate {
  /** The callers it counts; every caller, as its subject, when absent. */
  readonly callers?: readonly ApprovalsCaller[];
  /** none when absent. */
  readonly proof?: ApprovalsProof;
  /** For recent-sign-in, the most milliseconds a sign-in may come before the sign-off. */
  readonly proofWithinMs?: number;
  /** Whether the person the creator acted for, the table's createdFor, is refused a sign-off too; false when absent. */
  readonly excludeCreatorPerson?: boolean;
  /** Own fields that list subjects, persons and Agents who may not sign. */
  readonly excludeSubjectsIn?: readonly Extract<keyof T, string>[];
  /** Whether each guard reads each signer's permission again; false when absent. */
  readonly recheckPermission?: boolean;
  /** The conditions under which it applies, all of them; it always applies when absent. */
  readonly when?: readonly ApprovalsConditionOn<T>[];
  readonly requirementsFrom?: never;
}

/** Where a gate reads its requirements from (D200): the rows of schema whose to-one relation link points at the row, or with in, at one of the keys the row's own list field holds. */
export interface ApprovalsRequirementsFromOn<T> {
  readonly schema: string;
  readonly link: string;
  readonly in?: Extract<keyof T, string>;
}

/** A gate that reads its requirements from rows each instance writes (D200). It names its own resetOn, and no requirement member: each row holds its own. */
export interface ApprovalsRowsGateOn<T> extends ApprovalsGateBaseOn<T> {
  readonly requirementsFrom: ApprovalsRequirementsFromOn<T>;
  readonly resetOn: readonly Extract<keyof T, string>[];
  readonly count?: never;
  readonly permission?: never;
  readonly excludeCreator?: never;
  readonly callers?: never;
  readonly proof?: never;
  readonly proofWithinMs?: never;
  readonly excludeCreatorPerson?: never;
  readonly excludeSubjectsIn?: never;
  readonly recheckPermission?: never;
  readonly when?: never;
}

/** One gate of ApprovalsOn<T>: a requirement the config writes, or the rows it reads its requirements from (D200). */
export type ApprovalsGateOn<T> = ApprovalsRequirementGateOn<T> | ApprovalsRowsGateOn<T>;

/**
 * Sign-offs that gate the states of a table's Workflow (D103, D104, D200):
 * `export abstract class Task implements Approvals<Task, { gates: { ... } }>`.
 * The type stays a table. Approvals keeps its sign-offs in a sub-table of the
 * row (task_approvals_sign_offs); its operations (approvals.signOff,
 * withdrawSignOff, listSignOffs) change and read them. A gate that excludes
 * the creator reads the table's createdBy. A gate may read its requirements
 * from the rows of another table that each instance writes
 * (requirementsFrom), and a requirement may name which callers it counts,
 * the proof each sign-off shows, whom it excludes and when it applies. Like
 * Timers, it resolves to the empty object type; the first type argument is
 * the table itself.
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
  readonly maxHoldField?: FieldOf<T>;
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
  readonly limitsField?: FieldOf<T>;
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
  readonly keyField?: FieldOf<T>;
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
  readonly principalField: FieldOf<T>;
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
  readonly limitField?: FieldOf<T>;
  /** An integer field of the table whose positive value is the amount reserve takes, in place of reserve. */
  readonly reserveField?: FieldOf<T>;
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
  readonly fromField?: FieldOf<T>;
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

/** Numbering's config on a field (D56, D103's step 4). */
export interface NumberingConfig {
  /** How a number prints: text with {seq}, or {seq:N} zero-padded to N digits (1 to 10), exactly once, and {yyyy} or {yy}, the UTC year the row was created in, as often as wanted. No other {...} is allowed. */
  readonly format: string;
  /** Counts per target of a required to-one relation of the table, named by the relation's field: each target's rows are numbered 1, 2, 3, ... of their own. */
  readonly scope?: { readonly link: string };
  /** yearly starts the count again at 1 each UTC year, the year the row was created in; the format must then print the year. */
  readonly reset?: "yearly";
}

/**
 * A number a person reads on each row, set when the row is created from a
 * count per table, per target of a relation, or per year (D56, D103's step
 * 4): `number: Numbering<{ format: "INV-{yyyy}-{seq:4}"; scope: { link: "customer" }; reset: "yearly" }>`,
 * INV-2026-0042. The type stays a table: the field's column holds the
 * label, null on a row made before the table composed Numbering until
 * assignNumber numbers it, and the number in its count takes the column
 * named for the field (number_seq). Its counts are a table of the use's
 * own, kept for the table as a whole (invoice_number_counts), which a
 * delete leaves, so a delete leaves a gap and a refused create takes no
 * number. Nothing changes a number once set: only its operation
 * (number.assignNumber) writes it, and only on a row with none. A
 * repository's row types the field string | null.
 */
export type Numbering<C extends NumberingConfig> = string | null;

/** Revisions' config on a table (D16, D103's step 4): with review, a caller proposes a change of the own fields and a caller who holds the permission approves or rejects it. */
export interface RevisionsOn {
  /** Turns on the review step: propose, approve, reject and listProposals. */
  readonly review?: { readonly permission: string };
}

/**
 * The history of a table's rows' own fields, with an optional review step
 * (D16, D103's step 4):
 * `export abstract class Policy implements Revisions<Policy, { review: { permission: "policies.review" } }>`.
 * The type stays a table. A create keeps revision 1 of the row's own
 * fields, and every change of them the next, in a sub-table of the use
 * (policy_revisions_revisions), and the owned column revisions_revision
 * holds the latest number. With review, propose keeps a merge patch of the
 * own fields as a pending proposal (policy_revisions_proposals), and
 * approve applies it through every rule an update meets and records the
 * revision it makes; reject settles it. Its operations (revisions.
 * listRevisions, getRevision, propose, approve, reject, listProposals)
 * read and change them. A delete of the row deletes both. Like Timers, it
 * resolves to the empty object type; the first type argument is the table
 * itself.
 */
export type Revisions<T, C extends RevisionsOn> = NonNullable<unknown>;

/** A kind of block a table's Comments takes on a message (D77, D103's step 4): ref, another row; action, a card for what the poster did; code, a place in code at a revision; attachment, a file the deployment keeps (D154). */
export type CommentsBlockKind = "ref" | "action" | "code" | "attachment";

/** What an attachment block may describe (D154): the URI schemes its uri may use, the media types it may have, and the largest file. */
export interface CommentsAttachmentsOn {
  /** The largest file an attachment block may describe, in bytes; no limit when absent. */
  readonly maxBytes?: number;
  /** The media types an attachment may have, each type/subtype or type/* in lower case; any when absent. */
  readonly mediaTypes?: readonly string[];
  /** The URI schemes an attachment's uri may use, in lower case: attachment, for a file of the row's Attachments (attachment:<id>); https alone when absent. */
  readonly schemes?: readonly string[];
}

/** Comments' config on a table (D77, D103's step 4): the longest body, how long an author may edit, the block kinds a message may hold, what an attachment block may describe, and the permissions that moderate, pin and resolve. Every member is optional. Streamed replies, read state and deliveries, and code anchors wait for the core, so their members are not a table's. */
export interface CommentsOn {
  /** The longest body a post or an edit gives, in code points: 1 to 40000, 40000 when absent. */
  readonly maxBodyLength?: number;
  /** How long after a message's postedAt its author may edit it, in milliseconds, at least 1000; no limit when absent. A caller holding moderatePermission is not held to it. */
  readonly editWindowMs?: number;
  /** The block kinds a post or an edit may give, each once; every kind when absent, none when empty. */
  readonly blocks?: readonly CommentsBlockKind[];
  /** What an attachment block may describe (D154): its uri's scheme, its media type and its size. */
  readonly attachments?: CommentsAttachmentsOn;
  /** The permission that lets a caller edit and delete another's message, edit past editWindowMs, and purge. Absent, only a message's author edits or deletes it, and nobody purges. */
  readonly moderatePermission?: string;
  /** The permission a caller needs to pin and unpin. Absent, any caller the route lets call them may. */
  readonly pinPermission?: string;
  /** The permission that lets a caller resolve and reopen another's thread. Absent, only a thread's author does. */
  readonly resolvePermission?: string;
}

/**
 * A conversation on each of a table's rows (D77, D103's step 4):
 * `export abstract class Ticket implements Comments<Ticket, { moderatePermission: "tickets.moderate" }>`.
 * The type stays a table. Its messages, each top-level or a reply in the
 * thread of a top-level one, are rows of a sub-table of the use
 * (ticket_comments_messages), what each edit replaced another
 * (ticket_comments_edits), and the reactions a third
 * (ticket_comments_reactions); each conversation's high-water marks are a
 * row of a table of the use's own, kept for the table as a whole
 * (ticket_comments_conversations), which a delete of the row leaves, so a
 * row re-created with the key never reuses a message id. Every write is
 * beside the row: it moves no version of the row and asks no other
 * behavior's guard, so a lease another principal holds stops no reply. Its
 * operations (comments.post, edit, deleteMessage, purge, react, unreact,
 * pin, unpin, resolve, reopen, listMessages, getMessage, listEdits,
 * listReactions, listPinned) change and read them. Like Timers, it
 * resolves to the empty object type; the first type argument is the table
 * itself.
 */
export type Comments<T, C extends CommentsOn> = NonNullable<unknown>;

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

/** A recurrence's anchor (D152): calendar, an RFC 5545 RRULE from a start; afterDone, an interval after the last completion; relative, an offset from a date a relation reaches. */
export type RecurrenceAnchor = "calendar" | "afterDone" | "relative";

/** A day of the week, as Recurrence's weekendDays names it (D152). */
export type RecurrenceWeekday = "monday" | "tuesday" | "wednesday" | "thursday" | "friday" | "saturday" | "sunday";

/** Recurrence's config on a table (D152): the anchors a row's recurrence may take, the zone it is read in, how far ahead its occurrences are kept and how long finished ones stay, how a due date on a weekend day or a holiday moves, the holiday table and the field naming a row's calendar, the relation a relative recurrence reads its date through, the lead times, and the permissions its operations need. */
export interface RecurrenceOn<T> {
  /** The anchors a row's recurrence may take. */
  readonly anchors: readonly RecurrenceAnchor[];
  /** The IANA time zone a recurrence is read in when setRecurrence names none; UTC when absent. */
  readonly timeZone?: string;
  /** How many days ahead the occurrences are kept materialised, 1 to 1100; 400 when absent. */
  readonly horizonDays?: number;
  /** How many days a finished occurrence is kept after its due; 1100 when absent. */
  readonly historyDays?: number;
  /** How a due date on a weekend day or a holiday moves when setRecurrence names none; none when absent. */
  readonly adjust?: "none" | "before" | "after";
  /** The days a due date moves off; saturday and sunday when absent. */
  readonly weekendDays?: readonly RecurrenceWeekday[];
  /** The holiday table (rows the app fills, each a date of a calendar) and this table's field that names a row's calendar. */
  readonly holidays?: { readonly table: string; readonly calendarField: string; readonly dateField: string; readonly field: Extract<keyof T, string> };
  /** The relative anchor's date: this table's to-one relation, the type it points at, and that type's date field. */
  readonly relative?: { readonly via: Extract<keyof T, string>; readonly type: string; readonly field: string };
  /** How long before its due an occurrence opens (ISO 8601, P14D); with none it opens when the one before it falls due. */
  readonly opensBefore?: string;
  /** How long before its due an occurrence reminds (ISO 8601, P3D). */
  readonly remindBefore?: string;
  /** How long after its due an open occurrence is missed (ISO 8601, P1D); at its due when absent. */
  readonly overdueAfter?: string;
  /** The permission complete and listOccurrences need; any caller the route admits when absent. */
  readonly permission?: string;
  /** The permission setRecurrence, skip and moveOne need; permission when absent. */
  readonly managePermission?: string;
}

/**
 * A rule's dated occurrences on a table's rows (D152):
 * `export abstract class Filing implements Recurrence<Filing, { anchors: ["calendar"], timeZone: "Europe/London" }>`.
 * The type stays a table. Recurrence keeps the row's recurrence and its next
 * due in owned fields and an occurrence per date in a sub-table
 * (filing_recurrence_occurrences), each upcoming, open, done, missed or
 * skipped, up to horizonDays ahead; it arms each occurrence's open, remind
 * and overdue times as messages of the table's deadline queue, which the
 * API's @behaviorWorker(Filing) runs; its operations (recurrence.setRecurrence,
 * complete, skip, moveOne, listOccurrences) change and read them. A rule
 * change replans the occurrences that have not opened. The behaviors' core
 * alone implements it, and dates every language's occurrences alike. Like
 * Timers, it resolves to the empty object type; the first type argument is
 * the table itself.
 */
export type Recurrence<T, C extends RecurrenceOn<T>> = NonNullable<unknown>;

/** A Pool member's availability (D104): open, near its limit, or out. */
export type PoolAvailability = "open" | "near" | "out";

/** Pool's config on a table (D104): the field that ranks its members, how long a member is out when no time is named, how often an out member past its time is rechecked, and the permission markOut and reportRoom need. */
export interface PoolOn<T> {
  /** The own number field whose value ranks the members, lowest first, ties by key. */
  readonly order: FieldOf<T>;
  /** How long a member is out when markOut or a check names no time, in milliseconds, at least 1000; thirty minutes when absent. */
  readonly outForMs?: number;
  /** How often an out member past its outUntil is rechecked, in milliseconds, at least 1000; two minutes when absent. */
  readonly recheckAfterMs?: number;
  /** The permission markOut and reportRoom need; any caller the route admits when absent. pick needs none. */
  readonly permission?: string;
}

/**
 * A pool of interchangeable rows (D104):
 * `export abstract class Account implements Pool<Account, { order: "rank" }>`.
 * The type stays a table. Pool keeps each row's availability, outUntil,
 * outReason and checkedAt as owned columns, arms an out row's recheck as a
 * message of the table's deadline queue and writes each recheck's event to
 * its event queue, which the API's @behaviorWorker(Account) runs, handing
 * each event to the project's Workers method; its operations (pool.markOut,
 * pool.reportRoom, and pool.pick, a read of the table as a whole) change and
 * read them. The behaviors' core alone implements it: no engine type
 * composes it. Like Timers, it resolves to the empty object type; the first
 * type argument is the table itself.
 */
export type Pool<T, C extends PoolOn<T>> = NonNullable<unknown>;

/**
 * Where a rollup reads from on a table (D104's step 4): the table whose
 * rows point at the row (schema) and its to-one relation to this table
 * (link), the rows a HasMany of the table lists.
 */
export interface RollupSourceOn {
  /** The table whose rows point at this one; it composes a behavior, Workflow among them. */
  readonly schema: string;
  /** Its to-one relation to this table. */
  readonly link: string;
}

/**
 * One rollup of RollupsOn<T> (D104's step 4). count, all and any take no
 * field; countBy takes a string, enum or boolean field of the linking
 * table, or "Workflow.status", its Workflow's status; sum, min and max a
 * number field. all and any may gate states of the table's one Workflow
 * and count only terminal states with the outcomes they list. latest
 * waits for a later step on a table.
 */
export type RollupOn =
  | (RollupSourceOn & { readonly function: "count" })
  | (RollupSourceOn & { readonly function: "countBy" | "sum" | "min" | "max"; readonly field: string })
  | (RollupSourceOn & {
      readonly function: "all" | "any";
      /** The states of the table's Workflow that a transition into waits for the rollup to hold. */
      readonly gatedStates?: readonly string[];
      /** The outcomes a linked row's terminal state must have to count; every outcome when absent. */
      readonly outcomes?: readonly WorkflowOutcome[];
    });

/** Rollups' config on a table (D104's step 4): the rollups, by name. */
export interface RollupsOn<T> {
  /** The rollups, by name: camelCase, at most 64 characters. */
  readonly rollups: Readonly<Record<string, RollupOn>>;
}

/**
 * Values derived from the rows that point at a table's row (D104's step
 * 4): `export abstract class Project implements Rollups<Project, { rollups:
 * { done: { schema: "Task"; link: "project"; function: "all"; gatedStates:
 * ["done"] } } }>`. The type stays a table. Rollups keeps no column: a
 * read of the row computes its values through the core, and an all or any
 * rollup that gates a state holds a transition into it until the rollup
 * holds. Like Timers, it resolves to the empty object type; the first type
 * argument is the table itself.
 */
export type Rollups<T, C extends RollupsOn<T>> = NonNullable<unknown>;

/** Dependencies' config on a table (D104's step 4): the tables whose rows may block it, the gated states and the outcomes that finish a blocker. */
export interface DependenciesOn<T> {
  /** The tables whose rows may block this table's, each composing one Workflow; the table itself when absent. */
  readonly schemas?: readonly string[];
  /** The states of the table's Workflow that a transition into waits for every blocker to finish; every terminal state when absent. */
  readonly gatedStates?: readonly string[];
  /** The outcomes of a blocker's terminal state that finish it; success when absent. */
  readonly satisfiedBy?: readonly WorkflowOutcome[];
}

/**
 * Blockers between a table's rows (D104's step 4): `export abstract class
 * Task implements Dependencies<Task, { gatedStates: ["done"] }>`. The type
 * stays a table. Dependencies keeps a row's blockers in a sub-table
 * (task_dependencies_blockers), and holds a transition of the table's one
 * Workflow into a gated state while a blocker is not finished; its
 * operations (dependencies.addBlocker, removeBlocker, listBlockers) change
 * and read them, and a create takes blockers in its parameters. Like
 * Timers, it resolves to the empty object type; the first type argument is
 * the table itself.
 */
export type Dependencies<T, C extends DependenciesOn<T>> = NonNullable<unknown>;

/** One derived field of DerivedOn<T> (D73): the table's own fields it is derived from, never itself or another derived field; what the model is asked; the label of its model; and the states of the table's Workflow it is derived in. */
export interface DerivedFieldOn<T> {
  readonly from: readonly Extract<keyof T, string>[];
  /** What the model is asked, given the inputs: 1 to 4000 characters. */
  readonly instruction: string;
  /** A label the deployment maps to a provider's model, which the core only compares. */
  readonly model: string;
  /** The states of the table's one Workflow in which the field is derived; every state when absent. */
  readonly states?: readonly string[];
}

/** Derived's config on a table (D73, D103's step 4): the derived fields, by the table's own field, the permission a deriver needs and how many failed attempts a field takes. */
export interface DerivedOn<T> {
  readonly fields: { readonly [F in Extract<keyof T, string>]?: DerivedFieldOn<T> };
  /** The permission a deriver needs to pull, settle and apply values. */
  readonly permission: string;
  /** How many failed attempts a field takes before it waits for its inputs to move: 1 to 10, 3 when absent. */
  readonly maxAttempts?: number;
}

/**
 * Own fields a model outside the core fills (D73, D103's step 4):
 * `export abstract class Ticket implements Derived<Ticket, { fields: { category: { from: ["subject", "body"]; instruction: "Classify the request."; model: "standard" } }; permission: "tickets.derive" }>`.
 * The type stays a table, and a derived field is an own field of it, typed
 * by the table, whose JSON Schema is the output a deriver asks for. Derived
 * keeps each derived field's hashes, override and attempts in a sub-table
 * (ticket_derived_derivations); a deriver pulls the stale fields with
 * derived.staleDerivations and settles its answers with
 * derived.settleDerivations, operations of the table as a whole, which
 * write each row's values through derived.applyDerived; a caller's write of
 * a derived field overrides it until derived.rederive. Like Timers, it
 * resolves to the empty object type; the first type argument is the table
 * itself.
 */
export type Derived<T, C extends DerivedOn<T>> = NonNullable<unknown>;

/** Provenance's config on a table (D73, D103's step 4): the fields it records, which writes enter review, and the states of the table's Workflow a move into which requires fields out of review. */
export interface ProvenanceOn<T> {
  /** The table's own fields it records; every one when absent. */
  readonly fields?: readonly Extract<keyof T, string>[];
  /** Which writes enter review: the behaviors whose writes are proposed, the confidence at or above which one is accepted with no reviewer, and the permission a reviewer needs. */
  readonly review?: { readonly sources: readonly string[]; readonly below?: number; readonly permission: string };
  /** By state of the table's one Workflow, the recorded fields a move into it requires out of review, or all. */
  readonly gates?: Readonly<Record<string, { readonly reviewed: readonly Extract<keyof T, string>[] | "all" }>>;
}

/**
 * Who set each of a table's own fields last, and through what (D73, D103's
 * step 4): `export abstract class Ticket implements Provenance<Ticket, { review: { sources: ["Derived"]; permission: "tickets.review" } }>`.
 * The type stays a table. Provenance records each field's last write, its
 * source (caller, or the behavior whose operation wrote it), who made it and
 * for whom, when and with what confidence, in a sub-table
 * (ticket_provenance_records); with review, a write from a source is
 * proposed until a reviewer accepts or rejects it (provenance.accept,
 * reject), and the owned column provenance_in_review, which a list filters
 * on, holds whether any field is proposed. A gate holds a move of the
 * table's Workflow into its state while a field it lists is proposed. Like
 * Timers, it resolves to the empty object type; the first type argument is
 * the table itself.
 */
export type Provenance<T, C extends ProvenanceOn<T>> = NonNullable<unknown>;

/** A count Stock reads, or the own field of the table that holds one. */
export type StockAmountOn<T> = number | { readonly field: Extract<keyof T, string> };

/** Stock's config on a table (D70, D103's step 4): the permission receiving, adjusting and counting need, the unit price a hold returns, when a row reads low, the most one item holds, and whether a hold may take more than is available. */
export interface StockOn<T> {
  /** What receiveStock, adjustStock and countStock need; a count below what claims hold needs its .override. */
  readonly permission: string;
  /** The own integer field that holds the unit price, in its currency's minor units, and the currency: an ISO 4217 code or the own field that holds one. */
  readonly price?: { readonly field: Extract<keyof T, string>; readonly currency: string | { readonly field: Extract<keyof T, string> } };
  /** Stock.low is true while available is at or below this count or the own field's. */
  readonly lowAt?: StockAmountOn<T>;
  /** The most units one claim item holds, at least 1. */
  readonly maxPerHold?: number;
  /** Whether a hold may take more than is available; false when absent. */
  readonly backorder?: boolean;
}

/**
 * Units a supply counts (D70, D103's step 4): `export abstract class
 * Inventory implements Stock<Inventory, { permission: "stock.adjust";
 * price: { field: "priceCents"; currency: "GBP" } }>`. The type stays a
 * table. Stock keeps onHand and held as owned columns (stock_on_hand,
 * stock_held), each change of what is on hand a row of its ledger
 * (inventory_stock_movements) and its record of each claim item it holds
 * in another (inventory_stock_holds); available and low are computed at a
 * read. Its operations (stock.receiveStock, adjustStock, countStock and
 * listStockMovements) change and read them, and it implements the contract
 * Supply: Holds calls its holdFor, commitFor, releaseFor and returnFor on
 * the row, each holding the row's lock, and asks its checkHold, so a caller
 * takes units through a claim alone. Like Timers, it resolves to the empty
 * object type; the first type argument is the table itself.
 */
export type Stock<T, C extends StockOn<T>> = NonNullable<unknown>;

/** What each item of a hold asks: a quantity of units, a count or an own field (an each hold's element's field), or an interval of time from a date-time field, ending at another or after durationMs. */
export type HoldAskOn =
  | { readonly quantity: number | { readonly field: string } }
  | { readonly interval: { readonly start: string; readonly end: string } | { readonly start: string; readonly durationMs: number } };

/** One hold of HoldsOn<T> (D70): a to-one relation of the table (one item), or an own list field each of whose elements names a key of the supply table schema; the supply implements Supply (Stock or Calendar). */
export type HoldOn<T> = HoldAskOn &
  (
    | { readonly link: Extract<keyof T, string> }
    | { readonly each: { readonly field: Extract<keyof T, string>; readonly target: string }; readonly schema: string }
  );

/** Holds' config on a table (D70, D103's step 4): the holds by name, the states of the table's Workflow that take, commit, release and return them, and the most items a claim holds. */
export interface HoldsOn<T> {
  /** The holds, by name: camelCase, at most 16. */
  readonly holds: Readonly<Record<string, HoldOn<T>>>;
  /** The states whose entry takes the holds; the create alone when absent. */
  readonly takeOn?: readonly string[];
  /** The states whose entry commits what is held. */
  readonly commitOn?: readonly string[];
  /** The states whose entry lets go of what is held. */
  readonly releaseOn?: readonly string[];
  /** The states whose entry puts committed units back. */
  readonly returnOn?: readonly string[];
  /** The most items the holds ask together, 1 to 1000; 100 when absent. */
  readonly maxItems?: number;
  /** The Workflow whose status moves the holds: its field, when the table has several. */
  readonly status?: string;
}

/**
 * One claim's holds on any supply, tied to its Workflow (D70, D103's step
 * 4): `export abstract class Checkout implements Holds<Checkout, { holds: {
 * lines: { each: { field: "lines"; target: "inventoryId" }; schema:
 * "Inventory"; quantity: { field: "quantity" } } }; takeOn: ["held"];
 * commitOn: ["placed"]; releaseOn: ["cancelled"] }>`. The type stays a
 * table, beside its status Workflow. Holds keeps state and total as owned
 * columns (holds_state, holds_total), which a Reactions rule hears, and
 * each item it has taken in a sub-table (checkout_holds_items); items is
 * computed at a read, the items not taken yet pending. Entering a takeOn
 * state takes every item or none through each supply's Supply, under each
 * supply's lock; commitOn, releaseOn and returnOn move them on; an update
 * of a field a hold reads while it holds is refused. Its operations
 * (holds.retakeHolds, listHolds) take again and read. Like Timers, it
 * resolves to the empty object type; the first type argument is the table
 * itself.
 */
export type Holds<T, C extends HoldsOn<T>> = NonNullable<unknown>;

/** A day's opening hours, each range `["HH:MM", "HH:MM"]` from 00:00 to 24:00, by day of the week. */
export type CalendarDaysOn = Readonly<
  Partial<Record<"mon" | "tue" | "wed" | "thu" | "fri" | "sat" | "sun", readonly (readonly [string, string])[]>>
>;

/** Calendar's config on a table (D70, D103's step 4): weekly hours in an IANA time zone, how many holds may overlap, the grid and the length bounds of a hold, its buffers, the lead time and horizon, and the permission that sets a row's own hours and closures. */
export interface CalendarOn<T> {
  /** The IANA time zone the hours are in, read in the database the core bundles. */
  readonly timeZone: string;
  /** The weekly opening hours, by day. */
  readonly hours: CalendarDaysOn;
  /** How many holds may overlap at once, 1 to 10000, or the own integer field that holds it; 1 when absent. */
  readonly capacity?: number | { readonly field: Extract<keyof T, string> };
  /** The start grid, a whole number of slotMs after an opening time; any whole minute when absent. */
  readonly slotMs?: number;
  /** The shortest hold; slotMs, or a minute, when absent. */
  readonly minMs?: number;
  /** The longest hold, at most 31 days; a day when absent. */
  readonly maxMs?: number;
  /** The time kept free before and after each hold. */
  readonly bufferMs?: { readonly before?: number; readonly after?: number };
  /** How soon after now a hold may start; 0 when absent. */
  readonly leadMs?: number;
  /** How far ahead of now a hold may start; no limit when absent. */
  readonly horizonMs?: number;
  /** What setOpeningHours, addClosure and removeClosure need; nobody may call them when absent. */
  readonly permission?: string;
}

/**
 * Time a supply opens (D70, D103's step 4): `export abstract class Desk
 * implements Calendar<Desk, { timeZone: "Europe/London"; hours: { mon:
 * [["09:00", "17:30"]] }; slotMs: 900000; permission: "desk.manage" }>`.
 * The type stays a table. Calendar keeps its record of each claim item
 * that holds time of the row (desk_calendar_bookings), the row's closures
 * (desk_calendar_closures) and its own hours, time zone and count of
 * closures (desk_calendar_settings) in sub-tables; its fields timeZone,
 * hours and closures are computed at a read. Its operations
 * (calendar.setOpeningHours, addClosure, removeClosure, listBookings, and
 * findAvailability on the table as a whole) set and read them, and it
 * implements the contract Supply: Holds calls its holdFor, commitFor and
 * releaseFor on the row, each holding the row's lock, and asks its
 * checkHold, so a caller books time through a claim alone. Stock and
 * Calendar conflict. Like Timers, it resolves to the empty object type;
 * the first type argument is the table itself.
 */
export type Calendar<T, C extends CalendarOn<T>> = NonNullable<unknown>;

/** Payments' config on a table (D70, D103's step 4): what the row owes, an own integer field in a currency or the total of the table's Holds; the states a move into waits for money, voids in and captures in; what a refund drawn directly and the provider's adapter need; and, on a refund row, the relation to what it refunds and the states that issue it. */
export interface PaymentsOn<T> {
  /** What the row owes: an own integer field in its currency's minor units, or the total of the table's Holds, which the table lists before Payments. */
  readonly amount: { readonly field: Extract<keyof T, string> } | { readonly holds: true };
  /** The currency of an amount from a field: an ISO 4217 code or the own field that holds one. */
  readonly currency?: string | { readonly field: Extract<keyof T, string> };
  /** automatic (the default) or manual, which captures in captureOn's states. */
  readonly capture?: "automatic" | "manual";
  /** With capture manual, the states whose entry captures what is authorized. */
  readonly captureOn?: readonly string[];
  /** By state of the table's Workflow, what a move into it waits for: everything due authorized, or paid. */
  readonly require?: Readonly<Record<string, "authorized" | "paid">>;
  /** The states whose entry voids the attempts a provider can still void. */
  readonly voidOn?: readonly string[];
  /** Whether money captured in a voidOn state is refunded or kept for an operator; keep when absent. */
  readonly afterRelease?: "refund" | "keep";
  /** What refundPayment needs; a refund row refunds when absent. */
  readonly refunds?: { readonly permission: string };
  /** What the provider's adapter needs: recordPaymentEvent, listPaymentActions and settleRefundFor. */
  readonly record: { readonly permission: string };
  /** On a refund row, the required to-one relation to the row it refunds. */
  readonly refundOf?: { readonly link: Extract<keyof T, string> };
  /** On a refund row, the states whose entry asks the row it refunds for its amount. */
  readonly issueOn?: readonly string[];
}

/**
 * What a row owes and how it is settled (D70, D103's step 4): `export
 * abstract class Order implements Payments<Order, { amount: { field:
 * "totalCents" }; currency: "GBP"; require: { placed: "paid" }; record: {
 * permission: "payments.record" } }>`. The type stays a table, beside its
 * status Workflow. Payments keeps due, authorized, captured, refunded,
 * outstanding, overpaid, currency and state as owned columns, which a
 * Reactions rule hears, its attempts, actions, refunds and a refund row's
 * issues in sub-tables (order_payments_attempts and the rest), and the
 * provider events in a table kept for the table as a whole; attempts is
 * computed at a read. Its operations (payments.openPayment,
 * recordPaymentEvent, capturePayment, voidPayment, refundPayment,
 * refundFor, settleRefundFor, and listPaymentActions on the table as a
 * whole) record what a provider's adapter is to do and what it reports;
 * the core calls no provider. Like Timers, it resolves to the empty object
 * type; the first type argument is the table itself.
 */
export type Payments<T, C extends PaymentsOn<T>> = NonNullable<unknown>;
