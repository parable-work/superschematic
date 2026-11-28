// Type-level checks of behavior.ts against the behaviors' config schemas
// (behavior-config-schemas.ts, which go run ./internal/tools/behaviorconfigtypes
// writes from each declaration's configSchema). Nothing imports this file:
// `bun run typecheck`, which `make ts` runs, checks it, and fails when a
// behavior's configSchema moves and behavior.ts does not.
//
// behavior.ts is an engine type's form of each config, by hand: it names
// its parts, says in TypeScript what the schema says with if, prefixItems
// and the engine's checks, and leaves out what an engine type refuses. So
// for each behavior these hold:
//   - every config the type accepts, the schema accepts (sound);
//   - the type names no member the schema lacks, at any depth;
//   - the members of the schema the type leaves out, at any depth, are the
//     ones listed here: a table's alone, which the engine refuses on an
//     engine type (D104), or one an engine type does not read.
// A member a configSchema gains fails the last check until behavior.ts
// gives it or the list here names it.
import type {
  ApprovalsConfig,
  AssignmentConfig,
  BehaviorConfigs,
  BlueprintConfig,
  BudgetConfig,
  BudgetMeter,
  CalendarConfig,
  CommentsBlockKind,
  CommentsConfig,
  ConstantsConfig,
  DependenciesConfig,
  DerivedConfig,
  HoldsConfig,
  LeaseConfig,
  NumberingConfig,
  PaymentsConfig,
  PresenceConfig,
  ProvenanceConfig,
  ReactionsConfig,
  RetriesConfig,
  RevisionsConfig,
  RollupConfig,
  RollupsConfig,
  StateRule,
  StateRulesConfig,
  StockConfig,
  TimersConfig,
  VariantsConfig,
  WorkflowConfig,
  WorkflowOutcome,
} from "./behavior";
import type {
  ApprovalsConfigSchema,
  AssignmentConfigSchema,
  BehaviorConfigSchemas,
  BlueprintConfigSchema,
  BudgetConfigSchema,
  CalendarConfigSchema,
  CommentsConfigSchema,
  ConstantsConfigSchema,
  DependenciesConfigSchema,
  DerivedConfigSchema,
  HoldsConfigSchema,
  LeaseConfigSchema,
  NumberingConfigSchema,
  PaymentsConfigSchema,
  PresenceConfigSchema,
  ProvenanceConfigSchema,
  ReactionsConfigSchema,
  RetriesConfigSchema,
  RevisionsConfigSchema,
  RollupsConfigSchema,
  StateRulesConfigSchema,
  StockConfigSchema,
  TimersConfigSchema,
  VariantsConfigSchema,
  WorkflowConfigSchema,
} from "./behavior-config-schemas";

type Equal<A, B> = (<T>() => T extends A ? 1 : 2) extends <T>() => T extends B ? 1 : 2 ? true : false;
declare function holds<T extends true>(): void;

type Primitive = string | number | boolean | bigint | symbol | null | undefined;
type Keys<T> = T extends unknown ? keyof T : never;
type Member<T, K> = T extends unknown ? (K extends keyof T ? NonNullable<T[K]> : never) : never;
type ElementOf<T> = T extends readonly (infer E)[] ? E : never;
type Dot<P extends string> = P extends "" ? "" : `${P}.`;

/**
 * The members of S that H leaves out, at any depth, each by its path:
 * transitions[].by, rollups.${string}.exemptions. A member either side
 * types unknown is not looked into.
 */
type Omitted<H, S, P extends string = ""> = unknown extends H
  ? never
  : unknown extends S
    ? never
    :
        | ArrayOmitted<Extract<H, readonly unknown[]>, Extract<S, readonly unknown[]>, P>
        | ObjectOmitted<Exclude<H, Primitive | readonly unknown[]>, Exclude<S, Primitive | readonly unknown[]>, P>;
type ArrayOmitted<H, S, P extends string> = [H] extends [never]
  ? never
  : [S] extends [never]
    ? never
    : Omitted<ElementOf<H>, ElementOf<S>, `${P}[]`>;
type ObjectOmitted<H, S, P extends string> = [H] extends [never]
  ? never
  : [S] extends [never]
    ? never
    : {
        [K in Keys<S> & string]: K extends Keys<H> ? Omitted<Member<H, K>, Member<S, K>, `${Dot<P>}${K}`> : `${Dot<P>}${K}`;
      }[Keys<S> & string];

/** H accepts only configs S accepts. */
type Sound<H, S> = [H] extends [S] ? true : "the type accepts a config its schema refuses";
/** H names no member S lacks. */
type Known<H, S> = [Omitted<S, H>] extends [never] ? true : { notInSchema: Omitted<S, H> };
/** The members of S that H leaves out are Left. */
type LeavesOut<H, S, Left> = Equal<Omitted<H, S>, Left> extends true ? true : { leftOut: Omitted<H, S> };
/** H matches S, leaving out Left. */
type Matches<H, S, Left = never> =
  Sound<H, S> extends true ? (Known<H, S> extends true ? LeavesOut<H, S, Left> : Known<H, S>) : Sound<H, S>;

// Every behavior BehaviorConfigs lists has a config schema, and every
// behavior an engine runs is listed.
holds<Equal<keyof BehaviorConfigs, keyof BehaviorConfigSchemas>>();

// An engine type's Workflow names no by, overridePermission, repeat or
// finished (D113).
holds<Matches<WorkflowConfig, WorkflowConfigSchema, "finished" | "overridePermission" | "repeat" | "transitions[].by">>();
holds<Equal<WorkflowOutcome, ElementOf<NonNullable<DependenciesConfigSchema["satisfiedBy"]>>>>();
holds<Matches<CommentsConfig, CommentsConfigSchema>>();
holds<Equal<CommentsBlockKind, ElementOf<NonNullable<CommentsConfigSchema["blocks"]>>>>();
holds<Matches<RevisionsConfig, RevisionsConfigSchema>>();
holds<Matches<DependenciesConfig, DependenciesConfigSchema>>();
// exemptions is a table's alone.
holds<Matches<RollupsConfig, RollupsConfigSchema, `rollups.${string}.exemptions`>>();
holds<Equal<RollupConfig["function"], RollupsConfigSchema["rollups"][string]["function"]>>();
// status names one of a table's Workflows; an engine type has one, and
// the engine does not read it.
holds<Matches<ReactionsConfig, ReactionsConfigSchema, "status">>();
holds<Matches<ConstantsConfig, ConstantsConfigSchema, "oneWay">>();
holds<Matches<VariantsConfig, VariantsConfigSchema>>();
holds<Matches<StateRulesConfig, StateRulesConfigSchema, "status" | `states.${string}.by`>>();
holds<Equal<StateRule["frozen"], StateRulesConfigSchema["states"][string]["frozen"]>>();
holds<
  Matches<
    ApprovalsConfig,
    ApprovalsConfigSchema,
    | "status"
    | `gates.${string}.${
        | "callers"
        | "excludeCreatorPerson"
        | "excludeSubjectsIn"
        | "pinOn"
        | "pinned"
        | "proof"
        | "proofWithinMs"
        | "recheckPermission"
        | "requirementsFrom"
        | "resetOn"
        | "when"}`
  >
>();
holds<Matches<NumberingConfig, NumberingConfigSchema>>();
holds<Equal<NumberingConfig["reset"], NumberingConfigSchema["reset"]>>();
// As Reactions' status.
holds<Matches<TimersConfig, TimersConfigSchema, "status">>();
holds<Matches<DerivedConfig, DerivedConfigSchema>>();
holds<Matches<ProvenanceConfig, ProvenanceConfigSchema>>();
holds<Matches<LeaseConfig, LeaseConfigSchema, "status">>();
holds<Matches<AssignmentConfig, AssignmentConfigSchema>>();
holds<Matches<PresenceConfig, PresenceConfigSchema, "status" | "ttlByState">>();
holds<Matches<BlueprintConfig, BlueprintConfigSchema>>();
holds<Matches<BudgetConfig, BudgetConfigSchema, "observePermission" | "status" | `meters.${string}.window`>>();
// An engine type's meter resets daily or never: hourly, weekly, monthly
// and { everyMs } are a table's.
holds<Equal<Exclude<NonNullable<BudgetConfigSchema["meters"][string]["reset"]>, BudgetMeter["reset"]>, "hourly" | "weekly" | "monthly" | { readonly everyMs: number }>>();
holds<
  Matches<
    RetriesConfig,
    RetriesConfigSchema,
    "giveUpAfterMs" | "keyField" | "retryInto" | "status" | "then" | "window" | `classes.${string}.backoff`
  >
>();
holds<Matches<StockConfig, StockConfigSchema>>();
holds<Matches<CalendarConfig, CalendarConfigSchema>>();
// As Reactions' status: a table's, the field of its Workflow.
holds<Matches<HoldsConfig, HoldsConfigSchema, "status">>();
holds<Matches<PaymentsConfig, PaymentsConfigSchema>>();
holds<Equal<PaymentsConfig["capture"], PaymentsConfigSchema["capture"]>>();
holds<Equal<PaymentsConfig["afterRelease"], PaymentsConfigSchema["afterRelease"]>>();
holds<Equal<PaymentsConfig["require"], PaymentsConfigSchema["require"]>>();

// The checks catch what they are for: a member the schema lacks, one the
// type leaves out unlisted, and a config the schema refuses.
// @ts-expect-error the schema has no such member
holds<Matches<WorkflowConfig & { readonly guard: string }, WorkflowConfigSchema, "finished" | "overridePermission" | "repeat" | "transitions[].by">>();
// @ts-expect-error states is required, and left out unlisted
holds<Matches<Omit<WorkflowConfig, "states">, WorkflowConfigSchema, "finished" | "overridePermission" | "repeat" | "transitions[].by">>();
// @ts-expect-error a number is not a list of states
holds<Matches<Omit<WorkflowConfig, "states"> & { readonly states: number }, WorkflowConfigSchema, "finished" | "overridePermission" | "repeat" | "transitions[].by">>();
