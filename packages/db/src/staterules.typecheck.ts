// Type-level checks of StateRules<T, C> (D104). Nothing imports this file:
// `bun run typecheck`, which `make ts` runs, checks it, and fails on an
// expected error (@ts-expect-error) that no longer errors.
import { behavior } from "@superschematic/schema";
import type { StateRuleOn, StateRules, StateRulesOn, Workflow } from "./index";

// The carrier imposes no members: a table implements it with its own
// fields alone, and its first type argument is the table itself. Its plan
// is frozen while it is planned for all but its creator, and while it is
// working for all but its reviewer; it enters proving only with a proof.
export abstract class Task implements StateRules<Task, {
  states: {
    planned: { frozen: ["plan"]; by: ["createdBy"] };
    working: { frozen: ["plan"]; by: ["reviewer"] };
    proving: { required: ["proof"] };
    done: { frozen: "all" };
  };
  permission: "tasks.admin";
}> {
  id: string;
  plan?: string;
  proof?: string;
  reviewer?: string;
  createdBy: string;
  status: Workflow<{ states: ["planned", "working", "proving", "done"]; transitions: [] }>;
  review: Workflow<{ states: ["pending", "reviewed"]; transitions: [] }>;
}

// status names the field whose Workflow the states are of.
const reviewRules: StateRulesOn<Task> = { states: { pending: { frozen: ["plan"] } }, status: "review" };
const byReviewer: StateRuleOn<Task> = { frozen: ["plan", "proof"], by: ["reviewer"] };

// The fields are the table's, by is a list, and status names a field.
// @ts-expect-error a required field is the table's
type NoSuchRequired = StateRules<Task, { states: { proving: { required: ["evidence"] } } }>;
// @ts-expect-error a by field is the table's
type NoSuchBy = StateRules<Task, { states: { planned: { frozen: ["plan"]; by: ["assignee"] } } }>;
// @ts-expect-error by is a list of fields
type OneBy = StateRules<Task, { states: { planned: { frozen: ["plan"]; by: "reviewer" } } }>;
// @ts-expect-error status names a field of the table
type NoSuchStatus = StateRules<Task, { states: { planned: { frozen: ["plan"] } }; status: "phase" }>;
// @ts-expect-error frozen is a list of fields or "all"
type FrozenSome = StateRules<Task, { states: { planned: { frozen: "some" } } }>;

// An engine type's @behavior("StateRules", ...) takes @superschematic/schema's
// config, which has neither by nor status.
// @ts-expect-error by is a table's StateRules'
@behavior("StateRules", { states: { a: { frozen: ["x"], by: ["y"] } } })
export abstract class EngineStep {}
// @ts-expect-error status is a table's StateRules'
@behavior("StateRules", { states: { a: { frozen: ["x"] } }, status: "s" })
export abstract class EngineNote {}
@behavior("StateRules", { states: { a: { required: ["x"], frozen: "all" } }, permission: "notes.admin" })
export abstract class EngineMemo {}

export const checked: readonly unknown[] = [reviewRules, byReviewer];
export type Refused = NoSuchRequired | NoSuchBy | OneBy | NoSuchStatus | FrozenSome;
