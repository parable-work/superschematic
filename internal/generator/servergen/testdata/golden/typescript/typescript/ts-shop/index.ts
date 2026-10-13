// The implementation of the ts-shop API.
//
// superschematic wrote this package once, as a scaffold, because it was
// missing. It never writes it again: the package is yours. Each method
// answers 501 Not Implemented until you implement it.
// Each job fails until you implement it.
// Each worker fails every message until you implement it.

import { notImplemented } from '@superschematic/http-runtime';
import type {
  AuthenticatorFactory,
  Constructor,
  Deps,
  JobsConstructor,
  WorkersConstructor,
  CartsImplementation,
} from '@schemas/ts-shop-api';

/** Builds the implementation of ts-shop from its dependencies. Its type is the generated Constructor. */
export const create: Constructor = deps => ({
  carts: cartsImplementation(deps),
});

/**
 * Builds the authenticator that establishes the end user on the routes
 * that require one. The scaffold's establishes none, so each such route
 * answers 401: verify the request's credential (ctx.bearerToken, or a
 * header) with your identity provider and return its Principal.
 */
export const authenticate: AuthenticatorFactory = () => async () => null;

/**
 * Builds the jobs of ts-shop from its dependencies. Its type is the
 * generated JobsConstructor. A job's entrypoint calls one method per run,
 * with a signal that SIGTERM aborts; the run fails when it rejects.
 */
export const createJobs: JobsConstructor = deps => ({
  async sweepCarts() {
    throw notImplemented('job SweepCarts');
  },
});

/**
 * Builds the workers of ts-shop from its dependencies. Its type is the
 * generated WorkersConstructor. A worker's entrypoint calls one method per
 * message it claims; the message is done when it resolves, and fails, to be
 * retried, when it rejects. A message may come again, so a method is
 * idempotent.
 */
export const createWorkers: WorkersConstructor = deps => ({
  async remindShoppers(message) {
    throw notImplemented('worker RemindShoppers');
  },
});

function cartsImplementation(deps: Deps): CartsImplementation {
  return {
    async getCart() {
      throw notImplemented('carts.getCart');
    },
    async getMyCart() {
      throw notImplemented('carts.getMyCart');
    },
  };
}
