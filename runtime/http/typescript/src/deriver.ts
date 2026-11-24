/*
The loop of a stack's TypeScript deriver over tables (D146, section 8.6 of
docs/stack-model.md): the process that fills the Derived fields of tables
that implement Derived (D73, D103). The behaviors' core calls no model. A
deriver, holding the permission each Derived config names, pulls the stale
fields of its tables (staleDerivations), hands each to the project's
answer, which calls a model, and settles the answers (settleDerivations)
with the hash it pulled. The deriver's generated main.ts builds a
DerivedTable from each table's repository, opened as the deriver
(orm.withBehaviorCaller), and calls runDeriver. This is the package's
./deriver entry, which imports nothing: a table's operations are its
repository's functions, so the loop is the same for every table. It is the
loop the engine's runDeriver runs over an engine's types (D74), over a
table's repository in place of an engine.

It pages each table until nothing is stale, answering a page's items
`concurrency` at a time, and settles each page in one call. An answer that
throws settles as a failure, so Derived counts the attempt. When a pass
finds nothing it waits idleMs and pulls again: a table has no commit to
wake it. A pull or a settle that fails (the database is down, a table its
migration has not made yet, a command the core refuses as busy) is
reported to onRetry and waited out, a second, doubling to thirty, as a
worker waits out a claim that failed, so a deriver may start before its
database does. The loop resolves when its signal aborts.
*/

/** How many stale fields a pull asks for unless set. */
export const DEFAULT_DERIVER_BATCH_SIZE = 50;

/** The most stale fields a pull may ask for, and a settle take. */
export const MAX_DERIVER_BATCH_SIZE = 100;

/** How many answers run at a time unless set. */
export const DEFAULT_DERIVER_CONCURRENCY = 4;

/** How long a pass that found nothing waits before it pulls again unless set. */
export const DEFAULT_DERIVER_IDLE_MS = 5_000;

/** The first wait after a pull or a settle that failed, which doubles to the longest. */
const RETRY_MIN_MS = 1_000;
const RETRY_MAX_MS = 30_000;

/** The longest message a thrown answer's failure records. */
const MAX_FAILURE = 2_000;

/** One stale derived field, as a table's staleDerivations lists it. */
export interface DerivationItem {
  /** The row's key. */
  readonly id: string;
  readonly field: string;
  /** The hash a settle presents. */
  readonly sourceHash: string;
  /** Which attempt this is, from 1. */
  readonly attempt: number;
  readonly instruction: string;
  /** The config's model label, which the project maps to a provider's model. */
  readonly model: string;
  /** The inputs that hold a value, by field name. */
  readonly inputs: Readonly<Record<string, unknown>>;
  /** The derived field's JSON Schema: what the value must be. */
  readonly output: unknown;
}

/** What the project's answer gives for an item: a value, or why there is none. */
export type DerivationAnswer =
  | {
      readonly value: unknown;
      /** How sure the model is, from 0 to 1. */
      readonly confidence?: number;
      /** What the call cost. */
      readonly usage?: { readonly inputTokens?: number; readonly outputTokens?: number; readonly costMicros?: number; readonly modelId?: string };
    }
  | { readonly failure: { readonly class?: string; readonly message: string } };

/** One answer a settle takes: the item's row, field and hash, with its value or its failure. */
export interface DerivationSettlement {
  id: string;
  field: string;
  sourceHash: string;
  value?: unknown;
  confidence?: number;
  usage?: { inputTokens?: number; outputTokens?: number; costMicros?: number; modelId?: string };
  failure?: { class?: string; message: string };
}

/** What a table's settleDerivations returns. */
export interface SettleResult {
  readonly settled: number;
  readonly failed: number;
  readonly skipped: ReadonlyArray<{ readonly id: string; readonly field: string; readonly reason: string; readonly behavior?: string; readonly code?: string }>;
}

/**
 * A table's Derived operations, as its repository has them under the
 * use's member (orm.ticket.derived), opened as the deriver.
 */
export interface DerivedTable {
  staleDerivations(params: { limit?: number; cursor?: string }): Promise<unknown>;
  settleDerivations(params: { items: DerivationSettlement[] }): Promise<SettleResult>;
}

export interface DeriverOptions {
  /** The tables whose Derived fields it fills, by name, in the order it pulls them. */
  readonly tables: Readonly<Record<string, DerivedTable>>;
  /** The project's model call for one item of a table. */
  readonly answer: (table: string, item: DerivationItem, signal: AbortSignal) => Promise<DerivationAnswer> | DerivationAnswer;
  /** How many items a pull asks for: 1 to MAX_DERIVER_BATCH_SIZE, DEFAULT_DERIVER_BATCH_SIZE unless set. */
  readonly batchSize?: number;
  /** How many answers run at a time: DEFAULT_DERIVER_CONCURRENCY unless set. */
  readonly concurrency?: number;
  /** How long a pass that found nothing waits before it pulls again: DEFAULT_DERIVER_IDLE_MS unless set. */
  readonly idleMs?: number;
  /** Stops the deriver: the answers running are aborted, and nothing more is settled. */
  readonly signal?: AbortSignal;
  /** Called after each settle, for logs. */
  readonly onSettled?: (table: string, result: SettleResult) => void;
  /** Called with a failure of a pull or a settle the loop waits out, and the wait before it tries again, for logs. */
  readonly onRetry?: (error: unknown, waitMs: number) => void;
}

/** Runs a deriver over tables until its signal aborts. */
export async function runDeriver(options: DeriverOptions): Promise<void> {
  const batchSize = Math.min(Math.max(Math.trunc(options.batchSize ?? DEFAULT_DERIVER_BATCH_SIZE), 1), MAX_DERIVER_BATCH_SIZE);
  const concurrency = Math.max(Math.trunc(options.concurrency ?? DEFAULT_DERIVER_CONCURRENCY), 1);
  const idleMs = Math.max(options.idleMs ?? DEFAULT_DERIVER_IDLE_MS, 0);
  const signal = options.signal ?? new AbortController().signal;
  let wake: (() => void) | undefined;
  const aborted = (): void => wake?.();
  signal.addEventListener('abort', aborted);
  let backoff = RETRY_MIN_MS;
  try {
    while (!signal.aborted) {
      let found = false;
      let waitMs = idleMs;
      try {
        for (const [name, table] of Object.entries(options.tables)) {
          if (signal.aborted) {
            return;
          }
          found = (await derive(options, name, table, batchSize, concurrency, signal)) || found;
        }
        backoff = RETRY_MIN_MS;
      } catch (error) {
        if (signal.aborted) {
          return;
        }
        options.onRetry?.(error, backoff);
        waitMs = backoff;
        backoff = Math.min(backoff * 2, RETRY_MAX_MS);
        found = false;
      }
      if (!found && !signal.aborted) {
        await new Promise<void>(resolve => {
          const timer = setTimeout(done, waitMs);
          function done(): void {
            clearTimeout(timer);
            wake = undefined;
            resolve();
          }
          wake = done;
        });
      }
    }
  } finally {
    signal.removeEventListener('abort', aborted);
  }
}

// derive pages one table's stale fields, answers and settles each page,
// and says whether it found any.
async function derive(options: DeriverOptions, name: string, table: DerivedTable, batchSize: number, concurrency: number, signal: AbortSignal): Promise<boolean> {
  let cursor: string | null = null;
  let found = false;
  do {
    const params: { limit: number; cursor?: string } = { limit: batchSize };
    if (cursor !== null) {
      params.cursor = cursor;
    }
    const page = (await table.staleDerivations(params)) as { items?: DerivationItem[]; next?: string | null } | null;
    const items = page?.items ?? [];
    cursor = page?.next ?? null;
    if (items.length === 0) {
      continue;
    }
    found = true;
    const answers = await answerAll(options, name, items, concurrency, signal);
    if (signal.aborted) {
      return found;
    }
    const result = await table.settleDerivations({
      items: items.map((item, i) => ({ id: item.id, field: item.field, sourceHash: item.sourceHash, ...answers[i]! })),
    });
    options.onSettled?.(name, result);
  } while (cursor !== null && !signal.aborted);
  return found;
}

// answerAll answers items, concurrency at a time, in their order; a throw
// is the item's failure.
async function answerAll(options: DeriverOptions, name: string, items: readonly DerivationItem[], concurrency: number, signal: AbortSignal): Promise<DerivationAnswer[]> {
  const out: DerivationAnswer[] = new Array(items.length);
  let next = 0;
  const lane = async (): Promise<void> => {
    while (next < items.length) {
      const i = next++;
      const item = items[i]!;
      try {
        out[i] = await options.answer(name, item, signal);
      } catch (error) {
        out[i] = { failure: { class: 'thrown', message: (error instanceof Error ? error.message : String(error)).slice(0, MAX_FAILURE) } };
      }
    }
  };
  await Promise.all(Array.from({ length: Math.min(concurrency, items.length) }, lane));
  return out;
}
