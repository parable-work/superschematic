// The implementation of the derived-ts-api API.
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
} from '@schemas/derived-ts-api-api';

/** Builds the implementation of derived-ts-api from its dependencies. Its type is the generated Constructor. */
export const create: Constructor = deps => ({
  triage: triageImplementation(deps),
});

/**
 * Builds the derivers of derived-ts-api from its dependencies. Its type is the
 * generated DeriversConstructor. A deriver's entrypoint calls the method of
 * each stale derived field, which asks a model; a value settles the field,
 * and a failure or a rejection counts an attempt.
 */
export const createDerivers: DeriversConstructor = deps => ({
  fillAll: {
    async articleSummary(item) {
      throw notImplemented('deriver FillAll, Article.summary');
    },
    async ticketCategory(item) {
      throw notImplemented('deriver FillAll, Ticket.category');
    },
    async ticketPriority(item) {
      throw notImplemented('deriver FillAll, Ticket.priority');
    },
  },
  triageTickets: {
    async ticketCategory(item) {
      throw notImplemented('deriver TriageTickets, Ticket.category');
    },
    async ticketPriority(item) {
      throw notImplemented('deriver TriageTickets, Ticket.priority');
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
