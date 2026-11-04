// Type-level checks of Presence<T, C> (D104). Nothing imports this file:
// `bun run typecheck`, which `make ts` runs, checks it, and fails on an
// expected error (@ts-expect-error) that no longer errors.
import { behavior } from "@superschematic/schema";
import type { Presence, PresenceOn, Workflow } from "./index";

// The carrier imposes no members: a table implements it with its own
// fields alone, and its first type argument is the table itself. A session
// stays live three minutes after its last beat, fifteen in a turn, and its
// miss expires the device's leases on Job.
export abstract class Session implements Presence<Session, {
  ttlMs: 180000;
  ttlByState: { turn: 900000 };
  principalField: "device";
  onMissed: { transition: "missing"; from: ["idle", "turn"] };
  onBeat: { transition: "idle"; from: ["missing"] };
  releaseLeases: ["Job"];
}> {
  id: string;
  device: string;
  status: Workflow<{ states: ["idle", "turn", "missing"]; transitions: [{ from: "idle"; to: "turn" }, { from: "turn"; to: "idle" }, { from: "idle"; to: "missing" }, { from: "turn"; to: "missing" }, { from: "missing"; to: "idle" }] }>;
}

// principalField and status name fields of the table.
const turns: PresenceOn<Session> = { ttlMs: 60000, principalField: "device", status: "status", ttlByState: { turn: 600000 } };

// principalField and status name the table's fields, ttlByState's values
// are milliseconds, and sweepMs is the engine's runner's.
// @ts-expect-error principalField names a field of the table
type NoSuchPrincipal = Presence<Session, { ttlMs: 60000; principalField: "worker" }>;
// @ts-expect-error status names a field of the table
type NoSuchStatus = Presence<Session, { ttlMs: 60000; principalField: "device"; status: "phase" }>;
// @ts-expect-error a ttlByState value is a number of milliseconds
type TextTTL = Presence<Session, { ttlMs: 60000; principalField: "device"; ttlByState: { turn: "15m" } }>;
// @ts-expect-error a table's Presence takes no sweepMs
type SweepMs = Presence<Session, { ttlMs: 60000; principalField: "device"; sweepMs: 5000 }>;

// An engine type's @behavior("Presence", ...) takes @superschematic/schema's
// config, which has neither ttlByState nor status.
// @ts-expect-error ttlByState is a table's Presence's
@behavior("Presence", { ttlMs: 60000, principalField: "worker", ttlByState: { busy: 600000 } })
export abstract class EngineWorker {}
// @ts-expect-error status is a table's Presence's
@behavior("Presence", { ttlMs: 60000, principalField: "worker", status: "status" })
export abstract class EngineAgent {}
@behavior("Presence", { ttlMs: 60000, principalField: "worker", sweepMs: 5000 })
export abstract class EngineDevice {}

export const checked: readonly unknown[] = [turns];
export type Refused = NoSuchPrincipal | NoSuchStatus | TextTTL | SweepMs;
