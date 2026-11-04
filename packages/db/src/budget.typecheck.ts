// Type-level checks of Budget<T, C> (D104). Nothing imports this file:
// `bun run typecheck`, which `make ts` runs, checks it, and fails on an
// expected error (@ts-expect-error) that no longer errors.
import { behavior } from "@superschematic/schema";
import type { Budget, BudgetMeterOn, BudgetOn, Relation, Workflow } from "./index";

export abstract class Workspace implements Budget<Workspace, {
  meters: {
    questions: { limit: 60; reset: "hourly" };
    tokens: { limit: 2000000; reset: "daily"; window: "rolling" };
  };
}> {
  id: string;
}

// The carrier imposes no members: a table implements it with its own
// fields alone, and its first type argument is the table itself. Its
// meters reset hourly and count a rolling day, each drawn on its
// workspace's through the to-one relation workspace.
export abstract class Account implements Budget<Account, {
  meters: {
    questions: { limit: 40; reset: "hourly"; scope: "workspace" };
    tokens: { limit: 1000000; reset: "daily"; window: "rolling"; scope: "workspace" };
  };
  limitPermission: "accounts.limits";
  observePermission: "accounts.observe";
  escalate: { transition: "out"; from: ["open"] };
}> {
  id: string;
  cap?: number;
  workspace: Relation<Workspace>;
  status: Workflow<{ states: ["open", "out"]; transitions: [{ from: "open"; to: "out" }, { from: "out"; to: "open" }] }>;
}

// A meter's limitField and scope name fields of the table, and its reset
// takes everyMs.
const capped: BudgetOn<Account> = { meters: { cpu: { limitField: "cap", reset: { everyMs: 600000 } } }, status: "status" };
const weekly: BudgetMeterOn<Account> = { limit: 10, reset: "weekly", window: "rolling", scope: "workspace" };

// A meter's fields name the table's, a reset is one of its periods, a
// window is rolling, and status names a field of the table.
// @ts-expect-error scope names a field of the table
type NoSuchScope = Budget<Account, { meters: { cpu: { scope: "team" } } }>;
// @ts-expect-error limitField names a field of the table
type NoSuchLimitField = Budget<Account, { meters: { cpu: { limitField: "limit" } } }>;
// @ts-expect-error status names a field of the table
type NoSuchStatus = Budget<Account, { meters: { cpu: { limit: 1 } }; status: "phase" }>;
// @ts-expect-error reset is hourly, daily, weekly, monthly or { everyMs }
type Yearly = Budget<Account, { meters: { cpu: { reset: "yearly" } } }>;
// @ts-expect-error window is rolling
type Fixed = Budget<Account, { meters: { cpu: { reset: "hourly"; window: "fixed" } } }>;

// An engine type's @behavior("Budget", ...) takes @superschematic/schema's
// config, whose meters reset daily or never and which has neither window nor
// observePermission.
// @ts-expect-error an engine type's meter resets daily
@behavior("Budget", { meters: { cpu: { reset: "hourly" } } })
export abstract class EngineHourly {}
// @ts-expect-error window is a table's Budget's
@behavior("Budget", { meters: { cpu: { reset: "daily", window: "rolling" } } })
export abstract class EngineRolling {}
// @ts-expect-error observePermission is a table's Budget's
@behavior("Budget", { meters: { cpu: { limit: 1 } }, observePermission: "p" })
export abstract class EngineObserve {}
@behavior("Budget", { meters: { cpu: { limit: 1, reset: "daily" } }, limitPermission: "p" })
export abstract class EngineDaily {}

export const checked: readonly unknown[] = [capped, weekly];
export type Refused = NoSuchScope | NoSuchLimitField | NoSuchStatus | Yearly | Fixed;
