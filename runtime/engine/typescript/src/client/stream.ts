/*
The event stream (runtime/engine/README.md, "The event stream") read over
fetch. EventSource cannot send Authorization or Service-Authorization, so
the client parses the server-sent events itself, by the HTML standard's
rules: fields `event`, `data` and `id`, comments, CR, LF or CRLF line
ends, and a message with only an `id:` recording the id and dispatching
nothing, which is how the stream keeps a client's place past events its
filters dropped.

A subscription is an async iterable of messages: each event of the log,
and `ready` once per connection, when replay has caught up. Its cursor is
the id of the last message it has handed out, an id-only one included.
When the connection drops, or the stream ends because the engine closed,
it connects again with `Last-Event-ID` set to that cursor, after a
backoff, so it neither repeats nor skips an event. A refusal (a 4xx
problem, a policy change say) ends it with that problem; a 5xx, a 429 or
a network failure is retried.
*/

import { EngineProblem } from './errors.js';
import type { CallOptions, RequestSpec, Transport } from './transport.js';
import type { EngineEvent, EventKind } from './types.js';

/** The filters of an event read or stream, ANDed (runtime/engine/README.md, "The event log"). */
export interface EventFilters {
  /** Only this schema's events. */
  readonly schema?: string;
  /** Only this instance's events; needs schema. */
  readonly instanceId?: string;
  /** Only events of these kinds. */
  readonly kinds?: readonly EventKind[];
  /** Only the operation events of these behaviors. */
  readonly behaviors?: readonly string[];
  /** No operation events of operations by these names (`heartbeat`). */
  readonly exclude?: readonly string[];
}

export interface SubscribeOptions extends EventFilters, CallOptions {
  /** Where to start: after a cursor, at `head` for no replay, or at the start of the log (0) by default. */
  readonly after?: number | 'head';
  /** The backoff between reconnects; `false` ends the subscription when the connection drops. */
  readonly reconnect?: false | { readonly initialMs?: number; readonly maxMs?: number };
  /** Called before each reconnect with what ended the connection and the number of reconnects in a row. */
  readonly onReconnect?: (error: unknown, attempt: number) => void;
}

/** A message of the stream: an event of the log, or `ready` once a connection's replay has caught up. */
export type StreamMessage = { readonly type: 'event'; readonly event: EngineEvent } | { readonly type: 'ready'; readonly cursor: number };

/** The name of the event the stream sends once replay has caught up. */
export const READY_EVENT = 'ready';

export const DEFAULT_RECONNECT_INITIAL_MS = 500;
export const DEFAULT_RECONNECT_MAX_MS = 30_000;

/** One message the parser dispatched. */
export interface SseMessage {
  /** The event type: `message` when the stream named none. */
  readonly type: string;
  /** The data, with its lines joined by LF; empty for a message that only moves the last event id. */
  readonly data: string;
  /** The last event id after the message. */
  readonly lastEventId: string;
}

/** SseParser turns the stream's text, in chunks of any size, into messages, as an EventSource would. */
export class SseParser {
  private buffer = '';
  private data: string[] = [];
  private eventType = '';
  private lastEventIdBuffer = '';
  private hasData = false;
  private sawId = false;
  private started = false;
  private pendingCr = false;

  /** push parses a chunk and returns the messages it completes, in order. */
  push(chunk: string): SseMessage[] {
    let text = chunk;
    if (!this.started && text.length > 0) {
      this.started = true;
      if (text.charCodeAt(0) === 0xfeff) {
        text = text.slice(1);
      }
    }
    // A CR that ended the last chunk ended a line; an LF right after it belongs to it.
    if (this.pendingCr) {
      this.pendingCr = false;
      if (text.startsWith('\n')) {
        text = text.slice(1);
      }
    }
    this.buffer += text;
    const messages: SseMessage[] = [];
    let start = 0;
    for (let index = 0; index < this.buffer.length; index++) {
      const char = this.buffer[index];
      if (char !== '\n' && char !== '\r') {
        continue;
      }
      const line = this.buffer.slice(start, index);
      if (char === '\r') {
        if (index + 1 === this.buffer.length) {
          this.pendingCr = true;
        } else if (this.buffer[index + 1] === '\n') {
          index++;
        }
      }
      start = index + 1;
      const message = this.line(line);
      if (message !== undefined) {
        messages.push(message);
      }
    }
    this.buffer = this.buffer.slice(start);
    return messages;
  }

  private line(line: string): SseMessage | undefined {
    if (line === '') {
      return this.dispatch();
    }
    if (line.startsWith(':')) {
      return undefined;
    }
    const colon = line.indexOf(':');
    const field = colon === -1 ? line : line.slice(0, colon);
    let value = colon === -1 ? '' : line.slice(colon + 1);
    if (value.startsWith(' ')) {
      value = value.slice(1);
    }
    if (field === 'event') {
      this.eventType = value;
    } else if (field === 'data') {
      this.data.push(value);
      this.hasData = true;
    } else if (field === 'id' && !value.includes('\u0000')) {
      this.lastEventIdBuffer = value;
      this.sawId = true;
    }
    return undefined;
  }

  // The standard's dispatch: the last event id is set first, so a message
  // with an id and no data moves it and dispatches nothing.
  private dispatch(): SseMessage | undefined {
    const sawId = this.sawId;
    this.sawId = false;
    if (!this.hasData) {
      this.eventType = '';
      return sawId ? { type: '', data: '', lastEventId: this.lastEventIdBuffer } : undefined;
    }
    const message = { type: this.eventType || 'message', data: this.data.join('\n'), lastEventId: this.lastEventIdBuffer };
    this.data = [];
    this.hasData = false;
    this.eventType = '';
    return message;
  }
}

/** eventQuery is the event route's query for a read or a stream. */
export function eventQuery(filters: EventFilters, after: number | 'head' | undefined, limit?: number): Array<[string, string]> {
  const query: Array<[string, string]> = [];
  if (after !== undefined) {
    query.push(['after', String(after)]);
  }
  if (limit !== undefined) {
    query.push(['limit', String(limit)]);
  }
  if (filters.schema !== undefined) {
    query.push(['schema', filters.schema]);
  }
  if (filters.instanceId !== undefined) {
    query.push(['instanceId', filters.instanceId]);
  }
  for (const [name, values] of [
    ['kind', filters.kinds],
    ['behavior', filters.behaviors],
    ['exclude', filters.exclude],
  ] as const) {
    for (const value of values ?? []) {
      query.push([name, value]);
    }
  }
  return query;
}

/**
 * A subscription to a namespace's event stream: iterate it with `for
 * await`, once. Breaking out of the loop, `close()` or the options' signal
 * ends it.
 */
export class EventSubscription implements AsyncIterable<StreamMessage> {
  private readonly controller = new AbortController();
  private lastId: number | undefined;
  private iterated = false;

  constructor(
    private readonly transport: Transport,
    private readonly path: string,
    private readonly options: SubscribeOptions
  ) {
    const signal = options.signal;
    if (signal !== undefined) {
      if (signal.aborted) {
        this.controller.abort(signal.reason);
      } else {
        signal.addEventListener('abort', () => this.controller.abort(signal.reason), { once: true });
      }
    }
  }

  /**
   * The cursor a reconnect resumes from: the id of the last message handed
   * out, a ready's and an id-only one's included; undefined before the
   * first.
   */
  get cursor(): number | undefined {
    return this.lastId;
  }

  /** Whether it has ended, by close, the signal, a break or a refusal. */
  get closed(): boolean {
    return this.controller.signal.aborted;
  }

  /** close ends the subscription: the iteration returns, and the connection is cancelled. */
  close(): void {
    this.controller.abort();
  }

  [Symbol.asyncIterator](): AsyncIterator<StreamMessage> {
    if (this.iterated) {
      throw new TypeError('An EventSubscription is iterated once');
    }
    this.iterated = true;
    return this.messages();
  }

  private async *messages(): AsyncGenerator<StreamMessage, void, undefined> {
    const reconnect = this.options.reconnect;
    const initialMs = reconnect === false ? 0 : (reconnect?.initialMs ?? DEFAULT_RECONNECT_INITIAL_MS);
    const maxMs = reconnect === false ? 0 : (reconnect?.maxMs ?? DEFAULT_RECONNECT_MAX_MS);
    let failures = 0;
    try {
      for (;;) {
        let ended: unknown;
        try {
          const response = await this.connect();
          failures = 0;
          for await (const message of this.read(response)) {
            yield message;
          }
          ended = new StreamEnded();
        } catch (error) {
          if (this.closed) {
            return;
          }
          if (error instanceof EngineProblem && error.status < 500 && error.status !== 429) {
            throw error;
          }
          ended = error;
        }
        if (this.closed) {
          return;
        }
        if (reconnect === false) {
          if (ended instanceof StreamEnded) {
            return;
          }
          throw ended;
        }
        failures++;
        this.options.onReconnect?.(ended, failures);
        const retryAfter = ended instanceof EngineProblem && ended.retryAfter !== undefined ? ended.retryAfter * 1000 : 0;
        await this.sleep(Math.max(retryAfter, Math.min(maxMs, initialMs * 2 ** (failures - 1))));
        if (this.closed) {
          return;
        }
      }
    } finally {
      this.controller.abort();
    }
  }

  private connect(): Promise<Response> {
    const resume = this.lastId;
    const spec: RequestSpec = {
      method: 'GET',
      path: this.path,
      query: eventQuery(this.options, this.options.after),
      accept: 'text/event-stream',
      ...(resume !== undefined ? { headers: { 'last-event-id': String(resume) } } : {}),
      options: { ...(this.options.forward !== undefined ? { forward: this.options.forward } : {}), signal: this.controller.signal },
    };
    return this.transport.stream(spec);
  }

  private async *read(response: Response): AsyncGenerator<StreamMessage, void, undefined> {
    if (response.body === null) {
      return;
    }
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    const parser = new SseParser();
    const cancel = () => {
      reader.cancel().catch(() => undefined);
    };
    this.controller.signal.addEventListener('abort', cancel, { once: true });
    try {
      for (;;) {
        const { done, value } = await reader.read();
        if (done) {
          for (const message of parser.push(decoder.decode())) {
            yield* this.handOut(message);
          }
          return;
        }
        for (const message of parser.push(decoder.decode(value, { stream: true }))) {
          yield* this.handOut(message);
        }
      }
    } finally {
      this.controller.signal.removeEventListener('abort', cancel);
      cancel();
    }
  }

  // The cursor moves as each message is handed out, so a reconnect
  // resumes after the last one the caller received, not after one the
  // parser read ahead.
  private *handOut(message: SseMessage): Generator<StreamMessage, void, undefined> {
    const id = /^[0-9]{1,15}$/u.test(message.lastEventId) ? Number(message.lastEventId) : undefined;
    if (message.data === '') {
      if (id !== undefined) {
        this.lastId = id;
      }
      return;
    }
    if (message.type === 'message') {
      const event = parseJson(message.data, 'an event') as EngineEvent;
      this.lastId = id ?? event.cursor;
      yield { type: 'event', event };
    } else if (message.type === READY_EVENT) {
      const { cursor } = parseJson(message.data, 'a ready event') as { cursor: number };
      this.lastId = id ?? cursor;
      yield { type: 'ready', cursor };
    } else if (id !== undefined) {
      this.lastId = id;
    }
  }

  private sleep(ms: number): Promise<void> {
    return new Promise((resolve) => {
      const signal = this.controller.signal;
      if (signal.aborted || ms <= 0) {
        resolve();
        return;
      }
      const done = () => {
        clearTimeout(timer);
        signal.removeEventListener('abort', done);
        resolve();
      };
      const timer = setTimeout(done, ms);
      signal.addEventListener('abort', done, { once: true });
    });
  }
}

/** What ends a connection the server closed: the engine closing, or a proxy's timeout. */
export class StreamEnded extends Error {
  constructor() {
    super('the event stream ended');
    this.name = 'StreamEnded';
  }
}

function parseJson(text: string, what: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    throw new Error(`the event stream sent ${what} whose data is not JSON: ${text.slice(0, 200)}`);
  }
}
