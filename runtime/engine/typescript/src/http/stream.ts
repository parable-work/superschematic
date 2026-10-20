/*
The event stream's options (D16): the event log as server-sent events,
which the engine writes itself, every byte (D58; runtime/engine/README.md,
"The event stream"). A stream reads the log from a cursor in pages, as
its principal, with the read's filters, and sends each event as one
message: `id:` is the event's cursor and `data:` the event as JSON. The
first time a page is the last, replay has caught up, and it sends a
`ready` event whose id and data carry the cursor. Then it waits for a
commit and reads on from its cursor, and while it waits it sends a
comment every heartbeat, so proxies keep the connection open and a dead
client is found.
*/

import { MAX_PAGE_SIZE } from '../types/paging.js';

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

/** The event a stream sends once replay has caught up, its id and data the cursor. */
export const READY_EVENT = 'ready';

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
