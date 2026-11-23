// The host loop over a store that answers at once, as
// superschematic-rules-handler's serve and run_due run it in Rust
// (runtime/engine/rust/rules-handler/src/serve.rs): each request in one
// transaction, its reads answered, its unit applied whole, run again from
// fresh state when a fence did not hold, up to the layout's maxAttempts
// times, then refused as busy. The runner runs what the change queue holds
// and the deadlines due, each a request of its own.
//
// A store's answer to a read may be the JSON text it holds (RawJson), which
// the loop splices into the module's input as it is, so a row's fields
// reach the handler as the store keeps them.

import type { Answer, Caller, Context, DueDeadline, HttpResponseJson, Policy, QueuedChange, QueuedEvent, Refusal, HandlerRequest, Step, StoreAnswer, StoreRead, Unit } from './contract.js';
import { encode, JSON_CODEC, RawJson, type Codec } from './json.js';
import type { HandlerModule } from './wasm.js';

/** The HTTP status each refusal code answers with, the engine's. */
const STATUS: Record<string, number> = {
  invalid_argument: 400,
  forbidden: 403,
  not_found: 404,
  conflict: 409,
  incompatible_change: 409,
  vetoed: 409,
  invalid_schema: 422,
  invalid_instance: 422,
  behavior_error: 500,
  internal: 500,
  busy: 503,
};

/** A refusal of a code, as the core's Refusal::new makes it (an internal one of the class Error). */
export function refusalOf(code: string, message: string): Refusal {
  return { code, status: STATUS[code] ?? 500, class: code === 'internal' ? 'Error' : 'EngineError', message };
}

/** A refusal a store or the host loop throws: a unit that cannot apply as it stands, or a store that failed. */
export class RefusalError extends Error {
  readonly refusal: Refusal;

  constructor(refusal: Refusal) {
    super(refusal.message);
    this.name = 'RefusalError';
    this.refusal = refusal;
  }
}

/**
 * What a store does for a host that drives the handler in one process,
 * the handler's Store trait: each request in one transaction, which holds
 * the store's one writer from its start. Every method answers at once.
 * A failure is a RefusalError: busy for a writer it waited on too long,
 * internal for any other.
 */
export interface Store {
  /** The deadlines due at a time, earliest first: what the runner runs. */
  due(now: number): DueDeadline[];
  /** The change queue's messages, first in first out. */
  queued(): QueuedChange[];
  /** The event queue's messages after a number, in the order written. */
  events(after: number): QueuedEvent[];
  /** Begins the request's transaction. */
  begin(): void;
  /** Commits it. */
  commit(): void;
  /** Rolls it back; nothing when none is open. */
  rollback(): void;
  /** Answers one read in the transaction: the answer, or its JSON text. */
  read(read: StoreRead): StoreAnswer | RawJson;
  /**
   * Applies a unit in the transaction: true when every write applied,
   * false, with none applied, when a fence did not hold (a row moved), and
   * a thrown RefusalError, with none applied, when the unit cannot apply as
   * it stands (a row of the id exists).
   */
  apply(unit: Unit): boolean;
}

/** What a request came to: its answer or its refusal, and, when asked for, the HTTP response the handler wrote it as. */
export type Outcome =
  | { readonly ok: true; readonly response: unknown; readonly http?: HttpResponseJson }
  | { readonly ok: false; readonly refusal: Refusal; readonly http?: HttpResponseJson };

/** What a run of a request takes beside it. */
export interface ServeOptions {
  /** The access policy the handler asks; "allowAll" unless given. */
  readonly policy?: Policy;
  /** How the module's documents are read and written; JSON's unless given. */
  readonly codec?: Codec;
  /** A routed request's answer and id, for the HTTP response the handler writes. */
  readonly respond?: { readonly answer: Answer; readonly requestId: string };
}

// A run whose unit's fence did not hold.
const MOVED = Symbol('moved');

/**
 * Runs one request to its outcome over a store, in a transaction of its
 * own: committed when its unit applies (a refused request's unit, a
 * deadline's spend, among them), rolled back otherwise.
 */
export function serve(handler: HandlerModule, store: Store, request: HandlerRequest, context: Context, options: ServeOptions = {}): Outcome {
  const codec = options.codec ?? JSON_CODEC;
  const { maxAttempts, maxReadRounds } = handler.layout();
  const head =
    `{"request":${encode(request, codec)},"context":${encode(context, codec)},"policy":${encode(options.policy ?? 'allowAll', codec)}` +
    (options.respond === undefined ? '' : `,"respond":${encode(options.respond, codec)}`);
  try {
    for (let attempt = 0; attempt < maxAttempts; attempt++) {
      store.begin();
      let outcome: Outcome | typeof MOVED;
      try {
        outcome = attemptOnce(handler, store, request, head, maxReadRounds, codec);
      } catch (error) {
        store.rollback();
        throw error;
      }
      if (outcome === MOVED) {
        store.rollback();
        continue;
      }
      store.commit();
      return outcome;
    }
  } catch (error) {
    // A store's refusal (a row of the id exists, a busy writer, a failed
    // statement) is the request's.
    if (error instanceof RefusalError) {
      return { ok: false, refusal: error.refusal };
    }
    throw error;
  }
  return {
    ok: false,
    refusal: refusalOf('busy', `${request.kind}: the rows kept moving while the request ran; it ran ${maxAttempts} times and nothing was written, so it may be tried again`),
  };
}

// attemptOnce runs a request once in the store's transaction: its outcome
// when its unit applied, MOVED when a fence did not hold.
function attemptOnce(handler: HandlerModule, store: Store, request: HandlerRequest, head: string, maxReadRounds: number, codec: Codec): Outcome | typeof MOVED {
  const answers: string[] = [];
  for (let round = 0; round < maxReadRounds; round++) {
    const step = codec.parse(handler.call('run', `${head},"answers":{"reads":[${answers.join(',')}]}}`)) as Step;
    if ('read' in step) {
      for (const read of step.read) {
        answers.push(`[${encode(read, codec)},${encode(store.read(read), codec)}]`);
      }
      continue;
    }
    const done = step.done;
    if (done.unit.writes.length > 0 && !store.apply(done.unit)) {
      return MOVED;
    }
    const http = done.http === undefined ? {} : { http: done.http };
    return 'refused' in done ? { ok: false, refusal: done.refused, ...http } : { ok: true, response: done.response, ...http };
  }
  throw new RefusalError(refusalOf('internal', `${request.kind}: the request still asked for reads after ${maxReadRounds} rounds`));
}

/** One change or deadline the runner ran, and what it came to. */
export interface Ran {
  readonly request: HandlerRequest;
  readonly outcome: Outcome;
}

/** A request's id and a fresh version 4 UUID, for each request the runner runs. */
export type Fresh = () => { request: string; id: string };

/**
 * Runs what the change queue holds, first in first out, then the
 * deadlines due at now, earliest first, then the changes those queued, and
 * on, until neither holds any or the layout's maxRuns have run: each a
 * request of its own as caller, at now.
 */
export function runDue(handler: HandlerModule, store: Store, now: number, caller: Caller, fresh: Fresh, options: ServeOptions = {}): Ran[] {
  const { maxRuns } = handler.layout();
  const ran: Ran[] = [];
  const context = (): Context => ({ caller, now, ...fresh() });
  for (;;) {
    let progressed = false;
    for (;;) {
      const first = store.queued()[0];
      if (first === undefined) {
        break;
      }
      if (ran.length >= maxRuns) {
        return ran;
      }
      const request: HandlerRequest = { kind: 'change', type: first.type, id: first.id, change: first.change, seq: first.seq };
      ran.push({ request, outcome: serve(handler, store, request, context(), options) });
      progressed = true;
    }
    for (const due of store.due(now)) {
      if (ran.length >= maxRuns) {
        return ran;
      }
      const request: HandlerRequest = { kind: 'deadline', type: due.type, id: due.id, use: due.useId, name: due.name, at: due.at, payload: due.payload };
      ran.push({ request, outcome: serve(handler, store, request, context(), options) });
      progressed = true;
    }
    if (!progressed || store.queued().length === 0) {
      return ran;
    }
  }
}
