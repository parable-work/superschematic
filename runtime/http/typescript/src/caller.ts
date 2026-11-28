import { AsyncLocalStorage } from 'node:async_hooks';
import type { ActingFor, GrantReach, PrincipalKind } from './auth.js';

/*
The caller of the operation being run (D87). A generated router runs each
operation's implementation within its caller, the principal its route
established, and an ORM opened with no user of its own (Deps.orm) acts as
that caller: createdBy, updatedBy and deletedBy record who called, with no
orm.as(ctx.principal) in the implementation. A job, a worker, a manual
route and anything else outside an operation runs within no caller.

The caller is also whom the reads of the operation keep to (D156): an
@audience table's rows the caller is in the audience of, and for a party
or an agent run, the rows its grants reach. So the store holds the whole
principal as access needs it (currentAccess), and an operation with no
principal, a public route's, runs within an anonymous caller, who sees only
the rows everyone may: that is not the same as running outside any
operation, as a job does, which sees every row.

The caller travels in AsyncLocalStorage, which Bun and Node.js have and a
Worker has under the nodejs_als compatibility flag, which every Worker
version of a server names (D64, amended).
*/

/** Who an operation runs for: a principal, as ctx.principal holds one. */
export interface Caller {
  readonly subject: string;
  readonly kind?: PrincipalKind;
  readonly actingFor?: ActingFor;
  readonly grants?: readonly GrantReach[];
  readonly teams?: readonly string[];
}

/**
 * The caller of the operation being run, as the reads hold it (D156): its
 * subject, its kind and whom it acts for, and the grants it calls through,
 * or no subject for an operation with no principal.
 */
export interface AccessCaller {
  readonly subject?: string;
  readonly kind: PrincipalKind;
  readonly actingFor?: ActingFor;
  readonly grants?: readonly GrantReach[];
  /** The teams the caller belongs to, by their ids (#735): an audience that names one admits it. */
  readonly teams?: readonly string[];
}

const callers = new AsyncLocalStorage<AccessCaller>();

/** The anonymous caller: an operation with no principal. */
const ANONYMOUS: AccessCaller = Object.freeze({ kind: 'user' });

/** The access caller of a principal, or the anonymous one for none. */
export function accessCallerOf(caller: Caller | null | undefined): AccessCaller {
  if (!caller) return ANONYMOUS;
  return {
    subject: caller.subject,
    kind: caller.kind ?? 'user',
    ...(caller.actingFor ? { actingFor: caller.actingFor } : {}),
    ...(caller.grants ? { grants: caller.grants } : {}),
    ...(caller.teams && caller.teams.length > 0 ? { teams: caller.teams } : {}),
  };
}

/**
 * Runs fn within caller, the principal of the request it serves, or within
 * the anonymous caller for null: an ORM opened with no user acts as the
 * caller in everything fn runs, awaits and starts, and its reads keep to
 * what the caller may see.
 */
export function withCaller<T>(caller: Caller | null | undefined, fn: () => T): T {
  return callers.run(accessCallerOf(caller), fn);
}

/** The subject of the caller of the operation being run, or undefined outside one or for an anonymous one. */
export function currentCaller(): string | undefined {
  return callers.getStore()?.subject;
}

/** The caller of the operation being run as the reads hold it (D156), or undefined outside any operation. */
export function currentAccess(): AccessCaller | undefined {
  return callers.getStore();
}
