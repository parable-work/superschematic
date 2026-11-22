import { auth, behaviorOperations } from "@superschematic/api";
import { Ticket } from "@schemas/fixture-comments-db";

// A ticket's conversation for any signed-in caller (D103's step 4): each
// of Comments' operations a table runs is a route of its own
// (ticket/{id}/comments/post, ticket/{id}/comments/list-messages, ...);
// the ones it leaves out, and those of the namespace, are left out with a
// warning each.
@behaviorOperations(Ticket)
@auth
export abstract class TicketComments {}
