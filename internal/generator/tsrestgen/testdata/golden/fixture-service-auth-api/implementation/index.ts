// The implementation of the fixture-service-auth-api API.
//
// superschematic wrote this package once, as a scaffold, because it was
// missing. It never writes it again: the package is yours. Each method
// answers 501 Not Implemented until you implement it.

import { notImplemented } from '@superschematic/http-runtime';
import type {
  AuthenticatorFactory,
  Constructor,
  Deps,
  LedgerImplementation,
  StockImplementation,
  SyncImplementation,
} from '@schemas/fixture-service-auth-api-api';

/** Builds the implementation of fixture-service-auth-api from its dependencies. Its type is the generated Constructor. */
export const create: Constructor = deps => ({
  ledger: ledgerImplementation(deps),
  stock: stockImplementation(deps),
  sync: syncImplementation(deps),
});

/**
 * Builds the authenticator that establishes the end user on the routes
 * that require one. The scaffold's establishes none, so each such route
 * answers 401: verify the request's credential (ctx.bearerToken, or a
 * header) with your identity provider and return its Principal.
 */
export const authenticate: AuthenticatorFactory = () => async () => null;

function ledgerImplementation(deps: Deps): LedgerImplementation {
  return {
    async listReservations() {
      throw notImplemented('ledger.listReservations');
    },
  };
}

function stockImplementation(deps: Deps): StockImplementation {
  return {
    async reindexStock() {
      throw notImplemented('stock.reindexStock');
    },
    async reserveStock() {
      throw notImplemented('stock.reserveStock');
    },
    async getReservation() {
      throw notImplemented('stock.getReservation');
    },
    async releaseReservation() {
      throw notImplemented('stock.releaseReservation');
    },
  };
}

function syncImplementation(deps: Deps): SyncImplementation {
  return {
    async syncStatus() {
      throw notImplemented('sync.syncStatus');
    },
    async syncStock() {
      throw notImplemented('sync.syncStock');
    },
    async syncMyStock() {
      throw notImplemented('sync.syncMyStock');
    },
  };
}
