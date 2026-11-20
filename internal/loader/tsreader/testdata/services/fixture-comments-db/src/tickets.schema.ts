import { Identity } from "superscalar";
import { AutoGenerate, Comments, key } from "@superschematic/db";

// A ticket's conversation (D77, D103's step 4): messages and their
// replies, edits within 15 minutes of a post, reactions, pins and resolved
// threads, each write beside the row, and a caller holding tickets.moderate
// edits and deletes another's message and purges.
export abstract class Ticket implements Comments<Ticket, { moderatePermission: "tickets.moderate"; editWindowMs: 900000 }> {
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
}

// A review's conversation of refs and code blocks alone, its bodies at
// most 2000 code points, pinned and resolved by the permissions it names.
export abstract class Review implements Comments<
  Review,
  { blocks: ["ref", "code"]; maxBodyLength: 2000; pinPermission: "reviews.pin"; resolvePermission: "reviews.resolve" }
> {
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
}
