import type { OperationAuth, RequestContext } from './operation.js';
import { forbidden, unauthorized } from './problem.js';

/*
The permission gate, with the semantics of the Go runtime's session
package: no principal on a route that needs one is 401 "Authentication
required"; a principal whose permissions cover none of the required ones is
403 "Insufficient permissions". Permissions are dotted paths. A granted
permission covers a required one when they are equal or the required one is
nested under it (`reports` covers `reports.export`; `report` does not).
There is no root permission that covers everything.

How a principal is established is the service's business: the router takes
an Authenticator. The Hono adapter extracts a Bearer token with
hono/bearer-auth and puts it on RequestContext.bearerToken; an authenticator
can read it, a header, or anything else on the request. A project with its
own permission vocabulary (a root permission, roles resolved elsewhere)
supplies a PermissionMatcher, the equivalent of the Go runtime's
RequirePermissionsWith.
*/

export interface Principal {
  /** Stable subject identifier: a user id, an API key's owner. A calling service is ctx.serviceCaller, not a principal (D37). */
  readonly subject: string;
  readonly permissions: readonly string[];
  /** Verified claims for the implementation's own checks; never echoed. */
  readonly claims?: Readonly<Record<string, unknown>>;
}

/**
 * Establishes the caller for a request that requires one; null when there is
 * none. One that sets `readsAuthorization` reads the Authorization header
 * itself and answers a malformed one with its own problem, so the Hono
 * adapter leaves the header to it: it does not run hono/bearer-auth, which
 * answers 400 to a malformed Bearer header, nor set ctx.bearerToken.
 */
export type Authenticator = ((ctx: RequestContext) => Promise<Principal | null>) & {
  readonly readsAuthorization?: boolean;
};

/** Reports whether `held` satisfies `required`; an empty `required` must be satisfied. */
export type PermissionMatcher = (held: readonly string[], required: readonly string[]) => boolean;

/** Whether a granted permission covers a required one: equal, or the required one nested under it. */
export function covers(granted: string, required: string): boolean {
  return granted === required || required.startsWith(`${granted}.`);
}

/** The default PermissionMatcher: true when `required` is empty or any held permission covers any required one. */
export function hasAnyPermission(held: readonly string[], required: readonly string[]): boolean {
  if (required.length === 0) return true;
  for (const need of required) {
    for (const have of held) {
      if (covers(have, need)) return true;
    }
  }
  return false;
}

/** Applies an operation's auth requirements to the established principal, throwing 401 or 403. */
export function authorize(principal: Principal | null, auth: OperationAuth, matches: PermissionMatcher = hasAnyPermission): void {
  if (auth.public || !auth.required) return;
  if (!principal) throw unauthorized();
  if (!matches(principal.permissions, auth.permissions)) throw forbidden();
}
