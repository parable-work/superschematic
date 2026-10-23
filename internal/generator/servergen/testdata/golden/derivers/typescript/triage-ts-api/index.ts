// The implementation of the triage-ts-api API.
//
// superschematic wrote this package once, as a scaffold, because it was
// missing. It never writes it again: the package is yours. Each method
// answers 501 Not Implemented until you implement it.
// Each deriver fails every field until you implement it.

import { notImplemented } from '@superschematic/http-runtime';
import type {
  Constructor,
  Deps,
  DeriversConstructor,
  TriageImplementation,
} from '@schemas/triage-ts-api-api';

/** Builds the implementation of triage-ts-api from its dependencies. Its type is the generated Constructor. */
export const create: Constructor = deps => ({
  triage: triageImplementation(deps),
});

/**
 * Builds the derivers of triage-ts-api from its dependencies. Its type is the
 * generated DeriversConstructor. A deriver's entrypoint calls the method of
 * each stale derived field, which asks a model; a value settles the field,
 * and a failure or a rejection counts an attempt.
 */
export const createDerivers: DeriversConstructor = deps => ({
  fillAll: {
    async ticketCategory(item) {
      throw notImplemented('deriver FillAll, Ticket.category');
    },
    async ticketSummary(item) {
      throw notImplemented('deriver FillAll, Ticket.summary');
    },
  },
  triageTickets: {
    async ticketCategory(item) {
      throw notImplemented('deriver TriageTickets, Ticket.category');
    },
    async ticketSummary(item) {
      throw notImplemented('deriver TriageTickets, Ticket.summary');
    },
  },
});

function triageImplementation(deps: Deps): TriageImplementation {
  return {
    async summary() {
      throw notImplemented('triage.summary');
    },
  };
}
