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
throws settles as a failure, so Derived counts the attempt, and so does an
answer of no shape a settle takes (no value and no failure, a confidence
outside 0 to 1): the loop holds each answer to the settle's parameters
before it sends the page, so one bad answer costs its item an attempt and
never refuses the page. When a pass settles and fails nothing it waits
idleMs and pulls again: a table has no commit to wake it, and an item the
core skips without counting an attempt (another behavior's guard holds
it) would otherwise be pulled and answered back to back.

A pull or a settle that fails for a reason time cures (the database is
down, a table its migration has not made yet, a command the core refuses
as busy) is reported to onRetry and its table waited out, a second,
doubling to thirty, as a worker waits out a claim that failed, so a
deriver may start before its database does. The other tables go on
meanwhile. A failure time does not cure, a refusal of the core's other
than busy (forbidden, a caller without the config's permission, among
them) or a programming error, rejects runDeriver with the table's name, as
the engine's loop throws it, so the process ends and says why. The loop
resolves when its signal aborts, and starts no answer after that.
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

/** The longest message a failure records, the longest class, and the longest model id a usage names, as settleDerivations takes them. */
const MAX_FAILURE = 2_000;
const MAX_FAILURE_CLASS = 64;
const MAX_MODEL_ID = 200;

/** The core's refusals time does not cure: every code of its but busy. */
const PERMANENT_CODES = new Set(['invalid_schema', 'incompatible_change', 'not_found', 'invalid_argument', 'forbidden', 'invalid_instance', 'conflict', 'vetoed', 'behavior_error', 'internal']);

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
  /** The first wait of a table whose pull or settle failed, which doubles to retryMaxMs: a second, and thirty, unless set. */
  readonly retryMinMs?: number;
  readonly retryMaxMs?: number;
  /** Stops the deriver: the answers running are aborted, and nothing more is settled. */
  readonly signal?: AbortSignal;
  /** Called after each settle, for logs. */
  readonly onSettled?: (table: string, result: SettleResult) => void;
  /** Called with a failure of a pull or a settle the loop waits out, the wait before it tries that table again, and the table, for logs. */
  readonly onRetry?: (error: unknown, waitMs: number, table: string) => void;
}

/**
 * Whether a failure of a pull or a settle is one time does not cure: a
 * refusal of the core's other than busy, or a programming error. Anything
 * else, a database's or a network's error among them, is waited out.
 */
function permanent(error: unknown): boolean {
  if (error instanceof TypeError || error instanceof ReferenceError || error instanceof RangeError || error instanceof SyntaxError) return true;
  const code = (error as { code?: unknown } | null)?.code;
  return typeof code === 'string' && PERMANENT_CODES.has(code);
}

/**
 * Runs a deriver over tables until its signal aborts. It rejects on a
 * failure of a pull or a settle that time does not cure.
 */
export async function runDeriver(options: DeriverOptions): Promise<void> {
  const batchSize = Math.min(Math.max(Math.trunc(options.batchSize ?? DEFAULT_DERIVER_BATCH_SIZE), 1), MAX_DERIVER_BATCH_SIZE);
  const concurrency = Math.max(Math.trunc(options.concurrency ?? DEFAULT_DERIVER_CONCURRENCY), 1);
  const idleMs = Math.max(options.idleMs ?? DEFAULT_DERIVER_IDLE_MS, 0);
  const retryMinMs = Math.max(options.retryMinMs ?? RETRY_MIN_MS, 1);
  const retryMaxMs = Math.max(options.retryMaxMs ?? RETRY_MAX_MS, retryMinMs);
  const signal = options.signal ?? new AbortController().signal;
  let wake: (() => void) | undefined;
  const aborted = (): void => wake?.();
  signal.addEventListener('abort', aborted);
  // Each table that failed, with its wait and when it is tried again.
  const waiting = new Map<string, { backoff: number; retryAt: number }>();
  try {
    while (!signal.aborted) {
      let progressed = false;
      for (const [name, table] of Object.entries(options.tables)) {
        if (signal.aborted) {
          return;
        }
        const held = waiting.get(name);
        if (held !== undefined && held.retryAt > Date.now()) {
          continue;
        }
        try {
          progressed = (await derive(options, name, table, batchSize, concurrency, signal)) || progressed;
          waiting.delete(name);
        } catch (error) {
          if (signal.aborted) {
            return;
          }
          if (permanent(error)) {
            throw new Error(`deriver: table ${name}: ${error instanceof Error ? error.message : String(error)}`, { cause: error });
          }
          const backoff = held === undefined ? retryMinMs : Math.min(held.backoff * 2, retryMaxMs);
          waiting.set(name, { backoff, retryAt: Date.now() + backoff });
          options.onRetry?.(error, backoff, name);
        }
      }
      if (!progressed && !signal.aborted) {
        // Until the idle wait is over, or a table that failed is due again.
        let waitMs = idleMs;
        for (const { retryAt } of waiting.values()) {
          waitMs = Math.min(waitMs, Math.max(retryAt - Date.now(), 0));
        }
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
// and says whether a settle wrote a value or counted an attempt: a pass
// that only met items the core skips made no progress.
async function derive(options: DeriverOptions, name: string, table: DerivedTable, batchSize: number, concurrency: number, signal: AbortSignal): Promise<boolean> {
  let cursor: string | null = null;
  let progressed = false;
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
    const answers = await answerAll(options, name, items, concurrency, signal);
    if (signal.aborted) {
      return progressed;
    }
    const result = await table.settleDerivations({ items: items.map((item, i) => settlementOf(item, answers[i])) });
    progressed = progressed || result.settled > 0 || result.failed > 0;
    options.onSettled?.(name, result);
  } while (cursor !== null && !signal.aborted);
  return progressed;
}

// settlementOf holds an answer to what settleDerivations takes of an item,
// so that one answer of another shape fails its own item and the page
// still settles: a failure keeps its message and class, cut to their
// lengths; a value keeps a confidence from 0 to 1 and what of its usage
// is well formed; and anything else is the item's failure.
function settlementOf(item: DerivationItem, answer: unknown): DerivationSettlement {
  const base = { id: item.id, field: item.field, sourceHash: item.sourceHash };
  const refused = (message: string): DerivationSettlement => ({ ...base, failure: { class: 'invalid_answer', message: message.slice(0, MAX_FAILURE) } });
  if (typeof answer !== 'object' || answer === null) {
    return refused('the answer is no object: it holds a value, or a failure with a message');
  }
  const given = answer as Record<string, unknown>;
  if (given.failure !== undefined) {
    const failure = given.failure as Record<string, unknown> | null;
    if (typeof failure !== 'object' || failure === null || typeof failure.message !== 'string') {
      return refused("the answer's failure has no message");
    }
    const out: { class?: string; message: string } = { message: failure.message.slice(0, MAX_FAILURE) };
    if (typeof failure.class === 'string') out.class = failure.class.slice(0, MAX_FAILURE_CLASS);
    return { ...base, failure: out };
  }
  if (given.value === undefined) {
    return refused('the answer holds neither a value nor a failure');
  }
  const out: DerivationSettlement = { ...base, value: given.value };
  if (given.confidence !== undefined) {
    const confidence = given.confidence;
    if (typeof confidence !== 'number' || !(confidence >= 0 && confidence <= 1)) {
      return refused(`the answer's confidence, ${String(confidence)}, is no number from 0 to 1`);
    }
    out.confidence = confidence;
  }
  // What a call cost is kept for reports: a member of another shape is
  // left out, and a count is a whole number.
  if (typeof given.usage === 'object' && given.usage !== null) {
    const usage = given.usage as Record<string, unknown>;
    const kept: NonNullable<DerivationSettlement['usage']> = {};
    for (const member of ['inputTokens', 'outputTokens', 'costMicros'] as const) {
      const count = usage[member];
      if (typeof count === 'number' && Number.isFinite(count) && count >= 0) kept[member] = Math.round(count);
    }
    if (typeof usage.modelId === 'string') kept.modelId = usage.modelId.slice(0, MAX_MODEL_ID);
    if (Object.keys(kept).length > 0) out.usage = kept;
  }
  return out;
}

// answerAll answers items, concurrency at a time, in their order; a throw
// is the item's failure. Once the signal aborts it starts no answer more,
// and the items it did not reach hold none.
async function answerAll(options: DeriverOptions, name: string, items: readonly DerivationItem[], concurrency: number, signal: AbortSignal): Promise<unknown[]> {
  const out: unknown[] = new Array(items.length);
  let next = 0;
  const lane = async (): Promise<void> => {
    while (next < items.length && !signal.aborted) {
      const i = next++;
      const item = items[i]!;
      try {
        out[i] = await options.answer(name, item, signal);
      } catch (error) {
        out[i] = { failure: { class: 'thrown', message: error instanceof Error ? error.message : String(error) } };
      }
    }
  };
  await Promise.all(Array.from({ length: Math.min(concurrency, items.length) }, lane));
  return out;
}
