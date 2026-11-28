// The implementation of the fixture-references-api API.
//
// superschematic wrote this package once, as a scaffold, because it was
// missing. It never writes it again: the package is yours. Each method
// answers 501 Not Implemented until you implement it.

import { notImplemented } from '@superschematic/http-runtime';
import type {
  AuthenticatorFactory,
  Constructor,
  Deps,
  TicketImplementation,
} from '@schemas/fixture-references-api-api';

/** Builds the implementation of fixture-references-api from its dependencies. Its type is the generated Constructor. */
export const create: Constructor = deps => ({
  ticket: ticketImplementation(deps),
});

/**
 * Builds the authenticator that establishes the end user on the routes
 * that require one. The scaffold's establishes none, so each such route
 * answers 401: verify the request's credential (ctx.bearerToken, or a
 * header) with your identity provider and return its Principal.
 */
export const authenticate: AuthenticatorFactory = () => async () => null;

function ticketImplementation(deps: Deps): TicketImplementation {
  return {
    async getTicket() {
      throw notImplemented('ticket.getTicket');
    },
  };
}
