import { Identity } from "superscalar";
import { AutoGenerate, Mailbox, key } from "@superschematic/db";

// A message an agent takes in turn (D104, D103's step 5): a Mailbox whose
// taker holds the message's lease for thirty seconds at a time, a take
// needing messages.take. Its sender, whom createdBy holds, makes it urgent,
// puts it back and cancels it while it waits.
export abstract class Message
  implements
    Mailbox<Message, { ttlMs: 30000; takePermission: "messages.take" }>
{
  @key
  id: AutoGenerate<Identity.UUID>;
  body: string;
  createdBy: Identity.UUID;
}
