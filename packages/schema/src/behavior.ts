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
  /** Typed links to instances of the schemas it names, optionally pinned to a revision. */
  Links: LinksConfig;
  /** Values derived from the instances that point at this one through a link, computed when it is read. */
  Rollups: RollupsConfig;
  /** Full-text search over the type's own text fields. */
  Search: SearchConfig;
  /** Rules that move Workflow statuses after a change commits; it requires Workflow. */
  Reactions: ReactionsConfig;
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
}

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
   * The terminal states of the type's Workflow that a transition into
   * waits for every blocker to reach a terminal state of its own; every
   * terminal state when absent.
   */
  readonly gatedStates?: readonly string[];
}

/** Links' config. */
export interface LinksConfig {
  /** The links an instance may hold, by name: camelCase, at most 64 characters. */
  readonly links: Readonly<Record<string, LinkConfig>>;
}

/** One link of a Links config. */
export interface LinkConfig {
  /** The schema of the instance the link points at. */
  readonly schema: string;
  /** Once set, the link can be moved to another target but not unlinked, and the delete of its target is refused. */
  readonly required?: boolean;
  /** The link records the target's revision and reports whether the target has moved past it; the schema must compose Revisions. */
  readonly pinned?: boolean;
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
 * countBy takes a string, enum or boolean field, or status, and sum, min
 * and max a number or integer field. all and any may gate states of the
 * type's Workflow.
 */
export type RollupConfig =
  | (RollupSource & { readonly function: "count" })
  | (RollupSource & { readonly function: "countBy" | "sum" | "min" | "max"; readonly field: string })
  | (RollupSource & {
      readonly function: "all" | "any";
      /** The states of the type's Workflow that a transition into waits for the rollup to hold. */
      readonly gatedStates?: readonly string[];
    });

/** Search's config. */
export interface SearchConfig {
  /** The type's own top-level fields to index, by JSON key: each a string, or a scalar whose values are strings; at most 16. */
  readonly fields: readonly string[];
  /** A weight per indexed field, which multiplies that field's part of a match's score; 1 for a field it does not name. */
  readonly weights?: Readonly<Record<string, number>>;
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

/** What sets a rule off: one of enters and allTerminal. */
export type ReactionWhen =
  | {
      /** A state of the type's Workflow: the rule fires when the instance's status becomes it, by a create or a transition. */
      readonly enters: string;
    }
  | {
      /**
       * The rule fires on an instance when an instance of schema that links
       * to it through link changes or goes, and every instance that links to
       * it there is in a terminal state of its own schema's Workflow, at
       * least one.
       */
      readonly allTerminal: { readonly schema: string; readonly link: string };
    };

/** What a rule does: move the instance, or the instance its link points to, to a state through Workflow's transition. */
export interface ReactionThen {
  /** The state to move the target to. */
  readonly transition: string;
  /** A link of the type's Links config: the target is the instance it points to. The instance itself when absent. */
  readonly link?: string;
}

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
}

/** A move of the status a lease's end makes: to transition, from one of from. */
export interface LeaseTransition {
  readonly transition: string;
  readonly from: readonly string[];
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
  /** The child schema: it composes Links with parentLink, Dependencies when a step comes after another, and not Blueprint. */
  readonly schema: string;
  /** The link of the child schema's Links config that points at this schema. */
  readonly parentLink: string;
  /** A string field of the child's type that gets each step's key. */
  readonly keyField: string;
  /** Fields of this type copied to every child. */
  readonly copyFields?: readonly string[];
  /** Links of this type copied to every child, keeping a pinned one's revision; only with from. */
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

/** Where a Blueprint's steps are kept: a pinned link of the type, and the field of the linked instance that holds them. */
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
  /** The most the instance may have used and reserved together; no limit of its own without this or limitField. */
  readonly limit?: number;
  /** An integer field of the type that holds the instance's limit; not with limit. */
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
  /** The permission that changes limitsField once the instance exists; absent, the caps it was created with stay. */
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
