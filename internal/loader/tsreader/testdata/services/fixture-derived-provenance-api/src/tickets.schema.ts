import { auth, behaviorOperations } from "@superschematic/api";
import { Article, Ticket } from "@schemas/fixture-derived-provenance-db";

// A ticket's triage for any signed-in caller (D73, D103's step 4): a
// deriver pulls the stale fields at ticket/derived/stale-derivations and
// settles its answers at ticket/derived/settle-derivations, routes of the
// table as a whole; applyDerived and rederive are routes of the row under
// derived, and a reviewer's accept and reject routes of the row under
// provenance, beside the status Workflow's transition.
@behaviorOperations(Ticket)
@auth
export abstract class TicketTriage {}

// An article's summary, Derived's routes alone.
@behaviorOperations(Article)
@auth
export abstract class ArticleSummaries {}
