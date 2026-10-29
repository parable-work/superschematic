import { AsyncLocalStorage } from 'node:async_hooks';

/*
The caller of the operation being run (D87). A generated router runs each
operation's implementation within its caller, the principal its route
established, and an ORM opened with no user of its own (Deps.orm) acts as
that caller: createdBy, updatedBy and deletedBy record who called, with no
orm.as(ctx.principal) in the implementation. A job, a worker, a manual
route and anything else outside an operation runs within no caller.

The caller travels in AsyncLocalStorage, which Bun and Node.js have and a
Worker has under the nodejs_als compatibility flag, which every Worker
version of a server names (D64, amended).
*/

/** Who an operation runs for: a principal's subject, as ctx.principal holds one. */
export interface Caller {
  readonly subject: string;
}

const callers = new AsyncLocalStorage<string | undefined>();

/**
 * Runs fn within caller, the principal of the request it serves, or within
 * no caller for null: an ORM opened with no user acts as the caller in
 * everything fn runs, awaits and starts.
 */
export function withCaller<T>(caller: Caller | null | undefined, fn: () => T): T {
  return callers.run(caller?.subject, fn);
}

/** The subject of the caller of the operation being run, or undefined outside one. */
export function currentCaller(): string | undefined {
  return callers.getStore();
}
