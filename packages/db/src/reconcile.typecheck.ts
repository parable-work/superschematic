// Type-level checks of Reconcile<C> (D104). Nothing imports this file:
// `bun run typecheck`, which `make ts` runs, checks it, and fails on an
// expected error (@ts-expect-error) that no longer errors.
import { behavior } from "@superschematic/schema";
import type { Reconcile, ReconcileConfig, Workflow } from "./index";

// The field's type is the observed state, or null before the first
// observation: the carrier reads its config alone, and a repository's row
// keeps string | null, as it keeps the Workflow's status string; observe's
// generated input and result narrow it to the states of the Workflow
// status names.
export abstract class Sandbox {
  id: string;
  status: Workflow<{
    states: ["pending", "running", "unreachable"];
    transitions: [
      { from: "pending"; to: "running" },
      { from: "pending"; to: "unreachable" },
      { from: "running"; to: "unreachable" },
      { from: "unreachable"; to: "pending" },
      { from: "unreachable"; to: "running" }
    ];
  }>;
  observed: Reconcile<{ status: "status"; unreachable: "unreachable"; staleAfterMs: 300000; observePermission: "sandboxes.observe" }>;
}

const unobserved: Sandbox["observed"] = null;
const running: Sandbox["observed"] = "running";
const watched: ReconcileConfig = { status: "status", observePermission: "sandboxes.observe" };

// observePermission is required, staleAfterMs is a number, and the config
// names no member but status, unreachable, staleAfterMs and
// observePermission.
// @ts-expect-error a Reconcile config names observePermission
type NoPermission = Reconcile<{ status: "status" }>;
// @ts-expect-error staleAfterMs is a number of milliseconds
type StaleText = Reconcile<{ status: "status"; unreachable: "unreachable"; staleAfterMs: "5m"; observePermission: "x" }>;
// @ts-expect-error a Reconcile config names no other member
const unknownMember: ReconcileConfig = { status: "status", observePermission: "x", provider: "box" };

// An engine type names no Reconcile: @superschematic/schema's
// BehaviorConfigs has none, since no engine implements it.
// @ts-expect-error Reconcile is a table's alone
@behavior("Reconcile", { status: "status", observePermission: "x" })
export abstract class EngineSandbox {}

export const checked: readonly unknown[] = [unobserved, running, watched, unknownMember];
export type Refused = NoPermission | StaleText;
