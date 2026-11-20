// Type-level checks of Rollups<T, C> and Dependencies<T, C> (D104's step
// 4). Nothing imports this file: `bun run typecheck`, which `make ts` runs,
// checks it, and fails on an expected error (@ts-expect-error) that no
// longer errors.
import type { Dependencies, DependenciesOn, HasMany, Relation, RollupOn, Rollups, RollupsOn } from "./index";

// The carriers impose no members: a table implements them with its own
// fields alone, and the first type argument is the table itself. A barrier
// waits for every agent that points at it to be done (D104's Barrier).
export abstract class Barrier implements
  Rollups<Barrier, {
    rollups: {
      ready: { schema: "Agent"; link: "barrier"; function: "all"; gatedStates: ["released"] };
      agents: { schema: "Agent"; link: "barrier"; function: "count" };
      byStatus: { schema: "Agent"; link: "barrier"; function: "countBy"; field: "Workflow.status" };
    };
  }>
{
  id: string;
  agents: HasMany<Agent>;
}

export abstract class Agent implements
  Dependencies<Agent, { gatedStates: ["done"]; satisfiedBy: ["success", "neutral"] }>
{
  id: string;
  barrier: Relation<Barrier>;
}

const counted: RollupOn = { schema: "Agent", link: "barrier", function: "count" };
const summed: RollupOn = { schema: "Agent", link: "barrier", function: "sum", field: "estimate" };
const every: RollupsOn<Barrier> = { rollups: { counted, summed } };
const own: DependenciesOn<Agent> = {};

// A function that reads a field names one; count takes none; gatedStates
// and outcomes are all's and any's; latest waits on a table.
// @ts-expect-error sum reads a field
const noField: RollupOn = { schema: "Agent", link: "barrier", function: "sum" };
// @ts-expect-error count takes no gatedStates
const gatedCount: RollupOn = { schema: "Agent", link: "barrier", function: "count", gatedStates: ["released"] };
// @ts-expect-error latest waits for a later step on a table
const latest: RollupOn = { schema: "Agent", link: "barrier", function: "latest", field: "name" };
// @ts-expect-error a rollup names its table and its link
type NoLink = Rollups<Barrier, { rollups: { ready: { schema: "Agent"; function: "all" } } }>;
// @ts-expect-error an outcome is success, failure or neutral
const outcome: DependenciesOn<Agent> = { satisfiedBy: ["done"] };
// @ts-expect-error a config names no member but schemas, gatedStates and satisfiedBy
const unknownMember: DependenciesOn<Agent> = { blockers: [] };

export const checked: readonly unknown[] = [counted, summed, every, own, noField, gatedCount, latest, outcome, unknownMember];
export type Refused = NoLink;
