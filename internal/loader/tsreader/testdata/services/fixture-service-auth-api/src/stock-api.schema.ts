import {
  Authenticated,
  HttpMethod,
  allowService,
  auth,
  publicRoute,
  requirePermission,
  requireService,
  rest
} from "@superschematic/api";
import { FixtureServiceCallerApi } from "@schemas/fixture-service-caller-api";

// A hold on stock for an order.
export abstract class Reservation {
  id: string;
  sku: string;
  held: boolean;
}

// The outcome of a stock run.
export abstract class StockRun {
  id: string;
  done: boolean;
}

export class StockMutations {
  // Only the caller API's server, placing an order for a user who may.
  @requirePermission(["stock.reserve"])
  @requireService({ from: [FixtureServiceCallerApi] })
  @rest(HttpMethod.POST, "stock/reservations")
  reserveStock(sku: string): Reservation {
    throw new Error("schema declaration only");
  }

  // A user who may, or the caller API's server on its own.
  @requirePermission(["stock.write"])
  @allowService({ from: [FixtureServiceCallerApi] })
  @rest(HttpMethod.POST, "stock/reservations/{id}/release")
  releaseReservation(id: string): Reservation {
    throw new Error("schema declaration only");
  }

  // Any server with an edge to this API, and no end user.
  @requireService()
  @rest(HttpMethod.POST, "stock/reindex")
  reindexStock(): StockRun {
    throw new Error("schema declaration only");
  }

  // An end user only, as before service clauses.
  @auth
  @rest(HttpMethod.GET, "stock/reservations/{id}")
  getReservation(id: string): Reservation {
    throw new Error("schema declaration only");
  }
}

// Every operation takes the set's clause unless it declares its own or is
// @publicRoute.
@requireService({ from: [FixtureServiceCallerApi] })
export class SyncOperations {
  // The set's: only the caller API's server.
  @rest(HttpMethod.POST, "sync/stock")
  syncStock(): StockRun {
    throw new Error("schema declaration only");
  }

  // Its own replaces the set's: a user, or any server with an edge.
  @auth
  @allowService()
  @rest(HttpMethod.POST, "sync/stock/mine")
  syncMyStock(): StockRun {
    throw new Error("schema declaration only");
  }

  // Open to anyone, in a set with a service clause.
  @publicRoute
  @rest(HttpMethod.GET, "sync/status")
  syncStatus(): StockRun {
    throw new Error("schema declaration only");
  }
}

// An Authenticated set is the user clause the set's @allowService needs.
@allowService({ from: [FixtureServiceCallerApi] })
export class LedgerQueries extends Authenticated {
  // A signed-in user, or the caller API's server with no end user.
  @rest(HttpMethod.GET, "ledger/reservations")
  listReservations(): Reservation[] {
    throw new Error("schema declaration only");
  }
}
