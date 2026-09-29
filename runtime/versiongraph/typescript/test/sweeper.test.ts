// The sweeper's rules the sweep scenario cannot reach, since a scenario
// runs one step at a time: runSweeper's passes are skipped while another
// transaction holds the sweep lock and stop when its signal aborts, and a
// pass leaves an idle change set a write reached after the pass read it.
// The TypeScript counterpart of the Go engine's sweeper_test.go.
import { afterEach, beforeAll, expect, test } from "bun:test";
import {
  Engine,
  NotFoundError,
  type Edits,
  type Ref,
  type Storage,
  type SweepReport,
  type Tx,
} from "../dist/engine.js";
import { init, type VersionGraph } from "../dist/index.js";
import { PostgresAdapter, pgClient, pgPool } from "../dist/postgres.js";
import { descriptor, dsn, scratchSchema, type Scratch } from "./postgres.js";

const actor = "Cook";
let core: VersionGraph;

beforeAll(async () => {
  core = await init();
});

const scratches: Scratch[] = [];
afterEach(async () => {
  for (const scratch of scratches.splice(0)) {
    await scratch.close();
  }
});

async function open(): Promise<{ scratch: Scratch; adapter: PostgresAdapter; engine: Engine }> {
  const scratch = await scratchSchema("vg_sweeper_ts");
  scratches.push(scratch);
  const adapter = new PostgresAdapter(descriptor);
  const engine = new Engine(core, descriptor, adapter.storage(pgPool(scratch.pool)), { schemaEpoch: 1, snapshotEvery: 3 });
  return { scratch, adapter, engine };
}

test("runSweeper refuses an interval that is not positive", async () => {
  const engine = new Engine(core, descriptor, { transact: () => Promise.reject(new Error("no storage")) });
  let called = false;
  await expect(engine.runSweeper(0, { actor }, () => (called = true))).rejects.toThrow("must be positive");
  expect(called).toBe(false);
});

// A sweeper whose signal has already aborted stops with its reason before
// a pass: it opens no transaction and reports nothing.
test("runSweeper with an aborted signal runs no pass", async () => {
  let transactions = 0;
  const engine = new Engine(core, descriptor, {
    transact: () => {
      transactions++;
      return Promise.reject(new Error("no storage"));
    },
  });
  const controller = new AbortController();
  const reason = new Error("stopped");
  controller.abort(reason);
  let passes = 0;
  await expect(engine.runSweeper(3_600_000, { actor }, () => passes++, controller.signal)).rejects.toBe(reason);
  expect([transactions, passes]).toEqual([0, 0]);
});

// runSweeper runs while another transaction holds the graph's sweep lock:
// its passes are skipped until the lock is released, the next pass sweeps,
// and aborting its signal stops it with the signal's reason.
test.skipIf(dsn === "")("runSweeper skips while the lock is held", async () => {
  const { scratch, adapter, engine } = await open();
  const holder = await scratch.pool.connect();
  try {
    await holder.query("BEGIN");
    expect(await adapter.storage(pgClient(holder, { savepoint: true })).transact((tx) => tx.sweepLock())).toBe(true);

    const passes: (SweepReport | null)[] = [];
    let wake: (() => void) | undefined;
    const next = async (): Promise<SweepReport> => {
      const deadline = Date.now() + 10_000;
      while (passes.length === 0) {
        if (Date.now() > deadline) {
          throw new Error("no pass within 10s");
        }
        await new Promise<void>((resolve) => {
          wake = resolve;
          setTimeout(resolve, 50);
        });
      }
      const report = passes.shift()!;
      if (report === null) {
        throw new Error("a pass failed");
      }
      return report;
    };
    const controller = new AbortController();
    const running = engine.runSweeper(
      5,
      { actor },
      (report, err) => {
        passes.push(err === null ? report : null);
        wake?.();
      },
      controller.signal,
    );
    for (let i = 0; i < 3; i++) {
      expect((await next()).skipped).toBe(true);
    }
    await holder.query("ROLLBACK");
    while ((await next()).skipped) {
      // Passes that started before the rollback may still be skipped.
    }
    const reason = new Error("stop");
    controller.abort(reason);
    await expect(running).rejects.toBe(reason);
  } finally {
    holder.release();
  }
});

// A write lands on an idle change set after the pass read it as idle and
// before the pass discards it. The pass leaves that change set live, since
// it is no longer idle, and the rest of the pass lands: it discards the
// other idle change set and collects a discarded ref's rows.
test.skipIf(dsn === "")("a sweep skips an idle draft written during the pass", async () => {
  const { scratch, adapter, engine } = await open();
  const { pool } = scratch;
  await pool.query(
    "INSERT INTO recipe (id, title, created_by) VALUES ($1::uuid, 'Bread', $2::uuid)",
    ["00000000-0000-0000-0000-00000000012a", "00000000-0000-0000-0000-000000000001"],
  );
  const bread = "00000000-0000-0000-0000-00000000012a";
  const main = await engine.createPrimary(actor, bread, "main");
  const branch = (name: string): Promise<Ref> => engine.branch(actor, main.id, name);
  const idle = await branch("idle");
  const busy = await branch("busy");
  const dropped = await branch("dropped");
  const mix: Edits = { step: { upsert: ['{"entity_key": "Mix", "position": 1, "instruction": "Mix", "timings": {}}'] } };
  const saved = await engine.save(actor, dropped.id, dropped.version, mix);
  await engine.discard(actor, dropped.id, saved.ref.version);
  await pool.query("UPDATE recipe_ref SET updated_at = now() - interval '3 days' WHERE id = ANY($1::text[]::uuid[])", [
    [idle.id, busy.id].map(hyphenated),
  ]);
  await pool.query("UPDATE recipe_ref SET deleted_at = now() - interval '8 days' WHERE id = $1::uuid", [
    hyphenated(dropped.id),
  ]);

  // The write goes through another connection, in a transaction of its own
  // that commits while the pass's is open.
  const otherStore = adapter.storage(pgPool(pool));
  const writer = engine.withStorage(otherStore);
  let wrote = false;
  const inner = adapter.storage(pgPool(pool));
  const racing: Storage = {
    transact: (fn) =>
      inner.transact((tx) =>
        fn(
          new Proxy(tx, {
            get(target, property, receiver) {
              if (property === "idleDrafts") {
                return async (idleMs: number) => {
                  const refs = await target.idleDrafts(idleMs);
                  const ref = await otherStore.transact((other) => other.readRef(busy.id));
                  await writer.save(actor, busy.id, ref.version, mix);
                  wrote = true;
                  return refs;
                };
              }
              const value = Reflect.get(target, property, receiver) as unknown;
              return typeof value === "function" ? (value as (...args: unknown[]) => unknown).bind(target) : value;
            },
          }) as Tx,
        ),
      ),
  };
  const report = await engine.withStorage(racing).sweep({ actor, abandonAfter: 48 * 60 * 60 * 1000 });
  expect(wrote).toBe(true);
  expect([report.abandoned, report.collectedRefs, report.collectedRows.step]).toEqual([1, 1, 1]);
  await expect(engine.compose(idle.id)).rejects.toBeInstanceOf(NotFoundError);
  expect((await engine.compose(busy.id)).tree.step?.length).toBe(1);
});

function hyphenated(id: string): string {
  const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";
  let n = 0n;
  for (const c of id) {
    n = n * 62n + BigInt(alphabet.indexOf(c));
  }
  const hex = n.toString(16).padStart(32, "0");
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}
