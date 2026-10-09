import type { Authenticator, Principal } from '../auth.js';
import type { RequestContext } from '../operation.js';
import { requestHost } from './crossorigin.js';
import type { IdentityPrincipal, IdentityService } from './service.js';
import { extractCredential } from './token.js';

/*
The identity service as the router's Authenticator (D50): the principal's
subject is the user's id, its permissions are the user's roles', and its
claims carry the session id, the login, the name, the transport and the
roles, so @requirePermission, the permission matcher and every
implementation that reads ctx.principal work unchanged. It reads the
Authorization header itself (readsAuthorization), so a malformed one is 401
unauthorized, as in the Go runtime, rather than hono/bearer-auth's 400.
*/

const identityPrincipals = new WeakMap<Principal, IdentityPrincipal>();

/** The runtime's Principal for an identity principal. */
export function principalOf(p: IdentityPrincipal): Principal {
  const principal: Principal = {
    subject: p.id,
    permissions: [...p.permissions],
    claims: {
      sessionId: p.sessionId,
      login: p.login,
      name: p.name,
      transport: p.transport,
      roles: p.roles.map(role => ({ id: role.id, name: role.name, permissions: [...role.permissions] })),
    },
  };
  identityPrincipals.set(principal, p);
  return principal;
}

/** The identity principal an identityAuthenticator established, or undefined for any other principal. */
export function identityPrincipalOf(principal: Principal | null | undefined): IdentityPrincipal | undefined {
  return principal ? identityPrincipals.get(principal) : undefined;
}

/**
 * The router's Authenticator over the identity service. A request with no
 * credential has no principal (null), so the route's gate answers 401, and
 * a service the engine lets stand in for an end user still can; any other
 * refusal is the service's problem: 401 unauthorized (clearing a refused
 * cookie) or 403 cross_origin.
 */
export function identityAuthenticator(service: IdentityService): Authenticator {
  const authenticate = async (ctx: RequestContext): Promise<Principal | null> => {
    if (extractCredential(ctx.headers, service.cookieName).outcome === 'none') return null;
    return principalOf(await service.authenticate({ method: ctx.method, host: requestHost(ctx.raw), headers: ctx.headers }));
  };
  return Object.assign(authenticate, { readsAuthorization: true });
}
