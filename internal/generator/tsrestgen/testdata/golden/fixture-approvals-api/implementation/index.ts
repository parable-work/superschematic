// The implementation of the fixture-approvals-api API.
//
// superschematic wrote this package once, as a scaffold, because it was
// missing. It never writes it again: the package is yours. Each method
// answers 501 Not Implemented until you implement it.

import { notImplemented } from '@superschematic/http-runtime';
import type {
  AuthenticatorFactory,
  Constructor,
  Deps,
  TaskAdminImplementation,
} from '@schemas/fixture-approvals-api-api';

/** Builds the implementation of fixture-approvals-api from its dependencies. Its type is the generated Constructor. */
export const create: Constructor = deps => ({
  taskAdmin: taskAdminImplementation(deps),
});

/**
 * Builds the authenticator that establishes the end user on the routes
 * that require one. The scaffold's establishes none, so each such route
 * answers 401: verify the request's credential (ctx.bearerToken, or a
 * header) with your identity provider and return its Principal.
 */
export const authenticate: AuthenticatorFactory = () => async () => null;

function taskAdminImplementation(deps: Deps): TaskAdminImplementation {
  return {
    async reassignTask() {
      throw notImplemented('taskAdmin.reassignTask');
    },
  };
}
