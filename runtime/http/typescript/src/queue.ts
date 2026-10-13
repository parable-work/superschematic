import pg from 'pg';

/*
The queue client of a TypeScript API (D53, section 8.8 of
docs/stack-model.md), the TypeScript twin of the Go ORM's queues.go. There
is no TypeScript ORM, so a DB service's TypeScript types package carries
each of its queues as data, in its ./queues entry: the table, the
arguments, the column each field of the message writes, and the statements
on Postgres, which the generator writes from the one source the Go ORM's
come from. This module runs them over pg, and is part of the package's
./postgres entry, so the main entry never imports pg.

enqueue writes a message in the caller's transaction, so it commits with
the writes that caused it, or not at all. postgresQueue claims, completes,
fails, extends and releases messages for a worker: a claim takes up to n
messages that are ready and due, or claimed past their claim's expiry,
with FOR UPDATE SKIP LOCKED, gives each a new claim token and one more
attempt, and first marks dead each message whose last attempt's claim
expired. Each change but the claim refuses a token that no longer claims
the message with ClaimLostError. Delivery is at least once, and times are
the database's clock.

A claimed row's values reach the message as the TypeScript types hold
them: a timestamp a Date, a date its YYYY-MM-DD text, a 64-bit integer a
number, a JSON column the value it holds, and a list an array.
*/

/** A field of a queue's message and the column it writes, as a types package's ./queues entry declares it. */
export interface QueueField {
  /** The message's member. */
  readonly name: string;
  /** Its column, quoted for SQL where it must be. */
  readonly column: string;
  /** The column holds the value as JSON, which an enqueue writes with JSON.stringify. */
  readonly json?: boolean;
  /** The column holds a list as a Postgres array. */
  readonly list?: boolean;
  /** The schema's default of an enum, which a message that leaves the member unset writes. */
  readonly default?: string;
}

/**
 * A queue's statements on Postgres: insert begins an enqueue; expire marks
 * dead each message whose last attempt's claim expired ($1 the retries);
 * claim claims up to $1 due messages for $2 seconds; complete, fail, extend
 * and release change message $1 while token $2 claims it.
 */
export interface QueueStatements {
  readonly insert: string;
  readonly expire: string;
  readonly claim: string;
  readonly complete: string;
  readonly fail: string;
  readonly extend: string;
  readonly release: string;
}

/** A queue of a database, whose messages are M, as its types package's ./queues entry exports it. */
export interface QueueDefinition<M> {
  /** The @queue class. */
  readonly name: string;
  /** Its table, quoted for SQL where it must be. */
  readonly table: string;
  /** How many times a message whose handler fails is handled again before it is dead. */
  readonly retries: number;
  /** How long a failed message waits before it is due again. */
  readonly backoffSeconds: number;
  /** How long a claim holds a message unless its worker extends it. */
  readonly leaseSeconds: number;
  /** The message's fields, in the order a claim returns their columns. */
  readonly fields: readonly QueueField[];
  readonly statements: QueueStatements;
  /** The type of the queue's messages, which no definition sets. */
  readonly message?: M;
}

/** The query config the queue client passes pg. */
export interface QueueQueryConfig {
  readonly text: string;
  readonly values: unknown[];
  readonly rowMode?: 'array';
  readonly types?: { getTypeParser(oid: number, format?: 'text' | 'binary'): unknown };
}

/**
 * What the queue client needs of a pg Pool, PoolClient or Client: an
 * enqueue takes the client of the caller's transaction, and the claims a
 * pool.
 */
export interface QueueQueryable {
  query(config: QueueQueryConfig): Promise<{ rows: unknown[]; rowCount: number | null }>;
}

/**
 * The error complete, fail, extend and release return when the token no
 * longer claims the message: its claim expired, and another worker may
 * hold it.
 */
export class ClaimLostError extends Error {
  constructor(message = 'the claim on the message is lost: it expired, and another worker may hold the message') {
    super(message);
    this.name = 'ClaimLostError';
  }
}

/** A claimed message: its ID, the token of the claim, which attempt this is, from 1, and the message. */
export interface QueueClaim<M> {
  readonly id: string;
  readonly token: string;
  readonly attempt: number;
  readonly message: M;
}

/** The claims of a queue over a pool, which a worker makes. */
export interface PostgresQueue<M> {
  /** The @queue class. */
  readonly name: string;
  /** How long a claim holds a message unless extended: the queue's lease. */
  readonly leaseSeconds: number;
  /** Claims up to n due messages for leaseSeconds, the queue's lease unless given. */
  claim(n: number, leaseSeconds?: number): Promise<QueueClaim<M>[]>;
  /** Marks the message id, which token claims, done. */
  complete(id: string, token: string): Promise<void>;
  /**
   * Records cause on the message id, which token claims, and makes it due
   * again after the backoff, or dead when its retries are spent. Resolves
   * to whether the message is dead.
   */
  fail(id: string, token: string, cause: string): Promise<boolean>;
  /** Holds the message id, which token claims, for leaseSeconds from now, the queue's lease unless given. */
  extend(id: string, token: string, leaseSeconds?: number): Promise<void>;
  /** Gives the message id, which token claims, back, due at once, its attempt not counted. */
  release(id: string, token: string): Promise<void>;
}

/** What a message's member is, unset: absent or null. An enum with a default is also unset when empty, as in Go. */
function unset(value: unknown, field: QueueField): boolean {
  return value === undefined || value === null || (field.default !== undefined && value === '');
}

/**
 * Writes message into queue in the transaction client runs, the caller's:
 * the message is due once the transaction commits, and never if it rolls
 * back. Each member the message sets writes its column, a JSON column its
 * JSON text; one it leaves unset takes the column's default, and an enum
 * with a schema default writes that default, as the Go ORM writes them.
 */
export async function enqueue<M>(client: QueueQueryable, queue: QueueDefinition<M>, message: M): Promise<void> {
  const columns: string[] = [];
  const values: unknown[] = [];
  const members = message as unknown as Readonly<Record<string, unknown>>;
  for (const field of queue.fields) {
    const value = members[field.name];
    if (unset(value, field)) {
      if (field.default === undefined) continue;
      columns.push(field.column);
      values.push(field.default);
      continue;
    }
    columns.push(field.column);
    values.push(field.json ? JSON.stringify(value) : value);
  }
  const text =
    columns.length === 0
      ? `${queue.statements.insert} DEFAULT VALUES`
      : `${queue.statements.insert} (${columns.join(', ')}) VALUES (${columns.map((_, i) => `$${i + 1}`).join(', ')})`;
  try {
    await client.query({ text, values });
  } catch (error) {
    throw new Error(`enqueue on ${queue.table}: ${errorMessage(error)}`, { cause: error });
  }
}

/** The claims of queue over pool, for a worker. */
export function postgresQueue<M>(pool: QueueQueryable, queue: QueueDefinition<M>): PostgresQueue<M> {
  const update = async (what: string, statement: string, id: string, token: string, extra: unknown[] = []): Promise<void> => {
    let result: { rowCount: number | null };
    try {
      result = await pool.query({ text: statement, values: [id, token, ...extra] });
    } catch (error) {
      throw new Error(`${what} message ${id} on ${queue.table}: ${errorMessage(error)}`, { cause: error });
    }
    if (!result.rowCount) throw new ClaimLostError(`${what} message ${id} on ${queue.table}: ${new ClaimLostError().message}`);
  };
  return {
    name: queue.name,
    leaseSeconds: queue.leaseSeconds,
    async claim(n, leaseSeconds = queue.leaseSeconds) {
      if (n <= 0) return [];
      let rows: unknown[];
      try {
        await pool.query({ text: queue.statements.expire, values: [queue.retries] });
        ({ rows } = await pool.query({ text: queue.statements.claim, values: [n, leaseSeconds], rowMode: 'array', types: queueTypes }));
      } catch (error) {
        throw new Error(`claim on ${queue.table}: ${errorMessage(error)}`, { cause: error });
      }
      return rows.map(row => claimOf(queue, row as readonly unknown[]));
    },
    complete: (id, token) => update('complete', queue.statements.complete, id, token),
    async fail(id, token, cause) {
      let rows: unknown[];
      try {
        ({ rows } = await pool.query({ text: queue.statements.fail, values: [id, token, queue.retries, queue.backoffSeconds, cause] }));
      } catch (error) {
        throw new Error(`fail message ${id} on ${queue.table}: ${errorMessage(error)}`, { cause: error });
      }
      const row = rows[0] as { state?: unknown } | undefined;
      if (row === undefined) throw new ClaimLostError(`fail message ${id} on ${queue.table}: ${new ClaimLostError().message}`);
      return row.state === 'dead';
    },
    extend: (id, token, leaseSeconds = queue.leaseSeconds) => update('extend', queue.statements.extend, id, token, [leaseSeconds]),
    release: (id, token) => update('release', queue.statements.release, id, token),
  };
}

/** A claimed row, [id, token, attempt, ...the message's columns], as its claim. */
function claimOf<M>(queue: QueueDefinition<M>, row: readonly unknown[]): QueueClaim<M> {
  const [id, token, attempt] = row;
  const message: Record<string, unknown> = {};
  queue.fields.forEach((field, i) => {
    let value = row[3 + i];
    if (value === null || value === undefined) return;
    if (field.json && typeof value === 'string') {
      try {
        value = JSON.parse(value);
      } catch (error) {
        throw new Error(`message ${String(id)} of ${queue.table}: field ${field.name} holds no JSON: ${errorMessage(error)}`, { cause: error });
      }
    } else if (field.list && typeof value === 'string') {
      // A list of a type pg does not know, such as citext[], arrives as its text.
      value = parseArray(value);
    }
    message[field.name] = value;
  });
  return { id: String(id), token: String(token), attempt: Number(attempt), message: message as M };
}

/**
 * The type parsers of a claim: pg's, but for the types whose TypeScript
 * type is not what pg makes of them. A 64-bit integer is a number, as the
 * TypeScript types hold Generic.Int64 and every integer scalar, and a date
 * its YYYY-MM-DD text, Temporal.Date's, where pg would make a Date at local
 * midnight.
 */
const overrides: Readonly<Record<number, (value: string) => unknown>> = {
  20: value => Number(value),
  1016: value => parseArray(value).map(element => (element === null ? null : Number(element))),
  1082: value => value,
  1182: value => parseArray(value),
};

const queueTypes = {
  getTypeParser(oid: number, format?: 'text' | 'binary'): unknown {
    const override = (format ?? 'text') === 'text' ? overrides[oid] : undefined;
    return override ?? pg.types.getTypeParser(oid, format);
  },
};

/**
 * Parses a one-dimensional Postgres array literal, `{a,"b c",NULL}`, into
 * its elements as text, NULL as null.
 */
export function parseArray(text: string): (string | null)[] {
  if (!text.startsWith('{') || !text.endsWith('}')) throw new Error(`${JSON.stringify(text)} is no Postgres array`);
  const out: (string | null)[] = [];
  const body = text.slice(1, -1);
  if (body === '') return out;
  let i = 0;
  while (i <= body.length) {
    if (body[i] === '"') {
      let element = '';
      i++;
      while (i < body.length && body[i] !== '"') {
        if (body[i] === '\\') i++;
        element += body[i] ?? '';
        i++;
      }
      out.push(element);
      i += 2; // the closing quote and the comma
      continue;
    }
    let end = body.indexOf(',', i);
    if (end < 0) end = body.length;
    const element = body.slice(i, end);
    out.push(element === 'NULL' ? null : element);
    i = end + 1;
  }
  return out;
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
