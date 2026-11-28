/**
 * The config each behavior takes, keyed by the behavior's registered name.
 * It lists the behaviors the core declares (internal/registry/behaviors in
 * the superschematic repository), which @superschematic/engine implements,
 * the work-queue behaviors, which @superschematic/engine-workqueue
 * implements, and the commerce behaviors (D70), which
 * @superschematic/engine-commerce implements. Links, Search, Queue and
 * Branches are not among them: D103 folds each into a schema feature (a
 * relation, @searchField, @queue and @versioned), and the build refuses
 * them.
 * An extension's authoring package adds its behaviors by module
 * augmentation, as it adds an invocation policy key to MCPToolOptions:
 *
 *     declare module "@superschematic/schema" {
 *       interface BehaviorConfigs {
 *         "acme.Rating": { readonly maxStars: number };
 *       }
 *     }
 *
 * A behavior that takes no config maps to undefined. A program sees the
 * augmentation when it includes the file that declares it: through an
 * import of the authoring package, or a tsconfig.json include entry.
 *
 * The registry, not tsc, decides which behaviors a build accepts: it holds
 * each config to the behavior's declared config schema. A name no
 * augmentation in the program declares does not type-check, and a declared
 * name the binary does not register fails the load.
 */
export interface BehaviorConfigs {
  /** A state machine on the instance's status. */
  Workflow: WorkflowConfig;
  /** A conversation on the instance: messages and their threads, which their authors edit and delete, and others react to, pin and resolve. */
  Comments: CommentsConfig;
  /** Immutable revisions of the instance's own fields, with an optional review step. */
  Revisions: RevisionsConfig;
  /** Blockers between instances, which hold up the type's Workflow; it requires Workflow. */
  Dependencies: DependenciesConfig;
  /** Values derived from the instances that point at this one through a link, computed when it is read. */
  Rollups: RollupsConfig;
  /** Rules that move Workflow statuses after a change commits; it requires Workflow. */
  Reactions: ReactionsConfig;
  /** Fields of the type that its create sets and nothing changes after. */
  Constants: ConstantsConfig;
  /** Types an open JSON field of the type by the value of another of its fields. */
  Variants: VariantsConfig;
  /** Own fields each state of the type's Workflow requires to enter it, or freezes while the instance is in it; it requires Workflow. */
  StateRules: StateRulesConfig;
  /** Sign-offs by distinct principals that gate states of the type's Workflow; it requires Workflow. */
  Approvals: ApprovalsConfig;
  /** A number a person reads on each instance, set at its create from a count per schema, per scope target and per year. */
  Numbering: NumberingConfig;
  /** Named deadlines that start at the create, at an entry into a state of the type's Workflow or at a date-time field, and fire once due, moving the status. */
  Timers: TimersConfig;
  /** Own fields a model outside the engine fills from other own fields, which a deriver pulls and settles by hash. */
  Derived: DerivedConfig;
  /** Who set each own field last and through what, with an optional review that gates states of the type's Workflow. */
  Provenance: ProvenanceConfig;
  /** An exclusive, time-bounded lease on the instance, with a fencing token, heartbeats and directives to its holder. */
  Lease: LeaseConfig;
  /** Assigns the instance to one principal, who alone may then take its lease or claim it. */
  Assignment: AssignmentConfig;
  /** A heartbeat on an instance that stands for a worker, which the principal it names beats and a miss can expire that principal's leases. */
  Presence: PresenceConfig;
  /** Creates the instance's children and the edges between them, in one transaction, from a map of steps. */
  Blueprint: BlueprintConfig;
  /** Reserve-then-settle budgets in units the deployment names, held against limits here and in enclosing scopes. */
  Budget: BudgetConfig;
  /** Retries per failure class, with caps, kept results and stuck detection; it requires Workflow. */
  Retries: RetriesConfig;
  /** Units a supply counts: on hand, held by claims, and every movement, held and committed through the supply contract; it conflicts with Calendar. */
  Stock: StockConfig;
  /** Time a supply opens: weekly hours in a time zone, closures and a capacity, held and booked through the supply contract; it conflicts with Stock. */
  Calendar: CalendarConfig;
  /** One claim's holds on any supply, taken, committed, released and returned as its Workflow moves; it requires Workflow. */
  Holds: HoldsConfig;
  /** What the instance owes and the payments and refunds that settle it, through a provider adapter outside the engine. */
  Payments: PaymentsConfig;
}

/** Workflow's config. */
export interface WorkflowConfig {
  /** Every state an instance can be in: a letter, then letters, digits, `_` and `-`. */
  readonly states: readonly string[];
  /** The state a new instance starts in; the first of states when absent. */
  readonly initial?: string;
  /** The moves the status may make. A state that no transition leaves is terminal. */
  readonly transitions: readonly WorkflowTransition[];
  /**
   * The outcome of each terminal state it names. A terminal state it does
   * not name is a success; a state a transition leaves has no outcome. A
   * table's Workflow names the outcome of such a state in `finished`
   * (@superschematic/db, D113), which an engine type refuses.
   */
  readonly outcomes?: Readonly<Record<string, WorkflowOutcome>>;
}

/** The outcome of a Workflow's terminal state, which blockers, rollups and reactions read. */
export type WorkflowOutcome = "success" | "failure" | "neutral";

/** One move a Workflow's status may make. */
export interface WorkflowTransition {
  readonly from: string;
  readonly to: string;
  /** The permission a caller needs to make it; absent, any caller who may write the instance can. */
  readonly permission?: string;
}

/** Comments' config; every member is optional. */
export interface CommentsConfig {
  /** The longest body a post or an edit may give, in code points, 1 to 40000; 40000 when absent. */
  readonly maxBodyLength?: number;
  /** How long after posting its author may edit a message, in milliseconds, at least 1000; no limit when absent. A caller holding moderatePermission is not held to it. */
  readonly editWindowMs?: number;
  /** The block kinds a post or an edit may give; every kind when absent, none when empty. */
  readonly blocks?: readonly CommentsBlockKind[];
  /** The permission that lets a caller edit and delete another's message, edit past editWindowMs, and purge. Absent, only a message's author edits or deletes it, and nobody purges. */
  readonly moderatePermission?: string;
  /** The permission a caller needs to pin and unpin. Absent, any caller the policy lets write may. */
  readonly pinPermission?: string;
  /** The permission that lets a caller resolve and reopen another's thread. Absent, only a thread's author does. */
  readonly resolvePermission?: string;
}

/** A kind of block a Comments message may hold: a reference to another instance, or an action card. */
export type CommentsBlockKind = "ref" | "action";

/** Revisions' config. */
export interface RevisionsConfig {
  /** Turns on the review step: propose, approve, reject and listProposals. */
  readonly review?: {
    /** The permission a caller needs to approve or reject a proposal. */
    readonly permission: string;
  };
}

/** Dependencies' config. */
export interface DependenciesConfig {
  /** The schemas whose instances may block this type's, each composing Workflow; the type's own schema when absent. */
  readonly schemas?: readonly string[];
  /**
   * The states of the type's Workflow that a transition into waits for
   * every blocker to finish; every terminal state when absent.
   */
  readonly gatedStates?: readonly string[];
  /**
   * The outcomes of a blocker's terminal state that finish it; success
   * when absent. A blocker in a terminal state with another outcome blocks
   * until it is removed.
   */
  readonly satisfiedBy?: readonly WorkflowOutcome[];
}

/** Rollups' config. */
export interface RollupsConfig {
  /** The rollups, by name: camelCase, at most 64 characters. */
  readonly rollups: Readonly<Record<string, RollupConfig>>;
}

/** Where a rollup reads from: the instances of a schema that point at this one through a link. */
export interface RollupSource {
  /** The schema whose instances point at this one through a relation. */
  readonly schema: string;
  /** That schema's relation, by its field, that points at this schema. */
  readonly link: string;
}

/**
 * One rollup of a Rollups config. count, all and any take no field;
 * countBy takes a string, enum or boolean field, or "Workflow.status",
 * the linked instances' Workflow status; sum, min and max a number or
 * integer field; and latest any field, or "Workflow.status": its value on
 * the linked instance created last. all and any may gate states
 * of the type's Workflow, and count only terminal states with the
 * outcomes they list.
 */
export type RollupConfig =
  | (RollupSource & { readonly function: "count" })
  | (RollupSource & { readonly function: "countBy" | "sum" | "min" | "max" | "latest"; readonly field: string })
  | (RollupSource & {
      readonly function: "all" | "any";
      /** The states of the type's Workflow that a transition into waits for the rollup to hold. */
      readonly gatedStates?: readonly string[];
      /** The outcomes a linked instance's terminal state must have to count; every outcome when absent. */
      readonly outcomes?: readonly WorkflowOutcome[];
    });

/** Reactions' config. */
export interface ReactionsConfig {
  /** The rules, each one when and one then, in the order they run. */
  readonly rules: readonly ReactionRule[];
}

/** One rule of a Reactions config. */
export interface ReactionRule {
  readonly when: ReactionWhen;
  readonly then: ReactionThen;
}

/** What sets a rule off: one of enters, changes, allTerminal, anyTerminal, holds and revised. */
export type ReactionWhen =
  | {
      /** A state of the type's Workflow: the rule fires when the instance's status becomes it, by a create or a transition. */
      readonly enters: string;
    }
  | {
      /**
       * The rule fires when a create, an update or an operation of the
       * instance moves the value of field: to to when it is given, from
       * from when it is given. A create moves a field from absent; a delete
       * sets nothing off.
       */
      readonly changes: ReactionChanges;
    }
  | {
      /**
       * The rule fires on an instance when an instance of schema that links
       * to it through link changes or goes, and every instance that links to
       * it there is in a terminal state of its own schema's Workflow, with
       * an outcome outcomes lists when it is given, at least one.
       */
      readonly allTerminal: ReactionSource & { readonly outcomes?: readonly WorkflowOutcome[] };
    }
  | {
      /**
       * The rule fires on an instance when an instance of schema that links
       * to it through link enters a terminal state of its own schema's
       * Workflow whose outcome outcomes lists, or is linked to it while in
       * one.
       */
      readonly anyTerminal: ReactionSource & { readonly outcomes: readonly WorkflowOutcome[] };
    }
  | {
      /**
       * An all or any rollup of the type's Rollups config: the rule fires
       * on an instance when a change of an instance of the rollup's schema
       * makes the rollup hold over at least one linked instance, judged with
       * the other linked instances as they are when the rule runs.
       */
      readonly holds: string;
    }
  | {
      /**
       * The rule fires on an instance when the instance its link points to
       * gains a new revision of Revisions, or a new release of its version
       * graph (@versioned); for a link pinned to the one it gains, only on an
       * instance the new revision or release leaves stale.
       */
      readonly revised: { readonly link: string };
    };

/** The field a changes rule hears, and the values it moves between. */
export interface ReactionChanges {
  /**
   * An own field of the type, by its JSON key, or a field of a behavior
   * the type lists, by qualified name ("Workflow.status"), a member path
   * after it allowed ("Lease.ended.reason"). A volatile field, which moves
   * at a read with no event, and "Workflow.enteredAt", which moves with
   * the status, are refused.
   */
  readonly field: string;
  /** The value the field moves to; any value when absent. */
  readonly to?: unknown;
  /** The value the field moves from; any value when absent. A create moves a field from absent, which from never names. */
  readonly from?: unknown;
}

/** The instances an allTerminal or anyTerminal rule hears. */
export interface ReactionSource {
  /** The schema of the linking instances, which composes Workflow and points at this type's schema through a relation. */
  readonly schema: string;
  /** That schema's relation, by its field, that points at this type's schema. */
  readonly link: string;
}

/** What a rule does: move the instance, or the instance its link points to, to a state through Workflow's transition. */
export interface ReactionThen {
  /** The state to move the target to. */
  readonly transition: string;
  /** A relation of the type, by its field: the target is the instance it points to. The instance itself when absent. */
  readonly link?: string;
}

/** Constants' config. */
export interface ConstantsConfig {
  /**
   * The type's own top-level fields, by JSON key, that keep the value their
   * create gives them: an update that changes one is refused. A field the
   * create leaves absent stays absent.
   */
  readonly fields: readonly string[];
  /** The permission a caller needs to change them after the create; nobody may when absent. */
  readonly permission?: string;
}

/** Variants' config. */
export interface VariantsConfig {
  /** The type's own field the variants type: a Generic.JSON field, or one of a scalar whose values are JSON objects. */
  readonly field: string;
  /** The type's own string or enum field whose value picks the type. */
  readonly by: string;
  /**
   * By a value of by, the type of the schema document, besides the instance
   * type, that field holds while by holds the value. While by holds another
   * value or none, field holds none.
   */
  readonly types: Readonly<Record<string, string>>;
}

/** StateRules' config. */
export interface StateRulesConfig {
  /** The rules, by a state of the type's Workflow. */
  readonly states: Readonly<Record<string, StateRule>>;
  /** The permission a caller needs to change a field a state freezes; nobody may when absent. What a state requires holds for every caller. */
  readonly permission?: string;
}

/**
 * One state's rules under StateRules. A field is held when it is neither
 * absent nor null.
 */
export interface StateRule {
  /**
   * The type's own top-level fields, by JSON key, an instance must hold to
   * enter the state, and keeps while it is in it: a transition into the
   * state without one is refused, a create when the state is the
   * Workflow's initial one, and an update that clears one while the
   * instance is in the state.
   */
  readonly required?: readonly string[];
  /** The type's own top-level fields, by JSON key, that do not change while the instance is in the state, or "all", every one of them. */
  readonly frozen?: readonly string[] | "all";
}

/** Approvals' config. */
export interface ApprovalsConfig {
  /** The gates, by the state of the type's Workflow each gates: a transition into it waits for its sign-offs. */
  readonly gates: Readonly<Record<string, ApprovalsGate>>;
  /**
   * Whether a change of the instance's own fields, an update or an
   * operation's, consumes every gate's sign-offs; true when absent.
   */
  readonly resetOnChange?: boolean;
}

/** One gate of an Approvals config. */
export interface ApprovalsGate {
  /** How many distinct principals must sign off before a transition into the state: 1 to 100. */
  readonly count: number;
  /** The permission a caller needs to sign off. */
  readonly permission: string;
  /** The states of the type's Workflow a transition from is gated; every state when absent. */
  readonly from?: readonly string[];
  /** Whether the principal who created the instance is refused a sign-off; true when absent. */
  readonly excludeCreator?: boolean;
}

/** Derived's config. */
export interface DerivedConfig {
  /** The derived fields, by own top-level field: 1 to 16. */
  readonly fields: Readonly<Record<string, DerivedField>>;
  /** The permission a deriver needs to pull, settle and apply values. */
  readonly permission: string;
  /** How many failed attempts a field takes before it waits for its inputs to move: 1 to 10, 3 when absent. */
  readonly maxAttempts?: number;
}

/** One derived field of a Derived config. */
export interface DerivedField {
  /** The own top-level fields the value is derived from, never the field itself or another derived field. */
  readonly from: readonly string[];
  /** What the model is asked, given the inputs: at most 4000 characters. */
  readonly instruction: string;
  /** A label the deployment maps to a provider's model, which the engine only compares. */
  readonly model: string;
  /** The states of the type's Workflow in which the field is derived; every state when absent. */
  readonly states?: readonly string[];
}

/** Provenance's config. */
export interface ProvenanceConfig {
  /** The own top-level fields it records; every one when absent. */
  readonly fields?: readonly string[];
  /** Which writes enter review, and who reviews them. */
  readonly review?: ProvenanceReview;
  /** By state of the type's Workflow, the fields a move into it requires out of review. */
  readonly gates?: Readonly<Record<string, { readonly reviewed: readonly string[] | "all" }>>;
}

/** The review of a Provenance config. */
export interface ProvenanceReview {
  /** The behaviors whose writes enter review, each one the type composes. */
  readonly sources: readonly string[];
  /** A confidence from 0 to 1 at or above which a write is accepted with no reviewer; every write from a source is proposed when absent. */
  readonly below?: number;
  /** The permission a reviewer needs to accept or reject. */
  readonly permission: string;
}

/** Numbering's config. */
export interface NumberingConfig {
  /**
   * How a number prints: text with {seq}, or {seq:N} zero-padded to N
   * digits (1 to 10), exactly once, and {yyyy} or {yy}, the UTC year the
   * instance was created in: "INV-{yyyy}-{seq:4}" prints INV-2026-0042.
   */
  readonly format: string;
  /** Counts per target of a required relation of the type. */
  readonly scope?: NumberingScope;
  /** "yearly" starts the count again at 1 each UTC year; the format then prints the year. */
  readonly reset?: "yearly";
}

/** What a Numbering count runs per. */
export interface NumberingScope {
  /** The relation's field. */
  readonly link: string;
}

/** Timers' config. */
export interface TimersConfig {
  /** The deadlines, by name: a letter, then letters, digits, `_` and `-`. */
  readonly deadlines: Readonly<Record<string, TimersDeadline>>;
  /** How often the engine's runner fires the due deadlines, in milliseconds, at least 1000; 5000 when absent. */
  readonly sweepMs?: number;
  /** The permission a caller needs to reset or snooze a deadline; nobody may when absent. */
  readonly permission?: string;
}

/**
 * One deadline of a Timers config: due afterMs after what it starts from,
 * it fires once when it comes due, marked fired whatever its move meets.
 * A deadline that names a state needs Workflow on the type.
 */
export interface TimersDeadline {
  /** What it starts from: the create, each entry of the status into a state of the type's Workflow, or the time an own date-time field holds. */
  readonly start: TimersStart;
  /** How long after its start it is due, in milliseconds; 0 when absent. Negative, before it, for a field start alone. */
  readonly afterMs?: number;
  /** Moves the status of the type's Workflow when it fires, while the status is one of from. */
  readonly then?: TimersThen;
  /** Cancels it while it is pending: once the status is out of the state it starts in (leaves), or in one of the states given (enters). */
  readonly until?: TimersUntil;
}

/** What a Timers deadline starts from: a field start moves with its field, and its field cleared cancels it. */
export type TimersStart = { readonly create: true } | { readonly enters: string } | { readonly field: string };

/** The move a Timers deadline makes when it fires. */
export interface TimersThen {
  /** The state to move to. */
  readonly transition: string;
  /** The states it moves from; when absent, the state the deadline starts in, its enters state or the Workflow's initial one. A field start names them. */
  readonly from?: readonly string[];
}

/** What cancels a pending Timers deadline: leaves names the state it starts in, which a field start has none of. */
export type TimersUntil = { readonly leaves: string } | { readonly enters: readonly string[] };

/** Lease's config. */
export interface LeaseConfig {
  /** How long a lease lasts after its acquire or its last heartbeat, in milliseconds, at least 1000; 60000 when absent. */
  readonly ttlMs?: number;
  /** How often the holder should send a heartbeat, in milliseconds, at most half of ttlMs; a third of ttlMs when absent. */
  readonly heartbeatMs?: number;
  /** How often the engine's runner expires lapsed leases, in milliseconds, at least 1000; 5000 when absent. */
  readonly sweepMs?: number;
  /** The longest a principal may hold a lease after acquiring it, in milliseconds, renewed or not; no limit when absent. */
  readonly maxHoldMs?: number;
  /** An integer field of the type whose positive value overrides maxHoldMs for the instance. */
  readonly maxHoldField?: string;
  /**
   * Moves the instance's Workflow status, through transition, when a lease
   * ends with the work unfinished: at an expiry, an abandon and a release,
   * while the status is one of from. Needs Workflow on the type.
   */
  readonly onExpiry?: LeaseTransition;
  /** How many expiries an instance may have, abandons included, before its lease cannot be acquired again. */
  readonly maxExpiries?: number;
  /** Used instead of onExpiry at the expiry or abandon that reaches maxExpiries, which it needs. */
  readonly escalate?: LeaseTransition;
  /** Writing operations of the type's other behaviors, as `<Behavior>.<operation>`, that other principals may run while the lease is active. */
  readonly exempt?: readonly string[];
  /**
   * Refuses a write to an instance with an active lease that does not
   * present the current token as Lease's precondition, the holder's own
   * included, so every process of a principal fences its writes.
   */
  readonly requireToken?: boolean;
  /** The permission a principal needs to acquire a lease. */
  readonly acquirePermission?: string;
  /** The permission that releases another principal's lease, writes while another holds it, resets expiries, and sends directives when directPermission is absent. */
  readonly overridePermission?: string;
  /** The permission a principal needs to send the holder a directive. */
  readonly directPermission?: string;
  /**
   * Directives the engine's runner sends the holder of an instance's
   * active lease when the target of one of its links moves on: a new
   * revision, or a release. One entry per relation of the type; needs
   * directPermission or overridePermission, which the runner's principal
   * must hold.
   */
  readonly directOn?: readonly LeaseDirectOn[];
}

/** A move of the status a lease's end makes: to transition, from one of from. */
export interface LeaseTransition {
  readonly transition: string;
  readonly from: readonly string[];
}

/** A directive Lease's directOn sends when a link's target moves on. */
export interface LeaseDirectOn {
  /** The relation of the type, by its field, whose target's new revision or release sends it. */
  readonly revised: { readonly link: string };
  /** The directive's name. */
  readonly name: string;
  /** Its data, beside revised, which the runner sets to what moved. */
  readonly data?: Readonly<Record<string, unknown>>;
}

/** Assignment's config. */
export interface AssignmentConfig {
  /** The permission a principal needs to assign another principal, reassign, or unassign another principal. */
  readonly permission?: string;
}

/** Presence's config. */
export interface PresenceConfig {
  /** How long the instance stays present after its create or its last beat, in milliseconds, at least 1000. */
  readonly ttlMs: number;
  /** A string field of the type that holds the subject of the principal the instance stands for, the only principal that may beat it. */
  readonly principalField: string;
  /** Moves the instance's Workflow status, through transition, when it is missed while the status is one of from. Needs Workflow on the type. */
  readonly onMissed?: PresenceTransition;
  /** Moves the instance's Workflow status, through transition, at a beat while the status is one of from. Needs Workflow on the type. */
  readonly onBeat?: PresenceTransition;
  /** Schemas that compose Lease, whose leases the principal holds a miss expires through expireHolder. */
  readonly releaseLeases?: readonly string[];
  /** How often the engine's runner misses the instances past their deadline, in milliseconds, at least 1000; 5000 when absent. */
  readonly sweepMs?: number;
}

/** A move of the status a miss or a beat makes: to transition, from one of from. */
export interface PresenceTransition {
  readonly transition: string;
  readonly from: readonly string[];
}

/** Blueprint's config: one of steps and from. */
export type BlueprintConfig = BlueprintBase & ({ readonly steps: BlueprintSteps; readonly from?: never } | { readonly from: BlueprintSource; readonly steps?: never });

/** What every Blueprint config gives. */
export interface BlueprintBase {
  /** The child schema: it has parentLink, a relation to this schema, and composes Constants over keyField and every copied field, Dependencies when a step comes after another, and not Blueprint. */
  readonly schema: string;
  /** The child's relation, by its field, that points at this schema. */
  readonly parentLink: string;
  /** A string field of the child's type that gets each step's key. */
  readonly keyField: string;
  /** Fields of this type copied to every child. */
  readonly copyFields?: readonly string[];
  /** Relations of this type, by their fields, copied to every child: the targets the instance holds when it is stamped. */
  readonly copyLinks?: readonly string[];
}

/** A map of steps, by key: each one child. */
export type BlueprintSteps = Readonly<Record<string, BlueprintStep>>;

/** One step of a Blueprint. */
export interface BlueprintStep {
  /** The keys of the steps whose children block this step's child. */
  readonly after?: readonly string[];
  /** Stamps the step only when this instance's field equals a value, or is a list that includes one. */
  readonly when?: { readonly field: string; readonly equals: unknown } | { readonly field: string; readonly includes: unknown };
  /** Fields of the child beside its key and the copied fields. */
  readonly data?: Readonly<Record<string, unknown>>;
}

/** Where a Blueprint's steps are kept: a link of the type pinned to a revision, and the field of the linked instance that holds them. */
export interface BlueprintSource {
  readonly link: string;
  readonly field: string;
}

/** Budget's config. */
export interface BudgetConfig {
  /** The meters, by name: camelCase. */
  readonly meters: { readonly [meter: string]: BudgetMeter };
  /** The permission a principal needs to change a meter's limit, with setLimit or through limitField. */
  readonly limitPermission?: string;
  /** A directive sent to the holder of the active lease when usage takes the instance or an enclosing scope over its limit; needs Lease. */
  readonly onExceeded?: { readonly direct: string };
  /** Moves the instance's Workflow status, through transition, when usage leaves it over a limit while the status is one of from; needs Workflow. */
  readonly escalate?: { readonly transition: string; readonly from: readonly string[] };
}

/** One meter of a Budget config. */
export interface BudgetMeter {
  /** The most the instance may have used and reserved together; with limitField, the limit while the field holds none. No limit of its own without either. */
  readonly limit?: number;
  /** An integer field of the type that holds the instance's limit; limit is the limit while it holds no value of at least 0. */
  readonly limitField?: string;
  /** The amount reserve takes for the meter when it is given no meter; with reserveField, when the field holds no positive integer. */
  readonly reserve?: number;
  /** An integer field of the type whose positive value is that amount, in place of reserve. */
  readonly reserveField?: string;
  /** A relation of the type, by its field, whose target, which composes Budget with the meter, is the enclosing scope. */
  readonly scope?: string;
  /** daily: the used amount starts again at 0 at each UTC day. */
  readonly reset?: "daily";
}

/** Retries' config. */
export interface RetriesConfig {
  /** The failure classes, by name: a cap of attempts each, with a hint recordAttempt returns for a failure of the class, or terminal. */
  readonly classes: { readonly [failure: string]: { readonly attempts: number; readonly hint?: string } | "terminal" };
  /** How many failures of every class together an instance may have. */
  readonly totalAttempts: number;
  /** An object field of the type with the instance's own caps, by class name and totalAttempts. */
  readonly limitsField?: string;
  /**
   * The permission that changes limitsField once the instance exists; absent, the caps it was created with stay.
   * Never the holder of the instance's active lease, with it or without: Retries guards its own field, where
   * Constants, the general rule for a field nothing changes, would let any caller with its permission.
   */
  readonly limitsPermission?: string;
  /** Keeps the best scoring result: by at least minDelta, and never losing a neverRegress predicate. */
  readonly keepBest?: { readonly minDelta?: number; readonly neverRegress?: readonly string[] };
  /** How many failures in a row, none kept, with the same signature exhaust the instance as stuck. */
  readonly stuckAfter?: number;
  /** A field of the type a kept result is written to. */
  readonly resultField?: string;
  /** The state of the type's Workflow an exhausted instance moves to. */
  readonly exhaustedState: string;
  /** The states exhaustion moves the status from; every state but the terminal ones and exhaustedState when absent. */
  readonly from?: readonly string[];
  /** The permission a principal needs to record an attempt. */
  readonly permission?: string;
}

/** An own field of the type, by JSON key, that holds a value in place of a literal. */
export interface FieldSource {
  readonly field: string;
}

/** An ISO 4217 currency code, three uppercase letters, as Finance.Currency holds one. */
export type CurrencyCode = string;

/** Stock's config. */
export interface StockConfig {
  /** The unit price holdFor returns and Holds keeps on each item: an own integer field in the currency's minor units, and its currency. */
  readonly price?: StockPrice;
  /** Stock.low is true while available is at or below this count, or the count an own integer field holds. */
  readonly lowAt?: number | FieldSource;
  /** The most units one item of a claim may hold. */
  readonly maxPerHold?: number;
  /** Whether a hold may take more than is available; false when absent. */
  readonly backorder?: boolean;
  /** The permission a caller needs to receive, adjust and count stock; its .override counts below what claims hold. */
  readonly permission: string;
}

/** The unit price of a Stock config. */
export interface StockPrice {
  /** An own integer field, such as a Finance.Money field, holding the unit price in the currency's minor units. */
  readonly field: string;
  /** An ISO 4217 code, or an own Finance.Currency field that holds one. */
  readonly currency: CurrencyCode | FieldSource;
}

/** A wall time, "HH:MM", from "00:00" to "24:00". */
export type WallTime = string;

/** Weekly opening hours: each day a list of [open, close] wall-time ranges; a day absent or empty is closed. */
export type CalendarWeek = {
  readonly [day in "mon" | "tue" | "wed" | "thu" | "fri" | "sat" | "sun"]?: readonly (readonly [WallTime, WallTime])[];
};

/** Calendar's config. Durations are milliseconds. */
export interface CalendarConfig {
  /** The IANA time zone the hours are wall times in; an instance may set its own. */
  readonly timeZone: string;
  /** The weekly opening hours an instance has until it sets its own. */
  readonly hours: CalendarWeek;
  /** How many holds may overlap at any instant, or the count an own integer field holds; 1 when absent. */
  readonly capacity?: number | FieldSource;
  /** The start grid from each opening time; any whole minute when absent. */
  readonly slotMs?: number;
  /** The shortest hold; slotMs when absent. */
  readonly minMs?: number;
  /** The longest hold; a day when absent. */
  readonly maxMs?: number;
  /** Time kept free before and after each hold, which counts against capacity. */
  readonly bufferMs?: { readonly before?: number; readonly after?: number };
  /** How long ahead of its start a hold must be made; 0 when absent. */
  readonly leadMs?: number;
  /** How far ahead a hold may start; no limit when absent. */
  readonly horizonMs?: number;
  /** The permission a caller needs to set an instance's hours and closures; nobody may when absent. */
  readonly permission?: string;
}

/** Holds' config. */
export interface HoldsConfig {
  /** The holds, by name: camelCase. */
  readonly holds: Readonly<Record<string, HoldsHold>>;
  /** The states of the type's Workflow whose entry takes the holds, and the create into one; the create alone when absent. */
  readonly takeOn?: readonly string[];
  /** The states whose entry commits what is held. */
  readonly commitOn?: readonly string[];
  /** The states whose entry releases what is held, such as an expiry a Timers deadline moves the claim to. */
  readonly releaseOn?: readonly string[];
  /** The states whose entry returns the units of counted supply the claim committed. */
  readonly returnOn?: readonly string[];
  /** The most items the holds may ask together; 100 when absent. */
  readonly maxItems?: number;
}

/**
 * One hold of a Holds config: a single-valued link to the supply, or an
 * own list field whose elements each name a supply of schema; and the
 * units each item asks of counted supply, or the time of timed supply. An
 * each hold reads its fields from the list's element.
 */
export type HoldsHold = (
  | { readonly link: string; readonly each?: never; readonly schema?: never }
  | { readonly each: { readonly field: string; readonly target: string }; readonly schema: string; readonly link?: never }
) &
  (
    | { readonly quantity: number | FieldSource; readonly interval?: never }
    | { readonly interval: HoldsInterval; readonly quantity?: never }
  );

/** The time a timed hold's item asks: from a date-time field to another, or for durationMs. */
export type HoldsInterval =
  | { readonly start: string; readonly end: string; readonly durationMs?: never }
  | { readonly start: string; readonly durationMs: number; readonly end?: never };

/** Payments' config. */
export interface PaymentsConfig {
  /** What the instance owes: an own integer field in the currency's minor units, or the type's Holds.total. */
  readonly amount: FieldSource | { readonly holds: true };
  /** The currency of an amount from a field: an ISO 4217 code, or an own Finance.Currency field. */
  readonly currency?: CurrencyCode | FieldSource;
  /** automatic: the provider captures what it authorizes; manual: captureOn states capture it. */
  readonly capture?: "automatic" | "manual";
  /** With capture manual, the states whose entry captures what is authorized. */
  readonly captureOn?: readonly string[];
  /** Per state of the type's Workflow, what a move into it waits for. */
  readonly require?: Readonly<Record<string, "authorized" | "paid">>;
  /** The states whose entry cancels open attempts and voids authorized ones. */
  readonly voidOn?: readonly string[];
  /** What becomes of money captured while the instance is in a voidOn state; keep when absent. */
  readonly afterRelease?: "refund" | "keep";
  /** Who refunds directly with refundPayment. */
  readonly refunds?: { readonly permission: string };
  /** Who reports what providers did: the adapter's principal. */
  readonly record: { readonly permission: string };
  /** Makes the instance a refund of the instance a required relation of the type names. */
  readonly refundOf?: { readonly link: string };
  /** With refundOf, the states whose entry asks the linked instance to refund the amount. */
  readonly issueOn?: readonly string[];
}

/** A name @behavior takes: a key of BehaviorConfigs. */
export type BehaviorName = keyof BehaviorConfigs;

/**
 * The config argument of @behavior(name, config): optional when the
 * behavior takes no config or every key of its config is optional.
 */
export type BehaviorConfigArg<N extends BehaviorName> =
  undefined extends BehaviorConfigs[N]
    ? [config?: BehaviorConfigs[N]]
    : Record<string, never> extends BehaviorConfigs[N]
      ? [config?: BehaviorConfigs[N]]
      : [config: BehaviorConfigs[N]];

/**
 * Composes a behavior on a type: code that adds fields, operations, checks
 * and storage to the type when an engine runs the schema. Several apply in
 * source order, and a type lists a behavior once. The config is a literal,
 * checked against the behavior's declaration when the schema loads.
 */
export function behavior<N extends BehaviorName>(
  _name: N,
  ..._config: BehaviorConfigArg<N>
): ClassDecorator {
  return () => {};
}
