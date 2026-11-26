/** The parts of the agent orchestration (D158), each a class the build fills. */
export type AgentsPart = "definitions" | "runs" | "requests" | "policies" | "approvals" | "controls";

/**
 * Marks a class as one of the agent orchestration's tables (D158). The
 * build adds each of its part's fields the class does not declare, so a
 * class may add its own: the controls a Budget over `budgetLimit`, the
 * runs Lease and Retries over a Workflow `status`.
 *
 * - `definitions`: an agent, a `@versioned` table: its name, purpose,
 *   instructions, role (each agent is its own principal), tools,
 *   credential references, model, provider slots and limits.
 * - `runs`: a run's record: its trigger, the definition version it ran,
 *   its scope, reads, writes, messages, approvals, cost and outcome.
 * - `requests`: the dispatcher's queue, a `@queue` class.
 * - `policies`: auto, tell or ask per agent and per operation or class.
 * - `approvals`: what an ask holds: the exact parameters and their hash.
 * - `controls`: one row per agent and one for every agent: the stop
 *   switch and the budget.
 *
 * ```ts
 * @versioned @agents("definitions") export abstract class Agent {}
 * @agents("runs") export abstract class AgentRun {}
 * @queue() @agents("requests") export abstract class AgentRunRequest {}
 * ```
 */
export function agents(_part: AgentsPart): ClassDecorator {
  return () => {};
}
