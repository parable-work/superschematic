// The implementation of the fixture-api API.
//
// superschematic wrote this package once, as a scaffold, because it was
// missing. It never writes it again: the package is yours. Each method
// answers 501 Not Implemented until you implement it.

import { notImplemented } from '@superschematic/http-runtime';
import type {
  AuthenticatorFactory,
  Constructor,
  Deps,
  SessionImplementation,
  TenantImplementation,
} from '@schemas/fixture-api-api';

/** Builds the implementation of fixture-api from its dependencies. Its type is the generated Constructor. */
export const create: Constructor = deps => ({
  session: sessionImplementation(deps),
  tenant: tenantImplementation(deps),
});

/**
 * Builds the authenticator that establishes the end user on the routes
 * that require one. The scaffold's establishes none, so each such route
 * answers 401: verify the request's credential (ctx.bearerToken, or a
 * header) with your identity provider and return its Principal.
 */
export const authenticate: AuthenticatorFactory = () => async () => null;

function sessionImplementation(deps: Deps): SessionImplementation {
  return {
    async currentTenant() {
      throw notImplemented('session.currentTenant');
    },
  };
}

function tenantImplementation(deps: Deps): TenantImplementation {
  return {
    async listTenants() {
      throw notImplemented('tenant.listTenants');
    },
    async createTenant() {
      throw notImplemented('tenant.createTenant');
    },
    async getTenant() {
      throw notImplemented('tenant.getTenant');
    },
    async updateSecret() {
      throw notImplemented('tenant.updateSecret');
    },
  };
}
