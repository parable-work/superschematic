// Type-level checks of Approvals<T, C> (D104). Nothing imports this file:
// `bun run typecheck`, which `make ts` runs, checks it, and fails on an
// expected error (@ts-expect-error) that no longer errors.
import { behavior } from "@superschematic/schema";
import type { Approvals, ApprovalsGateOn, ApprovalsOn, Workflow } from "./index";

// The carrier imposes no members: a table implements it with its own
// fields alone, and its first type argument is the table itself. Its
// proving gate keeps its sign-offs through every edit; its done gate's
// sign-offs name the version their signer read, and a change of the proof
// consumes them.
export abstract class Task implements Approvals<Task, {
  gates: {
    proving: { count: 1; permission: "tasks.review"; resetOn: [] };
    done: { count: 1; permission: "tasks.review"; resetOn: ["proof"]; pinned: true };
  };
}> {
  id: string;
  proof?: string;
  createdBy: string;
  status: Workflow<{ states: ["working", "proving", "done"]; transitions: [] }>;
  review: Workflow<{ states: ["pending", "reviewed"]; transitions: [] }>;
}

// status names the field whose Workflow the gates are of; a gate without
// resetOn follows resetOnChange.
const reviewGates: ApprovalsOn<Task> = { gates: { reviewed: { count: 2, permission: "tasks.review", excludeCreator: false } }, resetOnChange: false, status: "review" };
const pinnedGate: ApprovalsGateOn<Task> = { count: 1, permission: "tasks.review", pinned: true };

// resetOn lists the table's fields, pinned is a boolean, status names a
// field, and a gate keeps its count and permission.
// @ts-expect-error resetOn is a list of fields
type OneResetOn = Approvals<Task, { gates: { done: { count: 1; permission: "p"; resetOn: "proof" } } }>;
// @ts-expect-error a resetOn field is the table's
type NoSuchResetOn = Approvals<Task, { gates: { done: { count: 1; permission: "p"; resetOn: ["evidence"] } } }>;
// @ts-expect-error pinned is a boolean
type PinnedYes = Approvals<Task, { gates: { done: { count: 1; permission: "p"; pinned: "yes" } } }>;
// @ts-expect-error status names a field of the table
type NoSuchStatus = Approvals<Task, { gates: { done: { count: 1; permission: "p" } }; status: "phase" }>;
// @ts-expect-error a gate needs its permission
type NoPermission = Approvals<Task, { gates: { done: { count: 1 } } }>;

// An engine type's @behavior("Approvals", ...) takes @superschematic/schema's
// config, which has none of resetOn, pinned and status.
// @ts-expect-error resetOn is a table's Approvals'
@behavior("Approvals", { gates: { a: { count: 1, permission: "p", resetOn: [] } } })
export abstract class EngineExpense {}
// @ts-expect-error pinned is a table's Approvals'
@behavior("Approvals", { gates: { a: { count: 1, permission: "p", pinned: true } } })
export abstract class EngineRelease {}
// @ts-expect-error status is a table's Approvals'
@behavior("Approvals", { gates: { a: { count: 1, permission: "p" } }, status: "s" })
export abstract class EngineDocument {}
@behavior("Approvals", { gates: { a: { count: 1, permission: "p", excludeCreator: false } }, resetOnChange: false })
export abstract class EngineMemo {}

export const checked: readonly unknown[] = [reviewGates, pinnedGate];
export type Refused = OneResetOn | NoSuchResetOn | PinnedYes | NoSuchStatus | NoPermission;
