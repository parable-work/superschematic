import { Identity } from "superscalar";
import { HttpMethod, auth, rest, rowFeatureOperations } from "@superschematic/api";
import { Note, Ticket } from "@schemas/fixture-references-db";

// A ticket's references, backlinks and files as routes (#723): every
// operation the table offers, each for a signed-in caller, the files in
// the API's one bucket.
@rowFeatureOperations(Ticket)
@auth
export abstract class TicketActions {}

// A note's: its references and backlinks, and its files but their
// removal, an upload needing notes.upload in the place of @auth's
// any caller.
@rowFeatureOperations(Note, {
  operations: ["references", "addReference", "removeReference", "backlinks", "attachments", "beginUpload", "completeUpload", "download", "fileAttachment"],
  permissions: { beginUpload: ["notes.upload"] }
})
@auth
export abstract class NoteActions {}

// A ticket as its table holds it.
export class TicketQueries {
  @auth
  @rest(HttpMethod.GET, "tickets/{id}")
  getTicket(id: Identity.UUID): Ticket {
    throw new Error("schema declaration only");
  }
}
