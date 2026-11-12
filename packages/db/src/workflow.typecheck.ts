// Type-level checks of Workflow<C> (D103). Nothing imports this file:
// `bun run typecheck`, which `make ts` runs, checks it, and fails on an
// expected error (@ts-expect-error) that no longer errors.
import { behavior } from "@superschematic/schema";
import type { Workflow, WorkflowConfig, WorkflowTransitionOn } from "./index";

type OrderStatus = Workflow<{
  states: ["draft", "paid", "shipped"];
  initial: "draft";
  transitions: [{ from: "draft"; to: "paid" }, { from: "paid"; to: "shipped"; permission: "orders.ship" }];
}>;

// A field's type is the union of its config's states.
export abstract class Order {
  status: OrderStatus;
}
const paid: OrderStatus = "paid";
// @ts-expect-error a state the config does not list
const lost: OrderStatus = "lost";

// A config read with typeof from a constant declared as const.
const shipmentFlow = {
  states: ["pending", "sent"],
  transitions: [{ from: "pending", to: "sent" }],
  outcomes: { sent: "success" },
} as const;
const sent: Workflow<typeof shipmentFlow> = "sent";

// The config holds Workflow's shape: states is a list, and a transition
// names where it goes.
// @ts-expect-error states is a list of states
type OneState = Workflow<{ states: "draft"; transitions: [] }>;
// @ts-expect-error a transition names its to
type NoTo = Workflow<{ states: ["draft"]; transitions: [{ from: "draft" }] }>;
// @ts-expect-error an outcome is success, failure or neutral
type BadOutcome = Workflow<{ states: ["draft"]; transitions: []; outcomes: { draft: "lost" } }>;

// A table's transition may list states in from and in to (D109): one
// transition from each to each, a state of both moving to the others.
type IssueStatus = Workflow<{
  states: ["backlog", "todo", "started", "done", "canceled"];
  transitions: [
    { from: ["backlog", "todo", "started"]; to: ["backlog", "todo", "started"] },
    { from: ["backlog", "todo", "started"]; to: ["done", "canceled"]; permission: "issues.close" },
    { from: "done"; to: "todo" },
  ];
}>;
const started: IssueStatus = "started";
// @ts-expect-error a list holds states by name
type NumberedFrom = Workflow<{ states: ["open", "done"]; transitions: [{ from: [1]; to: "done" }] }>;

// A table's Workflow names who makes a transition (by), the permission that
// moves a caller past by (overridePermission), and a move to the state the
// row is in answered as no change (repeat) (D104).
type QuestionStatus = Workflow<{
  states: ["open", "answered", "withdrawn", "closed"];
  transitions: [
    { from: "open"; to: "answered"; by: ["askedOf"] },
    { from: "open"; to: "withdrawn"; by: ["createdBy"] },
    { from: "answered"; to: "closed"; by: ["askedOf", "createdBy"]; permission: "questions.close" }
  ];
  overridePermission: "questions.moderate";
  repeat: "unchanged";
}>;
const answered: QuestionStatus = "answered";
const withdraw: WorkflowTransitionOn = { from: "open", to: "withdrawn", by: ["createdBy"] };
const moderated: WorkflowConfig = { states: ["open"], transitions: [], overridePermission: "questions.moderate" };

// A table's Workflow names the outcome of a state a transition leaves in
// finished (D113): a task that is done may be reopened and still counts as
// done for the rules that read an outcome.
type TaskStatus = Workflow<{
  states: ["todo", "doing", "done", "dropped"];
  transitions: [{ from: "todo"; to: "doing" }, { from: "doing"; to: ["done", "dropped"] }, { from: "done"; to: "todo" }];
  outcomes: { dropped: "neutral" };
  finished: { done: "success" };
}>;
const done: TaskStatus = "done";
// @ts-expect-error a finished state's outcome is success, failure or neutral
type BadFinished = Workflow<{ states: ["todo", "done"]; transitions: [{ from: "done"; to: "todo" }]; finished: { done: "closed" } }>;

// by is a list of fields, and repeat is unchanged alone.
// @ts-expect-error by lists fields
type OneBy = Workflow<{ states: ["open", "done"]; transitions: [{ from: "open"; to: "done"; by: "assignee" }] }>;
// @ts-expect-error repeat is "unchanged"
type AlwaysRepeat = Workflow<{ states: ["open"]; transitions: []; repeat: "always" }>;
// @ts-expect-error overridePermission is a permission's name
type NumberOverride = Workflow<{ states: ["open"]; transitions: []; overridePermission: 5 }>;

// An engine type's @behavior("Workflow", ...) takes @superschematic/schema's
// config, which has none of by, overridePermission, repeat and finished.
// @ts-expect-error by is a table's Workflow's
@behavior("Workflow", { states: ["open", "done"], transitions: [{ from: "open", to: "done", by: ["assignee"] }] })
export abstract class EngineRequest {}
// @ts-expect-error repeat is a table's Workflow's
@behavior("Workflow", { states: ["open"], transitions: [], repeat: "unchanged" })
export abstract class EngineTicket {}
// @ts-expect-error overridePermission is a table's Workflow's
@behavior("Workflow", { states: ["open"], transitions: [], overridePermission: "tickets.moderate" })
export abstract class EngineNote {}
// @ts-expect-error finished is a table's Workflow's
@behavior("Workflow", { states: ["open", "done"], transitions: [{ from: "done", to: "open" }], finished: { done: "success" } })
export abstract class EngineTask {}

export const checked: readonly unknown[] = [paid, lost, sent, answered, withdraw, moderated, done];
export type Refused = OneState | NoTo | BadOutcome | BadFinished | OneBy | AlwaysRepeat | NumberOverride;
