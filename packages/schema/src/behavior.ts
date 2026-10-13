/**
 * The config each behavior takes, keyed by the behavior's registered name.
 * It lists the behaviors the core declares (internal/registry/behaviors in
 * the superschematic repository), which @superschematic/engine implements,
 * and the work-queue behaviors, which @superschematic/engine-workqueue
 * implements.
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
  /** Comments on the instance; it takes no config. */
  Comments: undefined;
  /** Immutable revisions of the instance's own fields, with an optional review step. */
  Revisions: RevisionsConfig;
  /** Blockers between instances, which hold up the type's Workflow; it requires Workflow. */
  Dependencies: DependenciesConfig;
  /** Typed links to instances of the schemas it names, optionally pinned to a revision or a release. */
  Links: LinksConfig;
  /** Values derived from the instances that point at this one through a link, computed when it is read. */
  Rollups: RollupsConfig;
  /** Full-text search over the type's own text fields. */
  Search: SearchConfig;
  /** Rules that move Workflow statuses after a change commits; it requires Workflow. */
  Reactions: ReactionsConfig;
  /** Fields of the type that its create sets and nothing changes after. */
  Constants: ConstantsConfig;
  /** Types an open JSON field of the type by the value of another of its fields. */
  Variants: VariantsConfig;
  /** Makes each instance the root of a version graph over rows of the kinds it names, each kind's content a type of the document. */
  Branches: BranchesConfig;
  /** Own fields each state of the type's Workflow requires to enter it, or freezes while the instance is in it; it requires Workflow. */
  StateRules: StateRulesConfig;
  /** Sign-offs by distinct principals that gate states of the type's Workflow; it requires Workflow. */
  Approvals: ApprovalsConfig;
  /** A number a person reads on each instance, set at its create from a count per schema, per scope target and per year. */
  Numbering: NumberingConfig;
  /** Named deadlines that start at the create, at an entry into a state of the type's Workflow or at a date-time field, and fire once due, moving the status. */
  Timers: TimersConfig;
  /** An exclusive, time-bounded lease on the instance, with a fencing token, heartbeats and directives to its holder. */
  Lease: LeaseConfig;
  /** Assigns the instance to one principal, who alone may then take its lease or claim it. */
  Assignment: AssignmentConfig;
  /** Makes the type's instances claimable work, in priority order; it requires Workflow and Lease. */
  Queue: QueueConfig;
  /** A heartbeat on an instance that stands for a worker, which the principal it names beats and a miss can expire that principal's leases. */
  Presence: PresenceConfig;
  /** Creates the instance's children and the edges between them, in one transaction, from a map of steps. */
  Blueprint: BlueprintConfig;
  /** Reserve-then-settle budgets in units the deployment names, held against limits here and in enclosing scopes. */
  Budget: BudgetConfig;
  /** Retries per failure class, with caps, kept results and stuck detection; it requires Workflow. */
  Retries: RetriesConfig;
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
   * not name is a success; a state a transition leaves has no outcome.
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

/** Links' config. */
export interface LinksConfig {
  /** The links an instance may hold, by name: camelCase, at most 64 characters. */
  readonly links: Readonly<Record<string, LinkConfig>>;
}

/**
 * One link of a Links config. It holds one target, or with many a list of
 * at most max, in the order they were linked.
 */
export type LinkConfig = LinkTarget &
  (
    | { readonly many?: false }
    | {
        /** The link holds a list of targets rather than one. */
        readonly many: true;
        /** The most targets it holds, from 1 to 100. */
        readonly max: number;
      }
  );

/** What a link points at and how it holds its target. */
export interface LinkTarget {
  /** The schema of the instance the link points at. */
  readonly schema: string;
  /**
   * Every create gives the link, which can then be moved to another target
   * but not unlinked, and the delete of its target is refused. A
   * many-valued link holds at least one target, and only the delete of its
   * last is refused.
   */
  readonly required?: boolean;
  /**
   * What the link records of the target, reporting the target's latest
   * beside it and whether the target has moved past it: true or
   * "revision", its revision, whose schema must compose Revisions;
   * "release", its release, whose schema must compose Branches.
   */
  readonly pinned?: boolean | "revision" | "release";
}

/** Rollups' config. */
export interface RollupsConfig {
  /** The rollups, by name: camelCase, at most 64 characters. */
  readonly rollups: Readonly<Record<string, RollupConfig>>;
}

/** Where a rollup reads from: the instances of a schema that point at this one through a link. */
export interface RollupSource {
  /** The schema whose instances point at this one; it composes Links. */
  readonly schema: string;
  /** The link of that schema's Links config that points at this schema. */
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

/** Search's config. */
export interface SearchConfig {
  /** The type's own top-level fields to index, by JSON key: each a string, or a scalar whose values are strings; at most 16. */
  readonly fields: readonly string[];
  /** A weight per indexed field, which multiplies that field's part of a match's score; 1 for a field it does not name. */
  readonly weights?: Readonly<Record<string, number>>;
  /** A vector per instance, which an outside embedder computes from the indexed fields' text; none when absent. */
  readonly vectors?: SearchVectorsConfig;
}

/** Search's vectors: what an embedder computes them with, and who may settle them. */
export interface SearchVectorsConfig {
  /** How many numbers a vector holds, 1 to 4096. */
  readonly dimensions: number;
  /** A label for the embedding model, which the engine only compares: a new one makes every vector stale. */
  readonly model: string;
  /** The permission an embedder needs to settle vectors. */
  readonly permission: string;
}

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
       * gains a new revision of Revisions, or, when its schema composes
       * Branches, a new release; for a link pinned to the one it gains, only
       * on an instance the new revision or release leaves stale.
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
  /** The schema of the linking instances, which composes Links and Workflow. */
  readonly schema: string;
  /** The link of that schema's Links config that points at this type's schema. */
  readonly link: string;
}

/** What a rule does: move the instance, or the instance its link points to, to a state through Workflow's transition. */
export interface ReactionThen {
  /** The state to move the target to. */
  readonly transition: string;
  /** A link of the type's Links config: the target is the instance it points to. The instance itself when absent. */
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

/** Branches' config. */
export interface BranchesConfig {
  /** The graph's kinds, by name (camelCase): the rows each ref holds. */
  readonly kinds: Readonly<Record<string, BranchesKind>>;
  /** The name of each instance's primary line; main when absent. */
  readonly primary?: string;
  /** How many commits past the nearest snapshot on its chain a commit is snapshotted at; 64 when absent. */
  readonly snapshotEvery?: number;
  /** Turns the sweep schedule on for the schema. */
  readonly sweep?: BranchesSweep;
}

/** One kind of a Branches graph, as @graphMember, @conflictUnit and @versioned declare a member of a compiled graph. */
export interface BranchesKind {
  /**
   * The type of the schema document, besides the instance type, whose
   * fields are the kind's content. None of its fields may take the JSON key
   * of a role or audit column: id, entity_key, ref_id, root_id,
   * deleted_on_ref, _version, created_at, created_by, updated_at or
   * updated_by.
   */
  readonly type: string;
  /** Containment: key is the kind's field, of a UUID scalar, that holds the parent row's entity key, and of is the parent's kind. */
  readonly parent?: { readonly key: string; readonly of: string };
  /** The kind's integer field that orders siblings. */
  readonly order?: string;
  /** At most one live row of the kind. */
  readonly singleton?: boolean;
  /** A field's conflict unit: atomic (the default), keyed, jsonSchema, or excluded (not content). */
  readonly units?: Readonly<Record<string, "atomic" | "keyed" | "jsonSchema" | "excluded">>;
  /** How many days of the kind's history the sweep keeps; all of it when absent. */
  readonly retentionDays?: number;
}

/** When the Branches sweep runs on a schema, and what it keeps. */
export interface BranchesSweep {
  /** How often it runs, in milliseconds; at least 1000. */
  readonly intervalMs: number;
  /** How long a discarded ref's rows are kept, in milliseconds; seven days when absent. */
  readonly discardGrace?: number;
  /** At most this many history images of each kind pruned per run; no limit when absent. */
  readonly pruneBatch?: number;
  /** Discards a draft with no write for this many milliseconds; none when absent. */
  readonly abandonAfter?: number;
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

/** Numbering's config. */
export interface NumberingConfig {
  /**
   * How a number prints: text with {seq}, or {seq:N} zero-padded to N
   * digits (1 to 10), exactly once, and {yyyy} or {yy}, the UTC year the
   * instance was created in: "INV-{yyyy}-{seq:4}" prints INV-2026-0042.
   */
  readonly format: string;
  /** Counts per target of a link of the type's Links config, which is required and holds one target. */
  readonly scope?: NumberingScope;
  /** "yearly" starts the count again at 1 each UTC year; the format then prints the year. */
  readonly reset?: "yearly";
}

/** What a Numbering count runs per. */
export interface NumberingScope {
  /** The link's name in the type's Links config. */
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
   * revision, or a release. One entry per link of the type's Links; needs
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
  /** The link of the type's Links whose target's new revision or release sends it. */
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

/** Queue's config. */
export interface QueueConfig {
  /** The Workflow states an instance is claimed from, and the state a claim moves it to. */
  readonly claim: { readonly from: readonly string[]; readonly to: string };
  /** An integer field of the type that orders claimNext: higher first, an instance without a value last. */
  readonly priorityField?: string;
  /** The type's own top-level scalar fields claimNext and countClaimable may filter on, by a value or a list of values. */
  readonly match?: readonly string[];
  /** The most instances one claimNext tries; 100 when absent. */
  readonly maxCandidates?: number;
  /**
   * Pinned links of the type's Links config: an instance one of which is
   * pinned to a revision or a release its target has moved past is not
   * claimed, and is no candidate until a change of it lets it back in.
   */
  readonly excludeStale?: readonly string[];
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
  /** The child schema: it composes Links with parentLink, Constants over keyField and every copied field, Dependencies when a step comes after another, and not Blueprint. */
  readonly schema: string;
  /** The link of the child schema's Links config that points at this schema. */
  readonly parentLink: string;
  /** A string field of the child's type that gets each step's key. */
  readonly keyField: string;
  /** Fields of this type copied to every child. */
  readonly copyFields?: readonly string[];
  /** Links of this type copied to every child, keeping the revision or release a pinned one records where the child's pins the same: the ones the instance holds when it is stamped. */
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
  /** A link of the type's Links config whose target, which composes Budget with the meter, is the enclosing scope. */
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
