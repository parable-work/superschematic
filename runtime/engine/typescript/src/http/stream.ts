/*
The event log as server-sent events. A stream reads the log from a cursor
in pages, as its principal, with the read's filters, and sends each event
as one message: `id:` is the event's cursor and `data:` the event as
JSON. The first time a page is the last (`more` false), replay has caught
up, and it sends a `ready` event whose id and data carry the cursor. Then
it waits for the engine's notice that a commit appended events (the
in-process notifier, complete because one process writes the file) and
reads on from its cursor. While it waits it sends a comment every
heartbeat, so proxies keep the connection open and a dead client is found.

A page's cursor moves past every event it scanned, the ones its filters
dropped too, so the stream's cursor can be ahead of the last event it
sent. A message with only `id:` moves the client's last event id up to
it: EventSource records the id and, its data being empty, dispatches
nothing (the HTML standard's dispatch steps). The stream sends one after
a write whose last event is behind its cursor, for a page of replay that
kept no event, and, while it waits, in place of a heartbeat's comment;
so a client that reconnects with Last-Event-ID scans again at most what
one heartbeat let pass. The ready event's id does the same, so a client
that started at the head and has received no event still resumes from
where it caught up, not from the head at the time it reconnects.

The stream is pulled: it reads the next page only when the server has
taken the previous one, so it queues at most one page for a slow client
and replay never loads the backlog. It ends when the client
disconnects, when the engine closes, and when a read is refused (the
policy changed, say); the client then reconnects from its last id and
gets the refusal as a problem document.
*/

import type { Principal } from '../access.js';
import type { Engine } from '../engine.js';
import { EngineError } from '../errors.js';
import type { EventPage, ReadEventsOptions } from '../events/log.js';
import { MAX_PAGE_SIZE } from '../paging.js';

export interface StreamOptions {
  /** Events read per page, 100 by default and at most 500. */
  pageSize?: number;
  /**
   * Milliseconds between comments while the stream waits, 5000 by
   * default: under the 10 seconds after which Bun.serve closes a
   * connection that sends nothing, and the shorter proxy timeouts.
   */
  heartbeatMs?: number;
}

export const DEFAULT_STREAM_PAGE_SIZE = 100;
export const DEFAULT_HEARTBEAT_MS = 5_000;

/** checkStreamOptions refuses stream options no stream could run with. */
export function checkStreamOptions(options: StreamOptions): Required<StreamOptions> {
  const pageSize = options.pageSize ?? DEFAULT_STREAM_PAGE_SIZE;
  const heartbeatMs = options.heartbeatMs ?? DEFAULT_HEARTBEAT_MS;
  if (!Number.isInteger(pageSize) || pageSize < 1 || pageSize > MAX_PAGE_SIZE) {
    throw new TypeError(`stream.pageSize must be an integer from 1 to ${MAX_PAGE_SIZE}, got ${String(pageSize)}`);
  }
  if (!Number.isInteger(heartbeatMs) || heartbeatMs < 1) {
    throw new TypeError(`stream.heartbeatMs must be a positive integer, got ${String(heartbeatMs)}`);
  }
  return { pageSize, heartbeatMs };
}

export interface EventStreamRequest {
  engine: Engine;
  principal: Principal;
  /**
   * Where to read: the namespace, the optional schema and instance and
   * the filters; `after` is the cursor to start from, or `head`.
   */
  read: Omit<ReadEventsOptions, 'limit'>;
  options: Required<StreamOptions>;
  /** Aborts when the client goes away. */
  signal: AbortSignal;
  requestId: string;
}

const encoder = new TextEncoder();

/** The name of the event a stream sends once replay has caught up. */
export const READY_EVENT = 'ready';

/**
 * eventStream reads the first page before it answers, so a refused read
 * (403, 404, 400) is a problem document rather than a stream that ends at
 * once. It returns the streaming response.
 */
export function eventStream(request: EventStreamRequest): Response {
  const { engine, principal, options, signal } = request;
  const read = (after: number | 'head'): EventPage => engine.events.read(principal, { ...request.read, after, limit: options.pageSize });
  let first: EventPage | undefined = read(request.read.after ?? 0);
  // The cursor the stream reads on from, and the one the client would
  // resume from: its last event id, or where the stream started.
  let cursor = request.read.after === 'head' ? first.next : (request.read.after ?? 0);
  let sent = cursor;
  let ready = false;

  let done = false;
  let closed = false;
  let wake: (() => void) | undefined;
  let unwatch: (() => void) | undefined;

  const teardown = () => {
    if (done) {
      return;
    }
    done = true;
    unwatch?.();
    signal.removeEventListener('abort', teardown);
    wake?.();
  };

  const body = new ReadableStream<Uint8Array>({
    start(controller) {
      unwatch = engine.events.watch({
        committed: () => wake?.(),
        closed: () => {
          closed = true;
          wake?.();
        },
      });
      signal.addEventListener('abort', teardown);
      if (signal.aborted) {
        teardown();
      }
      // A comment first, so the response starts before any event does.
      controller.enqueue(encoder.encode(': open\n\n'));
    },

    async pull(controller) {
      for (;;) {
        if (done || closed) {
          teardown();
          end(controller);
          return;
        }
        let page: EventPage;
        try {
          page = first ?? read(cursor);
        } catch (error) {
          teardown();
          if (error instanceof EngineError || closed) {
            end(controller);
          } else {
            controller.error(error);
          }
          return;
        }
        first = undefined;
        cursor = page.next;
        let frames = page.events.map((event) => `id: ${event.cursor}\ndata: ${JSON.stringify(event)}\n\n`).join('');
        if (page.events.length > 0) {
          sent = page.events[page.events.length - 1].cursor;
        }
        if (!page.more && !ready) {
          ready = true;
          sent = cursor;
          frames += `event: ${READY_EVENT}\nid: ${cursor}\ndata: ${JSON.stringify({ cursor })}\n\n`;
        } else if (cursor > sent && (frames !== '' || page.more)) {
          // Past the events the filters dropped: with the page's events,
          // or alone for a page of replay that kept none.
          sent = cursor;
          frames += `id: ${cursor}\n\n`;
        }
        if (frames !== '') {
          controller.enqueue(encoder.encode(frames));
          return;
        }
        // Caught up, with nothing to send: wait for a commit or a beat.
        const beat = await new Promise<boolean>((resolve) => {
          const timer = setTimeout(() => resolve(true), options.heartbeatMs);
          wake = () => {
            clearTimeout(timer);
            resolve(false);
          };
        });
        wake = undefined;
        if (beat && !done && !closed) {
          // A heartbeat carries the cursor when the filters dropped what
          // the stream read since its last write.
          const moved = cursor > sent;
          sent = cursor;
          controller.enqueue(encoder.encode(moved ? `id: ${cursor}\n\n` : ': keepalive\n\n'));
          return;
        }
      }
    },

    cancel() {
      teardown();
    },
  });

  return new Response(body, {
    status: 200,
    headers: {
      'content-type': 'text/event-stream',
      'cache-control': 'no-store',
      'x-request-id': request.requestId,
    },
  });
}

// Closing a stream the client already cancelled throws; either way it is over.
function end(controller: ReadableStreamDefaultController<Uint8Array>): void {
  try {
    controller.close();
  } catch {
    // Already closed.
  }
}
