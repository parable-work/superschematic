import type { RequestContext } from './operation.js';
import { HttpProblem } from './problem.js';

/*
The handlers of a generated router's behavior operations (D103): the routes
the loader adds for an API's @behaviorOperations sets, which run each
operation of a behavior on a field of a table through the table's
repository in the database's orm.ts, with no implementation method. The
route runs every step an implemented route runs (the service step, the
end-user step and the permission check of the set's clause among them);
the handler then builds the caller the behaviors' core checks a behavior's
own permissions against (a Workflow transition's), runs the repository's
operation, and answers a refusal of the core's as the problem of its
status, code, message and details, as the Go and Rust servers answer it.

It imports nothing from pg, the repositories or the rules adapter, so the
main entry stays free of them: a refusal is recognized by its shape, the
adapter's RulesRefusal or a NotFoundError over one, whichever copy of the
runtime threw it.
*/

/**
 * Who a behavior operation runs as (D103), the shape of the rules adapter's
 * CoreCaller (./postgres), which a repository's operation takes as its
 * caller option: a subject and the permissions it holds, and whether it is
 * a calling service standing in for an end user, which the core then
 * answers no for every permission it checks.
 */
export interface BehaviorCaller {
  readonly subject: string;
  readonly permissions?: readonly string[];
  readonly standsIn?: boolean;
}

/**
 * The caller a behavior's command runs as (D103): the route's principal
 * with its permissions, each once; else a calling service standing in for
 * one (D37), by its deployable, holding none; else no one, an empty
 * subject, on a public route or one without a clause.
 */
export function behaviorCaller(ctx: RequestContext): BehaviorCaller {
  if (ctx.principal) {
    return { subject: ctx.principal.subject, permissions: [...new Set(ctx.principal.permissions)] };
  }
  if (ctx.serviceCaller) {
    return { subject: ctx.serviceCaller.deployable, standsIn: true };
  }
  return { subject: '' };
}

/** A refusal of the behaviors' core, as the rules adapter's CoreRefusal holds it. */
interface Refusal {
  readonly code: string;
  readonly status: number;
  readonly message: string;
  readonly details?: unknown;
}

function isRefusal(value: unknown): value is Refusal {
  if (value === null || typeof value !== 'object') return false;
  const refusal = value as Record<string, unknown>;
  return typeof refusal.code === 'string' && typeof refusal.status === 'number' && typeof refusal.message === 'string';
}

/**
 * The core's refusal error carries: its own refusal, as a RulesRefusal
 * holds it, or its cause's, as a repository's NotFoundError holds the
 * not_found refusal it stands for; undefined for any other error.
 */
function refusalOf(error: unknown): Refusal | undefined {
  for (let candidate = error, depth = 0; candidate !== null && typeof candidate === 'object' && depth < 2; depth++) {
    const refusal = (candidate as { refusal?: unknown }).refusal;
    if (isRefusal(refusal)) return refusal;
    candidate = (candidate as { cause?: unknown }).cause;
  }
  return undefined;
}

/**
 * Runs a generated route's behavior operation and resolves to its result.
 * The core's refusal, or the repository's NotFoundError over one, is thrown
 * as the HttpProblem of the refusal's status, code, message and details; a
 * refusal of status 500 (internal, behavior_error) is rethrown as an error
 * of the server's own, which the route answers as its internal error, with
 * none of the core's words, as it answers any error it did not expect. Any
 * other error is rethrown as it is.
 */
export async function behaviorOperation<T>(run: () => Promise<T>): Promise<T> {
  try {
    return await run();
  } catch (error) {
    const refusal = refusalOf(error);
    if (!refusal) throw error;
    if (refusal.status === 500 || !Number.isInteger(refusal.status) || refusal.status < 400 || refusal.status > 599) {
      throw new Error(`behavior operation failed: ${refusal.code}`, { cause: error });
    }
    throw new HttpProblem(refusal.status, refusal.message, {
      code: refusal.code,
      ...(refusal.details !== undefined ? { details: refusal.details } : {}),
      cause: error,
    });
  }
}
