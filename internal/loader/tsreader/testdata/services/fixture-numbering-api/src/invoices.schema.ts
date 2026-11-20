import { auth, behaviorOperations } from "@superschematic/api";
import { Invoice, Ticket } from "@schemas/fixture-numbering-db";

// An invoice's backfill for any signed-in caller (D103's step 4):
// assignNumber at invoice/{id}/number/assign-number numbers an invoice made
// before the table numbered, and answers the number of one numbered
// already.
@behaviorOperations(Invoice)
@auth
export abstract class InvoiceActions {}

// A ticket's backfill of its number and its reference, each a route of its
// own (ticket/{id}/number/assign-number, ticket/{id}/reference/assign-number).
@behaviorOperations(Ticket)
@auth
export abstract class TicketActions {}
