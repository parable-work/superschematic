// The implementation of the fixture-schedules-api API.
//
// superschematic wrote this package once, as a scaffold, because it was
// missing. It never writes it again: the package is yours. Each method
// answers 501 Not Implemented until you implement it.
// Each event handler fails every event until you implement it.

import { notImplemented } from '@superschematic/http-runtime';
import type {
  AuthenticatorFactory,
  Constructor,
  Deps,
  WorkersConstructor,
} from '@schemas/fixture-schedules-api-api';

/** Builds the implementation of fixture-schedules-api from its dependencies. Its type is the generated Constructor. */
export const create: Constructor = deps => ({
});

/**
 * Builds the authenticator that establishes the end user on the routes
 * that require one. The scaffold's establishes none, so each such route
 * answers 401: verify the request's credential (ctx.bearerToken, or a
 * header) with your identity provider and return its Principal.
 */
export const authenticate: AuthenticatorFactory = () => async () => null;

/**
 * Builds the workers of fixture-schedules-api from its dependencies. Its type is the
 * generated WorkersConstructor. A worker's entrypoint calls one method per
 * message it claims; the message is done when it resolves, and fails, to be
 * retried, when it rejects. A message may come again, so a method is
 * idempotent.
 */
export const createWorkers: WorkersConstructor = deps => ({
  async problemWakeUpsSchedulesRun(event, tx) {
    throw notImplemented('event handler problemWakeUpsSchedulesRun');
  },
});
