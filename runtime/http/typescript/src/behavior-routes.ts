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
own permissions against (a Workflow transition's), hands the repository
the input with each optional member that is null left out, and the
preconditions it presents (its preconditions member, D104) beside the
caller rather than among the parameters, runs the repository's operation,
and answers a refusal of the core's as the problem of its status, code,
message and details, as the Go and Rust servers answer it.

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

/**
 * A behavior operation's input as the route hands it to the repository: its
 * wire values with each of its optional members that is null left out
 * (D104), and each member omit names left out whatever its value. The
 * API's types take null for an optional member as its absence, as the Go
 * ORM's typed parameters omit one that is none, while a behavior's
 * paramsSchema types an optional parameter without null (Workflow's from,
 * the state the caller read), so the core would refuse a null one as
 * invalid_argument. optional names the input's optional members, by their
 * wire names; omit the members that are no parameter of the operation
 * (preconditions, which the handler presents apart, behaviorPreconditions);
 * any other member, and an input that is no object, is handed on as it is.
 */
export function behaviorParams(input: unknown, optional: readonly string[], omit: readonly string[] = []): unknown {
  if (input === null || typeof input !== 'object' || Array.isArray(input)) return input;
  const members = input as Readonly<Record<string, unknown>>;
  if (!optional.some(name => Object.hasOwn(members, name) && members[name] === null) && !omit.some(name => Object.hasOwn(members, name))) return input;
  const out: Record<string, unknown> = {};
  for (const [name, value] of Object.entries(members)) {
    if (omit.includes(name) || (value === null && optional.includes(name))) continue;
    out[name] = value;
  }
  return out;
}

/**
 * The preconditions a behavior operation's input presents (D16, D104): its
 * preconditions member, each use's entry by its use id ({ Lease: { token } }),
 * with each entry that is null left out, as the API's types take null for
 * an absent member; undefined for an input without one, or with none but
 * null entries. The handler passes them to the repository's operation
 * beside its caller, never among the operation's parameters, so the core
 * checks them before it reads the row and hands each its use's guard.
 */
export function behaviorPreconditions(input: unknown): Readonly<Record<string, unknown>> | undefined {
  if (input === null || typeof input !== 'object' || Array.isArray(input)) return undefined;
  const given = (input as Readonly<Record<string, unknown>>).preconditions;
  if (given === null || given === undefined || typeof given !== 'object' || Array.isArray(given)) return undefined;
  const out: Record<string, unknown> = {};
  for (const [id, entry] of Object.entries(given as Readonly<Record<string, unknown>>)) {
    if (entry !== null && entry !== undefined) out[id] = entry;
  }
  return Object.keys(out).length > 0 ? out : undefined;
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
