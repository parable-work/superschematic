/*
A deriver: the loop that fills Derived's fields (D73, D74). The engine
calls no model. A deriver, holding the permission each Derived config
names, pulls the stale fields of its schemas (staleDerivations), hands each
to the project's answer, which calls a model, and settles the answers
(settleDerivations) with the hash it pulled. It imports nothing of the
server side, so it runs over an engine in process (inProcess) or over
EngineClient's instances.

It pages each schema until nothing is stale, answering a page's items
`concurrency` at a time, and settles each page in one call. When a pass
finds nothing, it waits for the source's wake (a commit, for an engine in
process) or idleMs, whichever comes first. An answer that throws settles
as a failure, so Derived counts the attempt. A pass that meets a refusal
time cures (not_found, a schema not yet published; busy; unavailable)
waits a second, doubling to thirty, and tries again, so a deriver may
start before its server deploys the document. The loop stops when its
signal aborts, or throws any other error of a pull or a settle.
*/

/** One stale derived field, as staleDerivations lists it. */
export interface DerivationItem {
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
      /** What the call cost, which the settle's event keeps. */
      readonly usage?: { readonly inputTokens?: number; readonly outputTokens?: number; readonly costMicros?: number; readonly modelId?: string };
    }
  | { readonly failure: { readonly class?: string; readonly message: string } };

/** What a deriver calls the engine through: an engine in process (inProcess), or EngineClient's instances. */
export interface DerivationSource {
  invokeSchema(schema: string, operation: string, params: Record<string, unknown>): unknown;
  /** Calls wake after a commit that may have made a field stale; returns how to stop. Without it the loop polls every idleMs. */
  watch?(wake: () => void): () => void;
}

/** What settleDerivations returns. */
export interface SettleResult {
  readonly settled: number;
  readonly failed: number;
  readonly skipped: ReadonlyArray<{ readonly id: string; readonly field: string; readonly reason: string; readonly behavior?: string; readonly code?: string }>;
}

export interface DeriverOptions {
  readonly source: DerivationSource;
  /** The engine schemas whose Derived fields it fills. */
  readonly schemas: readonly string[];
  /** The project's model call for one item. */
  readonly answer: (schema: string, item: DerivationItem, signal: AbortSignal) => Promise<DerivationAnswer> | DerivationAnswer;
  /** How many items a pull asks for: 1 to 100, 50 by default. */
  readonly batchSize?: number;
  /** How many answers run at a time: 4 by default. */
  readonly concurrency?: number;
  /** How long a pass that found nothing waits for a wake before it pulls again: 5000 by default. */
  readonly idleMs?: number;
  readonly signal?: AbortSignal;
  /** Called after each settle, for logs. */
  readonly onSettled?: (schema: string, result: SettleResult) => void;
  /** Called with a refusal the loop waits out, and the wait before it tries again, for logs. */
  readonly onRetry?: (error: unknown, waitMs: number) => void;
}

// The codes of a refusal time cures.
const RETRYABLE = new Set(['not_found', 'busy', 'unavailable']);
const RETRY_MIN_MS = 1000;
const RETRY_MAX_MS = 30_000;

/**
 * A derivation source over an engine in process (an Engine or an
 * AsyncEngine), as the principal, in the namespace given or the engine's
 * default: its commits wake the loop.
 */
export function inProcess(
  engine: {
    instances: { invokeSchema(principal: never, schema: string, operation: string, params: Record<string, unknown>, options?: { namespace?: string }): unknown };
    events: { watch(watcher: { committed(cursor: number): void }): () => void };
  },
  principal: unknown,
  options: { namespace?: string } = {}
): DerivationSource {
  const target = options.namespace === undefined ? undefined : { namespace: options.namespace };
  return {
    invokeSchema: (schema, operation, params) => engine.instances.invokeSchema(principal as never, schema, operation, params, target),
    watch: (wake) => engine.events.watch({ committed: () => wake() }),
  };
}

/** Runs a deriver until its signal aborts. */
export async function runDeriver(options: DeriverOptions): Promise<void> {
  const batchSize = Math.min(Math.max(Math.trunc(options.batchSize ?? 50), 1), 100);
  const concurrency = Math.max(Math.trunc(options.concurrency ?? 4), 1);
  const idleMs = Math.max(options.idleMs ?? 5000, 0);
  const signal = options.signal ?? new AbortController().signal;
  let woken = false;
  let wake: (() => void) | undefined;
  const stop = options.source.watch?.(() => {
    woken = true;
    wake?.();
  });
  const aborted = () => wake?.();
  signal.addEventListener('abort', aborted);
  let backoff = RETRY_MIN_MS;
  try {
    while (!signal.aborted) {
      woken = false;
      let found = false;
      let waitMs = idleMs;
      try {
        for (const schema of options.schemas) {
          if (signal.aborted) {
            return;
          }
          found = (await derive(options, schema, batchSize, concurrency, signal)) || found;
        }
        backoff = RETRY_MIN_MS;
      } catch (error) {
        if (!RETRYABLE.has(String((error as { code?: unknown } | null)?.code))) {
          throw error;
        }
        options.onRetry?.(error, backoff);
        waitMs = backoff;
        backoff = Math.min(backoff * 2, RETRY_MAX_MS);
        found = false;
        woken = false;
      }
      if (!found && !woken && !signal.aborted) {
        await new Promise<void>((resolve) => {
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
    stop?.();
  }
}

// derive pages one schema's stale fields, answers and settles each page,
// and says whether it found any.
async function derive(options: DeriverOptions, schema: string, batchSize: number, concurrency: number, signal: AbortSignal): Promise<boolean> {
  let cursor: string | null = null;
  let found = false;
  do {
    const params: Record<string, unknown> = { limit: batchSize };
    if (cursor !== null) {
      params.cursor = cursor;
    }
    const page = (await options.source.invokeSchema(schema, 'staleDerivations', params)) as { items: DerivationItem[]; next: string | null };
    cursor = page.next;
    if (page.items.length === 0) {
      continue;
    }
    found = true;
    const answers = await answerAll(options, schema, page.items, concurrency, signal);
    if (signal.aborted) {
      return found;
    }
    const items = page.items.map((item, i) => ({ id: item.id, field: item.field, sourceHash: item.sourceHash, ...answers[i] }));
    const result = (await options.source.invokeSchema(schema, 'settleDerivations', { items })) as SettleResult;
    options.onSettled?.(schema, result);
  } while (cursor !== null && !signal.aborted);
  return found;
}

// answerAll answers items, concurrency at a time, in their order; a throw
// is the item's failure.
async function answerAll(
  options: DeriverOptions,
  schema: string,
  items: readonly DerivationItem[],
  concurrency: number,
  signal: AbortSignal
): Promise<DerivationAnswer[]> {
  const out: DerivationAnswer[] = new Array(items.length);
  let next = 0;
  const lane = async (): Promise<void> => {
    while (next < items.length) {
      const i = next++;
      const item = items[i]!;
      try {
        out[i] = await options.answer(schema, item, signal);
      } catch (error) {
        out[i] = { failure: { class: 'thrown', message: (error instanceof Error ? error.message : String(error)).slice(0, 2000) } };
      }
    }
  };
  await Promise.all(Array.from({ length: Math.min(concurrency, items.length) }, lane));
  return out;
}
