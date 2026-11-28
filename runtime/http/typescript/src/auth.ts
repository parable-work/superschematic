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
  /** Stable subject identifier: a user id, an API key's owner, a party's or an agent run's id. A calling service is ctx.serviceCaller, not a principal (D37). */
  readonly subject: string;
  readonly permissions: readonly string[];
  /** Verified claims for the implementation's own checks; never echoed. */
  readonly claims?: Readonly<Record<string, unknown>>;
  /**
   * What the principal is (D156): a user, signed in by a session or an API
   * key; a party, a row of an app table that holds a grant; an agent run;
   * or a runtime provider calling as itself (D159). Absent reads as user,
   * every authenticator's before D156.
   */
  readonly kind?: PrincipalKind;
  /**
   * Whom the principal acts for (D156): the user whose reply an agent run
   * interprets, the party it answers. The audit records both, and an
   * audience admits a row an agent run's principal or whom it acts for
   * may see. Absent for a principal that acts for itself.
   */
  readonly actingFor?: ActingFor;
  /**
   * The grants a party or an agent run calls through (D156): each one's
   * target and the relation paths its role reaches from it. A principal
   * with grants reaches, of a table, only the rows one of them reaches.
   * Absent for a user, who reaches rows through roles and row rules.
   */
  readonly grants?: readonly GrantReach[];
  /**
   * The credential the call came with, for the access log (D114, D156): an
   * API key's, a grant's or an OAuth grant's id (#695); absent for a
   * session.
   */
  readonly credential?: { readonly kind: 'apiKey' | 'grant' | 'oauth'; readonly id: string };
  /**
   * How the authenticator verified the principal for this call (D200),
   * which an Approvals requirement's proof reads: when its session last
   * signed in, and whether the call carried a passkey assertion with user
   * verification bound to it. Absent proves nothing.
   */
  readonly assurance?: Assurance;
}

/** How an authenticator verified a principal for one call (D200). */
export interface Assurance {
  /** When the principal's session last signed in, in epoch milliseconds. */
  readonly signedInAt?: number;
  /** Whether the call carried a passkey assertion with user verification, over a challenge bound to it. */
  readonly userVerified?: boolean;
}

/**
 * The kinds of principal (D156): a user, a party, an agent run, or a
 * runtime provider (D159), which acts as itself with the narrow role its
 * package declares (extensionPrincipal in ./providers).
 */
export type PrincipalKind = 'user' | 'party' | 'agent' | 'extension';

/**
 * Whom a principal acts for (D156): its kind and its subject. The engine's
 * principal names the subject alone (D77), and passes as this one, so an
 * absent kind reads as user, as the principal's own does.
 */
export interface ActingFor {
  readonly kind?: PrincipalKind;
  readonly subject: string;
}

/**
 * What one grant reaches (D156): its target row, by its table's type and
 * its key, and the relation paths its role reaches other rows by, by
 * table: { Request: ['unit.contract'] } reaches a request whose unit's
 * contract is the target. The target's own table reaches the target.
 */
export interface GrantReach {
  readonly id: string;
  readonly targetType: string;
  readonly targetId: string;
  readonly reaches: Readonly<Record<string, readonly string[]>>;
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

/**
 * The authenticator of a route the router does not mount (D152, D153,
 * D155): authenticate, called for a request alone, with the request's
 * headers, its method and path, and its Bearer token as the Hono adapter
 * reads it. A route that is no operation has no operation spec, path
 * captures or request id of the router's, and an authenticator that reads
 * them reads them empty.
 */
export function requestAuthenticator(authenticate: Authenticator): (request: Request) => Promise<Principal | null> {
  return request => {
    const url = new URL(request.url);
    const header = request.headers.get('authorization') ?? '';
    const bearer = header.startsWith('Bearer ') ? header.slice('Bearer '.length).trim() : '';
    const ctx = {
      requestId: request.headers.get('x-request-id') ?? '',
      method: request.method,
      path: url.pathname,
      headers: request.headers,
      raw: request,
      pathParams: {},
      query: url.searchParams,
      principal: null,
      signal: request.signal,
      ...(bearer !== '' && !authenticate.readsAuthorization ? { bearerToken: bearer } : {}),
    };
    return authenticate(ctx as unknown as RequestContext);
  };
}

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
