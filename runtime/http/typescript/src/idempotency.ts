import type { SqlDatabase, SqlValue } from './d1.js';
import { HttpProblem } from './problem.js';
import { postgresPlaceholders, quoteIdentifier as quote } from './sql-text.js';

/*
Idempotency keys (D140). An operation declared @idempotent answers a
repeated call what the first one answered: a caller sends
`Idempotency-Key`, the route keeps a row per operation, principal and key
in the reserved table of the API's database, and a second request under
the key gets the stored answer with `Idempotent-Replay: true`, never a
second run.

Before the implementation the route inserts a `running` row. If it
inserted, the implementation runs: an answer under 500 is stored as
`done`, and one of 500 or more deletes the row, so a retry runs again. A
`done` row with the same request hash is replayed; another hash is 422
`idempotency_key_reused`; a `running` row younger than the route's time
limit is 409 `idempotency_in_flight` with Retry-After, and an older one is
taken over. The row is not in the implementation's transaction: a crash
after the implementation commits and before the answer is stored leaves
`running`, and a repeat after the time limit runs the implementation
again. It is once per key but for that window.

The Go runtime's idempotency package keeps the same rows and decides each
request alike (runtime/http/testdata/idempotency_parity.json).
*/

/** The request header that carries the key. */
export const IDEMPOTENCY_KEY_HEADER = 'Idempotency-Key';
/** The response header of a stored answer. */
export const IDEMPOTENT_REPLAY_HEADER = 'Idempotent-Replay';
/** How long a key's answer is kept when @idempotent names no ttl: 24 hours. */
export const DEFAULT_IDEMPOTENCY_TTL_SECONDS = 24 * 60 * 60;
/** How long a running row holds its key on a route with no @timeout, in seconds. */
export const DEFAULT_IDEMPOTENCY_IN_FLIGHT_SECONDS = 60;
/** How often a server sweeps expired rows, at most, in milliseconds, and how many it deletes. */
export const IDEMPOTENCY_SWEEP_INTERVAL_MS = 60_000;
export const IDEMPOTENCY_SWEEP_LIMIT = 100;

/** The problem codes of the step's refusals. */
export const IdempotencyCode = {
  /** 400: the operation requires a key and the request sent none. */
  REQUIRED: 'idempotency_key_required',
  /** 400: the key is not 1 to 255 printable ASCII characters. */
  INVALID: 'idempotency_key_invalid',
  /** 422: the key was used for another request. */
  REUSED: 'idempotency_key_reused',
  /** 409: the first request under the key is still running. */
  IN_FLIGHT: 'idempotency_in_flight',
} as const;

/** An operation's @idempotent, as its OperationSpec carries it. */
export interface OperationIdempotency {
  /** How long a key's answer is kept, in seconds. */
  readonly ttlSeconds: number;
  /** Whether a request without the header is refused; absent runs it as any other. */
  readonly required?: boolean;
}

/** Whether key is one a caller may send: 1 to 255 printable ASCII characters, space among them. */
export function isIdempotencyKey(key: string): boolean {
  return /^[ -~]{1,255}$/u.test(key);
}

function hex(bytes: Uint8Array): string {
  return Array.from(bytes, byte => byte.toString(16).padStart(2, '0')).join('');
}

async function sha256Hex(bytes: Uint8Array): Promise<string> {
  return hex(new Uint8Array(await crypto.subtle.digest('SHA-256', bytes as Uint8Array<ArrayBuffer>)));
}

function concat(parts: readonly Uint8Array[]): Uint8Array {
  const whole = new Uint8Array(parts.reduce((size, part) => size + part.byteLength, 0));
  let offset = 0;
  for (const part of parts) {
    whole.set(part, offset);
    offset += part.byteLength;
  }
  return whole;
}

/**
 * What names a row: the SHA-256 of the operation, the principal and the
 * key, each as its length in UTF-8 bytes, a colon and its bytes, so no two
 * triples share a name. A key is one caller's, on one operation: another
 * caller's same key is another row.
 */
export function idempotencyLookup(operation: string, principal: string, key: string): Promise<string> {
  const encoder = new TextEncoder();
  const parts: Uint8Array[] = [];
  for (const text of [operation, principal, key]) {
    const bytes = encoder.encode(text);
    parts.push(encoder.encode(`${bytes.byteLength}:`), bytes);
  }
  return sha256Hex(concat(parts));
}

/**
 * What a request is, for a key to be reused only with the same one: the
 * SHA-256 of its method, a line feed, its path with its query, a line
 * feed, and its body's bytes.
 */
export function idempotencyRequestHash(method: string, pathAndQuery: string, body: Uint8Array): Promise<string> {
  return sha256Hex(concat([new TextEncoder().encode(`${method.toUpperCase()}\n${pathAndQuery}\n`), body]));
}

/** One request under a key, as begin takes it. Times are milliseconds since the epoch. */
export interface IdempotencyAttempt {
  readonly lookup: string;
  readonly operation: string;
  readonly principal: string;
  readonly key: string;
  readonly requestHash: string;
  readonly now: number;
  /** When the key's row expires. */
  readonly expiresAt: number;
  /** A running row that started before this is taken over. */
  readonly takeOverBefore: number;
}

/** What begin decides. */
export type IdempotencyBegin =
  /** The row is this request's: the implementation runs. */
  | { readonly kind: 'started' }
  /** A done row with the same hash: its answer. */
  | { readonly kind: 'replay'; readonly status: number; readonly body: string }
  /** A row with another hash. */
  | { readonly kind: 'mismatch' }
  /** A running row with the same hash, too young to take over: when it started. */
  | { readonly kind: 'running'; readonly startedAt: number };

/**
 * Where the keys are kept. finish and abandon name the attempt by when it
 * started, so an attempt whose row another took over changes nothing.
 */
export interface IdempotencyStore {
  begin(attempt: IdempotencyAttempt): Promise<IdempotencyBegin>;
  /** Stores the answer of the attempt that started at startedAt, as done. */
  finish(lookup: string, startedAt: number, status: number, body: string): Promise<void>;
  /** Deletes the running row of the attempt that started at startedAt, so a retry runs again. */
  abandon(lookup: string, startedAt: number): Promise<void>;
  /** Deletes up to limit rows that expired at or before now, and answers how many. */
  sweep(now: number, limit: number): Promise<number>;
}

/**
 * The reserved table's name and its columns' (D140), which the build
 * reads from the DB schema's `implements IdempotencyKeys` class and its
 * entrypoint hands the store.
 */
export interface IdempotencyTable {
  readonly table: string;
  readonly columns: {
    readonly lookup: string;
    readonly operation: string;
    readonly principal: string;
    readonly key: string;
    readonly requestHash: string;
    readonly state: string;
    readonly status: string;
    readonly body: string;
    readonly startedAt: string;
    readonly expiresAt: string;
  };
}

/** One statement's rows and how many rows it changed. Placeholders are ?, in order. */
export type IdempotencySql = (sql: string, values: readonly SqlValue[]) => Promise<{ readonly rows: readonly Record<string, unknown>[]; readonly changes: number }>;

/** How often begin starts over when another request changes the row under it. */
const BEGIN_ATTEMPTS = 5;

/** The store over one way to run a statement, which each dialect's store supplies. */
export function sqlIdempotencyStore(run: IdempotencySql, names: IdempotencyTable): IdempotencyStore {
  const table = quote(names.table);
  const column = Object.fromEntries(Object.entries(names.columns).map(([member, name]) => [member, quote(name)])) as Record<keyof IdempotencyTable['columns'], string>;
  return {
    async begin(attempt) {
      for (let tries = 0; tries < BEGIN_ATTEMPTS; tries++) {
        const inserted = await run(
          `INSERT INTO ${table} (${column.lookup}, ${column.operation}, ${column.principal}, ${column.key}, ${column.requestHash}, ${column.state}, ${column.startedAt}, ${column.expiresAt}) VALUES (?, ?, ?, ?, ?, 'running', ?, ?) ON CONFLICT (${column.lookup}) DO NOTHING`,
          [attempt.lookup, attempt.operation, attempt.principal, attempt.key, attempt.requestHash, attempt.now, attempt.expiresAt]
        );
        if (inserted.changes === 1) return { kind: 'started' };
        const found = await run(
          `SELECT ${column.requestHash} AS request_hash, ${column.state} AS state, ${column.status} AS status, ${column.body} AS body, ${column.startedAt} AS started_at, ${column.expiresAt} AS expires_at FROM ${table} WHERE ${column.lookup} = ?`,
          [attempt.lookup]
        );
        const row = found.rows[0];
        // Swept or abandoned since the insert found it: insert again.
        if (row === undefined) continue;
        const startedAt = Number(row.started_at);
        const expired = Number(row.expires_at) <= attempt.now;
        const stale = row.state === 'running' && startedAt < attempt.takeOverBefore;
        if (!expired && row.request_hash !== attempt.requestHash) return { kind: 'mismatch' };
        if (!expired && row.state === 'done') return { kind: 'replay', status: Number(row.status), body: String(row.body ?? '') };
        if (!expired && !stale) return { kind: 'running', startedAt };
        // An expired row, or a running one past the time limit, becomes
        // this request's, unless another request changed it first.
        const taken = await run(
          `UPDATE ${table} SET ${column.requestHash} = ?, ${column.state} = 'running', ${column.status} = NULL, ${column.body} = NULL, ${column.startedAt} = ?, ${column.expiresAt} = ? WHERE ${column.lookup} = ? AND ${column.startedAt} = ? AND ${column.state} = ?`,
          [attempt.requestHash, attempt.now, attempt.expiresAt, attempt.lookup, startedAt, String(row.state)]
        );
        if (taken.changes === 1) return { kind: 'started' };
      }
      // Every try lost its row to another request: one of them is running.
      return { kind: 'running', startedAt: attempt.now };
    },
    async finish(lookup, startedAt, status, body) {
      await run(`UPDATE ${table} SET ${column.state} = 'done', ${column.status} = ?, ${column.body} = ? WHERE ${column.lookup} = ? AND ${column.startedAt} = ? AND ${column.state} = 'running'`, [
        status,
        body,
        lookup,
        startedAt,
      ]);
    },
    async abandon(lookup, startedAt) {
      await run(`DELETE FROM ${table} WHERE ${column.lookup} = ? AND ${column.startedAt} = ? AND ${column.state} = 'running'`, [lookup, startedAt]);
    },
    async sweep(now, limit) {
      const swept = await run(
        `DELETE FROM ${table} WHERE ${column.lookup} IN (SELECT ${column.lookup} FROM ${table} WHERE ${column.expiresAt} <= ? ORDER BY ${column.expiresAt} LIMIT ?)`,
        [now, limit]
      );
      return swept.changes;
    },
  };
}

/** What the Postgres store calls: a pg Pool, or a client of one. */
export interface IdempotencyPgQueryable {
  query(config: { text: string; values: unknown[] }): Promise<{ rows: Record<string, unknown>[]; rowCount: number | null }>;
}

/** The store on Postgres, over a pg Pool. */
export function postgresIdempotencyStore(pool: IdempotencyPgQueryable, names: IdempotencyTable): IdempotencyStore {
  return sqlIdempotencyStore(async (sql, values) => {
    const result = await pool.query({ text: postgresPlaceholders(sql), values: [...values] });
    return { rows: result.rows, changes: result.rowCount ?? 0 };
  }, names);
}

/** The store on SQLite, or on a D1 binding: a SqlDatabase. */
export function sqliteIdempotencyStore(db: SqlDatabase, names: IdempotencyTable): IdempotencyStore {
  return sqlIdempotencyStore(async (sql, values) => {
    const result = await db
      .prepare(sql)
      .bind(...values)
      .all();
    return { rows: result.results, changes: result.meta.changes };
  }, names);
}

/** An in-memory store, for a test or one process: it keeps no key past a restart, and shares none between replicas. */
export function memoryIdempotencyStore(): IdempotencyStore {
  interface Row {
    requestHash: string;
    state: 'running' | 'done';
    status?: number;
    body?: string;
    startedAt: number;
    expiresAt: number;
  }
  const rows = new Map<string, Row>();
  return {
    async begin(attempt) {
      const row = rows.get(attempt.lookup);
      const fresh = (): IdempotencyBegin => {
        rows.set(attempt.lookup, { requestHash: attempt.requestHash, state: 'running', startedAt: attempt.now, expiresAt: attempt.expiresAt });
        return { kind: 'started' };
      };
      if (row === undefined || row.expiresAt <= attempt.now) return fresh();
      if (row.requestHash !== attempt.requestHash) return { kind: 'mismatch' };
      if (row.state === 'done') return { kind: 'replay', status: row.status ?? 200, body: row.body ?? '' };
      if (row.startedAt < attempt.takeOverBefore) return fresh();
      return { kind: 'running', startedAt: row.startedAt };
    },
    async finish(lookup, startedAt, status, body) {
      const row = rows.get(lookup);
      if (row && row.state === 'running' && row.startedAt === startedAt) rows.set(lookup, { ...row, state: 'done', status, body });
    },
    async abandon(lookup, startedAt) {
      const row = rows.get(lookup);
      if (row && row.state === 'running' && row.startedAt === startedAt) rows.delete(lookup);
    },
    async sweep(now, limit) {
      const expired = [...rows.entries()]
        .filter(([, row]) => row.expiresAt <= now)
        .sort(([, a], [, b]) => a.expiresAt - b.expiresAt)
        .slice(0, limit);
      for (const [lookup] of expired) rows.delete(lookup);
      return expired.length;
    },
  };
}

/** What the step reads of a request. */
export interface IdempotentRequest {
  /** The operation, `<namespace>.<name>`. */
  readonly operation: string;
  /** The caller the key belongs to; '' for a request with none. */
  readonly principal: string;
  readonly method: string;
  /** The request's path with its query. */
  readonly pathAndQuery: string;
  readonly headers: Headers;
  /** Reads the request's body, whole, without consuming what the implementation reads. */
  readonly body: () => Promise<Uint8Array>;
  readonly requestId: string;
  /** The route's time limit in seconds, absent for a route with none. */
  readonly timeoutSeconds?: number;
}

/** When each store was last swept, so a server sweeps one at most once a minute. */
const lastSweep = new WeakMap<IdempotencyStore, number>();

function sweepLater(store: IdempotencyStore, now: number): void {
  if (now - (lastSweep.get(store) ?? 0) < IDEMPOTENCY_SWEEP_INTERVAL_MS) return;
  lastSweep.set(store, now);
  void store.sweep(now, IDEMPOTENCY_SWEEP_LIMIT).catch(() => undefined);
}

/** The stored answer, as the route first gave it, marked a replay. */
function replayResponse(status: number, body: string, requestId: string): Response {
  return new Response(status === 204 || status === 304 ? null : body, {
    status,
    headers: {
      'content-type': status >= 400 ? 'application/problem+json' : 'application/json',
      'cache-control': 'no-store',
      'idempotent-replay': 'true',
      'x-request-id': requestId,
    },
  });
}

/**
 * Runs an @idempotent operation's work under the request's key (D140):
 * work, which never throws, answers the request, and the step stores what
 * it answered or answers what an earlier request stored. now is the clock,
 * milliseconds since the epoch. Refusals throw HttpProblem.
 */
export async function runIdempotent(
  request: IdempotentRequest,
  config: OperationIdempotency,
  store: IdempotencyStore,
  work: () => Promise<Response>,
  now: () => number = Date.now
): Promise<Response> {
  const key = request.headers.get(IDEMPOTENCY_KEY_HEADER);
  if (key === null) {
    if (config.required) throw new HttpProblem(400, `${request.operation} needs an ${IDEMPOTENCY_KEY_HEADER} header`, { code: IdempotencyCode.REQUIRED });
    return work();
  }
  if (!isIdempotencyKey(key)) {
    throw new HttpProblem(400, `${IDEMPOTENCY_KEY_HEADER} is 1 to 255 printable ASCII characters`, { code: IdempotencyCode.INVALID });
  }
  const [lookup, requestHash] = await Promise.all([
    idempotencyLookup(request.operation, request.principal, key),
    request.body().then(body => idempotencyRequestHash(request.method, request.pathAndQuery, body)),
  ]);
  const startedAt = now();
  const inFlightMs = (request.timeoutSeconds ?? DEFAULT_IDEMPOTENCY_IN_FLIGHT_SECONDS) * 1000;
  const begin = await store.begin({
    lookup,
    operation: request.operation,
    principal: request.principal,
    key,
    requestHash,
    now: startedAt,
    // A row expires no sooner than its in-flight limit, so a ttl shorter
    // than the route's time limit never lets a repeat take over a first
    // request that may still be running, nor a sweep delete its row.
    expiresAt: startedAt + Math.max(config.ttlSeconds * 1000, inFlightMs),
    takeOverBefore: startedAt - inFlightMs,
  });
  switch (begin.kind) {
    case 'replay':
      return replayResponse(begin.status, begin.body, request.requestId);
    case 'mismatch':
      throw new HttpProblem(422, `This ${IDEMPOTENCY_KEY_HEADER} was used for another request`, { code: IdempotencyCode.REUSED });
    case 'running': {
      const retryAfter = Math.max(1, Math.ceil((begin.startedAt + inFlightMs - startedAt) / 1000));
      throw new HttpProblem(409, `The first request under this ${IDEMPOTENCY_KEY_HEADER} is still running`, {
        code: IdempotencyCode.IN_FLIGHT,
        details: { retryAfterSeconds: retryAfter },
        headers: { 'retry-after': String(retryAfter) },
      });
    }
    case 'started':
      break;
  }
  sweepLater(store, startedAt);
  let response: Response;
  try {
    response = await work();
  } catch (error) {
    await store.abandon(lookup, startedAt).catch(() => undefined);
    throw error;
  }
  if (response.status >= 500) {
    await store.abandon(lookup, startedAt).catch(() => undefined);
    return response;
  }
  // The answer is the envelope or a problem: JSON, read whole.
  const body = await response.clone().text();
  // The work is done and may have committed: a store that fails to keep
  // its answer must not turn it into a 500, which tells the caller to
  // retry. The row stays running, so a repeat within the in-flight window
  // is 409 and one after it runs again, the window the docs name (Go's
  // step ignores the error too).
  await store.finish(lookup, startedAt, response.status, body).catch(() => undefined);
  return response;
}
