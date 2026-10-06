/*
Who calls the engine over HTTP and MCP: the end user the deployment's
Authenticator returns, the service the HTTP runtime's service step
verified (D37, `authenticateService`), or both.

The runtime runs the service step before the end-user step on every route
when the deployment passes a service authenticator, and puts the caller on
ctx.serviceCaller. The engine's routes declare no service clause, since
what a service may do is the access policy's to say, so the end-user step
still runs; the engine wraps the deployment's Authenticator so that a
verified service that brings no end user stands in for one, as D37's
`@allowService` admits it, rather than being refused 401.

- An end user and no service: the principal the Authenticator returns.
- A service acting for an end user: that end user's principal, with the
  service beside it (Principal.service, standsIn false). The end user is
  the one the forwarded Authorization names, and the Authenticator must
  accept it: a request that carries an Authorization header the
  Authenticator refuses is 401, whatever service sent it, so a service
  forwarding an expired token never acts with its own authority instead.
- A service and no Authorization header, which the Authenticator does not
  take for an end user: the service stands in (servicePrincipal), with the
  subject `service:<deployable>` and no permissions.
*/

import { unauthorized, type Authenticator, type RequestContext } from '@superschematic/http-runtime';

import { servicePrincipal, type Principal } from '../access.js';

// The principals callerAuthenticator made for a service standing in, so
// principalOf tells them from an end user's.
const standIns = new WeakSet<object>();

/**
 * callerAuthenticator wraps the deployment's Authenticator (none means no
 * end user authenticates): its principal when it returns one, else a
 * stand-in for the verified service caller when the request carries no
 * Authorization header, else none (401).
 */
export function callerAuthenticator(deployed: Authenticator | undefined): Authenticator {
  return async (ctx) => {
    const principal = deployed ? await deployed(ctx) : null;
    if (principal) {
      return principal;
    }
    const service = ctx.serviceCaller;
    if (service === null || (ctx.headers.get('authorization') ?? '').trim() !== '') {
      return null;
    }
    const standIn = { subject: servicePrincipal(service).subject, permissions: [] };
    standIns.add(standIn);
    return standIn;
  };
}

/**
 * principalOf is the engine's principal for the callers the runtime
 * established: the end user's, with the calling service beside it, or the
 * service's own when it stands in. The HTTP runtime's Principal has the
 * engine's shape; a copy keeps only its members. A principal without a
 * subject is no caller.
 */
export function principalOf(ctx: RequestContext): Principal {
  const caller = ctx.principal;
  if (!caller || typeof caller.subject !== 'string' || caller.subject === '') {
    throw unauthorized();
  }
  const service = ctx.serviceCaller;
  if (service !== null && standIns.has(caller)) {
    return servicePrincipal(service);
  }
  return {
    subject: caller.subject,
    permissions: caller.permissions,
    ...(caller.claims !== undefined ? { claims: caller.claims } : {}),
    ...(service !== null
      ? { service: { deployable: service.deployable, serves: [...service.serves], subject: service.subject, standsIn: false } }
      : {}),
  };
}
