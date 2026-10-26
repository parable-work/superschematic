import { AsyncLocalStorage } from 'node:async_hooks';
import pg from 'pg';
import type { Pool, PoolConfig } from 'pg';
import { withTypeParsers } from './pgcodec.js';
import type { PostgresPoolSettings } from './postgres.js';
import type { HyperdriveBinding } from './workers.js';

/*
The database of a TypeScript server, job or worker on Cloudflare Workers
whose DB service lists postgres first (D92): a Postgres database outside
Cloudflare, reached through a Hyperdrive config, which keeps a pool of
connections to it near the database and hands each Worker a connection
string. The Worker's hyperdrive binding gives that string, and node-postgres
connects to it over the runtime's TCP sockets (cloudflare:sockets), which
pg selects itself on Workers, with Node's modules, which the Worker's
compatibility date or nodejs_compat gives.

A Worker may not use an I/O object, a socket among them, in a request other
than the one that opened it, and a server's Deps lives as long as its
isolate, across its requests (D64). So hyperdrivePool returns a pg Pool that
connects nothing itself: each scope withHyperdrive runs, a request or a Cron
Trigger's firing, opens a pool of its own when it first queries, and every
query, connect and transaction the scope makes on the Pool goes to that
pool, which the scope ends when it ends. The generated repositories,
transactions and queue client take it as they take any pg Pool. Hyperdrive
keeps its connections to the origin open between scopes, so a scope's
first connection goes no farther than Cloudflare's edge.

A query outside any scope is refused (HyperdriveScopeError): a Worker's
module scope runs no I/O, and a scope that ended has given its connections
back. A scope's pool holds at most DEFAULT_MAX_CONNECTIONS unless the
settings say otherwise, under the six connections a Worker's invocation
may hold open at once, which its fetches share. Its type parsers are
./postgres's (D79): a BIGINT a number, a NUMERIC its exact text, a DATE its
YYYY-MM-DD text.

This is the package's ./hyperdrive entry. It imports pg and
node:async_hooks, so a bundle that imports it runs with Node's modules, and
bundle.ts keeps Node's modules external for it (D92).
*/

export type { HyperdriveBinding } from './workers.js';

/** The connections a scope's pool holds at most unless its settings say otherwise: room under a Worker's six open connections for its fetches. */
export const DEFAULT_MAX_CONNECTIONS = 4;

export interface HyperdrivePoolOptions {
  /** Pool settings each scope's pool takes: max and the like. */
  readonly pool?: PostgresPoolSettings;
  /**
   * Called with the error of a scope's pooled connection that fails while
   * idle; the pool drops it. console.error by default.
   */
  readonly onIdleError?: (error: Error) => void;
  /** Whether each pool reads with ./postgres's type parsers (D79); true unless false. */
  readonly typeParsers?: boolean;
}

/** Thrown by a Hyperdrive pool's query or connect outside a scope withHyperdrive runs. */
export class HyperdriveScopeError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'HyperdriveScopeError';
  }
}

/** The pools of the scope that runs, one per Hyperdrive pool it has queried. */
const scopes = new AsyncLocalStorage<Map<HyperdrivePool, Pool>>();

/**
 * The pools whose scope has ended them: the runtime's socket reports its
 * own close to a connection that pg is ending, which is no failure.
 */
const ending = new WeakSet<Pool>();

/**
 * Runs run in a scope of its own: each Hyperdrive pool it queries opens a
 * pool for it, which every query, connect and transaction of the scope
 * shares, and which the scope ends when run settles. waitUntil, a Worker's
 * ctx.waitUntil, takes the ending, so the response does not wait for it;
 * without one the scope awaits it. A scope within a scope has pools of its
 * own.
 */
export async function withHyperdrive<T>(run: () => T | Promise<T>, waitUntil?: (promise: Promise<unknown>) => void): Promise<T> {
  const pools = new Map<HyperdrivePool, Pool>();
  try {
    return await scopes.run(pools, run);
  } finally {
    const ended = Promise.all(
      [...pools.values()].map(pool => {
        ending.add(pool);
        return pool.end().catch(() => undefined);
      })
    ).then(() => undefined);
    if (waitUntil) waitUntil(ended);
    else await ended;
  }
}

/** A pg Pool whose connections each scope opens and ends (hyperdrivePool). */
class HyperdrivePool extends pg.Pool {
  readonly #config: PoolConfig;
  readonly #onIdleError: (error: Error) => void;

  constructor(config: PoolConfig, onIdleError: (error: Error) => void) {
    // The base pool is never connected: each scope's pool connects.
    super(config);
    this.#config = config;
    this.#onIdleError = onIdleError;
  }

  /** The running scope's pool of this one, opened on the scope's first use. */
  #scoped(what: string): Pool {
    const pools = scopes.getStore();
    if (pools === undefined) {
      throw new HyperdriveScopeError(
        `a Hyperdrive pool's ${what} ran outside a request or a firing: a Worker opens its database connections within one (withHyperdrive), and may not use them in another (D92)`
      );
    }
    let pool = pools.get(this);
    if (pool === undefined) {
      const opened = new pg.Pool(this.#config);
      opened.on('error', error => {
        if (!ending.has(opened)) this.#onIdleError(error);
      });
      pools.set(this, opened);
      pool = opened;
    }
    return pool;
  }

  // pg's query and connect are overloaded, a promise or a callback; each
  // goes, as it was called, to the scope's pool.
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  override query(...args: any[]): any {
    const pool = this.#scoped('query');
    return (pool.query as (...a: unknown[]) => unknown).apply(pool, args);
  }

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  override connect(...args: any[]): any {
    const pool = this.#scoped('connect');
    return (pool.connect as (...a: unknown[]) => unknown).apply(pool, args);
  }
}

/**
 * A pg Pool over binding, a Worker's hyperdrive binding, for Deps.db and the
 * generated repositories: it connects only within a scope withHyperdrive
 * runs, a pool per scope, as the module says. Throws for a binding with no
 * connection string.
 */
export function hyperdrivePool(binding: HyperdriveBinding, options: HyperdrivePoolOptions = {}): Pool {
  if (typeof binding?.connectionString !== 'string' || binding.connectionString === '') {
    throw new Error('hyperdrivePool: the binding holds no connection string; a Worker takes it from its hyperdrive binding (D92)');
  }
  const config: PoolConfig = { max: DEFAULT_MAX_CONNECTIONS, ...(options.pool ?? {}), connectionString: binding.connectionString };
  if (options.typeParsers !== false) config.types = withTypeParsers(pg.types);
  const onIdleError = options.onIdleError ?? ((error: Error) => console.error(`hyperdrive: an idle connection failed: ${error.message}`));
  return new HyperdrivePool(config, onIdleError);
}
