// Type-level checks of Retries<T, C> (D104). Nothing imports this file:
// `bun run typecheck`, which `make ts` runs, checks it, and fails on an
// expected error (@ts-expect-error) that no longer errors.
import { behavior } from "@superschematic/schema";
import type { Retries, RetriesOn, RetryClassOn, Workflow } from "./index";

// The carrier imposes no members: a table implements it with its own
// fields alone, and its first type argument is the table itself. A timeout
// waits a minute, then twice as long, up to ten; then moves a waiting job
// back to queued; a build failure is retried once per revision.
export abstract class Job implements Retries<Job, {
  classes: {
    timeout: { attempts: 5; backoff: { firstMs: 60000; maxMs: 600000 } };
    build: { attempts: 1 };
    rejected: "terminal";
  };
  totalAttempts: 6;
  exhaustedState: "failed";
  retryInto: ["running"];
  then: { transition: "queued"; from: ["waiting"] };
  giveUpAfterMs: 1800000;
  window: { attempts: 4; ms: 3600000 };
  keyField: "revision";
}> {
  id: string;
  revision: number;
  caps?: Readonly<Record<string, number>>;
  status: Workflow<{ states: ["queued", "running", "waiting", "failed"]; transitions: [{ from: "queued"; to: "running" }, { from: "running"; to: "waiting" }, { from: "waiting"; to: "queued" }, { from: "running"; to: "failed" }] }>;
}

// limitsField, keyField and status name fields of the table, and a class's
// backoff takes a factor.
const capped: RetriesOn<Job> = {
  classes: { timeout: { attempts: 3, backoff: { firstMs: 1000, maxMs: 8000, factor: 3 } } },
  totalAttempts: 3,
  exhaustedState: "failed",
  limitsField: "caps",
  keyField: "revision",
  status: "status",
  retryInto: ["running"],
};
const terminal: RetryClassOn = { attempts: 1, hint: "Fix the build." };

// resultField waits for an update an operation makes; keyField names a field
// of the table; a backoff gives its longest wait.
// @ts-expect-error a table's Retries takes no resultField
type ResultField = Retries<Job, { classes: { timeout: { attempts: 1 } }; totalAttempts: 1; exhaustedState: "failed"; resultField: "result" }>;
// @ts-expect-error keyField names a field of the table
type NoSuchKeyField = Retries<Job, { classes: { timeout: { attempts: 1 } }; totalAttempts: 1; exhaustedState: "failed"; keyField: "version" }>;
// @ts-expect-error a backoff gives maxMs
type NoMaxMs = Retries<Job, { classes: { timeout: { attempts: 1; backoff: { firstMs: 1 } } }; totalAttempts: 1; exhaustedState: "failed" }>;
// @ts-expect-error then names its from states
type NoFrom = Retries<Job, { classes: { timeout: { attempts: 1 } }; totalAttempts: 1; exhaustedState: "failed"; then: { transition: "queued" } }>;

// An engine type's @behavior("Retries", ...) takes @superschematic/schema's
// config, which has none of the waits.
const engineRetries = { classes: { timeout: { attempts: 3 } }, totalAttempts: 3, exhaustedState: "failed" } as const;
// @ts-expect-error retryInto is a table's Retries'
@behavior("Retries", { ...engineRetries, retryInto: ["running"] })
export abstract class EngineRetryInto {}
// @ts-expect-error then is a table's Retries'
@behavior("Retries", { ...engineRetries, then: { transition: "queued", from: ["waiting"] } })
export abstract class EngineThen {}
// @ts-expect-error giveUpAfterMs is a table's Retries'
@behavior("Retries", { ...engineRetries, giveUpAfterMs: 60000 })
export abstract class EngineGiveUp {}
// @ts-expect-error window is a table's Retries'
@behavior("Retries", { ...engineRetries, window: { attempts: 2, ms: 60000 } })
export abstract class EngineWindow {}
// @ts-expect-error keyField is a table's Retries'
@behavior("Retries", { ...engineRetries, keyField: "revision" })
export abstract class EngineKeyField {}
// @ts-expect-error status is a table's Retries'
@behavior("Retries", { ...engineRetries, status: "status" })
export abstract class EngineStatus {}
// @ts-expect-error backoff is a table's Retries'
@behavior("Retries", { classes: { timeout: { attempts: 3, backoff: { firstMs: 1, maxMs: 2 } } }, totalAttempts: 3, exhaustedState: "failed" })
export abstract class EngineBackoff {}
@behavior("Retries", engineRetries)
export abstract class EngineJob {}

export const checked: readonly unknown[] = [capped, terminal];
export type Refused = ResultField | NoSuchKeyField | NoMaxMs | NoFrom;
